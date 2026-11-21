//go:build !windows

package main

import "os/exec"

// 非 Windows 平台由调用方终端管理输出，这些辅助函数都是空操作。
func messageBox(title, text string) {}

func configureChild(cmd *exec.Cmd) {}

func configureChildConsole(cmd *exec.Cmd) {}
