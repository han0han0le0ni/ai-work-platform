package main

import (
	"strings"
)

// ---------------------------------------------------------------------------
// 显示宽度：中文、emoji 占两列。终端排版最容易错的地方就在这里。
// ---------------------------------------------------------------------------

// wideRanges 必须保持有序，供二分查找使用。
var wideRanges = [][2]rune{
	{0x1100, 0x115F},   // 谚文字母
	{0x2E80, 0x303E},   // 康熙部首、CJK 符号
	{0x3041, 0x33FF},   // 平假名、片假名、注音、谚文兼容、CJK 兼容
	{0x3400, 0x4DBF},   // CJK 扩展 A
	{0x4E00, 0x9FFF},   // CJK 基本区
	{0xA000, 0xA4CF},   // 彝文
	{0xAC00, 0xD7A3},   // 谚文音节
	{0xF900, 0xFAFF},   // CJK 兼容表意
	{0xFE10, 0xFE19},   // 竖排形式
	{0xFE30, 0xFE6F},   // CJK 兼容形式
	{0xFF00, 0xFF60},   // 全角形式
	{0xFFE0, 0xFFE6},   // 全角符号
	{0x1F000, 0x1F0FF}, // 麻将、多米诺
	{0x1F200, 0x1F2FF}, // 带圈表意文字补充
	{0x1F300, 0x1F64F}, // 常用 emoji
	{0x1F680, 0x1F6FF}, // 交通类 emoji
	{0x1F900, 0x1F9FF},
	{0x1FA00, 0x1FAFF},
	{0x20000, 0x3FFFD}, // CJK 扩展 B 及以上
}

func isWide(r rune) bool {
	lo, hi := 0, len(wideRanges)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case r < wideRanges[mid][0]:
			hi = mid - 1
		case r > wideRanges[mid][1]:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}

// runeWidth 返回单个字符占用的终端列数。
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case r >= 0x0300 && r <= 0x036f: // 组合附加符号
		return 0
	case r >= 0x200b && r <= 0x200f: // 零宽字符
		return 0
	case r == 0xfeff:
		return 0
	case isWide(r):
		return 2
	}
	return 1
}

