//go:build linux

package main

import "syscall"

// termios 的 ioctl 请求号各平台不同，这里按平台固定。
const (
	ioctlReadTermios  = syscall.TCGETS
	ioctlWriteTermios = syscall.TCSETS
)
