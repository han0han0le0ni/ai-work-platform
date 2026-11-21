//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"unsafe"
)

var user32 = syscall.NewLazyDLL("user32.dll")

// messageBox 弹一个模态提示框。启动器是 GUI 子系统程序，没有控制台可写，
// 所以错误/提示改用弹窗，用户一定能看到。
func messageBox(title, text string) {
	t, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	c, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	proc := user32.NewProc("MessageBoxW")
	proc.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x40) // MB_ICONINFORMATION
}

// configureChild 配置子进程不创建控制台窗口。
// 启动器本身是 GUI 子系统（无控制台），core 是控制台子系统；默认启动 core 时
// Windows 会再弹一个黑框，用 CREATE_NO_WINDOW 阻止它。core 的日志照常写文件，
// stdin/stdout 走 pipe，不受影响。
func configureChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// configureChildConsole 配置子进程独占一个新控制台。
// 仅用于首启引导（无插件）场景：core 会进入 TUI 引导菜单，必须有可见的控制台让用户交互。
func configureChildConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010} // CREATE_NEW_CONSOLE
}
