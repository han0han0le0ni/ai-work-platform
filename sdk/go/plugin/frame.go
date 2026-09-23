package plugin

import (
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// 插件自带界面的绘制原语（配合 ui.panel.v1）
//
// 为什么需要这一层：ui.v1 同一时刻只能有一个所有者（终端只能被一个界面接管），
// 所以插件没法各自去开终端。插件自带界面的办法是 —— **自己把要显示的东西画成
// 一组文本行 + 若干可点区域**，交给 UI 插件去合成、去写终端。
//
// 于是有一条硬约束：算出来的行宽必须和终端实际占的列数一致（中文、emoji 占两列），
// 差一列宿主的右边框就会歪。这件事最容易错，也最不该让每个插件各写一遍，
// 所以宽度计算、边界截断、文本框都放在这里。
//
// 一次渲染 = 一块"恰好 w 列 × h 行"的画面（Block）+ 若干可点区域（Hit）：
//
//	b := plugin.NewBlock(w, h)
//	b.At(0).Text(plugin.Bold, "我的插件").Fill(plugin.Dim)
//	b.Hit(0, 0, 8, "about", "")
//	return b.Render(), nil   // 交给 ui.panel.v1.render 返回
//
// Block 保证"每行不多不少 w 列"：Text 写超了会截断，Lines() 会把不足的补齐。
// 与其让插件算错宽度把人家整屏顶歪，不如在这里默默收住。
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 显示宽度
// ---------------------------------------------------------------------------

// wideRanges 必须保持有序，供二分查找使用。
//
// 这份表与 UI 插件（workbench/render.go）里的那份是**同一套规则**：
// 两边算得不一样，边框就会歪。workbench 有一个用例专门逐字符比对两者。
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

// IsWideRune 判断一个字符是否占两列。
func IsWideRune(r rune) bool {
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

// RuneWidth 返回单个字符占用的终端列数。
func RuneWidth(r rune) int {
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
	case IsWideRune(r):
		return 2
	}
	return 1
}

// DisplayWidth 返回纯文本占用的终端列数。
//
// 传进来的**不能带 ANSI 样式码**：那串字符本身也占字节，会被算成可见宽度。
// 带样式的内容请用 Block/Line 拼，宽度由那边统一记账。
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += RuneWidth(r)
	}
	return w
}

