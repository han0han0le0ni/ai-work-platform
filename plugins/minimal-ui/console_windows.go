//go:build windows

package main

import (
	"errors"
	"io"
	"os"
	"syscall"
	"unsafe"
)

// 控制台模式位
const (
	enableLineInput             = 0x0002
	enableEchoInput             = 0x0004
	enableVirtualTerminalInput  = 0x0200
	enableVirtualTerminalOutput = 0x0004
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

type winCoord struct {
	x, y int16
}

type winSmallRect struct {
	left, top, right, bottom int16
}

type winConsoleInfo struct {
	size              winCoord
	cursorPosition    winCoord
	attributes        uint16
	window            winSmallRect
	maximumWindowSize winCoord
}

type winState struct {
	in  syscall.Handle
	out syscall.Handle
}

func openConsole() (*terminal, error) {
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	t := &terminal{
		in:      in,
		out:     out,
		closers: []io.Closer{in, out},
		color:   enableVT(out),
		state:   &winState{in: syscall.Handle(in.Fd()), out: syscall.Handle(out.Fd())},
	}
	return t, nil
}

// enableVT 打开 ANSI 输出支持；失败说明是旧控制台，退化为纯文本界面。
func enableVT(f *os.File) bool {
	h := syscall.Handle(f.Fd())
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return false
	}
	r, _, _ = procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminalOutput))
	return r != 0
}

// setRaw 关掉行输入与回显，并打开 VT 输入让方向键以转义序列到达。
//
// 刻意保留 ENABLE_PROCESSED_INPUT：这样 Ctrl+C 仍然产生控制台事件，
// 核心和插件都能收到并以优雅方式退出，而不是变成普通字节被吞掉。
func setRaw(t *terminal) (func(), error) {
	st, ok := t.state.(*winState)
	if !ok {
		return nil, errors.New("控制台句柄不可用")
	}
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(st.in), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return nil, errors.New("读取控制台输入模式失败")
	}
	next := (mode &^ (enableLineInput | enableEchoInput)) | enableVirtualTerminalInput
	if r, _, _ := procSetConsoleMode.Call(uintptr(st.in), uintptr(next)); r == 0 {
		return nil, errors.New("切换控制台到原始模式失败")
	}
	old := mode
	return func() {
		procSetConsoleMode.Call(uintptr(st.in), uintptr(old))
	}, nil
}

func consoleSize(t *terminal) (int, int, bool) {
	st, ok := t.state.(*winState)
	if !ok {
		return 0, 0, false
	}
	var info winConsoleInfo
	if r, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(st.out), uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0, 0, false
	}
	w := int(info.window.right-info.window.left) + 1
	h := int(info.window.bottom-info.window.top) + 1
	return w, h, true
}
