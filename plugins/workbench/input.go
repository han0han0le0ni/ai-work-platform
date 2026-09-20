package main

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// 统一输入事件
//
// 键盘和鼠标最终都变成这里的 event，界面主循环只认识这一种东西。
// 两条来源通路都归一到同一个模型：
//   - Windows 真控制台：ReadConsoleInputW 的原生事件（console_windows.go）
//   - 其他情况：字节流 + 转义序列解析（下面的 eventParser）
// ---------------------------------------------------------------------------

type eventKind int

const (
	evKey eventKind = iota
	evMouse
	evResize
)

type keyKind int

const (
	keyRune keyKind = iota
	keyEnter
	keyBackspace
	keyDelete
	keyLeft
	keyRight
	keyUp
	keyDown
	keyHome
	keyEnd
	keyTab
	keyEsc
	keyCtrlL
	keyCtrlC

	// Alt+方向键：挪浮窗。
	//
	// 不先"点一下选中"再挪：浮窗挡住了什么、该挪到哪儿，用户在按之前就看见了，
	// 多一步选中纯属打扰。而单独的 Esc/方向键早有别的用途，只能用带修饰键的组合。
	keyAltUp
	keyAltDown
	keyAltLeft
	keyAltRight

	keyUnknown
)

type mouseEvent struct {
	X, Y  int // 窗口内坐标，0 基
	Wheel int // -1 上滚，+1 下滚，0 表示点击
	Press bool

	// Drag / Rel 是拖动的两头：按住左键移动、松开。
	//
	// 原先两者都被丢掉了（注释写着"界面不需要"）—— 那是浮窗还不会动的时候。
	// 要拖得动，这一对必须一路送到界面。
	Drag bool
	Rel  bool
}

type event struct {
	kind  eventKind
	key   keyKind
	r     rune
	mouse mouseEvent

	// paste 表示这个字符来自"粘贴"而不是逐键输入。
	//
	// 必须区分：密钥里混进制表符或换行是常事，而制表符正好是界面里的"切页"键 ——
	// 一次粘贴就能把用户从设置页弹回聊天页。所以粘贴来的字符只当文本，绝不当导航键。
	paste bool
}

func keyEvent(k keyKind) event { return event{kind: evKey, key: k} }

// startInput 启动输入读取，返回事件通道与停止函数。
//
// 通道关闭表示输入源结束（例如重定向的文件读到 EOF），界面据此退出。
// hold=true 时不关闭通道，而是在 EOF 后继续等新追加的输入 —— 供自动化驱动分步喂输入。
func startInput(t *terminal, hold bool) (<-chan event, func()) {
	out := make(chan event, 64)
	quit := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(quit) }) }

	if t.native {
		go pumpNative(t, out, quit)
	} else {
		go pumpBytes(t, out, quit, hold)
	}
	return out, stop
}

