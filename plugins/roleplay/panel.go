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
// 角色页：这一页住在插件自己身上
//
// 以前它长在 UI 插件里（workbench 的「角色」页），于是"界面不认识角色扮演"这句话
// 是假的 —— 界面里有 tabRole、roleCard、roleItem、loadRole/save/toggle/delete 一整套。
//
// 现在的分工：
//
//	这个文件   把画面画成"文本行 + 可点区域"，自己处理点击与按键
//	UI 插件    只借一块 w×h 的地方、把点击和按键转过来
//
// 业务逻辑一行都不在这里：保存 / 启用 / 删卡 / 拼提示词仍然只有 Handle 后面那一份
// （见 main.go 与 card.go）。这里只负责把结果画出来 —— 于是"改界面"和"改角色卡
// 怎么拼成提示词"永远是两件事，谁也不会把谁带歪。
//
// 三个刻意的取舍：
//
//  1. **键盘只有一个"环"**：列表项、字段、按钮排成一圈，↑↓ 在上面走，回车落下去，
//     打字进当前字段。宿主把 Tab 拿走了（切页），所以这一页不能再靠 Tab 换控件 ——
//     与其把"换角色"和"换字段"挤在上下键上，不如让它们共用一条环。
//  2. **焦点永远看得见**：焦点跑到可视区外时，这一页自己把滚动位置挪过去 ——
//     宿主不知道这一页里有什么，替不了我们做这件事。
//  3. **记忆只在真的动过时才提交**：模型可能刚在对话里用 remember 追加了一条，
//     而框里还是旧内容；无条件提交会把那条刚记住的抹掉。
// ---------------------------------------------------------------------------

const (
	// panelService 是这一页的服务名：ui.page.<插件自己的名字>.v1。
	//
	// 不叫统一的 ui.page.v1：服务名在注册表里是一对一的，那样第二个想做界面的
	// 插件会被拒之门外。
	panelService = "ui.page.roleplay.v1"
	panelID      = "roleplay"

	// panelChangedEvent 是"我这一页变了，重新画一遍"。
	panelChangedEvent = "ui.panel.changed"

	// rpListWin 是列表区最多显示几个角色；再多就跟着选中项滚。
	rpListWin = 6
)

// rpFieldDef 描述编辑器里的一个字段。
type rpFieldDef struct {
	key     string
	label   string
	persona bool // 属于"我的身份"（全局，不属于某张卡）
}

// rpFieldDefs 的顺序 = 画出来的顺序 = 键盘环里的顺序。
var rpFieldDefs = []rpFieldDef{
	{key: "name", label: "角色名"},
	{key: "description", label: "人物设定"},
	{key: "personality", label: "性格"},
	{key: "scenario", label: "场景"},
	{key: "greeting", label: "开场白"},
	{key: "memory", label: "记忆"},
	{key: "personaName", label: "我的名字", persona: true},
	{key: "personaDesc", label: "我的身份", persona: true},
}

const (
	rpFieldName        = 0
	rpFieldDesc        = 1
	rpFieldPersonality = 2
	rpFieldScenario    = 3
	rpFieldGreeting    = 4
	rpFieldMemory      = 5
	rpFieldMeName      = 6
	rpFieldMeDesc      = 7
)

// rpBtnCount 是顶栏按钮的个数。
const rpBtnCount = 6

// rpPanel 是这一页的全部界面状态。
//
// 它整个住在插件进程里：UI 插件那边只有一个借来的矩形和一串转过来的事件。
type rpPanel struct {
	// mu 护住下面这些：render / action / key 可能同时到
	//（宿主的三条通道各自独立，插件这边得自己串起来）。
	mu sync.Mutex

	loaded   bool
	loadedID string      // 表单里现在装的是哪张卡（决定"换人就覆盖"）
	sel      int         // 选中的角色下标
	items    []Character // 列表快照
	active   string      // 启用中的角色 id

	fields []rpField
	focus  int // 环上的下标；-1 = 键盘不落在任何控件上

	note       string
	ok         bool
	confirmDel bool
	preview    bool
	previewTop int

	// dirty 记"哪个字段被用户动过"。
	//
	// 两个用处：刷新时别拿存储里的值盖掉正在输入的内容；保存时只提交动过的
	// 那几项 —— 记忆尤其要紧，模型可能刚在对话里用 remember 追加了一条，
	// 而框里还是旧内容，无条件提交会把那条刚记住的抹掉。
	dirty []bool

	agentReady bool
	toolReady  bool

	// stale 表示"存储被别处改过了，下一帧重新读一次"。
	//
	// 用原子量而不是锁：立它的人可能正握着面板的锁（界面自己的动作就走这条路），
	// 再去加锁就自锁了。立标记、渲染时读 —— 那一刻锁是我们自己的。
	stale atomic.Bool

	rowOf map[int]int // 环下标 -> 内容区行号（画的时候登记）
}

