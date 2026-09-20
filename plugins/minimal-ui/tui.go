package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// 按键解析：把原始字节流还原成按键与字符
// ---------------------------------------------------------------------------

type keyKind int

const (
	keyRune keyKind = iota
	keyEnter
	keyBackspace
	keyDelete
	keyLeft
	keyRight
	keyHome
	keyEnd
	keyUp
	keyDown
	keyEsc
	keyCtrlL
	keyCtrlC
	keyUnknown
)

type keyEvent struct {
	kind keyKind
	r    rune
}

// escapeSeqs 覆盖方向键、Home/End、Delete 的常见编码。
var escapeSeqs = map[string]keyKind{
	"[A": keyUp, "[B": keyDown, "[C": keyRight, "[D": keyLeft,
	"[H": keyHome, "[F": keyEnd,
	"[1~": keyHome, "[4~": keyEnd, "[7~": keyHome, "[8~": keyEnd,
	"[3~": keyDelete,
}

func isEscapePrefix(seq string) bool {
	for known := range escapeSeqs {
		if strings.HasPrefix(known, seq) {
			return true
		}
	}
	return false
}

// keyParser 是逐字节的按键状态机，同时负责拼装多字节 UTF-8 字符。
type keyParser struct {
	inEsc bool
	esc   []byte
	utf8  []byte
}

func (p *keyParser) feed(b byte) []keyEvent {
	if p.inEsc {
		p.esc = append(p.esc, b)
		seq := string(p.esc)

		if kind, ok := escapeSeqs[seq]; ok {
			p.inEsc, p.esc = false, nil
			return []keyEvent{{kind: kind}}
		}
		if isEscapePrefix(seq) {
			return nil // 序列还不完整
		}

		// 未知的 CSI/SS3 序列：一直吞到终止字节（0x40–0x7E），
		// 否则 "[9Z" 这种会变成一堆垃圾字符跑进输入框。
		if first := p.esc[0]; first == '[' || first == 'O' {
			for _, c := range p.esc[1:] {
				if c >= 0x40 && c <= 0x7e {
					p.inEsc, p.esc = false, nil
					return []keyEvent{{kind: keyUnknown}}
				}
			}
			return nil
		}

		// ESC + 单字符（等价于 Alt+键）：两个键都发出去，避免丢字符
		c := p.esc[0]
		p.inEsc, p.esc = false, nil
		out := []keyEvent{{kind: keyEsc}}
		if c >= 0x20 {
			out = append(out, p.feed(c)...)
		}
		return out
	}

	switch b {
	case 0x1b:
		p.inEsc, p.esc = true, nil
		return nil
	case 0x0d, 0x0a:
		return []keyEvent{{kind: keyEnter}}
	case 0x7f, 0x08:
		return []keyEvent{{kind: keyBackspace}}
	case 0x03:
		return []keyEvent{{kind: keyCtrlC}}
	case 0x0c:
		return []keyEvent{{kind: keyCtrlL}}
	case 0x09:
		return []keyEvent{{kind: keyUnknown}}
	}
	if b < 0x20 {
		return []keyEvent{{kind: keyUnknown}}
	}

	// UTF-8：不到一个完整字符就继续攒
	p.utf8 = append(p.utf8, b)
	if !utf8.FullRune(p.utf8) {
		return nil
	}
	r, _ := utf8.DecodeRune(p.utf8)
	p.utf8 = nil
	if r == utf8.RuneError {
		return []keyEvent{{kind: keyUnknown}}
	}
	return []keyEvent{{kind: keyRune, r: r}}
}

// ---------------------------------------------------------------------------
// TUI 应用
// ---------------------------------------------------------------------------

type chatOutcome struct {
	reply string
	via   string
	err   error
}

// uiBackend 是 TUI 需要的插件能力。抽成接口是为了能在测试里驱动整个主循环。
type uiBackend interface {
	probeServices() []serviceStatus
	chat(text string) (string, string, error)
	runTool(service, method, argText string) (string, error)
	eventsEnabled() bool
	setEventsEnabled(bool)
}

// tuiApp 驱动全屏界面。
//
// 所有 uiState 的修改都发生在 loop 这一个 goroutine 上：
// 外部（RPC 事件、异步回复）通过 post 投递闭包，因此不需要任何锁。
type tuiApp struct {
	ui     *uiState
	scr    screen
	in     io.Reader
	keys   *keyParser
	colors bool
	plug   uiBackend

	hist    []string
	histIdx int

	apply    chan func()
	done     chan struct{}
	stopOnce sync.Once
}

