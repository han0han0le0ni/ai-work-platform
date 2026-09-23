package plugin

import (
	"strings"
	"testing"
)

// plainWidth 按显示列数一行文本，跳过 ANSI 转义序列。
//
// 单测里必须有它：样式码本身也占字节，直接用 DisplayWidth 会把 \033[38;5;111m
// 当成十几个可见字符，于是"每行恰好 w 列"这条断言根本测不到真东西。
func plainWidth(s string) int {
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && s[j] < 0x40 {
					j++
				}
				if j < len(s) {
					j++
				}
			} else {
				j++
			}
			i = j
			continue
		}
		r := rune(s[i])
		if r < 0x80 {
			w += RuneWidth(r)
			i++
			continue
		}
		// 非 ASCII：按 UTF-8 解一个字符
		rr := []rune(s[i:])[0]
		w += RuneWidth(rr)
		i += len(string(rr))
	}
	return w
}

// 每一行都必须**恰好 w 列**：少一列宿主的右边框就左移，多一列就右移，
// 两种都是整屏歪。这是插件自带界面最要紧的一条不变量。
func TestBlockLinesAreExactlyWidth(t *testing.T) {
	for _, w := range []int{0, 1, 2, 7, 12, 33, 40} {
		b := NewBlock(w, 4)
		b.At(0).Text(SAccent+SBold, "中文与 emoji 🚀 混排")
		b.At(1).Text("", "短的")
		b.At(2).Fill(SDim, "")
		b.At(3).Text(SBad, strings.Repeat("很长的内容/", 12))

		for y, ln := range b.Lines() {
			if got := plainWidth(ln); got != w {
				t.Fatalf("w=%d 第 %d 行宽 %d，应为 %d：%q", w, y, got, w, ln)
			}
		}
	}
	// 越界行不该炸，也不该被画出来
	b := NewBlock(10, 2)
	if b.At(5) != nil {
		t.Fatal("越界行应返回 nil")
	}
	if len(b.Lines()) != 2 {
		t.Fatalf("行数应等于高度，实际 %d", len(b.Lines()))
	}
}

// Text 写超了要截断而不是撑破：宁可少几个字，也不能把人家整屏顶歪。
func TestLineTextTruncates(t *testing.T) {
	b := NewBlock(6, 1)
	b.At(0).Text("", "abcdefghij")
	if got := b.Lines()[0]; plainWidth(got) != 6 {
		t.Fatalf("应截断到 6 列，实际 %q 宽 %d", got, plainWidth(got))
	}
	if strings.Contains(b.Lines()[0], "g") {
		t.Fatalf("超出宽度的字符不该出现：%q", b.Lines()[0])
	}

	// 宽字符卡在边界上：宽度只剩 1 列时，一个汉字整个让位，不能只画一半
	b2 := NewBlock(3, 1)
	b2.At(0).Text("", "ab中")
	if got := plainWidth(b2.Lines()[0]); got != 3 {
		t.Fatalf("汉字让位后应补空格到 3 列，实际 %d", got)
	}
}

// 热区登记要不越界、要能带参数。
func TestBlockHitClamp(t *testing.T) {
	b := NewBlock(10, 2)
	b.Hit(0, 2, 20, "open", "a") // 右越界 → 收到 10
	b.Hit(1, -3, 5, "open", "b") // 左越界 → 收到 [0,2)
	b.Hit(9, 0, 3, "open", "")   // 行越界 → 丢掉
	b.Hit(0, 0, 3, "", "")       // 没有动作名 → 丢掉
	hits := b.Hits()
	if len(hits) != 2 {
		t.Fatalf("应登记 2 个热区，实际 %d：%+v", len(hits), hits)
	}
	if hits[0].X != 2 || hits[0].W != 8 || hits[0].Arg != "a" {
		t.Fatalf("右越界应收到右边界：%+v", hits[0])
	}
	if hits[1].X != 0 || hits[1].W != 2 || hits[1].Arg != "b" {
		t.Fatalf("左越界应从 0 开始：%+v", hits[1])
	}
}

