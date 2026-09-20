package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- 按列覆盖

// 拼接不能按下标切：行里混着 ANSI，砍断一个序列就是一屏乱码。
func TestSpliceColumnsPlain(t *testing.T) {
	line := "0123456789abcdefghij"
	if got := spliceColumns(line, 5, 6, "[x]"); got != "01234[x]bcdefghij" {
		t.Errorf("拼接不对：%q", got)
	}
	// 覆盖区整段换掉，不是叠上去
	if got := spliceColumns(line, 0, 20, "全部"); got != "全部" {
		t.Errorf("整行覆盖不对：%q", got)
	}
	// 行太窄也要贴得上去（补空格）
	if got := spliceColumns("ab", 6, 3, "XY"); got != "ab    XY" {
		t.Errorf("短行应补空格再贴：%q", got)
	}
}

// 覆盖时不能把样式弄丢：覆盖区右边的内容要接着用原来的颜色，
// 也不能留下半个转义序列。
func TestSpliceColumnsKeepsStyle(t *testing.T) {
	// 蓝字 hello，然后红字 world
	line := "\033[36mhello\033[0m\033[31mworld\033[0m"
	got := spliceColumns(line, 3, 4, "##")

	if !strings.Contains(got, "\033[31m") {
		t.Errorf("覆盖区之后的样式没接回来：%q", got)
	}
	// 可见部分：he + ## + 被盖掉的 lo 之后的 world
	plain := stripANSI(got)
	if plain != "hel##rld" {
		t.Errorf("可见内容不对：%q", plain)
	}
	assertNoDanglingESC(t, got)
}

// 宽汉字跨在覆盖区边界上：留它半格，浮窗的右边框就会被顶歪一列。
func TestSpliceColumnsSplitsWideRunes(t *testing.T) {
	line := "啊诶比西" // 每个汉字 2 列
	got := spliceColumns(line, 2, 4, "##")
	if dispWidth(got) != 6 { // 啊 + ## + 西
		t.Errorf("宽度不对：%d，期望 6", dispWidth(got))
	}
	plain := stripANSI(got)
	if plain != "啊##西" {
		t.Errorf("内容不对：%q", plain)
	}

	// 从汉字中间切开：跨界的那个整体让位，让掉的半格补成空格。
	// 不补的话，覆盖区右边的东西会整体左移一列 —— 界面从这儿开始歪。
	got = spliceColumns(line, 3, 4, "##")
	if dispWidth(got) != 5 { // 啊(2) + 补的空格(1) + ##(2)
		t.Errorf("跨界时宽度不对：%d，期望 5", dispWidth(got))
	}
	if !strings.HasPrefix(stripANSI(got), "啊 ##") {
		t.Errorf("缺口应补成空格，实际 %q", stripANSI(got))
	}
}

// stripANSI 去掉转义序列，只留看得见的字。
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = escSeqEnd(s, i)
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func assertNoDanglingESC(t *testing.T, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		j := escSeqEnd(s, i)
		if j <= i+1 || j > len(s) {
			t.Fatalf("第 %d 字节处有未闭合的转义序列：%q", i, s)
		}
		if !strings.HasSuffix(s[i:j], "m") && !strings.HasSuffix(s[i:j], "K") {
			t.Fatalf("出现非样式序列：%q", s[i:j])
		}
		i = j - 1
	}
}

// ---------------------------------------------------------------- 数据

