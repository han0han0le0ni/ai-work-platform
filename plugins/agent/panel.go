package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// 「预算」页：这一页住在 agent 插件自己身上
//
// 以前它长在界面插件的「设置」页里（一个"工具调用预算"小节），和模型接入挤在
// 同一屏。而"最多让模型调几轮工具、超时多久、单条工具结果喂回多少字符"全是
// agent 编排的参数 —— 界面替它画，就不得不认识 maxSteps / timeoutMs 这些字段名，
// 这是"数据 + 能改什么"和"怎么画"又揉在了一起。
//
// 现在这一页自己画自己：读配置、改配置都是 agent.v1 自己的事。
// ---------------------------------------------------------------------------

const (
	// panelService 是这一页的服务名：ui.page.<插件自己的名字>.v1。
	panelService = "ui.page.agent.v1"
	panelID      = "agent"

	// panelChangedEvent 是"我这一页变了，重新画一遍"。
	panelChangedEvent = "ui.panel.changed"
)

// agCtl 是环上的一个控件。
type agCtl struct {
	kind string // chip / field / btn
	arg  string
}

// agEntry 是内容里的一行。
type agEntry struct {
	draw func(b *plugin.Block, y int)
	ctls []int
}

// agentPanel 是这一页的全部界面状态。
type agentPanel struct {
	mu sync.Mutex

	loaded bool
	stale  atomic.Bool

	// steps / unlimited 是当前预算。Unlimited(-1) 表示不限制。
	steps      int
	unlimited  bool
	stepsDraft plugin.Field // 用户正在填的步数（限制步数时用）

	// 只读快照：这些也来自编排配置，但这一页先只展示、不逐个开放编辑。
	timeoutMs int
	toolLimit int
	toolCount int
	hasLLM    bool
	hasTool   bool

	note string
	ok   bool

	ring  []agCtl
	focus int
	rowOf map[int]int
}

func newAgentPanel() agentPanel {
	return agentPanel{steps: defaultMaxSteps, focus: -1}
}

func (p *agentPanel) list() []plugin.PanelInfo {
	return []plugin.PanelInfo{{ID: panelID, Title: "预算", Order: 30}}
}

func (a *Agent) handlePanel(method string, args json.RawMessage) (any, error) {
	switch method {
	case "list":
		return map[string]any{"panels": a.panel.list()}, nil
	case "render":
		var in plugin.PanelRequest
		_ = json.Unmarshal(args, &in)
		if in.Panel != "" && in.Panel != panelID {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "agent 没有页面 %s", in.Panel)
		}
		return a.panel.render(a, in), nil
	case "action":
		var in plugin.PanelRequest
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
		}
		return map[string]any{"note": a.panel.action(a, in)}, nil
	case "key":
		var in plugin.PanelRequest
		_ = json.Unmarshal(args, &in)
		copyText, exit := a.panel.key(a, in.Kind, in.Text)
		out := map[string]any{"ok": true}
		if copyText != "" {
			out["copy"] = copyText
		}
		if exit {
			out["exit"] = true
		}
		return out, nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%s 不支持方法 %s", panelService, method)
	}
}

func (p *agentPanel) changed(a *Agent) {
	if a == nil || a.ctx == nil {
		return
	}
	_ = a.ctx.Emit(panelChangedEvent, map[string]any{
		"service": panelService,
		"panel":   panelID,
	})
}

func (p *agentPanel) say(a *Agent, note string, ok bool) string {
	p.note, p.ok = note, ok
	p.changed(a)
	return note
}

// load 把编排配置读进来。
//
// 直接读配置而不是调 info()：info 会把 -1 翻成字符串"不限制"，而这一页要的是
// 原始值好回填输入框。
func (p *agentPanel) load(a *Agent) {
	p.steps = a.ctx.GetConfigInt("maxSteps", defaultMaxSteps)
	p.timeoutMs = a.ctx.GetConfigInt("timeoutMs", defaultTimeoutMs)
	p.toolLimit = a.ctx.GetConfigInt("maxToolResultChars", defaultToolLimit)
	p.unlimited = p.steps == Unlimited
	p.stepsDraft.Clear()
	if !p.unlimited {
		p.stepsDraft.SetValue(strconv.Itoa(p.steps))
	}
	_, names, _, _ := a.loadTools("")
	p.toolCount = len(names)
	p.hasLLM = consumeOK(a.ctx, llmService)
	p.hasTool = consumeOK(a.ctx, toolService)
	p.loaded = true
	p.stale.Store(false)
}