// chip 是"多选一"：选中与未选中一眼要分清，而且要能点、要能并排排开。
//
// 单独立一个原语只为一件事：插件页里"平台 / 协议 / 模式"这类选项都会想要它，
// 各自拿 Text 拼一个 ● 的话，很快就会出现五种不同的写法。
func TestChipMarksSelection(t *testing.T) {
	b := NewBlock(40, 2)
	x := b.Chip(0, 1, "云端", "mode", "cloud", true)
	x = b.Chip(0, x, "本地", "mode", "local", false)

	lines := b.Lines()
	if !strings.Contains(lines[0], "● 云端") {
		t.Fatalf("选中的那一项要有 ● 标记：%q", lines[0])
	}
	if !strings.Contains(lines[0], "  本地  ") {
		t.Fatalf("没选中的那一项不该带标记：%q", lines[0])
	}
	// 每一项都要能点，而且带上自己的参数
	hits := b.Hits()
	if len(hits) != 2 {
		t.Fatalf("两个 chip 都该登记热区，实际 %d：%+v", len(hits), hits)
	}
	if hits[1].Action != "mode" || hits[1].Arg != "local" {
		t.Fatalf("第二个 chip 的参数不对：%+v", hits[1])
	}
	if hits[0].X+hits[0].W > hits[1].X {
		t.Fatalf("两个 chip 的热区不该重叠：%+v", hits)
	}
}

// 放不下时 chip 要认怂：宁可什么都不画，也不能画到框外
// ——画出去的热区还在、人却看不见，点上去就是"时灵时不灵"。
func TestChipRefusesOutOfBounds(t *testing.T) {
	b := NewBlock(6, 1)
	if got := b.Chip(0, 4, "很长的一项", "pick", "x", false); got != 4 {
		t.Fatalf("放不下时不该前进，实际 %d", got)
	}
	if len(b.Hits()) != 0 {
		t.Fatalf("放不下就不该登记热区：%+v", b.Hits())
	}
}

// 文本框的编辑：插入、退格、删除、移动，以及"光标不会被删到负数"。
func TestFieldEditing(t *testing.T) {
	var f Field
	for _, r := range "abc" {
		f.Insert(r)
	}
	if f.Value != "abc" || f.Cur() != 3 {
		t.Fatalf("插入后应为 abc/3，实际 %q/%d", f.Value, f.Cur())
	}
	f.Backspace()
	if f.Value != "ab" || f.Cur() != 2 {
		t.Fatalf("退格后应为 ab/2，实际 %q/%d", f.Value, f.Cur())
	}
	f.Home()
	if f.Cur() != 0 {
		t.Fatalf("Home 后光标应为 0，实际 %d", f.Cur())
	}
	f.Delete()
	if f.Value != "b" {
		t.Fatalf("删除光标处字符后应为 b，实际 %q", f.Value)
	}
	f.Left()
	f.Backspace() // 已经在开头，不该出事
	if f.Value != "b" || f.Cur() != 0 {
		t.Fatalf("开头退格应无副作用，实际 %q/%d", f.Value, f.Cur())
	}
	f.End()
	f.Insert('中')
	if f.Value != "b中" {
		t.Fatalf("末尾插入应为 b中，实际 %q", f.Value)
	}
}

// 控制字符进不了文本框：单行框里塞进换行/制表符会把版面劈开，
// 而粘贴是最容易带进这些字符的路径。
func TestFieldRejectsControlChars(t *testing.T) {
	var f Field
	f.SetValue("a")
	for _, r := range []rune{'\n', '\r', '\t', 0x07, 0x7f} {
		f.Insert(r)
	}
	if f.Value != "a" {
		t.Fatalf("控制字符都该被丢掉，实际 %q", f.Value)
	}
	// SetValue **不压平**：值里本来带换行（记忆那种一行一条）就原样留着 ——
	// 压平了再存回去等于把用户记下来的结构毁了。
	f.SetValue("sk-abc\ndef")
	if f.Value != "sk-abc\ndef" {
		t.Fatalf("SetValue 不该改内容，实际 %q", f.Value)
	}
}

