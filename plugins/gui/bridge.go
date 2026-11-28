// 数据桥：GUI 与核心之间的唯一通道。
//
// GUI 作为核心加载的插件进程运行，天然拥有 ctx.Invoke / ctx.On / ctx.Capability。
// 这里把 webui 里那套经过真机验证的对话逻辑搬过来，渲染层（Gio）只消费这里的
// 快照，不直接碰 JSON-RPC —— 界面归界面，数据归数据。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"aiwork/sdk/go/plugin"
)

const convService = "conversation.v1"

// 会话列表项。
type sessionInfo struct {
	ID      string
	Name    string
	Nodes   int
	Updated string
}

// 一条对话消息（对应消息树当前路径上的一个节点）。
type msgView struct {
	ID      string
	Role    string
	Text    string
	At      string
	Version int
	Total   int
	Via     string
	Err     string
	Trunc   bool
}

// 服务链路状态。
type serviceStatus struct {
	Name     string
	Alive    bool
	Provider string
	Note     string
}

// 一次过程追踪（模型思考 + 工具调用），用于对话区的"过程"折叠块。
type traceState struct {
	Round int
	Phase string
	Model string
	Think string
	Done  bool
	Steps []traceStep
}

type traceStep struct {
	Tool string
	Args string
	Ms   int64
	OK   bool
	Done bool
}

// snapshot 是渲染层一次性拿到全部数据的快照。
type snapshot struct {
	SessionID string
	Name      string
	Note      string
	Model     string
	// ModelList 是 llm.v1 返回的可选模型名（输入区模型菜单用）。
	ModelList []string
	// ModelNote 是拉取模型清单失败时的原因，菜单里给用户一句提示，
	// 否则"只有跟随当前配置"看起来像功能坏了。
	ModelNote string
	Sessions  []sessionInfo
	Path      []msgView
	Services  []serviceStatus
	Plugins   []pluginInfo
	Trace     traceState
	Thinking  bool

	// 各功能页数据（按需加载）。
	ModelCfg  modelConfigView
	Roles     rolesView
	Budget    budgetView
	ImageGen  imagegenView
	PluginMgr pluginMgrView
	Workspace workspaceView
	Usage     usageView

	// 插件 UI 贡献（DSH 式 slot）：由 ui.contrib.v1 汇总得到。
	// 壳子只认插槽，不认插件；条目自带 id / order / label 与页面目标。
	Contribs       []contribution
	ContribsLoaded bool
	ContribsRev    int
}

type pluginInfo struct {
	ID      string
	Name    string
	Version string
	Alive   bool
}

// bridge 封装所有对核心的调用。线程安全：Gio 在 UI goroutine 读快照，
// JSON-RPC 事件回调在别的 goroutine 写快照。
type bridge struct {
	ctx *plugin.Context

	mu            sync.RWMutex
	snap          snapshot
	onChange      func() // 数据变化时通知 UI 重绘（由 Gio 的 invalidate 注入）
	onPluginState func() // 插件状态变化时通知 UI 补拉当前页（由 UI 注入）

	// pendingAttach 是后台线程选完、读好的附件，等 UI 线程取走后挂到输入框上。
	// 文件对话框与读文件都在后台线程做，界面线程不能等它们。
	pendingAttach []attachFile
}

// maxAttachments 是单条消息最多能带的附件数（chip 行一行放得下的数量）。
const maxAttachments = 4

// attachFile 是一个待发送的附件（形状与 conversation.v1 / agent.v1 一致）。
type attachFile struct {
	Type    string `json:"type"`              // text / image / file
	Name    string `json:"name,omitempty"`    // 文件名
	Text    string `json:"text,omitempty"`    // type=text：文件内容
	DataURL string `json:"dataUrl,omitempty"` // type=image：data:image/png;base64,xxx
	Size    int64  `json:"size,omitempty"`
	// Path 只用于本机展示（chip 的 tooltip / 去重），不发给后端。
	Path string `json:"-"`
}