func (p *agentPanel) render(a *Agent, req plugin.PanelRequest) plugin.PanelRender {
	p.mu.Lock()
	defer p.mu.Unlock()

	w, h := req.W, req.H
	if w < 1 || h < 1 {
		return plugin.PanelRender{}
	}
	if !p.loaded || p.stale.Swap(false) {
		p.load(a)
	}
	b := plugin.NewBlock(w, h)
	if w < 40 || h < 6 {
		b.At(0).Text(plugin.SWarn, " 窗口太小，放不下这一页")
		return b.Render(0, 0, "把窗口拉大些")
	}

	p.ring, p.rowOf = nil, map[int]int{}
	const top = 2
	p.drawHeader(b, w)

	ch := h - top
	doc := p.body(w)

	maxScroll := maxInt(0, len(doc)-ch)
	scroll := clampInt(req.Scroll, 0, maxScroll)
	if row, ok := p.rowOf[p.focus]; ok {
		if row < scroll {
			scroll = row
		} else if row >= scroll+ch {
			scroll = row - ch + 1
		}
		scroll = clampInt(scroll, 0, maxScroll)
	}
	for i := 0; i < ch; i++ {
		if idx := scroll + i; idx < len(doc) {
			doc[idx].draw(b, top+i)
		}
	}

	res := b.Render(scroll, maxScroll, "↑↓ 选控件 · 回车按下去 · Esc 退出焦点 · Tab 切页")
	res.WantKeys = true
	res.WantCopy = true // 有步数输入框：Ctrl+C 交给这一页复制
	return res
}

func (p *agentPanel) drawHeader(b *plugin.Block, w int) {
	b.At(0).Text(plugin.SAccent+plugin.SBold, " 预算")
	cur := "不限制"
	if !p.unlimited {
		cur = fmt.Sprintf("最多 %d 轮工具调用", p.steps)
	}
	b.At(0).Text(plugin.SDim, "  "+plugin.Truncate(cur, maxInt(0, w-6)))
	if p.note != "" {
		st := plugin.Style(plugin.SDim)
		if !p.ok {
			st = plugin.Style(plugin.SBad)
		}
		b.At(1).Text(st, " "+plugin.Truncate(plugin.OneLine(p.note), maxInt(0, w-2)))
	}
}