// invalidate 说"存储变了，下一帧重新读"。任何一次改动（界面点的、模型调的
// remember、别的插件调的）都要走它，否则页面会停在旧内容上。
func (p *rpPanel) invalidate(r *Roleplay) {
	p.stale.Store(true)
	p.changed(r)
}

// curID 返回当前编辑的那张卡的 id。
func (p *rpPanel) curID() string {
	if p.sel < 0 || p.sel >= len(p.items) {
		return ""
	}
	return p.items[p.sel].ID
}

// rpField 是一个字段的运行时状态。
type rpField struct {
	rpFieldDef
	f plugin.Field
}

// rpEntry 是内容区里的一行：怎么画 + 它属于环上的哪个控件（-1 = 不属于）。
type rpEntry struct {
	draw rpLine
	ctl  int
}

// rpLine 画内容区的一行。
type rpLine func(b *plugin.Block, y int)

// ---------------------------------------------------------------------------
// 与宿主对接
// ---------------------------------------------------------------------------

// list 回答"我有哪几页"。
func (p *rpPanel) list() []plugin.PanelInfo {
	return []plugin.PanelInfo{{ID: panelID, Title: "角色", Order: 30}}
}

// handlePanel 是 ui.page.roleplay.v1 的分发入口。
func (r *Roleplay) handlePanel(method string, args json.RawMessage) (any, error) {
	switch method {
	case "list":
		return map[string]any{"panels": r.panel.list()}, nil
	case "render":
		var in plugin.PanelRequest
		_ = json.Unmarshal(args, &in)
		if in.Panel != "" && in.Panel != panelID {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "角色扮演没有页面 %s", in.Panel)
		}
		return r.panel.render(r, in), nil
	case "action":
		var in plugin.PanelRequest
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
		}
		return map[string]any{"note": r.panel.action(r, in)}, nil
	case "key":
		var in plugin.PanelRequest
		_ = json.Unmarshal(args, &in)
		copyText, exit := r.panel.key(r, in.Kind, in.Text)
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

// call 调本插件已有的方法。
//
// 界面就在插件里，没必要绕一圈 JSON-RPC 再回来；而业务逻辑（推系统提示词、
// 落盘、发事件）只有一份，就在 Handle 后面 —— 界面不重新实现任何一条。
func (p *rpPanel) call(r *Roleplay, method string, args map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	out, err := r.Handle(serviceName, method, raw)
	if err != nil {
		return nil, err
	}
	m, _ := out.(map[string]any)
	return m, nil
}

// changed 通知宿主"这一页变了，重新画一遍"。
//
// 少了它，界面会一直停在旧画面上 —— 那看起来就是"点了没反应"。
func (p *rpPanel) changed(r *Roleplay) {
	if r == nil || r.api == nil {
		return
	}
	_ = r.api.Emit(panelChangedEvent, map[string]any{
		"service": panelService,
		"panel":   panelID,
	})
}

// say 记一句反馈并通知宿主重画。
func (p *rpPanel) say(r *Roleplay, note string, ok bool) string {
	p.note, p.ok = note, ok
	p.changed(r)
	return note
}

// ---------------------------------------------------------------------------
// 读写
// ---------------------------------------------------------------------------

// load 读一次存储，刷新列表与表单。
//
// 直接读 store 而不是走 state/get 两个方法：这一页就在插件进程里，两次转发
// （各自再读一次配置）没有任何意义，只是把刷新拖慢一倍。
func (p *rpPanel) load(r *Roleplay) {
	st := r.load()
	p.items = st.Characters
	p.active = st.ActiveID
	p.agentReady = consumeOK(r.api, agentService)
	p.toolReady = consumeOK(r.api, toolService)
	if len(p.fields) == 0 {
		p.fields = make([]rpField, 0, len(rpFieldDefs))
		for _, def := range rpFieldDefs {
			f := plugin.Field{Cursor: -1}
			// 记忆是"只会往后长"的：不聚焦时显示**末尾**，
			// 否则刚记住的那条永远在框外，看起来就像没记上。
			f.Tail = def.key == "memory"
			p.fields = append(p.fields, rpField{def, f})
		}
		p.dirty = make([]bool, len(p.fields))
	}
	if len(p.items) == 0 {
		p.sel, p.loadedID = 0, ""
		p.fillFields(st, Character{}, true)
		p.loaded = true
		p.focus = clampInt(p.focus, -1, p.ringLen()-1)
		return
	}
	p.sel = clampInt(p.sel, 0, len(p.items)-1)
	card := p.items[p.sel]

	// 换了一张卡就**无条件覆盖**表单：留着上一张的内容，用户会照着错的设定改，
	// 保存下去就把另一张卡写坏了。同一个人则只刷新"没被改过"的字段 ——
	// 记忆尤其要紧：模型刚 remember 了一条，框里必须跟着长出来。
	force := p.loadedID != card.ID
	p.fillFields(st, card, force)
	if force {
		p.loadedID = card.ID
	}
	p.loaded = true
	p.confirmDel = false
	p.focus = clampInt(p.focus, -1, p.ringLen()-1)
	// 刚读回来的就是最新的：清掉"存储被别处改过"的标记，免得下一帧又白读一次
	p.stale.Store(false)
}