func newBridge(ctx *plugin.Context) *bridge {
	return &bridge{ctx: ctx}
}

// setOnChange 注入"请求重绘"回调。
func (b *bridge) setOnChange(fn func()) { b.onChange = fn }

// setOnPluginState 注入"插件状态变化时补拉当前页"回调。
func (b *bridge) setOnPluginState(fn func()) { b.onPluginState = fn }

func (b *bridge) changed() {
	if b.onChange != nil {
		b.onChange()
	}
}

// get 返回当前快照的只读副本（值拷贝，切片共享但 UI 只读）。
func (b *bridge) get() snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.snap
}

// invalidateContribs 让插件 UI 贡献缓存失效：下一帧由 UI 线程重新汇总。
// 插件启停发生在后台线程，这里只动一个标志位，不在后台碰界面状态。
func (b *bridge) invalidateContribs() {
	b.mu.Lock()
	b.snap.ContribsLoaded = false
	b.mu.Unlock()
	b.changed()
}

// 订阅核心事件：对话完成 / 思考 / 工具调用 / 插件变化。
func (b *bridge) subscribe() {
	for _, name := range []string{
		"chat.message", "chat.response", "agent.think", "chat.tool.before",
		"chat.tool.after", "agent.done", "session.updated",
		"plugin.deleted", "plugin.state",
	} {
		ev := name
		b.ctx.On(ev, func(payload json.RawMessage) { b.onEvent(ev, payload) })
	}
}

// reprobe 重新探测服务链路、模型与插件清单。
//
// 插件是逐个激活的：GUI 往往比 session.v1 / llm.v1 更早起来，
// 启动时那一次 refresh 会把还没注册的服务全判成"未就绪"，
// 之后必须补探测，否则侧栏会一直停在红色的旧状态。
func (b *bridge) reprobe() {
	services := b.services()
	model := b.model()
	plugins := b.plugins()

	b.mu.Lock()
	b.snap.Services = services
	b.snap.Model = model
	b.snap.Plugins = plugins
	b.mu.Unlock()
	b.changed()
}

// refresh 重新拉取会话快照 + 服务链路 + 插件清单。
func (b *bridge) refresh() {
	b.mu.Lock()
	defer b.mu.Unlock()

	raw, err := b.conv("history", nil)
	if err != nil {
		b.snap.Note = "未安装对话插件（conversation / session / agent / llm）"
		b.snap.Services = b.services()
		b.changed()
		return
	}
	b.applyConv(raw)
	b.snap.Note = ""
	b.snap.Services = b.services()
	b.snap.Model = b.model()
	b.snap.Plugins = b.plugins()
	b.changed()
}

// ---------------------------------------------------------------
// 附件：选文件 → 按类型读内容 → 交给 UI 挂到输入框
// ---------------------------------------------------------------

// pickAttachments 弹文件选择框并读取内容（后台线程，对话框阻塞也不影响界面）。
func (b *bridge) pickAttachments(initial string) {
	b.async(func() {
		raw, err := b.ctx.Capability("core.shell.pickfiles", map[string]any{
			"title":   "选择要附加的文件",
			"initial": initial,
		})
		if err != nil {
			b.patch(func(s *snapshot) { s.Note = "选择文件失败: " + err.Error() })
			return
		}
		var out struct {
			Paths     []string `json:"paths"`
			Cancelled bool     `json:"cancelled"`
		}
		_ = json.Unmarshal(raw, &out)
		if out.Cancelled || len(out.Paths) == 0 {
			return
		}
		got := make([]attachFile, 0, len(out.Paths))
		for _, p := range out.Paths {
			if len(got) >= maxAttachments {
				break
			}
			att, err := b.readAttachment(p)
			if err != nil {
				b.patch(func(s *snapshot) { s.Note = "读取附件失败: " + err.Error() })
				continue
			}
			got = append(got, att)
		}
		if len(got) == 0 {
			return
		}
		b.mu.Lock()
		b.pendingAttach = append(b.pendingAttach, got...)
		b.mu.Unlock()
		b.changed()
	})
}

