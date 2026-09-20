// Package format 是格式化应急通道：把系统重置回出厂状态。
//
// 格式化流程：
//  1. 立即停止调度（不再转发 core.invoke、不再分发事件）
//  2. 杀死所有插件子进程（SIGTERM → 3 秒 → SIGKILL）
//  3. 清空 plugins/（文件被锁时移入 pending-delete/）
//  4. 清空 plugin-configs/
//  5. 重置核心内存状态
//  6. 保留 core 二进制、core.json、logs/
//  7. exec 自身，带 --first-run，进入引导菜单
package format

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Host 是格式化流程需要核心提供的能力。由宿主内核实现。
type Host interface {
	RuntimeDir() string
	Pause()
	KillAllPlugins(grace time.Duration)
	ResetMemory()
	Reexec(extra []string) error
	Logf(level, format string, args ...any)
}

// Reset 执行完整格式化流程。
func Reset(h Host) error {
	rt := h.RuntimeDir()
	h.Pause()
	h.Logf("warn", "格式化开始：已停止调度")

	h.KillAllPlugins(3 * time.Second)

	if err := purgeDir(filepath.Join(rt, "plugins"), filepath.Join(rt, "pending-delete")); err != nil {
		h.Logf("error", "清空 plugins/ 失败: %v", err)
	}
	if err := purgeDir(filepath.Join(rt, "plugin-configs"), filepath.Join(rt, "pending-delete")); err != nil {
		h.Logf("error", "清空 plugin-configs/ 失败: %v", err)
	}

	h.ResetMemory()
	h.Logf("warn", "格式化完成：plugins/ 与 plugin-configs/ 已清空，保留 core / core.json / logs/")

	if err := h.Reexec([]string{"--first-run"}); err != nil {
		h.Logf("error", "重启核心失败: %v", err)
		return err
	}
	return nil
}

// purgeDir 清空目录下的所有条目。
// Windows 上文件被锁导致删除失败时，移入 pending-delete/，重启后清理。
func purgeDir(dir, pendingDir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var firstErr error
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(p); err != nil {
			if pendingDir != "" {
				_ = os.MkdirAll(pendingDir, 0o755)
				dst := filepath.Join(pendingDir, fmt.Sprintf("%s.%d", e.Name(), time.Now().UnixNano()))
				if renErr := os.Rename(p, dst); renErr == nil {
					continue
				}
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("删除 %s 失败: %w", p, err)
			}
		}
	}
	return firstErr
}
