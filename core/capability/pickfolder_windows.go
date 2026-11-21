//go:build windows

package capability

import (
	"encoding/base64"
	"os/exec"
	"strings"
)

// pickFolder 在 Windows 上弹现代文件夹选择框（IFileOpenDialog），返回选中的目录。
//
// cancelled 表示用户点了取消（此时 path 为空、err 为 nil）。
// 这是阻塞调用：对话框不关，它就不返回。
func pickFolder(title, initial string) (path string, cancelled bool, err error) {
	enc := psEncodedCommand(pickFolderScript(title, initial))
	// -STA：COM 对话框需要单线程单元；-NonInteractive：别弹欢迎/错误框。
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-STA", "-EncodedCommand", enc)
	out, err := cmd.Output()
	if err != nil {
		return "", false, err
	}
	path = decodePickedPath(out)
	if path == "" {
		return "", true, nil
	}
	return path, false, nil
}

// decodePickedPath 把 PowerShell 那一侧的输出还原成用户选中的目录。
//
// 路径是先经 UTF-8 编成 base64 才写出来的（见 pickFolderScript），为什么绕这一趟：
// PowerShell 往管道写字用的是**系统当前的代码页** —— 中文机器上是 GBK，于是
// `E:\万物皆插件` 到这里就是一串 GBK 字节，被当成 UTF-8 一拼，整条路径成了乱码，
// 而这恰恰是最容易出现的那类路径：用户的文件夹大多是有名字的，不是 test123。
// base64 只用 ASCII，任何代码页都不会把它改样，所以这条路跟语言设置无关。
//
// 解码失败时按原样交出那行文本：宁可拿到一串可能错的字，
// 也不该把用户已经选好的目录直接丢掉。
func decodePickedPath(out []byte) string {
	last := ""
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			last = s
		}
	}
	if last == "" {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(last)
	if err != nil {
		return last
	}
	return strings.TrimSpace(string(b))
}
