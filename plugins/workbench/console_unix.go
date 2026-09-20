//go:build linux || darwin

package main

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

type unixWinsize struct {
	rows, cols, xpixel, ypixel uint16
}

func openConsole() (*terminal, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &terminal{
		in:      f,
		out:     f,
		closers: []io.Closer{f},
		color:   true,
		tty:     true,
		state:   int(f.Fd()),
	}, nil
}

// setRaw 只关掉规范模式与回显，并改成按字节返回。
//
// 刻意保留 ISIG 与 ICRNL：Ctrl+C 仍然发给前台进程组（整个平台优雅退出），
// 回车仍然到达为 \n。这样比全量 raw 更少踩坑，也更好恢复。
func setRaw(t *terminal) (func(), error) {
	fd, ok := t.state.(int)
	if !ok {
		return nil, syscall.EINVAL
	}
	var old syscall.Termios
	if err := ioctlPtr(fd, ioctlReadTermios, unsafe.Pointer(&old)); err != nil {
		return nil, err
	}

	raw := old
	raw.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ECHONL
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctlPtr(fd, ioctlWriteTermios, unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}

	saved := old
	return func() {
		_ = ioctlPtr(fd, ioctlWriteTermios, unsafe.Pointer(&saved))
	}, nil
}

func consoleSize(t *terminal) (int, int, bool) {
	fd, ok := t.state.(int)
	if !ok {
		return 0, 0, false
	}
	var ws unixWinsize
	if err := ioctlPtr(fd, syscall.TIOCGWINSZ, unsafe.Pointer(&ws)); err != nil {
		return 0, 0, false
	}
	return int(ws.cols), int(ws.rows), true
}

func ioctlPtr(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}
