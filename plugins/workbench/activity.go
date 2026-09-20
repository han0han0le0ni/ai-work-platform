package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 本文件是「AI 在干什么」那一块：把 agent 抛出来的事件攒成一个过程，
// 再画成消息区里的一个框。
//
// 为什么要它：模型一轮思考动辄几十秒，中间还夹着好几次工具调用。
// 原来的界面只画一个转圈的占位框，AI 想了什么、调了什么、卡在哪一步
// 全都看不见 —— 用户能得到的唯一反馈是"它在转圈"。
//
// 数据来源是事件总线（agent.think / chat.tool.before / chat.tool.after /
// agent.done）。界面不认识 agent，也不调用它：编排插件换一个，这里照样有画面。
//
// 事件由 RPC 线程进来，一律经 app.post 投递到界面线程再改状态 ——
// 界面状态只有一个 goroutine 能碰（见 tui.go 的 app 注释）。

// ---------------------------------------------------------------------------
// 过程的数据形状
// ---------------------------------------------------------------------------

// traceStep 是一次工具调用。
type traceStep struct {
	tool  string
	args  string // 参数摘要（不是原始 JSON：那是给模型看的，不是给人看的）
	ms    int64
	ok    bool
	done  bool
	chars int
	err   string
}

// traceView 是这一次生成的过程。
type traceView struct {
	started time.Time
	model   string
	round   int    // 第几轮（从 1 开始；0 表示还没收到任何事件）
	phase   string // 此刻在做什么：调用模型 / 准备调用 N 个工具 / 完成…
	think   string // 最近一次思考内容（模型给的 reasoning）
	said    string // 模型这一轮说的话（通常是"我打算先看什么"）
	steps   []traceStep
	done    bool
	err     string
}

