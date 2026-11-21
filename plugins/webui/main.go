// Command webui 是浏览器图形界面插件（provides: webui.v1）。
//
// 与终端 UI（aurora / workbench）不同：它不起 HTTP 服务、不开窗口接管终端，
// 而是起一个本地 HTTP 服务，浏览器打开 http://127.0.0.1:<port> 即可聊天。
// 聊天链路直接复用平台的 conversation.v1（会话 / 时间线 / 多轮），
// 实时性靠 SSE（Server-Sent Events）推送——前端发消息用普通 POST，
// 后端把「回复完成 / AI 在做什么」通过事件流推给浏览器。
//
// 之所以不抢 ui.v1：Web UI 和终端 UI 是两种界面，可以共存，互不影响。
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"aiwork/sdk/go/plugin"
)

//go:embed static/index.html
var indexHTML []byte

const convService = "conversation.v1"

// probeList 是界面侧栏要展示的服务链路。
var probeList = []string{
	"conversation.v1", "session.v1", "agent.v1", "llm.v1", "toolExecutor.v1",
}

// WebUI 实现 plugin.Plugin。
type WebUI struct {
	ctx  *plugin.Context
	port string
	hub  *hub

	mu           sync.Mutex
	trace        traceState
	sessionID    string      // 缓存的当前会话 id：发消息不再每次拉完整历史
	craftRunning bool        // 同一时间只允许一个「一键造插件」任务
	craftState   *craftState // 最近一次造插件任务状态（GET /api/craft 查询）
}

