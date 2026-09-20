package main

import (
	"io"
	"os"
	"strings"
)

// screen 是 TUI 需要的终端能力。抽象出来是为了能在测试里喂假终端。
type screen interface {
	Size() (int, int)
	WriteString(string)
}

// terminal 是控制终端的抽象。
//
// 插件进程的 stdin/stdout 属于 JSON-RPC 硬通道，界面必须绕开它们：
//   - 默认：Windows 打开 CONIN$ / CONOUT$，Unix 打开 /dev/tty
//   - 可用环境变量 AIWORK_UI_TTY 指定设备或文件（无终端环境、自动化测试用）
type terminal struct {
	in      io.Reader
	out     io.Writer
	closers []io.Closer
	color   bool // 终端是否支持 ANSI
	raw     bool
	state   any    // 平台私有句柄
	restore func() // 退出原始模式
}

// openTerminal 打开控制终端。
func openTerminal() (*terminal, error) {
	if p := os.Getenv("AIWORK_UI_TTY"); p != "" {
		in, err := os.OpenFile(p, os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			_ = in.Close()
			return nil, err
		}
		// 普通文件/管道：不加颜色，也不进全屏模式
		return &terminal{in: in, out: out, closers: []io.Closer{in, out}}, nil
	}
	return openConsole()
}

// Size 返回终端的字符行列数。
func (t *terminal) Size() (int, int) {
	if w, h, ok := consoleSize(t); ok && w > 0 && h > 0 {
		return w, h
	}
	return 80, 24
}

// WriteString 输出到终端，统一用 CRLF 避免各平台换行差异。
func (t *terminal) WriteString(s string) {
	_, _ = io.WriteString(t.out, strings.ReplaceAll(s, "\n", "\r\n"))
}

// Enter 进入全屏模式：备用屏 + 原始模式 + 隐藏光标。
//
// 失败说明当前不是一个可交互终端，调用方应退化为行式界面。
func (t *terminal) Enter() error {
	if !t.raw {
		restore, err := setRaw(t)
		if err != nil {
			return err
		}
		t.restore = restore
		t.raw = true
	}
	t.WriteString("\033[?1049h\033[2J\033[H\033[?25l")
	return nil
}

// Leave 恢复终端：光标、颜色、备用屏、原始模式，全部还原。
// 必须幂等且永远能被调用——终端被留在原始模式是最糟的失败方式。
func (t *terminal) Leave() {
	if t.restore != nil {
		t.WriteString("\033[?25h\033[0m\033[?1049l")
		t.restore()
		t.restore = nil
		t.raw = false
	}
	for _, c := range t.closers {
		_ = c.Close()
	}
	t.closers = nil
}
