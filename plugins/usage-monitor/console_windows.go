//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
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

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

// openConsoleOut 打开控制台输出设备。
//
// 插件进程的 stdin/stdout 是 JSON-RPC 硬通道，界面必须绕开它们 ——
// 工作台也是这么做的（打开 CONOUT$ 直接渲染）。
func openConsoleOut() (*os.File, error) {
	return os.OpenFile("CONOUT$", os.O_RDWR, 0)
}

// consoleSize 返回可见窗口的列数与行数。
func consoleSize(f *os.File) (int, int) {
	var info winConsoleInfo
	if r, _, _ := procGetConsoleScreenBufferInfo.Call(f.Fd(), uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0, 0
	}
	w := int(info.window.right-info.window.left) + 1
	h := int(info.window.bottom-info.window.top) + 1
	return w, h
}
