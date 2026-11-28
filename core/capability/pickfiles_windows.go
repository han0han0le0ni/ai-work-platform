//go:build windows

package capability

import (
	"os/exec"
)

// pickFiles 在 Windows 上弹可多选的现代文件选择框，返回用户选中的文件路径。
//
// cancelled 表示用户点了取消（paths 为空、err 为 nil）。
// 与 pickFolder 一样是阻塞调用：对话框不关就不返回。
func pickFiles(title, initial string) (paths []string, cancelled bool, err error) {
	enc := psEncodedCommand(pickFilesScript(title, initial))
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-STA", "-EncodedCommand", enc)
	out, err := cmd.Output()
	if err != nil {
		return nil, false, err
	}
	paths = splitPickedPaths(decodePickedPath(out))
	if len(paths) == 0 {
		return nil, true, nil
	}
	return paths, false, nil
}
