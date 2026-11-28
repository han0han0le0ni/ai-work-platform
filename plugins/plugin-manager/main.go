// Command plugin-manager 是官方插件管理插件，提供 pluginManager.v1。
//
// 它和 fs-tool 一样**不直接碰文件系统**：列目录、读 manifest、删除文件全部交给
// 核心的能力代理（core.fs.list / core.fs.read / core.fs.write / core.fs.delete），
// 权限没声明就删不掉 —— 这是设计，不是限制。
//
// “删干净”具体指四件事，删除一个插件时全部做完：
//  1. 插件目录本身（plugins/<name>.plugin/）
//  2. 它的配置（plugin-configs/<id>.json）
//  3. 它的日志（logs/<id>.log，可用 keepLogs 保留）
//  4. 目标正在运行、文件被占用时：目录原样保留并记为待删除，核心在下次启动、
//     加载任何插件之前把它清掉 —— 那时没有进程占用，删得干净
//
// 删除遵循规范 2.6：变更重启生效，绝不热更新。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"aiwork/sdk/go/plugin"
)

// 服务名与方法清单。
const serviceName = "pluginManager.v1"

// contribService 是 DSH 式"UI 贡献"服务：壳子来问这个插件要往哪些插槽
// （侧栏导航 / 脚部动作 / 顶栏动作）放什么，而不是由插件自己画一整页。
const contribService = "ui.contrib.v1"

// uiServiceName 是界面插件提供的服务。
//
// 提供它的插件**不能热卸载**：它的进程就是用户正在看的界面，当场杀掉的话
// 用户连删除结果都看不到（界面直接没了）。删除界面插件退回"重启生效"。
const uiServiceName = "ui.v1"

// 运行时目录布局（相对核心解析出的基准目录）。
const (
	pluginsDir  = "plugins"
	configsDir  = "plugin-configs"
	logsDir     = "logs"
	pendingDir  = "pending-delete"
	pendingFile = "pending-restart.json"
)

// disabledSuffix 是禁用插件时给目录加的后缀。
//
// 核心的扫描只认 *.plugin 结尾的目录（loader.Scan），所以
// demo.plugin → demo.plugin.disabled 就等于"不再加载"，改回来就是启用。
// 不需要核心提供任何"禁用"接口。
const disabledSuffix = ".disabled"

// managerAPI 是本插件用到的那几样核心能力（生产上注入的就是 *plugin.Context）。
//
// 抽成接口只为一件事：单测里能换成假的 —— 界面逻辑（点了什么、画成什么样）
// 不该为了测一下就去连真核心。
type managerAPI interface {
	ID() string
	Can(perm string) bool
	Consume(name string) (string, error)
	Invoke(service, method string, args any) (json.RawMessage, error)
	Emit(event string, payload any) error
	Log(level, msg string, meta map[string]any)
	// Capability 直连核心方法（如 core.unload 热卸载、core.restart）。生产上是
	// *plugin.Context，测试里是 fakeAPI。
	Capability(method string, params any) (json.RawMessage, error)
	FSList(path string) (json.RawMessage, error)
	FSRead(path string) (json.RawMessage, error)
	FSWrite(path, content string, appendMode, mkdir bool) (json.RawMessage, error)
	FSDelete(path string, recursive bool) (json.RawMessage, error)
	FSMove(from, to string) (json.RawMessage, error)
	FSMkdir(path string, recursive bool) (json.RawMessage, error)
}

// Manager 实现 plugin.Plugin。
type Manager struct {
	ctx  *plugin.Context
	api  managerAPI
	root string // 运行时根目录，由核心解析得到（core.json 的 fsRoot，或运行时目录）

	// panel 是插件页那一页的界面状态（见 panel.go）。
	panel pmPanel
}

// ---------------------------------------------------------------------------
// 数据模型
// ---------------------------------------------------------------------------

