// Command tool-executor 是官方工具执行器插件（provides: toolExecutor.v1）。
//
// 它解决一个具体问题：**模型不知道有哪些工具，也不知道参数长什么样。**
// 光有 readFile.v1 这种服务是不够的 —— LLM 需要一份带 JSON Schema 的工具清单，
// 否则 tools 参数根本填不出来，agent 就只能干聊天。
//
// 它做三件事：
//  1. 维护工具目录：出厂目录把已有服务桥接成模型可用的工具
//  2. list：把 name + description + schema 交给 agent，由 agent 喂给 LLM
//  3. execute：校验必填参数，转发到真正的服务，把结果或错误原样带回
//
// 它自己不碰文件、不碰网络，所以 manifest 的 permissions 是空的：
// 真正的权限声明在 readFile.v1 这类工具插件身上，核心的能力代理照常校验。
package main

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"aiwork/sdk/go/plugin"
)

const (
	serviceName = "toolExecutor.v1"
	configKey   = "registeredTools"
)

// Tool 是工具目录里的一项。
//
// Service / Method 说明这个工具落在哪个插件服务上；Builtin=true 表示不依赖任何插件。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Service     string          `json:"service,omitempty"`
	Method      string          `json:"method,omitempty"`
	Builtin     bool            `json:"builtin,omitempty"`
}

// toolView 是返回给调用方的工具视图：在定义之上补上「现在能不能用、谁提供的」。
type toolView struct {
	Tool
	Available bool   `json:"available"`
	Provider  string `json:"provider,omitempty"`
	Note      string `json:"note,omitempty"`
}

// toolNamePattern：工具名会变成 LLM 的 function name，只能是字母数字下划线。
var toolNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Executor 实现 plugin.Plugin。
type Executor struct {
	ctx *plugin.Context

	mu    sync.RWMutex
	extra map[string]Tool // register 进来的工具，持久化在插件配置里
}

// Activate 注册 toolExecutor.v1 并载入已注册的工具。
func (e *Executor) Activate(ctx *plugin.Context) error {
	e.ctx = ctx
	e.extra = map[string]Tool{}

	if err := ctx.Provide(serviceName, map[string]any{
		"description": "工具目录与执行器：list 给模型看，execute 真正跑",
		"methods":     []string{"list", "execute", "register", "unregister", "get"},
	}); err != nil {
		return err
	}

	if err := e.loadRegistered(); err != nil {
		ctx.Log("warn", "读取已注册工具失败（将只使用出厂目录）："+err.Error(), nil)
	}

	names := make([]string, 0, 8)
	for _, t := range e.all() {
		names = append(names, t.Name)
	}
	ctx.Log("info", "工具执行器已就绪", map[string]any{
		"tools": strings.Join(names, ","),
		"extra": len(e.extra),
	})
	return nil
}

// Handle 处理 core.invoke。
func (e *Executor) Handle(service, method string, args json.RawMessage) (any, error) {
	if service != serviceName {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "工具执行器不提供服务 %s", service)
	}
	switch method {
	case "list":
		return e.list(args)
	case "execute":
		return e.execute(args)
	case "register":
		return e.register(args)
	case "unregister":
		return e.unregister(args)
	case "get":
		return e.get(args)
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "toolExecutor.v1 不支持方法 %s", method)
	}
}

// Deactivate 清理。
func (e *Executor) Deactivate() error {
	if e.ctx != nil {
		e.ctx.Log("info", "工具执行器退出", nil)
	}
	return nil
}

// ---------------------------------------------------------------------------
// list：给模型看的工具清单
// ---------------------------------------------------------------------------