// fillFields 用存储里的值刷新表单：force 为真时全部覆盖，否则只碰没被改过的。
func (p *rpPanel) fillFields(st *store, card Character, force bool) {
	vals := []string{
		card.Name, card.Description, card.Personality, card.Scenario,
		card.Greeting, card.Memory, st.Persona.Name, st.Persona.Desc,
	}
	for i := range p.fields {
		if !force && p.dirty[i] {
			continue // 用户正在改这个框，别拿存储里的覆盖他
		}
		p.fields[i].f.SetValue(vals[i])
		p.dirty[i] = false
	}
	if force {
		for i := range p.dirty {
			p.dirty[i] = false
		}
	}
}

// markDirty 记下"这个字段被用户改过了"。
func (p *rpPanel) markDirty(i int) {
	if i >= 0 && i < len(p.dirty) {
		p.dirty[i] = true
	}
}

// focusedIndex 返回当前聚焦字段的下标（不在字段上时 -1）。
func (p *rpPanel) focusedIndex() int {
	for i := range p.fields {
		if p.ctlIsField(i) {
			return i
		}
	}
	return -1
}

// cardValues 把表单里的内容收成一张卡要提交的那几项。
func (p *rpPanel) cardValues() map[string]any {
	return map[string]any{
		"name":        strings.TrimSpace(p.fields[rpFieldName].f.Value),
		"description": strings.TrimSpace(p.fields[rpFieldDesc].f.Value),
		"personality": strings.TrimSpace(p.fields[rpFieldPersonality].f.Value),
		"scenario":    strings.TrimSpace(p.fields[rpFieldScenario].f.Value),
		"greeting":    strings.TrimSpace(p.fields[rpFieldGreeting].f.Value),
	}
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

// render 画这一页。w×h 是宿主给的地方，由它说了算。
//
// 这里**不做慢操作**：数据在 load 里读好了。渲染要按帧回答，顺手读一次盘
// 等于把宿主的每一帧都拖住。
func (p *rpPanel) render(r *Roleplay, req plugin.PanelRequest) plugin.PanelRender {
	p.mu.Lock()
	defer p.mu.Unlock()

	w, h := req.W, req.H
	if w < 1 || h < 1 {
		return plugin.PanelRender{}
	}
	if !p.loaded || p.stale.Swap(false) {
		p.load(r)
	}
	b := plugin.NewBlock(w, h)
	if w < 44 || h < 8 {
		b.At(0).Text(plugin.SWarn, " 窗口太小，放不下这一页")
		return b.Render(0, 0, "把窗口拉大些")
	}
	if p.preview {
		return p.renderPreview(r, b, w, h, req.Scroll)
	}
	p.drawHeader(b, w)

	// 钉住的部分：顶栏两行 + 两条按钮 + 一条分隔。这一页最先要看的两件事
	//（现在是谁、能按什么）不该跟着内容滚走。
	const pinned = 5
	ch := h - pinned
	entries := p.contentEntries(r, w)
	p.rowOf = map[int]int{}
	for i, e := range entries {
		if e.ctl >= 0 {
			p.rowOf[e.ctl] = i
		}
	}

	maxScroll := maxInt(0, len(entries)-ch)
	scroll := clampInt(req.Scroll, 0, maxScroll)
	// 焦点必须看得见：只有这一页自己知道环上的第 n 个控件画在第几行
	if row, ok := p.rowOf[p.focus]; ok {
		if row < scroll {
			scroll = row
		} else if row >= scroll+ch {
			scroll = row - ch + 1
		}
		scroll = clampInt(scroll, 0, maxScroll)
	}
	for i := 0; i < ch; i++ {
		if idx := scroll + i; idx < len(entries) {
			entries[idx].draw(b, pinned+i)
		}
	}

	res := b.Render(scroll, maxScroll, rpHint(w))
	res.WantKeys = true
	res.WantCopy = true // 有角色卡输入框：Ctrl+C 交给这一页复制
	return res
}

// drawHeader 画顶栏：现在是谁 + 两行按钮。
func (p *rpPanel) drawHeader(b *plugin.Block, w int) {
	head := " 角色 " + strconv.Itoa(len(p.items))
	if name := p.activeName(); name != "" {
		head += " · 启用中：" + name
	}
	b.At(0).Text(plugin.SAccent+plugin.SBold, plugin.Truncate(head, w-2))
	b.At(1).Fill(plugin.SBorder, "─")

	btns := p.buttons()
	row := 2
	x := 1
	for i, btn := range btns {
		if i == 4 { // 前四个管这张卡，后两个是"拿它去用"
			row, x = 3, 1
		}
		x = p.drawButton(b, row, x, w, i, btn)
	}
	b.At(4).Fill(plugin.SBorder, "─")
}

// activeName 返回启用中角色的名字。
func (p *rpPanel) activeName() string {
	for _, c := range p.items {
		if c.ID == p.active {
			return c.Name
		}
	}
	return ""
}

// rpButton 是一个按钮。
type rpButton struct {
	action string
	label  string
	chat   bool   // 走宿主的口子（@ui:chat）而不是回插件
	arg    string // 回给宿主的内容
}

// buttons 返回这一页的全部按钮：顺序 = 画出来的顺序 = 环里的顺序。
func (p *rpPanel) buttons() []rpButton {
	toggle := "启用"
	if p.active != "" && p.active == p.curID() {
		toggle = "停用"
	}
	del := "删除"
	if p.confirmDel {
		del = "确认删除"
	}
	return []rpButton{
		{action: "new", label: "新建"},
		{action: "save", label: "保存角色"},
		{action: "toggle", label: toggle},
		{action: "del", label: del},
		{action: "preview", label: "预览提示词"},
		{action: "greet", label: "用开场白起头", chat: true, arg: strings.TrimSpace(p.fields[rpFieldGreeting].f.Value)},
	}
}

// drawButton 画一个按钮，返回下一个可用列。
//
// 放不下就不画（也不登记热区）：画不出来的按钮，用户根本不知道它存在，
// 留一个点得到却看不见的热区只会更难查。
func (p *rpPanel) drawButton(b *plugin.Block, y, x, w, k int, btn rpButton) int {
	if x < 0 {
		return x
	}
	text := " " + btn.label + " "
	bw := plugin.DisplayWidth(text)
	if x+bw > w {
		return x
	}
	if btn.chat {
		// 让界面去干（把开场白放进聊天输入框）—— 插件不该知道聊天页长什么样
		b.At(y).Text(plugin.SBtn+plugin.SBold, text)
		b.Hit(y, x, bw, "@ui:chat", btn.arg)
		return x + bw + 2
	}
	if p.focus == p.ringButton(k) {
		b.At(y).Text(plugin.SAccent+plugin.SBold, text)
		b.Hit(y, x, bw, "btn", btn.action)
		return x + bw + 2
	}
	return b.Button(y, x, btn.label, "btn", btn.action, k < 4)
}

// contentEntries 建内容区：列表 → 角色卡字段 → 链路 → 我的身份 → 反馈。
//
// 建的同时把"环上第几个控件在第几行"交给调用方（见 render）—— 顺序就是契约，
// 所以列表项、字段、按钮在环里的排法必须与这里 append 的顺序一致。
func (p *rpPanel) contentEntries(r *Roleplay, w int) []rpEntry {
	var out []rpEntry
	add := func(draw rpLine, ctl int) { out = append(out, rpEntry{draw: draw, ctl: ctl}) }
	blank := func() { add(func(*plugin.Block, int) {}, -1) }

	// ① 列表：窗口跟着选中项走（不是跟着页面滚动），选中的那个永远看得见
	add(func(b *plugin.Block, y int) {
		b.At(y).Text(plugin.SDim+plugin.SBold, " 角色")
		b.At(y).Text(plugin.SDim, "  "+strconv.Itoa(len(p.items))+" 个 · 点名字换人")
	}, -1)
	if len(p.items) == 0 {
		add(func(b *plugin.Block, y int) {
			b.At(y).Text(plugin.SDim, " （还没有角色）点下面的「新建」起一个")
		}, p.ringList(0))
	}
	from := 0
	if len(p.items) > rpListWin {
		from = plugin.Window(p.sel, 0, rpListWin, len(p.items))
	}
	for i := from; i < len(p.items) && i < from+rpListWin; i++ {
		idx := i
		add(func(b *plugin.Block, y int) { p.drawListItem(b, y, w, idx) }, p.ringList(idx))
	}
	if rest := len(p.items) - (from + rpListWin); rest > 0 {
		add(func(b *plugin.Block, y int) {
			b.At(y).Text(plugin.SDim, fmt.Sprintf(" （下面还有 %d 个）", rest))
		}, -1)
	}
	blank()

	// ② 角色卡
	sub := "存下来即生效"
	if p.active != "" && p.active == p.curID() {
		sub = "启用中：改完保存，下一条消息就按新人设说"
	}
	if p.sel < len(p.items) && p.items[p.sel].Updated != "" {
		sub = "更新于 " + p.items[p.sel].Updated + " · " + sub
	}
	add(func(b *plugin.Block, y int) {
		b.At(y).Text(plugin.SAccent+plugin.SBold, " 角色卡")
		b.At(y).Text(plugin.SDim, plugin.Truncate("  "+sub, maxInt(0, w-9)))
	}, -1)
	for i := range p.fields {
		if p.fields[i].persona {
			continue
		}
		idx := i
		add(func(b *plugin.Block, y int) { p.drawField(b, y, w, idx) }, p.ringField(idx))
	}
	add(func(b *plugin.Block, y int) {
		b.At(y).Text(plugin.SDim, plugin.Truncate(" 共 "+
			strconv.Itoa(memoryCount(p.fields[rpFieldMemory].f.Value))+
			" 条 · 新记忆接在末尾；输入一个减号 = 清空", w-1))
	}, -1)
	blank()

	// ③ 链路：设定推不推得出去、模型能不能自己写记忆。
	// 断哪一条都会表现成"保存成功但没人照做"，所以摆在卡片下面说清楚。
	add(func(b *plugin.Block, y int) {
		b.At(y).Text(plugin.SDim+plugin.SBold, " 链路")
		if p.agentReady {
			b.At(y).Text(plugin.SOK, "  agent ✓ 设定推得出去")
		} else {
			b.At(y).Text(plugin.SWarn, "  agent ✗ 装 agent.v1 才推得出去")
		}
		if p.toolReady {
			b.At(y).Text(plugin.SOK, "  remember ✓")
		} else {
			b.At(y).Text(plugin.SWarn, "  remember ✗ 装 toolExecutor.v1 才能自动写记忆")
		}
	}, -1)
	blank()

	// ④ 我的身份（全局，对所有角色生效）
	add(func(b *plugin.Block, y int) {
		b.At(y).Text(plugin.SAccent+plugin.SBold, " 我的身份")
		b.At(y).Text(plugin.SDim, "  对你所有的角色生效")
	}, -1)
	for i := range p.fields {
		if !p.fields[i].persona {
			continue
		}
		idx := i
		add(func(b *plugin.Block, y int) { p.drawField(b, y, w, idx) }, p.ringField(idx))
	}
	add(func(b *plugin.Block, y int) { p.drawPersonaButton(b, y, w) }, p.ringPersona())
	blank()

	// ⑤ 反馈
	if p.note != "" {
		add(func(b *plugin.Block, y int) {
			st := plugin.Style(plugin.SDim)
			if !p.ok {
				st = plugin.Style(plugin.SBad)
			}
			b.At(y).Text(st, " "+plugin.Truncate(p.note, w-2))
		}, -1)
	}
	return out
}

// drawListItem 画一个角色条目：启用标记 + 名字 + 记忆条数与最后改动时间。
func (p *rpPanel) drawListItem(b *plugin.Block, y, w, idx int) {
	c := p.items[idx]
	style := plugin.Style(plugin.SBold)
	mark := "  "
	if c.ID == p.active {
		mark, style = "● ", plugin.Style(plugin.SOK+plugin.SBold)
	}
	if idx == p.sel {
		style = plugin.Style(plugin.SSel + plugin.SBold)
	}
	tail := ""
	if n := memoryCount(c.Memory); n > 0 {
		tail = strconv.Itoa(n) + " 条"
	}
	if c.Updated != "" {
		tail += " · 更新于 " + shortStamp(c.Updated)
	}
	nameW := w - 5 - plugin.DisplayWidth(tail) - 2
	if nameW < 6 {
		nameW = 6
	}
	b.At(y).Text(style, " "+mark)
	b.At(y).Text(style, plugin.Pad(plugin.Truncate(c.Name, nameW), nameW))
	b.At(y).Text(plugin.SDim, " "+plugin.Truncate(tail, maxInt(0, w-5)))
	b.Hit(y, 0, w, "list", strconv.Itoa(idx))
}

// drawField 画一个字段行：标签 + [ 值 ]；聚焦时点亮边框并把光标画出来。
func (p *rpPanel) drawField(b *plugin.Block, y, w, i int) {
	f := &p.fields[i]
	label := " " + plugin.Pad(f.label, 8)
	lw := plugin.DisplayWidth(label)
	inner := w - lw - 4
	if inner < 6 {
		inner = 6
	}
	focused := p.ctlIsField(i)

	b.At(y).Text(plugin.SDim, label)
	border := plugin.Style(plugin.SBorder)
	if focused {
		border = plugin.Style(plugin.SAccent + plugin.SBold)
	}
	b.At(y).Text(border, "[")
	used := f.f.Draw(b.At(y), inner, focused, plugin.Style(plugin.SAccent2+plugin.SBold))
	if used < inner {
		b.At(y).Text("", strings.Repeat(" ", inner-used))
	}
	b.At(y).Text(border, "]")

	// 整框可点（聚焦），逐列可点（把光标放到那一列）
	b.Hit(y, lw, inner+2, "field", strconv.Itoa(i))
	for col := 0; col < inner; col++ {
		b.Hit(y, lw+1+col, 1, "caret", strconv.Itoa(i)+":"+strconv.Itoa(col))
	}
}

// drawPersonaButton 画「保存身份」（它在内容区里，焦点落在它身上时要点亮）。
func (p *rpPanel) drawPersonaButton(b *plugin.Block, y, w int) {
	const label = "保存身份"
	text := " " + label + " "
	if 1+plugin.DisplayWidth(text) > w {
		return
	}
	if p.focus == p.ringPersona() {
		b.At(y).Text(plugin.SAccent+plugin.SBold, text)
		b.Hit(y, 1, plugin.DisplayWidth(text), "btn", "savepersona")
		return
	}
	b.Button(y, 1, label, "btn", "savepersona", true)
}

// renderPreview 显示"这张卡会被怎么描述给模型"。
//
// 标题钉上、按钮钉下，中间那段提示词自己滚 —— 整块一起滚的话，往下翻两屏
// 就再也看不到"这是预览"和怎么回去。
func (p *rpPanel) renderPreview(r *Roleplay, b *plugin.Block, w, h int, scroll int) plugin.PanelRender {
	b.At(0).Text(plugin.SAccent+plugin.SBold, " 系统提示词")
	b.At(0).Text(plugin.SDim, "  下面这段就是模型看到的全部设定")

	room := h - 2
	if room < 1 {
		room = 1
	}
	text := "（还没有角色可预览）"
	if id := p.curID(); id != "" {
		if out, err := p.call(r, "prompt", map[string]any{"id": id}); err == nil {
			if s, ok := out["prompt"].(string); ok && strings.TrimSpace(s) != "" {
				text = s
			}
		}
	}
	body := plugin.Wrap(text, maxInt(8, w-4))
	maxScroll := maxInt(0, len(body)-room)
	top := clampInt(scroll, 0, maxScroll)
	p.previewTop = top
	for i := 0; i < room && top+i < len(body); i++ {
		b.At(1+i).Text("", "  "+body[top+i])
	}
	tail := "滚轮翻阅 · Esc 返回"
	if maxScroll > 0 {
		shown := top + room
		if shown > len(body) {
			shown = len(body)
		}
		tail = fmt.Sprintf("滚轮翻阅（%d/%d）· Esc 返回", shown, len(body))
	}
	x := b.Button(h-1, 1, "返回编辑", "close-preview", "", true)
	b.At(h-1).Text(plugin.SDim, " "+plugin.Truncate(tail, maxInt(0, w-x)))
	res := b.Render(top, maxScroll, "这一页由角色扮演插件自己绘制 · Esc 或「返回编辑」回到表单")
	res.WantKeys = true
	return res
}

// rpHint 是底部那行用法说明（宿主照抄）。
func rpHint(w int) string {
	if w < 60 {
		return "点字段直接改 · ↑↓ 换控件 · Ctrl+Enter 保存 · Tab 切页"
	}
	return "点字段直接改 · ↑↓ 换控件 · 回车走下一个 · Ctrl+Enter 保存 · 点列表换角色 · 滚轮翻页"
}

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------

// action 处理一次点击。
func (p *rpPanel) action(r *Roleplay, req plugin.PanelRequest) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.loaded {
		p.load(r)
	}
	switch req.Action {
	case "close-preview":
		p.preview = false
		return p.say(r, "", true)

	case "list":
		if i := atoiSafe(req.Arg); i >= 0 && i < len(p.items) {
			p.sel = i
			p.load(r)
			p.setFocus(p.ringList(i))
			return p.say(r, "已切到「"+p.items[i].Name+"」", true)
		}
		return ""

	case "field":
		p.setFocus(p.ringField(atoiSafe(req.Arg)))
		return p.say(r, "", true)

	case "caret":
		i, col := splitPair(req.Arg)
		if i < 0 || i >= len(p.fields) {
			return ""
		}
		p.fields[i].f.SetCursor(p.fields[i].f.CaretAtCol(col))
		p.fields[i].f.SelStart(p.fields[i].f.Cur()) // 按下：落点也是拖选锚点
		p.setFocus(p.ringField(i))
		return p.say(r, "", true)

	case "sel":
		// 按住拖动：宿主把拖到的列号转过来，选区从锚点扩过去
		i, col := splitPair(req.Arg)
		if i < 0 || i >= len(p.fields) {
			return ""
		}
		p.fields[i].f.SelExtend(p.fields[i].f.CaretAtCol(col))
		p.setFocus(p.ringField(i))
		if p.fields[i].f.HasSel() {
			p.say(r, fmt.Sprintf("已选中 %d 个字符：Ctrl+C 复制，Esc 取消", len([]rune(p.fields[i].f.Selection()))), true)
		}
		return ""

	case "btn":
		return p.press(r, req.Arg)
	}
	return ""
}

