//go:build !windows

package capability

import "fmt"

// decodeGB18030 只在 Windows 上可用。
//
// 中文遗留编码的码表本来就该由系统提供：项目坚持零依赖，不去手抄那两万多个映射。
// Windows 装了这张表（见 gbk_windows.go），其余平台给不出可靠的替代，
// 所以如实报错，而不是返回一串来路不明的字让调用方以为读懂了。
func decodeGB18030(data []byte) (string, error) {
	return "", fmt.Errorf("当前平台不提供 GB18030 解码（仅 Windows）；请先把文件转成 UTF-8")
}
