package main

import (
	"regexp"
	"strings"
	"testing"
)

func demoState() *uiState {
	return &uiState{
		coreVersion: "1.0.0",
		apiVersion:  "1",
		pluginID:    "com.official.minimal-ui",
		services: []serviceStatus{
			{name: "session.v1", note: "未安装"},
			{name: "echo.v1", alive: true, provider: "com.official.echo"},
			{name: "readFile.v1", alive: true, provider: "com.official.fs-tool"},
			{name: "llm.v1", note: "未安装"},
			{name: "sandbox.v1", note: "依赖缺失"},
		},
		messages: []chatMsg{
			{role: "系统", at: "14:32:01", text: "欢迎使用 AI 工作平台。\n直接输入文字即发送；输入 /help 查看命令。"},
			{role: "你", at: "14:32:10", text: "读一下 runtime/core.json"},
			{role: "AI", at: "14:32:10", text: "{\n  \"apiVersion\": \"1\",\n  \"controlPort\": 9123\n}\n经由 readFile.v1 返回"},
		},
		events: []string{"14:32:11 chat.message {\"text\":\"读一下…\"}", "14:32:11 core.log {\"level\":\"info\"}"},
		input:  []rune("帮我再读一下 go.mod"),
		cursor: 4,
	}
}

var ansiRe = regexp.MustCompile("\033\\[[0-9;?]*[a-zA-Z]")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// 最关键的排版不变量：每一行都必须精确等于终端宽度，否则边框会错位。
func TestFrameWidthInvariant(t *testing.T) {
	sizes := [][2]int{
		{40, 8}, {60, 12}, {80, 24}, {84, 24}, {100, 26}, {120, 30}, {160, 40}, {200, 50},
	}
	for _, sz := range sizes {
		for _, color := range []bool{false, true} {
			st := demoState()
			st.busy = true
			frame := renderFrame(st, sz[0], sz[1], color)
			lines := strings.Split(frame, "\n")
			if len(lines) != sz[1] {
				t.Fatalf("%dx%d color=%v: 行数 %d，期望 %d", sz[0], sz[1], color, len(lines), sz[1])
			}
			for i, ln := range lines {
				if got := dispWidth(stripANSI(ln)); got != sz[0] {
					t.Errorf("%dx%d color=%v 第 %d 行宽度 %d，期望 %d\n%s",
						sz[0], sz[1], color, i, got, sz[0], stripANSI(ln))
				}
			}
		}
	}
}

// 终端太小时不应崩，也不应产出内容。
func TestFrameTooSmall(t *testing.T) {
	if got := renderFrame(demoState(), 10, 3, false); got != "" {
		t.Fatalf("过小的终端应返回空帧，实际 %q", got)
	}
}

// 中文与 emoji 按两列计算，这是终端对齐的基础。
func TestDisplayWidth(t *testing.T) {
	cases := map[string]int{
		"abc":   3,
		"中文":    4,
		"中a文":   5,
		"你好，世界": 10,
		"✓":     1,
		"→":     1,
		"🀄":     2,
		"":      0,
	}
	for s, want := range cases {
		if got := dispWidth(s); got != want {
			t.Errorf("dispWidth(%q) = %d，期望 %d", s, got, want)
		}
	}
}

// 换行必须按显示宽度走，而不是按字节或字符数。
func TestWrapTextCJK(t *testing.T) {
	got := wrapText("一二三四五六", 4)
	if len(got) != 3 || got[0] != "一二" || got[2] != "五六" {
		t.Fatalf("中文换行不正确: %#v", got)
	}
	got = wrapText("第一行\n第二行", 20)
	if len(got) != 2 {
		t.Fatalf("强制换行不正确: %#v", got)
	}
}

// 超长输入时输入行仍保持宽度，且不溢出。
func TestInputRowLongText(t *testing.T) {
	st := demoState()
	st.input = []rune(strings.Repeat("很长的一段输入", 40))
	st.cursor = len(st.input)
	frame := renderFrame(st, 80, 20, false)
	for _, ln := range strings.Split(frame, "\n") {
		if w := dispWidth(ln); w != 80 {
			t.Fatalf("输入过长时行宽 %d，期望 80: %q", w, ln)
		}
	}
	if col := cursorPos(st, 80); col >= 80 || col < 0 {
		t.Fatalf("光标列 %d 越界", col)
	}
}

// 视觉检查：go test ./plugins/minimal-ui -run TestFramePreview -v
func TestFramePreview(t *testing.T) {
	st := demoState()
	st.busy = true
	st.spinner = 3
	for _, ln := range strings.Split(renderFrame(st, 100, 26, false), "\n") {
		t.Log(ln)
	}
	t.Log("")
	for _, ln := range strings.Split(renderFrame(demoState(), 72, 18, false), "\n") {
		t.Log(ln)
	}
}
