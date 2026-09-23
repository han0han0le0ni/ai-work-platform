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
// 「文生图」配置页：这一页住在 imagegen 插件自己身上。
//
// 文生图用什么服务、密钥、模型、尺寸，之前只能靠环境变量 / 配置文件改，
// 界面上没有入口。现在和 llm 的「模型」页同一套办法：插件自己渲染这一页，
// 直接调 settings() / configure()，不再需要界面认识 imagegen 的任何字段名。
// ---------------------------------------------------------------------------

const (
	panelService = "ui.page.imagegen.v1"
	panelID      = "imagegen"

	panelChangedEvent = "ui.panel.changed"

	fieldBaseURL = "baseUrl"
	fieldModel   = "model"
	fieldSize    = "size"
	fieldDir     = "dir"
	fieldKey     = "apiKey"
)

type igCtl struct {
	kind string // preset / field / btn
	arg  string
}

type igEntry struct {
	draw func(b *plugin.Block, y int)
	ctls []int
}

type igPanel struct {
	mu sync.Mutex

	loaded bool
	stale  atomic.Bool

	presetKey string

	baseURL plugin.Field
	model   plugin.Field
	apiKey  plugin.Field
	size    plugin.Field
	dir     plugin.Field

	// 密钥只显示"配没配"：明文拿不回来，框里留空表示"不改动"。
	hasKey  bool
	keyMask string

	// 读回来那一份，保存时只提交真正变过的字段。
	loadedProvider string
	loadedBaseURL  string
	loadedModel    string
	loadedSize     string
	loadedDir      string

	note string
	ok   bool

	ring  []igCtl
	focus int
	rowOf map[int]int
}

func newIGPanel() igPanel { return igPanel{focus: -1} }

func (p *igPanel) list() []plugin.PanelInfo {
	return []plugin.PanelInfo{{ID: panelID, Title: "文生图", Order: 40}}
}

func (g *Provider) handlePanel(method string, args json.RawMessage) (any, error) {
	switch method {
	case "list":
		return map[string]any{"panels": g.panel.list()}, nil
	case "render":
		var in plugin.PanelRequest
		_ = json.Unmarshal(args, &in)
		if in.Panel != "" && in.Panel != panelID {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "imagegen 没有页面 %s", in.Panel)
		}
		return g.panel.render(g, in), nil
	case "action":
		var in plugin.PanelRequest
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
		}
		return map[string]any{"note": g.panel.action(g, in)}, nil
	case "key":
		var in plugin.PanelRequest
		_ = json.Unmarshal(args, &in)
		copyText, exit := g.panel.key(g, in.Kind, in.Text)
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

func (p *igPanel) changed(g *Provider) {
	if g == nil || g.ctx == nil {
		return
	}
	_ = g.ctx.Emit(panelChangedEvent, map[string]any{
		"service": panelService, "panel": panelID,
	})
}

func (p *igPanel) say(g *Provider, note string, ok bool) string {
	p.note, p.ok = note, ok
	p.changed(g)
	return note
}

func (p *igPanel) load(g *Provider) {
	s := g.settings()
	p.baseURL.SetValue(s.BaseURL)
	p.model.SetValue(s.Model)
	p.size.SetValue(s.Size)
	p.dir.SetValue(s.Dir)
	p.apiKey.Clear()
	p.hasKey = s.APIKey != ""
	p.keyMask = ""
	if p.hasKey {
		p.keyMask = maskKey(s.APIKey)
	}
	p.presetKey = presetByBaseURL(s.BaseURL, s.Provider)
	p.loadedProvider = s.Provider
	p.loadedBaseURL = s.BaseURL
	p.loadedModel = s.Model
	p.loadedSize = s.Size
	p.loadedDir = s.Dir
	p.loaded = true
	p.stale.Store(false)
}

// presetByBaseURL 按当前配置反推选中的服务商；对不上就按 provider 兜底。
func presetByBaseURL(baseURL, provider string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, it := range imagePresets {
		if baseURL != "" && strings.TrimRight(it.BaseURL, "/") == baseURL {
			return it.Key
		}
	}
	for _, it := range imagePresets {
		if it.Key == provider {
			return it.Key
		}
	}
	return ""
}