// Activate 起 HTTP 服务。
func (w *WebUI) Activate(ctx *plugin.Context) error {
	w.ctx = ctx
	w.port = firstNonEmpty(os.Getenv("AIWORK_WEBUI_PORT"), "9125")
	w.hub = newHub()

	if err := ctx.Provide("webui.v1", map[string]any{
		"kind":        "http",
		"description": "浏览器图形界面",
	}); err != nil {
		return err
	}

	for _, name := range []string{
		"chat.message", "chat.response",
		"agent.think", "chat.tool.before", "chat.tool.after", "agent.done",
		"session.updated",
		// 插件装 / 删 / 启停：前端「插件」页靠它即时刷新
		"plugin.deleted", "plugin.state",
	} {
		event := name
		ctx.On(event, func(payload json.RawMessage) { w.onEvent(event, payload) })
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", w.handleIndex)
	mux.HandleFunc("/api/state", w.handleState)
	mux.HandleFunc("/api/send", w.handleSend)
	mux.HandleFunc("/api/session", w.handleSession)
	// 消息级操作：重新生成 / 编辑 / 消息树翻版本 / 删除
	mux.HandleFunc("/api/msg", w.handleMsg)
	mux.HandleFunc("/api/stream", w.handleStream)
	// 通用服务代理：前端把 {service, method, args} 丢过来，后端转发给对应插件。
	// 插件页 / 模型页 / 工作区 / 角色 / 预算 / 文生图 全部走这一个口子。
	mux.HandleFunc("/api/invoke", w.handleInvoke)
	// 本地图片服务：文生图把图片存到磁盘绝对路径，浏览器经它取回显示。
	mux.HandleFunc("/api/image", w.handleImage)
	// 打开文件夹：浏览器借核心能力（shell:pickfolder）弹系统目录选择框。
	mux.HandleFunc("/api/pickfolder", w.handlePickFolder)
	// 平台电源：重启 / 停止核心（core.restart / core.shutdown，仅官方插件可调）。
	mux.HandleFunc("/api/restart", w.handleRestart)
	mux.HandleFunc("/api/shutdown", w.handleShutdown)
	mux.HandleFunc("/api/craft", w.handleCraft)

	go func() {
		ctx.Log("info", "Web UI 已就绪", map[string]any{"url": "http://127.0.0.1:" + w.port})
		if err := http.ListenAndServe(":"+w.port, mux); err != nil {
			ctx.Log("error", "Web UI HTTP 服务退出: "+err.Error(), nil)
		}
	}()

	// 桌面模式：起一个原生应用窗口（Windows 用系统自带 Edge 的 --app 模式，
	// 无地址栏、无标签、独立任务栏图标，体验等同桌面 App）。设 AIWORK_WEBUI_APP=0 可关。
	//
	// 「重启平台」接班起来的新实例不再开窗：旧窗口还在（SSE 会自动重连），
	// 再弹一个就变成两个相同窗口。标记由 /api/restart 在请求重启前写下，
	// 这里读到即清除——一次性，不影响用户下次正常双击启动。
	if w.consumeSkipAppWindow() {
		ctx.Log("info", "平台重启接班：复用已有桌面窗口，不再另开", nil)
	} else {
		go w.openAppWindow()
	}
	return nil
}

// consumeSkipAppWindow 读取并清除「重启接班，跳过开窗」标记。
func (w *WebUI) consumeSkipAppWindow() bool {
	raw, err := w.ctx.GetConfig("skipAppWindow")
	if err != nil || len(raw) == 0 {
		return false
	}
	var skip bool
	_ = json.Unmarshal(raw, &skip)
	_ = w.ctx.SetConfig("skipAppWindow", false)
	return skip
}

// openAppWindow 用「应用窗口」模式打开桌面界面。它不是完整浏览器 ——
// --app 模式没有地址栏、书签、标签页，就是一个独立的桌面窗口。
func (w *WebUI) openAppWindow() {
	if os.Getenv("AIWORK_WEBUI_APP") == "0" {
		return
	}
	url := "http://127.0.0.1:" + w.port
	// 等自己的 HTTP 服务真的在监听了再开窗：否则窗口先弹出来、页面还没起，
	// 用户第一眼看到的是"无法访问"，这就是启动不同步的来源。
	if !waitPort("127.0.0.1", w.port, 3*time.Second) {
		w.ctx.Log("warn", "Web UI 端口未就绪，跳过桌面窗口："+url, nil)
		return
	}
	var cmds [][]string
	if runtime.GOOS == "windows" {
		cmds = [][]string{
			{"msedge", "--app=" + url},
			{`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`, "--app=" + url},
			{`C:\Program Files\Microsoft\Edge\Application\msedge.exe`, "--app=" + url},
		}
	} else {
		cmds = [][]string{{"xdg-open", url}, {"open", url}}
	}
	for _, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.SysProcAttr = detachedSysProcAttr()
		if err := cmd.Start(); err == nil {
			// 释放句柄：窗口进程比平台长寿是常态（平台重启它都还在），
			// 不该让它作为"我们的子进程"被一路挂着。
			_ = cmd.Process.Release()
			w.ctx.Log("info", "已打开桌面应用窗口", map[string]any{"url": url})
			return
		}
	}
	w.ctx.Log("warn", "未找到桌面窗口（Edge）；可用浏览器访问 "+url, nil)
}

// Handle 处理 core.invoke。webui.v1.info 返回访问地址。
func (w *WebUI) Handle(service, method string, args json.RawMessage) (any, error) {
	if service != "webui.v1" {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "webui 不提供服务 %s", service)
	}
	switch method {
	case "info":
		return map[string]any{
			"kind": "http",
			"url":  "http://127.0.0.1:" + w.port,
		}, nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "webui.v1 不支持方法 %s", method)
	}
}

// Deactivate 无资源需要显式清理。
func (w *WebUI) Deactivate() error { return nil }

// ---------------------------------------------------------------------------
// HTTP 处理
// ---------------------------------------------------------------------------

func (w *WebUI) handleIndex(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = rw.Write(indexHTML)
}

func (w *WebUI) handleState(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, w.snapshot())
}

