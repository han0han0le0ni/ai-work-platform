package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// 工作区插件自带的那一页界面（ui.panel.v1）
//
// 这一页以前长在 UI 插件里（workbench 的「文件」页），于是每改一次界面都要动
// UI 插件。现在它长在自己身上：**插件自己把画面画成文本行 + 可点区域**，
// 交给 UI 插件去合成 —— 换个 UI 插件，这一页照样能用；改这一页也不用碰别人。
//
// 分工：宿主只管"把 w×h 这么大一块地方给你"和"把点击、按键转给你"，
// 画什么、怎么排、点了干什么，全在这里。
// ---------------------------------------------------------------------------

// panelService 是"插件自带页面"这条通道的服务名。
//
// 约定是 ui.page.<名字>.v1，名字里带上插件自己的名字 —— 服务名在注册表里
// 是一对一的，统一叫 ui.page.v1 的话，第二个想做界面的插件就会被拒之门外。
const panelService = "ui.page.workspace.v1"

// panelID 是这一页的稳定标识：切页与缓存都用它。
const panelID = "workspace"

// panelChangedEvent 是"我这一页变了，重新画一遍"的通知事件。
const panelChangedEvent = "ui.panel.changed"

// panelCtx 是这一页需要的那几样能力。
//
// 抽成接口只为一件事：单测里能换成假的 —— 界面逻辑（点了什么、画成什么样）
// 不该为了测一下就去连真磁盘。生产上注入的就是 plugin.Context。
type panelCtx interface {
	FSList(path string) (json.RawMessage, error)
	SetConfig(key string, value any) error
	Emit(event string, payload any) error
	Log(level, msg string, meta map[string]any)
}

// panelRequest 就是线协议里的请求参数（定义在 SDK 里，两边共用一份）。
type panelRequest = plugin.PanelRequest

// wsPanel 是这一页的全部界面状态。
//
// 它和工作区本身的状态（root）分开：输入框里的是**正在编辑的草稿**，
// 没点「设为工作区」之前不该影响任何实际行为。
type wsPanel struct {
	field plugin.Field // 路径输入框
	focus bool         // 输入框是否聚焦：聚焦时键盘进输入框，否则上下键选条目
	sel   int          // 选中的条目
	items []item       // 当前列出的条目
	path  string       // 正在浏览的目录
	up    string       // 上一级
	note  string       // 底部的一句反馈
	ok    bool         // 上面那句是好消息还是坏消息

	// loaded 表示"已经读过一次目录"。首次渲染时才读：不显示这一页的会话
	// 不该为它付一次目录读取的代价。
	loaded bool
}