func (p *igPanel) render(g *Provider, req plugin.PanelRequest) plugin.PanelRender {
	p.mu.Lock()
	defer p.mu.Unlock()

	w, h := req.W, req.H
	if w < 1 || h < 1 {
		return plugin.PanelRender{}
	}
	if !p.loaded || p.stale.Swap(false) {
		p.load(g)
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
	res := b.Render(scroll, maxScroll, "点服务商 → 地址与模型自动填好 → 粘密钥 → 保存并生效 · Tab 切页")
	res.WantKeys = true
	res.WantCopy = true // 有可选的输入框：Ctrl+C 交给这一页复制
	return res
}

func (p *igPanel) drawHeader(b *plugin.Block, w int) {
	b.At(0).Text(plugin.SAccent+plugin.SBold, " 文生图")
	cur := firstNonEmpty(strings.TrimSpace(p.model.Value), "未指定模型")
	b.At(0).Text(plugin.SDim, "  "+plugin.Truncate(cur, maxInt(0, w-8)))
	if p.note != "" {
		st := plugin.Style(plugin.SDim)
		if !p.ok {
			st = plugin.Style(plugin.SBad)
		}
		b.At(1).Text(st, " "+plugin.Truncate(plugin.OneLine(p.note), maxInt(0, w-2)))
	}
}

func (p *igPanel) body(w int) []igEntry {
	e := &igBuilder{p: p, w: w}
	text := func(s, style string) { e.text(s, style) }
	blank := e.blank

	text(" 服务商", plugin.SDim+plugin.SBold)
	e.note("点一个，地址与默认模型自动填好，你只剩粘密钥这一步。", 2)
	var chips []igChip
	for _, it := range imagePresets {
		chips = append(chips, p.chip(it.Label, "preset", it.Key, p.presetKey == it.Key))
	}
	e.chips(w, chips...)
	// 选中后说清这是哪个、密钥去哪申请
	if it, ok := presetByKey(p.presetKey); ok {
		e.text("   "+plugin.Truncate(it.Label+" · "+it.Note, maxInt(8, w-4)), plugin.SAccent)
	}
	blank()

	text(" 当前连接", plugin.SDim+plugin.SBold)
	key := "未配置密钥"
	if p.hasKey {
		key = "密钥 " + firstNonEmpty(p.keyMask, "已配置")
	}
	e.text("   "+plugin.Truncate(
		firstNonEmpty(strings.TrimSpace(p.model.Value), "未指定模型")+" · "+key,
		maxInt(4, w-5)), plugin.SDim)
	blank()

	e.field(fieldBaseURL, &p.baseURL, "地址", "例：https://api.openai.com/v1")
	e.field(fieldModel, &p.model, "模型", "例：dall-e-3")
	e.field(fieldSize, &p.size, "尺寸", "例：1024x1024 / 1024x1792")
	e.field(fieldDir, &p.dir, "目录", "空 = 工作区/generated-images")
	e.field(fieldKey, &p.apiKey, "密钥", p.keyHint())
	blank()

	e.row(func(b *plugin.Block, y int) {
		b.Button(y, 1, "保存并生效", "btn", "save", true)
	}, p.ctl("btn", "save"))
	return e.out
}

func (p *igPanel) keyHint() string {
	if p.hasKey {
		if p.keyMask != "" {
			return "已配置 " + p.keyMask + "；要换直接粘新的，减号清空"
		}
		return "已配置；要换直接粘新的，减号清空"
	}
	return "未配置：点一下再粘贴"
}

// ---------------------------------------------------------------------------
// 画一行的小工具
// ---------------------------------------------------------------------------

type igChip struct {
	label, action, arg string
	on                 bool
	ctl                int
}

func (p *igPanel) chip(label, action, arg string, on bool) igChip {
	return igChip{label: label, action: action, arg: arg, on: on, ctl: p.ctl(action, arg)}
}

func (p *igPanel) ctl(kind, arg string) int {
	p.ring = append(p.ring, igCtl{kind: kind, arg: arg})
	return len(p.ring) - 1
}

type igBuilder struct {
	p   *igPanel
	w   int
	out []igEntry
}

func (e *igBuilder) add(list ...igEntry) { e.out = append(e.out, list...) }

func (e *igBuilder) blank() {
	e.out = append(e.out, igEntry{draw: func(*plugin.Block, int) {}})
}

func (e *igBuilder) text(s, style string) {
	line := s
	e.out = append(e.out, igEntry{draw: func(b *plugin.Block, y int) {
		b.At(y).Text(plugin.Style(style), plugin.Truncate(line, e.w))
	}})
}

func (e *igBuilder) note(s string, indent int) {
	for _, ln := range plugin.Wrap(s, maxInt(8, e.w-2-indent)) {
		e.text(" "+strings.Repeat(" ", indent)+ln, plugin.SDim)
	}
}

func (e *igBuilder) chips(w int, items ...igChip) {
	var cur []igChip
	used := 2
	flush := func() {
		if len(cur) == 0 {
			return
		}
		batch := append([]igChip{}, cur...)
		var ctls []int
		for _, it := range batch {
			ctls = append(ctls, it.ctl)
		}
		e.out = append(e.out, igEntry{ctls: ctls, draw: func(b *plugin.Block, y int) {
			x := 1
			for _, it := range batch {
				nx := b.Chip(y, x, it.label, it.action, it.arg, it.on)
				if nx == x {
					break
				}
				x = nx
			}
		}})
		cur, used = nil, 2
	}
	for _, it := range items {
		need := plugin.DisplayWidth(it.label) + 4
		if it.on {
			need += 2
		}
		if used+need > w-1 {
			flush()
		}
		cur = append(cur, it)
		used += need + 2
	}
	flush()
}

func (e *igBuilder) row(draw func(b *plugin.Block, y int), ctls ...int) {
	e.out = append(e.out, igEntry{ctls: ctls, draw: func(b *plugin.Block, y int) {
		draw(b, y)
	}})
}

func (e *igBuilder) field(name string, f *plugin.Field, label, hint string) {
	p := e.p
	ctl := p.ctl("field", name)
	e.out = append(e.out, igEntry{ctls: []int{ctl}, draw: func(b *plugin.Block, y int) {
		focused := p.focus == ctl
		st := plugin.Style("")
		if focused {
			st = plugin.Style(plugin.SSel)
		}
		b.At(y).Text(st, " "+plugin.Pad(label, 6))
		inner := maxInt(8, e.w-20)
		b.At(y).Text(st, "[")
		used := f.Draw(b.At(y), inner, focused, "")
		b.At(y).Text(st, plugin.Pad("", maxInt(0, inner-used)))
		b.At(y).Text(st, "]")
		for col := 0; col < inner; col++ {
			b.Hit(y, 8+col, 1, "caret", name+":"+strconv.Itoa(col))
		}
		if !focused && strings.TrimSpace(f.Value) == "" && hint != "" {
			b.At(y).Text(plugin.SDim, " "+plugin.Truncate(hint, maxInt(0, e.w-8-inner-3)))
		}
	}})
}

// ---------------------------------------------------------------------------
// 焦点与按键
// ---------------------------------------------------------------------------

func (p *igPanel) moveFocus(delta int) {
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

func (p *igPanel) focusedField() (*plugin.Field, string) {
	if p.focus < 0 || p.focus >= len(p.ring) {
		return nil, ""
	}
	c := p.ring[p.focus]
	if c.kind != "field" {
		return nil, ""
	}
	switch c.arg {
	case fieldBaseURL:
		return &p.baseURL, fieldBaseURL
	case fieldModel:
		return &p.model, fieldModel
	case fieldSize:
		return &p.size, fieldSize
	case fieldDir:
		return &p.dir, fieldDir
	case fieldKey:
		return &p.apiKey, fieldKey
	}
	return nil, ""
}

func (p *igPanel) key(g *Provider, kind, text string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.loaded || p.stale.Swap(false) {
		p.load(g)
	}
	// Ctrl+C：只复制。没选中就返回空，退出是 Ctrl+Q（宿主管），不在这里。
	if kind == "ctrl+c" {
		return p.selection(), false
	}
	if f, _ := p.focusedField(); f != nil {
		switch kind {
		case "rune":
			for _, r := range text {
				f.Insert(r)
			}
			p.say(g, "", true)
			return "", false
		case "backspace":
			f.Backspace()
			p.say(g, "", true)
			return "", false
		case "delete":
			f.Delete()
			p.say(g, "", true)
			return "", false
		case "left":
			f.Left()
			return "", false
		case "right":
			f.Right()
			return "", false
		case "home":
			f.Home()
			return "", false
		case "end":
			f.End()
			return "", false
		case "enter":
			p.moveFocus(1)
			p.say(g, "", true)
			return "", false
		case "esc":
			p.focus = -1
			p.say(g, "", true)
			return "", false
		case "up":
			p.moveFocus(-1)
			p.say(g, "", true)
			return "", false
		case "down":
			p.moveFocus(1)
			p.say(g, "", true)
			return "", false
		}
		return "", false
	}
	switch kind {
	case "up":
		p.moveFocus(-1)
		p.say(g, "", true)
	case "down":
		p.moveFocus(1)
		p.say(g, "", true)
	case "enter":
		p.activate(g)
	case "esc":
		p.focus = -1
		p.say(g, "", true)
	}
	return "", false
}

// selection 返回当前选中的文字：哪个字段里有选中段就取哪个。
func (p *igPanel) selection() string {
	for _, f := range []*plugin.Field{&p.baseURL, &p.model, &p.size, &p.dir, &p.apiKey} {
		if f.HasSel() {
			return f.Selection()
		}
	}
	return ""
}

func (p *igPanel) activate(g *Provider) {
	if p.focus < 0 || p.focus >= len(p.ring) {
		p.moveFocus(1)
		p.say(g, "", true)
		return
	}
	c := p.ring[p.focus]
	switch c.kind {
	case "field":
		return
	case "preset", "btn":
		p.apply(g, plugin.PanelRequest{Action: c.kind, Arg: c.arg})
	}
}

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------

func (p *igPanel) action(g *Provider, req plugin.PanelRequest) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.apply(g, req)
}

func (p *igPanel) apply(g *Provider, req plugin.PanelRequest) string {
	if !p.loaded || p.stale.Swap(false) {
		p.load(g)
	}
	switch req.Action {
	case "caret":
		name, colText, ok := strings.Cut(req.Arg, ":")
		if !ok {
			return ""
		}
		if f := p.fieldByName(name); f != nil {
			f.SetCursor(f.CaretAtCol(atoiSafe(colText)))
			f.SelStart(f.Cur()) // 按下那一下：光标落点也是拖选锚点
		}
		p.focus = p.ctlIndex("field", name)
		return ""

	case "sel":
		// 按住拖动：宿主把拖到的列号转过来，选区从锚点扩过去
		name, colText, ok := strings.Cut(req.Arg, ":")
		if !ok {
			return ""
		}
		if f := p.fieldByName(name); f != nil {
			f.SelExtend(f.CaretAtCol(atoiSafe(colText)))
		}
		p.focus = p.ctlIndex("field", name)
		if f := p.fieldByName(name); f != nil && f.HasSel() {
			p.say(g, fmt.Sprintf("已选中 %d 个字符：Ctrl+C 复制，Esc 取消", len([]rune(f.Selection()))), true)
		}
		return ""

	case "preset":
		p.focus = p.ctlIndex("preset", req.Arg)
		return p.applyPreset(g, req.Arg)

	case "btn":
		if req.Arg == "save" {
			return p.save(g)
		}
	}
	return ""
}

func (p *igPanel) ctlIndex(kind, arg string) int {
	for i, c := range p.ring {
		if c.kind == kind && c.arg == arg {
			return i
		}
	}
	return -1
}

func (p *igPanel) fieldByName(name string) *plugin.Field {
	switch name {
	case fieldBaseURL:
		return &p.baseURL
	case fieldModel:
		return &p.model
	case fieldSize:
		return &p.size
	case fieldDir:
		return &p.dir
	case fieldKey:
		return &p.apiKey
	}
	return nil
}

// applyPreset 应用一个服务商预设：填好地址与默认模型，焦点送到密钥框。
func (p *igPanel) applyPreset(g *Provider, key string) string {
	it, ok := presetByKey(key)
	if !ok {
		return ""
	}
	p.presetKey = it.Key
	p.baseURL.SetValue(it.BaseURL)
	p.model.SetValue(it.Model)
	p.focus = p.ctlIndex("field", fieldKey)
	return p.say(g, "已选 "+it.Label+"：粘贴 API Key，点「保存并生效」即生效", true)
}

// save 保存配置：只提交真正变过的字段；密钥留空=不改动，减号=清空。
func (p *igPanel) save(g *Provider) string {
	params := map[string]any{}
	if s := strings.TrimSpace(p.baseURL.Value); s != "" && s != p.loadedBaseURL {
		params["baseUrl"] = s
	}
	if s := strings.TrimSpace(p.model.Value); s != "" && s != p.loadedModel {
		params["model"] = s
	}
	if s := strings.TrimSpace(p.size.Value); s != "" && s != p.loadedSize {
		params["size"] = s
	}
	if s := strings.TrimSpace(p.dir.Value); s != p.loadedDir {
		params["dir"] = s
	}
	// 密钥：框里从来不装明文，所以"空"只能解释成不改动
	switch s := strings.TrimSpace(p.apiKey.Value); s {
	case "":
	case "-":
		params["apiKey"] = ""
	default:
		params["apiKey"] = s
	}
	if len(params) == 0 {
		return p.say(g, "没有可保存的改动", false)
	}
	blob, _ := json.Marshal(params)
	if _, err := g.configure(json.RawMessage(blob)); err != nil {
		return p.say(g, "保存失败："+err.Error(), false)
	}
	p.apiKey.Clear()
	p.load(g)
	return p.say(g, "已保存并生效：下次生图就按这份配置走", true)
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
