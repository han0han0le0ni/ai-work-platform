//go:build windows

package process

import "os"

// terminate 在 Windows 上没有 SIGTERM 语义，只能直接终止进程。
// 插件若想优雅退出，应响应 core.shutdown（核心已在宽限期内发出）。
func terminate(p *os.Process) error {
	return p.Kill()
}
