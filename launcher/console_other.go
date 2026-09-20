//go:build !windows

package main

// 非 Windows 平台没有控制台窗口标题这回事，标题由启动它的终端决定。
func setConsoleTitle(string) {}
