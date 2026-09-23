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
// 插件页：这一页住在插件管理器自己身上
//
// 以前它长在 UI 插件里（workbench 的「插件」页），于是"界面不认识插件管理"这句话
// 也是假的 —— 界面里有 pluginInfo、scanPlugins、setPluginEnabled、deletePlugin
// 一整套，连"哪台机器上装了什么服务"都是硬编码的 probeList。
//
// 现在的分工：
//
//	这个文件   把画面画成"文本行 + 可点区域"，自己处理点击与按键
//	UI 插件    只借一块 w×h 的地方、把点击和按键转过来
//
// 业务逻辑一行都不在这里：列清单、启停、删除仍然是 main.go 里那一套
//（scan / setEnabled / remove），这里只在它上面加一层"看得见、点得到"。
//
// 三个刻意的取舍：
//
//  1. **宽屏分两栏，窄屏一列**：这一页天生是"左边一排东西、右边看详情"，
//     宽屏挤成一列等于把最该并排看的两样东西拆开。窄屏只能一列时，
//     列表缩成几行、详情跟在后面。
//  2. **列表永远看得见**：列表自己跟着选中项走（不跟着页面滚动），
//     详情才滚 —— 翻详情翻到忘了自己在看谁，是最常见的一种迷路。
//  3. **删除要说清楚会动哪些文件**：不可逆的操作，确认态里要把
//     目录 / 配置 / 日志三样摊开，还有"谁在依赖它"。光写一句"确认删除"，
//     用户点下去靠的是运气。
// ---------------------------------------------------------------------------

const (
	// panelService 是这一页的服务名：ui.page.<插件自己的名字>.v1。
	//
	// 不叫统一的 ui.page.v1：服务名在注册表里是一对一的，那样第二个想做界面的
	// 插件会被拒之门外。
	panelService = "ui.page.pluginmanager.v1"
	panelID      = "plugins"

	// panelChangedEvent 是"我这一页变了，重新画一遍"。
	panelChangedEvent = "ui.panel.changed"

	// pmBtnCount 是顶栏按钮个数（重新扫描 / 重启核心）。
	pmBtnCount = 2

	// pmListW 是宽屏下左栏的列宽：够放 "✓ com.official.workbench" 这样的 ID。
	pmListW = 36

	// pmWideMin 到多宽才分两栏。再窄下去两栏各自都放不下东西，
	// 不如一列来得实在。
	pmWideMin = 84

	// pmNarrowListRows 是窄屏下列表最多占几行。
	pmNarrowListRows = 6
)

// pmEntry 是详情区里的一行：怎么画 + 它属于环上的哪个控件（-1 = 不属于）。
type pmEntry struct {
	draw pmLine
	ctl  int
}

// pmLine 画一行（起始列与可用宽度在构建时定好，画的时候只给行号）。
type pmLine func(b *plugin.Block, y int)

// pmPanel 是这一页的全部界面状态。
type pmPanel struct {
	// mu 护住下面这些：render / action / key 可能同时到。
	mu sync.Mutex

	loaded bool
	items  []installed     // 已安装插件（含 manifest，详情要用的都在里面）
	alive  map[string]bool // 目录名 -> 是否在运行（Consume 探出来的）
	sel    int             // 选中项
	root   string          // 插件根目录，标题上顺带显示
	focus  int             // 环上下标；-1 = 键盘不落在任何控件上

	note       string
	ok         bool
	confirmDel bool // 删除的二次确认

	// stale 表示"外部可能动过插件目录了，下一帧重新扫一次"。
	//
	// 用原子量而不是锁：立它的人可能正握着面板的锁（这一页自己的动作就走这条路），
	// 再去加锁就自锁了。
	stale atomic.Bool

	rowOf map[int]int // 环下标 -> 详情区行号（画的时候登记）
}

// ---------------------------------------------------------------------------
// 与宿主对接
// ---------------------------------------------------------------------------

// list 回答"我有哪几页"。
func (p *pmPanel) list() []plugin.PanelInfo {
	return []plugin.PanelInfo{
		{ID: panelID, Title: "插件", Order: 10},
		{ID: rulesPanelID, Title: "规则", Order: 20},
	}
}