// 多行内容要能画成一行，而且宽度算得准（换行换成空格，一一对应）。
func TestFieldDrawsMultilineAsOneLine(t *testing.T) {
	var f Field
	f.SetValue("- 爱喝美式\n- 住在城南")
	line := &Line{width: 40}
	used := f.Draw(line, 40, false, "")
	if used != pluginWidth("- 爱喝美式 - 住在城南") {
		t.Fatalf("画出来的宽度应等于把换行换成空格后的宽度，实际 %d", used)
	}
	// 光标换算与画出来的是同一份坐标：点第 0 列应落在下标 0
	if got := f.CaretAtCol(0); got != 0 {
		t.Fatalf("点第一列应落到下标 0，实际 %d", got)
	}
}

// Tail：未聚焦时显示末尾 —— 记忆这种"只会往后长"的内容，新记的必须看得见。
func TestFieldTailShowsTheEnd(t *testing.T) {
	var f Field
	f.Tail = true
	f.SetValue("aaa\nbbb\nccc")
	line := &Line{width: 8}
	f.Draw(line, 8, false, "")
	got := line.String()
	if !strings.Contains(got, "ccc") {
		t.Fatalf("末尾必须看得见，实际 %q", got)
	}
	if pluginWidth(stripEsc(got)) != 8 {
		t.Fatalf("补齐后应恰好 8 列，实际 %q", got)
	}

	// 聚焦时改看开头附近（跟光标走），不再显示末尾
	f.End()
	line = &Line{width: 8}
	f.Draw(line, 8, true, "")
	if strings.Contains(line.String(), "aaa") {
		t.Fatalf("聚焦时应跟光标走，实际 %q", line.String())
	}
}

func pluginWidth(s string) int { return DisplayWidth(s) }

