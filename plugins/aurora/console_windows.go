//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"syscall"
	"unsafe"
)

// 控制台输入模式位
const (
	enableProcessedInput = 0x0001 // 保留：Ctrl+C 仍然产生信号，整个平台可优雅退出
	enableLineInput      = 0x0002
	enableEchoInput      = 0x0004
	enableMouseInput     = 0x0010
	enableQuickEditMode  = 0x0040 // 必须关掉，否则鼠标点击被控制台拿去选文本
	enableExtendedFlags  = 0x0080
	enableVTOutput       = 0x0004
)

// 输入记录类型
const (
	keyEventType    = 0x0001
	mouseEventType  = 0x0002
	resizeEventType = 0x0004
)

// 鼠标按钮与事件标志
const (
	buttonLeft     = 0x0001
	mouseMoved     = 0x0001
	mouseDoubleClk = 0x0002
	mouseWheeled   = 0x0004
	mouseHWheeled  = 0x0008
	leftCtrlDown   = 0x0008
	rightAltDown   = 0x0001 // Win32：RIGHT_ALT_PRESSED
	leftAltDown    = 0x0002 // Win32：LEFT_ALT_PRESSED
	rightCtrlDown  = 0x0004 // Win32：RIGHT_CTRL_PRESSED
)

// 虚拟键码
const (
	vkBack   = 0x08
	vkTab    = 0x09
	vkReturn = 0x0D
	vkEscape = 0x1B
	vkEnd    = 0x23
	vkHome   = 0x24
	vkLeft   = 0x25
	vkUp     = 0x26
	vkRight  = 0x27
	vkDown   = 0x28
	vkDelete = 0x2E
	// vkPacket 是"字符注入"：控制台的粘贴、输入法都通过它把字符送进来
	vkPacket = 0xE7
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procReadConsoleInputW          = kernel32.NewProc("ReadConsoleInputW")
)

type winCoord struct {
	x, y int16
}

type winSmallRect struct {
	left, top, right, bottom int16
}

type winConsoleInfo struct {
	size              winCoord
	cursorPosition    winCoord
	attributes        uint16
	window            winSmallRect
	maximumWindowSize winCoord
}

type winState struct {
	in  syscall.Handle
	out syscall.Handle
}

// inputRecord 对应 Win32 的 INPUT_RECORD：4 字节事件类型 + 16 字节联合体。
//
// 用 [16]byte 而不是具名结构体来装联合体，是为了不依赖 C 结构体对齐规则 ——
// KEY_EVENT_RECORD 与 MOUSE_EVENT_RECORD 都是 16 字节，按偏移解码最稳。
type inputRecord struct {
	eventType uint16
	_         uint16
	body      [16]byte
}

func openConsole() (*terminal, error) {
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	return &terminal{
		in:      in,
		out:     out,
		closers: []io.Closer{in, out},
		color:   enableVT(out),
		tty:     true,
		native:  true, // 鼠标走原生事件，绕开 VT 输入不转发鼠标的限制
		state:   &winState{in: syscall.Handle(in.Fd()), out: syscall.Handle(out.Fd())},
	}, nil
}

// enableVT 打开 ANSI 输出支持；失败说明是旧控制台，退化为纯文本界面。
func enableVT(f *os.File) bool {
	h := syscall.Handle(f.Fd())
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	r, _, _ := procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVTOutput))
	return r != 0
}

// setRaw 关掉行输入、回显与快速编辑，并打开鼠标输入。
//
// 快速编辑必须关：它是"点击即选中文本"的模式，会把鼠标事件全部截走，
// 程序一个点击也收不到。代价是这个界面运行时不能拖选复制文本。
func setRaw(t *terminal) (func(), error) {
	st, ok := t.state.(*winState)
	if !ok {
		return nil, errors.New("控制台句柄不可用")
	}
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(st.in), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return nil, errors.New("读取控制台输入模式失败")
	}
	next := (mode &^ (enableLineInput | enableEchoInput | enableQuickEditMode)) |
		enableExtendedFlags | enableMouseInput
	if r, _, _ := procSetConsoleMode.Call(uintptr(st.in), uintptr(next)); r == 0 {
		return nil, errors.New("切换控制台到交互模式失败")
	}
	old := mode
	return func() {
		procSetConsoleMode.Call(uintptr(st.in), uintptr(old))
	}, nil
}

func consoleSize(t *terminal) (int, int, bool) {
	info, ok := consoleInfo(t)
	if !ok {
		return 0, 0, false
	}
	w := int(info.window.right-info.window.left) + 1
	h := int(info.window.bottom-info.window.top) + 1
	return w, h, true
}

func consoleInfo(t *terminal) (winConsoleInfo, bool) {
	var info winConsoleInfo
	st, ok := t.state.(*winState)
	if !ok {
		return info, false
	}
	if r, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(st.out), uintptr(unsafe.Pointer(&info))); r == 0 {
		return info, false
	}
	return info, true
}

// ---------------------------------------------------------------------------
// 原生输入：ReadConsoleInputW
//
// 为什么不用 ENABLE_VIRTUAL_TERMINAL_INPUT：它只把**键盘**翻译成转义序列，
// 鼠标事件不会变成字节，界面就永远收不到点击。所以鼠标必须走原生事件。
// ---------------------------------------------------------------------------

