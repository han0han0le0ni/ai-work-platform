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
	for {
		select {
		case sig := <-ch:
			h.logs.Core("info", "收到信号 %v：退出", sig)
			h.Shutdown()
			return
		case <-h.stopped:
			// Shutdown 由别的路径触发（core.shutdown 能力 / 重启收尾）：
			// 不能还堵在信号循环里，否则 Run 永远等不到返回。
			return
		}
	}
}
