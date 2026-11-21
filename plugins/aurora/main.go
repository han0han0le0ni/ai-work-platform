// Command aurora 是官方极光 UI 插件（provides: ui.v1）。
//
// 和极简 UI 的区别：**这一版可以点**。
//
//   - 聊天页：消息区 + 可点击的服务清单（点一个服务就跳到它的插件详情）
//   - 插件页：列出所有已安装插件（读 plugins/*/manifest.json），点开看服务/权限/依赖
//   - 设置页：接入模型分「云端 / 本地」两条路 ——
//     云端＝知名平台快捷连接 + 自定义/冷门平台；本地＝扫描本机 AI + 已知应用 + 手动填。
//     挑完服务，右边填密钥、模型、地址，保存即生效。
//
// 硬通道约束不变：插件的 stdin/stdout 是 JSON-RPC，界面直接渲染控制终端。
// 鼠标按平台各走一条路：
//   - Windows 控制台：ReadConsoleInputW 的原生鼠标事件（VT 输入不转发鼠标）
//   - Unix / 重定向：xterm SGR 鼠标上报，解析成同一个事件模型
//
// 关于"列出插件"：核心只有 6 个基础接口（规范 1.5），没有枚举插件的 API。
// 所以界面像一个普通插件那样，经能力代理把 plugins/ 目录读出来 —— 代价是
// 需要 filesystem:read 权限，好处是**新装的插件自动出现在界面上**。
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aiwork/sdk/go/plugin"
)

// probeList 是界面探测链路的候选服务。
var probeList = []string{
	"conversation.v1",
	"session.v1",
	"agent.v1",
	"llm.v1",
	"toolExecutor.v1",
	"workspace.v1",
	"pluginManager.v1",
	"readFile.v1",
	"writeFile.v1",
	"listDir.v1",
	"sandbox.v1",
	"secrets.v1",
}

// disabledSuffix 与 plugin-manager 保持一致：目录加了这个后缀，核心就不再加载它。
const disabledSuffix = ".disabled"

// Aurora 实现 plugin.Plugin，同时是界面的 backend。
type Aurora struct {
	ctx *plugin.Context

	mu  sync.Mutex
	app *app
}

// Activate 认领终端并进入界面。
func (w *Aurora) Activate(ctx *plugin.Context) error {
	w.ctx = ctx

	// ui.v1 只能有一个所有者。抢不到说明已经有别的 UI 接管了终端 ——
	// 这时候安静退出，绝不去和它抢屏幕（两个界面同时画会互相覆盖）。
	if err := ctx.Provide("ui.v1", map[string]any{
		"kind":        "terminal",
		"description": "可点击的极光界面（聊天 / 各插件自带页面）",
	}); err != nil {
		ctx.Log("warn", "极光界面未接管终端（ui.v1 已被占用）："+err.Error(), nil)
		return nil
	}

	for _, name := range watchedEvents() {
		event := name
		ctx.On(event, func(payload json.RawMessage) { w.onEvent(event, payload) })
	}

	term, err := openTerminal()
	if err != nil {
		ctx.Log("warn", "没有可用的控制终端，极光界面进入静默模式："+err.Error(), nil)
		return nil
	}
	if err := term.Enter(); err != nil {
		ctx.Log("warn", "当前终端不支持全屏交互模式，极光界面进入静默模式："+err.Error(), nil)
		term.Leave()
		return nil
	}

	a := newApp(term, w, ctx.CoreVersion, ctx.APIVersion, ctx.PluginID)
	w.mu.Lock()
	w.app = a
	w.mu.Unlock()

	// Ctrl+C 会同时通知核心与本插件：先停界面、再还原终端，然后退出
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		a.Stop()
		a.waitStopped(500 * time.Millisecond)
		term.Leave()
		os.Exit(0)
	}()

	// 用量浮窗归界面画：告诉用量插件别再自己写终端了（否则两块窗互相盖）。
	//
	// 插件按目录名字母序激活，usage-monitor 排在我前面，理论上它已经订阅好了；
	// 补发一次是为了它重启、或将来换个编排顺序时也接得上。
	_ = ctx.Emit("ui.overlay.claim", map[string]any{"by": ctx.PluginID})
	time.AfterFunc(3*time.Second, func() {
		_ = ctx.Emit("ui.overlay.claim", map[string]any{"by": ctx.PluginID})
	})

	// 浮窗位置是用户拖出来的，得记住。保存回调注入到界面（落盘丢后台），
	// 读取在浮窗清单回来时按每个浮窗的 svc/id 去查（见 applyWidgets）。
	a.post(func() {
		a.st.widgetSave = func(svc, id string, x, y int, fold bool) {
			go w.saveWidget(svc, id, x, y, fold)
		}
	})

	ctx.Log("info", "极光界面已就绪（可点击）", map[string]any{"permissions": ctx.Permissions})
	go func() {
		defer term.Leave()
		a.Start()
		term.Leave()
	}()
	return nil
}