// pluginInfo 是一个已安装插件的可读摘要。
type pluginInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Version     string   `json:"version,omitempty"`
	Runtime     string   `json:"runtime,omitempty"`
	Entry       string   `json:"entry,omitempty"`
	Provides    []string `json:"provides,omitempty"`
	Consumes    []string `json:"consumes,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Compat      string   `json:"compat,omitempty"`     // 兼容的核心版本范围
	Dir         string   `json:"dir"`                  // 目录名，例如 demo.plugin（禁用后带 .disabled）
	DirPath     string   `json:"dirPath"`              // 绝对路径
	Self        bool     `json:"self"`                 // 是否是插件管理器自己
	Disabled    bool     `json:"disabled"`             // 目录带 .disabled：重启后不会被加载
	Unreadable  bool     `json:"unreadable,omitempty"` // manifest 缺失或损坏
	Error       string   `json:"error,omitempty"`
	Description string   `json:"description,omitempty"`
}

// manifestFile 只取管理需要的字段，其余交给核心校验。
type manifestFile struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Version      string            `json:"version"`
	Runtime      string            `json:"runtime"`
	Entry        string            `json:"entry"`
	Provides     []string          `json:"provides"`
	Consumes     []string          `json:"consumes"`
	Permissions  []string          `json:"permissions"`
	Compat       map[string]string `json:"compat"`
	Dependencies map[string]string `json:"dependencies"`
}

// installed 是“目录 + manifest”的组合。
type installed struct {
	info pluginInfo
	man  manifestFile
}

// deleteResult 是单次删除的结果，字段与 core.fs.delete 一致。
type deleteResult struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Deleted bool   `json:"deleted"`
	Moved   bool   `json:"moved"`
	Pending string `json:"pending,omitempty"`
	Error   string `json:"error,omitempty"`
}

// pendingChange 与核心 loader.Change 结构保持一致，重启时由核心消费。
type pendingChange struct {
	Action   string `json:"action"`
	PluginID string `json:"pluginId,omitempty"`
	Dir      string `json:"dir,omitempty"`
	By       string `json:"by,omitempty"`
	At       string `json:"at"`
}

// entry 是 core.fs.list 返回的目录条目。
type entry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

// deleteArgs 是 delete 方法的入参。
type deleteArgs struct {
	ID       string `json:"id"`       // 插件 ID（manifest 的 id），也接受目录名
	Dir      string `json:"dir"`      // 目录名，例如 demo.plugin；ID 有重复时靠它区分
	DryRun   bool   `json:"dryRun"`   // 只返回将要删除的清单，不真删
	KeepLogs bool   `json:"keepLogs"` // 保留 logs/<id>.log
}

// ---------------------------------------------------------------------------
// 生命周期
// ---------------------------------------------------------------------------

// Activate 注册服务并确定运行时根目录。
func (m *Manager) Activate(ctx *plugin.Context) error {
	m.ctx, m.api = ctx, ctx

	root, err := m.resolveRoot()
	if err != nil {
		return err
	}
	m.root = root

	if err := ctx.Provide(serviceName, map[string]any{
		"description": "插件管理：列出 / 检查 / 启停 / 删除插件（删除连配置与日志一起清理，重启后生效）",
		"methods": []string{
			"list", "inspect", "enable", "disable", "setEnabled",
			"delete", "create", "purge", "rules",
		},
	}); err != nil {
		return err
	}

	// 自带界面：把插件页挂在 UI 插件上。
	//
	// 提供 ui.page.* 就代表"我有一页自己的界面" —— UI 插件会来问
	// list / render / action / key，页面上有什么它一概不知道。
	if err := ctx.Provide(panelService, map[string]any{
		"description": "插件页：清单 / 详情 / 启停 / 删除（插件管理器自己渲染）",
		"methods":     []string{"list", "render", "action", "key"},
	}); err != nil {
		ctx.Log("warn", "自带界面没能挂上（"+err.Error()+"），功能不受影响", nil)
	}

	// DSH 式 UI 贡献：告诉壳子"规则页放侧栏哪一格、插件页放顶栏哪个按钮"。
	// 壳子负责几何与渲染，这里只声明条目。
	if err := ctx.Provide(contribService, map[string]any{
		"description": "UI 贡献：侧栏导航 / 顶栏动作（DSH slot 模型）",
		"methods":     []string{"list"},
	}); err != nil {
		ctx.Log("warn", "UI 贡献没能注册（"+err.Error()+"），插件页仍可从面板列表进入", nil)
	}

	ctx.Log("info", "插件管理器已就绪（pluginManager.v1）", map[string]any{
		"root":        m.root,
		"permissions": ctx.Permissions,
	})
	return nil
}

// resolveRoot 询问核心：相对路径 "." 解析到哪里 —— 那就是运行时基准目录。
// 核心未配置 fsRoot 时是运行时目录，配置了则是 fsRoot。
func (m *Manager) resolveRoot() (string, error) {
	raw, err := m.api.FSList(".")
	if err != nil {
		return "", err
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析 core.fs.list 响应失败: %w", err)
	}
	if out.Path == "" {
		return "", plugin.Errorf(plugin.CodeInternalError, "核心未返回基准目录")
	}
	return out.Path, nil
}

// Handle 分发 core.invoke。
func (m *Manager) Handle(service, method string, args json.RawMessage) (any, error) {
	if service == contribService {
		return m.handleContrib(method)
	}
	if service == panelService {
		return m.handlePanel(method, args)
	}
	if service != serviceName {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "插件管理器不提供服务 %s", service)
	}
	switch method {
	case "list":
		return m.list()
	case "inspect":
		return m.inspect(args)
	case "delete", "uninstall":
		return m.remove(args)
	case "enable":
		return m.setEnabled(args, true)
	case "disable":
		return m.setEnabled(args, false)
	case "setEnabled":
		return m.setEnabledArgs(args)
	case "create", "new", "scaffold":
		return m.create(args)
	case "purge":
		return m.purge()
	case "rules":
		return m.rules()
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%s 不支持方法 %s", service, method)
	}
}

// handleContrib 回答"我往哪些插槽放什么"。
//
// 条目只声明意图：slot / id / label / order / 页面目标。壳子决定几何与样式，
// 所以这里不出现任何尺寸、颜色或绘制代码。
func (m *Manager) handleContrib(method string) (any, error) {
	if method != "list" {
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%s 不支持方法 %s", contribService, method)
	}
	return map[string]any{
		"contributions": []map[string]any{
			{
				"slot": "header.action", "id": "plugins", "label": "插件",
				"icon": "⌘", "order": 20,
				"page": map[string]any{"native": "plugins"},
			},
		},
	}, nil
}

// Deactivate 清理。
func (m *Manager) Deactivate() error {
	if m.ctx != nil {
		m.api.Log("info", "插件管理器退出", nil)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 方法实现
// ---------------------------------------------------------------------------

// list 列出 plugins/ 下所有插件。
func (m *Manager) list() (any, error) {
	all, err := m.scan()
	if err != nil {
		return nil, err
	}
	items := make([]pluginInfo, 0, len(all))
	for _, it := range all {
		items = append(items, it.info)
	}
	return map[string]any{
		"root":      m.root,
		"count":     len(items),
		"plugins":   items,
		"self":      m.api.ID(),
		"protected": []string{m.api.ID()},
	}, nil
}

// inspect 查看单个插件，并给出“删掉它会动哪些文件”。
func (m *Manager) inspect(args json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if in.ID == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 id")
	}
	all, err := m.scan()
	if err != nil {
		return nil, err
	}
	target, ok := findPlugin(all, in.ID)
	if !ok {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "未找到插件 %q（用 %s list 查看）", in.ID, serviceName)
	}

	return map[string]any{
		"plugin":          target.info,
		"manifest":        target.man,
		"alive":           m.alive(target),
		"willRemove":      m.plan(target, false),
		"warnings":        dependents(all, target),
		"restartRequired": true,
	}, nil
}

// remove 删除一个插件：目录 + 配置 + 日志，并记一条待重启变更。
// providesService 判断一个插件的 manifest 是否声明了某个服务。
func providesService(man manifestFile, svc string) bool {
	for _, s := range man.Provides {
		if s == svc {
			return true
		}
	}
	return false
}

func (m *Manager) remove(args json.RawMessage) (any, error) {
	var in deleteArgs
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if in.ID == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 id")
	}

	all, err := m.scan()
	if err != nil {
		return nil, err
	}
	target, ok := findPluginBy(all, in.ID, in.Dir)
	if !ok {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "未找到插件 %q（用 %s list 查看）", in.ID, serviceName)
	}
	if target.info.Self {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"不能删除插件管理器自己（%s）：删掉之后就没有东西能装回它了", target.info.ID)
	}
	if target.man.ID == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"插件目录 %s 没有可读的 manifest.json，无法确定归属，拒绝删除", target.info.Dir)
	}
	if !validID(target.man.ID) {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"插件 ID %q 含有路径字符，拒绝据此删除文件", target.man.ID)
	}

	plan := m.plan(target, in.KeepLogs)
	warns := dependents(all, target)

	if in.DryRun {
		return map[string]any{
			"dryRun":          true,
			"pluginId":        target.man.ID,
			"dir":             target.info.Dir,
			"willRemove":      plan,
			"warnings":        warns,
			"restartRequired": true,
		}, nil
	}

	// 先自检权限：没有 filesystem:delete 就别动，省得每一条都撞 -32002
	if !m.hasPerm("filesystem:delete") {
		return nil, plugin.Errorf(plugin.CodePermissionDenied,
			"插件管理器缺少 filesystem:delete 权限，无法删除插件")
	}

	// 先热卸载：停掉它的进程（如果它在运行），目录就能当场删掉，不必等重启。
	//
	// 这是规范 2.6"重启生效"的例外（卸载比加载安全）：卸载成功，下面删目录就能
	// 一次删干净、不用写 pending；卸载失败（进程赖着不走 / 没在运行）就照旧走
	// 下面的删文件 + 标记 pending 的兜底。热卸载失败不挡住删除。
	unloaded := false
	switch {
	case providesService(target.man, uiServiceName):
		// 界面插件不热卸载：杀掉它用户就看不到界面了
		m.api.Log("info", "界面插件跳过热卸载，退回重启删除：杀掉它用户连结果都看不到",
			map[string]any{"pluginId": target.man.ID})
	default:
		res, err := m.api.Capability("core.unload", map[string]any{"id": target.man.ID})
		if err != nil {
			m.api.Log("warn", "热卸载失败，退回重启删除："+err.Error(), map[string]any{"pluginId": target.man.ID})
			break
		}
		var u struct {
			Stopped bool `json:"stopped"`
		}
		if json.Unmarshal(res, &u) == nil {
			unloaded = u.Stopped
		}
	}

	removed := make([]deleteResult, 0, len(plan))
	for i, p := range plan {
		res, _ := m.deletePath(p, i == 0) // 第 0 项是插件目录，需要递归
		removed = append(removed, res)
	}

	// 复核一遍：还剩下的（删不掉的）会在下次启动时被核心清掉
	deferred := make([]string, 0, len(plan))
	for _, p := range plan {
		if m.exists(p) {
			deferred = append(deferred, p)
		}
	}

	// 规范 2.6：只有删不干净的才记待重启变更。核心在下次启动、插件还没起来的
	// 时候执行删除 —— 那时没有进程占用，能删干净。
	//
	// 全删干净了就不记：否则 pending-restart.json 里留一条"下次启动白删一遍"
	// 的记录，是垃圾。
	var errs []string
	marked := false
	if len(deferred) > 0 {
		if err := m.markPending("delete", target.man.ID, target.info.Dir); err != nil {
			errs = append(errs, "写入 pending-restart.json 失败: "+err.Error())
		} else {
			marked = true
		}
	}

	status := "removed"
	for _, p := range deferred {
		if p == target.info.DirPath {
			status = "deferred" // 目录还在：多半是它正在运行，文件被占用
		}
	}
	restartRequired := len(deferred) > 0
	note := "插件目录已从 plugins/ 移除；仍在占用的文件（如日志）会在下次启动时清干净。重启后生效。"
	switch {
	case unloaded && !restartRequired:
		note = "已当场卸载并删除，不用重启。"
	case status == "deferred":
		note = "目标正在运行，文件被占用，已标记删除：下次启动时核心会在加载插件前把它清除。"
	}

	_ = m.api.Emit("plugin.deleted", map[string]any{
		"pluginId": target.man.ID,
		"dir":      target.info.Dir,
		"root":     m.root,
	})
	m.api.Log("info", "已删除插件", map[string]any{
		"pluginId": target.man.ID,
		"dir":      target.info.Dir,
		"warnings": len(warns),
	})

	return map[string]any{
		"pluginId":        target.man.ID,
		"dir":             target.info.Dir,
		"status":          status,
		"removed":         removed,
		"deferred":        deferred,
		"warnings":        warns,
		"errors":          errs,
		"restartRequired": restartRequired,
		"pendingRestart":  marked,
		"note":            note,
	}, nil
}

// setEnabledArgs 是 setEnabled 的入参形式：{"id":"...","enabled":false}。
func (m *Manager) setEnabledArgs(args json.RawMessage) (any, error) {
	var in struct {
		ID      string `json:"id"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if in.ID == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 id")
	}
	if in.Enabled == nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 enabled（true 启用 / false 禁用）")
	}
	return m.setEnabled(args, *in.Enabled)
}

