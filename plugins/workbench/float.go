package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 用量浮窗
//
// 数据来自 usage-monitor 广播的 usage.snapshot，画面由工作台自己画。
//
// 以前这块小窗是 usage-monitor 自己往控制终端写的：它不知道工作台画了什么，
// 工作台也不知道它在哪儿 —— 于是工作台每重绘一帧就把它盖掉（只能靠插件自己
// 每秒补画一次，肉眼看得见闪），更谈不上拖动，因为鼠标在工作台手里。
//
// 现在反过来：插件只管广播数据，画面归终端的所有者画。代价是多一条事件，
// 换来的是不闪、能拖、能收起、位置能记住。插件收到 ui.overlay.claim 就交出终端
// （没有工作台时它照旧自己画，功能不丢）。
// ---------------------------------------------------------------------------

const (
	floatW     = 30 // 展开宽度
	floatFoldW = 26 // 收起后只剩一行
	floatH     = 7  // 展开高度（顶 + 5 行 + 底）
)

// actFloatGrab 是浮窗的点击动作：落在它上面就是"抓住浮窗"。
const actFloatGrab = "float:grab"

// usageSnapshot 是一次用量广播在界面侧的样子。
type usageSnapshot struct {
	BalanceOK  bool
	BalanceErr string
	Balance    string // 已带货币符号，例如 "¥12.34"
	Prompt     int
	Completion int
	HitRate    string // "62.3%"，没有样本时是 "—"
	Requests   int
	LastAt     string
}

// balanceText 把余额压成一行。查不到也要给个字，不能留空让人以为是 0。
func (u usageSnapshot) balanceText() string {
	switch {
	case u.BalanceErr != "":
		return "不可用"
	case !u.BalanceOK:
		return "查询中…"
	case u.Balance == "":
		return "—"
	default:
		return u.Balance
	}
}

// applyUsage 解析一次用量广播。调用方必须已经在界面线程上。
func (w *Workbench) applyUsage(a *app, payload json.RawMessage) {
	var ev struct {
		Balance struct {
			OK       bool   `json:"ok"`
			Total    string `json:"total"`
			Currency string `json:"currency"`
			Err      string `json:"err"`
		} `json:"balance"`
		Totals struct {
			PromptTokens     int    `json:"promptTokens"`
			CompletionTokens int    `json:"completionTokens"`
			Requests         int    `json:"requests"`
			LastAt           string `json:"lastAt"`
		} `json:"totals"`
		HitRate float64 `json:"hitRate"`
		// Overlay 是插件侧的浮窗开关。用指针是因为缺省等于"开着"：
		// 老版本、或手工构造的事件里没这个字段，不能当成"关掉"。
		Overlay *bool `json:"overlay"`
	}
	// 解析不出来就当没收到：浮窗不画，但要把原因说出来 ——
	// 静默失败的表现是"浮窗莫名其妙不见了"，最难查的那一种。
	if err := json.Unmarshal(payload, &ev); err != nil {
		// 没有 ctx 就无处写日志（单测里就是这么进来的），别为记一笔把界面搞崩
		if w.ctx != nil {
			w.ctx.Log("warn", "用量广播解析失败，浮窗不画："+err.Error(), nil)
		}
		return
	}

	// 用户在插件侧关了浮窗：界面也别画
	a.st.usageOK = ev.Overlay == nil || *ev.Overlay
	if !a.st.usageOK {
		return
	}

	snap := usageSnapshot{
		BalanceOK:  ev.Balance.OK,
		BalanceErr: ev.Balance.Err,
		Prompt:     ev.Totals.PromptTokens,
		Completion: ev.Totals.CompletionTokens,
		Requests:   ev.Totals.Requests,
		LastAt:     ev.Totals.LastAt,
	}
	if ev.Balance.Total != "" {
		snap.Balance = currencyMark(ev.Balance.Currency) + ev.Balance.Total
	}
	if ev.Totals.Requests > 0 || ev.Totals.PromptTokens > 0 {
		snap.HitRate = fmt.Sprintf("%.1f%%", ev.HitRate*100)
	} else {
		snap.HitRate = "—"
	}

	a.st.usage = snap
	a.st.usageOK = true
}

func currencyMark(cur string) string {
	switch cur {
	case "USD":
		return "$"
	case "CNY", "":
		return "¥"
	default:
		return cur + " "
	}
}

// ---------------------------------------------------------------------------
// 画
// ---------------------------------------------------------------------------

// drawFloat 把浮窗贴到画布上，并登记它的热区。
//
// 浮窗是盖在版面**之上**的，所以它的热区要排在队列最前面：
// 否则被它盖住的那个按钮会先被命中，点下去就不是拖浮窗了。
func drawFloat(c *canvas, st *state) {
	if !st.usageOK {
		return
	}
	// 先把行数补满到 h：frame() 补的那些空行如果晚于浮窗出现，浮窗就贴不上去了
	_ = c.frame()

	// 宽高取形态常量，而不是去量那几行字符串：
	// 行里混着 ANSI 样式码，dispWidth 会把 \033[38;5;109m 当成十几个显示列，
	// 量出来的宽度只在关掉颜色时才碰巧对。量错的话，画到的位置和热区就不是一个地方。
	lines := floatLines(st, c.color)
	fw, fh := floatSize(st)
	x, y := floatOrigin(st, c.w, c.h, fw, fh)

	c.overlay(x, y, fw, lines)

	boxes := make([]hitbox, 0, fh)
	for i := 0; i < fh; i++ {
		boxes = append(boxes, hitbox{x: x, y: y + i, w: fw, action: actFloatGrab})
	}
	c.boxes = append(boxes, c.boxes...)
}

