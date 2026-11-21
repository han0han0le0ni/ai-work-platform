// 「一键造插件」编排：一句自然语言需求 → AI 学规则 → 生成代码 →
// 落盘骨架 →（Go）编译 → 重启即装。
//
// 它把三个官方插件串成一条流水线：
//
//	pluginManager.v1 rules   —— 取回机器可读的插件规则与完整骨架
//	llm.v1 chat              —— 让模型按规则 + 骨架设计并写出完整入口源码
//	pluginManager.v1 create  —— 校验 id/服务名/权限并写入 plugins/<id>.plugin
//	builder.v1 build         —— Go 源码编译成可执行文件（带两轮编译错误自愈）
//
// 全程可能耗时一两分钟，HTTP 请求立即返回 accepted，进度经 SSE 的 craft
// 事件逐步推给浏览器；成功后由前端一键 core.restart 完成安装。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"aiwork/sdk/go/plugin"
)

// craftStep 是流水线里的一步。
type craftStep struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	State string `json:"state"` // running / ok / fail
	Note  string `json:"note,omitempty"`
}

// craftState 是推给浏览器的完整进度帧（每次整体替换，丢一帧也不影响显示）。
type craftState struct {
	Running bool        `json:"running"`
	Done    bool        `json:"done"`
	Error   string      `json:"error,omitempty"`
	Steps   []craftStep `json:"steps"`
	Plugin  *craftInfo  `json:"plugin,omitempty"`
}

// craftInfo 是造好的插件摘要，供前端展示与触发重启安装。
type craftInfo struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Runtime  string   `json:"runtime"`
	Dir      string   `json:"dir"`
	Files    []string `json:"files"`
	Provides []string `json:"provides"`
	Methods  []string `json:"methods"`
}

var craftStepOrder = []struct{ key, title string }{
	{"rules", "研读插件规则与骨架"},
	{"design", "AI 设计接口并编写代码"},
	{"create", "写入插件目录与清单"},
	{"build", "编译可执行文件"},
	{"install", "安装（重启平台后加载）"},
}

// craftPlan 是模型必须吐出的设计结果。
type craftPlan struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Runtime     string   `json:"runtime"`
	Provides    []string `json:"provides"`
	Methods     []string `json:"methods"`
	Consumes    []string `json:"consumes"`
	Permissions []string `json:"permissions"`
	Code        string   `json:"code"`
}

func (w *WebUI) broadcastCraft(st *craftState) { w.hub.broadcast("craft", st) }

// craftMark 把某一步置为状态并广播。
func (w *WebUI) craftMark(st *craftState, key, state, note string) {
	for i := range st.Steps {
		if st.Steps[i].Key == key {
			st.Steps[i].State = state
			st.Steps[i].Note = note
			break
		}
	}
	w.broadcastCraft(st)
}

func newCraftSteps() []craftStep {
	steps := make([]craftStep, 0, len(craftStepOrder))
	for _, s := range craftStepOrder {
		steps = append(steps, craftStep{Key: s.key, Title: s.title, State: "wait"})
	}
	return steps
}