// setEnabled 启用 / 禁用一个插件：改目录名，不加也不减任何文件。
//
// 核心只扫描 *.plugin 结尾的目录，所以加/去 .disabled 后缀就等于启停开关。
// 和删除一样是**重启生效**：当前这一轮里，运行中的插件不会因为被禁用而立刻消失，
// 已经被加载的也不会因为被启用而立刻出现。
func (m *Manager) setEnabled(args json.RawMessage, enable bool) (any, error) {
	var in struct {
		ID  string `json:"id"`
		Dir string `json:"dir"` // 目录名；ID 有重复时靠它认准是哪一份
	}
	_ = decode(args, &in)
	if in.ID == "" && in.Dir == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 id")
	}

	all, err := m.scan()
	if err != nil {
		return nil, err
	}
	target, ok := findPluginBy(all, in.ID, in.Dir)
	if !ok {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "未找到插件 %q（用 %s list 查看）", in.ID, serviceName)
	}
	if target.info.Self {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"不能禁用插件管理器自己（%s）：禁掉之后就没有东西能把它启用回来了", target.info.ID)
	}
	if target.info.Unreadable {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"插件目录 %s 没有可读的 manifest.json，拒绝启停", target.info.Dir)
	}

	if target.info.Disabled == !enable {
		return map[string]any{
			"pluginId":        target.info.ID,
			"dir":             target.info.Dir,
			"enabled":         enable,
			"changed":         false,
			"restartRequired": false,
			"note":            "已经是这个状态了，无需改动",
		}, nil
	}

	from := target.info.DirPath
	to := filepath.Join(m.root, pluginsDir, toggleDisabledName(target.info.Dir, enable))

	action := "disable"
	if enable {
		action = "enable"
	}

	// 目录可能同时存在 x.plugin 与 x.plugin.disabled（停用后又被构建脚本重新生成）。
	// 以启用中的那份为准：否则改名会因“目标已存在”失败，表现就是插件永远停不下来。
	if enable && m.exists(to) {
		if _, derr := m.deletePath(from, true); derr != nil {
			return nil, plugin.Errorf(plugin.CodeInternalError, "清理重复的停用副本失败：%v", derr)
		}
		m.api.Log("info", "已启用插件（清理重复停用副本）", map[string]any{
			"pluginId": target.info.ID,
			"dir":      filepath.Base(to),
		})
		return map[string]any{
			"pluginId":        target.info.ID,
			"dir":             filepath.Base(to),
			"enabled":         true,
			"changed":         true,
			"restartRequired": false,
			"note":            "已启用：目标目录已存在，已清理重复的停用副本",
		}, nil
	}
	if !enable && m.exists(to) {
		if _, derr := m.deletePath(to, true); derr != nil {
			return nil, plugin.Errorf(plugin.CodeInternalError, "清理旧的停用副本失败：%v", derr)
		}
	}

	// 禁用前先热卸载：Windows 会锁住运行中插件的目录，不停进程连改名都会被拒。
	// 卸载失败不阻断流程——后面改名失败仍会退回"标记待重启"。
	unloaded := false
	if !enable {
		if raw, uerr := m.api.Capability("core.unload", map[string]any{"id": target.info.ID}); uerr == nil {
			var res struct {
				Stopped bool `json:"stopped"`
			}
			_ = decode(raw, &res)
			unloaded = res.Stopped
		}
	}

	if _, err := m.api.FSMove(from, to); err != nil {
		// 刚才若已卸载成功，尽量把插件拉回来，避免"进程停了但界面还显示已启用"。
		if unloaded {
			_, _ = m.api.Capability("core.load", map[string]any{"id": target.info.ID})
		}
		// 和删不掉是同一个原因：插件正在运行，目录被占用，连改名都会被拒。
		// 记一条待重启变更 —— 核心启动时还没有任何插件在跑，改名一定成功。
		if perr := m.markPending(action, target.info.ID, target.info.Dir); perr != nil {
			return nil, plugin.Errorf(plugin.CodeInternalError,
				"切换失败（%v），且无法记录待重启变更：%v", err, perr)
		}
		note := "目标正在运行，目录被占用；已标记" + map[bool]string{true: "启用", false: "禁用"}[enable] +
			"，下次启动时生效"
		return map[string]any{
			"pluginId":        target.info.ID,
			"dir":             target.info.Dir,
			"enabled":         enable,
			"changed":         false,
			"deferred":        true,
			"restartRequired": true,
			"note":            note,
		}, nil
	}

	_ = m.api.Emit("plugin.state", map[string]any{
		"pluginId": target.info.ID,
		"enabled":  enable,
		"dir":      filepath.Base(to),
	})
	m.api.Log("info", "已切换插件启用状态", map[string]any{
		"pluginId": target.info.ID,
		"enabled":  enable,
		"dir":      filepath.Base(to),
	})

	// 热生效：禁用已在改名前提早卸载；启用则在改名后当场加载。
	// 热加载失败（比如依赖服务还没起来）就退回"重启后生效"，不隐藏问题。
	restartRequired := false
	note := "已禁用：已热卸载，服务当场注销，无需重启"
	if !unloaded {
		note = "已禁用：插件未在运行，无需重启"
	}
	if enable {
		if _, lerr := m.api.Capability("core.load", map[string]any{"id": target.info.ID}); lerr == nil {
			note = "已启用：服务已当场注册，无需重启"
		} else {
			restartRequired = true
			note = "已启用，但热加载未成功（" + lerr.Error() + "），重启后生效"
		}
	}
	return map[string]any{
		"pluginId":        target.info.ID,
		"dir":             filepath.Base(to),
		"enabled":         enable,
		"changed":         true,
		"restartRequired": restartRequired,
		"note":            note,
	}, nil
}