// handlePickFolder 弹系统文件夹选择对话框，返回 {path, cancelled}。
//
// 浏览器没法自己弹 Windows 原生对话框，由本插件借核心能力（shell:pickfolder）
// 调系统选择器，再把用户选的目录回给前端。阻塞调用：对话框不关不返回。
func (w *WebUI) handlePickFolder(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	initial := strings.TrimSpace(r.URL.Query().Get("initial"))
	raw, err := w.ctx.PickFolder(initial, "选择工作目录")
	if err != nil {
		writeJSON(rw, map[string]any{"error": map[string]any{
			"code":    plugin.CodeOf(err),
			"message": err.Error(),
		}})
		return
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		v = string(raw)
	}
	writeJSON(rw, map[string]any{"result": v})
}

// handleRestart 请求核心重启：核心先回 {ok:true}，约 250ms 后杀掉全部插件、
// 用自身镜像重新启动。HTTP 响应必须赶在那之前发出去，调用方（本插件）也会被杀。
func (w *WebUI) handleRestart(rw http.ResponseWriter, r *http.Request) {
	// 告诉接班的新实例：桌面窗口还在，别再弹一个（标记随插件配置持久化，
	// 新实例 Activate 时读到即清除）。
	_ = w.ctx.SetConfig("skipAppWindow", true)
	w.power(rw, r, "core.restart")
}

// handleShutdown 请求核心停止：插件全部优雅退出，进程退出码 0 收尾。
// 再用平台要重新双击启动器；停止后这个页面将永远连不上，属预期。
func (w *WebUI) handleShutdown(rw http.ResponseWriter, r *http.Request) {
	w.power(rw, r, "core.shutdown")
}

func (w *WebUI) power(rw http.ResponseWriter, r *http.Request, method string) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := w.ctx.Capability(method, map[string]any{})
	if err != nil {
		writeJSON(rw, map[string]any{"error": map[string]any{
			"code":    plugin.CodeOf(err),
			"message": err.Error(),
		}})
		return
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		v = string(raw)
	}
	writeJSON(rw, map[string]any{"result": v})
}

func (w *WebUI) handleSend(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
		// Attachments 随本条消息一起发给模型（图片 dataUrl / 文本内容），不落会话树。
		Attachments []map[string]any `json:"attachments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Text) == "" && len(in.Attachments) == 0 {
		http.Error(rw, "empty text", http.StatusBadRequest)
		return
	}
	// 同步发送会阻塞到模型回复完成，前端会一直挂着。这里丢后台，
	// 结果通过 SSE 的 snapshot 事件推回浏览器。
	w.resetTrace()
	w.broadcastTrace()
	go func() {
		_ = w.sendConv(in.Text, in.Attachments)
		w.pushSnapshot()
	}()
	writeJSON(rw, map[string]any{"accepted": true})
}

// handleMsg 处理消息级操作：
//   - regenerate  重新生成一条回复（异步，要跑模型）
//   - edit        编辑消息（用户消息改完会自动再问一遍，异步）
//   - version     消息树翻版本（只改 Active 下标，同步秒回）
//   - delete      删除一条消息及其下游（同步）
func (w *WebUI) handleMsg(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
		NodeID string `json:"nodeId"`
		Text   string `json:"text"`
		Delta  int    `json:"delta"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	if in.NodeID == "" {
		http.Error(rw, "missing nodeId", http.StatusBadRequest)
		return
	}
	switch in.Action {
	case "regenerate", "edit":
		if in.Action == "edit" && strings.TrimSpace(in.Text) == "" {
			http.Error(rw, "empty text", http.StatusBadRequest)
			return
		}
		// 与 send 一样丢后台：可能要跑几分钟模型，结果走 SSE 快照推回。
		w.resetTrace()
		w.broadcastTrace()
		go func() {
			if in.Action == "regenerate" {
				_, _ = w.conv("regenerate", map[string]any{"nodeId": in.NodeID})
			} else {
				_, _ = w.conv("edit", map[string]any{"nodeId": in.NodeID, "text": in.Text})
			}
			w.pushSnapshot()
		}()
		writeJSON(rw, map[string]any{"accepted": true})
	case "version", "delete":
		method := map[string]string{"version": "switchVersion", "delete": "deleteMessage"}[in.Action]
		params := map[string]any{"nodeId": in.NodeID}
		if in.Action == "version" {
			params["delta"] = in.Delta
		}
		if _, err := w.conv(method, params); err != nil {
			writeJSON(rw, map[string]any{"error": map[string]any{"message": err.Error()}})
			return
		}
		w.pushSnapshot()
		writeJSON(rw, map[string]any{"ok": true})
	default:
		http.Error(rw, "unknown action", http.StatusBadRequest)
	}
}

