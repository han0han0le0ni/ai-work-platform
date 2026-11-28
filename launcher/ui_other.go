//go:build !windows

package main

// focusExistingUI 在非 Windows 平台没有统一的原生窗口查找方式，交给核心补开界面即可。
func focusExistingUI() bool { return false }