// purge 手动清空 pending-delete/（核心在退出与启动时也会各清理一次）。
func (m *Manager) purge() (any, error) {
	dir := filepath.Join(m.root, pendingDir)
	entries, err := m.listDir(dir)
	if err != nil {
		// 目录不存在视为已清空
		return map[string]any{"purged": 0, "failed": []string{}, "note": "pending-delete/ 为空或不存在"}, nil
	}
	var failed []string
	purged := 0
	for _, e := range entries {
		p := filepath.Join(dir, e.Name)
		if _, err := m.deletePath(p, e.Dir); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", e.Name, err))
			continue
		}
		purged++
	}
	return map[string]any{"purged": purged, "failed": failed}, nil
}

// ---------------------------------------------------------------------------
// 内部：扫描、定位、删除
// ---------------------------------------------------------------------------

// scan 读取 plugins/ 下所有插件目录的 manifest。
func (m *Manager) scan() ([]installed, error) {
	entries, err := m.listDir(filepath.Join(m.root, pluginsDir))
	if err != nil {
		return nil, err
	}
	out := make([]installed, 0, len(entries))
	for _, e := range entries {
		if !e.Dir {
			continue
		}
		// 核心只加载 *.plugin，被禁用的目录多一个 .disabled 后缀。
		// 两种都要列出来，否则一禁用插件就从清单里消失了，没法再启用回来。
		disabled := strings.HasSuffix(e.Name, disabledSuffix)
		base := strings.TrimSuffix(e.Name, disabledSuffix)
		if !strings.HasSuffix(strings.ToLower(base), ".plugin") {
			continue
		}
		dirPath := filepath.Join(m.root, pluginsDir, e.Name)
		it := installed{info: pluginInfo{Dir: e.Name, DirPath: dirPath, Disabled: disabled}}

		var man manifestFile
		if err := m.readJSON(filepath.Join(dirPath, "manifest.json"), &man); err != nil {
			// manifest 缺失或损坏：仍然列出来，但不允许按 ID 删除
			it.info.Unreadable = true
			it.info.Name = e.Name
			it.info.Error = err.Error()
			out = append(out, it)
			continue
		}
		it.man = man
		it.info.ID = man.ID
		it.info.Name = man.Name
		it.info.Version = man.Version
		it.info.Runtime = man.Runtime
		it.info.Entry = man.Entry
		it.info.Provides = man.Provides
		it.info.Consumes = man.Consumes
		it.info.Permissions = man.Permissions
		it.info.Compat = man.Compat["core"]
		it.info.Description = man.Description
		it.info.Self = man.ID != "" && man.ID == m.api.ID()
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		// 读不出 manifest 的排最后：它们只能看一眼、什么都动不了，
		// 摆在最前面会让每次打开这一页都先撞上一段错误说明。
		if out[i].info.Unreadable != out[j].info.Unreadable {
			return !out[i].info.Unreadable
		}
		if out[i].info.ID != out[j].info.ID {
			return out[i].info.ID < out[j].info.ID
		}
		return out[i].info.Dir < out[j].info.Dir
	})
	return out, nil
}