func newTUIApp(scr screen, in io.Reader, colors bool, plug uiBackend, coreVersion, apiVersion, pluginID string) *tuiApp {
	return &tuiApp{
		ui: &uiState{
			coreVersion: coreVersion,
			apiVersion:  apiVersion,
			pluginID:    pluginID,
			services:    []serviceStatus{},
			messages: []chatMsg{
				{role: "系统", at: nowHM(), text: "欢迎使用 AI 工作平台。\n直接输入文字即发送；输入 /help 查看命令。"},
			},
		},
		scr:    scr,
		in:     in,
		keys:   &keyParser{},
		colors: colors,
		plug:   plug,
		apply:  make(chan func(), 64),
		done:   make(chan struct{}),
	}
}

// post 把一个状态修改投递到 UI 线程执行。
func (a *tuiApp) post(fn func()) {
	select {
	case a.apply <- fn:
	case <-a.done:
	}
}

// Start 拉取一次服务状态并进入事件循环（阻塞，应在 goroutine 中调用）。
func (a *tuiApp) Start() {
	defer a.Stop()
	a.ui.services = a.plug.probeServices()
	a.loop()
}

// PushNotice 让别的插件把一条提示打到对话区。
func (a *tuiApp) PushNotice(text string) {
	a.post(func() { a.pushSystem(text) })
}

// Stop 让事件循环退出。可重复调用。
func (a *tuiApp) Stop() {
	a.stopOnce.Do(func() { close(a.done) })
}

// PushEvent 由 RPC 线程调用，把事件塞进侧栏。
func (a *tuiApp) PushEvent(name, summary string) {
	if !a.plug.eventsEnabled() {
		return
	}
	line := nowHM() + " " + name
	if summary != "" {
		line += " " + summary
	}
	a.post(func() {
		a.ui.events = append(a.ui.events, line)
		if len(a.ui.events) > 12 {
			a.ui.events = a.ui.events[len(a.ui.events)-12:]
		}
	})
}