// stripEsc 去掉样式码，用来量纯文本宽度。
func stripEsc(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 2
			for j < len(s) && s[j] < 0x40 {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// 点在第几列就要把光标放到第几列 —— 而且要用**画的时候那个窗口**算，
// 否则长内容滚过之后，点哪儿都是错位的。
func TestFieldCaretAtColRoundTrip(t *testing.T) {
	var f Field
	f.SetValue("0123456789")
	f.End()

	// 短内容：窗口从 0 开始
	line := &Line{width: 20}
	f.Draw(line, 20, true, "")
	for col := 0; col <= 10; col++ {
		if got := f.CaretAtCol(col); got != col {
			t.Fatalf("col=%d 应换算成下标 %d，实际 %d", col, col, got)
		}
	}

	// 长内容 + 光标在末尾：窗口滚过之后，第一列对应的是窗口起点而不是 0
	f.SetValue(strings.Repeat("x", 30))
	f.End()
	line = &Line{width: 10}
	f.Draw(line, 10, true, "")
	idx := f.CaretAtCol(0)
	if idx == 0 {
		t.Fatal("窗口滚动后第一列不该还是下标 0")
	}
	if idx >= 30 {
		t.Fatalf("窗口内还能点回中间位置，实际 %d", idx)
	}
	// 点最右一列应落在窗口末尾附近，不会跳到内容开头
	if right := f.CaretAtCol(9); right <= idx {
		t.Fatalf("越靠右下标应越大：左 %d 右 %d", idx, right)
	}
}

// 拖选三下：SelStart 落锚点、SelExtend 扩出去、Selection 取出选中段。
func TestFieldSelection(t *testing.T) {
	var f Field
	f.SetValue("hello world")

	f.SelStart(6)   // 按住落在第 6 个字符
	f.SelExtend(11) // 拖到末尾
	if got := f.Selection(); got != "world" {
		t.Fatalf("应选中 world，实际 %q", got)
	}
	if !f.HasSel() {
		t.Fatal("有选中段时 HasSel 应为真")
	}

	// 反向拖：越过锚点，自动取 [min,max)
	f.SelExtend(0)
	if got := f.Selection(); got != "hello " {
		t.Fatalf("反向拖应选中 hello+空格，实际 %q", got)
	}

	// 落到同一个点：没有选择
	f.SelStart(3)
	f.SelExtend(3)
	if f.HasSel() {
		t.Fatal("选区长度为 0 时不该算有选择")
	}
	if f.Selection() != "" {
		t.Fatalf("空选区应返回空串，实际 %q", f.Selection())
	}
}

// 一编辑（移动光标 / 打字 / 退格）旧选区就作废：选中的文字改完不能再复制旧的。
func TestFieldSelectionClearedOnEdit(t *testing.T) {
	var f Field
	f.SetValue("abcdef")
	f.SelStart(1)
	f.SelExtend(4)
	if f.Selection() != "bcd" {
		t.Fatalf("应选中 bcd，实际 %q", f.Selection())
	}

	f.SetCursor(2)
	if f.HasSel() {
		t.Fatal("移动光标应清掉选择")
	}

	f.SelStart(0)
	f.SelExtend(6)
	f.Insert('X')
	if f.HasSel() {
		t.Fatal("打字应清掉选择")
	}

	f.SelStart(0)
	f.SelExtend(4)
	f.Backspace()
	if f.HasSel() {
		t.Fatal("退格应清掉选择")
	}
}

// 选中的字换成底色，但**占的列数不能变**：不然框会宽一截、右边的竖线跟着歪。
func TestFieldSelectionKeepsWidth(t *testing.T) {
	var f Field
	f.SetValue("0123456789")
	f.SelStart(2)
	f.SelExtend(7)

	line := &Line{width: 12}
	used := f.Draw(line, 12, false, "") // 不聚焦：没有光标竖条干扰，宽度就是内容
	if used != 10 {
		t.Fatalf("画出来的宽度应仍是 10，实际 %d", used)
	}
	if got := DisplayWidth(stripEsc(line.String())); got != 10 {
		t.Fatalf("去掉样式码后宽度应仍是 10，实际 %d", got)
	}
	if !strings.Contains(line.String(), SSel) {
		t.Fatalf("选中段应带 SSel 底色，实际 %q", line.String())
	}
}

// 光标必须始终落在看得见的地方。
func TestFieldCursorStaysVisible(t *testing.T) {
	var f Field
	f.SetValue(strings.Repeat("字", 20)) // 每个汉字两列，共 40 列
	f.End()
	line := &Line{width: 12}
	f.Draw(line, 12, true, "")
	if f.lastStart == 0 {
		t.Fatal("光标在末尾时窗口应滚过去")
	}
	// 窗口内剩余宽度必须放得下光标
	if !f.visibleCursor(12) {
		t.Fatalf("窗口起点 %d 下光标不可见（宽 12）", f.lastStart)
	}
}

// visibleCursor 是测试用的断言：窗口内能不能看到光标。
func (f *Field) visibleCursor(width int) bool {
	acc := 0
	for i := f.lastStart; i < f.Cur(); i++ {
		acc += RuneWidth(f.Runes()[i])
	}
	return acc < width
}

// 列表窗口跟着选中项走，且不越出总数范围。
func TestWindowKeepsSelectionVisible(t *testing.T) {
	cases := []struct {
		sel, from, rows, total, want int
	}{
		{0, 0, 5, 20, 0},
		{9, 0, 5, 20, 5},
		{3, 5, 5, 20, 3},
		{19, 0, 5, 20, 15},
		{19, 15, 5, 20, 15},
		{1, 0, 5, 3, 0},  // 总数比窗口还少，直接从头看
		{0, 0, 0, 20, 0}, // 窗口为 0 时不动
	}
	for _, c := range cases {
		if got := Window(c.sel, c.from, c.rows, c.total); got != c.want {
			t.Fatalf("Window(%d,%d,%d,%d)=%d，应为 %d",
				c.sel, c.from, c.rows, c.total, got, c.want)
		}
	}
}

// 宽字符的宽度判断是两边（插件与 UI）共用的契约，抽几个典型值钉住。
func TestDisplayWidthSamples(t *testing.T) {
	cases := map[string]int{
		"abc":      3,
		"中文":       4,
		"a中":       3,
		"🚀":        2,
		"":         0,
		"a\u0301":  1, // 组合附加符号不占列
		"a\u200bb": 2, // 零宽空格不占列
	}
	for s, want := range cases {
		if got := DisplayWidth(s); got != want {
			t.Fatalf("%q 宽 %d，应为 %d", s, got, want)
		}
	}
	if Truncate("中文字", 5) != "中文…" {
		t.Fatalf("截断应为 中文…，实际 %q", Truncate("中文字", 5))
	}
	if Pad("中", 4) != "中  " {
		t.Fatalf("补齐应为 中  ，实际 %q", Pad("中", 4))
	}
}