// key 处理一次按键。
//
// 只处理这一页自己的事：Tab（切页）由宿主扣下，否则用户会被困在这一页里出不去。
// Ctrl+C 由宿主在页面声明 WantCopy 时转过来：只复制，退出是 Ctrl+Q。
func (p *rpPanel) key(r *Roleplay, kind, text string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.loaded {
		p.load(r)
	}
	if p.preview {
		if kind == "esc" {
			p.preview = false
			p.say(r, "", true)
		}
		return "", false
	}

	// Ctrl+C：只复制。没选中就返回空，退出是 Ctrl+Q（宿主管），不在这里。
	if kind == "ctrl+c" {
		return p.selection(), false
	}

	switch kind {
	case "rune":
		f := p.focusedField()
		if f == nil {
			return "", false
		}
		for _, ch := range text {
			f.f.Insert(ch)
		}
		p.markDirty(p.focusedIndex())
		p.say(r, "", true)

	case "backspace", "delete", "left", "right", "home", "end":
		f := p.focusedField()
		if f == nil {
			return "", false
		}
		switch kind {
		case "backspace":
			f.f.Backspace()
		case "delete":
			f.f.Delete()
		case "left":
			f.f.Left()
		case "right":
			f.f.Right()
		case "home":
			f.f.Home()
		case "end":
			f.f.End()
		}
		p.markDirty(p.focusedIndex())
		p.say(r, "", true)

	case "up":
		p.moveFocus(r, -1)
	case "down":
		p.moveFocus(r, 1)
	case "enter":
		p.enterRing(r)
	case "ctrl+enter":
		p.save(r)

	case "esc":
		if p.confirmDel {
			p.confirmDel = false
			p.say(r, "已取消删除", true)
			return "", false
		}
		p.focus = -1
		p.say(r, "", true)
	}
	return "", false
}

