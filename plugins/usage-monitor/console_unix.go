//go:build !windows

package main

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

type winsize struct {
	row    uint16
	col    uint16
	xpixel uint16
	ypixel uint16
}

// openConsoleOut 打开控制终端（不是 stdin/stdout —— 那两个被 JSON-RPC 占着）。
func openConsoleOut() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}

// consoleSize 先用 TIOCGWINSZ 问内核，失败再退回环境变量。
func consoleSize(f *os.File) (int, int) {
	var ws winsize
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno == 0 && ws.col > 0 && ws.row > 0 {
		return int(ws.col), int(ws.row)
	}
	w, _ := strconv.Atoi(os.Getenv("COLUMNS"))
	h, _ := strconv.Atoi(os.Getenv("LINES"))
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 30
	}
	return w, h
}
