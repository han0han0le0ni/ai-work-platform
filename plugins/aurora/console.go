package main

import (
	"io"
	"os"
	"strings"
)

// terminal 是控制终端的抽象。
//
// 插件进程的 stdin/stdout 是 JSON-RPC 硬通道，界面必须绕开它们：
//   - 默认：Windows 打开 CONIN$ / CONOUT$，Unix 打开 /dev/tty
//   - AIWORK_UI_TTY 可指定设备或文件（无终端环境、自动化测试用）
//
// 三个标志位决定了怎么读输入、要不要开鼠标上报：
//
//	tty    真终端。为 true 才发送鼠标上报的转义序列。
//	native 平台原生事件读取（Windows 控制台）。为 true 时鼠标是原生事件而非字节流。
type terminal struct {
	in      io.Reader
	out     io.Writer
	closers []io.Closer
	color   bool
	raw     bool
	tty     bool
	native  bool
	state   any
	restore func()
}

// openTerminal 打开控制终端。
//
// AIWORK_UI_TTY 指定输入源；AIWORK_UI_OUT 可选地指定**另一个**输出文件。
// 自动化驱动时读写同一个文件会让界面读回自己刚画的内容（把边框当成键盘输入），
// 所以提供分离输出的开关。两者都只在没有真终端时才有意义。
func openTerminal() (*terminal, error) {
	if p := os.Getenv("AIWORK_UI_TTY"); p != "" {
		in, err := os.OpenFile(p, os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		outPath, flags := p, os.O_WRONLY|os.O_CREATE|os.O_APPEND
		if o := os.Getenv("AIWORK_UI_OUT"); o != "" {
			outPath, flags = o, os.O_WRONLY|os.O_CREATE|os.O_TRUNC
		}
		out, err := os.OpenFile(outPath, flags, 0o644)
		if err != nil {
			_ = in.Close()
			return nil, err
		}
		// 普通文件/管道：不是终端，不加颜色，不开鼠标上报（但注入的鼠标序列照样能解析）
		return &terminal{in: in, out: out, closers: []io.Closer{in, out}}, nil
	}
	return openConsole()
}

// Size 返回终端的字符行列数。
func (t *terminal) Size() (int, int) {
	if w, h, ok := consoleSize(t); ok && w > 0 && h > 0 {
		return w, h
	}
	return 96, 30
}

// WriteString 输出文本，统一用 CRLF 避免各平台换行差异。
func (t *terminal) WriteString(s string) {
	_, _ = io.WriteString(t.out, strings.ReplaceAll(s, "\n", "\r\n"))
}

// WriteRaw 输出原始字节，不做换行转换（转义序列必须走这里）。
func (t *terminal) WriteRaw(s string) {
	_, _ = io.WriteString(t.out, s)
}

// Enter 进入全屏交互模式。
//
// 只有真终端才需要（也可能失败于）切换原始模式。指向文件/管道时没有终端模式可改，
// 直接用即可 —— 转义序列会原样写进文件，输入就是文件里的字节。
func (t *terminal) Enter() error {
	if !t.raw {
		if t.tty {
			restore, err := setRaw(t)
			if err != nil {
				return err
			}
			t.restore = restore
		}
		t.raw = true
	}
	t.WriteRaw("\033[?1049h\033[2J\033[H\033[?25l")
	if t.tty {
		// 1002h = 按住键移动也上报。**浮窗能不能拖全看这一条**：
		// 只开 1000h 的话终端只在按下和松手时报一下，中间的移动一个字节都不发，
		// 界面永远收不到"拖到哪儿了"，浮窗就钉死在原地。
		//
		// 1000h 仍然开着：不支持 1002 的老终端会忽略未知模式，
		// 那时至少点击还是好的（拖动改用 Alt+方向键）。
		// 1006h = SGR 扩展坐标（不受 223 列限制），
		// 2004h = 括号粘贴（把粘贴内容包起来，界面才能区分"粘贴"和"逐键输入"）
		t.WriteRaw("\033[?1000h\033[?1002h\033[?1006h\033[?2004h")
	}
	return nil
}

// Leave 恢复终端。必须幂等：终端被留在原始模式是最糟的失败方式。
func (t *terminal) Leave() {
	if t.raw {
		out := "\033[?25h\033[0m\033[?1049l"
		if t.tty {
			out = "\033[?1000l\033[?1002l\033[?1006l\033[?2004l" + out
		}
		t.WriteRaw(out)
		if t.restore != nil {
			t.restore()
		}
		t.restore = nil
		t.raw = false
	}
	for _, c := range t.closers {
		_ = c.Close()
	}
	t.closers = nil
}