func (a *tuiApp) loop() {
	results := make(chan chatOutcome, 4)

	keys := make(chan byte, 256)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := a.in.Read(buf)
			if n > 0 {
				select {
				case keys <- buf[0]:
				default:
				}
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()

	ticker := time.NewTicker(110 * time.Millisecond)
	defer ticker.Stop()

	a.render()
	for {
		select {
		case b, ok := <-keys:
			if !ok {
				return
			}
			for _, k := range a.keys.feed(b) {
				if a.handleKey(k, results) {
					return
				}
			}
			a.render()
		case out := <-results:
			a.applyOutcome(out)
			a.render()
		case fn := <-a.apply:
			fn()
			a.render()
		case <-a.done:
			return
		case <-ticker.C:
			if a.ui.busy {
				a.ui.spinner++
				a.render()
			}
		}
	}
}

func (a *tuiApp) render() {
	w, h := a.scr.Size()
	frame := renderFrame(a.ui, w, h, a.colors)
	if frame == "" {
		return
	}
	a.scr.WriteString(fmt.Sprintf("\033[H%s\033[%d;%dH", frame, h-1, cursorPos(a.ui, w)+1))
}

func (a *tuiApp) handleKey(k keyEvent, results chan chatOutcome) (quit bool) {
	ui := a.ui
	switch k.kind {
	case keyRune:
		ui.input = append(ui.input, 0)
		copy(ui.input[ui.cursor+1:], ui.input[ui.cursor:])
		ui.input[ui.cursor] = k.r
		ui.cursor++
	case keyBackspace:
		if ui.cursor > 0 {
			ui.input = append(ui.input[:ui.cursor-1], ui.input[ui.cursor:]...)
			ui.cursor--
		}
	case keyDelete:
		if ui.cursor < len(ui.input) {
			ui.input = append(ui.input[:ui.cursor], ui.input[ui.cursor+1:]...)
		}
	case keyLeft:
		if ui.cursor > 0 {
			ui.cursor--
		}
	case keyRight:
		if ui.cursor < len(ui.input) {
			ui.cursor++
		}
	case keyHome:
		ui.cursor = 0
	case keyEnd:
		ui.cursor = len(ui.input)
	case keyUp:
		a.historyBack()
	case keyDown:
		a.historyForward()
	case keyCtrlL:
		ui.messages = nil
	case keyEsc:
		ui.input = nil
		ui.cursor = 0
	case keyCtrlC:
		return true
	case keyEnter:
		return a.submit(results)
	}
	return false
}

func (a *tuiApp) historyBack() {
	if len(a.hist) == 0 {
		return
	}
	if a.histIdx > 0 {
		a.histIdx--
	}
	a.ui.input = []rune(a.hist[a.histIdx])
	a.ui.cursor = len(a.ui.input)
}

func (a *tuiApp) historyForward() {
	if len(a.hist) == 0 {
		return
	}
	if a.histIdx < len(a.hist)-1 {
		a.histIdx++
		a.ui.input = []rune(a.hist[a.histIdx])
	} else {
		a.histIdx = len(a.hist)
		a.ui.input = nil
	}
	a.ui.cursor = len(a.ui.input)
}

func (a *tuiApp) submit(results chan chatOutcome) (quit bool) {
	text := strings.TrimSpace(string(a.ui.input))
	a.ui.input = nil
	a.ui.cursor = 0
	if text == "" {
		return false
	}
	if strings.HasPrefix(text, "/") {
		return a.runCommand(text, results)
	}

	a.hist = append(a.hist, text)
	a.histIdx = len(a.hist)
	a.ui.messages = append(a.ui.messages, chatMsg{role: "你", at: nowHM(), text: text})
	a.ui.busy = true
	a.ui.spinner = 0

	plug := a.plug
	go func() {
		reply, via, err := plug.chat(text)
		select {
		case results <- chatOutcome{reply: reply, via: via, err: err}:
		case <-a.done:
		}
	}()
	return false
}

func (a *tuiApp) applyOutcome(out chatOutcome) {
	a.ui.busy = false
	if out.err != nil {
		a.ui.messages = append(a.ui.messages, chatMsg{role: "错误", at: nowHM(), text: out.err.Error()})
		return
	}
	a.ui.messages = append(a.ui.messages, chatMsg{role: "AI", at: nowHM(), text: humanizeReply(out.reply, out.via)})
}

func (a *tuiApp) runCommand(text string, results chan chatOutcome) (quit bool) {
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])
	arg := ""
	if len(fields) > 1 {
		arg = strings.ToLower(fields[1])
	}

	switch cmd {
	case "/exit", "/quit", "/q":
		return true
	case "/help", "/?":
		a.pushSystem(helpText)
	case "/clear", "/cls":
		a.ui.messages = nil
		a.ui.events = nil
	case "/services", "/svc":
		plug := a.plug
		go func() {
			svc := plug.probeServices()
			a.post(func() { a.ui.services = svc })
		}()
	case "/tool":
		a.runTool(strings.TrimSpace(text[len(cmd):]), results)
	case "/events":
		switch arg {
		case "on":
			a.plug.setEventsEnabled(true)
			a.pushSystem("事件显示已开启。")
		case "off":
			a.plug.setEventsEnabled(false)
			a.ui.events = nil
			a.pushSystem("事件显示已关闭。")
		default:
			a.pushSystem("用法：/events on|off")
		}
	default:
		a.pushSystem("未知命令：" + cmd + "（输入 /help 查看全部命令）")
	}
	return false
}

func (a *tuiApp) runTool(rest string, results chan chatOutcome) {
	fields := strings.SplitN(rest, " ", 3)
	if len(fields) < 2 || fields[0] == "" || fields[1] == "" {
		a.pushSystem("用法：/tool <service> <method> [json参数]")
		return
	}
	service, method, argText := fields[0], fields[1], ""
	if len(fields) == 3 {
		argText = strings.TrimSpace(fields[2])
	}

	a.ui.messages = append(a.ui.messages, chatMsg{role: "你", at: nowHM(), text: "/tool " + service + " " + method})
	a.ui.busy = true
	plug := a.plug
	go func() {
		reply, err := plug.runTool(service, method, argText)
		select {
		case results <- chatOutcome{reply: reply, via: service + "." + method, err: err}:
		case <-a.done:
		}
	}()
}

func (a *tuiApp) pushSystem(text string) {
	a.ui.messages = append(a.ui.messages, chatMsg{role: "系统", at: nowHM(), text: text})
}

func nowHM() string { return time.Now().Format("15:04:05") }

const helpText = `可用命令：
  /help              显示本帮助
  /services          重新探测服务链路
  /tool <svc> <m> [json]  调试：直接调用任意服务
  /events on|off     开关侧栏的事件总线显示
  /clear             清空对话与事件
  /exit              退出界面（核心继续运行）

键盘：
  回车 发送 · Shift 不支持换行，请用 /tool 传多行 JSON
  ↑↓   翻阅历史输入
  ←→   Home/End 编辑光标
  Ctrl+C 关闭整个平台（界面会先把终端还原）`