func TestApplyUsage(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	w := &Workbench{}

	w.applyUsage(a, json.RawMessage(`{
	  "balance": {"ok": true, "total": "12.34", "currency": "CNY"},
	  "totals": {"promptTokens": 1200000, "completionTokens": 345000, "requests": 9, "lastAt": "21:44"},
	  "hitRate": 0.623,
	  "overlay": true
	}`))

	if !a.st.usageOK {
		t.Fatal("收到快照就该显示浮窗")
	}
	if a.st.usage.Balance != "¥12.34" {
		t.Errorf("余额应带货币符号，实际 %q", a.st.usage.Balance)
	}
	if a.st.usage.HitRate != "62.3%" {
		t.Errorf("命中率格式化不对：%q", a.st.usage.HitRate)
	}
	if got := humanTok(a.st.usage.Prompt); got != "1.2M" {
		t.Errorf("大数字应压成 1.2M，实际 %q", got)
	}

	// 用户在插件侧关掉了浮窗：界面也要跟着不画
	w.applyUsage(a, json.RawMessage(`{"overlay": false}`))
	if a.st.usageOK {
		t.Error("插件关掉浮窗后，界面不该再画")
	}

	// 老版本事件里没有 overlay 字段 —— 那不等于"关掉"
	w.applyUsage(a, json.RawMessage(`{"balance": {"ok": false}}`))
	if !a.st.usageOK {
		t.Error("缺 overlay 字段时默认应显示")
	}
	if got := a.st.usage.balanceText(); got != "查询中…" {
		t.Errorf("余额还没查到时不能显示成 0，实际 %q", got)
	}
}

// ---------------------------------------------------------------- 拖动

// 默认贴右上；拖到哪儿停在哪儿，松手落盘。
func TestFloatDrag(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.usageOK = true
	a.st.usage = usageSnapshot{BalanceOK: true, Balance: "¥12.34", HitRate: "62.3%", Requests: 7, LastAt: "21:44"}
	layout(a, 100, 30)

	saved := 0
	a.st.floatSave = func(int, int, bool) { saved++ }

	fx0, fy0 := floatOrigin(a.st, 100, 30, floatW, floatH)
	if fx0 != 100-floatW-1 || fy0 != 1 {
		t.Fatalf("默认应贴右上，实际 (%d,%d)", fx0, fy0)
	}

	x, y := findBox(t, a, actFloatGrab)
	if !a.grabFloat(x, y) {
		t.Fatal("按在浮窗上就该抓住它")
	}
	a.dragFloat(x-30, y+5)
	a.dropFloat()

	fx1, fy1 := floatOrigin(a.st, 100, 30, floatW, floatH)
	if fx1 != fx0-30 || fy1 != fy0+5 {
		t.Errorf("浮窗没跟着手走：从 (%d,%d) 到 (%d,%d)", fx0, fy0, fx1, fy1)
	}
	if saved != 1 {
		t.Errorf("松手应把位置存下来，实际存了 %d 次", saved)
	}
}

// 拖出屏幕也不能让它消失：夹在可见范围内。
func TestFloatDragClamped(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.usageOK = true
	layout(a, 100, 30)

	x, y := findBox(t, a, actFloatGrab)
	a.grabFloat(x, y)
	a.dragFloat(-80, -80)
	a.dropFloat()
	if fx, fy := floatOrigin(a.st, 100, 30, floatW, floatH); fx != 1 || fy != 1 {
		t.Errorf("左上角夹不住：(%d,%d)", fx, fy)
	}

	a.grabFloat(x, y)
	a.dragFloat(500, 500)
	a.dropFloat()
	if fx, fy := floatOrigin(a.st, 100, 30, floatW, floatH); fx != 100-floatW-1 || fy != 30-floatH-1 {
		t.Errorf("右下角夹不住：(%d,%d)", fx, fy)
	}
}

// 按下没动就松手 = 轻点：收起 / 展开。
func TestFloatTapFolds(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.usageOK = true
	layout(a, 100, 30)
	x, y := findBox(t, a, actFloatGrab)

	a.grabFloat(x, y)
	a.dropFloat()
	if !a.st.floatFold {
		t.Fatal("轻点应收起")
	}
	layout(a, 100, 30)
	if fw, _ := floatSize(a.st); fw != floatFoldW {
		t.Errorf("收起后应只剩窄窄一条，实际宽 %d", fw)
	}

	// 再点一下展开
	x, y = findBox(t, a, actFloatGrab)
	a.grabFloat(x, y)
	a.dropFloat()
	if a.st.floatFold {
		t.Error("再点一下应展开")
	}
}