// handleCraft 接收 {idea, lang}，后台开跑流水线。
func (w *WebUI) handleCraft(rw http.ResponseWriter, r *http.Request) {
	// GET：查询最近一次造插件任务状态（页面刷新 / 诊断用）。
	if r.Method == http.MethodGet {
		w.mu.Lock()
		st := w.craftState
		w.mu.Unlock()
		if st == nil {
			writeJSON(rw, map[string]any{"running": false})
			return
		}
		writeJSON(rw, st)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Idea string `json:"idea"`
		Lang string `json:"lang"` // auto / python / go
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	in.Idea = strings.TrimSpace(in.Idea)
	if in.Idea == "" {
		http.Error(rw, "empty idea", http.StatusBadRequest)
		return
	}
	if _, err := w.ctx.Consume("llm.v1"); err != nil {
		writeJSON(rw, map[string]any{"error": map[string]any{
			"message": "造插件需要模型服务（llm.v1），请先在「模型接入」里配好模型",
		}})
		return
	}
	w.mu.Lock()
	if w.craftRunning {
		w.mu.Unlock()
		http.Error(rw, "已有一个插件正在制造中", http.StatusConflict)
		return
	}
	w.craftRunning = true
	w.mu.Unlock()

	st := &craftState{Running: true, Steps: newCraftSteps()}
	w.mu.Lock()
	w.craftState = st
	w.mu.Unlock()
	w.broadcastCraft(st)
	go func() {
		defer func() {
			w.mu.Lock()
			w.craftRunning = false
			w.mu.Unlock()
		}()
		w.runCraft(in.Idea, strings.ToLower(strings.TrimSpace(in.Lang)), st)
	}()
	writeJSON(rw, map[string]any{"accepted": true})
}

// runCraft 是完整流水线；任何失败都转成 craft 错误帧，不 panic 到外面。
func (w *WebUI) runCraft(idea, lang string, st *craftState) {
	fail := func(format string, args ...any) {
		st.Running = false
		st.Error = fmt.Sprintf(format, args...)
		w.broadcastCraft(st)
	}

	// 1) 取回规则 + 骨架
	w.craftMark(st, "rules", "running", "")
	rulesRaw, err := w.ctx.Invoke("pluginManager.v1", "rules", map[string]any{})
	if err != nil {
		w.craftMark(st, "rules", "fail", err.Error())
		fail("读取插件规则失败（需要插件管理器 pluginManager.v1）：%v", err)
		return
	}
	var rules struct {
		Sections []struct {
			Title string   `json:"title"`
			Lines []string `json:"lines"`
		} `json:"sections"`
		Skeletons     map[string]string `json:"skeletons"`
		SkeletonEntry map[string]string `json:"skeletonEntry"`
		Flow          []string          `json:"flow"`
	}
	if err := json.Unmarshal(rulesRaw, &rules); err != nil {
		w.craftMark(st, "rules", "fail", err.Error())
		fail("解析插件规则失败：%v", err)
		return
	}

	// auto：一律优先 python（免编译、一次成功率高，也是平台推荐语言）。
	// 不依赖 builder 的 toolchains 探测 —— Windows 上 python.exe 常是商店别名，
	// spawn `python --version` 会误报不可用，但解释器实际能跑插件（由核心加载验证）。
	if lang == "" || lang == "auto" {
		lang = "python"
	}
	if lang != "python" && lang != "go" {
		fail("不支持的语言 %q（只支持 python / go）", lang)
		return
	}
	w.craftMark(st, "rules", "ok", "规则已就绪 · 目标语言 "+lang)

	// 2) 让模型按规则 + 骨架设计并写码
	w.craftMark(st, "design", "running", "")
	systemPrompt := buildCraftPrompt(rules.Sections, rules.Skeletons[lang],
		rules.SkeletonEntry[lang], rules.Flow, lang)
	plan, err := w.designPlugin(systemPrompt, idea)
	if err != nil {
		w.craftMark(st, "design", "fail", err.Error())
		fail("AI 设计插件失败：%v", err)
		return
	}
	if plan.Runtime == "" {
		plan.Runtime = lang
	}
	if plan.Runtime != lang {
		// 模型自作主张换语言：以用户选择为准，避免它要了 go 却给 python
		plan.Runtime = lang
	}

	// 2.5) 服务名占用查重：小模型常随手写成 toolExecutor.v1 这种已被官方插件
	// 占用的名字，重启时核心会拒绝注册（插件"运行中"却没有服务）。
	// 创建前把撞名的服务改成 <id 末段>.vN 并同步替换源码字面量。
	occupied := w.occupiedServices(plan.ID)
	if len(occupied) > 0 {
		var renamed []string
		plan.Provides, plan.Code, renamed = resolveServiceCollisions(plan.Provides, plan.ID, plan.Code, occupied)
		if len(renamed) > 0 {
			w.craftMark(st, "design", "running",
				"服务名与已装插件冲突，自动改为 "+strings.Join(renamed, ", "))
		}
	}
	methods := strings.Join(plan.Methods, ", ")
	if methods == "" {
		methods = "-"
	}
	w.craftMark(st, "design", "ok",
		fmt.Sprintf("%s（%s）· 服务 %s · 方法 %s", plan.Name, plan.ID,
			strings.Join(plan.Provides, ", "), methods))

	// 3) create 落盘
	w.craftMark(st, "create", "running", "")
	created, err := w.createFromPlan(plan, false)
	if err != nil {
		w.craftMark(st, "create", "fail", err.Error())
		fail("写入插件失败：%v", err)
		return
	}
	w.craftMark(st, "create", "ok", created.Dir)
	st.Plugin = &craftInfo{
		ID: created.ID, Name: plan.Name, Runtime: created.Runtime,
		Dir: created.Dir, Files: created.Files, Provides: plan.Provides,
		Methods: plan.Methods,
	}

	// 4) Go 需要编译，失败带着 stderr 让模型修两轮
	if lang == "go" {
		w.craftMark(st, "build", "running", "")
		if err := w.compileWithRepair(plan, rules.Skeletons["go"], st); err != nil {
			w.craftMark(st, "build", "fail", err.Error())
			fail("插件代码已生成但编译失败：%v\n\n可在插件目录里手动修正后重新编译。", err)
			return
		}
		w.craftMark(st, "build", "ok", "编译通过")
	} else {
		w.craftMark(st, "build", "ok", "Python 无需编译")
	}

	// 5) 安装（重启）由前端确认后触发，这里只把状态摆好
	st.Steps[4].State = "wait"
	st.Steps[4].Note = "点击下方按钮重启平台即完成安装"
	st.Running = false
	st.Done = true
	w.broadcastCraft(st)
}

// craftCreateResult 是 create 返回里流水线关心的字段。
type craftCreateResult struct {
	ID      string   `json:"id"`
	Dir     string   `json:"dir"`
	Runtime string   `json:"runtime"`
	Files   []string `json:"files"`
}

func (w *WebUI) createFromPlan(plan *craftPlan, force bool) (*craftCreateResult, error) {
	raw, err := w.ctx.Invoke("pluginManager.v1", "create", map[string]any{
		"id":          plan.ID,
		"name":        plan.Name,
		"description": plan.Description,
		"runtime":     plan.Runtime,
		"provides":    plan.Provides,
		"consumes":    plan.Consumes,
		"permissions": plan.Permissions,
		"code":        plan.Code,
		"force":       force,
	})
	if err != nil {
		return nil, err
	}
	var out craftCreateResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 create 响应失败: %w", err)
	}
	return &out, nil
}