// selection 返回当前选中的文字：哪个字段里有选中段就取哪个。
func (p *rpPanel) selection() string {
	for i := range p.fields {
		if p.fields[i].f.HasSel() {
			return p.fields[i].f.Selection()
		}
	}
	return ""
}

// press 按下一个按钮。
func (p *rpPanel) press(r *Roleplay, action string) string {
	switch action {
	case "new":
		out, err := p.call(r, "save", map[string]any{})
		if err != nil {
			return p.say(r, "新建失败："+err.Error(), false)
		}
		id, _ := out["id"].(string)
		p.loadedID = "" // 强制把新卡灌进表单
		p.load(r)
		for i, c := range p.items {
			if c.ID == id {
				p.sel = i
			}
		}
		p.load(r)
		p.setFocus(p.ringField(rpFieldName))
		return p.say(r, noteOr(out, "已新建角色卡"), true)

	case "save":
		return p.save(r)

	case "toggle":
		id := p.curID()
		if id == "" {
			return p.say(r, "先选一个角色", false)
		}
		method, args := "deactivate", map[string]any{}
		if p.active != id {
			method, args = "activate", map[string]any{"id": id}
		}
		out, err := p.call(r, method, args)
		if err != nil {
			return p.say(r, "操作失败："+err.Error(), false)
		}
		p.load(r)
		return p.say(r, noteOr(out, "已切换启用状态"), true)

	case "del":
		id := p.curID()
		if id == "" {
			return p.say(r, "先选一个角色", false)
		}
		if !p.confirmDel {
			p.confirmDel = true
			p.changed(r)
			return p.say(r, "再点一次「确认删除」：角色卡连同它的记忆都会没", false)
		}
		p.confirmDel = false
		out, err := p.call(r, "delete", map[string]any{"id": id})
		if err != nil {
			return p.say(r, "删除失败："+err.Error(), false)
		}
		p.sel, p.loadedID = 0, ""
		p.load(r)
		return p.say(r, noteOr(out, "已删除"), true)

	case "preview":
		if p.curID() == "" {
			return p.say(r, "先选一个角色，再预览它的提示词", false)
		}
		p.preview, p.previewTop = true, 0
		p.changed(r)
		return p.say(r, "提示词预览：滚轮翻阅，Esc 返回编辑", true)

	case "savepersona":
		out, err := p.call(r, "persona", map[string]any{
			"name": strings.TrimSpace(p.fields[rpFieldMeName].f.Value),
			"desc": strings.TrimSpace(p.fields[rpFieldMeDesc].f.Value),
		})
		if err != nil {
			return p.say(r, "保存身份失败："+err.Error(), false)
		}
		return p.say(r, noteOr(out, "身份已保存"), true)
	}
	return ""
}