func (w *WebUI) handleSession(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		Action    string `json:"action"`
		SessionID string `json:"sessionId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	switch in.Action {
	case "new", "switch", "delete":
	default:
		http.Error(rw, "unknown action", http.StatusBadRequest)
		return
	}
	w.session(in.Action, in.SessionID)
	w.pushSnapshot()
	writeJSON(rw, map[string]any{"ok": true})
}

// handleStream 是 SSE 端点：把后端广播的事件推给浏览器。
func (w *WebUI) handleStream(rw http.ResponseWriter, r *http.Request) {
	fl, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("Connection", "keep-alive")
	rw.Header().Set("X-Accel-Buffering", "no")

	ch := w.hub.subscribe()
	defer w.hub.unsubscribe(ch)

	for {
		select {
		case msg := <-ch:
			_, _ = fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", msg.event, msg.data)
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// invokeReq 是 /api/invoke 的入参。args 缺省为空对象。
type invokeReq struct {
	Service string          `json:"service"`
	Method  string          `json:"method"`
	Args    json.RawMessage `json:"args"`
}

// handleInvoke 通用代理：任何插件服务都能经它调。
//
// 它是本地工具（只监听 127.0.0.1），转发范围与插件自身的能力一致 ——
// 权限仍由各插件声明、核心裁决，这里不额外放权。
func (w *WebUI) handleInvoke(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in invokeReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Service) == "" || strings.TrimSpace(in.Method) == "" {
		http.Error(rw, "service and method required", http.StatusBadRequest)
		return
	}
	raw, err := w.ctx.Invoke(in.Service, in.Method, in.Args)
	if err != nil {
		writeJSON(rw, map[string]any{"error": map[string]any{
			"code":    plugin.CodeOf(err),
			"message": err.Error(),
		}})
		return
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		v = string(raw)
	}
	writeJSON(rw, map[string]any{"result": v})
}

// handleImage 提供文生图产物：浏览器经 <img src="/api/image?path=..."> 取图。
//
// 只放行图片扩展名；工作区已设定时，路径必须落在工作区里 —— 免得有人拿
// 这个口子扫全盘文件。
func (w *WebUI) handleImage(rw http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" || !isImagePath(path) {
		http.Error(rw, "invalid image path", http.StatusBadRequest)
		return
	}
	// 工作区未设定时，仅放行相对 runtime 的 generated-images 目录（imagegen 默认落点）
	if !filepath.IsAbs(path) {
		path = filepath.Join("generated-images", path)
	}
	if !w.pathAllowed(path) {
		http.Error(rw, "path outside workspace", http.StatusForbidden)
		return
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		http.Error(rw, "not found", http.StatusNotFound)
		return
	}
	rw.Header().Set("Content-Type", mimeTypeByExt(path))
	rw.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = rw.Write(blob)
}

// pathAllowed 判断图片路径是否可服务：工作区已设则必须在其内，否则退回
// generated-images 相对目录。
func (w *WebUI) pathAllowed(path string) bool {
	if root := w.workspaceRoot(); root != "" {
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return false
		}
		return true
	}
	return strings.Contains(path, "generated-images")
}

// workspaceRoot 问一次 workspace.v1 当前工作区；未装或未设返回空串。
func (w *WebUI) workspaceRoot() string {
	if _, err := w.ctx.Consume("workspace.v1"); err != nil {
		return ""
	}
	raw, err := w.ctx.Invoke("workspace.v1", "browse", map[string]any{})
	if err != nil {
		return ""
	}
	var out struct {
		Root string `json:"root"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Root == "" {
		return ""
	}
	return out.Root
}

