//go:build !windows

package format

import (
	"os"
	"syscall"
)

// ExecSelf 用自身镜像替换当前进程（Unix）。成功后永不返回。
func ExecSelf(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	argv := append([]string{exe}, args...)
	return syscall.Exec(exe, argv, os.Environ())
}
