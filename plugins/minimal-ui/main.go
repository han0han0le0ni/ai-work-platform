// Command minimal-ui 是官方极简 UI 插件。
//
// 硬通道约束：插件进程的 stdin/stdout 属于 JSON-RPC，界面必须绕开它们，
// 因此本插件直接打开“控制终端”渲染（Windows: CONIN$/CONOUT$，Unix: /dev/tty）。
//
// 界面有两档，自动选择：
//   - 全屏 TUI（可交互终端）：边框、分栏、CJK 对齐、方向键编辑、实时事件
//   - 行式界面（拿不到原始模式时）：逐行问答，永不失败
//
// 它不认识 LLM，也不知道会话怎么存——它只会 core.invoke。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"

	"aiwork/sdk/go/plugin"
)

// 颜色与样式（ANSI）
const (
	cReset   = "\033[0m"
	cDim     = "\033[2m"
	cBold    = "\033[1m"
	cAccent  = "\033[38;5;111m"
	cAccent2 = "\033[38;5;141m"
	cOK      = "\033[38;5;79m"
	cWarn    = "\033[38;5;215m"
	cBad     = "\033[38;5;203m"
	cBorder  = "\033[38;5;240m"
)

// probeList 是 UI 探测链路的候选服务。
var probeList = []string{
	"session.v1",
	"agent.v1",
	"llm.v1",
	"toolExecutor.v1",
	"workspace.v1",
	"pluginManager.v1",
	"readFile.v1",
	"writeFile.v1",
	"sandbox.v1",
	"secrets.v1",
}

// UI 实现 plugin.Plugin。
type UI struct {
	ctx  *plugin.Context
	term *terminal
	app  *tuiApp

	eventsOn atomic.Bool
}

// Activate 打开控制终端并进入界面。
func (u *UI) Activate(ctx *plugin.Context) error {
	u.ctx = ctx

	if err := ctx.Provide("ui.v1", map[string]any{
		"kind":        "terminal",
		"description": "终端界面（全屏 TUI，自动退化为行式）",
	}); err != nil {
		return err
	}

	for _, name := range []string{"core.log", "plugin.state", "chat.message", "chat.response"} {
		event := name
		ctx.On(event, func(payload json.RawMessage) {
			if u.app != nil {
				u.app.PushEvent(event, summarize(payload))
			}
			// 插件是逐个启动的，UI 往往先于它们就绪。不刷新的话，界面会一直停在
			// 启动那一刻的快照上，把「稍后注册的插件」永久显示成「未安装」。
			if event == "plugin.state" && bytes.Contains(payload, []byte("active")) {
				u.refreshServices()
			}
		})
	}

	term, err := openTerminal()
	if err != nil {
		// 没有终端（例如被当作后台服务启动）：静默模式，核心照常运行
		ctx.Log("warn", "没有可用的控制终端，极简 UI 进入静默模式："+err.Error(), nil)
		return nil
	}
	u.term = term

	// Ctrl+C 会同时通知核心与本插件：先把终端还原，再退出，避免留下坏终端
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		term.Leave()
		os.Exit(0)
	}()

	if err := term.Enter(); err == nil {
		u.app = newTUIApp(term, term.in, term.color, u, ctx.CoreVersion, ctx.APIVersion, ctx.PluginID)
		ctx.Log("info", "极简 UI（全屏终端）已就绪", nil)
		go func() {
			defer term.Leave()
			u.app.Start()
			term.Leave()
		}()
		return nil
	}

	ctx.Log("info", "当前终端不支持全屏模式，极简 UI 退化为行式界面", nil)
	go func() {
		defer term.Leave()
		u.runREPL()
	}()
	return nil
}

// Handle 处理 core.invoke。其他插件可以通过 ui.v1.notify 往界面推消息。
func (u *UI) Handle(service, method string, args json.RawMessage) (any, error) {
	if service != "ui.v1" {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "极简 UI 不提供服务 %s", service)
	}
	switch method {
	case "info":
		services := map[string]any{}
		for _, s := range u.probeServices() {
			services[s.name] = map[string]any{"alive": s.alive, "pluginId": s.provider}
		}
		return map[string]any{
			"kind":     "terminal",
			"pluginId": u.ctx.PluginID,
			"services": services,
		}, nil
	case "notify":
		var in struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(args, &in)
		if u.app != nil {
			u.app.PushNotice(in.Text)
		} else if u.term != nil {
			u.term.WriteString("· " + in.Text + "\n")
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "ui.v1 不支持方法 %s", method)
	}
}

