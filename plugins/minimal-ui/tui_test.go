package main

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 按键解析
// ---------------------------------------------------------------------------

func feedAll(p *keyParser, s string) []keyEvent {
	var out []keyEvent
	for i := 0; i < len(s); i++ {
		out = append(out, p.feed(s[i])...)
	}
	return out
}

func TestKeyParserBasic(t *testing.T) {
	got := feedAll(&keyParser{}, "ab\r")
	if len(got) != 3 || got[0].r != 'a' || got[1].r != 'b' || got[2].kind != keyEnter {
		t.Fatalf("基本输入解析不正确: %+v", got)
	}
}

// 方向键以转义序列到达，必须被识别成按键而不是一串字符
func TestKeyParserArrows(t *testing.T) {
	cases := map[string]keyKind{
		"\x1b[A":  keyUp,
		"\x1b[B":  keyDown,
		"\x1b[C":  keyRight,
		"\x1b[D":  keyLeft,
		"\x1b[3~": keyDelete,
		"\x1b[H":  keyHome,
	}
	for seq, want := range cases {
		got := feedAll(&keyParser{}, seq)
		if len(got) != 1 || got[0].kind != want {
			t.Errorf("%q 应解析为 %v，实际 %+v", seq, want, got)
		}
	}
}

// 未知转义序列必须整段吞掉，不能把序列字节当输入
func TestKeyParserUnknownEscape(t *testing.T) {
	got := feedAll(&keyParser{}, "\x1b[9Zx")
	if len(got) != 2 || got[0].kind != keyUnknown || got[1].r != 'x' {
		t.Fatalf("未知 CSI 序列应被吞掉，只保留后续字符，实际 %+v", got)
	}
	// ESC + 单字符（Alt+键）不能丢字符
	got = feedAll(&keyParser{}, "\x1bx")
	if len(got) != 2 || got[0].kind != keyEsc || got[1].r != 'x' {
		t.Fatalf("Alt+键应解析为 ESC + 字符，实际 %+v", got)
	}
}

// 中文是多字节 UTF-8，必须逐字节攒齐后还原成一个字符
func TestKeyParserUTF8(t *testing.T) {
	got := feedAll(&keyParser{}, "中文")
	if len(got) != 2 || got[0].r != '中' || got[1].r != '文' {
		t.Fatalf("UTF-8 解析不正确: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// 驱动整个 TUI 主循环
// ---------------------------------------------------------------------------

type fakeScreen struct {
	mu   sync.Mutex
	w, h int
	buf  strings.Builder
}

func (f *fakeScreen) Size() (int, int) { return f.w, f.h }

func (f *fakeScreen) WriteString(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buf.WriteString(s)
}

func (f *fakeScreen) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.String()
}

// scriptReader 先吐出脚本字节，然后卡在 gate 上，模拟“终端还开着”。
type scriptReader struct {
	data []byte
	pos  int
	gate chan struct{}
}

func (r *scriptReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		<-r.gate
		return 0, io.EOF
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

type fakeBackend struct {
	events bool
	tools  []string
}

func (b *fakeBackend) probeServices() []serviceStatus {
	return []serviceStatus{
		{name: "echo.v1", alive: true, provider: "com.official.echo"},
		{name: "llm.v1", note: "未安装"},
	}
}

func (b *fakeBackend) chat(text string) (string, string, error) {
	return `{"echo":"` + text + `"}`, "echo.v1", nil
}

func (b *fakeBackend) runTool(service, method, argText string) (string, error) {
	b.tools = append(b.tools, service+"."+method+" "+argText)
	return `{"ok":true}`, nil
}

func (b *fakeBackend) eventsEnabled() bool      { return b.events }
func (b *fakeBackend) setEventsEnabled(on bool) { b.events = on }

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func TestTUILoopChat(t *testing.T) {
	scr := &fakeScreen{w: 90, h: 20}
	r := &scriptReader{data: []byte("你好\r"), gate: make(chan struct{})}
	be := &fakeBackend{}
	app := newTUIApp(scr, r, false, be, "1.0.0", "1", "com.test.ui")

	done := make(chan struct{})
	go func() { defer close(done); app.Start() }()

	waitUntil(t, "回复出现在界面上", func() bool {
		return strings.Contains(scr.String(), "经由 echo.v1 返回")
	})

	out := scr.String()
	if !strings.Contains(out, "你好") {
		t.Errorf("界面上应出现用户输入")
	}
	if !strings.Contains(out, "✓ echo.v1") || !strings.Contains(out, "✗ llm.v1") {
		t.Errorf("侧栏应展示服务链路状态")
	}

	close(r.gate)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("输入结束后 TUI 未退出")
	}
}

func TestTUILoopExitCommand(t *testing.T) {
	scr := &fakeScreen{w: 90, h: 20}
	r := &scriptReader{data: []byte("/exit\r"), gate: make(chan struct{})}
	app := newTUIApp(scr, r, false, &fakeBackend{}, "1.0.0", "1", "com.test.ui")

	done := make(chan struct{})
	go func() { defer close(done); app.Start() }()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("/exit 应当结束界面")
	}
	close(r.gate)
}

// 方向键上翻历史后回车，应当重新提交上一条
func TestTUILoopHistory(t *testing.T) {
	scr := &fakeScreen{w: 90, h: 24}
	r := &scriptReader{data: []byte("第一条\r\x1b[A\r"), gate: make(chan struct{})}
	app := newTUIApp(scr, r, false, &fakeBackend{}, "1.0.0", "1", "com.test.ui")

	done := make(chan struct{})
	go func() { defer close(done); app.Start() }()

	waitUntil(t, "历史记录被重新提交", func() bool {
		return strings.Count(scr.String(), "第一条") >= 3
	})

	close(r.gate)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TUI 未退出")
	}
}

// /tool 调试命令应当把参数原样交给后端
func TestTUILoopToolCommand(t *testing.T) {
	scr := &fakeScreen{w: 100, h: 20}
	r := &scriptReader{data: []byte("/tool readFile.v1 run {\"path\":\"a.txt\"}\r"), gate: make(chan struct{})}
	be := &fakeBackend{}
	app := newTUIApp(scr, r, false, be, "1.0.0", "1", "com.test.ui")

	done := make(chan struct{})
	go func() { defer close(done); app.Start() }()

	waitUntil(t, "/tool 被调用", func() bool { return len(be.tools) == 1 })
	if be.tools[0] != `readFile.v1.run {"path":"a.txt"}` {
		t.Fatalf("/tool 传参不正确: %q", be.tools[0])
	}

	close(r.gate)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TUI 未退出")
	}
}

// 事件推送必须能出现在界面上
func TestTUIPushEvent(t *testing.T) {
	scr := &fakeScreen{w: 100, h: 20}
	r := &scriptReader{data: []byte{}, gate: make(chan struct{})}
	be := &fakeBackend{events: true}
	app := newTUIApp(scr, r, false, be, "1.0.0", "1", "com.test.ui")

	done := make(chan struct{})
	go func() { defer close(done); app.Start() }()

	app.PushEvent("plugin.state", `{"pluginId":"a"}`)
	waitUntil(t, "事件出现在侧栏", func() bool {
		return strings.Contains(scr.String(), "plugin.state")
	})

	close(r.gate)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TUI 未退出")
	}
}