// isImagePath 只认常见图片扩展名。
func isImagePath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return true
	}
	return false
}

// mimeTypeByExt 图片扩展名 → Content-Type。
func mimeTypeByExt(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	}
	return "application/octet-stream"
}

// ---------------------------------------------------------------------------
// 后端逻辑：调用 conversation.v1
// ---------------------------------------------------------------------------

// snapshot 是前端一次要拿到的完整状态。
type snapshot struct {
	SessionID string          `json:"sessionId"`
	Name      string          `json:"name"`
	Note      string          `json:"note"`
	Sessions  []sessionInfo   `json:"sessions"`
	Path      []msgView       `json:"path"`
	Services  []serviceStatus `json:"services"`
	Model     string          `json:"model"`
}

type sessionInfo struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Nodes     int    `json:"nodes"`
	Updated   string `json:"updated"`
}

type msgView struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Text    string `json:"text"`
	At      string `json:"at"`
	Version int    `json:"version"`
	Total   int    `json:"total"`
	Via     string `json:"via"`
	Err     string `json:"err"`
	Trunc   bool   `json:"trunc"`
}

type serviceStatus struct {
	Name     string `json:"name"`
	Alive    bool   `json:"alive"`
	Provider string `json:"provider"`
	Note     string `json:"note"`
}

// snapshot 拉取会话快照 + 服务链路。
func (w *WebUI) snapshot() snapshot {
	s := snapshot{Services: w.services(), Model: w.model()}
	raw, err := w.conv("history", nil)
	if err != nil {
		s.Note = "未安装对话插件（conversation.v1）：请先装 conversation / session / agent / llm"
		return s
	}
	applyConv(&s, raw)
	w.mu.Lock()
	w.sessionID = s.SessionID
	w.mu.Unlock()
	return s
}

// services 探测服务链路。
func (w *WebUI) services() []serviceStatus {
	out := make([]serviceStatus, 0, len(probeList))
	for _, name := range probeList {
		pid, err := w.ctx.Consume(name)
		if err != nil {
			out = append(out, serviceStatus{Name: name, Alive: false, Note: reasonOf(err)})
			continue
		}
		out = append(out, serviceStatus{Name: name, Alive: true, Provider: pid})
	}
	return out
}