// handlePanel 是 ui.page.pluginmanager.v1 的分发入口。
func (m *Manager) handlePanel(method string, args json.RawMessage) (any, error) {
	switch method {
	case "list":
		return map[string]any{"panels": m.panel.list()}, nil
	case "render":
		var in plugin.PanelRequest
		_ = json.Unmarshal(args, &in)
		switch in.Panel {
		case panelID:
			return m.panel.render(m, in), nil
		case rulesPanelID:
			return renderRules(in), nil
		default:
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "插件管理器没有页面 %s", in.Panel)
		}
	case "action":
		var in plugin.PanelRequest
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
		}
		if in.Panel == rulesPanelID {
			// 「规则」页是纯文档，没有可点的东西
			return map[string]any{"note": ""}, nil
		}
		return map[string]any{"note": m.panel.action(m, in)}, nil
	case "key":
		var in plugin.PanelRequest
		_ = json.Unmarshal(args, &in)
		if in.Panel == rulesPanelID {
			// 「规则」页不认键盘（滚轮翻页归宿主）
			return map[string]any{"ok": true}, nil
		}
		m.panel.key(m, in.Kind)
		return map[string]any{"ok": true}, nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%s 不支持方法 %s", panelService, method)
	}
}

// invalidate 说"插件目录可能变了，下一帧重新扫一次"。
//
// 启停 / 删除之后必须立它，否则页面会停在旧清单上 —— 那看起来就是"点了没反应"。
func (p *pmPanel) invalidate(m *Manager) {
	p.stale.Store(true)
	p.changed(m)
}

// changed 通知宿主"这一页变了，重新画一遍"。
func (p *pmPanel) changed(m *Manager) {
	if m == nil || m.api == nil {
		return
	}
	_ = m.api.Emit(panelChangedEvent, map[string]any{
		"service": panelService,
		"panel":   panelID,
	})
}

// say 记一句反馈并通知宿主重画。
func (p *pmPanel) say(m *Manager, note string, ok bool) string {
	p.note, p.ok = note, ok
	p.changed(m)
	return note
}

// ---------------------------------------------------------------------------
// 读写
// ---------------------------------------------------------------------------

// load 扫一遍插件目录，并把"在不在运行"探出来。
//
// 直接用 m.scan() / m.alive()，不走 RPC：这一页就在插件管理器进程里，
// 绕一圈（各自再扫一次目录）没有任何意义，只是把刷新拖慢一倍。
func (p *pmPanel) load(m *Manager) {
	items, err := m.scan()
	if err != nil {
		p.items, p.alive = nil, nil
		p.note, p.ok = "读取插件目录失败："+err.Error(), false
		p.loaded = true
		return
	}
	p.items = items
	p.root = m.root
	p.alive = make(map[string]bool, len(items))
	for _, it := range items {
		p.alive[it.info.Dir] = m.alive(it)
	}
	if len(items) == 0 {
		p.sel = 0
	} else {
		p.sel = clampInt(p.sel, 0, len(items)-1)
	}
	p.loaded = true
	p.confirmDel = false
	p.focus = clampInt(p.focus, -1, p.ringLen()-1)
	// 刚扫完，手上这份就是最新的：清掉"外部可能动过"的标记，
	// 免得下一帧又白发一次扫描。
	p.stale.Store(false)
}

// selected 返回当前选中的那一项。
func (p *pmPanel) selected() (installed, bool) {
	if p.sel < 0 || p.sel >= len(p.items) {
		return installed{}, false
	}
	return p.items[p.sel], true
}