func (p *agentPanel) body(w int) []agEntry {
	var out []agEntry
	text := func(s, style string) {
		out = append(out, agEntry{draw: func(b *plugin.Block, y int) {
			b.At(y).Text(plugin.Style(style), plugin.Truncate(s, w))
		}})
	}
	note := func(s string) {
		for _, ln := range plugin.Wrap(s, maxInt(8, w-2)) {
			text(" "+ln, plugin.SDim)
		}
	}
	blank := func() { out = append(out, agEntry{draw: func(*plugin.Block, int) {}}) }
	ctl := func(kind, arg string) int {
		p.ring = append(p.ring, agCtl{kind: kind, arg: arg})
		return len(p.ring) - 1
	}

	text(" ① 工具调用预算", plugin.SDim+plugin.SBold)
	note("模型反复调工具是常态，但「最多几轮」该由你定。设成不限制，就等于把收手的判断完全交给模型自己。")

	// 限制步数 / 不限制
	ctls := []int{ctl("chip", "limited"), ctl("chip", "unlimited")}
	out = append(out, agEntry{ctls: ctls, draw: func(b *plugin.Block, y int) {
		x := b.Chip(y, 1, "限制步数", "chip", "limited", !p.unlimited)
		b.Chip(y, x, "不限制", "chip", "unlimited", p.unlimited)
	}})

	// 步数输入框（不限制时置灰）
	fCtl := ctl("field", "steps")
	out = append(out, agEntry{ctls: []int{fCtl}, draw: func(b *plugin.Block, y int) {
		focused := p.focus == fCtl
		st := plugin.Style("")
		if focused {
			st = plugin.Style(plugin.SSel)
		}
		b.At(y).Text(st, " "+plugin.Pad("步数", 6))
		inner := maxInt(8, w-22)
		b.At(y).Text(st, "[")
		if p.unlimited {
			b.At(y).Text(plugin.SDim, plugin.Pad("（不限制）", inner))
		} else {
			used := p.stepsDraft.Draw(b.At(y), inner, focused, "")
			b.At(y).Text(st, plugin.Pad("", maxInt(0, inner-used)))
			for col := 0; col < inner; col++ {
				b.Hit(y, 8+col, 1, "caret", "steps:"+strconv.Itoa(col))
			}
		}
		b.At(y).Text(st, "]")
		if !p.unlimited && !focused {
			b.At(y).Text(plugin.SDim, " 例：12")
		}
	}})
	blank()

	saveCtl := ctl("btn", "save")
	out = append(out, agEntry{ctls: []int{saveCtl}, draw: func(b *plugin.Block, y int) {
		b.Button(y, 1, "应用预算", "btn", "save", true)
		b.At(y).Text(plugin.SDim, " "+plugin.Truncate("保存后立即生效，重启也记得", maxInt(0, w-14)))
	}})
	blank()

	// 只读快照：这些也归编排管，先只摆出来让人看见
	text(" 当前编排状态", plugin.SDim+plugin.SBold)
	llm, tool := "未连接", "未连接"
	if p.hasLLM {
		llm = "已连接"
	}
	if p.hasTool {
		tool = "已连接"
	}
	text("  LLM "+llm+" · 工具执行器 "+tool+" · 可用工具 "+strconv.Itoa(p.toolCount)+" 个", "")
	text("  超时 "+fmtSeconds(p.timeoutMs)+" · 单条工具结果上限 "+strconv.Itoa(p.toolLimit)+" 字符", plugin.SDim)
	return out
}

// fmtSeconds 把毫秒说成"秒"；不是整秒的也如实写出来。
func fmtSeconds(ms int) string {
	if ms%1000 == 0 {
		return strconv.Itoa(ms/1000) + " 秒"
	}
	return strconv.Itoa(ms) + " 毫秒"
}

// ---------------------------------------------------------------------------
// 焦点环
// ---------------------------------------------------------------------------

func (p *agentPanel) moveFocus(delta int) {
	if len(p.ring) == 0 {
		p.focus = -1
		return
	}
	if p.focus < 0 {
		p.focus = 0
	} else {
		p.focus = clampInt(p.focus+delta, 0, len(p.ring)-1)
	}
}