// plan 列出删除一个插件会动到的路径。第 0 项固定是插件目录。
func (m *Manager) plan(target installed, keepLogs bool) []string {
	plan := []string{target.info.DirPath}
	plan = append(plan, filepath.Join(m.root, configsDir, target.man.ID+".json"))
	if !keepLogs {
		plan = append(plan, filepath.Join(m.root, logsDir, target.man.ID+".log"))
	}
	return plan
}

// alive 用 core.consume 探测插件是否有服务正在提供（说明它当前在运行）。
func (m *Manager) alive(target installed) bool {
	for _, svc := range target.man.Provides {
		if id, err := m.api.Consume(svc); err == nil && id != "" {
			return true
		}
	}
	return false
}

// deletePath 经核心删除一个路径。
func (m *Manager) deletePath(path string, recursive bool) (deleteResult, error) {
	raw, err := m.api.FSDelete(path, recursive)
	if err != nil {
		return deleteResult{Path: path, Error: err.Error()}, err
	}
	var res deleteResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return deleteResult{Path: path, Error: err.Error()}, err
	}
	return res, nil
}

// exists 判断路径是否还在。查不到父目录时保守地认为“还在”。
func (m *Manager) exists(path string) bool {
	entries, err := m.listDir(filepath.Dir(path))
	if err != nil {
		return true
	}
	want := filepath.Base(path)
	for _, e := range entries {
		if e.Name == want {
			return true
		}
	}
	return false
}

// hasPerm 检查本插件是否被授予了某个权限。
func (m *Manager) hasPerm(perm string) bool { return m.api.Can(perm) }

// markPending 往 pending-restart.json 追加一条待重启变更（delete / enable / disable）。
//
// 运行时改不动的（文件被占用）都靠它：核心在下次启动、加载任何插件之前执行，
// 那时没有进程占用，一定改得动。
func (m *Manager) markPending(action, id, dir string) error {
	path := filepath.Join(m.root, pendingFile)
	var list []pendingChange
	// 文件不存在或内容损坏都从空列表开始：记录只是可追溯性，失败不该挡住删除
	_ = m.readJSON(path, &list)

	// 去重：同一个动作（同一 id + 目录）不重复记 —— 重复删除同一插件时，
	// 记录会越堆越多，重启时核心白删一遍。
	for _, c := range list {
		if c.Action == action && c.PluginID == id && c.Dir == dir {
			return nil
		}
	}

	list = append(list, pendingChange{
		Action:   action,
		PluginID: id,
		Dir:      dir,
		By:       m.api.ID(), // 核心启动时会核对此插件是否真有 filesystem:delete
		At:       time.Now().UTC().Format(time.RFC3339),
	})
	blob, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	_, err = m.api.FSWrite(path, string(blob)+"\n", false, true)
	return err
}

// ---------------------------------------------------------------------------
// 内部：能力代理的小包装
// ---------------------------------------------------------------------------

func (m *Manager) listDir(path string) ([]entry, error) {
	raw, err := m.api.FSList(path)
	if err != nil {
		return nil, err
	}
	var out struct {
		Path    string  `json:"path"`
		Entries []entry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 core.fs.list 响应失败: %w", err)
	}
	return out.Entries, nil
}