// running 判断某个插件是不是正在运行。
//
// 禁用的那份不算"运行中"：这一份重启后根本不会被加载，服务是同 ID 的另一份
// 在提供 —— 标成 ✓ 就成了两条一模一样的插件。
func (p *pmPanel) running(it installed) bool {
	if it.info.Disabled {
		return false
	}
	return p.alive[it.info.Dir]
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

// render 画这一页。w×h 是宿主给的地方，由它说了算。
//
// 这里**不做慢操作**：清单在 load 里扫好了（扫一次要读 N 个 manifest，
// 每帧都扫等于把宿主的每一帧都拖住）。
func (p *pmPanel) render(m *Manager, req plugin.PanelRequest) plugin.PanelRender {
	p.mu.Lock()
	defer p.mu.Unlock()

	w, h := req.W, req.H
	if w < 1 || h < 1 {
		return plugin.PanelRender{}
	}
	if !p.loaded || p.stale.Swap(false) {
		p.load(m)
	}
	b := plugin.NewBlock(w, h)
	if w < 40 || h < 6 {
		b.At(0).Text(plugin.SWarn, " 窗口太小，放不下这一页")
		return b.Render(0, 0, "把窗口拉大些")
	}

	// 顶栏两行钉住：这一页最先要看的两件事（装了多少、能按什么）不该跟着内容滚走
	const top = 2
	p.drawHeader(b, w)
	ch := h - top

	// 宽屏分两栏：左列列表（不跟着页面滚动），右列详情（跟着页面滚动）。
	// 分隔线占 1 列，后面再让 1 列空格，详情才不至于贴在竖线上。
	wide := w >= pmWideMin
	listW, detailW := 0, w
	if wide {
		listW = pmListW
		detailW = w - listW - 2
	}

	entries := p.detailLines(m, detailW)
	p.rowOf = map[int]int{}
	for i, e := range entries {
		if e.ctl >= 0 {
			p.rowOf[e.ctl] = i
		}
	}

	// 详情区从第几行开始（窄屏下列表占掉上面几行）
	listRows := len(p.items)
	if !wide {
		if listRows > pmNarrowListRows {
			listRows = pmNarrowListRows
		}
	}
	detailTop := 0
	if !wide {
		detailTop = listRows
	}
	detailRows := ch - detailTop
	if detailRows < 1 {
		detailRows = 1
	}

	maxScroll := maxInt(0, len(entries)-detailRows)
	scroll := clampInt(req.Scroll, 0, maxScroll)
	// 焦点必须看得见 —— 只有这一页自己知道环上的第 n 个控件画在第几行
	if row, ok := p.rowOf[p.focus]; ok {
		if row < scroll {
			scroll = row
		} else if row >= scroll+detailRows {
			scroll = row - detailRows + 1
		}
		scroll = clampInt(scroll, 0, maxScroll)
	}

	listFrom := 0
	if n := len(p.items) - listRows; n > 0 {
		listFrom = plugin.Window(p.sel, 0, listRows, len(p.items))
	}
	for i := 0; i < ch; i++ {
		y := top + i
		if wide {
			p.drawListRow(b, y, listW, listFrom, i)
			b.At(y).Text(plugin.SBorder, "│")
			b.At(y).Text("", " ")
		} else if i < detailTop {
			p.drawListRow(b, y, w, listFrom, i)
		}
		if i >= detailTop {
			if idx := scroll + (i - detailTop); idx < len(entries) {
				entries[idx].draw(b, y)
			}
		}
	}

	res := b.Render(scroll, maxScroll, pmHint(w))
	res.WantKeys = true
	return res
}

// drawHeader 画顶栏：装了多少、运行多少、能按什么。
func (p *pmPanel) drawHeader(b *plugin.Block, w int) {
	run := 0
	for _, it := range p.items {
		if p.running(it) {
			run++
		}
	}
	head := fmt.Sprintf(" 插件 %d · 运行 %d", len(p.items), run)
	b.At(0).Text(plugin.SAccent+plugin.SBold, head)
	tail := p.root
	if p.note != "" {
		st := plugin.Style(plugin.SDim)
		if !p.ok {
			st = plugin.Style(plugin.SBad)
		}
		b.At(0).Text(st, "  "+plugin.Truncate(p.note, maxInt(0, w-plugin.DisplayWidth(head)-4)))
	} else if tail != "" {
		b.At(0).Text(plugin.SDim, "  "+plugin.Truncate(tail, maxInt(0, w-plugin.DisplayWidth(head)-4)))
	}

	x := 1
	x = p.drawHeaderButton(b, 1, x, w, 0, "重新扫描", "btn", "reload", false)
	// 重启交给宿主：它是界面自己的事（重启会把界面自己也收掉，得先把话写在屏幕上）
	p.drawHeaderButton(b, 1, x, w, 1, "重启核心", "@ui:restart", "", false)
}

// drawHeaderButton 画一个顶栏按钮，返回下一个可用列。
func (p *pmPanel) drawHeaderButton(b *plugin.Block, y, x, w, k int, label, action, arg string, primary bool) int {
	text := " " + label + " "
	if x+plugin.DisplayWidth(text) > w {
		return x
	}
	if p.focus == p.ringButton(k) {
		b.At(y).Text(plugin.SAccent+plugin.SBold, text)
		b.Hit(y, x, plugin.DisplayWidth(text), action, arg)
		return x + plugin.DisplayWidth(text) + 2
	}
	return b.Button(y, x, label, action, arg, primary)
}

// drawListRow 画列表区的第 i 行（列表窗口跟着选中项走，不跟着页面滚动）。
func (p *pmPanel) drawListRow(b *plugin.Block, y, listW, from, i int) {
	idx := from + i
	if idx >= len(p.items) {
		b.At(y).Text("", strings.Repeat(" ", listW))
		return
	}
	it := p.items[idx]
	mark, style := "✗", plugin.Style(plugin.SBad)
	switch {
	case it.info.Disabled:
		mark, style = "·", plugin.Style(plugin.SDim)
	case it.info.Unreadable:
		mark, style = "!", plugin.Style(plugin.SWarn)
	case p.running(it):
		mark, style = "✓", plugin.Style(plugin.SOK)
	}
	if idx == p.sel {
		style = plugin.Style(plugin.SSel + plugin.SBold)
	}
	name := firstNonEmpty(it.info.ID, it.info.Dir)
	if it.info.Disabled {
		name += "（禁用）"
	} else if it.info.Unreadable {
		// 列表里只留一个短标记：为什么读不出、后果是什么，在详情里说
		name += "（读不出）"
	}
	// ID 后面留出版本的位置：v1.0.0 之类通常 6 列够用
	ver := ""
	if it.info.Version != "" {
		ver = "v" + it.info.Version
	}
	b.At(y).Text(style, plugin.Pad(plugin.Truncate(" "+mark+" "+name, listW-8), listW-8))
	b.At(y).Text(plugin.SDim, plugin.Pad(plugin.Truncate(ver, 8), 8))
	b.Hit(y, 0, listW, "pick", strconv.Itoa(idx))
}

// detailLines 建详情区：选中的插件是谁、能改什么。
func (p *pmPanel) detailLines(m *Manager, w int) []pmEntry {
	var out []pmEntry
	add := func(draw pmLine, ctl int) { out = append(out, pmEntry{draw: draw, ctl: ctl}) }
	blank := func() { add(func(*plugin.Block, int) {}, -1) }
	text := func(s, style string) {
		add(func(b *plugin.Block, y int) {
			b.At(y).Text(plugin.Style(style), plugin.Truncate(s, w))
		}, -1)
	}
	wrap := func(s, style string) {
		for _, ln := range plugin.Wrap(s, maxInt(8, w-1)) {
			text(" "+ln, style)
		}
	}

	it, ok := p.selected()
	if !ok {
		add(func(b *plugin.Block, y int) {
			b.At(y).Text(plugin.SDim, " 没有读到插件目录")
		}, -1)
		return out
	}
	info := it.info

	text(" 详情", plugin.SDim+plugin.SBold)
	if info.Unreadable {
		text(" "+info.Dir, plugin.SWarn+plugin.SBold)
		wrap("这个目录没有可读的 manifest.json，插件管理器认不出它归属谁，因此拒绝启停与删除。", plugin.SWarn)
		wrap(fmt.Sprintf("%v", info.Error), plugin.SDim)
		return out
	}
	text(" "+firstNonEmpty(info.ID, info.Dir), plugin.SAccent+plugin.SBold)
	if info.Name != "" && info.Name != info.ID {
		text(" "+info.Name, "")
	}
	text(" 目录   "+info.Dir, plugin.SDim)
	if info.Version != "" {
		text(" 版本   v"+info.Version+"   "+info.Runtime, "")
	}
	if info.Entry != "" {
		text(" 入口   "+info.Entry, "")
	}
	text(" 状态   "+pmStateText(p, it), pmStateStyle(p, it))
	if info.Description != "" {
		blank()
		wrap(info.Description, plugin.SDim)
	}
	if n := p.sameIDCount(info.ID); n > 1 {
		blank()
		wrap(fmt.Sprintf("目录里有 %d 份 ID 相同的插件，启停 / 删除只对选中的这一份生效（靠目录名区分）。", n), plugin.SWarn)
	}
	if info.Self {
		blank()
		wrap("这是插件管理器自己：不能停用，也不能删除 —— 删掉之后就没有东西能把它装回来了。", plugin.SWarn)
	}

	// 提供的服务：✓ 表示此刻真的有人在提供它（Consume 探得通）
	if len(info.Provides) > 0 {
		blank()
		text(" 提供的服务", plugin.SDim+plugin.SBold)
		for _, s := range info.Provides {
			style, mark := plugin.SBad, "✗"
			if p.alive[info.Dir] {
				style, mark = plugin.SOK, "✓"
			}
			text("  "+mark+" "+s, style)
		}
	}
	if len(info.Consumes) > 0 {
		blank()
		text(" 消费的服务", plugin.SDim+plugin.SBold)
		for _, s := range info.Consumes {
			text("  · "+s, "")
		}
	}
	if len(info.Permissions) > 0 {
		blank()
		text(" 权限", plugin.SDim+plugin.SBold)
		for _, s := range info.Permissions {
			text("  · "+s, "")
		}
	}
	if len(it.man.Dependencies) > 0 {
		blank()
		text(" 依赖", plugin.SDim+plugin.SBold)
		for id, rng := range it.man.Dependencies {
			text(fmt.Sprintf("  · %s %s", id, rng), "")
		}
	}
	if info.Compat != "" {
		text(" 兼容核心 "+info.Compat, plugin.SDim)
	}

	if info.Self {
		return out
	}

	// 动作：启停 + 删除。删除不可逆，所以要按两次。
	blank()
	text(" 启停", plugin.SDim+plugin.SBold)
	label, note := "禁用", "当前已启用，重启后照常加载"
	if info.Disabled {
		label, note = "启用", "当前已禁用，重启后不再加载"
	}
	add(func(b *plugin.Block, y int) {
		p.drawRowButton(b, y, 1, w, p.ringToggle(), label, "btn", "toggle", !info.Disabled)
		b.At(y).Text(plugin.SDim, " "+plugin.Truncate(note, maxInt(0, w-15)))
	}, p.ringToggle())

	blank()
	if p.confirmDel {
		p.deleteWarnings(m, it, w, text, wrap)
		add(func(b *plugin.Block, y int) {
			x := p.drawRowButton(b, y, 1, w, -1, "确认删除", "btn", "del", true)
			if x+8 <= w {
				b.At(y).Text(plugin.SBtn+plugin.SBold, " 取消 ")
				b.Hit(y, x, 6, "btn", "delcancel")
			}
			b.At(y).Text(plugin.SBad, " Esc 取消")
		}, p.ringDel())
	} else {
		add(func(b *plugin.Block, y int) {
			p.drawRowButton(b, y, 1, w, p.ringDel(), "删除插件", "btn", "del", false)
			b.At(y).Text(plugin.SDim, " 连配置与日志一起清除")
		}, p.ringDel())
	}
	return out
}

// deleteWarnings 把"删掉会动哪些文件、谁在依赖它"摊开。
//
// 不可逆的操作，确认态里必须说清楚代价：只写一句"确认删除"，
// 用户点下去靠的是运气。
func (p *pmPanel) deleteWarnings(m *Manager, it installed, w int,
	text func(string, string), wrap func(string, string)) {
	text(" 将删除", plugin.SWarn+plugin.SBold)
	kinds := []string{"插件目录", "配置", "日志"}
	for i, path := range m.plan(it, false) {
		kind := "其它"
		if i < len(kinds) {
			kind = kinds[i]
		}
		text("  · "+kind+"  "+path, plugin.SDim)
	}
	for _, wmsg := range dependents(p.items, it) {
		wrap(wmsg, plugin.SWarn)
	}
}

// drawRowButton 画一个行内按钮，返回下一个可用列。
func (p *pmPanel) drawRowButton(b *plugin.Block, y, x, w, k int, label, action, arg string, primary bool) int {
	text := " " + label + " "
	if x+plugin.DisplayWidth(text) > w {
		return x
	}
	if k >= 0 && p.focus == k {
		b.At(y).Text(plugin.SAccent+plugin.SBold, text)
		b.Hit(y, x, plugin.DisplayWidth(text), action, arg)
		return x + plugin.DisplayWidth(text) + 2
	}
	return b.Button(y, x, label, action, arg, primary)
}

// pmStateText / pmStateStyle 把状态说成人话。
func pmStateText(p *pmPanel, it installed) string {
	switch {
	case it.info.Unreadable:
		return "无法读取"
	case it.info.Disabled:
		return "已禁用（重启后不再加载）"
	case p.running(it) && len(it.info.Provides) > 0:
		return "运行中"
	case p.running(it):
		return "运行中（没有对外提供服务的声明）"
	default:
		return "未运行（重启后会被加载）"
	}
}

func pmStateStyle(p *pmPanel, it installed) string {
	if p.running(it) {
		return plugin.SOK
	}
	if it.info.Unreadable {
		return plugin.SBad
	}
	return plugin.SDim
}

// pmHint 是底部那行用法说明（宿主照抄）。
func pmHint(w int) string {
	if w < 70 {
		return "↑↓ 选插件 · 回车按按钮 · Esc 取消 · Tab 切页"
	}
	return "点插件看详情 · ↑↓ 换控件 · 回车按下去 · 启停与删除都是重启后生效 · Tab 切页"
}

// sameIDCount 数一数目录里有几份 ID 相同的插件。
func (p *pmPanel) sameIDCount(id string) int {
	if id == "" {
		return 0
	}
	n := 0
	for _, it := range p.items {
		if it.info.ID == id {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// 焦点环
// ---------------------------------------------------------------------------

// 环 = [重新扫描][重启核心][列表项…][启用停用][删除]。
//
// 顺序就是"从上到下看到的顺序"：↑↓ 一路走下来，不会在屏幕两端来回跳。
func (p *pmPanel) ringButton(k int) int { return k }
func (p *pmPanel) ringList(i int) int   { return pmBtnCount + i }
func (p *pmPanel) ringToggle() int      { return pmBtnCount + len(p.items) }
func (p *pmPanel) ringDel() int         { return pmBtnCount + len(p.items) + 1 }
func (p *pmPanel) ringLen() int         { return pmBtnCount + len(p.items) + 2 }

func (p *pmPanel) setFocus(i int) { p.focus = clampInt(i, -1, p.ringLen()-1) }

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------

// action 处理一次点击。
func (p *pmPanel) action(m *Manager, req plugin.PanelRequest) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.loaded || p.stale.Swap(false) {
		p.load(m)
	}
	switch req.Action {
	case "pick":
		i := atoiSafe(req.Arg)
		if i < 0 || i >= len(p.items) {
			return ""
		}
		p.sel, p.confirmDel = i, false
		p.setFocus(p.ringList(i))
		return p.say(m, "已选中 "+firstNonEmpty(p.items[i].info.ID, p.items[i].info.Dir), true)

	case "toggle":
		p.setFocus(p.ringToggle())
		return p.toggle(m)

	case "del":
		p.setFocus(p.ringDel())
		return p.remove(m)

	case "delcancel":
		p.confirmDel = false
		return p.say(m, "已取消删除", true)

	case "reload":
		p.setFocus(p.ringButton(0))
		p.load(m)
		return p.say(m, fmt.Sprintf("已重新扫描：%d 个插件", len(p.items)), true)
	}
	return ""
}

// key 处理一次按键。
//
// 只处理这一页自己的事：Tab（切页）由宿主扣下，Ctrl+C 是复制，退出是 Ctrl+Q。
// 否则用户会被困在这一页里出不去。
func (p *pmPanel) key(m *Manager, kind string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.loaded || p.stale.Swap(false) {
		p.load(m)
	}
	switch kind {
	case "up":
		p.moveFocus(m, -1)
	case "down":
		p.moveFocus(m, 1)
	case "enter":
		p.enterRing(m)
	case "esc":
		if p.confirmDel {
			p.confirmDel = false
			p.say(m, "已取消删除", true)
			return
		}
		p.focus = -1
		p.say(m, "", true)
	}
}

// moveFocus 上下移一格；落在列表项上就顺手把它选过来。
func (p *pmPanel) moveFocus(m *Manager, delta int) {
	if p.focus < 0 {
		p.setFocus(0)
	} else {
		p.setFocus(p.focus + delta)
	}
	if i := p.focus - pmBtnCount; i >= 0 && i < len(p.items) && i != p.sel {
		p.sel, p.confirmDel = i, false
	}
	p.say(m, "", true)
}

// enterRing 回车落在不同控件上有不同的意思。
func (p *pmPanel) enterRing(m *Manager) {
	switch {
	case p.focus < 0:
		p.setFocus(p.ringButton(0))
		p.say(m, "", true)
	case p.focus < pmBtnCount:
		// 顶栏两个按钮：第 1 个是重新扫描；第 2 个（重启核心）走宿主，键盘够不着它
		if p.focus == 0 {
			p.load(m)
			p.say(m, fmt.Sprintf("已重新扫描：%d 个插件", len(p.items)), true)
			return
		}
		p.say(m, "「重启核心」请用鼠标点：它由界面执行，得先把提示写在屏幕上", false)
	case p.focus < pmBtnCount+len(p.items):
		p.sel = p.focus - pmBtnCount
		p.confirmDel = false
		p.say(m, "已选中 "+firstNonEmpty(p.items[p.sel].info.ID, p.items[p.sel].info.Dir), true)
	case p.focus == p.ringToggle():
		p.toggle(m)
	default:
		p.remove(m)
	}
}

// toggle 启用 / 停用选中的插件。
func (p *pmPanel) toggle(m *Manager) string {
	it, ok := p.selected()
	if !ok {
		return p.say(m, "先选一个插件", false)
	}
	enable := it.info.Disabled // 当前禁用的就启用，反之禁用
	raw, err := json.Marshal(map[string]any{"id": it.info.ID, "dir": it.info.Dir})
	if err != nil {
		return p.say(m, "参数编码失败："+err.Error(), false)
	}
	out, err := m.setEnabled(raw, enable)
	if err != nil {
		return p.say(m, "操作失败："+err.Error(), false)
	}
	p.stale.Store(true)
	p.load(m)
	return p.say(m, noteOf(out, "已切换启用状态"), true)
}

// remove 删除插件：不可逆，所以要按两次。
func (p *pmPanel) remove(m *Manager) string {
	it, ok := p.selected()
	if !ok {
		return p.say(m, "先选一个插件", false)
	}
	if !p.confirmDel {
		p.confirmDel = true
		p.changed(m)
		return p.say(m, "再点一次「确认删除」：目录、配置、日志都会清掉，重启后生效", false)
	}
	p.confirmDel = false
	raw, err := json.Marshal(map[string]any{"id": it.info.ID, "dir": it.info.Dir})
	if err != nil {
		return p.say(m, "参数编码失败："+err.Error(), false)
	}
	out, err := m.remove(raw)
	if err != nil {
		return p.say(m, "删除失败："+err.Error(), false)
	}
	p.stale.Store(true)
	p.load(m)
	return p.say(m, noteOf(out, "已删除插件；重启后生效"), true)
}

// noteOf 取插件方法返回里的 note。
func noteOf(out any, def string) string {
	m, ok := out.(map[string]any)
	if !ok {
		return def
	}
	if s, ok := m["note"].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return def
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

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
