//go:build windows

package main

import "syscall"

// detachedSysProcAttr 让子进程脱离本插件（乃至核心）的控制台。
//
// 桌面窗口（Edge --app）是长寿命 GUI 进程，若默认继承核心的控制台，
// 平台停止、启动器随之关闭控制台时，Windows 要逐个等待挂在该控制台上的
// 进程响应 CTRL_CLOSE —— Edge 这种有自己窗口体系的 GUI 进程不搭理它，
// 整个退出链会被拖住几分钟。DETACHED_PROCESS 把它从控制台上摘干净。
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200}
}