func (p *agentPanel) ctlAt(kind, arg string) int {
	for i, c := range p.ring {
		if c.kind == kind && c.arg == arg {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// 按键
// ---------------------------------------------------------------------------

func (p *agentPanel) key(a *Agent, kind, text string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.loaded || p.stale.Swap(false) {
		p.load(a)
	}
	// Ctrl+C：只复制。没选中就返回空，退出是 Ctrl+Q（宿主管），不在这里。
	if kind == "ctrl+c" {
		return p.stepsDraft.Selection(), false
	}
	// 步数框聚焦时：打字、退格、左右移动，都只落在这个框里
	if f := p.ctlAt("field", "steps"); p.focus == f && !p.unlimited {
		switch kind {
		case "rune":
			for _, r := range text {
				if r >= '0' && r <= '9' {
					p.stepsDraft.Insert(r)
				}
			}
			p.say(a, "", true)
			return "", false
		case "backspace":
			p.stepsDraft.Backspace()
			p.say(a, "", true)
			return "", false
		case "delete":
			p.stepsDraft.Delete()
			p.say(a, "", true)
			return "", false
		case "left":
			p.stepsDraft.Left()
			return "", false
		case "right":
			p.stepsDraft.Right()
			return "", false
		case "home":
			p.stepsDraft.Home()
			return "", false
		case "end":
			p.stepsDraft.End()
			return "", false
		case "enter":
			p.moveFocus(1)
			p.say(a, "", true)
			return "", false
		case "esc":
			p.focus = -1
			p.say(a, "", true)
			return "", false
		case "up":
			p.moveFocus(-1)
			p.say(a, "", true)
			return "", false
		case "down":
			p.moveFocus(1)
			p.say(a, "", true)
			return "", false
		}
		return "", false
	}

	switch kind {
	case "up":
		p.moveFocus(-1)
		p.say(a, "", true)
	case "down":
		p.moveFocus(1)
		p.say(a, "", true)
	case "enter":
		p.activate(a)
	case "esc":
		p.focus = -1
		p.say(a, "", true)
	}
	return "", false
}

func (p *agentPanel) activate(a *Agent) {
	if p.focus < 0 || p.focus >= len(p.ring) {
		p.moveFocus(1)
		p.say(a, "", true)
		return
	}
	c := p.ring[p.focus]
	switch c.kind {
	case "field":
		return
	case "chip", "btn":
		p.apply(a, plugin.PanelRequest{Action: c.kind, Arg: c.arg})
	}
}

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------

func (p *agentPanel) action(a *Agent, req plugin.PanelRequest) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.apply(a, req)
}

func (p *agentPanel) apply(a *Agent, req plugin.PanelRequest) string {
	if !p.loaded || p.stale.Swap(false) {
		p.load(a)
	}
	switch req.Action {
	case "caret":
		_, colText, ok := strings.Cut(req.Arg, ":")
		if !ok {
			return ""
		}
		p.stepsDraft.SetCursor(p.stepsDraft.CaretAtCol(atoiSafe(colText)))
		p.stepsDraft.SelStart(p.stepsDraft.Cur()) // 按下：落点也是拖选锚点
		p.focus = p.ctlAt("field", "steps")
		return ""

	case "sel":
		// 按住拖动：宿主把拖到的列号转过来，选区从锚点扩过去
		_, colText, ok := strings.Cut(req.Arg, ":")
		if !ok {
			return ""
		}
		p.stepsDraft.SelExtend(p.stepsDraft.CaretAtCol(atoiSafe(colText)))
		p.focus = p.ctlAt("field", "steps")
		if p.stepsDraft.HasSel() {
			p.say(a, fmt.Sprintf("已选中 %d 个字符：Ctrl+C 复制，Esc 取消", len([]rune(p.stepsDraft.Selection()))), true)
		}
		return ""

	case "chip":
		switch req.Arg {
		case "limited":
			p.unlimited = false
			p.focus = p.ctlAt("chip", "limited")
			return p.say(a, "限制步数：填好步数后点「应用预算」", true)
		case "unlimited":
			p.unlimited = true
			p.focus = p.ctlAt("chip", "unlimited")
			return p.say(a, "不限制：由模型自己决定何时收手", true)
		}
		return ""

	case "btn":
		if req.Arg == "save" {
			return p.save(a)
		}
	}
	return ""
}

// save 应用预算。
//
// 只提交真正变过的字段；没变就不打扰（configure 对"没可更新字段"会报错）。
func (p *agentPanel) save(a *Agent) string {
	target := Unlimited
	if !p.unlimited {
		n, err := strconv.Atoi(strings.TrimSpace(p.stepsDraft.Value))
		if err != nil || n <= 0 {
			return p.say(a, "步数要填一个正整数；不想限制就选「不限制」", false)
		}
		target = n
	}
	if target == p.steps {
		return p.say(a, "没有可保存的改动", false)
	}
	blob, _ := json.Marshal(map[string]any{"maxSteps": target})
	if _, err := a.configure(json.RawMessage(blob)); err != nil {
		return p.say(a, "保存失败："+err.Error(), false)
	}
	p.load(a)
	if p.unlimited {
		return p.say(a, "已设为不限制（立即生效，重启也记得）", true)
	}
	return p.say(a, fmt.Sprintf("已设为最多 %d 轮工具调用（立即生效，重启也记得）", target), true)
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return -1
	}
	return n
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