// onEvent 是事件订阅的回调体。
//
// 单独抽出来是为了能直接验证「哪些事件会触发刷新」。这里曾经有两个坑叠在一起：
// 一是订阅本身压根没生效（当时 SDK 的 ctx.On 只注册了本地处理函数、没向核心发
// core.on，于是所有插件的回调都是死的），二是即使收到了 plugin.state，也只把它
// 记进侧栏的事件列表、不刷新快照。两个都修掉之后，排在界面之后激活的插件
// （workspace.plugin 的 works 在 workbench 的 workb 之后）才不会永久显示成「未运行」。
// watchedEvents 是界面订阅的事件清单。
//
// 订阅里有一半是为了「看见 AI 在干什么」：agent.think / chat.tool.*
// 把编排过程一条条抛到总线上，界面攒起来就是那个过程框（见 activity.go）。
//
// 另一小半是"平台本身在动"：插件起来 / 挂掉 / **被删掉**都得重扫一遍快照，
// 否则界面停在旧事实上。plugin.deleted 尤其不能漏 —— 漏了它，被删插件
// 提供的那一页（ui.page.*）会永远留在标签栏上，点进去只剩一句"插件不在了"。
func watchedEvents() []string {
	out := []string{
		"plugin.state", "plugin.deleted",
		"chat.message", "chat.response", "core.log",
	}
	// 插件自带页面 / 浮窗靠事件喊"我变了"：界面据此重新去要一帧。
	out = append(out, pageChangedEvent, widgetChangedEvent)
	return append(out, agentEvents...)
}

func (w *Aurora) onEvent(name string, payload json.RawMessage) {
	a := w.currentApp()
	if a == nil {
		return
	}
	a.post(func() {
		w.pushEvent(name, summarize(payload))
		// agent 那几个事件额外并进「AI 在干什么」的过程框
		w.applyTrace(a, name, payload)
	})

	if name == "plugin.deleted" {
		// 删除是"重启生效"的：正在运行的插件目录被占用、删不掉，只是标了
		// "待重启删除"，目录此刻还在 —— 光重扫没用，扫描还是会看到它。
		// 所以除了重扫，还要**立刻**把它的页面下线，并记住"它被删了"，
		// 之后每一轮扫描结果都把它滤掉。
		var del struct {
			PluginID string `json:"pluginId"`
			Dir      string `json:"dir"`
		}
		_ = json.Unmarshal(payload, &del)
		a.post(func() { a.dropPlugin(del.PluginID, del.Dir) })
		a.scheduleRescan()
	} else if name == "plugin.state" {
		// 插件是逐个激活的，界面自己也在队列里。有插件动了就补扫一次快照，
		// 免得把「稍后才注册」的服务一直显示成未安装。
		a.scheduleRescan()
	}

	// 某个插件的页面说自己变了：把那一页的缓存作废，下一帧自然会重新去要。
	// 少了这一步，页面就会停在旧内容上 —— 看起来就是"点了没反应"。
	if name == pageChangedEvent {
		svc, id := pageChangedRef(payload)
		a.post(func() { a.invalidatePage(svc, id) })
	}

	// 某个浮窗说自己变了：重新要一帧内容。
	if name == widgetChangedEvent {
		svc, id := widgetChangedRef(payload)
		a.post(func() { a.invalidateWidget(svc, id) })
	}
}