// readJSON 读文件并把 content 字段反序列化到 v。
func (m *Manager) readJSON(path string, v any) error {
	raw, err := m.api.FSRead(path)
	if err != nil {
		return err
	}
	var wrapper struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return fmt.Errorf("解析 %s 响应失败: %w", path, err)
	}
	if err := json.Unmarshal([]byte(wrapper.Content), v); err != nil {
		return fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 创建插件：平台自己造插件的入口
// ---------------------------------------------------------------------------

// createParams 是 create 的入参。
type createParams struct {
	ID          string     `json:"id"`   // 反向域名，例如 com.ai.calc
	Name        string     `json:"name"` // 人类可读的名字
	Description string     `json:"description"`
	Version     string     `json:"version"` // 默认 1.0.0
	Runtime     string     `json:"runtime"` // python / go（go 只写源码，需 builder 编译）
	Entry       string     `json:"entry"`   // 默认按 runtime 推导
	Provides    []string   `json:"provides"`
	Consumes    []string   `json:"consumes"`
	Permissions []string   `json:"permissions"`
	Code        string     `json:"code"`  // 入口文件内容；留空则生成骨架
	Tools       []toolSpec `json:"tools"` // 要注册成 AI 工具的方法
	Force       bool       `json:"force"` // 已存在同名插件时覆盖
}

// toolSpec 描述一个要注册成 AI 工具的方法。
type toolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Method      string `json:"method"`
	Schema      any    `json:"schema"`
}

// idPattern 与核心 loader 保持一致：反向域名格式。
var idPattern = regexp.MustCompile(
	`^[A-Za-z0-9]([A-Za-z0-9_-]*)(\.[A-Za-z0-9]([A-Za-z0-9_-]*))+$`)

// permissionPattern 与核心保持一致：小写、冒号分隔。
var permissionPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(:[a-z][a-z0-9]*)*$`)

// create 新建一个插件目录：写入口文件 + 按规范生成 manifest。
//
// 这是"让 AI 给自己造工具"的关键一步。AI 只管给出 id 和代码，
// 而**格式**由这里保证：目录结构、manifest 的必填字段与取值、版本范围，
// 全部按核心的校验规则生成 —— 否则 AI 随手写一个 manifest 很容易被核心跳过加载。
//
// 返回值里 nextStep 会说清楚接下来该干什么（python 直接重启；go 要先编译）。
func (m *Manager) create(args json.RawMessage) (any, error) {
	var in createParams
	if err := decode(args, &in); err != nil {
		return nil, err
	}

	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"缺少 id（必须是反向域名，例如 com.ai.calc）")
	}
	if !idPattern.MatchString(in.ID) {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"id 必须是反向域名格式（至少两段），例如 com.ai.calc，当前 %q", in.ID)
	}

	runtime := strings.ToLower(strings.TrimSpace(in.Runtime))
	if runtime == "" {
		runtime = "python"
	}
	// 核心只认这几种 runtime；go 是"写 Go 源码"的简写，最终产物仍是 binary
	switch runtime {
	case "python", "go", "binary":
	case "node", "java", "wasm":
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"%s 暂时不支持在 create 里生成（需要对应工具链与打包方式）", runtime)
	default:
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"runtime 必须是 python / go / binary，当前 %q", runtime)
	}

	if in.Version == "" {
		in.Version = "1.0.0"
	}
	version, err := ParseVersion(in.Version)
	if err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "version 不是合法版本号: %v", err)
	}
	for _, p := range in.Permissions {
		if !permissionPattern.MatchString(p) {
			return nil, plugin.Errorf(plugin.CodeInvalidParams,
				"权限名必须是小写冒号分隔，例如 filesystem:read，当前 %q", p)
		}
	}
	for _, s := range in.Provides {
		if !strings.Contains(s, ".v") {
			return nil, plugin.Errorf(plugin.CodeInvalidParams,
				"服务名必须带版本后缀，例如 calc.v1，当前 %q", s)
		}
	}

	dir := pluginDirName(in.ID)
	base := filepath.Join(m.root, pluginsDir, dir)

	// 已存在就别覆盖 —— 除非显式 force
	if m.exists(base) && !in.Force {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"插件目录 %s 已存在；要覆盖请传 force:true，或换个 id", dir)
	}

	entry := strings.TrimSpace(in.Entry)
	manifest := map[string]any{}
	files := map[string]string{}

	switch runtime {
	case "python":
		if entry == "" {
			entry = "main.py"
		}
		manifest["runtime"] = "python"
		code := in.Code
		if strings.TrimSpace(code) == "" {
			code = pythonSkeleton(in.ID, in.Name, in.Provides)
		}
		files[entry] = code
	case "go":
		if entry == "" {
			entry = "main"
		}
		// Go 源码编译后是原生二进制：核心按 binary 直接执行 entry
		manifest["runtime"] = "binary"
		code := in.Code
		if strings.TrimSpace(code) == "" {
			code = goSkeleton(in.ID, in.Provides)
		}
		files["main.go"] = code
	case "binary":
		if entry == "" {
			entry = "main"
		}
		manifest["runtime"] = "binary"
		if strings.TrimSpace(in.Code) == "" {
			return nil, plugin.Errorf(plugin.CodeInvalidParams,
				"runtime=binary 必须提供 code（或改用 go，由 builder 编译）")
		}
		files[entry] = in.Code
	}

	// manifest 的必填字段一个都不能少，取值也必须落在核心允许的范围内
	manifest["id"] = in.ID
	manifest["name"] = firstNonEmpty(in.Name, in.ID)
	manifest["description"] = firstNonEmpty(in.Description, "由 pluginManager.v1 create 创建")
	manifest["version"] = version
	manifest["apiVersion"] = "1"
	manifest["entry"] = entry
	manifest["provides"] = in.Provides
	manifest["consumes"] = in.Consumes
	manifest["permissions"] = in.Permissions
	manifest["compat"] = map[string]string{"core": ">=1.0 <2.0"}

	blob, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "生成 manifest 失败: %v", err)
	}
	files["manifest.json"] = string(blob) + "\n"

	written := make([]string, 0, len(files))
	if _, err := m.api.FSMkdir(base, true); err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "创建插件目录失败: %v", err)
	}
	for _, name := range []string{"manifest.json", entry, "main.go"} {
		content, ok := files[name]
		if !ok {
			continue
		}
		if _, err := m.api.FSWrite(filepath.Join(base, name), content, false, true); err != nil {
			return nil, err
		}
		written = append(written, name)
	}

	// 顺手把方法注册成 AI 的工具：新插件不然只是"能调用"，模型看不见
	registered := m.registerTools(in)

	next := "重启核心后自动加载（所有变更重启生效）"
	if runtime == "go" {
		next = "源码已生成，但还没编译：先调用 builder.v1 build {\"dir\":\"" + dir +
			"\"} 编译出可执行文件，再重启核心"
	}

	return map[string]any{
		"id":              in.ID,
		"dir":             dir,
		"path":            base,
		"runtime":         manifest["runtime"],
		"entry":           entry,
		"files":           written,
		"toolsRegistered": registered,
		"restartRequired": true,
		"nextStep":        next,
	}, nil
}

// registerTools 把插件的方法注册进 AI 的工具目录。
//
// toolExecutor 没装就算了：工具目录不是插件能用与否的前提，
// 不能因为注册失败就让整个 create 失败。
func (m *Manager) registerTools(in createParams) []string {
	if len(in.Tools) == 0 || len(in.Provides) == 0 {
		return nil
	}
	if _, err := m.api.Consume("toolExecutor.v1"); err != nil {
		return nil
	}
	service := in.Provides[0]
	var done []string
	for _, t := range in.Tools {
		name := firstNonEmpty(t.Name, t.Method)
		method := firstNonEmpty(t.Method, "run")
		schema := t.Schema
		if schema == nil {
			schema = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		params := map[string]any{
			"name":        name,
			"description": firstNonEmpty(t.Description, in.Name+" 的 "+method+" 方法"),
			"service":     service,
			"method":      method,
			"schema":      schema,
		}
		if _, err := m.api.Invoke("toolExecutor.v1", "register", params); err != nil {
			m.api.Log("warn", "注册 AI 工具失败", map[string]any{"tool": name, "err": err.Error()})
			continue
		}
		done = append(done, name)
	}
	return done
}

// ---------------------------------------------------------------------------
// 骨架：create 没给 code 时生成的可用最小插件
// ---------------------------------------------------------------------------

// pythonSkeleton 生成一个自包含的 Python 插件骨架。
//
// 刻意不依赖 sdk/python/aiwork_plugin.py：插件目录要自包含，而 create 时
// 未必读得到源码树里的 SDK。协议就那几行，自己实现更稳。
func pythonSkeleton(id string, name string, provides []string) string {
	service := "unnamed.v1"
	if len(provides) > 0 {
		service = provides[0]
	}
	return fmt.Sprintf(`#!/usr/bin/env python3