// drainAttachments 由 UI 线程取走后台读好的附件。
func (b *bridge) drainAttachments() []attachFile {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.pendingAttach
	b.pendingAttach = nil
	return out
}

// readAttachment 读一个文件，翻译成 message attachment。
//
// 规则与工作区里的附件一致：图片按 base64 让模型"看见"，文本读正文，
// 其余类型（视频、压缩包等）只带文件名和大小，模型至少知道有这个文件。
func (b *bridge) readAttachment(path string) (attachFile, error) {
	name := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		var out struct {
			Content   string `json:"content"`
			Size      int64  `json:"size"`
			Truncated bool   `json:"truncated"`
		}
		raw, err := b.ctx.Capability("core.fs.read", map[string]any{
			"path": path, "encoding": "base64", "limit": 2 << 20,
		})
		if err != nil {
			return attachFile{}, err
		}
		_ = json.Unmarshal(raw, &out)
		mime := "image/png"
		switch ext {
		case ".jpg", ".jpeg":
			mime = "image/jpeg"
		case ".gif":
			mime = "image/gif"
		case ".webp":
			mime = "image/webp"
		case ".bmp":
			mime = "image/bmp"
		}
		return attachFile{
			Type: "image", Name: name, Path: path, Size: out.Size,
			DataURL: "data:" + mime + ";base64," + out.Content,
		}, nil
	}
	if textExts[ext] {
		var out struct {
			Content   string `json:"content"`
			Size      int64  `json:"size"`
			Truncated bool   `json:"truncated"`
		}
		raw, err := b.ctx.Capability("core.fs.read", map[string]any{
			"path": path, "limit": 256 << 10,
		})
		if err != nil {
			return attachFile{}, err
		}
		_ = json.Unmarshal(raw, &out)
		text := out.Content
		if out.Truncated {
			text += "\n\n[文件超过 256KB，内容已截断]"
		}
		return attachFile{Type: "text", Name: name, Path: path, Size: out.Size, Text: text}, nil
	}
	var size int64
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	return attachFile{Type: "file", Name: name, Path: path, Size: size}, nil
}

// textExts 是"按纯文本读给模型"的扩展名白名单。
// 用白名单而不是"猜编码"：把二进制文件强行当文本读，模型只会看到一堆乱码。
var textExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".log": true, ".csv": true,
	".json": true, ".jsonl": true, ".yaml": true, ".yml": true, ".toml": true,
	".ini": true, ".conf": true, ".cfg": true, ".env": true, ".xml": true,
	".html": true, ".htm": true, ".css": true, ".scss": true,
	".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".vue": true,
	".py": true, ".go": true, ".rs": true, ".java": true, ".kt": true,
	".c": true, ".h": true, ".cpp": true, ".hpp": true, ".cs": true,
	".sh": true, ".ps1": true, ".bat": true, ".sql": true, ".mod": true,
}

// send 发一条消息（异步：调 send 会阻塞到模型回复完成，放到后台）。
func (b *bridge) send(text, mode, reasoning, permission string, atts []attachFile) {
	b.mu.RLock()
	sid := b.snap.SessionID
	b.mu.RUnlock()

	go func() {
		params := map[string]any{"sessionId": sid, "text": text}
		if mode != "" {
			params["mode"] = mode
		}
		if reasoning != "" {
			params["reasoningEffort"] = reasoning
		}
		if permission != "" {
			params["permission"] = permission
		}
		if len(atts) > 0 {
			params["attachments"] = atts
		}
		_, _ = b.conv("send", params)
		b.refresh()
	}()

	b.mu.Lock()
	b.snap.Path = append(b.snap.Path, msgView{Role: "user", Text: text})
	b.snap.Thinking = true
	b.snap.Trace = traceState{}
	b.changed()
	b.mu.Unlock()
}