func pumpBytes(t *terminal, out chan<- event, quit <-chan struct{}, hold bool) {
	p := &eventParser{}
	buf := make([]byte, 512)
	for {
		n, err := t.in.Read(buf)
		for _, b := range buf[:n] {
			for _, ev := range p.feed(b) {
				select {
				case out <- ev:
				case <-quit:
					return
				}
			}
		}
		if err != nil {
			if hold && err == io.EOF {
				select {
				case <-time.After(40 * time.Millisecond):
					continue
				case <-quit:
					return
				}
			}
			close(out)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// 字节流解析
// ---------------------------------------------------------------------------

var (
	pasteStartSeq = []byte("\033[200~")
	pasteEndSeq   = []byte("\033[201~")
)

// csiKeys 覆盖方向键、Home/End、Delete 的常见编码。
//
// 带修饰键的方向键是 "1;<修饰>字母"：3 = Alt，5 = Ctrl。终端里挪浮窗靠 Alt 那一组。
var csiKeys = map[string]keyKind{
	"A": keyUp, "B": keyDown, "C": keyRight, "D": keyLeft,
	"H": keyHome, "F": keyEnd, "Z": keyTab,
	"1~": keyHome, "4~": keyEnd, "7~": keyHome, "8~": keyEnd, "3~": keyDelete,
	"1;3A": keyAltUp, "1;3B": keyAltDown, "1;3C": keyAltRight, "1;3D": keyAltLeft,
}

// maxPasteBytes 是单次粘贴的缓冲上限，防止未闭合的粘贴把内存吃掉。
const maxPasteBytes = 256 * 1024

type eventParser struct {
	esc  bool
	raw  []byte
	utf8 []byte

	// 括号粘贴（bracketed paste）：开启后终端把粘贴内容包在 200~ … 201~ 之间，
	// 于是"粘贴"和"逐键输入"可以精确区分。
	pasting  bool
	pasteBuf []byte
}

func (p *eventParser) feed(b byte) []event {
	if p.pasting {
		p.pasteBuf = append(p.pasteBuf, b)
		if bytes.HasSuffix(p.pasteBuf, pasteEndSeq) {
			body := p.pasteBuf[:len(p.pasteBuf)-len(pasteEndSeq)]
			p.pasting, p.pasteBuf = false, nil
			return pasteEvents(body)
		}
		if len(p.pasteBuf) > maxPasteBytes {
			body := p.pasteBuf
			p.pasteBuf = nil
			return pasteEvents(body)
		}
		return nil
	}

	// 正在拼装转义序列：把后续字节攒起来交给解析器。
	// 方向键、鼠标、括号粘贴全都走这条路 —— 少了它，'[' 会变成普通字符。
	if p.esc {
		p.raw = append(p.raw, b)
		return p.parseEscape()
	}

	switch b {
	case 0x1b:
		p.esc, p.raw = true, nil
		return nil
	case 0x0d, 0x0a:
		return []event{keyEvent(keyEnter)}
	case 0x7f, 0x08:
		return []event{keyEvent(keyBackspace)}
	case 0x09:
		return []event{keyEvent(keyTab)}
	case 0x03:
		return []event{keyEvent(keyCtrlC)}
	case 0x0c:
		return []event{keyEvent(keyCtrlL)}
	}
	if b < 0x20 {
		return nil
	}

	p.utf8 = append(p.utf8, b)
	if !utf8.FullRune(p.utf8) {
		return nil
	}
	r, _ := utf8.DecodeRune(p.utf8)
	p.utf8 = nil
	if r == utf8.RuneError {
		return nil
	}
	return []event{{kind: evKey, key: keyRune, r: r}}
}

func (p *eventParser) parseEscape() []event {
	raw := p.raw
	if len(raw) == 0 {
		return nil
	}
	reset := func() { p.esc, p.raw = false, nil }

	// SGR 鼠标：ESC [ < b ; x ; y (M|m)
	if raw[0] == '[' && len(raw) > 1 && raw[1] == '<' {
		last := raw[len(raw)-1]
		if last == 'M' || last == 'm' {
			reset()
			if ev, ok := parseSGRMouse(string(raw[2:len(raw)-1]), last == 'M'); ok {
				return []event{ev}
			}
			return nil
		}
		if len(raw) > 32 { // 畸形序列，别把缓冲区撑爆
			reset()
		}
		return nil
	}

	// 传统 X10 鼠标：ESC [ M b x y（坐标是 1 基 + 32 偏移）
	if raw[0] == '[' && len(raw) > 1 && raw[1] == 'M' {
		if len(raw) < 5 {
			return nil
		}
		reset()
		btn := int(raw[2]) - 32
		x := int(raw[3]) - 33
		y := int(raw[4]) - 33
		if btn&0x40 != 0 {
			wheel := 1
			if btn&1 == 0 {
				wheel = -1
			}
			return []event{{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Wheel: wheel}}}
		}
		if btn&0x03 == 3 { // 按钮 3 = 松手
			return []event{{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Rel: true}}}
		}
		if btn&0x20 != 0 { // 按住左键移动
			return []event{{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Drag: true}}}
		}
		if btn&0x03 == 0 {
			return []event{{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}}}
		}
		return nil
	}

	// 其他 CSI / SS3 序列
	if raw[0] == '[' || raw[0] == 'O' {
		body := string(raw[1:])
		switch body {
		case "200~": // 粘贴开始
			reset()
			p.pasting, p.pasteBuf = true, nil
			return nil
		case "201~": // 孤立的粘贴结束标记：忽略
			reset()
			return nil
		}
		if kind, ok := csiKeys[body]; ok {
			reset()
			return []event{keyEvent(kind)}
		}
		if isCSIPrefix(body) {
			return nil // 序列还没收完
		}
		reset() // 未知序列整个吞掉，避免 "[9Z" 之类变成垃圾字符
		return nil
	}

	// ESC + 单字符（等价于 Alt+键）：两个键都发出去，避免丢字符
	reset()
	out := []event{keyEvent(keyEsc)}
	if raw[0] >= 0x20 {
		out = append(out, p.feed(raw[0])...)
	}
	return out
}

// isCSIPrefix 判断 body 是否还是某个已知序列的前缀。
//
// 括号粘贴的 "200~" / "201~" 必须算进来：它们逐个字节到达，
// 少了这一条，"2" 就会被当成未知序列整个吞掉，粘贴内容永远等不到。
func isCSIPrefix(body string) bool {
	if body == "" {
		return true
	}
	for known := range csiKeys {
		if strings.HasPrefix(known, body) {
			return true
		}
	}
	for _, known := range []string{"200~", "201~"} {
		if strings.HasPrefix(known, body) {
			return true
		}
	}
	return false
}

// pasteEvents 把粘贴内容整体转成文本字符（带 paste 标记）。
func pasteEvents(body []byte) []event {
	out := make([]event, 0, len(body))
	for _, r := range string(body) {
		out = append(out, event{kind: evKey, key: keyRune, r: r, paste: true})
	}
	return out
}

// parseSGRMouse 解析 SGR 鼠标参数 "b;x;y"。
func parseSGRMouse(params string, press bool) (event, bool) {
	parts := strings.Split(params, ";")
	if len(parts) < 3 {
		return event{}, false
	}
	btn, err1 := strconv.Atoi(parts[0])
	x, err2 := strconv.Atoi(parts[1])
	y, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return event{}, false
	}

	me := mouseEvent{X: x - 1, Y: y - 1} // SGR 坐标是 1 基
	switch {
	case btn&0x40 != 0: // 滚轮
		if btn&1 == 0 {
			me.Wheel = -1
		} else {
			me.Wheel = 1
		}
	case btn&0x20 != 0: // 按住左键移动
		me.Drag = true
	case !press: // 结尾是 m 而不是 M：松手
		me.Rel = true
	case btn&0x03 == 0:
		me.Press = true
	default:
		return event{}, false
	}
	return event{kind: evMouse, mouse: me}, true
}