func pumpNative(t *terminal, out chan<- event, quit <-chan struct{}) {
	st, ok := t.state.(*winState)
	if !ok {
		close(out)
		return
	}
	for {
		var rec inputRecord
		var n uint32
		r, _, _ := procReadConsoleInputW.Call(
			uintptr(st.in), uintptr(unsafe.Pointer(&rec)), 1, uintptr(unsafe.Pointer(&n)))
		if r == 0 || n == 0 {
			close(out)
			return
		}

		var ev event
		var ok2 bool
		switch rec.eventType {
		case keyEventType:
			ev, ok2 = decodeKeyRecord(rec)
		case mouseEventType:
			ev, ok2 = decodeMouseRecord(rec, t)
		case resizeEventType:
			ev, ok2 = event{kind: evResize}, true
		}
		if !ok2 {
			continue
		}
		select {
		case out <- ev:
		case <-quit:
			return
		}
	}
}

// decodeKeyRecord 按 INPUT_RECORD 的偏移解出 KEY_EVENT_RECORD。
//
//	bKeyDown          [0:4]
//	wRepeatCount      [4:6]
//	wVirtualKeyCode   [6:8]
//	wVirtualScanCode  [8:10]
//	uChar             [10:12]
//	dwControlKeyState [12:16]
func decodeKeyRecord(rec inputRecord) (event, bool) {
	if binary.LittleEndian.Uint32(rec.body[0:4]) == 0 {
		return event{}, false // 只处理按下，抬起忽略
	}
	vk := binary.LittleEndian.Uint16(rec.body[6:8])
	ch := binary.LittleEndian.Uint16(rec.body[10:12])
	ctrl := binary.LittleEndian.Uint32(rec.body[12:16])

	// VK_PACKET：粘贴或输入法注入的字符。只当文本，绝不当导航键 ——
	// 否则密钥里混进一个制表符，就会把用户从设置页一键弹回聊天页。
	// 非 BMP 字符会拆成两个代理对事件送达，这里直接丢掉，避免插进半个字符。
	if vk == vkPacket {
		if ch >= 0xD800 && ch <= 0xDFFF {
			return event{}, false
		}
		return event{kind: evKey, key: keyRune, r: rune(ch), paste: true}, ch != 0
	}

	switch vk {
	case vkReturn:
		// Ctrl+回车 = 发送。回车本身已经是换行了，键盘用户需要这样一个送出口。
		if ctrl&(leftCtrlDown|rightCtrlDown) != 0 {
			return keyEvent(keyCtrlEnter), true
		}
		return keyEvent(keyEnter), true
	case vkBack:
		return keyEvent(keyBackspace), true
	case vkTab:
		return keyEvent(keyTab), true
	case vkEscape:
		return keyEvent(keyEsc), true
	case vkLeft:
		return arrowKey(ctrl, keyLeft, keyAltLeft), true
	case vkRight:
		return arrowKey(ctrl, keyRight, keyAltRight), true
	case vkUp:
		return arrowKey(ctrl, keyUp, keyAltUp), true
	case vkDown:
		return arrowKey(ctrl, keyDown, keyAltDown), true
	case vkHome:
		return keyEvent(keyHome), true
	case vkEnd:
		return keyEvent(keyEnd), true
	case vkDelete:
		return keyEvent(keyDelete), true
	}
	if ch >= 0x20 {
		return event{kind: evKey, key: keyRune, r: rune(ch)}, true
	}
	if ctrl&leftCtrlDown != 0 {
		switch ch {
		case 0x03:
			return keyEvent(keyCtrlC), true
		case 0x0c:
			return keyEvent(keyCtrlL), true
		case 0x11:
			return keyEvent(keyCtrlQ), true
		}
	}
	return event{}, false
}

// arrowKey 分辨方向键是不是 Alt+方向：是就发挪浮窗的那个键。
//
// 控制台把 Alt 状态放在控制键位里（而不是另给一套虚拟键码），
// 所以只能在这里看一眼 —— 少了这一步，Alt+方向就和普通方向键一个样。
func arrowKey(ctrl uint32, plain, withAlt keyKind) event {
	if ctrl&(leftAltDown|rightAltDown) != 0 {
		return keyEvent(withAlt)
	}
	return keyEvent(plain)
}

// decodeMouseRecord 按偏移解出 MOUSE_EVENT_RECORD。
//
//	dwMousePosition   [0:4]   COORD，屏幕缓冲坐标
//	dwButtonState     [4:8]   低 16 位按钮，高 16 位滚轮增量（有符号）
//	dwControlKeyState [8:12]
//	dwEventFlags      [12:16]
func decodeMouseRecord(rec inputRecord, t *terminal) (event, bool) {
	x := int(int16(binary.LittleEndian.Uint16(rec.body[0:2])))
	y := int(int16(binary.LittleEndian.Uint16(rec.body[2:4])))
	buttons := binary.LittleEndian.Uint32(rec.body[4:8])
	flags := binary.LittleEndian.Uint32(rec.body[12:16])

	// 屏幕缓冲坐标 → 窗口内坐标（界面只关心可见区域）
	if info, ok := consoleInfo(t); ok {
		x -= int(info.window.left)
		y -= int(info.window.top)
	}

	switch {
	case flags&mouseWheeled != 0:
		delta := int16(binary.LittleEndian.Uint16(rec.body[6:8]))
		wheel := 1
		if delta > 0 {
			wheel = -1 // 向上滚
		}
		return event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Wheel: wheel}}, true
	case flags&mouseHWheeled != 0:
		return event{}, false
	case flags&mouseDoubleClk != 0:
		// 双击会先来一次单击，这里忽略避免动作被触发两次
		return event{}, false
	case flags&mouseMoved != 0:
		// 按住左键的移动才是拖动；没按住只是划过，界面不关心
		if buttons&buttonLeft == 0 {
			return event{}, false
		}
		return event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Drag: true}}, true
	case buttons&buttonLeft != 0:
		return event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}}, true
	case flags == 0:
		// 没有移动标志、左键也没按着：这是一次松手
		return event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Rel: true}}, true
	}
	return event{}, false
}
