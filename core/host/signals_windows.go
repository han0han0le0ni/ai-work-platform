//go:build windows

package host

import (
	"os"
	"os/signal"
)

// watchSignals 在 Windows 上没有 SIGUSR2，格式化只能走本地端口 + Token 通道。
// 这里只监听 Ctrl+C / 关闭事件。
func (h *Host) watchSignals() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, os.Interrupt)
	for sig := range ch {
		h.logs.Core("info", "收到信号 %v：退出", sig)
		h.Shutdown()
		return
	}
}