// save 保存当前角色。
//
// 记忆只在用户真的动过那个框时才提交：模型可能刚在对话里用 remember 追加了
// 一条，而框里还是旧内容 —— 无条件提交会把那条刚记住的抹掉，用户还毫不知情。
func (p *rpPanel) save(r *Roleplay) string {
	args := p.cardValues()
	// 还没选中任何角色时点「保存」等同新建 —— 别让用户白敲一遍
	if id := p.curID(); id != "" {
		args["id"] = id
	}
	// 记忆只在用户真的动过那个框时才提交（见 dirty 的说明）
	if p.dirty[rpFieldMemory] {
		args["memory"] = strings.TrimSpace(p.fields[rpFieldMemory].f.Value)
	}
	out, err := p.call(r, "save", args)
	if err != nil {
		return p.say(r, "保存失败："+err.Error(), false)
	}
	p.markClean()
	p.load(r)
	return p.say(r, noteOr(out, "已保存"), true)
}

// markClean 说"表单里的内容已经和存储一致了"。
func (p *rpPanel) markClean() {
	for i := range p.dirty {
		p.dirty[i] = false
	}
}

// ---------------------------------------------------------------------------
// 焦点环
// ---------------------------------------------------------------------------

// 环 = [顶栏按钮…][列表项…][字段…][保存身份]。
//
// 顺序就是"从上到下看到的顺序"：↑↓ 一路走下来，不会在屏幕两端来回跳。
// （"保存身份"在内容区里，所以它排在字段之后 —— 它是这一页最后一步。）
func (p *rpPanel) ringButton(k int) int { return k }
func (p *rpPanel) ringList(i int) int   { return rpBtnCount + i }
func (p *rpPanel) ringField(i int) int  { return rpBtnCount + len(p.items) + i }
func (p *rpPanel) ringPersona() int     { return rpBtnCount + len(p.items) + len(p.fields) }