// dispWidth 返回字符串占用的终端列数。
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// cutDisplay 按显示宽度硬截断，不补省略号。
func cutDisplay(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if dispWidth(s) <= max {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > max {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

// truncDisplay 按显示宽度截断，截断时补省略号。
func truncDisplay(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if dispWidth(s) <= max {
		return s
	}
	return cutDisplay(s, max-1) + "…"
}

// padDisplay 右补空格到指定显示宽度。
func padDisplay(s string, w int) string {
	d := dispWidth(s)
	if d >= w {
		return s
	}
	return s + strings.Repeat(" ", w-d)
}

// wrapText 按显示宽度换行，中文友好；文本里的 \n 强制换行。
func wrapText(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		cur := &strings.Builder{}
		curW := 0
		for _, r := range para {
			rw := runeWidth(r)
			if curW+rw > w {
				out = append(out, cur.String())
				cur.Reset()
				curW = 0
			}
			cur.WriteRune(r)
			curW += rw
		}
		out = append(out, cur.String())
	}
	return out
}

// ---------------------------------------------------------------------------
// 单元格：一段带样式的文本 + 它占用多少列
//
// 关键点：宽度只在构造时按**纯文本**计算一次，样式码永远不参与宽度。
// 之前把带 ANSI 的字符串当纯文本补空格的写法会算错，这里从结构上避免。
// ---------------------------------------------------------------------------

type segment struct {
	style string
	text  string
}

type cell struct {
	segs  []segment
	width int
}

func newCell(segs ...segment) cell {
	c := cell{segs: segs}
	for _, s := range segs {
		c.width += dispWidth(s.text)
	}
	return c
}

func plainCell(s string) cell { return newCell(segment{"", s}) }

// trunc 把单元格压到指定显示宽度，超出部分用省略号收尾。
func (c cell) trunc(max int) cell {
	if c.width <= max {
		return c
	}
	limit := max - 1
	if limit < 0 {
		limit = 0
	}
	out := cell{}
	used := 0
	for _, s := range c.segs {
		if used >= limit {
			break
		}
		txt := cutDisplay(s.text, limit-used)
		if txt == "" {
			continue
		}
		out.segs = append(out.segs, segment{s.style, txt})
		used += dispWidth(txt)
	}
	out.segs = append(out.segs, segment{"", "…"})
	out.width = used + 1
	return out
}

// pad 把单元格补到指定显示宽度。
func (c cell) pad(w int) cell {
	if c.width >= w {
		return c
	}
	c.segs = append(c.segs, segment{"", strings.Repeat(" ", w-c.width)})
	c.width = w
	return c
}

// fit 先截断再补齐，保证单元格宽度精确等于 w。
func (c cell) fit(w int) cell {
	if c.width > w {
		c = c.trunc(w)
	}
	return c.pad(w)
}

// ---------------------------------------------------------------------------
// 逐行拼装器
// ---------------------------------------------------------------------------

type lineBuilder struct {
	b     strings.Builder
	width int
	color bool
}

func newLine(color bool) *lineBuilder { return &lineBuilder{color: color} }

func (l *lineBuilder) styled(code, s string) *lineBuilder {
	if s == "" {
		return l
	}
	if l.color && code != "" {
		l.b.WriteString(code)
		l.b.WriteString(s)
		l.b.WriteString(cReset)
	} else {
		l.b.WriteString(s)
	}
	l.width += dispWidth(s)
	return l
}

func (l *lineBuilder) put(c cell) *lineBuilder {
	for _, s := range c.segs {
		l.styled(s.style, s.text)
	}
	return l
}

func (l *lineBuilder) pad(w int) *lineBuilder {
	if l.width < w {
		l.b.WriteString(strings.Repeat(" ", w-l.width))
		l.width = w
	}
	return l
}

func (l *lineBuilder) String() string { return l.b.String() }

// ---------------------------------------------------------------------------
// 边框
// ---------------------------------------------------------------------------

func borderTop(w int, title, right string, color bool) string {
	const (
		capLeft  = "┌─ "
		capRight = " ─┐"
	)
	l := newLine(color)
	// 标题先按可用宽度收窄，保证后再怎么算都不会溢出
	titleMax := w - dispWidth(capLeft) - dispWidth(capRight)
	if titleMax < 1 {
		titleMax = 1
	}
	title = truncDisplay(title, titleMax)

	l.styled(cBorder, capLeft)
	l.styled(cBold+cAccent2, title)
	used := dispWidth(capLeft) + dispWidth(title)

	if right != "" {
		need := used + dispWidth(right) + dispWidth(capRight) + 2
		if fill := w - need; fill >= 2 {
			l.styled(cBorder, " "+strings.Repeat("─", fill)+" ")
			l.styled(cDim, right)
			l.styled(cBorder, capRight)
			return l.pad(w).String()
		}
	}
	fill := w - used - dispWidth(capRight)
	if fill < 0 {
		fill = 0
	}
	l.styled(cBorder, strings.Repeat("─", fill)+capRight)
	return l.pad(w).String()
}

func borderBottom(w int, hint string, color bool) string {
	const capLeft = "└─ "
	l := newLine(color)
	l.styled(cBorder, capLeft)

	// └─ <提示> ────…────┘  至少保留一条填充线，所以提示最多 w-6 列
	room := w - dispWidth(capLeft) - 3
	if room < 1 {
		room = 1
	}
	if dispWidth(hint) > room {
		hint = truncDisplay(hint, room)
	}
	l.styled(cDim, hint)

	fill := w - dispWidth(capLeft) - dispWidth(hint) - 2
	if fill < 1 {
		fill = 1
	}
	l.styled(cBorder, " "+strings.Repeat("─", fill)+"┘")
	return l.pad(w).String()
}

// borderSplit 画正文与输入区之间的分隔线，┴ 必须和正文里的竖线对齐。
func borderSplit(w, leftW, rightW int, color bool) string {
	l := newLine(color)
	l.styled(cBorder, "├"+strings.Repeat("─", leftW))
	if rightW > 0 {
		l.styled(cBorder, "┴"+strings.Repeat("─", rightW))
	}
	l.styled(cBorder, "┤")
	return l.pad(w).String()
}

// bodyRow 拼一行正文：│ 左栏 │ 右栏 │
func bodyRow(left, right cell, leftW, rightW int, color bool) string {
	l := newLine(color)
	l.styled(cBorder, "│")
	l.put(left.fit(leftW))
	if rightW > 0 {
		l.styled(cBorder, "│")
		l.put(right.fit(rightW))
	}
	l.styled(cBorder, "│")
	return l.String()
}

// ---------------------------------------------------------------------------
// 整帧渲染
// ---------------------------------------------------------------------------

type chatMsg struct {
	role string
	at   string
	text string
}

type serviceStatus struct {
	name     string
	alive    bool
	provider string
	note     string
}

type uiState struct {
	coreVersion string
	apiVersion  string
	pluginID    string
	services    []serviceStatus
	messages    []chatMsg
	input       []rune
	cursor      int
	busy        bool
	spinner     int
	events      []string
}

// sidebarMinWidth 低于这个宽度就隐藏右侧栏，保证对话区可用。
const sidebarMinWidth = 84

// sidebarWidth 是右侧栏宽度。
const sidebarWidth = 26

// roleStyle 返回角色对应的配色与标签。
func roleStyle(role string) (code, label string) {
	switch role {
	case "你":
		return cAccent, "你"
	case "AI":
		return cAccent2, "AI"
	case "错误":
		return cBad, "错误"
	default:
		return cDim, "系统"
	}
}

var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// renderLeft 把消息渲染成正文行（已按宽度换行）。
func renderLeft(st *uiState, w int, color bool) []cell {
	out := []cell{}
	for i, m := range st.messages {
		if i > 0 {
			out = append(out, plainCell(""))
		}
		code, label := roleStyle(m.role)
		out = append(out, newCell(
			// 标签补齐到 4 列，让不同角色的时间戳纵向对齐
			segment{code + cBold, " " + padDisplay(label, 4)},
			segment{cDim, " " + m.at},
		))
		for _, ln := range wrapText(m.text, w-4) {
			out = append(out, plainCell("    "+ln))
		}
	}

	if st.busy {
		spin := string(spinnerFrames[st.spinner%len(spinnerFrames)])
		out = append(out, plainCell(""), newCell(
			segment{cAccent2, " " + spin},
			segment{cDim, " 正在转发…"},
		))
	}
	return out
}

// renderRight 渲染右侧栏：服务链路 + 事件总线。
//
// 服务多到装不下时自动省略“提供者”细节行，优先保证列表本身完整。
func renderRight(st *uiState, w int, availH int) []cell {
	out := []cell{}
	showDetail := len(st.services)*2+len(st.events)+6 <= availH

	out = append(out, newCell(segment{cDim + cBold, "服务链路"}))
	for _, s := range st.services {
		mark, style := "✗", cBad
		if s.alive {
			mark, style = "✓", cOK
		}
		out = append(out, newCell(
			segment{style, " " + mark + " "},
			segment{"", truncDisplay(s.name, w-2)},
		))
		if !showDetail {
			continue
		}
		detail := s.note
		if s.alive {
			detail = s.provider
		}
		if detail != "" {
			out = append(out, newCell(segment{cDim, "    " + truncDisplay(detail, w-5)}))
		}
	}

	out = append(out, plainCell(""), newCell(segment{cDim + cBold, "事件总线"}))
	if len(st.events) == 0 {
		out = append(out, newCell(segment{cDim, " 输入 /events on 开启"}))
	}
	for _, e := range st.events {
		out = append(out, newCell(segment{cDim, " " + truncDisplay(e, w-1)}))
	}
	return out
}

// renderFrame 把状态渲染成整屏。返回的每一行显示宽度都精确等于 w。
func renderFrame(st *uiState, w, h int, color bool) string {
	if w < 20 || h < 6 {
		return ""
	}

	right := ""
	if st.coreVersion != "" {
		right = "core v" + st.coreVersion + " · apiVersion " + st.apiVersion
	}

	lines := make([]string, 0, h)
	lines = append(lines, borderTop(w, "AI 工作平台 · 极简 UI", right, color))

	// 行分配：顶部边框 1 + 正文 + 分隔 1 + 输入 1 + 底部边框 1
	bodyH := h - 4

	sideW := 0
	leftW := w - 2
	if w >= sidebarMinWidth {
		sideW = sidebarWidth
		leftW = w - 3 - sideW
	}

	left := renderLeft(st, leftW, color)
	rightCells := renderRight(st, sideW-2, bodyH)

	// 贴底显示
	start := len(left) - bodyH
	if start < 0 {
		start = 0
	}

	for i := 0; i < bodyH; i++ {
		var lc, rc cell
		if idx := start + i; idx >= 0 && idx < len(left) {
			lc = left[idx]
		}
		if sideW > 0 && i < len(rightCells) {
			rc = rightCells[i]
		}
		lines = append(lines, bodyRow(lc, rc, leftW, sideW, color))
	}

	lines = append(lines, borderSplit(w, leftW, sideW, color))
	lines = append(lines, inputRow(st, w, color))
	lines = append(lines, borderBottom(w, "Enter 发送 · ↑↓ 历史 · /help 命令 · Ctrl+C 退出界面", color))

	return strings.Join(lines, "\n")
}

// inputWindow 计算输入行的显示窗口：返回起始 rune 下标与已渲染的列宽。
func inputWindow(st *uiState, avail int) (from int, widths []int, cursorW int) {
	widths = make([]int, len(st.input))
	for i, r := range st.input {
		widths[i] = runeWidth(r)
		if i < st.cursor {
			cursorW += runeWidth(r)
		}
	}
	if avail < 1 {
		avail = 1
	}
	if cursorW < avail {
		return 0, widths, cursorW
	}
	// 光标越界时把窗口右移，让光标停在窗口 3/4 处
	target := cursorW - avail*3/4
	acc := 0
	for i := 0; i < len(st.input); i++ {
		if acc >= target {
			return i, widths, cursorW
		}
		acc += widths[i]
	}
	return len(st.input), widths, cursorW
}

// inputRow 渲染输入行，输入过长时围绕光标开窗。
func inputRow(st *uiState, w int, color bool) string {
	const prompt = " 你 › "
	avail := w - 2 - dispWidth(prompt)
	if avail < 1 {
		avail = 1
	}
	from, widths, _ := inputWindow(st, avail)

	l := newLine(color)
	l.styled(cBorder, "│")
	l.styled(cAccent+cBold, prompt)

	used := 0
	for i := from; i < len(st.input); i++ {
		if used+widths[i] > avail {
			break
		}
		l.styled("", string(st.input[i]))
		used += widths[i]
	}
	l.pad(w - 1)
	l.styled(cBorder, "│")
	return l.String()
}

// cursorPos 返回光标在输入行内的列偏移（0 基），供定位真实光标。
func cursorPos(st *uiState, w int) int {
	const prompt = " 你 › "
	avail := w - 2 - dispWidth(prompt)
	from, widths, _ := inputWindow(st, avail)

	col := dispWidth(prompt) + 1 // +1：左侧边框
	for i := from; i < st.cursor && i < len(st.input); i++ {
		col += widths[i]
	}
	if col > w-2 {
		col = w - 2
	}
	return col
}