// errBuildFailed 区分"编译器跑了但代码有错"（stderr 有料）与 builder 自身故障。
var errBuildFailed = errors.New("build failed")

// compileWithRepair 编译 Go 插件，三级救场：
//
//	自动修 import（go build 报错里最常见的两类，秒修）→
//	模型看着完整报错 + 骨架回炉一次（防小模型多轮重写越改越飘，只一轮）→
//	再自动修一次 import（回炉稿也常忘 import）。
//
// 仍不过就如实报错，代码已留在插件目录里可手工处理。
func (w *WebUI) compileWithRepair(plan *craftPlan, skeleton string, st *craftState) error {
	// build 编译当前 plan.Code 已落盘的目录；返回 (报错详情, nil|errBuildFailed|其他)。
	build := func() (string, error) {
		raw, err := w.ctx.Invoke("builder.v1", "build", map[string]any{
			"dir":  plan.dirName(),
			"lang": "go",
		})
		var res struct {
			OK     bool   `json:"ok"`
			Stderr string `json:"stderr"`
			Stdout string `json:"stdout"`
		}
		_ = json.Unmarshal(raw, &res)
		if err == nil && res.OK {
			return "", nil
		}
		if err != nil {
			return "", err // builder 自身故障（go 未安装 / spawn 失败等）
		}
		return strings.TrimSpace(res.Stderr + "\n" + res.Stdout), errBuildFailed
	}
	save := func() error { _, e := w.createFromPlan(plan, true); return e }

	detail, err := build()
	if err == nil {
		return nil
	}
	if !errors.Is(err, errBuildFailed) {
		return err
	}

	// 第一轮：不打扰模型，按报错自动修 import。
	w.craftMark(st, "build", "running", "编译未过，正在自动修正 import…")
	if fixed, ok := autoFixGoImports(plan.Code, detail); ok {
		plan.Code = fixed
		if save() == nil {
			if d2, e2 := build(); e2 == nil {
				return nil
			} else if errors.Is(e2, errBuildFailed) {
				detail = d2
			} else {
				return e2
			}
		}
	}

	// 第二轮：把完整报错、坏代码、骨架一起给模型，回炉重写一次。
	w.craftMark(st, "build", "running", "AI 正在按报错回炉重写…")
	fixed, ferr := w.repairGoCode(plan.Code, detail, skeleton, plan.Provides)
	if ferr != nil {
		return fmt.Errorf("编译失败，自动回炉也未产出有效代码。\n编译报错：%s\n回炉环节：%v", detail, ferr)
	}
	plan.Code = fixed
	if e := save(); e != nil {
		return fmt.Errorf("写入回炉代码失败：%w（编译报错：%s）", e, detail)
	}
	detail2, err2 := build()
	if err2 == nil {
		return nil
	}
	if !errors.Is(err2, errBuildFailed) {
		return err2
	}

	// 第三轮：回炉稿若仍只是 import 小毛病，再自动修一次。
	if fixed2, ok := autoFixGoImports(plan.Code, detail2); ok {
		plan.Code = fixed2
		if save() == nil {
			if _, e3 := build(); e3 == nil {
				return nil
			}
		}
	}
	return fmt.Errorf("两轮自动修复后仍编译失败：\n%s", detail2)
}

