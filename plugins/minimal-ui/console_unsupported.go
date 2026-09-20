//go:build !windows && !linux && !darwin

package main

import "errors"

// openConsole 在其他 Unix 上暂未实现原始模式，退化为行式界面。
func openConsole() (*terminal, error) {
	return nil, errors.New("当前平台暂不支持全屏终端，已退化为行式界面")
}

func setRaw(t *terminal) (func(), error) {
	return nil, errors.New("当前平台暂不支持原始模式")
}

func consoleSize(t *terminal) (int, int, bool) {
	return 0, 0, false
}
