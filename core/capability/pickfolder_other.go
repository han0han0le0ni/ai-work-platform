//go:build !windows

package capability

import "fmt"

// pickFolder 只在 Windows 上实现：其余平台没有系统文件夹选择框可弹。
func pickFolder(title, initial string) (string, bool, error) {
	return "", false, fmt.Errorf("当前系统不支持弹系统文件夹选择框（仅 Windows）")
}