func (p *craftPlan) dirName() string {
	parts := strings.Split(p.ID, ".")
	return parts[len(parts)-1] + ".plugin"
}

// designPlugin 调一次 llm.v1 让模型吐出严格 JSON 的插件设计。
func (w *WebUI) designPlugin(systemPrompt, idea string) (*craftPlan, error) {
	raw, err := w.ctx.Invoke("llm.v1", "chat", map[string]any{
		"system": systemPrompt,
		"messages": []map[string]any{
			{"role": "user", "content": "请按规则制造这个插件：\n\n" + idea},
		},
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析模型响应失败: %w", err)
	}
	plan, err := parseCraftPlan(out.Content)
	if err != nil {
		// 第二次机会：小模型最常栽在 JSON 字符串转义上 —— 明确要求改用分隔块格式。
		raw2, err2 := w.ctx.Invoke("llm.v1", "chat", map[string]any{
			"system": systemPrompt,
			"messages": []map[string]any{
				{"role": "user", "content": "请按规则制造这个插件：\n\n" + idea},
				{"role": "assistant", "content": out.Content},
				{"role": "user", "content": "你的上一条回复无法解析（" + err.Error() + "）。" +
					"请严格按下面的纯文本格式重新输出，代码保持原样、不要做任何转义，" +
					"不要 markdown 代码块、不要多余解释：\n" + craftBlockTemplate()},
			},
		})
		if err2 != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw2, &out)
		plan, err = parseCraftPlan(out.Content)
		if err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// repairGoCode 带着完整编译错误、骨架与服务名约束让模型回炉重写 main.go。
func (w *WebUI) repairGoCode(code, buildErr, skeleton string, provides []string) (string, error) {
	service := ""
	if len(provides) > 0 {
		service = provides[0]
	}
	system := "你是 Go 插件修复专家。你会收到一份编译失败的插件 main.go 和完整 go build 报错。" +
		"要求：\n" +
		"1. 参照给定骨架，重新输出**一个完整的 main.go 文件**\n" +
		"2. 输出必须恰好以 `package main` 开头、只含一个 `func main()`，" +
		"禁止拼接两段代码、禁止重复 import 块、禁止 markdown 与任何解释\n" +
		"3. 保留对 aiwork/sdk/go/plugin 的导入和 plugin.Serve 入口\n" +
		"4. 只 import 真正用到的包；业务逻辑写在 Handle 里"
	if service != "" {
		system += "\n5. 提供的服务名必须仍是 " + service + "，不得改名"
	}
	user := "go build 完整报错：\n" + buildErr + "\n\n编译失败的 main.go：\n" + code +
		"\n\n官方骨架（参照它的结构）：\n" + skeleton
	raw, err := w.ctx.Invoke("llm.v1", "chat", map[string]any{
		"system":   system,
		"messages": []map[string]any{{"role": "user", "content": user}},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return sanitizeGoSource(out.Content)
}

// sanitizeGoSource 校验模型回炉后的 Go 源码是"一个文件"而不是多段拼接。
func sanitizeGoSource(s string) (string, error) {
	s = stripCodeFence(s)
	// 容忍模型在 package main 前加几行注释 / 空行；从第一个 package main 截断起头。
	if i := strings.Index(s, "package main"); i > 0 {
		s = s[i:]
	}
	if !strings.HasPrefix(s, "package main") {
		return "", plugin.Errorf(plugin.CodeInternalError, "输出不是以 package main 开头的 Go 源码")
	}
	if strings.Count(s, "package main") != 1 {
		return "", plugin.Errorf(plugin.CodeInternalError, "输出里出现了多个 package main（多段代码拼接）")
	}
	if strings.Count(s, "func main()") != 1 {
		return "", plugin.Errorf(plugin.CodeInternalError, "输出里 func main() 不唯一")
	}
	if !strings.Contains(s, "aiwork/sdk/go/plugin") || !strings.Contains(s, "plugin.Serve") {
		return "", plugin.Errorf(plugin.CodeInternalError, "输出缺少 SDK 导入或 plugin.Serve 入口")
	}
	return s, nil
}

// parseCraftPlan 从模型输出里抠出 JSON 并校验必填字段。
func parseCraftPlan(s string) (*craftPlan, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("模型返回为空")
	}
	// 主格式：分隔块（代码原文包裹、零转义，小模型也容易遵守）。
	if p, err := parseCraftBlock(s); err == nil {
		return p, nil
	}
	// 兼容格式：模型自行输出 JSON（含被 ```json 包裹的情况）。
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		var p craftPlan
		if err := json.Unmarshal([]byte(s[start:end+1]), &p); err == nil &&
			strings.TrimSpace(p.ID) != "" && strings.TrimSpace(p.Code) != "" && len(p.Provides) > 0 {
			return finalizePlan(&p)
		}
	}
	return nil, fmt.Errorf("既找不到 CODE-BEGIN/CODE-END 代码块，也找不到合法 JSON")
}

// craftBlockTemplate 是要求模型遵守的纯文本输出模板。
func craftBlockTemplate() string {
	return `PLUGIN-ID: com.ai.<英文短名>
PLUGIN-NAME: <中文名>
RUNTIME: python 或 go
PROVIDES: <服务名.v1，多个用逗号分隔>
METHODS: <对外方法名，必须照用用户需求里给出的名字，多个用逗号分隔>
CONSUMES: <没有就留空>
PERMISSIONS: <没有就留空，多个用逗号分隔>
DESCRIPTION: <一句话描述>
CODE-BEGIN
<这里是完整入口源码，原样输出，不要转义，不要 markdown>
CODE-END`
}

// blockFieldPattern 抓 "KEY: value" 头部行。注意只能吃水平空白：
// \s 包含换行，会把下一个空值字段名吞进上一行的值里。
var blockHeadPattern = regexp.MustCompile(`(?m)^([A-Z-]+)[ \t]*:[ \t]?(.*)$`)

// parseCraftBlock 解析分隔块格式；code 取 CODE-BEGIN / CODE-END 之间的原文。
func parseCraftBlock(s string) (*craftPlan, error) {
	codeRe := regexp.MustCompile(`(?is)CODE-BEGIN[^\S\r\n]*\r?\n(.*?)\r?\n?[^\S\r\n]*CODE-END`)
	m := codeRe.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("缺少 CODE-BEGIN/CODE-END")
	}
	code := strings.TrimSpace(m[1])
	code = stripCodeFence(code) // 模型若手滑又套了 ``` 也能救回来
	if code == "" {
		return nil, fmt.Errorf("CODE-BEGIN 与 CODE-END 之间没有代码")
	}
	// 头部字段只取 CODE-BEGIN 之前的区域，避免把代码里的 KEY: 行误识别。
	headStart := strings.Index(s, "CODE-BEGIN")
	headText := s[:headStart]

	fields := map[string]string{}
	for _, hm := range blockHeadPattern.FindAllStringSubmatch(headText, -1) {
		fields[strings.ToUpper(hm[1])] = strings.TrimSpace(hm[2])
	}
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := fields[k]; ok {
				return v
			}
		}
		return ""
	}
	id := get("PLUGIN-ID", "ID")
	provides := splitCSV(get("PROVIDES", "PROVIDE", "SERVICES"))
	if id == "" || len(provides) == 0 {
		return nil, fmt.Errorf("块格式缺少 PLUGIN-ID 或 PROVIDES")
	}
	p := &craftPlan{
		ID:          id,
		Name:        get("PLUGIN-NAME", "NAME"),
		Description: get("DESCRIPTION", "DESC"),
		Runtime:     strings.ToLower(get("RUNTIME", "LANGUAGE", "LANG")),
		Provides:    provides,
		Methods:     splitCSV(get("METHODS", "METHOD")),
		Consumes:    splitCSV(get("CONSUMES", "CONSUME")),
		Permissions: splitCSV(get("PERMISSIONS", "PERMISSION")),
		Code:        code,
	}
	return finalizePlan(p)
}