// floatOrigin 算出浮窗左上角，并把它夹在屏幕内。
//
// 位置是浮窗自己的坐标，屏幕一变小就可能跑到界外 —— 夹一下，
// 免得缩窗口之后浮窗"消失"（其实是停在看不见的地方）。
func floatOrigin(st *state, w, h, fw, fh int) (int, int) {
	// <= 0 都算"还没拖过"：位置一旦拖过就是正数，而 0 只会来自没设置过
	x, y := st.floatX, st.floatY
	if x <= 0 || y <= 0 {
		// 还没拖过：贴右上角，避开第 0 行的标题栏
		x, y = w-fw-1, 1
	}
	return clampInt(x, 1, maxInt(1, w-fw-1)), clampInt(y, 1, maxInt(1, h-fh-1))
}

// floatSize 是当前形态的宽高。
func floatSize(st *state) (int, int) {
	if st.floatFold {
		return floatFoldW, 1
	}
	return floatW, floatH
}

func floatLines(st *state, color bool) []string {
	if st.floatFold {
		return []string{floatChip(st.usage, color)}
	}
	u := st.usage
	return []string{
		floatTop(color),
		floatRow("余额", u.balanceText(), color),
		floatRow("输入", humanTok(u.Prompt)+" tok", color),
		floatRow("输出", humanTok(u.Completion)+" tok", color),
		floatRow("缓存", u.HitRate, color),
		floatRow("请求", fmt.Sprintf("%d 次 · %s", u.Requests, orDash(u.LastAt)), color),
		floatBottom(color),
	}
}

func floatTop(color bool) string {
	l := newLine(color)
	head := "┌─ 用量 · 拖我 "
	fill := floatW - dispWidth(head) - 1
	if fill < 0 {
		fill = 0
	}
	l.styled(cBorder, head+strings.Repeat("─", fill)+"┐")
	return l.String()
}

func floatBottom(color bool) string {
	l := newLine(color)
	fill := floatW - 2
	if fill < 0 {
		fill = 0
	}
	l.styled(cBorder, "└"+strings.Repeat("─", fill)+"┘")
	return l.String()
}

// floatRow 画一行「│ 键            值 │」。
func floatRow(key, val string, color bool) string {
	inner := floatW - 2
	left := " " + key
	right := val + " "
	pad := inner - dispWidth(left) - dispWidth(right)
	if pad < 0 {
		avail := inner - dispWidth(left) - 1
		if avail < 1 {
			avail = 1
		}
		right = truncDisplay(val, avail) + " "
		pad = inner - dispWidth(left) - dispWidth(right)
		if pad < 0 {
			pad = 0
		}
	}
	l := newLine(color)
	l.styled(cBorder, "│")
	l.styled(cDim, left)
	l.styled("", strings.Repeat(" ", maxInt(0, pad)))
	l.styled(cAccent, right)
	l.styled(cBorder, "│")
	return l.String()
}

// floatChip 是收起后的那一小条：只留余额和命中率，挡不住什么。
func floatChip(u usageSnapshot, color bool) string {
	l := newLine(color)
	body := truncDisplay(" 用量 "+u.balanceText()+" · "+u.HitRate+" ", floatFoldW-2)
	l.styled(cBtnBG+cBold+cAccent2, " "+padDisplay(body, floatFoldW-2)+" ")
	return l.String()
}

func humanTok(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%.0fK", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ---------------------------------------------------------------------------
// 拖动 / 挪动
// ---------------------------------------------------------------------------

// grabFloat 按下时若落在浮窗上就抓住它。返回 true 表示这次按下归浮窗。
func (a *app) grabFloat(x, y int) bool {
	if !a.st.usageOK || a.canv == nil {
		return false
	}
	if a.canv.hit(x, y) != actFloatGrab {
		return false
	}
	fw, fh := floatSize(a.st)
	fx, fy := floatOrigin(a.st, a.st.w, a.st.h, fw, fh)
	a.st.floatDrag = &floatDrag{gx: x - fx, gy: y - fy}
	return true
}

// dragFloat 跟着鼠标走。
func (a *app) dragFloat(x, y int) {
	d := a.st.floatDrag
	if d == nil {
		return
	}
	fw, fh := floatSize(a.st)
	a.setFloat(x-d.gx, y-d.gy, fw, fh)
	d.moved = true
}

// dropFloat 松手：拖过就是挪位置，没拖动就是一次轻点 —— 收起 / 展开。
func (a *app) dropFloat() {
	d := a.st.floatDrag
	if d == nil {
		return
	}
	a.st.floatDrag = nil
	if !d.moved {
		a.st.floatFold = !a.st.floatFold
		a.st.notice = ""
	}
	a.saveFloat()
}

// setFloat 挪到新位置，并夹在屏幕内（别让它跑出去）。
func (a *app) setFloat(x, y, fw, fh int) {
	a.st.floatX = clampInt(x, 1, maxInt(1, a.st.w-fw-1))
	a.st.floatY = clampInt(y, 1, maxInt(1, a.st.h-fh-1))
}

// nudgeFloat 是 Alt+方向键：一格一格挪。手比鼠标稳的时候用它。
func (a *app) nudgeFloat(dx, dy int) {
	if !a.st.usageOK {
		return
	}
	fw, fh := floatSize(a.st)
	fx, fy := floatOrigin(a.st, a.st.w, a.st.h, fw, fh)
	a.setFloat(fx+dx, fy+dy, fw, fh)
	a.saveFloat()
}

// saveFloat 把位置落盘。回调由插件层注入：界面线程不该自己做 RPC。
func (a *app) saveFloat() {
	if a.st.floatSave != nil {
		a.st.floatSave(a.st.floatX, a.st.floatY, a.st.floatFold)
	}
}