// model 返回当前模型（llm.v1 ping 很轻，只读配置）。拿不到就空串。
func (w *WebUI) model() string {
	if _, err := w.ctx.Consume("llm.v1"); err != nil {
		return ""
	}
	raw, err := w.ctx.Invoke("llm.v1", "ping", map[string]any{})
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

// sendConv 发一条消息。
func (w *WebUI) sendConv(text string, atts []map[string]any) error {
	w.mu.Lock()
	sid := w.sessionID
	w.mu.Unlock()
	if sid == "" {
		sid = w.currentSession() // 首次兜底：还没拉过历史时取一次
	}
	params := map[string]any{"sessionId": sid, "text": text}
	if len(atts) > 0 {
		params["attachments"] = atts
	}
	_, err := w.conv("send", params)
	return err
}

// session 处理新建 / 切换 / 删除。
func (w *WebUI) session(action, id string) {
	switch action {
	case "new":
		_, _ = w.conv("newSession", nil)
	case "switch":
		_, _ = w.conv("switch", map[string]any{"sessionId": id})
	case "delete":
		_, _ = w.conv("deleteSession", map[string]any{"sessionId": id})
	}
}

// currentSession 取当前会话 id。send 需要带上它（服务端也维护一份）。
func (w *WebUI) currentSession() string {
	s := w.snapshot()
	return s.SessionID
}

// conv 调一次 conversation.v1 并返回原始结果。
func (w *WebUI) conv(method string, params map[string]any) (json.RawMessage, error) {
	if _, err := w.ctx.Consume(convService); err != nil {
		return nil, fmt.Errorf("未安装对话插件（conversation.v1）")
	}
	if params == nil {
		params = map[string]any{}
	}
	return w.ctx.Invoke(convService, method, params)
}

// applyConv 把 conversation.v1 的返回解析进快照。
func applyConv(s *snapshot, raw json.RawMessage) {
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
	s.SessionID = out.SessionID
	s.Name = out.Name
	s.Note = out.Note
	for _, it := range out.Sessions {
		s.Sessions = append(s.Sessions, sessionInfo{
			SessionID: it.SessionID, Name: it.Name, Nodes: it.Nodes, Updated: it.Updated,
		})
	}
	for _, it := range out.Path {
		s.Path = append(s.Path, msgView{
			ID: it.ID, Role: it.Role, Text: it.Text, At: it.At,
			Version: it.Version, Total: it.Total, Via: it.Via, Err: it.Err, Trunc: it.Trunc,
		})
	}
}

// ---------------------------------------------------------------------------
// 事件 → trace / 快照推送
// ---------------------------------------------------------------------------

type traceState struct {
	Round int
	Phase string
	Model string
	Think string
	Done  bool
	Steps []traceStep
}

type traceStep struct {
	Tool string `json:"tool"`
	Args string `json:"args"`
	Ms   int64  `json:"ms"`
	OK   bool   `json:"ok"`
	Done bool   `json:"done"`
}

// onEvent 处理订阅到的事件：更新 AI 过程、推送快照。
func (w *WebUI) onEvent(name string, payload json.RawMessage) {
	switch name {
	case "chat.response", "agent.done", "session.updated":
		w.pushSnapshot()
	case "plugin.deleted", "plugin.state":
		// 插件清单变了：广播一条专属事件，前端「插件」页当场重扫
		w.hub.broadcast("plugins", map[string]any{"changed": true})
	case "agent.think":
		w.onThink(payload)
	case "chat.tool.before":
		w.onToolBefore(payload)
	case "chat.tool.after":
		w.onToolAfter(payload)
	}
}

func (w *WebUI) onThink(payload json.RawMessage) {
	var ev struct {
		Round     int    `json:"round"`
		Phase     string `json:"phase"`
		Thinking  string `json:"thinking"`
		ToolCalls int    `json:"toolCalls"`
		Model     string `json:"model"`
		Error     string `json:"error"`
	}
	_ = json.Unmarshal(payload, &ev)

	w.mu.Lock()
	t := &w.trace
	t.Round = ev.Round + 1
	if ev.Model != "" {
		t.Model = ev.Model
	}
	switch ev.Phase {
	case "start":
		t.Phase = "正在思考"
		t.Done = false
	case "done":
		if ev.ToolCalls > 0 {
			t.Phase = fmt.Sprintf("准备调用 %d 个工具", ev.ToolCalls)
		} else {
			t.Phase = "正在整理回答"
		}
		if ev.Thinking != "" {
			t.Think = ev.Thinking
		}
	case "error":
		t.Phase = "模型调用失败：" + ev.Error
		t.Done = true
	}
	w.mu.Unlock()
	w.broadcastTrace() // 锁外广播：broadcastTrace 自己也要拿 w.mu，锁内调用会死锁
}

func (w *WebUI) onToolBefore(payload json.RawMessage) {
	var ev struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	_ = json.Unmarshal(payload, &ev)
	w.mu.Lock()
	w.trace.Steps = append(w.trace.Steps, traceStep{Tool: ev.Tool, Args: shortArgs(ev.Args)})
	w.trace.Phase = "正在调用 " + ev.Tool
	w.trace.Done = false
	w.mu.Unlock()
	w.broadcastTrace()
}

func (w *WebUI) onToolAfter(payload json.RawMessage) {
	var ev struct {
		Tool string `json:"tool"`
		OK   bool   `json:"ok"`
		Ms   int64  `json:"ms"`
	}
	_ = json.Unmarshal(payload, &ev)
	w.mu.Lock()
	for i := len(w.trace.Steps) - 1; i >= 0; i-- {
		if w.trace.Steps[i].Tool == ev.Tool && !w.trace.Steps[i].Done {
			w.trace.Steps[i].Done = true
			w.trace.Steps[i].OK = ev.OK
			w.trace.Steps[i].Ms = ev.Ms
			break
		}
	}
	w.mu.Unlock()
	w.broadcastTrace()
}

func (w *WebUI) resetTrace() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.trace = traceState{Phase: "正在思考"}
}