// Handle 处理 core.invoke。别的插件可以经 ui.v1.notify 往对话区推消息。
func (w *Aurora) Handle(service, method string, args json.RawMessage) (any, error) {
	if service != "ui.v1" {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "极光界面不提供服务 %s", service)
	}
	switch method {
	case "info":
		services := map[string]any{}
		for _, s := range w.probeServices() {
			services[s.name] = map[string]any{"alive": s.alive, "pluginId": s.provider}
		}
		return map[string]any{
			"kind":     "terminal",
			"pluginId": w.ctx.PluginID,
			"services": services,
		}, nil
	case "notify":
		var in struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(args, &in)
		if a := w.currentApp(); a != nil {
			a.PushNotice(in.Text)
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "ui.v1 不支持方法 %s", method)
	}
}

// Deactivate 还原终端。
//
// 核心停插件只给一次机会（规范 2.4 的 core.shutdown）：SDK 调完这里就 os.Exit(0)，
// 没有第二次收尾。所以终端必须在这里收拾干净 —— 不退出 alt screen、不关掉鼠标
// 上报、不还原原始模式，新界面就画在残局上：满屏错行、点哪都不对。
// 重启核心走的正是这条路（见 Aurora.restart）。
func (w *Aurora) Deactivate() error {
	if a := w.currentApp(); a != nil {
		a.Stop() // 先停输入：否则还会有半个事件写进已还原的终端
		// 主循环自己退出时也会还原终端（见 Activate 里的 goroutine），
		// 这里等它一下再兜底：Leave 是幂等的，重复调用没有副作用。
		a.waitStopped(500 * time.Millisecond)
		a.term.Leave()
	}
	return nil
}

// restart 请核心重启：重新加载所有插件。
//
// 规范 2.6：插件的安装 / 删除 / 启停都只做标记，重启后才生效。
// 界面上一堆"重启后生效"的提示，这一下就是让它们落地的地方。
//
// 重启会连界面自己也杀掉（界面也是核心拉起来的插件），
// 所以这里只管把请求发出去：核心重新起来会再把界面拉起来。
func (w *Aurora) restart() (string, error) {
	w.ctx.Log("info", "请求重启核心", nil)
	if _, err := w.ctx.Capability("core.restart", map[string]any{}); err != nil {
		return "", fmt.Errorf("核心没重启：%w", err)
	}
	return "正在重启核心：插件的变更重启后才生效", nil
}

func (w *Aurora) currentApp() *app {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.app
}

// pushEvent 在界面线程上追加一条事件（调用方已通过 post 调度）。
func (w *Aurora) pushEvent(name, summary string) {
	a := w.currentApp()
	if a == nil {
		return
	}
	line := nowHM() + " " + name
	if summary != "" {
		line += " " + summary
	}
	a.st.events = append(a.st.events, line)
	if len(a.st.events) > 12 {
		a.st.events = a.st.events[len(a.st.events)-12:]
	}
}

// ---------------------------------------------------------------------------
// backend 实现
// ---------------------------------------------------------------------------

func (w *Aurora) probeServices() []serviceStatus {
	out := make([]serviceStatus, 0, len(probeList))
	for _, name := range probeList {
		pid, err := w.ctx.Consume(name)
		if err != nil {
			out = append(out, serviceStatus{name: name, note: reasonOf(err)})
			continue
		}
		out = append(out, serviceStatus{name: name, alive: true, provider: pid})
	}
	return out
}