"""%s —— 由 pluginManager.v1 create 生成。

这是一个自包含的插件骨架：不用装任何依赖，自己按核心的
JSON-RPC over stdio 协议说话。只需要改 handle() 里的逻辑。
"""

import json
import queue
import sys
import threading

SERVICE = %q
DESCRIPTION = %q

_send_lock = threading.Lock()
_inbox = queue.Queue()
_pending = {}
_next_id = [1]


def send(obj):
    with _send_lock:
        sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
        sys.stdout.flush()


def call(method, params):
    """发一个出站请求并等响应（例如 core.provide / core.fs.read）。"""
    rid = _next_id[0]
    _next_id[0] += 1
    slot = queue.Queue(maxsize=1)
    _pending[str(rid)] = slot
    send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
    try:
        resp = slot.get(timeout=10)
    except queue.Empty:
        raise RuntimeError(method + " 超时")
    if "error" in resp:
        raise RuntimeError(str(resp["error"]))
    return resp.get("result")


def _reader():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except Exception:
            continue
        if "method" in msg and "id" in msg:
            _inbox.put(msg)
        elif "id" in msg:
            slot = _pending.pop(str(msg.get("id")), None)
            if slot is not None:
                slot.put(msg)
    _inbox.put(None)


def handle(method, args):
    """AI：在这里实现你自己的逻辑。args 是一个 dict。"""
    if method == "run":
        return {"ok": True, "service": SERVICE, "echo": args}
    if method == "ping":
        return {"pong": True, "service": SERVICE}
    raise ValueError(SERVICE + " 不支持方法：" + str(method))


def main():
    threading.Thread(target=_reader, daemon=True).start()
    while True:
        msg = _inbox.get()
        if msg is None:
            return
        mid = msg.get("id")
        method = msg.get("method")
        params = msg.get("params") or {}
        try:
            if method == "core.init":
                call("core.provide", {"name": SERVICE, "meta": {"description": DESCRIPTION}})
                send({"jsonrpc": "2.0", "id": mid, "result": {"ready": True}})
            elif method == "core.invoke":
                result = handle(params.get("method"), params.get("args") or {})
                send({"jsonrpc": "2.0", "id": mid, "result": result})
            elif method == "core.shutdown":
                send({"jsonrpc": "2.0", "id": mid, "result": {"ok": True}})
                return
            else:
                send({"jsonrpc": "2.0", "id": mid,
                      "error": {"code": -32601, "message": "不支持的方法 " + str(method)}})
        except Exception as exc:
            send({"jsonrpc": "2.0", "id": mid,
                  "error": {"code": -32000, "message": str(exc)}})


if __name__ == "__main__":
    for stream in (sys.stdin, sys.stdout):
        try:
            stream.reconfigure(encoding="utf-8")
        except Exception:
            pass
    main()
`, firstNonEmpty(name, id), service, firstNonEmpty(name, id))
}

// goSkeleton 生成一个 Go 插件骨架（用官方 SDK，需 builder 编译）。
func goSkeleton(id string, provides []string) string {
	service := "unnamed.v1"
	if len(provides) > 0 {
		service = provides[0]
	}
	return fmt.Sprintf(`// %s —— 由 pluginManager.v1 create 生成。