// splitCSV 拆逗号 / 空白分隔的清单并去空。
func splitCSV(s string) []string {
	s = strings.ReplaceAll(s, "，", ",")
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// finalizePlan 做两种格式共用的必填校验、id 规范化与服务名规范化。
func finalizePlan(p *craftPlan) (*craftPlan, error) {
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Code) == "" || len(p.Provides) == 0 {
		return nil, fmt.Errorf("缺少必填字段（id / provides / code）")
	}
	p.ID = normalizePluginID(p.ID)
	if !pluginIDPattern.MatchString(p.ID) {
		return nil, fmt.Errorf("id %q 无法修正为反向域名格式", p.ID)
	}
	p.Provides, p.Code = normalizeProvides(p.Provides, p.ID, p.Code)
	// 模型可能把代码里实际注册的名字写错（漏 .v1、与 manifest 不一致），
	// 以规范化后的第一个服务名为唯一真相，强制对齐代码中的注册行。
	want := p.Provides[0]
	p.Code = enforceRegisteredServiceName(p.Code, want, p.Runtime)
	// 对外方法名也以代码里实际分发的分支为准（模型声明不可信）。
	if got := extractMethods(p.Code, p.Runtime); len(got) > 0 {
		p.Methods = got
	}
	return p, nil
}