func (e *Executor) list(args json.RawMessage) (any, error) {
	var in struct {
		AvailableOnly bool `json:"availableOnly"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in)
	}

	out := make([]toolView, 0, 16)
	for _, t := range e.all() {
		view := e.view(t)
		if in.AvailableOnly && !view.Available {
			continue
		}
		out = append(out, view)
	}
	return map[string]any{
		"tools": out,
		"count": len(out),
		"total": len(e.all()),
	}, nil
}

func (e *Executor) get(args json.RawMessage) (any, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	t, ok := e.lookup(strings.TrimSpace(in.Name))
	if !ok {
		return nil, e.unknownTool(in.Name)
	}
	return e.view(t), nil
}

// view 计算一个工具的当前可用性。
//
// 工具插件没装时不报错、也不消失 —— 如实返回 available=false 和原因，
// 这样 /tool toolExecutor.v1 list 一眼就能看出「还差哪个插件」。
func (e *Executor) view(t Tool) toolView {
	if t.Builtin {
		return toolView{Tool: t, Available: true, Provider: e.ctx.PluginID, Note: "内置"}
	}
	pid, err := e.ctx.Consume(t.Service)
	if err != nil {
		return toolView{Tool: t, Note: reasonOf(err)}
	}
	return toolView{Tool: t, Available: true, Provider: pid}
}

// ---------------------------------------------------------------------------
// execute：真正跑一次工具
// ---------------------------------------------------------------------------

func (e *Executor) execute(args json.RawMessage) (any, error) {
	var in struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	t, ok := e.lookup(strings.TrimSpace(in.Name))
	if !ok {
		return nil, e.unknownTool(in.Name)
	}

	toolArgs := in.Args
	if len(toolArgs) == 0 || string(toolArgs) == "null" {
		toolArgs = json.RawMessage(`{}`)
	}
	// 必填参数在这里拦一道：错误信息会原样喂回模型，让它自己改参数重试
	if err := validateArgs(t, toolArgs); err != nil {
		return nil, err
	}

	start := time.Now()
	if t.Builtin {
		out, err := e.runBuiltin(t, toolArgs)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tool": t.Name, "result": out, "via": "builtin", "ms": ms(start)}, nil
	}

	if _, err := e.ctx.Consume(t.Service); err != nil {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound,
			"工具 %s 暂不可用（服务 %s %s）", t.Name, t.Service, reasonOf(err))
	}

	raw, err := e.ctx.Invoke(t.Service, t.Method, json.RawMessage(toolArgs))
	if err != nil {
		return nil, err // 原样透传工具的错误码（-32002 权限不足 / -32602 参数非法…）
	}
	return map[string]any{
		"tool":   t.Name,
		"result": json.RawMessage(raw),
		"via":    t.Service,
		"ms":     ms(start),
	}, nil
}

// runBuiltin 执行不依赖任何插件的内置工具。
func (e *Executor) runBuiltin(t Tool, args json.RawMessage) (any, error) {
	switch t.Name {
	case "now":
		now := time.Now()
		return map[string]any{
			"iso":  now.Format(time.RFC3339),
			"date": now.Format("2006-01-02"),
			"time": now.Format("15:04:05"),
			"unix": now.Unix(),
		}, nil
	default:
		return nil, plugin.Errorf(plugin.CodeInternalError, "内置工具 %s 没有实现", t.Name)
	}
}

// validateArgs 只做高价值的一类校验：schema 里声明为 required 的参数必须给。
// 这是模型最常犯的错，且错误信息可以原样喂回去让它自我修正。
func validateArgs(t Tool, args json.RawMessage) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return plugin.Errorf(plugin.CodeInvalidParams, "工具 %s 的参数必须是 JSON 对象: %v", t.Name, err)
	}
	if len(t.Schema) == 0 {
		return nil
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(t.Schema, &schema); err != nil {
		return nil // schema 写坏了不该阻塞执行
	}
	var missing []string
	for _, key := range schema.Required {
		v, ok := obj[key]
		if !ok || len(v) == 0 || string(v) == "null" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return plugin.Errorf(plugin.CodeInvalidParams,
			"工具 %s 缺少必填参数：%s", t.Name, strings.Join(missing, "、"))
	}
	return nil
}

func (e *Executor) unknownTool(name string) error {
	names := make([]string, 0, 8)
	for _, t := range e.all() {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return plugin.Errorf(plugin.CodeServiceNotFound,
		"没有名为 %q 的工具；可用工具：%s", name, strings.Join(names, "、"))
}

// ---------------------------------------------------------------------------
// register / unregister：不改代码就能加工具
// ---------------------------------------------------------------------------

func (e *Executor) register(args json.RawMessage) (any, error) {
	var in Tool
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 name")
	}
	if !toolNamePattern.MatchString(in.Name) {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"工具名只能是字母数字下划线（会作为 LLM 的 function name），当前为 %q", in.Name)
	}
	if strings.TrimSpace(in.Service) == "" || strings.TrimSpace(in.Method) == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"缺少 service / method：工具必须指向一个插件服务（内置工具请改代码）")
	}
	if _, exists := e.lookupDefault(in.Name); exists {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"工具 %s 已在出厂目录里，不需要重复注册（避免被意外覆盖）", in.Name)
	}

	e.mu.Lock()
	if e.extra == nil {
		e.extra = map[string]Tool{}
	}
	e.extra[in.Name] = in
	e.mu.Unlock()

	persisted := true
	if err := e.persist(); err != nil {
		persisted = false
		e.ctx.Log("warn", "工具 "+in.Name+" 注册成功但持久化失败："+err.Error(), nil)
	}
	e.ctx.Log("info", "已注册工具 "+in.Name, map[string]any{"service": in.Service, "persisted": persisted})
	return map[string]any{"ok": true, "name": in.Name, "persisted": persisted}, nil
}

func (e *Executor) unregister(args json.RawMessage) (any, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	name := strings.TrimSpace(in.Name)
	if _, exists := e.lookupDefault(name); exists {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "工具 %s 来自出厂目录，不能用 unregister 删除", name)
	}

	e.mu.Lock()
	_, ok := e.extra[name]
	delete(e.extra, name)
	e.mu.Unlock()
	if !ok {
		return nil, e.unknownTool(name)
	}

	if err := e.persist(); err != nil {
		e.ctx.Log("warn", "工具 "+name+" 已移除但持久化失败："+err.Error(), nil)
	}
	return map[string]any{"ok": true, "name": name}, nil
}

// ---------------------------------------------------------------------------
// 目录与持久化
// ---------------------------------------------------------------------------

// all 返回出厂目录 + 注册目录的合并结果（按名字排序，输出稳定）。
func (e *Executor) all() []Tool {
	out := defaultCatalog()

	e.mu.RLock()
	for _, t := range e.extra {
		out = append(out, t)
	}
	e.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (e *Executor) lookup(name string) (Tool, bool) {
	name = strings.TrimSpace(name)
	if t, ok := e.lookupDefault(name); ok {
		return t, true
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	t, ok := e.extra[name]
	return t, ok
}

func (e *Executor) lookupDefault(name string) (Tool, bool) {
	for _, t := range defaultCatalog() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// persist 把注册进来的工具写入插件配置，重启后依然在。
func (e *Executor) persist() error {
	e.mu.RLock()
	list := make([]Tool, 0, len(e.extra))
	for _, t := range e.extra {
		list = append(list, t)
	}
	e.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return e.ctx.SetConfig(configKey, list)
}

func (e *Executor) loadRegistered() error {
	raw, err := e.ctx.GetConfig(configKey)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var list []Tool
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, t := range list {
		if t.Name != "" {
			e.extra[t.Name] = t
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func ms(since time.Time) int64 { return time.Since(since).Milliseconds() }

// reasonOf 把「服务不可用」翻译成一句人能看懂的原因。
func reasonOf(err error) string {
	switch {
	case err == nil:
		return ""
	case plugin.IsCode(err, plugin.CodeServiceNotFound):
		return "提供者插件未安装"
	case plugin.IsCode(err, plugin.CodeProviderDead):
		return "提供者插件已退出"
	case plugin.IsCode(err, plugin.CodeDependencyMissing):
		return "提供者插件依赖缺失"
	case plugin.IsCode(err, plugin.CodeAPIVersionIncompatible):
		return "提供者插件版本不兼容"
	default:
		return err.Error()
	}
}

func main() {
	if err := plugin.Serve(&Executor{}); err != nil {
		plugin.Logf("error", "工具执行器退出: %v", err)
		os.Exit(1)
	}
}