// 没有鼠标的人：Alt+方向键一格一格挪。
func TestFloatNudge(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.usageOK = true
	layout(a, 100, 30)
	saved := 0
	a.st.floatSave = func(int, int, bool) { saved++ }

	fx0, _ := floatOrigin(a.st, 100, 30, floatW, floatH)
	a.nudgeFloat(-2, 0)
	if fx, _ := floatOrigin(a.st, 100, 30, floatW, floatH); fx != fx0-2 {
		t.Errorf("Alt+左 应往左挪：%d → %d", fx0, fx)
	}
	a.nudgeFloat(0, 1)
	if _, fy := floatOrigin(a.st, 100, 30, floatW, floatH); fy != 2 {
		t.Errorf("Alt+下 应往下挪，实际 y=%d", fy)
	}
	if saved != 2 {
		t.Errorf("每次挪动都该存一次，实际 %d", saved)
	}
}

// 浮窗没数据时不该出现，也不该抢走点击。
func TestFloatHiddenWithoutData(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	layout(a, 100, 30)
	if a.canv.hit(90, 3) == actFloatGrab {
		t.Error("没有用量数据时不该有浮窗")
	}
	if a.grabFloat(90, 3) {
		t.Error("没有浮窗时不该抓住任何东西")
	}
}

// ---------------------------------------------------------------- 画出來

// 浮窗是盖在版面之上的：贴完以后每一行宽度必须还是整屏宽，
// 差一列整个界面就会歪。
func TestFloatDrawnOnCanvas(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.usageOK = true
	a.st.usage = usageSnapshot{BalanceOK: true, Balance: "¥12.34", HitRate: "62.3%", Requests: 7, LastAt: "21:44"}

	for _, tc := range []struct{ w, h int }{{100, 30}, {80, 24}, {60, 20}} {
		layout(a, tc.w, tc.h)
		for i, row := range a.canv.rows {
			if got := dispWidth(row); got != tc.w {
				t.Errorf("%dx%d：第 %d 行宽 %d", tc.w, tc.h, i, got)
			}
		}
		out := a.canv.frame()
		for _, want := range []string{"用量", "余额", "¥12.34", "62.3%"} {
			if !strings.Contains(out, want) {
				t.Errorf("%dx%d：画面上看不到 %q", tc.w, tc.h, want)
			}
		}
	}
}

// 开颜色时浮窗必须照样画出来。
//
// 真终端一定带颜色，而样式码是混在行里的：宽度一量错，浮窗不是贴到别处、
// 就是整个不来（dispWidth 会把 \033[38;5;109m 当成十几个显示列）。
// 这条用例守的就是"真机上能看见它"。
func TestFloatDrawnWithColor(t *testing.T) {
	for _, tc := range []struct{ w, h int }{{120, 30}, {100, 30}, {60, 20}} {
		st := &state{w: tc.w, h: tc.h, floatX: -1, floatY: -1, usageOK: true}
		st.usage = usageSnapshot{BalanceOK: true, Balance: "¥12.34", HitRate: "62.3%", Requests: 7, LastAt: "21:44"}

		c := newCanvas(tc.w, tc.h, true)
		drawFloat(c, st)

		out := c.frame()
		for _, want := range []string{"用量", "余额", "¥12.34", "62.3%"} {
			if !strings.Contains(out, want) {
				t.Errorf("%dx%d 彩色：画面上看不到 %q", tc.w, tc.h, want)
			}
		}
		for i, row := range c.rows {
			if i >= tc.h {
				break
			}
			if got := dispWidth(stripANSI(row)); got != tc.w {
				t.Errorf("%dx%d 彩色：第 %d 行宽 %d（期望 %d）", tc.w, tc.h, i, got, tc.w)
			}
		}
	}
}

// 浮窗在最上层：它盖住的按钮不该抢走这一次点击。
func TestFloatCoversButtons(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.usageOK = true
	layout(a, 100, 30)

	x, y := findBox(t, a, actFloatGrab)
	if got := a.canv.hit(x, y); got != actFloatGrab {
		t.Errorf("浮窗盖住的位置应先命中浮窗，实际 %q", got)
	}
}