// scanPlugins 经能力代理读 plugins/ 目录与各插件的 manifest。
func (w *Aurora) scanPlugins() ([]pluginInfo, error) {
	raw, err := w.ctx.FSList("plugins")
	if err != nil {
		return nil, err
	}
	var listing struct {
		Entries []struct {
			Name string `json:"name"`
			Dir  bool   `json:"dir"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, fmt.Errorf("解析目录列表失败: %w", err)
	}

	out := make([]pluginInfo, 0, len(listing.Entries))
	for _, e := range listing.Entries {
		if !e.Dir {
			continue
		}
		// 被禁用的插件目录多一个 .disabled 后缀，也要列出来 ——
		// 否则一禁用它就从清单里消失了，没法再点回来。
		base := strings.TrimSuffix(e.Name, disabledSuffix)
		if !strings.HasSuffix(base, ".plugin") {
			continue
		}
		out = append(out, w.readPlugin(e.Name))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (w *Aurora) readPlugin(dir string) pluginInfo {
	base := strings.TrimSuffix(dir, disabledSuffix)
	info := pluginInfo{
		ID:  strings.TrimSuffix(base, ".plugin"),
		Dir: dir,
	}

	raw, err := w.ctx.FSRead("plugins/" + dir + "/manifest.json")
	if err != nil {
		info.ReadErr = err.Error()
		return info
	}
	var file struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		info.ReadErr = "解析读取结果失败: " + err.Error()
		return info
	}

	// 只取两样用得上的：id（认归属）与 provides（认 ui.page.*、说清谁提供服务）。
	// 其余字段（名字、版本、简介、依赖、权限…）都随插件页搬去了 pluginManager。
	var m struct {
		ID       string   `json:"id"`
		Provides []string `json:"provides"`
	}
	if err := json.Unmarshal([]byte(file.Content), &m); err != nil {
		info.ReadErr = "manifest.json 非法: " + err.Error()
		return info
	}

	info.ID = firstNonEmpty(m.ID, info.ID)
	info.Provides = m.Provides

	// 声明的服务到底是谁在提供：只有真正登记在那个插件名下的才算"运行中"
	for _, svc := range m.Provides {
		if pid, err := w.ctx.Consume(svc); err == nil && pid == info.ID {
			info.Alive = append(info.Alive, svc)
		}
	}
	return info
}

// wsBrowse 列出目录，供文件页挑工作区。
//
// path 为空时让 workspace.v1 用默认目录（通常是运行时目录），
// 这样刚打开文件页就有东西可点，而不是一片空白。
func (w *Aurora) wsBrowse(path string) (wsListing, error) {
	if _, err := w.ctx.Consume("workspace.v1"); err != nil {
		return wsListing{}, fmt.Errorf("未安装工作区插件（workspace.v1），无法浏览目录")
	}
	params := map[string]any{}
	if strings.TrimSpace(path) != "" {
		params["path"] = path
	}
	raw, err := w.ctx.Invoke("workspace.v1", "browse", params)
	if err != nil {
		return wsListing{}, err
	}
	ls, err := parseListing(raw)
	if err != nil {
		return wsListing{}, err
	}
	ls.Note = fmt.Sprintf("已读取 %d 项：%s", len(ls.Entries), ls.Path)
	return ls, nil
}

// ---------------------------------------------------------------------------
// 插件自带的页面（ui.page.*）
//
// 界面不实现任何页面，只做四件事：问清单、要一帧、把点击转过去、把按键转过去。
// 装一个带界面的插件就多一页，这个文件不用改 —— 这才是"万物皆插件"。
// ---------------------------------------------------------------------------

// widgetList 问一个服务有哪些浮窗。
func (w *Aurora) widgetList(svc string) ([]plugin.WidgetInfo, error) {
	raw, err := w.ctx.Invoke(svc, "list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Widgets []plugin.WidgetInfo `json:"widgets"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析浮窗清单失败: %w", err)
	}
	return out.Widgets, nil
}

// widgetRender 问插件要浮窗的展开态内容。
func (w *Aurora) widgetRender(svc, id string) (plugin.WidgetRender, error) {
	var out plugin.WidgetRender
	raw, err := w.ctx.Invoke(svc, "render", map[string]any{"id": id})
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("解析浮窗帧失败: %w", err)
	}
	return out, nil
}

