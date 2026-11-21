//go:build !windows

package host

import (
	"os"
	"os/signal"
	"syscall"

	"aiwork/core/format"
)

// watchSignals 监听 SIGUSR2（格式化）与 SIGINT/SIGTERM（退出）。
func (h *Host) watchSignals() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR2)
	for {
		select {
		case sig := <-ch:
			if sig == syscall.SIGUSR2 {
				h.logs.Core("warn", "收到 SIGUSR2：执行格式化")
				go func() {
					if err := format.Reset(h); err != nil {
						h.logs.Core("error", "格式化失败：%v", err)
					}
				}()
				continue
			}
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
