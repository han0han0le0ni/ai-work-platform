//go:build windows

package format

import (
	"os"
	"os/exec"
)

// ExecSelf 在 Windows 上没有 syscall.Exec，改为派生一个全新的自身并立即退出。
//
// 不等待子进程：与 Unix 的 exec 语义保持一致 —— 重启后系统里始终只有一个核心进程，
// 不会留下一条“父进程等子进程”的长链。
func ExecSelf(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	// 释放对子进程句柄的引用，避免父进程退出前阻塞子进程
	_ = cmd.Process.Release()
	os.Exit(0)
	return nil
}