// CutDisplay 按显示宽度硬截断，不补省略号。
func CutDisplay(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if DisplayWidth(s) <= max {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := RuneWidth(r)
		if w+rw > max {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

// Truncate 按显示宽度截断，截断时补省略号。
func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if DisplayWidth(s) <= max {
		return s
	}
	return CutDisplay(s, max-1) + "…"
}

// Pad 右补空格到指定显示宽度；已经够宽就原样返回。
func Pad(s string, w int) string {
	d := DisplayWidth(s)
	if d >= w {
		return s
	}
	return s + strings.Repeat(" ", w-d)
}

// OneLine 把内容压成一行：换行 / 制表符 / 连续空格都收成一个空格。
func OneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Wrap 按显示宽度折行；文本里的 \n 强制换行。
//
// 折行是按显示列算的，所以中英混排不会切出半个字。
func Wrap(s string, w int) []string {
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
			rw := RuneWidth(r)
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
// 样式
//
// 只用最通用的 SGR：宿主会把整行原样写进终端，不认识的序列由终端自己忽略。
// 想彻底不带样式就把 Style 都留空，宿主在无色终端下也会主动剥掉这些码。
// ---------------------------------------------------------------------------

const (
	SReset   = "\033[0m"
	SBold    = "\033[1m"
	SDim     = "\033[2m"
	SUnder   = "\033[4m"
	SAccent  = "\033[38;5;111m" // 主色（蓝）
	SAccent2 = "\033[38;5;141m" // 次色（紫）
	SOK      = "\033[38;5;79m"  // 成功（绿）
	SWarn    = "\033[38;5;215m" // 提醒（橙）
	SBad     = "\033[38;5;203m" // 失败（红）
	SBorder  = "\033[38;5;240m" // 边框（灰）
	SSel     = "\033[48;5;237m" // 选中底色
	SBtn     = "\033[48;5;236m" // 按钮底色
)

// Style 是一段 SGR 前缀。空串 = 不套样式。
type Style string

// S 把一个 Style 套到文本上（空 Style 原样返回）。
func (st Style) S(text string) string {
	if st == "" || text == "" {
		return text
	}
	return string(st) + text + SReset
}

// ---------------------------------------------------------------------------
// 画面
// ---------------------------------------------------------------------------

// Hit 是画面上的一个可点区域，坐标是**面板内**的（左上角为 0,0）。
//
// Action 是插件自己定义的动作名，宿主点击后原样回传给 ui.panel.v1.action；
// Arg 用来带参数（第几行、第几列、哪个条目）。两者都可以是任意字符串 ——
// 宿主不解析它们，只负责原样送回来。
type Hit struct {
	Y      int    `json:"y"`
	X      int    `json:"x"`
	W      int    `json:"w"`
	Action string `json:"action"`
	Arg    string `json:"arg,omitempty"`
}

// PanelRequest 是宿主发过来的参数（除 list 之外的方法共用）。
//
// 放在 SDK 里而不是各插件各定义一份：字段名就是协议本身，两边理解不一致
// 就会出现"点了没反应"这种最难查的问题。
type PanelRequest struct {
	Panel  string `json:"panel"`  // 哪个页面
	W      int    `json:"w"`      // 渲染：可用的宽（列）
	H      int    `json:"h"`      // 渲染：可用的高（行）
	Scroll int    `json:"scroll"` // 渲染：宿主当前滚到哪儿（插件夹好后原样报回）
	Action string `json:"action"` // 动作：插件自己定义的动作名
	Arg    string `json:"arg"`    // 动作：参数（第几项、第几列）
	Kind   string `json:"kind"`   // 按键：rune / backspace / enter / up …
	Text   string `json:"text"`   // 按键：字符内容（kind=rune 时用）
}

// PanelInfo 是插件报给宿主的"我有这么一页"。
type PanelInfo struct {
	ID    string `json:"id"`    // 稳定标识：切页与缓存都用它
	Title string `json:"title"` // 标签上的名字，越短越好（两三个字）
	Order int    `json:"order"` // 排序，小的靠前；相同则按 ID
	Note  string `json:"note,omitempty"`
}

// PanelRender 是一次渲染的结果。
//
// Lines 必须是**恰好 h 行**、每行不超过 w 列（用 Block 拼就不会错）。
// 宿主会再兜一层：宽度对不上就剥掉样式按纯文本重排，宁可难看也不让整屏歪。
type PanelRender struct {
	Title     string   `json:"title,omitempty"` // 覆盖标签名（可随状态变）
	Lines     []string `json:"lines"`
	Hits      []Hit    `json:"hits,omitempty"`
	Scroll    int      `json:"scroll"`              // 实际生效的滚动位置（宿主按它显示进度）
	MaxScroll int      `json:"maxScroll"`           // 还能往下滚多少
	Note      string   `json:"note,omitempty"`      // 底部那一行说明（宿主照抄）
	WantKeys  bool     `json:"wantKeys,omitempty"`  // 要不要键盘：要的话除了 Tab 全给插件
	WantFocus bool     `json:"wantFocus,omitempty"` // 想收多行输入（宿主据此放宽回车/粘贴）
	// WantCopy 声明"我这一页有可选的文本，Ctrl+C 请转给我复制"。
	//
	// 带输入框的页面都该置 true。宿主据此把 Ctrl+C 也转给插件：插件有选择就
	// 回 copy 让宿主写剪贴板，没选择就回空（不退出）。退出永远是宿主的 Ctrl+Q，
	// 不经过插件、也不和复制抢 Ctrl+C 这一个键。
	WantCopy bool `json:"wantCopy,omitempty"`
}

// WidgetInfo 是插件报给宿主的"我有一个浮窗"。
//
// 浮窗是盖在界面上、可以拖、可以收起成一条的小矩形。它和页面（ui.page.*）是
// 两种 UI：页面占一整块标签页，浮窗浮在任意位置、不被翻页影响。
type WidgetInfo struct {
	ID    string `json:"id"`    // 稳定标识：一个插件可以有多个浮窗
	Title string `json:"title"` // 收起成一条时显示的字，拖拽时的标题
	W     int    `json:"w"`     // 建议展开宽（列）
	H     int    `json:"h"`     // 建议展开高（行）
}

// WidgetRender 是一次浮窗渲染的结果。
//
// Lines 是展开态的每一行，宽度应恰好等于 w（宿主会兜底）。收起态不用插件给：
// 宿主拿 Title 自己生成一条 chip。所以插件只需实现展开态这一份。
type WidgetRender struct {
	Title string   `json:"title,omitempty"` // 可覆盖 WidgetInfo.Title（比如带条数）
	Lines []string `json:"lines"`
	Hits  []Hit    `json:"hits,omitempty"` // 浮窗上的可点区域，坐标相对浮窗左上角
}

// Line 是 Block 里的一行。所有写入都受目标宽度约束，永远不会越界。
type Line struct {
	buf   strings.Builder
	col   int
	width int
}

// Text 追加一段文本。超出这一行剩余宽度的部分会被截掉。
func (l *Line) Text(st Style, text string) *Line {
	if l == nil || text == "" {
		return l
	}
	room := l.width - l.col
	if room <= 0 {
		return l
	}
	text = CutDisplay(text, room)
	if text == "" {
		return l
	}
	l.buf.WriteString(st.S(text))
	l.col += DisplayWidth(text)
	return l
}

// Fill 用 ch 把这一行剩下的宽度填满（ch 为空按空格）。
func (l *Line) Fill(st Style, ch string) *Line {
	if l == nil {
		return l
	}
	if ch == "" {
		ch = " "
	}
	if chW := DisplayWidth(ch); chW > 1 || chW == 0 {
		ch = " "
	}
	room := l.width - l.col
	if room <= 0 {
		return l
	}
	l.buf.WriteString(st.S(strings.Repeat(ch, room)))
	l.col += room
	return l
}

// Col 返回这一行已经写了多少列。
func (l *Line) Col() int { return l.col }

// String 返回这一行已经写进去的内容（含样式码，不含补白）。
func (l *Line) String() string { return l.buf.String() }

// Block 是一块"恰好 w 列 × h 行"的画面。
type Block struct {
	w, h  int
	lines []*Line
	hits  []Hit
}

// NewBlock 建一块画面。
func NewBlock(w, h int) *Block {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	b := &Block{w: w, h: h, lines: make([]*Line, h)}
	for i := range b.lines {
		b.lines[i] = &Line{width: w}
	}
	return b
}

// W 返回画面宽度，H 返回高度。
func (b *Block) W() int { return b.w }
func (b *Block) H() int { return b.h }

// At 取第 y 行来写。超出范围返回 nil（调用方链式调用，nil 会被忽略）。
func (b *Block) At(y int) *Line {
	if y < 0 || y >= b.h {
		return nil
	}
	return b.lines[y]
}

// Hit 在 (y, x) 处登记一个宽 w 的可点区域。
//
// 超出画面的部分会被丢掉：登记了却画不出来的热区，点上去"时灵时不灵"，
// 是最难查的一类问题，宁可不登记。
func (b *Block) Hit(y, x, w int, action, arg string) {
	if b == nil || action == "" || w <= 0 || y < 0 || y >= b.h {
		return
	}
	if x < 0 {
		w += x
		x = 0
	}
	if x+w > b.w {
		w = b.w - x
	}
	if w <= 0 {
		return
	}
	b.hits = append(b.hits, Hit{Y: y, X: x, W: w, Action: action, Arg: arg})
}

// Hits 返回已登记的可点区域。
func (b *Block) Hits() []Hit { return b.hits }

// Button 在 (y, x) 画一个按钮并登记热区，返回下一个可用列（含按钮之间的 2 列间隔）。
//
// 越界就不画也不登记 —— 画不出来的按钮，用户根本不知道它存在，
// 留一个点得到却看不见的热区只会更难查。
func (b *Block) Button(y, x int, label, action, arg string, primary bool) int {
	if b == nil || y < 0 || y >= b.h {
		return x
	}
	text := " " + label + " "
	w := DisplayWidth(text)
	if x < 0 || x+w > b.w {
		return x
	}
	st := Style(SBtn + SBold)
	if primary {
		st = Style(SAccent + SBold)
	}
	b.At(y).Text(st, text)
	b.Hit(y, x, w, action, arg)
	return x + w + 2
}

// Chip 画一个"多选一"的小选项，返回下一个可用列。
//
// 与 Button 的区别在**语义**，不只是长相：按钮是"按一下做一件事"（做完就完了），
// chip 是"这些里现在选的是哪个"（它有个持续的状态）。画法上 chip 用 ● 与高亮标出
// 当前选中项，一眼能看出"现在连的是哪个平台 / 说的是哪套协议"。
//
// 之所以要单独给一个原语：插件页里但凡有"平台 / 协议 / 模式"这类选项，
// 都会想要它；让每个插件自己拿 Text 拼一个 ●，很快就会出现五种不同的写法。
//
// on 为真表示这一项是当前选中的。
func (b *Block) Chip(y, x int, label, action, arg string, on bool) int {
	if b == nil || y < 0 || y >= b.h {
		return x
	}
	// 选中时前面多一个 ● 前缀，所以选中与未选中的宽度不同 —— 这是有意的：
	// 选项之间因此不会挤在一起，而换行位置也只是差两列。
	text, st := "  "+label+"  ", Style(SDim)
	if on {
		text, st = "● "+label+" ", Style(SAccent+SBold)
	}
	w := DisplayWidth(text)
	if x < 0 || x+w > b.w {
		return x
	}
	b.At(y).Text(st, text)
	b.Hit(y, x, w, action, arg)
	return x + w + 2
}

// Lines 返回可以交给宿主的文本行：每行**恰好 w 列**（不足的补空格）。
func (b *Block) Lines() []string {
	out := make([]string, 0, b.h)
	for _, l := range b.lines {
		out = append(out, l.buf.String()+strings.Repeat(" ", max(0, l.width-l.col)))
	}
	return out
}

// Render 把画面打包成宿主能直接用的结果。
//
// scroll / maxScroll 由调用方给：宿主是按插件给的值显示滚动进度的，
// 插件自己夹好再报上去，两边才不会各说各话。
func (b *Block) Render(scroll, maxScroll int, note string) PanelRender {
	if maxScroll < 0 {
		maxScroll = 0
	}
	return PanelRender{
		Lines:     b.Lines(),
		Hits:      b.hits,
		Scroll:    scroll,
		MaxScroll: maxScroll,
		Note:      note,
	}
}

// ---------------------------------------------------------------------------
// 文本框
//
// "能改点什么"的界面几乎都要它。光标、窗口滚动、点击定位全都在这儿，
// 插件只管把 Draw 出来的东西放到该放的行上。
// ---------------------------------------------------------------------------

// Field 是一个单行文本框。
//
// **值是原样的**，含换行也不改：单行框里塞不进多行，但值不能因此被压坏 ——
// "记忆"那种一行一条的内容，压平了再存回去就毁了。显示时控制字符会换成空格，
// 光标与点击的换算也按同一份换算走，所以列数永远对得上。
type Field struct {
	Value  string
	Cursor int // rune 下标；-1 表示"在末尾"

	// Tail 表示**未聚焦时显示末尾**而不是开头。
	//
	// 给"只会往后长"的内容用：记忆是一条条追加的，显示开头的话，刚记住的
	// 那条永远在框外，看起来就像没记上。
	Tail bool

	// 选择（rune 下标，前闭后开 [selA, selB)）。selOn 为 false 时 selA/selB 无效。
	//
	// 拖选由三下组成：SelStart 把锚点落在一处、SelExtend 从锚点往外扩、
	// 复制用 Selection 取出。这三下都在插件侧发生，宿主只负责把"拖到哪一列"
	// 传过来（见 panel 协议的 sel 动作）。
	selOn     bool
	selA      int
	selB      int
	selAnchor int

	// 最近一次 Draw 用过的窗口起点与宽度，供 CaretAtCol 换算点击位置。
	lastStart int
	lastW     int
}

// Runes 返回值里原本的 rune 切片（含换行）。
func (f *Field) Runes() []rune { return []rune(f.Value) }

// cells 返回"画出来"的那份 rune：控制字符（换行、制表符…）换成空格。
//
// 一个字符换一个，绝不增删 —— 这样光标下标在原始值与显示之间是一一对应的，
// 点击定位与光标显示才不会随着内容里有没有换行而错位。
func (f *Field) cells() []rune {
	rs := f.Runes()
	for i, r := range rs {
		if runeWidthOf(r) == 0 {
			rs[i] = ' '
		}
	}
	return rs
}

// runeWidthOf 与 RuneWidth 同义，单独一个名字是为了让上面的意图更直白。
func runeWidthOf(r rune) int { return RuneWidth(r) }

// Cur 返回光标下标（夹在合法范围内；-1 视为末尾）。
func (f *Field) Cur() int {
	n := len(f.Runes())
	if f.Cursor < 0 || f.Cursor > n {
		return n
	}
	return f.Cursor
}

// SetCursor 把光标夹到合法范围，并清掉选择 —— 光标一挪，旧选区作废。
func (f *Field) SetCursor(i int) {
	n := len(f.Runes())
	if i < 0 {
		i = 0
	}
	if i > n {
		i = n
	}
	f.Cursor = i
	f.selOn = false
}

// SetValue 整体替换内容，光标移到末尾。
//
// 刻意不做 OneLine：值里本来带换行（记忆那种一行一条）就原样留着，
// 压平了再存回去等于把用户记下来的结构毁了。显示那边会换成空格。
func (f *Field) SetValue(s string) {
	f.Value = s
	f.Cursor = -1
	f.selOn = false
}

// Insert 在光标处插入一个字符，光标随之前移。
//
// 控制字符（含换行 / 制表符）一律丢弃：文本框是单行的，塞进去只会
// 把一行劈成两行，把宿主的版面顶歪。
func (f *Field) Insert(r rune) {
	if r == '\n' || r == '\r' || r == '\t' || (r < 0x20 && r != 0) || r == 0x7f {
		return
	}
	rs := f.Runes()
	i := f.Cur()
	out := make([]rune, 0, len(rs)+1)
	out = append(out, rs[:i]...)
	out = append(out, r)
	out = append(out, rs[i:]...)
	f.Value = string(out)
	f.Cursor = i + 1
	f.selOn = false
}

// Backspace 删掉光标前一个字符。
func (f *Field) Backspace() {
	rs := f.Runes()
	i := f.Cur()
	if i <= 0 {
		return
	}
	f.Value = string(append(append([]rune{}, rs[:i-1]...), rs[i:]...))
	f.Cursor = i - 1
	f.selOn = false
}

// Delete 删掉光标处那个字符。
func (f *Field) Delete() {
	rs := f.Runes()
	i := f.Cur()
	if i >= len(rs) {
		return
	}
	f.Value = string(append(append([]rune{}, rs[:i]...), rs[i+1:]...))
	f.Cursor = i
	f.selOn = false
}

// Left / Right / Home / End 移动光标。
func (f *Field) Left()  { f.SetCursor(f.Cur() - 1) }
func (f *Field) Right() { f.SetCursor(f.Cur() + 1) }
func (f *Field) Home()  { f.Cursor = 0 }
func (f *Field) End()   { f.Cursor = -1 }

// Clear 清空内容。
func (f *Field) Clear() { f.Value, f.Cursor, f.selOn = "", -1, false }

// ---------------------------------------------------------------------------
// 选择
// ---------------------------------------------------------------------------

// HasSel 问"现在框里有没有一段选中文字"。
func (f *Field) HasSel() bool { return f.selOn && f.selA < f.selB }

// Selection 返回选中的文字；没有选择返回空串。
//
// 取的是**原始值**（Runes），不是显示那份：显示里控制字符被换成了空格，
// 复制出去的必须和存进去的一字不差。
func (f *Field) Selection() string {
	if !f.HasSel() {
		return ""
	}
	rs := f.Runes()
	if f.selA < 0 {
		f.selA = 0
	}
	if f.selB > len(rs) {
		f.selB = len(rs)
	}
	if f.selA >= f.selB {
		return ""
	}
	return string(rs[f.selA:f.selB])
}

// SelStart 开始一段拖选：锚点落在 i，旧选区清空。
//
// 用在"按住鼠标"那一下 —— 光标先落到点的那一列，锚点跟着落，选区是空的。
func (f *Field) SelStart(i int) {
	n := len(f.Runes())
	if i < 0 {
		i = 0
	}
	if i > n {
		i = n
	}
	f.selAnchor = i
	f.selOn = false
	f.selA, f.selB = i, i
}

// SelExtend 把选区从锚点扩到 i（可越过锚点，自动取 [min, max)）。
//
// 用在"按住拖动"那几下 —— 只动选区，不动光标。
func (f *Field) SelExtend(i int) {
	n := len(f.Runes())
	if i < 0 {
		i = 0
	}
	if i > n {
		i = n
	}
	f.selA, f.selB = f.selAnchor, i
	if f.selA > f.selB {
		f.selA, f.selB = f.selB, f.selA
	}
	f.selOn = f.selA != f.selB
}

// SelClear 清空选择（不碰内容与光标）。
func (f *Field) SelClear() { f.selOn = false }

// Draw 把内容画到这一行上，返回占用的列数。
//
// 聚焦时按光标位置滚动窗口，并把光标画成一个竖条；未聚焦时从开头显示。
// Draw 会记住这次用的窗口，之后 CaretAtCol 才能把"点在那一列"换回光标位置。
func (f *Field) Draw(l *Line, width int, focused bool, caret Style) int {
	if l == nil {
		return 0
	}
	if width < 1 {
		width = 1
	}
	if caret == "" {
		caret = Style(SAccent2 + SBold)
	}
	rs := f.cells()
	start := 0
	switch {
	case focused:
		start = cursorWindow(rs, f.Cur(), width)
	case f.Tail:
		start = tailWindow(rs, width)
	}
	f.lastStart, f.lastW = start, width

	used := 0
	if !focused && start > 0 {
		// 显示末尾时，开头得有个记号告诉人"前面还有"
		l.Text(SDim, "…")
		used = 1
	}
	cur := f.Cur()
	curCol := 0
	for i := start; i < cur; i++ {
		curCol += RuneWidth(rs[i])
	}
	sel := f.HasSel()
	selA, selB := f.selA, f.selB
	drawn := false
	for i := start; i < len(rs); i++ {
		rw := RuneWidth(rs[i])
		if used+rw > width {
			break
		}
		if focused && !drawn && used >= curCol {
			l.Text(caret, "▏")
			used++
			drawn = true
			if used >= width {
				break
			}
		}
		// 选区内的字换成选中底色：拖出来的那段一眼就能看出来
		st := Style("")
		if sel && i >= selA && i < selB {
			st = Style(SSel)
		}
		l.Text(st, string(rs[i]))
		used += rw
	}
	if focused && !drawn && used <= width {
		l.Text(caret, "▏")
		used++
	}
	return used
}

// CaretAtCol 把"点在内容区第 col 列"换算成光标下标。
//
// 用的是最近一次 Draw 的窗口，所以调用方不用自己记滚动位置 ——
// 画的时候按哪个窗口画的，点回来就按哪个窗口算。
func (f *Field) CaretAtCol(col int) int {
	rs := f.cells()
	acc := 0
	for i := f.lastStart; i < len(rs); i++ {
		if acc >= col {
			return i
		}
		acc += RuneWidth(rs[i])
	}
	return len(rs)
}

// tailWindow 返回"显示末尾"的窗口起点：末尾必须看得见，前面加个省略号。
func tailWindow(rs []rune, width int) int {
	if width <= 1 || len(rs) == 0 {
		return 0
	}
	w := 0
	i := len(rs)
	for i > 0 {
		rw := RuneWidth(rs[i-1])
		if w+rw > width-1 { // 留一列给开头的省略号
			break
		}
		w += rw
		i--
	}
	return i
}

// cursorWindow 返回"让光标可见"的窗口起点：光标尽量贴左，但绝不滚出右缘。
func cursorWindow(rs []rune, cur, width int) int {
	if width <= 1 || cur <= 0 {
		return 0
	}
	acc := 0
	i := cur
	for i > 0 {
		rw := RuneWidth(rs[i-1])
		if acc+rw >= width {
			break
		}
		acc += rw
		i--
	}
	return i
}

// ---------------------------------------------------------------------------
// 列表窗口
// ---------------------------------------------------------------------------

// Window 按"选中项始终可见"的规则算出一个列表窗口。
//
// from 是窗口首行的下标，可见行数 rows；sel 是选中项。返回的 from 保证选中项
// 落在窗口内 —— 键盘上下移动时，这一条让列表自己跟着走，不用调用方自己算。
func Window(sel, from, rows, total int) int {
	if rows < 1 || total <= rows {
		return 0
	}
	if sel < from {
		from = sel
	}
	if sel >= from+rows {
		from = sel - rows + 1
	}
	if from > total-rows {
		from = total - rows
	}
	if from < 0 {
		from = 0
	}
	return from
}

// SortPanels 按 Order（小的在前）、再按 ID 给页面清单排序。
func SortPanels(list []PanelInfo) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Order != list[j].Order {
			return list[i].Order < list[j].Order
		}
		return list[i].ID < list[j].ID
	})
}