var (
	// Python 骨架：if method == "xxx" / elif method == 'xxx'
	rePyMethodBranch = regexp.MustCompile(`(?:if|elif)\s+method\s*==\s*["']([A-Za-z_][A-Za-z0-9_]*)["']`)
	// Go 骨架：switch req.Method { case "xxx": }
	reGoMethodCase = regexp.MustCompile(`(?m)case\s+"([A-Za-z_][A-Za-z0-9_]*)"\s*:`)
)

// extractMethods 从源码的方法分发分支里提取真实对外方法名，保序去重。
func extractMethods(code, runtime string) []string {
	var re *regexp.Regexp
	if runtime == "go" {
		re = reGoMethodCase
	} else {
		re = rePyMethodBranch
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(code, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

var (
	// Python 骨架：_SERVICE = "x" 常量，以及 core.provide 的 name 字段。
	rePyServiceConst = regexp.MustCompile(`(?m)^(\s*_?SERVICE\s*=\s*)["'][^"']*["']`)
	rePyProvideName  = regexp.MustCompile(`(?m)(.*core\.provide[^\n]*?"name"\s*:\s*)["'][^"']*["']`)
	// Go 骨架：const serviceName = "x"，或内联 ctx.Provide("x", …)。
	reGoServiceConst = regexp.MustCompile(`(?m)(^|\n)(\s*(?:const|var)\s+\w*[Ss]ervice\w*\s*=\s*)"[^"]*"`)
	reGoProvideName  = regexp.MustCompile(`(\.\w*[Pp]rovide\(\s*)"[^"]*"`)
)

// enforceRegisteredServiceName 把代码里向核心注册服务的名字统一改成 want。
// 骨架用的是常量、模型偶尔内联字符串或漏写版本后缀，两种写法都覆盖。
func enforceRegisteredServiceName(code, want, runtime string) string {
	q := func(s string) string { return `"` + s + `"` }
	switch runtime {
	case "go":
		code = reGoServiceConst.ReplaceAllString(code, "${1}${2}"+q(want))
		code = reGoProvideName.ReplaceAllString(code, "${1}"+q(want))
	default: // python 及其它
		code = rePyServiceConst.ReplaceAllString(code, "${1}"+q(want))
		code = rePyProvideName.ReplaceAllString(code, "${1}"+q(want))
	}
	return code
}

// serviceNamePattern 与核心 registry 的命名规则保持一致：
//
//	^[A-Za-z][A-Za-z0-9]*(\.[A-Za-z0-9]+)*\.v[0-9]+$
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(\.[A-Za-z0-9]+)*\.v[0-9]+$`)

var serviceBadChars = regexp.MustCompile(`[^A-Za-z0-9]+`)

// normalizeProvides 保证每个服务名都能通过核心注册：
// 不合法（常见于小模型给出中文 / 带下划线的名字）时，用 id 末段派生 <slug>.v1，
// 并把源码里出现的旧名字面量一并替换 —— 否则 manifest 改了、代码注册的还是旧名。
func normalizeProvides(provides []string, id, code string) ([]string, string) {
	segs := strings.Split(id, ".")
	fallback := serviceBadChars.ReplaceAllString(segs[len(segs)-1], "")
	if fallback == "" || fallback[0] < 'A' || (fallback[0] > 'Z' && fallback[0] < 'a') || fallback[0] > 'z' {
		fallback = "svc" + fallback
	}
	fallback += ".v1"

	out := make([]string, 0, len(provides))
	seen := map[string]bool{}
	for _, name := range provides {
		name = strings.TrimSpace(name)
		if serviceNamePattern.MatchString(name) {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
			continue
		}
		fixed := fallback
		// 模型给的若是 xxx.v2 之类、只是前缀非法，保留它声明的版本号。
		if m := regexp.MustCompile(`\.v([0-9]+)$`).FindStringSubmatch(name); m != nil {
			fixed = strings.TrimSuffix(fallback, ".v1") + ".v" + m[1]
		}
		if name != "" {
			code = strings.ReplaceAll(code, name, fixed)
		}
		if !seen[fixed] {
			seen[fixed] = true
			out = append(out, fixed)
		}
	}
	return out, code
}

// occupiedServices 汇总当前已安装插件声明的服务名（撞名会被核心拒绝注册）。
// selfID 对应的插件是"自己"（force 重建场景），不计入占用。
func (w *WebUI) occupiedServices(selfID string) map[string]bool {
	raw, err := w.ctx.Invoke("pluginManager.v1", "list", map[string]any{})
	if err != nil {
		return nil // 查不到就不做这层防护，create / 核心注册仍会兜底
	}
	var list struct {
		Plugins []struct {
			ID       string   `json:"id"`
			Provides []string `json:"provides"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, p := range list.Plugins {
		if p.ID == selfID {
			continue
		}
		for _, s := range p.Provides {
			out[s] = true
		}
	}
	return out
}

// resolveServiceCollisions 把撞名的服务改写为 <id 末段>[序号].v<N>，
// 同时替换源码里出现的旧名字面量；返回 (新服务清单, 新代码, 改名描述)。
func resolveServiceCollisions(provides []string, id, code string, occupied map[string]bool) ([]string, string, []string) {
	segs := strings.Split(id, ".")
	base := serviceBadChars.ReplaceAllString(segs[len(segs)-1], "")
	if base == "" {
		base = "svc"
	}
	used := map[string]bool{}
	out := make([]string, 0, len(provides))
	var renamed []string
	for _, name := range provides {
		final := name
		if occupied[name] {
			ver := "v1"
			if m := regexp.MustCompile(`\.(v[0-9]+)$`).FindStringSubmatch(name); m != nil {
				ver = m[1]
			}
			cand := base + "." + ver
			for n := 2; occupied[cand] || used[cand]; n++ {
				cand = fmt.Sprintf("%s%d.%s", base, n, ver)
			}
			code = strings.ReplaceAll(code, name, cand)
			renamed = append(renamed, name+" → "+cand)
			final = cand
		}
		if !used[final] {
			used[final] = true
			out = append(out, final)
		}
	}
	return out, code, renamed
}

// pluginIDPattern 与核心 / plugin-manager 的校验保持一致。
var pluginIDPattern = regexp.MustCompile(
	`^[A-Za-z0-9]([A-Za-z0-9_-]*)(\.[A-Za-z0-9]([A-Za-z0-9_-]*))+$`)

// idBadChars 把不合规字符替成短横线。
var idBadChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// normalizePluginID 兜底模型给出的奇怪 id：
// 合法则原样返回；缺点 / 含非法字符时尽量修成 com.ai.<英文短名>。
func normalizePluginID(id string) string {
	id = strings.TrimSpace(id)
	if pluginIDPattern.MatchString(id) {
		return id
	}
	segs := strings.Split(id, ".")
	last := strings.TrimSpace(segs[len(segs)-1])
	last = strings.ToLower(idBadChars.ReplaceAllString(last, "-"))
	last = strings.Trim(last, "-_")
	if last == "" {
		last = "custom"
	}
	return "com.ai." + last
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}

// buildCraftPrompt 把规则与骨架拼成系统提示词。
func buildCraftPrompt(sections []struct {
	Title string   `json:"title"`
	Lines []string `json:"lines"`
}, skeleton, entryNote string, flow []string, lang string) string {
	var b strings.Builder
	b.WriteString("你是「AI 工作平台」的插件制造机。平台是 Go 微内核 + stdio JSON-RPC 子进程插件架构。")
	b.WriteString("用户用一句中文描述需求，你要输出一个可直接安装运行的 ")
	b.WriteString(lang)
	b.WriteString(" 插件。\n\n## 平台强制执行的插件规则（违反则加载被拒）\n")
	for _, sec := range sections {
		b.WriteString("### " + sec.Title + "\n")
		for _, ln := range sec.Lines {
			b.WriteString("- " + ln + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## 标准流程\n")
	for _, f := range flow {
		b.WriteString(f + "\n")
	}
	b.WriteString("\n## 入口文件形态\n" + entryNote + "\n")
	b.WriteString("\n## 你必须严格参照的 ")
	b.WriteString(lang)
	b.WriteString(" 完整骨架（协议样板一字不易，只替换/新增业务逻辑；提供的服务名与方法要自洽）\n```\n")
	b.WriteString(skeleton)
	b.WriteString("\n```\n\n")
	b.WriteString("## 输出格式（必须严格遵守，禁止输出除此之外的任何文字、解释或 markdown）\n")
	b.WriteString(craftBlockTemplate())
	b.WriteString("\n\n其余约束：\n")
	b.WriteString("- PLUGIN-ID 第一段统一用 com.ai，最后一段用英文短名（如 com.ai.weather），全程小写\n")
	b.WriteString("- PROVIDES 服务名必须与代码里 ctx.Provide / core.provide 的名字完全一致且带 .v1\n")
	b.WriteString("- METHODS 与代码里的方法分支必须是用户需求中**原话给出的方法名**" +
		"（用户说 gcd 方法就只能叫 gcd，禁止改成 run/handle/execute 之类）；用户没指定时才自选一个达意的英文名\n")
	b.WriteString("- 不要照搬骨架示例方法：删掉用不上的 ping/run，只保留本插件真正对外的方法\n")
	b.WriteString("- PERMISSIONS 只申请真正用到的；不需要就整行留空\n")
	b.WriteString("- CODE-BEGIN 与 CODE-END 之间必须是**完整可运行**的入口文件（含全部协议样板），不能留 TODO 或省略号，代码原样输出、不要做 JSON 式转义\n")
	b.WriteString("- 业务逻辑就地实现：纯计算 / 字符串 / 时间等不依赖网络；确需网络时申请 network 权限\n")
	b.WriteString("- 需求不明确时，按最合理的默认理解实现，并在 description 里写清楚插件做什么\n")
	return b.String()
}
