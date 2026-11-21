//go:build !windows

package main

import "syscall"

// detachedSysProcAttr 非 Windows 无需特殊处理：桌面窗口走 xdg-open / open，
// 本就不与终端控制台绑定。
func detachedSysProcAttr() *syscall.SysProcAttr { return nil }
