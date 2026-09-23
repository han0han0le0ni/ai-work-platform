package main

import (
	"strings"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// 颜色与样式
// ---------------------------------------------------------------------------

const (
	cReset   = "\033[0m"
	cDim     = "\033[2m"
	cBold    = "\033[1m"
	cAccent  = "\033[38;5;111m"
	cAccent2 = "\033[38;5;141m"
	cOK      = "\033[38;5;79m"
	cWarn    = "\033[38;5;215m"
	cBad     = "\033[38;5;203m"
	cBorder  = "\033[38;5;240m"
	cBtnBG   = "\033[48;5;236m"
	cSelBG   = "\033[48;5;237m"
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

// oneLine 把内容压成一行：换行/制表符/连续空格都收成一个空格。
//
// 输入框是**单行**的。值里带换行就会把那一行劈成两行，右侧那一栏的内容
// 当场被顶歪 —— 而且不是歪一点，是整块错位。记忆天然是多行的，粘贴进来的
// 文本也可能带换行，所以显示前必须过这一道。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// tailDisplay 取**末尾**，开头补省略号。
//
// 给"只会往后长"的内容用（记忆是一条条追加的列表）。用截断显示开头的话，
// 用户刚记住的那条永远在框外，看起来就像没记上。
func tailDisplay(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if dispWidth(s) <= max {
		return s
	}
	rs := []rune(s)
	// 从后往前凑够宽度（留一列放省略号）
	w := 0
	i := len(rs)
	for i > 0 {
		rw := runeWidth(rs[i-1])
		if w+rw > max-1 {
			break
		}
		w += rw
		i--
	}
	return "…" + string(rs[i:])
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
// ---------------------------------------------------------------------------

// 插件自带界面（ui.page.*）送过来的是一整行带 ANSI 样式码的字符串。样式码本身
// 不占列，但按 rune 数一遍会把它算成十几个可见字符，于是"这一行到底几列"
// 就量错了 —— 而量错的直接后果是右边框歪掉。所以必须跳过转义序列再量。

// plainWidth 返回一行文本的显示列数，跳过 ANSI 转义序列。
func plainWidth(s string) int {
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = escSeqEnd(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w += runeWidth(r)
		i += size
	}
	return w
}

// stripANSI 剥掉所有 ANSI 转义序列，只留下可见内容。
//
// 终端不支持颜色时用它：不然满屏都是 [38;5;111m 这样的乱码。
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = escSeqEnd(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

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

// raw 写入一段**自带转义序列**的内容，并显式声明它占多少显示列。
//
// 缩略图这类内容每个格子都带自己的前景/背景色，走不了 styled ——
// styled 会给整段套同一个颜色，而且会把转义序列当成可见字符算进宽度，
// 边框立刻就会歪。所以宽度由调用方明说。
func (l *lineBuilder) raw(s string, width int) *lineBuilder {
	if s == "" || width <= 0 {
		return l
	}
	l.b.WriteString(s)
	l.width += width
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

// borderSplit 画正文与底栏之间的分隔线，┴ 与正文里的竖线对齐。
func borderSplit(w, leftW, rightW int, color bool) string {
	l := newLine(color)
	l.styled(cBorder, "├"+strings.Repeat("─", leftW))
	if rightW > 0 {
		l.styled(cBorder, "┴"+strings.Repeat("─", rightW))
	}
	l.styled(cBorder, "┤")
	return l.pad(w).String()
}

// ---------------------------------------------------------------------------
// 画布：整帧的行 + 可点击区域
//
// 立即模式：渲染时就登记好每个可点击区域的矩形，点击时按坐标反查动作。
// 这样"画了什么"和"能点什么"永远同步，不会出现按钮位置和热区错位。
// ---------------------------------------------------------------------------

type hitbox struct {
	x, y, w int
	action  string
	label   string // 这个热区上画的是哪段文字；空表示热区不绑定具体标签
}

type canvas struct {
	w, h  int
	color bool
	rows  []string
	boxes []hitbox
}

func newCanvas(w, h int, color bool) *canvas {
	return &canvas{w: w, h: h, color: color}
}

// row 追加一行。fn 里用 l.styled 拼内容，用 c.box 登记可点击区域。
func (c *canvas) row(fn func(l *lineBuilder, y int)) {
	y := len(c.rows)
	l := newLine(c.color)
	fn(l, y)
	c.rows = append(c.rows, l.pad(c.w).String())
}

// rowFn 渲染一行里的一栏。x 是该栏起始列，y 是行号（登记点击热区要用）。
type rowFn func(c *canvas, l *lineBuilder, y, x int)

// twoCol 追加一行「│ 左 │ 右 │」。leftW + rightW 必须等于 w-3。
func (c *canvas) twoCol(leftW, rightW int, left, right rowFn) {
	c.row(func(l *lineBuilder, y int) {
		l.styled(cBorder, "│")
		if left != nil {
			left(c, l, y, 1)
		}
		l.pad(1 + leftW)
		l.styled(cBorder, "│")
		rx := 2 + leftW
		if right != nil {
			right(c, l, y, rx)
		}
		l.pad(rx + rightW)
		l.styled(cBorder, "│")
	})
}

// oneCol 追加一行「│ 内容 │」，内容占满 w-2 列。
func (c *canvas) oneCol(fn rowFn) {
	c.row(func(l *lineBuilder, y int) {
		l.styled(cBorder, "│")
		if fn != nil {
			fn(c, l, y, 1)
		}
		l.pad(c.w - 1)
		l.styled(cBorder, "│")
	})
}

// box 登记一个可点击区域（高 1 行）。
func (c *canvas) box(action string, x, y, w int) {
	c.boxWithLabel(action, "", x, y, w)
}

// boxWithLabel 与 box 相同，额外记下热区上画的是哪段文字。
// 单测据此断言"画在哪就点哪"：只有画出来的东西才该被点到。
func (c *canvas) boxWithLabel(action, label string, x, y, w int) {
	if w <= 0 || action == "" {
		return
	}
	c.boxes = append(c.boxes, hitbox{x: x, y: y, w: w, action: action, label: label})
}

// hit 返回命中坐标的动作，没有命中返回空串。
func (c *canvas) hit(x, y int) string {
	for _, b := range c.boxes {
		if y == b.y && x >= b.x && x < b.x+b.w {
			return b.action
		}
	}
	return ""
}

// frame 输出整屏，行数补满到 h。
func (c *canvas) frame() string {
	for len(c.rows) < c.h {
		c.rows = append(c.rows, newLine(c.color).pad(c.w).String())
	}
	if len(c.rows) > c.h {
		c.rows = c.rows[:c.h]
	}
	return strings.Join(c.rows, "\n")
}

// button 画一个按钮并登记点击区域，返回下一个 x。
//
// 返回的 x 已经算进了按钮之间那 2 列间隔，但间隔不会当场画出来（要等行尾 pad）。
// 所以每次进来先 pad(x) 把光标推到登记位置：不这么做，第二个按钮起热区就会比
// 文字偏右 2 列、并随按钮个数逐个累积 —— 表现为"按钮点上去时灵时不灵"。
func (c *canvas) button(l *lineBuilder, y, x int, action, label string, active bool) int {
	style := cBtnBG + cBold
	if active {
		style = cAccent + cBold
	}
	text := " " + label + " "
	l.pad(x)
	w := dispWidth(text)
	l.styled(style, text)
	c.boxWithLabel(action, text, x, y, w)
	return x + w + 2
}

// ---------------------------------------------------------------------------
// 按列覆盖：把浮窗贴到已经画好的行上
// ---------------------------------------------------------------------------

// spliceColumns 把 line 里 [col, col+width) 这一段换成 text，两边照旧。
//
// 为什么不能按字节下标切：一行里混着 ANSI 样式码，\033[38;5;111m 有十一个字节
// 却不占显示列。按字节切等于把它砍成两半，屏幕上立刻是一串乱码。
//
// 所以这里按**显示列**走，并且记住跨越覆盖区时的样式 —— 覆盖区右边的内容
// 要接着用原来的颜色，否则会被浮窗的颜色一路染到行尾。
func spliceColumns(line string, col, width int, text string) string {
	if width <= 0 || col < 0 {
		return line
	}
	end := col + width

	var out strings.Builder
	style := ""      // 当前样式（最近一个 SGR 序列）
	at := 0          // 输入侧走到第几列
	outCols := 0     // 输出侧已经写出多少列
	written := false // text 是否已经写进去
	resumed := false // 覆盖区之后是否已经补回样式

	for i := 0; i < len(line); {
		// 转义序列：落在覆盖区里的丢掉（但要记住它设的样式），两侧的照原样搬
		if line[i] == 0x1b {
			j := escSeqEnd(line, i)
			seq := line[i:j]
			if strings.HasSuffix(seq, "m") {
				style = seq
			}
			switch {
			case at >= end:
				if !resumed {
					out.WriteString(style)
					resumed = true
				}
				out.WriteString(seq)
			case at < col:
				out.WriteString(seq)
			}
			i = j
			continue
		}

		r, size := utf8.DecodeRuneInString(line[i:])
		rw := runeWidth(r)

		// 走到覆盖区起点：先把浮窗贴上去。
		//
		// 中间可能有个缺口 —— 起点正好切在一个汉字身上时，那个汉字要让位，
		// 让掉的那半格得补成空格，否则整行就短了一列、界面从这儿开始歪。
		if at >= col && !written {
			if n := col - outCols; n > 0 {
				out.WriteString(strings.Repeat(" ", n))
			}
			out.WriteString(text)
			outCols = col + width
			written = true
		}

		switch {
		case at+rw <= col: // 完全在覆盖区左边
			out.WriteString(line[i : i+size])
			outCols += rw
		case at >= end: // 覆盖区右边：先接回被盖住那一格的样式
			if !resumed {
				out.WriteString(style)
				resumed = true
			}
			out.WriteString(line[i : i+size])
			outCols += rw
		default: // 落在覆盖区里（含跨在边界上的那个字）→ 整块让位
		}

		at += rw
		i += size
	}

	if !written {
		// 这一行比 col 还短（窗口极窄时会发生）：先补空格再贴
		if n := col - outCols; n > 0 {
			out.WriteString(strings.Repeat(" ", n))
		}
		out.WriteString(text)
	}
	return out.String()
}

// escSeqEnd 返回从 i 开始的转义序列结束后的下标。
func escSeqEnd(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return len(s)
	}
	if s[j] == '[' { // CSI：参数字节走到终止符（0x40–0x7E）为止
		j++
		for j < len(s) && s[j] < 0x40 {
			j++
		}
		if j < len(s) {
			j++
		}
		return j
	}
	return j + 1
}

// overlay 把一块矩形贴到画布上。浮窗用它盖在已经排好的版面上。
//
// width 是**显示列数**，必须由调用方给：行里可能混着 ANSI 样式码，
// 在这里现量一次 dispWidth 会把 \033[38;5;109m 当成十几个列，
// 结果就是多吃掉右边一截、整行短一列 —— 界面从这一行开始歪。
func (c *canvas) overlay(x, y, width int, lines []string) {
	for i, ln := range lines {
		row := y + i
		if row < 0 || row >= len(c.rows) {
			continue
		}
		c.rows[row] = spliceColumns(c.rows[row], x, width, ln)
	}
}

// chip 画一个可选项（单选样式），返回下一个 x。间隔的处理同 button。
func (c *canvas) chip(l *lineBuilder, y, x int, action, label string, selected bool) int {
	text := "  " + label + "  "
	style := cDim
	if selected {
		text = "● " + label + " "
		style = cAccent + cBold
	}
	l.pad(x)
	w := dispWidth(text)
	l.styled(style, text)
	c.boxWithLabel(action, text, x, y, w)
	return x + w + 2
}