// list 回答"我有哪几页"。
func (p *wsPanel) list() []plugin.PanelInfo {
	return []plugin.PanelInfo{{
		ID:    panelID,
		Title: "工作区",
		Order: 20,
	}}
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

// render 画这一页。w×h 是宿主给的可用空间，由它说了算 ——
// 插件不能假设自己有多大地方。
//
// 这里**不做任何慢操作**：目录已经在 reload 里读好了。渲染是要按帧回答的，
// 顺手读一次盘等于把宿主的每一帧都拖住。
func (p *wsPanel) render(w *Workspace, req panelRequest) plugin.PanelRender {
	width, height := req.W, req.H
	if width < 1 || height < 1 {
		return plugin.PanelRender{}
	}
	if !p.loaded {
		p.loaded = true
		_ = p.reload(w, p.field.Value)
	}
	if !p.focus && p.field.Value == "" {
		p.focus = true // 一进来就能直接打字，不用先点一下
	}

	b := plugin.NewBlock(width, height)
	if width < 20 || height < 6 {
		b.At(0).Text(plugin.SWarn, " 窗口太小，放不下这一页")
		return b.Render(0, 0, "把窗口拉大些")
	}

	// ① 标题：一眼看清现在挂在哪个目录上
	const title = " 工作区文件"
	b.At(0).Text(plugin.SAccent+plugin.SBold, title)
	cur := "当前：" + firstNonEmpty(w.root, "（未指定）")
	b.At(0).Text(plugin.SDim, "  "+plugin.Truncate(plugin.OneLine(cur), maxInt(0, width-plugin.DisplayWidth(title)-3)))

	// ② 路径输入框：点哪一列就把光标放到哪一列
	const label = " 路径 "
	lw := plugin.DisplayWidth(label)
	inner := width - lw - 4
	if inner < 8 {
		inner = 8
	}
	b.At(1).Text(plugin.SDim, label)
	border := plugin.Style(plugin.SBorder)
	if p.focus {
		border = plugin.Style(plugin.SAccent + plugin.SBold)
	}
	b.At(1).Text(border, "[")
	used := p.field.Draw(b.At(1), inner, p.focus, plugin.Style(plugin.SAccent2+plugin.SBold))
	if used < inner {
		b.At(1).Text("", strings.Repeat(" ", inner-used))
	}
	b.At(1).Text(border, "]")
	for col := 0; col < inner; col++ {
		b.Hit(1, lw+1+col, 1, "caret", strconv.Itoa(col))
	}
	b.Hit(1, lw, inner+2, "focus", "")

	// ③ 操作按钮
	cx := b.Button(2, 1, "设为工作区", "setroot", "", true)
	cx = b.Button(2, cx, "刷新", "refresh", "", false)
	if p.up != "" {
		b.Button(2, cx, "向上一级", "up", "", false)
	}

	// ④ 分隔 + 目录标题
	b.At(3).Fill(plugin.SBorder, "─")
	head := " 目录 " + plugin.Truncate(firstNonEmpty(p.path, "（还没有读取）"), maxInt(6, width-20))
	b.At(4).Text(plugin.SDim+plugin.SBold, head)
	b.At(4).Text(plugin.SDim, plugin.Truncate("  "+strconv.Itoa(len(p.items))+" 项",
		maxInt(0, width-plugin.DisplayWidth(head)-3)))

	// ⑤ 条目区（可滚动）：窗口高够的话，底部留一行放反馈
	bottom := height
	if height >= 9 {
		bottom = height - 1
	}
	rows := bottom - 5
	maxScroll := 0
	if n := len(p.items) - rows; n > 0 {
		maxScroll = n
	}
	scroll := req.Scroll
	if scroll < 0 {
		scroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if rows > 0 {
		// 选中项必须看得见：上下键移动时，列表自己跟着走
		if p.sel >= 0 && p.sel < len(p.items) {
			scroll = plugin.Window(p.sel, scroll, rows, len(p.items))
		}
		for i := 0; i < rows && scroll+i < len(p.items); i++ {
			drawEntry(b, 5+i, width, p.items[scroll+i], scroll+i, scroll+i == p.sel)
		}
		if len(p.items) == 0 {
			b.At(5).Text(plugin.SDim, " （这个目录是空的）")
		}
	}

	// ⑥ 底部反馈
	if bottom < height && p.note != "" {
		st := plugin.Style(plugin.SDim)
		if !p.ok {
			st = plugin.Style(plugin.SBad)
		}
		b.At(bottom).Text(st, " "+plugin.Truncate(p.note, maxInt(0, width-2)))
	}

	res := b.Render(scroll, maxScroll,
		"点路径可直接改 · 点目录进入 · 回车设为工作区 · Tab 切页")
	res.WantKeys = true
	res.WantCopy = true // 有路径输入框：Ctrl+C 交给这一页复制
	return res
}

// drawEntry 画一个条目：目录用 ▸ 打头，文件带大小。
//
// 热区的参数是**条目下标**而不是屏幕行号：屏幕行随滚动变，下标不会 ——
// 用行号的话，滚一下再点就会点错人。
func drawEntry(b *plugin.Block, y, width int, it item, idx int, selected bool) {
	mark, style := " · ", plugin.Style(plugin.SDim)
	if it.Dir {
		mark, style = " ▸ ", plugin.Style(plugin.SAccent)
	}
	if selected {
		style = plugin.Style(plugin.SSel + plugin.SAccent + plugin.SBold)
	}
	b.At(y).Text(style, mark)

	size := ""
	if !it.Dir {
		size = humanBytes(it.Size)
	}
	nameW := width - 5 - plugin.DisplayWidth(size) - 2
	if nameW < 6 {
		nameW = 6
	}
	b.At(y).Text(style, plugin.Pad(plugin.Truncate(it.Name, nameW), nameW))
	b.At(y).Text(plugin.SDim, " "+plugin.Truncate(size, 9))

	action := "pick"
	if it.Dir {
		action = "open"
	}
	b.Hit(y, 0, width, action, strconv.Itoa(idx))
}

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------

// action 处理一次点击，返回要显示给用户的一句话。
func (p *wsPanel) action(w *Workspace, req panelRequest) string {
	switch req.Action {
	case "focus":
		p.focus = true
		p.say(w, "", true)
		return ""

	case "caret":
		col, _ := strconv.Atoi(req.Arg)
		p.focus = true
		p.field.SetCursor(p.field.CaretAtCol(col))
		p.field.SelStart(p.field.Cur()) // 按下：落点也是拖选锚点
		p.say(w, "", true)
		return ""

	case "sel":
		// 按住拖动：宿主把拖到的列号转过来，选区从锚点扩过去
		col, _ := strconv.Atoi(req.Arg)
		p.field.SelExtend(p.field.CaretAtCol(col))
		if p.field.HasSel() {
			p.say(w, fmt.Sprintf("已选中 %d 个字符：Ctrl+C 复制，Esc 取消", len([]rune(p.field.Selection()))), true)
		}
		return ""

	case "pick", "open":
		idx, _ := strconv.Atoi(req.Arg)
		if idx < 0 || idx >= len(p.items) {
			return ""
		}
		it := p.items[idx]
		p.sel = idx
		if req.Action == "open" && it.Dir {
			return p.jump(w, filepath.Join(p.path, it.Name))
		}
		p.say(w, "", true)
		return "已选中 " + it.Name

	case "setroot":
		return p.setRoot(w)

	case "refresh":
		return p.jump(w, p.field.Value)

	case "up":
		if p.up == "" {
			return "已经到最上一级了"
		}
		return p.jump(w, p.up)
	}
	return ""
}

// key 处理一次按键。
//
// Tab（切页）由宿主扣下 —— 面板拿不到它，否则用户会被困在这一页里出不去。
// Ctrl+C 由宿主在页面声明 WantCopy 时转过来：只复制，退出是 Ctrl+Q。
func (p *wsPanel) key(w *Workspace, kind, text string) (string, bool) {
	if kind == "ctrl+c" {
		// 只复制。没选中返回空，退出是 Ctrl+Q（宿主管），不在这里。
		return p.field.Selection(), false
	}
	switch kind {
	case "rune":
		if !p.focus {
			return "", false
		}
		for _, r := range text {
			p.field.Insert(r)
		}
		p.say(w, "", true)

	case "backspace":
		if p.focus {
			p.field.Backspace()
			p.say(w, "", true)
		}
	case "delete":
		if p.focus {
			p.field.Delete()
			p.say(w, "", true)
		}
	case "left":
		if p.focus {
			p.field.Left()
			p.say(w, "", true)
		}
	case "right":
		if p.focus {
			p.field.Right()
			p.say(w, "", true)
		}
	case "home":
		if p.focus {
			p.field.Home()
			p.say(w, "", true)
		}
	case "end":
		if p.focus {
			p.field.End()
			p.say(w, "", true)
		}

	case "up":
		if len(p.items) > 0 && p.sel > 0 {
			p.sel--
			p.say(w, "", true)
		}
	case "down":
		if len(p.items) > 0 && p.sel < len(p.items)-1 {
			p.sel++
			p.say(w, "", true)
		}

	case "enter":
		// 回车 = 把输入框里的路径设为工作区（填完不用再去点按钮）
		_ = p.setRoot(w)

	case "esc":
		p.focus = false
		p.say(w, "", true)
	}
	return "", false
}

// ---------------------------------------------------------------------------
// 状态
// ---------------------------------------------------------------------------

// setRoot 把输入框里的路径设为工作目录。
func (p *wsPanel) setRoot(w *Workspace) string {
	path := strings.TrimSpace(p.field.Value)
	if path == "" {
		return p.say(w, "先填一个路径，或在下面的目录里点进去挑一个", false)
	}
	if _, err := w.setRootPath(path); err != nil {
		return p.say(w, err.Error(), false)
	}
	p.field.SetValue(w.root)
	return p.say(w, "工作区已设为 "+w.root+"（重启后仍然记得）", true)
}

// jump 切到某个目录去浏览。
func (p *wsPanel) jump(w *Workspace, path string) string {
	if err := p.reload(w, path); err != nil {
		return p.say(w, err.Error(), false)
	}
	return p.say(w, "已读取 "+p.path, true)
}

// reload 读一次目录，把结果放进面板状态。
func (p *wsPanel) reload(w *Workspace, path string) error {
	ls, err := w.browseInto(path)
	if err != nil {
		if p.path == "" {
			// 第一次就读不到（比如还没设置工作目录）：留空列表，把原因摆到反馈行
			p.items, p.up = nil, ""
			p.note, p.ok = err.Error(), false
		}
		return err
	}
	p.path, p.up, p.items = ls.Path, ls.Parent, ls.Entries
	p.field.SetValue(ls.Path)
	p.sel, p.loaded = 0, true
	return nil
}

// say 记一句反馈并通知宿主重画。
//
// 通知是必须的：宿主只在收到它（或换了尺寸、滚动位置）时才重新要一帧，
// 少了它，界面就会停在旧内容上 —— "点了没反应"就是这么来的。
func (p *wsPanel) say(w *Workspace, note string, ok bool) string {
	p.note, p.ok = note, ok
	p.changed(w)
	return note
}

// changed 通知宿主"这一页变了，重新画一遍"。
//
// 带上服务名：宿主据此精确定位到"哪一页"（只报页面 id 也行，但两个插件
// 页面重名时会多刷一页）。
func (p *wsPanel) changed(w *Workspace) {
	if w == nil || w.api == nil {
		return
	}
	_ = w.api.Emit(panelChangedEvent, map[string]any{
		"service": panelService,
		"panel":   panelID,
	})
}

// ---------------------------------------------------------------------------
// 与宿主对接
// ---------------------------------------------------------------------------

// listing 是一次目录浏览的结果。
type listing struct {
	Path    string
	Parent  string
	Entries []item
}

// browseInto 列出一个目录，返回结构化结果（面板内部用，不走 JSON）。
func (w *Workspace) browseInto(path string) (listing, error) {
	target := firstNonEmpty(path, ".")
	raw, err := w.api.FSList(target)
	if err != nil {
		return listing{}, err
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return listing{}, fmt.Errorf("解析目录列表失败: %w", err)
	}
	items, err := w.listOne(out.Path)
	if err != nil {
		return listing{}, err
	}
	ls := listing{Path: out.Path, Entries: items}
	if out.Path != "" {
		if up := filepath.Dir(out.Path); up != out.Path {
			ls.Parent = up
		}
	}
	return ls, nil
}

// handlePanel 是 ui.panel.v1 的分发入口。
func (w *Workspace) handlePanel(method string, args json.RawMessage) (any, error) {
	switch method {
	case "list":
		return map[string]any{"panels": w.panel.list(), "note": "工作区"}, nil
	case "render":
		var in panelRequest
		_ = json.Unmarshal(args, &in)
		if in.Panel != "" && in.Panel != panelID {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "工作区没有页面 %s", in.Panel)
		}
		return w.panel.render(w, in), nil
	case "action":
		var in panelRequest
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
		}
		return map[string]any{"note": w.panel.action(w, in)}, nil
	case "key":
		var in panelRequest
		_ = json.Unmarshal(args, &in)
		copyText, exit := w.panel.key(w, in.Kind, in.Text)
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

// humanBytes 把字节数说成一眼能读的大小。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