// pageList 问一遍这些服务各自提供哪些页面。
//
// services 由界面从扫描到的 manifest 里挑出来（见 pageServices）：插件是逐个
// 启动的，靠广播互相发现要看运气，而 manifest 一向扫得到。
func (w *Aurora) pageList(services []string) ([]pagePanel, error) {
	var out []pagePanel
	for _, svc := range services {
		if _, err := w.ctx.Consume(svc); err != nil {
			// 声明了却没跑起来（被跳过 / 崩了）：跳过它，别让一页拖垮整张清单
			continue
		}
		raw, err := w.ctx.Invoke(svc, "list", map[string]any{})
		if err != nil {
			w.ctx.Log("warn", "问页面清单失败（"+svc+"）："+err.Error(), nil)
			continue
		}
		var got struct {
			Panels []plugin.PanelInfo `json:"panels"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			w.ctx.Log("warn", "解析页面清单失败（"+svc+"）："+err.Error(), nil)
			continue
		}
		for _, p := range got.Panels {
			if strings.TrimSpace(p.ID) == "" {
				continue
			}
			out = append(out, pagePanel{svc: svc, id: p.ID, title: p.Title, order: p.Order})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].order != out[j].order {
			return out[i].order < out[j].order
		}
		return out[i].ref() < out[j].ref()
	})
	return out, nil
}

// pageRender 找插件要一帧。
//
// 刻意不先 Consume 一次：这里就在打字与滚动的路径上，多一次往返就是多一截
// 打字延迟。插件真的不在了，Invoke 自己会返回"提供者已死"。
func (w *Aurora) pageRender(svc, id string, width, height, scroll int) (plugin.PanelRender, error) {
	var out plugin.PanelRender
	raw, err := w.ctx.Invoke(svc, "render", map[string]any{
		"panel": id, "w": width, "h": height, "scroll": scroll,
	})
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("解析页面帧失败: %w", err)
	}
	return out, nil
}

// pageAction 把面板里的一次点击转给插件，返回它想显示的那句话。
func (w *Aurora) pageAction(svc, id, action, arg string) (string, error) {
	raw, err := w.ctx.Invoke(svc, "action", map[string]any{
		"panel": id, "action": action, "arg": arg,
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Note string `json:"note"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Note, nil
}

// pageKey 把一次按键转给插件，带回插件对这次按键的回应（见 pageKeyResult）。
//
// 用 Invoke 而不是单向通知：SDK 没有"发完不等"的调用，而按键必须**按顺序**
// 抵达插件（乱序打字就是乱码）—— 顺序由界面侧的一条队列保证（见 startPageKeys）。
func (w *Aurora) pageKey(svc, id, kind, text string) (pageKeyResult, error) {
	raw, err := w.ctx.Invoke(svc, "key", map[string]any{
		"panel": id, "kind": kind, "text": text,
	})
	if err != nil {
		return pageKeyResult{}, err
	}
	var out struct {
		Copy string `json:"copy,omitempty"` // 请宿主把这段文字写进剪贴板
		Exit bool   `json:"exit,omitempty"` // 请宿主退出（Ctrl+C 且没选中）
	}
	_ = json.Unmarshal(raw, &out)
	return pageKeyResult{copyText: out.Copy, exit: out.Exit}, nil
}

// pageChangedRef 从事件载荷里取出"哪一页变了"。
//
// 插件报服务名最好（精确到某个插件的某一页）；只报页面 id 也能用 ——
// 名字撞车时最多多刷一页，不会刷错东西。
func pageChangedRef(payload json.RawMessage) (svc, id string) {
	var in struct {
		Service string `json:"service"`
		Panel   string `json:"panel"`
	}
	_ = json.Unmarshal(payload, &in)
	return in.Service, in.Panel
}

// ---------------------------------------------------------------------------
// 附件：把一个路径读成能发给模型的东西
// ---------------------------------------------------------------------------

// maxAttachBytes 是单个附件的上限。
//
// 再大就超过核心单次调用的输出上限（4 MB），也会把请求撑到模型那边超时。
// 与其发出去一半、模型收到个残文件，不如在这里就拒绝。
const maxAttachBytes = 3 << 20

// readAttachment 读取一个文件，按类型决定怎么交给模型：
//
//	图片 → base64 的 data URL，模型能"看见"
//	文本 → 原文，放进正文
//	其它 → 只带名字和大小，如实说明看不了内容
func (w *Aurora) readAttachment(path string) (attachment, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return attachment{}, fmt.Errorf("路径为空")
	}
	name := filepath.Base(clean)
	att := attachment{Path: clean, Name: name}
	att.Type = att.kind()

	var raw json.RawMessage
	var err error
	if att.Type == "image" {
		raw, err = w.ctx.FSReadBase64(clean)
	} else {
		raw, err = w.ctx.FSRead(clean)
	}
	if err != nil {
		return attachment{}, err
	}

	var out struct {
		Size      int64  `json:"size"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return attachment{}, fmt.Errorf("解析读取结果失败: %w", err)
	}
	att.Size = out.Size

	if att.Size > maxAttachBytes {
		return attachment{}, fmt.Errorf("%s 有 %s，超过 %s 的上限", att.Name,
			humanSize(att.Size), humanSize(maxAttachBytes))
	}
	// 截断的内容不能发：模型会拿着半个文件一本正经地胡说
	if out.Truncated {
		return attachment{}, fmt.Errorf("%s 太大，读取时被截断，无法完整发送", att.Name)
	}

	switch att.Type {
	case "image":
		att.DataURL = "data:" + imageMIME(att.Name) + ";base64," + out.Content
	case "text":
		att.Text = out.Content
	}
	return att, nil
}

// imageMIME 按扩展名给出 data URL 的 MIME。
func imageMIME(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return "image/png"
	}
}

// readImageBytes 读出一张图的原始字节。
//
// 必须走 base64 通道：普通读取会把二进制转成字符串，PNG 里的无效 UTF-8
// 字节会被换成 U+FFFD，解出来就是一张坏图。
func (w *Aurora) readImageBytes(path string) ([]byte, error) {
	raw, err := w.ctx.FSReadBase64(path)
	if err != nil {
		return nil, err
	}
	var out struct {
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析读取结果失败: %w", err)
	}
	if out.Truncated {
		return nil, fmt.Errorf("图片太大，读不全")
	}
	data, err := base64.StdEncoding.DecodeString(out.Content)
	if err != nil {
		return nil, fmt.Errorf("base64 解码失败: %w", err)
	}
	return data, nil
}

// openPath 把这个路径交给系统默认程序打开（需 shell:open）。
func (w *Aurora) openPath(path string) (string, error) {
	if _, err := w.ctx.ShellOpen(path); err != nil {
		return "", err
	}
	return "已交给系统打开：" + path, nil
}

// parseListing 把 workspace.v1 browse 的返回值转成界面用的结构。
func parseListing(raw json.RawMessage) (wsListing, error) {
	var out struct {
		Path   string `json:"path"`
		Parent string `json:"parent"`
		Root   string `json:"root"`
		Err    string `json:"error"`
		Note   string `json:"note"`
		Items  []struct {
			Name string `json:"name"`
			Dir  bool   `json:"dir"`
			Size int64  `json:"size"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return wsListing{}, fmt.Errorf("解析目录列表失败: %w", err)
	}
	ls := wsListing{Path: out.Path, Parent: out.Parent, Root: out.Root, Note: out.Note}
	if out.Err != "" {
		return ls, fmt.Errorf("%s", out.Err)
	}
	for _, e := range out.Items {
		ls.Entries = append(ls.Entries, dirEntry{Name: e.Name, Dir: e.Dir, Size: e.Size})
	}
	return ls, nil
}

// loadAgentBudget 读 agent 当前的工具调用预算。
//
// agent.v1.info 把"不限制"报成字符串而不是数字，所以这里两种都要认。
func (w *Aurora) loadAgentBudget() (int, bool, error) {
	if _, err := w.ctx.Consume("agent.v1"); err != nil {
		return 0, false, fmt.Errorf("agent.v1 不可用（%s）", reasonOf(err))
	}
	raw, err := w.ctx.Invoke("agent.v1", "info", map[string]any{})
	if err != nil {
		return 0, false, err
	}
	var out struct {
		MaxSteps json.RawMessage `json:"maxSteps"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, false, fmt.Errorf("解析 agent.v1.info 失败: %w", err)
	}
	var n int
	if json.Unmarshal(out.MaxSteps, &n) == nil {
		return n, false, nil
	}
	var s string
	if json.Unmarshal(out.MaxSteps, &s) == nil {
		return 0, true, nil // "不限制"
	}
	return 0, false, nil
}

// saveAgentBudget 写入预算。unlimited=true 时用 -1 这个约定值。
func (w *Aurora) saveAgentBudget(steps int, unlimited bool) (string, error) {
	if _, err := w.ctx.Consume("agent.v1"); err != nil {
		return "agent.v1 不可用（" + reasonOf(err) + "）", err
	}
	if unlimited {
		steps = -1
	} else if steps <= 0 {
		return "工具上限要填一个正整数", fmt.Errorf("工具上限非法: %d", steps)
	}
	if _, err := w.ctx.Invoke("agent.v1", "configure", map[string]any{"maxSteps": steps}); err != nil {
		return "应用失败：" + err.Error(), err
	}
	if unlimited {
		return "已设为不限制：模型自己决定何时停手", nil
	}
	return fmt.Sprintf("已设为最多 %d 轮工具调用，立即生效", steps), nil
}

// ---------------------------------------------------------------------------
// 链式对话（conversation.v1）
//
// 界面只做两件事：把意图传下去、把返回的时间线铺开。
// 消息树的增删改、版本翻页、分享文本全部由对话插件负责 —— 它才是那些数据的家，
// 界面连树长什么样都不需要知道，只认识「一条时间线 + 会话清单」。
// ---------------------------------------------------------------------------

const convService = "conversation.v1"

func (w *Aurora) convSupported() bool {
	_, err := w.ctx.Consume(convService)
	return err == nil
}

func (w *Aurora) convList() (convSnapshot, error) {
	return w.convInvoke("history", nil)
}

func (w *Aurora) convNew() (convSnapshot, error) {
	return w.convInvoke("newSession", nil)
}

func (w *Aurora) convDelete(sessionID string) (convSnapshot, error) {
	return w.convInvoke("deleteSession", map[string]any{"sessionId": sessionID})
}

func (w *Aurora) convSwitch(sessionID string) (convSnapshot, error) {
	return w.convInvoke("switch", map[string]any{"sessionId": sessionID})
}

func (w *Aurora) convSend(sessionID, text string, atts []attachment) (convSnapshot, error) {
	params := map[string]any{"sessionId": sessionID, "text": text}
	if len(atts) > 0 {
		params["attachments"] = atts
	}
	return w.convInvoke("send", params)
}

func (w *Aurora) convEdit(sessionID, nodeID, text string) (convSnapshot, error) {
	return w.convInvoke("edit", map[string]any{
		"sessionId": sessionID, "nodeId": nodeID, "text": text,
	})
}

func (w *Aurora) convRegenerate(sessionID, nodeID string) (convSnapshot, error) {
	return w.convInvoke("regenerate", map[string]any{"sessionId": sessionID, "nodeId": nodeID})
}

func (w *Aurora) convPage(sessionID, nodeID string, delta int) (convSnapshot, error) {
	return w.convInvoke("switchVersion", map[string]any{
		"sessionId": sessionID, "nodeId": nodeID, "delta": delta,
	})
}

// convMarkdown 只要分享文本，不落盘 —— 复制到系统剪贴板用。
func (w *Aurora) convMarkdown(sessionID string) (string, error) {
	if _, err := w.ctx.Consume(convService); err != nil {
		return "", fmt.Errorf("未安装对话插件（conversation.v1）")
	}
	raw, err := w.ctx.Invoke(convService, "share", map[string]any{"sessionId": sessionID})
	if err != nil {
		return "", err
	}
	var out struct {
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析分享内容失败: %w", err)
	}
	if strings.TrimSpace(out.Markdown) == "" {
		return "", fmt.Errorf("这条会话还没有可分享的内容")
	}
	return out.Markdown, nil
}

// convExport 让对话插件把当前时间线写成 .md 落到工作区里。
//
// 界面自己不写文件（它只有 filesystem:read），写哪儿、怎么写由对话插件
// 经 workspace.v1 决定 —— 界面只把路径念给用户听。
func (w *Aurora) convExport(sessionID string) (string, error) {
	if _, err := w.ctx.Consume(convService); err != nil {
		return "", fmt.Errorf("未安装对话插件（conversation.v1）")
	}
	raw, err := w.ctx.Invoke(convService, "export", map[string]any{"sessionId": sessionID})
	if err != nil {
		return "", err
	}
	var out struct {
		Path string `json:"path"`
		Note string `json:"note"`
	}
	_ = json.Unmarshal(raw, &out)
	return firstNonEmpty(out.Note, "已导出到 "+out.Path, "已导出"), nil
}

// convInvoke 调一次 conversation.v1 并把返回解析成界面用的快照。
func (w *Aurora) convInvoke(method string, params map[string]any) (convSnapshot, error) {
	if _, err := w.ctx.Consume(convService); err != nil {
		return convSnapshot{}, fmt.Errorf("未安装对话插件（conversation.v1）")
	}
	if params == nil {
		params = map[string]any{}
	}
	raw, err := w.ctx.Invoke(convService, method, params)
	if err != nil {
		return convSnapshot{}, err
	}
	return parseConv(raw)
}

// parseConv 把 conversation.v1 的返回转成界面结构。
func parseConv(raw json.RawMessage) (convSnapshot, error) {
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
	if err := json.Unmarshal(raw, &out); err != nil {
		return convSnapshot{}, fmt.Errorf("解析 conversation.v1 响应失败: %w", err)
	}

	s := convSnapshot{sessionID: out.SessionID, name: out.Name, note: out.Note}
	for _, it := range out.Sessions {
		s.sessions = append(s.sessions, sessionInfo{
			id: it.SessionID, name: it.Name, nodes: it.Nodes, updated: it.Updated,
		})
	}
	for _, it := range out.Path {
		s.msgs = append(s.msgs, msgView{
			id: it.ID, role: it.Role, text: it.Text, at: it.At,
			version: it.Version, total: it.Total,
			via: it.Via, err: it.Err, trunc: it.Trunc,
		})
	}
	// 末尾那条回复失败时插件会给一句 note；万一没有，至少让用户知道能重试
	if s.note == "" && out.Err != "" {
		s.note = "生成失败：" + out.Err + " · 点这条消息的「重新生成」可重试"
	}
	return s, nil
}

// widgetPos 读某个浮窗上次停下的位置；没读过返回 (0,0,false)（界面会贴右上角）。
func (w *Aurora) widgetPos(svc, id string) (int, int, bool) {
	raw, err := w.ctx.GetConfig("widget:" + svc + "/" + id)
	if err != nil || len(raw) == 0 {
		return 0, 0, false
	}
	var v struct {
		X    int  `json:"x"`
		Y    int  `json:"y"`
		Fold bool `json:"fold"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return 0, 0, false
	}
	return v.X, v.Y, v.Fold
}

// saveWidget 把某个浮窗停下的地方写回配置：下次启动还在那儿。
//
// 每个浮窗一个键（widget:<服务>/<id>）：位置是界面的 UI 状态，跟着浮窗走，
// 不塞进插件的配置里 —— 插件根本不该知道自己的浮窗被拖到了哪儿。
func (w *Aurora) saveWidget(svc, id string, x, y int, fold bool) {
	key := "widget:" + svc + "/" + id
	blob, err := json.Marshal(map[string]any{"x": x, "y": y, "fold": fold})
	if err != nil {
		return
	}
	if err := w.ctx.SetConfig(key, json.RawMessage(blob)); err != nil {
		w.ctx.Log("warn", "浮窗位置没存下来（下次会回到右上角）："+err.Error(), nil)
	}
}

// chat 按"会话 → Agent"的顺序探测可用链路。
func (w *Aurora) chat(text string) (string, string, error) {
	_ = w.ctx.Emit("chat.message", map[string]any{"text": text, "from": "aurora"})

	raw, via, err := w.dispatch(text)
	if err != nil {
		return "", "", err
	}
	_ = w.ctx.Emit("chat.response", map[string]any{"via": via, "reply": string(raw)})
	return string(raw), via, nil
}

func (w *Aurora) dispatch(text string) (json.RawMessage, string, error) {
	if _, err := w.ctx.Consume("session.v1"); err == nil {
		out, err := w.ctx.Invoke("session.v1", "sendMessage", map[string]any{"text": text})
		if err == nil {
			return out, "session.v1", nil
		}
		if !plugin.IsCode(err, plugin.CodeServiceNotFound) {
			return nil, "session.v1", err
		}
	}
	if _, err := w.ctx.Consume("agent.v1"); err == nil {
		out, err := w.ctx.Invoke("agent.v1", "run", map[string]any{
			"messages": []map[string]string{{"role": "user", "content": text}},
		})
		if err != nil {
			return nil, "agent.v1", err
		}
		return out, "agent.v1", nil
	}
	return nil, "", fmt.Errorf("链路未装齐：至少需要 session.v1 或 agent.v1")
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func reasonOf(err error) string {
	switch {
	case err == nil:
		return ""
	case plugin.IsCode(err, plugin.CodeServiceNotFound):
		return "未安装"
	case plugin.IsCode(err, plugin.CodeProviderDead):
		return "提供者已死"
	case plugin.IsCode(err, plugin.CodeDependencyMissing):
		return "依赖缺失"
	case plugin.IsCode(err, plugin.CodeAPIVersionIncompatible):
		return "版本不兼容"
	default:
		return err.Error()
	}
}

// summarize 把事件载荷压成一行。
func summarize(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	s := strings.Join(strings.Fields(string(payload)), " ")
	if len(s) > 44 {
		s = s[:44] + "…"
	}
	return s
}

func main() {
	if err := plugin.Serve(&Aurora{}); err != nil {
		plugin.Logf("error", "极光界面退出: %v", err)
		os.Exit(1)
	}
}
