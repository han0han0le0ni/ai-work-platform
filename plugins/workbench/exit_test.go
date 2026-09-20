package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 退出必须把终端还原。
//
// 核心停插件只给一次机会（规范 2.4 的 core.shutdown）：调完 Deactivate 就 os.Exit(0)，
// 没有第二次收尾。所以 alt screen、鼠标上报、原始模式都得在这里收拾干净 ——
// 漏掉任何一样，重启核心后新界面就画在残局上：满屏错行、点哪都不对。
// 重启按钮走的正是这条路（见 Workbench.restart）。
func TestDeactivateRestoresTerminal(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.txt")
	out := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(in, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// 输入是个空文件，界面一读就是 EOF：不挂住的话主循环立刻就退了，
	// 测的就不是"正在运行时被叫停"这个真实场景。
	t.Setenv("AIWORK_UI_TTY", in)
	t.Setenv("AIWORK_UI_OUT", out)
	t.Setenv("AIWORK_UI_HOLD_ON_EOF", "1")

	term, err := openTerminal()
	if err != nil {
		t.Fatal(err)
	}
	if err := term.Enter(); err != nil {
		t.Fatal(err)
	}

	a := newApp(term, &fakeBackend{}, "1.0.0", "1", "com.official.workbench")
	w := &Workbench{app: a}

	// 主循环故意不起来：真实时序里 SDK 调完 Deactivate 就 os.Exit(0)，
	// 主循环那条 defer（见 Activate）未必有机会跑到。
	// 所以还原终端这件事，Deactivate 必须自己扛得住。
	if err := w.Deactivate(); err != nil {
		t.Fatalf("Deactivate 失败: %v", err)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// 1049l = 退出 alt screen；25h = 把光标还回来
	for _, want := range []string{"\033[?1049l", "\033[?25h"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("退出后终端没还原：输出里缺少 %q —— 重启后新界面会画在残局上", want)
		}
	}
}