func (p *rpPanel) ringLen() int {
	return rpBtnCount + len(p.items) + len(p.fields) + 1
}

func (p *rpPanel) setFocus(i int) {
	if len(p.fields) == 0 {
		p.focus = -1
		return
	}
	p.focus = clampInt(i, -1, p.ringLen()-1)
}

// ctlIsField 判断环上的焦点是不是第 i 个字段。
func (p *rpPanel) ctlIsField(i int) bool {
	if len(p.fields) == 0 || p.focus < 0 {
		return false
	}
	return p.focus == p.ringField(i)
}

// focusedField 返回当前聚焦的字段；焦点不在字段上时返回 nil。
func (p *rpPanel) focusedField() *rpField {
	for i := range p.fields {
		if p.ctlIsField(i) {
			return &p.fields[i]
		}
	}
	return nil
}

// moveFocus 上下移一格；落到列表项上就顺手把那个人选过来（换人 = 换表单）。
func (p *rpPanel) moveFocus(r *Roleplay, delta int) {
	if p.focus < 0 {
		p.setFocus(0)
	} else {
		p.setFocus(p.focus + delta)
	}
	if i := p.focus - rpBtnCount; i >= 0 && i < len(p.items) && i != p.sel {
		p.sel = i
		p.load(r)
	}
	p.say(r, "", true)
}