// headline 拼出过程框第一行。
func (t *traceState) headline() string {
	round := ""
	if t.Round > 0 {
		round = fmt.Sprintf("第 %d 轮 · ", t.Round)
	}
	if t.Done {
		n := 0
		for _, s := range t.Steps {
			if s.Done {
				n++
			}
		}
		if n == 0 {
			return round + "完成"
		}
		return round + fmt.Sprintf("完成 · %d 次工具调用", n)
	}
	if t.Phase == "" {
		return round + "正在生成…"
	}
	return round + t.Phase
}

// broadcastTrace 把当前过程广播给所有 SSE 客户端。
func (w *WebUI) broadcastTrace() {
	w.mu.Lock()
	t := w.trace
	w.mu.Unlock()
	w.hub.broadcast("trace", map[string]any{
		"headline": t.headline(),
		"steps":    t.Steps,
		"think":    truncate(t.Think, 240),
		"done":     t.Done,
	})
}

// pushSnapshot 重新拉一次会话状态并广播。
func (w *WebUI) pushSnapshot() {
	w.hub.broadcast("snapshot", w.snapshot())
}

// ---------------------------------------------------------------------------
// hub：SSE 广播
// ---------------------------------------------------------------------------

type hub struct {
	mu   sync.Mutex
	subs map[chan sseMsg]struct{}
}

type sseMsg struct {
	event string
	data  string
}

func newHub() *hub {
	return &hub{subs: make(map[chan sseMsg]struct{})}
}

func (h *hub) subscribe() chan sseMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan sseMsg, 64)
	h.subs[ch] = struct{}{}
	return ch
}

func (h *hub) unsubscribe(ch chan sseMsg) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, ch)
}

func (h *hub) broadcast(event string, data any) {
	blob, err := json.Marshal(data)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- sseMsg{event: event, data: string(blob)}:
		default: // 慢客户端：丢弃，别拖垮广播
		}
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(v)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// waitPort 轮询拨号，直到 host:port 能连上或超时。开桌面窗口前用它确认
// HTTP 服务已就绪，避免窗口弹出时页面还是"无法访问"。
func waitPort(host, port string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort(host, port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(120 * time.Millisecond)
	}
	return false
}

func reasonOf(err error) string {
	switch {
	case err == nil:
		return ""
	case plugin.IsCode(err, plugin.CodeServiceNotFound):
		return "未安装"
	case plugin.IsCode(err, plugin.CodeProviderDead):
		return "提供者已死"
	default:
		return err.Error()
	}
}

func shortArgs(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "{}" {
		return ""
	}
	s := strings.Join(strings.Fields(string(raw)), " ")
	return truncate(s, 40)
}

func truncate(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}

func main() {
	if err := plugin.Serve(&WebUI{}); err != nil {
		plugin.Logf("error", "Web UI 退出: %v", err)
		os.Exit(1)
	}
}