//
// 用官方 SDK 写的插件骨架。需要先编译：builder.v1 build，然后重启核心加载。
package main

import (
	"encoding/json"

	"aiwork/sdk/go/plugin"
)

const serviceName = %q

type Plugin struct{ ctx *plugin.Context }

func (p *Plugin) Activate(ctx *plugin.Context) error {
	p.ctx = ctx
	return ctx.Provide(serviceName, map[string]any{
		"description": "由 pluginManager.v1 create 生成的插件",
		"methods":     []string{"run", "ping"},
	})
}

func (p *Plugin) Handle(service, method string, args json.RawMessage) (any, error) {
	if service != serviceName {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "不提供服务 %%s", service)
	}
	var in map[string]any
	_ = json.Unmarshal(args, &in)
	switch method {
	case "ping":
		return map[string]any{"pong": true, "service": serviceName}, nil
	case "run":
		// AI：在这里实现你的逻辑
		return map[string]any{"ok": true, "args": in}, nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%%s 不支持方法 %%s", service, method)
	}
}

func (p *Plugin) Deactivate() error { return nil }

func main() {
	if err := plugin.Serve(&Plugin{}); err != nil {
		plugin.Logf("error", "插件退出: %%v", err)
		os.Exit(1)
	}
}
`, id, service)
}

// ---------------------------------------------------------------------------
// 纯函数（便于测试）
// ---------------------------------------------------------------------------

// firstNonEmpty 取第一个非空字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// pluginDirName 由插件 ID 推出目录名：com.ai.calc -> calc.plugin
func pluginDirName(id string) string {
	parts := strings.Split(id, ".")
	last := parts[len(parts)-1]
	if last == "" {
		last = "plugin"
	}
	return last + ".plugin"
}

// ParseVersion 校验并规范化版本号，规则与核心 loader 完全一致。
//
// 刻意照抄核心的宽松规则（v 前缀、1.0 两段、预发布后缀都认）：create 校验通过的版本，
// 核心加载时必须也认 —— 否则 AI 传个 "v1.2" 就会建出一个永远加载不了的插件。
// 返回规范形式 "1.2.0"，写进 manifest 的就是它。
func ParseVersion(s string) (string, error) {
	raw := strings.TrimSpace(s)
	raw = strings.TrimPrefix(raw, "v")
	if raw == "" {
		return "", errors.New("版本号为空")
	}
	// 预发布与构建元数据不参与格式校验
	if i := strings.IndexAny(raw, "-+"); i >= 0 {
		raw = raw[:i]
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 3 {
		return "", fmt.Errorf("版本号格式非法: %s", s)
	}
	var nums [3]int
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return "", fmt.Errorf("版本号格式非法: %s", s)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return "", fmt.Errorf("版本号格式非法: %s", s)
		}
		nums[i] = n
	}
	return fmt.Sprintf("%d.%d.%d", nums[0], nums[1], nums[2]), nil
}

// findPlugin 按插件 ID 定位，找不到时再按目录名匹配。
func findPlugin(all []installed, target string) (installed, bool) {
	for _, it := range all {
		if it.man.ID != "" && it.man.ID == target {
			return it, true
		}
	}
	for _, it := range all {
		if it.info.Dir == target || strings.TrimSuffix(it.info.Dir, ".plugin") == target {
			return it, true
		}
	}
	return installed{}, false
}

// findPluginBy 定位一个插件：给了目录名就按目录名找，否则按 ID 找。
//
// 目录名优先是必须的：目录里可能同时躺着 x.plugin 和 x.plugin.disabled，
// 两份的 ID 一模一样，只按 ID 找永远命中同一份 —— 用户在界面上选中后一份、
// 点了删除，删掉的却是前一份。
func findPluginBy(all []installed, id, dir string) (installed, bool) {
	if dir != "" {
		for _, it := range all {
			if it.info.Dir == dir {
				return it, true
			}
		}
	}
	if id != "" {
		return findPlugin(all, id)
	}
	return installed{}, false
}

// toggleDisabledName 按目标状态算出目录名：禁用加后缀，启用去掉后缀。
func toggleDisabledName(dir string, enable bool) string {
	if enable {
		return strings.TrimSuffix(dir, disabledSuffix)
	}
	if strings.HasSuffix(dir, disabledSuffix) {
		return dir
	}
	return dir + disabledSuffix
}

// dependents 找出有多少别的插件会因为这个插件消失而受影响。
func dependents(all []installed, target installed) []string {
	var out []string
	for _, other := range all {
		if other.info.Dir == target.info.Dir {
			continue
		}
		label := other.man.ID
		if label == "" {
			label = other.info.Dir
		}
		for _, need := range other.man.Consumes {
			for _, give := range target.man.Provides {
				if give != "" && need == give {
					out = append(out, fmt.Sprintf("%s 正在消费服务 %s，删除后它将降级或报错", label, give))
				}
			}
		}
		if _, ok := other.man.Dependencies[target.man.ID]; ok && target.man.ID != "" {
			out = append(out, fmt.Sprintf("%s 在 dependencies 中声明了 %s，重启后可能因依赖缺失被跳过", label, target.man.ID))
		}
	}
	for _, give := range target.man.Provides {
		if give == "ui.v1" {
			out = append(out, "该插件提供 ui.v1：删除并重启后将没有界面，可用引导菜单装回 minimal-ui 或 aurora（或 workbench）")
		}
	}
	return out
}

// validID 拒绝会把路径拼到别处去的插件 ID。
func validID(id string) bool {
	if id == "" {
		return false
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") || strings.Contains(id, ":") {
		return false
	}
	return true
}

func decode(args json.RawMessage, v any) error {
	if len(args) == 0 || string(args) == "null" {
		return nil
	}
	if err := json.Unmarshal(args, v); err != nil {
		return plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	return nil
}

func main() {
	if err := plugin.Serve(&Manager{}); err != nil {
		plugin.Logf("error", "插件管理器退出: %v", err)
		os.Exit(1)
	}
}