// newSession / switchSession / deleteSession 会话管理。
func (b *bridge) newSession() {
	go func() { _, _ = b.conv("newSession", nil); b.refresh() }()
}
func (b *bridge) switchSession(id string) {
	go func() { _, _ = b.conv("switch", map[string]any{"sessionId": id}); b.refresh() }()
}
func (b *bridge) deleteSession(id string) {
	go func() { _, _ = b.conv("deleteSession", map[string]any{"sessionId": id}); b.refresh() }()
}

// conv 调一次 conversation.v1。
func (b *bridge) conv(method string, params map[string]any) (json.RawMessage, error) {
	if _, err := b.ctx.Consume(convService); err != nil {
		return nil, fmt.Errorf("未安装对话插件（%s）", convService)
	}
	if params == nil {
		params = map[string]any{}
	}
	return b.ctx.Invoke(convService, method, params)
}

// applyConv 把 history 返回解析进快照（结构与 webui 一致）。
func (b *bridge) applyConv(raw json.RawMessage) {
	var out struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
		Note      string `json:"note"`
		Err       string `json:"error"`
		Sessions  []struct {
			SessionID string `json:"sessionId"`
			Name      string `json:"name"`
			Nodes     int    `json:"nodes"`
			Updated   string `json:"updated"`
		} `json:"sessions"`
		Path []struct {
			ID      string `json:"id"`
			Role    string `json:"role"`
			Text    string `json:"text"`
			At      string `json:"at"`
			Version int    `json:"version"`
			Total   int    `json:"total"`
			Via     string `json:"via"`
			Err     string `json:"err"`
			Trunc   bool   `json:"trunc"`
		} `json:"path"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return
	}
	b.snap.SessionID = out.SessionID
	b.snap.Name = out.Name
	if out.Note != "" {
		b.snap.Note = out.Note
	}
	if out.Err != "" {
		b.snap.Note = out.Err
	}
	b.snap.Sessions = nil
	for _, it := range out.Sessions {
		b.snap.Sessions = append(b.snap.Sessions, sessionInfo{
			ID: it.SessionID, Name: it.Name, Nodes: it.Nodes, Updated: it.Updated,
		})
	}
	b.snap.Path = nil
	for _, it := range out.Path {
		b.snap.Path = append(b.snap.Path, msgView{
			ID: it.ID, Role: it.Role, Text: it.Text, At: it.At,
			Version: it.Version, Total: it.Total, Via: it.Via, Err: it.Err, Trunc: it.Trunc,
		})
	}
	b.snap.Thinking = false
}

// services 探测服务链路是否活着。
func (b *bridge) services() []serviceStatus {
	probes := []string{
		"workspace.v1", "session.v1", "agent.v1", "llm.v1",
		"toolExecutor.v1", "conversation.v1", "pluginManager.v1", "usage.v1",
	}
	out := make([]serviceStatus, 0, len(probes))
	for _, name := range probes {
		pid, err := b.ctx.Consume(name)
		if err != nil {
			out = append(out, serviceStatus{Name: name, Alive: false, Note: reasonOf(err)})
		} else {
			out = append(out, serviceStatus{Name: name, Alive: true, Provider: pid})
		}
	}
	return out
}

// model 返回当前模型名。
func (b *bridge) model() string {
	raw, err := b.ctx.Invoke("llm.v1", "ping", map[string]any{})
	if err != nil {
		return ""
	}
	var out struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	if out.Model == "" || out.Model == out.Provider {
		return out.Provider
	}
	return out.Provider + " · " + out.Model
}

// loadModels 拉当前提供商的可用模型清单，供输入区的模型选择器使用。
func (b *bridge) loadModels() {
	b.async(func() {
		if _, err := b.ctx.Consume("llm.v1"); err != nil {
			b.patch(func(s *snapshot) { s.ModelNote = "LLM 插件不在线" })
			return
		}
		raw, err := b.ctx.Invoke("llm.v1", "models", map[string]any{})
		if err != nil {
			b.patch(func(s *snapshot) { s.ModelNote = err.Error() })
			return
		}
		var out struct {
			Models []string `json:"models"`
		}
		if json.Unmarshal(raw, &out) != nil {
			b.patch(func(s *snapshot) { s.ModelNote = "模型清单解析失败" })
			return
		}
		sort.Strings(out.Models)
		b.patch(func(s *snapshot) {
			s.ModelList = out.Models
			s.ModelNote = ""
		})
	})
}

// setModel 切换当前模型（写入 llm.v1 的活动配置），并刷新顶栏显示。
func (b *bridge) setModel(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	b.async(func() {
		if _, err := b.ctx.Invoke("llm.v1", "configure", map[string]any{"model": name}); err != nil {
			return
		}
		m := b.model()
		b.patch(func(s *snapshot) { s.Model = m })
	})
}

// plugins 拉已安装插件清单。
func (b *bridge) plugins() []pluginInfo {
	raw, err := b.ctx.Invoke("pluginManager.v1", "list", map[string]any{})
	if err != nil {
		return nil
	}
	var arr []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Version  string `json:"version"`
		Disabled bool   `json:"disabled"`
	}
	if json.Unmarshal(raw, &arr) != nil {
		// 有些实现包一层 {plugins:[...]}
		var wrap struct {
			Plugins []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Version  string `json:"version"`
				Disabled bool   `json:"disabled"`
			} `json:"plugins"`
		}
		if json.Unmarshal(raw, &wrap) != nil {
			return nil
		}
		for _, it := range wrap.Plugins {
			arr = append(arr, it)
		}
	}
	out := make([]pluginInfo, 0, len(arr))
	for _, it := range arr {
		out = append(out, pluginInfo{
			ID: it.ID, Name: it.Name, Version: it.Version, Alive: !it.Disabled,
		})
	}
	return out
}

func reasonOf(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// onEvent 处理核心事件，更新过程追踪与快照。
func (b *bridge) onEvent(name string, payload json.RawMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch name {
	case "chat.response", "agent.done", "session.updated":
		b.snap.Thinking = false
		b.snap.Trace.Done = true
		b.changed()
	case "plugin.deleted", "plugin.state":
		// 插件启停后服务链路与模型都会变，补一次探测。
		go b.reprobe()
		// 插件逐个激活：服务刚就绪时通知 UI 补拉当前页，避免功能页停在"未安装"。
		if b.onPluginState != nil {
			go b.onPluginState()
		}
		b.changed()
	case "agent.think":
		b.onThink(payload)
	case "chat.tool.before":
		b.onTool(payload, false)
	case "chat.tool.after":
		b.onTool(payload, true)
	}
}

func (b *bridge) onThink(payload json.RawMessage) {
	var p struct {
		Round int    `json:"round"`
		Phase string `json:"phase"`
		Model string `json:"model"`
		Think string `json:"thinking"`
		Text  string `json:"text"`
		Done  bool   `json:"done"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	b.snap.Thinking = true
	b.snap.Trace.Round = p.Round
	b.snap.Trace.Phase = p.Phase
	if p.Model != "" {
		b.snap.Trace.Model = p.Model
	}
	if p.Think != "" {
		b.snap.Trace.Think = p.Think
	}
	b.changed()
}

func (b *bridge) onTool(payload json.RawMessage, after bool) {
	var p struct {
		Tool string `json:"tool"`
		Args string `json:"args"`
		Ms   int64  `json:"ms"`
		OK   bool   `json:"ok"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	step := traceStep{Tool: p.Tool, Args: p.Args, Ms: p.Ms, OK: p.OK, Done: after}
	found := false
	for i := range b.snap.Trace.Steps {
		if b.snap.Trace.Steps[i].Tool == p.Tool && b.snap.Trace.Steps[i].Args == p.Args {
			b.snap.Trace.Steps[i] = step
			found = true
			break
		}
	}
	if !found {
		b.snap.Trace.Steps = append(b.snap.Trace.Steps, step)
	}
	b.changed()
}
