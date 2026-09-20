//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// setConsoleTitle 改控制台窗口标题。双击启动时窗口标题默认是 exe 的完整路径，
// 又长又难看；核心不管窗口，所以这里顺手改掉。
func setConsoleTitle(title string) {
	p, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("SetConsoleTitleW")
	if err := proc.Find(); err != nil {
		return
	}
	// 没有控制台（比如输出被重定向）时调用会失败，失败也无所谓。
	proc.Call(uintptr(unsafe.Pointer(p)))
}
