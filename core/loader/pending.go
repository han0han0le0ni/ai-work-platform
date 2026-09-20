package loader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// 规范 2.6：安装 / 删除 / 更新都只是“标记 pending-restart”，重启后才生效。
//
// 核心把待生效的变更记在 <runtime>/pending-restart.json；
// 下次启动时读取并写入日志，然后删除标记文件。绝不热更新。

// PendingFile 是待重启生效的变更记录文件名。
const PendingFile = "pending-restart.json"

// DisabledSuffix 是插件目录被禁用时的后缀。
//
// Scan 只认 *.plugin 结尾的目录，所以加上这个后缀就等于关掉它，去掉就是启用。
const DisabledSuffix = ".disabled"

// Change 描述一次待生效的插件变更。
type Change struct {
	Action   string `json:"action"` // install / delete / update / enable / disable
	PluginID string `json:"pluginId,omitempty"`
	Dir      string `json:"dir,omitempty"`
	By       string `json:"by,omitempty"` // 发起变更的插件 ID，供启动时校验其权限
	At       string `json:"at"`
}

// MarkPendingRestart 追记一条待重启生效的变更。
func MarkPendingRestart(runtimeDir string, c Change) error {
	path := filepath.Join(runtimeDir, PendingFile)
	if c.At == "" {
		c.At = time.Now().UTC().Format(time.RFC3339)
	}
	list := readPending(path)
	list = append(list, c)

	blob, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(blob, '\n'), 0o644)
}

// TakePendingRestart 取出并清除待生效变更（核心每次启动调用一次）。
func TakePendingRestart(runtimeDir string) []Change {
	path := filepath.Join(runtimeDir, PendingFile)
	list := readPending(path)
	if len(list) > 0 {
		_ = os.Remove(path)
	}
	return list
}

func readPending(path string) []Change {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []Change
	if err := json.Unmarshal(data, &list); err != nil {
		return nil
	}
	return list
}