// elapsed 是到现在为止的用时。渲染时才算，所以每帧都是新的。
func (t *traceView) elapsed() string {
	d := time.Since(t.started)
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// headline 是过程框的第一行：一句话说清此刻在做什么。
func (t *traceView) headline() string {
	round := ""
	if t.round > 0 {
		round = fmt.Sprintf("第 %d 轮 · ", t.round)
	}
	switch {
	case t.err != "":
		return round + "出错：" + t.err
	case t.done:
		n := 0
		for _, s := range t.steps {
			if s.done {
				n++
			}
		}
		if n == 0 {
			return round + "完成 · 没有调用工具 · 用时 " + t.elapsed()
		}
		return round + fmt.Sprintf("完成 · %d 次工具调用 · 用时 %s", n, t.elapsed())
	case t.phase == "":
		return round + "正在生成…"
	default:
		return round + t.phase + " · 已用 " + t.elapsed()
	}
}

// stepLine 把一次工具调用画成一行：左边是「标记 工具 · 参数」，右边是耗时。
func (s traceStep) stepLine(inner int) string {
	mark := "…"
	switch {
	case s.done && s.ok:
		mark = "✓"
	case s.done:
		mark = "✗"
	}
	left := mark + " " + s.tool
	if s.args != "" {
		left += " · " + s.args
	}
	right := "进行中"
	if s.done {
		if s.ok {
			right = fmt.Sprintf("%dms", s.ms)
		} else {
			right = "失败"
		}
	}
	rs := " " + right + " "
	avail := inner - dispWidth(rs)
	if avail < 6 {
		avail = 6
	}
	return truncDisplay(left, avail) + strings.Repeat(" ", maxInt(0, avail-dispWidth(left))) + rs
}

func (s traceStep) style() string {
	switch {
	case !s.done:
		return cDim
	case s.ok:
		return cOK
	default:
		return cBad
	}
}

// ---------------------------------------------------------------------------
// 事件 -> 状态
// ---------------------------------------------------------------------------

// agentEvents 是界面为了「看见过程」而订阅的事件。
//
// 全是 agent 插件自己 emit 的既有事件（agent.think 是这次新增的），
// 界面只订阅、不调用谁 —— 换掉 agent 也不会让这里崩。
var agentEvents = []string{"agent.think", "agent.step", "chat.tool.before", "chat.tool.after", "agent.done"}

// applyTrace 把一条事件并进当前过程。调用方必须已经在界面线程上。
func (w *Workbench) applyTrace(a *app, name string, payload json.RawMessage) {
	st := a.st
	switch name {
	case "agent.think":
		var ev struct {
			Round     int    `json:"round"`
			Phase     string `json:"phase"`
			Thinking  string `json:"thinking"`
			Text      string `json:"text"`
			ToolCalls int    `json:"toolCalls"`
			Model     string `json:"model"`
			Error     string `json:"error"`
		}
		_ = json.Unmarshal(payload, &ev)
		t := st.ensureTrace()
		t.round = ev.Round + 1 // 事件里从 0 数，界面上从 1 数
		if ev.Model != "" {
			t.model = ev.Model
		}
		switch ev.Phase {
		case "start":
			t.phase = "正在思考"
			t.done = false
			t.err = ""
		case "done":
			switch {
			case ev.ToolCalls > 0:
				t.phase = fmt.Sprintf("准备调用 %d 个工具", ev.ToolCalls)
			case strings.TrimSpace(ev.Text) != "":
				t.phase = "正在整理回答"
			default:
				t.phase = "正在整理回答"
			}
			if ev.Thinking != "" {
				t.think = ev.Thinking
			}
			if ev.Text != "" {
				t.said = ev.Text
			}
		case "error":
			t.phase = "模型调用失败"
			t.err = ev.Error
			t.done = true
		}

	case "chat.tool.before":
		var ev struct {
			Tool string          `json:"tool"`
			Args json.RawMessage `json:"args"`
		}
		_ = json.Unmarshal(payload, &ev)
		t := st.ensureTrace()
		t.steps = append(t.steps, traceStep{tool: ev.Tool, args: argsSummary(ev.Args, 40)})
		t.phase = "正在调用 " + ev.Tool
		t.done = false

	case "chat.tool.after":
		var ev struct {
			Tool  string `json:"tool"`
			OK    bool   `json:"ok"`
			Ms    int64  `json:"ms"`
			Chars int    `json:"chars"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &ev)
		if t := st.trace; t != nil {
			// 同名工具可能连续调多次：只认最后一个还没结束的那次
			for i := len(t.steps) - 1; i >= 0; i-- {
				if t.steps[i].tool == ev.Tool && !t.steps[i].done {
					t.steps[i].done = true
					t.steps[i].ok = ev.OK
					t.steps[i].ms = ev.Ms
					t.steps[i].chars = ev.Chars
					t.steps[i].err = ev.Error
					break
				}
			}
		}

	case "agent.done":
		var ev struct {
			Stopped string `json:"stopped"`
			Steps   int    `json:"steps"`
			Ms      int64  `json:"ms"`
		}
		_ = json.Unmarshal(payload, &ev)
		if t := st.trace; t != nil {
			t.done = true
			t.err = ""
			switch ev.Stopped {
			case "timeout":
				t.phase = "时间预算用尽，已停在这里"
			case "max_steps":
				t.phase = "达到工具调用上限，已停在这里"
			default:
				t.phase = "完成"
			}
		}
	}

	if st.trace != nil {
		// 状态栏也说一句：在别的页（插件 / 文件）也能看见它在忙什么
		st.status = "AI：" + st.trace.headline()
		st.statusOK = st.trace.err == ""
	}
}

// ensureTrace 拿到当前过程；没有就开一个（事件比「开始生成」先到时兜住）。
func (st *state) ensureTrace() *traceView {
	if st.trace == nil {
		st.trace = &traceView{started: time.Now()}
	}
	return st.trace
}

// resetTrace 开一次新过程。发消息（以及重新生成）时调用：
// 上一次的过程留着就没意义了，混在一起只会让人看错轮次。
func (st *state) resetTrace() {
	st.trace = &traceView{started: time.Now()}
}

// argsSummary 把工具参数压成一眼能读的「k=v · k=v」。
//
// 原始 JSON 是给模型看的：一个 read_file 的参数连带转义能有几百字符，
// 铺在界面上只会把正文挤没。取前两个键就够认出"它在读哪个文件"。
func argsSummary(raw json.RawMessage, max int) string {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil || len(obj) == 0 {
		s := strings.TrimSpace(strings.ReplaceAll(string(raw), "\n", " "))
		return truncDisplay(s, max)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 2 {
		keys = keys[:2]
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+valueText(obj[k], 30))
	}
	out := strings.Join(parts, " · ")
	if len(obj) > 2 {
		out += " …"
	}
	return truncDisplay(out, max)
}

// valueText 把参数值压成短文本；长字符串截断，其余走 JSON。
func valueText(v any, max int) string {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(strings.ReplaceAll(x, "\n", " "))
		return truncDisplay(s, max)
	case nil:
		return "null"
	case bool:
		return fmt.Sprintf("%v", x)
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", x), "0"), ".")
	default:
		blob, err := json.Marshal(x)
		if err != nil {
			return "…"
		}
		return truncDisplay(strings.ReplaceAll(string(blob), "\n", " "), max)
	}
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

const (
	// maxTraceSteps / maxThinkLines 限制过程框的高度。
	//
	// 消息区是贴底显示的：框太高就把对话本身顶出屏幕，本末倒置。
	maxTraceSteps  = 3
	maxThinkLines  = 2
	traceThinkClip = 240 // 思考内容最多显示多少个字符
)

// traceRows 把过程画成一个消息框。没有过程时返回 nil（聊天页据此不放占位）。
//
// 生成结束后过程框**不立刻消失**：留在那儿，用户还能回看"它刚才调了什么"。
// 下一次发消息时 resetTrace 会把它换掉。
func traceRows(st *state, bw int) []rowFn {
	t := st.trace
	if t == nil {
		return nil
	}
	inner := msgInner(bw)

	title := "AI 在做什么"
	tail := ""
	if t.model != "" {
		tail = " · " + t.model
	}
	rows := []rowFn{traceTopRow(title, tail, cAccent2, bw)}

	head := t.headline()
	if !t.done && st.busy {
		head = string(spinnerFrames[st.spinner%len(spinnerFrames)]) + " " + head
	}
	rows = append(rows, msgTextRow(head, bw, cAccent2))

	steps := t.steps
	if len(steps) > maxTraceSteps {
		steps = steps[len(steps)-maxTraceSteps:]
	}
	for _, s := range steps {
		rows = append(rows, msgTextRow(s.stepLine(inner), bw, s.style()))
	}

	// 换行压平：思考是分段的，直接 wrap 会在框里留下几行空白。
	think := strings.Join(strings.Fields(t.think), " ")
	if think != "" {
		if len([]rune(think)) > traceThinkClip {
			think = string([]rune(think)[:traceThinkClip]) + "…"
		}
		for i, ln := range wrapText("思考："+think, inner) {
			if i >= maxThinkLines {
				break
			}
			rows = append(rows, msgTextRow(ln, bw, cDim))
		}
	}
	return append(rows, msgBottomRow(bw))
}

// traceTopRow 画过程框的顶边：┌─ AI 在做什么 · 模型 ────┐
//
// 整行宽度必须精确等于 bw。固定开销是「 ┌─ 」和结尾的「 ─┐」共 6 列，
// 再留至少一列填充线 —— 窄屏上就得先砍标题、再砍尾巴，不能让右边框溢出。
func traceTopRow(title, tail, code string, bw int) rowFn {
	room := bw - 7
	if room < 1 {
		room = 1
	}
	tail = truncDisplay(tail, room)
	if dispWidth(title) > room {
		title, tail = truncDisplay(title, room), ""
	}
	if dispWidth(title)+dispWidth(tail) > room {
		tail = truncDisplay(tail, room-dispWidth(title))
	}
	fill := bw - 6 - dispWidth(title) - dispWidth(tail)
	if fill < 1 {
		fill = 1
	}
	return func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		l.styled(cBorder, "┌─ ")
		l.styled(code+cBold, title)
		l.styled(cDim, tail)
		l.styled(cBorder, " "+strings.Repeat("─", fill)+"┐")
	}
}
