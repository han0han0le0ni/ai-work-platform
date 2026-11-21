//go:build windows

package capability

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// cpGB18030 是 Windows 的 GB18030 代码页编号。
const cpGB18030 = 54936

// decodeGB18030 把 GB18030 字节转成 UTF-8 字符串。
//
// 为什么不自带一张码表：平台坚持零依赖，而 GB18030 的双字节区有两万多个映射，
// 手写等于把整张表抄进仓库，还可能抄错。Windows 早就装好了这张表，
// MultiByteToWideChar 就是它的官方入口 —— 借来用，UTF-16 到 UTF-8 由标准库做。
func decodeGB18030(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	dll, err := syscall.LoadDLL("kernel32.dll")
	if err != nil {
		return "", err
	}
	proc, err := dll.FindProc("MultiByteToWideChar")
	if err != nil {
		return "", err
	}

	// 第一段：先问需要多少个 UTF-16 码元（输出缓冲区给 0 时返回值就是所需长度）。
	need, _, err := proc.Call(
		uintptr(cpGB18030), 0,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
		0, 0)
	if need == 0 {
		return "", fmt.Errorf("MultiByteToWideChar 无法解码: %v", err)
	}

	buf := make([]uint16, need)
	got, _, err := proc.Call(
		uintptr(cpGB18030), 0,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(need))
	if got == 0 {
		return "", fmt.Errorf("MultiByteToWideChar 无法解码: %v", err)
	}
	return string(utf16.Decode(buf[:got])), nil
}
