//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	procFindWindowW    = user32.NewProc("FindWindowW")
	procShowWindow     = user32.NewProc("ShowWindow")
	procSwitchToWindow = user32.NewProc("SwitchToThisWindow")
	procBringToTop     = user32.NewProc("BringWindowToTop")
	procSetForeground  = user32.NewProc("SetForegroundWindow")
	procSetActive      = user32.NewProc("SetActiveWindow")
	procGetForeground  = user32.NewProc("GetForegroundWindow")
	procGetWindowPID   = user32.NewProc("GetWindowThreadProcessId")
	procAttachInput    = user32.NewProc("AttachThreadInput")
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentTID  = kernel32.NewProc("GetCurrentThreadId")
)

// focusExistingUI 找到已经打开的界面窗口并把它置前。
// 窗口标题由 GUI 插件设置为产品名（appName），和启动器保持一致。
//
// Windows 默认禁止后台进程抢前台，所以这里用 AttachThreadInput 临时并入
// 当前前台线程的输入队列，再用 SwitchToThisWindow / SetForegroundWindow 兜底。
func focusExistingUI() bool {
	title, err := syscall.UTF16PtrFromString(appName)
	if err != nil {
		return false
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return false
	}

	// SW_RESTORE：最小化的窗口先还原。
	procShowWindow.Call(hwnd, 9)
	procSwitchToWindow.Call(hwnd, 1)

	fg, _, _ := procGetForeground.Call()
	myTID, _, _ := procGetCurrentTID.Call()
	fgTID := uintptr(0)
	if fg != 0 {
		fgTID, _, _ = procGetWindowPID.Call(fg, 0)
	}
	if fgTID != 0 && fgTID != myTID {
		procAttachInput.Call(myTID, fgTID, 1)
		defer procAttachInput.Call(myTID, fgTID, 0)
	}
	procBringToTop.Call(hwnd)
	procSetForeground.Call(hwnd)
	procSetActive.Call(hwnd)
	return true
}