// Deactivate 还原终端并退出界面。
func (u *UI) Deactivate() error {
	if u.app != nil {
		u.app.Stop()
	}
	if u.term != nil {
		u.term.Leave()
	}
	return nil
}

// ---------------------------------------------------------------------------
// 事件开关
// ---------------------------------------------------------------------------

func (u *UI) eventsEnabled() bool      { return u.eventsOn.Load() }
func (u *UI) setEventsEnabled(on bool) { u.eventsOn.Store(on) }

// ---------------------------------------------------------------------------
// 服务链路探测
// ---------------------------------------------------------------------------

func (u *UI) probeServices() []serviceStatus {
	out := make([]serviceStatus, 0, len(probeList))
	for _, name := range probeList {
		pid, err := u.ctx.Consume(name)
		if err != nil {
			out = append(out, serviceStatus{name: name, note: reasonOf(err)})
			continue
		}
		out = append(out, serviceStatus{name: name, alive: true, provider: pid})
	}
	return out
}

// refreshServices 重新探测链路并把结果推回界面。
func (u *UI) refreshServices() {
	svc := u.probeServices()
	if u.app == nil {
		return
	}
	u.app.post(func() { u.app.ui.services = svc })
}

func reasonOf(err error) string {
	switch {
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

// ---------------------------------------------------------------------------
// 聊天链路
// ---------------------------------------------------------------------------

// chat 按“会话 -> Agent”的顺序探测可用链路。
//
// 核心不关心缺哪个插件，UI 也不关心；它只负责把能用的用起来。
func (u *UI) chat(text string) (string, string, error) {
	_ = u.ctx.Emit("chat.message", map[string]any{"text": text, "from": "ui"})

	reply, via, err := u.dispatch(text)
	if err != nil {
		return "", "", err
	}
	_ = u.ctx.Emit("chat.response", map[string]any{"via": via, "reply": reply})
	return string(reply), via, nil
}

func (u *UI) dispatch(text string) (json.RawMessage, string, error) {
	if _, err := u.ctx.Consume("session.v1"); err == nil {
		out, err := u.ctx.Invoke("session.v1", "sendMessage", map[string]any{"text": text})
		if err == nil {
			return out, "session.v1", nil
		}
		if !plugin.IsCode(err, plugin.CodeServiceNotFound) {
			return nil, "session.v1", err
		}
	}
	if _, err := u.ctx.Consume("agent.v1"); err == nil {
		out, err := u.ctx.Invoke("agent.v1", "run", map[string]any{
			"messages": []map[string]string{{"role": "user", "content": text}},
		})
		if err == nil {
			return out, "agent.v1", nil
		}
		return nil, "agent.v1", err
	}
	return nil, "", errors.New("链路未装齐：至少需要 session.v1 / agent.v1 中的一个")
}

// runTool 调试用：直接调用任意服务的任意方法。
func (u *UI) runTool(service, method, argText string) (string, error) {
	params := any(map[string]any{})
	if argText != "" {
		if err := json.Unmarshal([]byte(argText), &params); err != nil {
			return "", fmt.Errorf("参数不是合法 JSON：%v", err)
		}
	}
	resp, err := u.ctx.Invoke(service, method, params)
	if err != nil {
		return "", err
	}
	return string(resp), nil
}

// ---------------------------------------------------------------------------
// 回复美化
// ---------------------------------------------------------------------------

// primaryKeys 是回复里“主要信息”的候选字段，命中后不再重复展示。
var primaryKeys = []string{"echo", "text", "content", "message", "reply", "answer"}

const maxReplyChars = 4000

// humanizeReply 把结构化响应变成一行人话 + 其余字段的缩进 JSON。
func humanizeReply(reply, via string) string {
	if strings.TrimSpace(reply) == "" {
		return "（空响应）"
	}

	var plain string
	if json.Unmarshal([]byte(reply), &plain) == nil {
		return clip(plain) + "\n" + viaLine(via)
	}

	var obj map[string]any
	if json.Unmarshal([]byte(reply), &obj) == nil {
		for _, key := range primaryKeys {
			v, ok := obj[key].(string)
			if !ok || v == "" {
				continue
			}
			delete(obj, key)
			rest := prettyJSONAny(obj)
			if rest == "{}" || rest == "null" {
				return clip(v) + "\n" + viaLine(via)
			}
			return clip(v) + "\n" + rest + "\n" + viaLine(via)
		}
		return prettyJSON(reply) + "\n" + viaLine(via)
	}
	return clip(reply) + "\n" + viaLine(via)
}

func viaLine(via string) string {
	if via == "" {
		return ""
	}
	return "经由 " + via + " 返回"
}

func clip(s string) string {
	rs := []rune(s)
	if len(rs) <= maxReplyChars {
		return s
	}
	return string(rs[:maxReplyChars]) + "\n…（内容过长，已截断）"
}

func prettyJSON(raw string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return raw
	}
	return buf.String()
}

func prettyJSONAny(v any) string {
	blob, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(blob)
}

// summarize 把事件载荷压成一行，塞进侧栏。
func summarize(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	s := strings.Join(strings.Fields(string(payload)), " ")
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}

func main() {
	if err := plugin.Serve(&UI{}); err != nil {
		plugin.Logf("error", "极简 UI 退出: %v", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// 行式界面：全屏模式不可用时的兜底
// ---------------------------------------------------------------------------

// paint 只在终端支持 ANSI 时着色。
func (u *UI) paint(code, s string) string {
	if u.term == nil || !u.term.color || code == "" {
		return s
	}
	return code + s + cReset
}

func (u *UI) replServices() {
	for _, s := range u.probeServices() {
		mark, tail := "✗", s.note
		if s.alive {
			mark, tail = "✓", s.provider
		}
		u.term.WriteString(fmt.Sprintf("  %s %-16s %s\n",
			u.paint(cOK, mark), s.name, u.paint(cDim, tail)))
	}
}

func (u *UI) runREPL() {
	t := u.term
	t.WriteString("\n=== AI 工作平台 · 极简 UI（行式模式）===\n")
	t.WriteString(u.paint(cDim, "当前终端不支持全屏模式，已退化。输入 /help 查看命令。") + "\n\n")
	u.replServices()
	t.WriteString("\n")

	in := bufio.NewReader(t.in)
	for {
		t.WriteString(u.paint(cAccent, "你 › "))
		line, err := in.ReadString('\n')
		text := strings.TrimSpace(line)
		switch {
		case text != "" && strings.HasPrefix(text, "/"):
			if u.replCommand(text) {
				return
			}
		case text != "":
			reply, via, cerr := u.chat(text)
			if cerr != nil {
				t.WriteString(u.paint(cBad, "错误 › ") + cerr.Error() + "\n")
			} else {
				t.WriteString(u.paint(cAccent2, "AI › ") + humanizeReply(reply, via) + "\n")
			}
		}
		if err != nil {
			t.WriteString(u.paint(cDim, "输入已结束，界面退出。") + "\n")
			return
		}
	}
}

func (u *UI) replCommand(text string) (quit bool) {
	fields := strings.Fields(text)
	switch strings.ToLower(fields[0]) {
	case "/exit", "/quit", "/q":
		u.term.WriteString(u.paint(cDim, "界面已退出。核心仍在运行；在核心所在终端按 Ctrl+C 可关闭整个平台。") + "\n")
		return true
	case "/services", "/svc":
		u.replServices()
	case "/tool":
		rest := strings.TrimSpace(text[len("/tool"):])
		parts := strings.SplitN(rest, " ", 3)
		if len(parts) < 2 {
			u.term.WriteString(u.paint(cWarn, "用法：/tool <service> <method> [json参数]") + "\n")
			break
		}
		argText := ""
		if len(parts) == 3 {
			argText = strings.TrimSpace(parts[2])
		}
		reply, err := u.runTool(parts[0], parts[1], argText)
		if err != nil {
			u.term.WriteString(u.paint(cBad, "错误 › ") + err.Error() + "\n")
			break
		}
		u.term.WriteString(u.paint(cAccent2, "结果 › ") + prettyJSON(reply) + "\n")
	case "/help", "/?":
		u.term.WriteString(u.paint(cDim, "可用命令：/help /services /tool <svc> <m> [json] /clear /exit") + "\n")
	case "/clear", "/cls":
		u.term.WriteString("\033[2J\033[H")
	default:
		u.term.WriteString(u.paint(cWarn, "未知命令："+fields[0]) + "\n")
	}
	return false
}
