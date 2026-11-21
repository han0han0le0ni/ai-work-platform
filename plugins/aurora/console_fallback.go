//go:build !windows && !linux && !darwin

package main

import "errors"

// 其他平台暂未实现原始模式，界面会退化为静默模式，核心照常运行。
func openConsole() (*terminal, error) {
	return nil, errors.New("当前平台暂不支持全屏终端")
}

func setRaw(t *terminal) (func(), error) {
	return nil, errors.New("当前平台暂不支持原始模式")
}

func consoleSize(t *terminal) (int, int, bool) {
	return 0, 0, false
}