// enterRing 回车落在不同控件上有不同的意思：按钮 = 按下去、列表项 = 选它、
// 字段 = 走下一个。
func (p *rpPanel) enterRing(r *Roleplay) {
	if p.focus < 0 {
		p.setFocus(p.ringField(rpFieldName))
		p.say(r, "", true)
		return
	}
	switch {
	case p.focus < rpBtnCount:
		p.press(r, p.buttons()[p.focus].action)
		return
	case p.focus < rpBtnCount+len(p.items):
		p.sel = p.focus - rpBtnCount
		p.load(r)
		p.say(r, "已选「"+p.items[p.sel].Name+"」", true)
		return
	case p.focus < p.ringPersona():
		if p.focus == p.ringPersona()-1 {
			p.setFocus(p.ringPersona()) // 字段走完了 → 保存身份
		} else {
			p.setFocus(p.focus + 1)
		}
		p.say(r, "", true)
		return
	default:
		p.press(r, "savepersona")
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func noteOr(out map[string]any, def string) string {
	if s, ok := out["note"].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return def
}

// shortStamp 把 "2026-09-22 20:15" 缩成 "09-22 20:15"（列表里放不下年份）。
func shortStamp(s string) string {
	if len(s) >= 16 {
		return s[5:16]
	}
	return s
}

func splitPair(s string) (int, int) {
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return atoiSafe(s), 0
	}
	return atoiSafe(a), atoiSafe(b)
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
