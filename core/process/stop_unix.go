//go:build !windows

package process

import (
	"os"
	"syscall"
)

// terminate 发送 SIGTERM，给插件一次体面退出的机会。
// 规范 1.6：退出时核心发送 SIGTERM，3 秒后 SIGKILL。
func terminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
