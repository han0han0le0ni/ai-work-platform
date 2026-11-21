package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// 状态
// ---------------------------------------------------------------------------

type tabKind int

// 内置页现在只剩「聊天」这一页 —— 对话是界面存在的理由，不属于任何插件。
// 其余的一律由插件自带（tabPanel）—— 装一个带界面的插件就多一页，
// 这个文件一行都不用改。
const (
	tabChat tabKind = iota
	tabPanel
)

var allTabs = []tabKind{tabChat}

func (t tabKind) key() string {
	if t == tabPanel {
		return "panel"
	}
	return "chat"
}

func (t tabKind) label() string {
	if t == tabPanel {
		// 插件页的标签名由插件自己给（见 renderTabs），这里只是兜底
		return "插件页"
	}
	return "聊天"
}

// msgView 是渲染一条消息需要的全部信息。
//
// 它是 conversation.v1 的 nodeView 在界面侧的样子。id 为空表示这条消息不在会话树里
// （系统提示、以及没装对话插件时的线性聊天），这时不画版本翻页与操作按钮。
type msgView struct {
	id      string
	role    string // user / assistant / 系统 / 错误
	at      string
	text    string
	version int // 它是这组版本里的第几个（从 1 开始）
	total   int // 这组一共几个；<= 1 时不画翻页组
	via     string
	err     string
	trunc   bool
}

// sessionInfo 是会话列表里的一项。
type sessionInfo struct {
	id      string
	name    string
	nodes   int
	updated string
}

type serviceStatus struct {
	name     string
	alive    bool
	provider string
	note     string
}

// pluginInfo 是界面自己扫出来的一个插件摘要。
//
// 数据来自磁盘上的 manifest.json：核心没有"列出插件"的接口（规范 1.5 只有 6 个），
// 界面就像普通插件那样，经能力代理把 plugins/ 目录读出来。好处是**新装的插件会自动出现**。
//
// 界面拿它只做两件事：
//
//  1. 认出哪些插件提供了"自带页面"（ui.page.*），好把它们合成到标签栏里；
//  2. 说清楚侧栏里某个服务此刻是谁在提供。
//
// 插件本身的管理（清单、详情、启停、删除）是 pluginManager.v1 的事，
// 由它自带的那一页负责展示 —— 界面不再留第二份副本。所以那些"名字、版本、
// 简介、依赖、权限"之类的字段都随插件页搬走了，这里只留上面两件事用得上的。
type pluginInfo struct {
	ID       string
	Dir      string   // 目录名，例如 demo.plugin；被禁用时带 .disabled 后缀
	Provides []string // manifest 声明的服务
	Alive    []string // 声明里现在能发现到的服务
	ReadErr  string   // manifest 读不出来时的原因
}

func (p pluginInfo) running() bool { return len(p.Alive) > 0 }

type state struct {
	coreVersion string
	apiVersion  string
	pluginID    string

	tab   tabKind
	w, h  int
	color bool

	// 聊天（链式对话）
	msgs     []msgView
	sessions []sessionInfo
	sessID   string
	convOK   bool // conversation.v1 可用；不可用时退化成一条直线的聊天
	convBusy bool
	input    []rune
	cursor   int
	busy     bool
	spinner  int
	scroll   int

	// 聊天输入框的鼠标选择（rune 下标，前闭后开）。selOn 为 false 时 selA/selB 无效。
	selOn      bool
	selA, selB int

	// 正在进行的鼠标拖动（选文本）。nil 表示没在拖。
	drag *dragSel

	// trace 是「AI 在干什么」的过程记录：模型在想什么、调了哪些工具、卡在哪。
	//
	// 它由事件总线喂进来（见 activity.go），生成结束后**不**立刻清掉 ——
	// 留着让人回看，下一次发消息时才换新的。
	trace *traceView

	// 通用浮窗（ui.widget.*）：见 widget.go。每个浮窗一份状态，位置归用户。
	widgets    []widgetView
	widgetDrag *widgetDrag
	// widgetSave 把某个浮窗的位置落盘。界面线程不做 RPC，由插件层注入。
	widgetSave func(svc, id string, x, y int, fold bool)

	// 修改某条消息：输入行临时被这条消息占住，回车提交成一个新版本
	editing  string // 正在修改的消息 id；空串表示普通发送
	editPrev string // 进编辑态之前输入框里的内容，Esc 时还回去

	// 会话删除的二次确认（与插件删除分开：两者可以同时出现在不同页）
	confirmSessDel bool

	// 分享面板：聊天页的输入行临时让给它
	shareOpen bool

	// 附件：随消息一起发给 AI 的文件 / 图片
	attachOpen    bool   // 附件面板是否占住聊天区
	attachPath    string // 正在浏览的目录
	attachParent  string
	attachEntries []dirEntry
	attachSel     int
	attachScroll  int
	attachBusy    bool
	attachments   []attachment

	// 图片缩略图缓存：路径 -> 渲染好的行。
	// 渲染每帧同步跑，而读图要走核心（RPC），所以只能"先画占位、读完再补"。
	thumbs      map[string]*thumbEntry
	thumbWanted []string // 这一帧遇到、还没加载过的路径，由主循环负责去读

	// 聊天页底部的一句话反馈（复制到剪贴板 / 导出结果 / 切换会话…）
	notice   string
	noticeOK bool

	// 侧栏 / 插件页
	services []serviceStatus
	// plugins 是扫出来的插件清单。界面只用它做两件事：认出 ui.page.* 的提供者、
	// 说清侧栏某个服务是谁在提供（见 pluginInfo）。
	plugins []pluginInfo

	// droppedPlugins 是本次运行里"已经被删掉"的插件（按 ID 与目录名记）。
	//
	// 删除是**重启生效**的：正在运行的插件目录被占用、删不掉，只是标了"待重启
	// 删除"，目录此刻还在。所以光重扫没用 —— 扫描还是会看到它。界面得记住
	// "它被删了"，重扫结果里把它的条目滤掉，否则它的页面会一直赖在标签栏上。
	droppedPlugins map[string]bool

	// focus 现在只剩一个用途：标记键盘焦点在聊天输入框上（"chat"）。
	focus    string
	status   string
	statusOK bool

	// 插件自带的页面（ui.page.*）：见 panel.go。
	//
	// 这几项是"借地方"这件事本身的账：页面清单、当前看的是哪一页、
	// 拿回来的帧缓存、这一帧登记的热区。至于页面上有什么，宿主一概不知道。
	pages     []pagePanel
	pagesBusy bool
	// pagesSeen 是"上一次扫目录时，哪些服务声称自己带页面"。
	//
	// 页面清单只在**这组服务变过**时才重新去问。以前是"清单为空才补问"
	// （为了接住启动竞态），那样删掉一个插件之后它的页面会永远留在标签栏上：
	// 清单非空，就再也没人去问第二次。
	pagesSeen    []string
	pageSvc      string // 当前这一页是谁提供的
	pageID       string
	pageTitle    string
	pageScroll   int
	pageMax      int
	pageNote     string
	pageWantKeys bool
	// pageWantCopy 表示当前这一页声明了"有可选的文本"，Ctrl+C 交它复制。
	pageWantCopy bool

	pageCache   map[string]*pageRender
	pagePending map[string]bool
	pageWanted  []pageReq
	pageHits    []pageHitRef

	events []string
}

// dragSel 是一次"按住鼠标拖选"的中间状态。
//
// 起点按下时就记下：拖到哪里都以它为基准算选区，鼠标甩出输入框再回来也不会乱。
// target 是 "chat"（聊天输入框）、字段 key（旧接口），或 "panel"（插件页面）。
// 前两者用 fx/anchor 自己换算列；"panel" 不换算 —— 插件为字段的每一列都登记了
// caret 热区，拖到哪个热区就把哪个热区的列号转过去。
type dragSel struct {
	target string
	anchor int
	fx     int

	// panel 拖选用：哪一页的哪个字段
	svc   string
	id    string
	field string
}

// dirEntry 是附件面板里的一行：一个文件或目录。
type dirEntry struct {
	Name string
	Dir  bool
	Size int64
}

// attachment 是随一条消息一起发出的附件。
type attachment struct {
	Type    string `json:"type"`              // text / image / file
	Name    string `json:"name"`              // 文件名
	Path    string `json:"path"`              // 完整路径
	Text    string `json:"text,omitempty"`    // type=text：已读出的内容
	DataURL string `json:"dataUrl,omitempty"` // type=image：data:image/png;base64,xxx
	Size    int64  `json:"size"`              // 原始字节数
}

// kind 按扩展名猜附件的处理方式。
//
// 猜不准就当普通文件（只带元数据）—— 把二进制当文本读出来只会得到一堆乱码，
// 与其让模型看乱码，不如如实告诉它"这是个文件，我看不了内容"。
func (a attachment) kind() string {
	switch strings.ToLower(filepath.Ext(a.Name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return "image"
	case ".txt", ".md", ".go", ".py", ".java", ".c", ".h", ".json", ".yaml", ".yml",
		".toml", ".csv", ".log", ".xml", ".html", ".css", ".js", ".ts", ".sh":
		return "text"
	default:
		return "file"
	}
}

// ---------------------------------------------------------------------------
// 整帧
// ---------------------------------------------------------------------------

// sidebarW 是右侧栏宽度；低于 sidebarMinW 就不再分栏。
const (
	sidebarW    = 34
	sidebarMinW = 96
)

// restartMinW 是标签栏上出现「重启」按钮所需的最小宽度。
//
// 按钮连间隔要占 8 列，右侧还有一眼可见的汇总。窄屏上标签加汇总已经排满一行，
// 宁可不画这个按钮，也不把汇总挤掉 —— 汇总比"重启"常用得多。
const restartMinW = 56

var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// roleStyle 把角色翻成颜色与两个字。
//
// 两种写法都要认：conversation.v1 返回的是 user / assistant，
// 本地提示与降级聊天用的是中文角色名。
func roleStyle(role string) (code, label string) {
	switch role {
	case "user", "你":
		return cAccent, "你"
	case "assistant", "AI":
		return cAccent2, "AI"
	case "错误":
		return cBad, "错误"
	default:
		return cDim, "系统"
	}
}

// renderFrame 渲染整屏，并把可点击区域一并登记进画布。
func renderFrame(st *state, w, h int, color bool) *canvas {
	c := newCanvas(w, h, color)
	if w < 44 || h < 12 {
		c.row(func(l *lineBuilder, y int) {
			l.styled(cBad, fmt.Sprintf(" 窗口太小（当前 %d×%d，至少 44×12）", w, h))
		})
		return c
	}

	c.rows = append(c.rows, borderTop(w, "AI 工作平台 · 极光", titleRight(st), color))
	renderTabs(c, st)

	// 每帧重算：这一帧"想要哪些页的帧"、登记了哪些热区。
	// 渲染是同步的，不能在里面发 RPC（见 panel.go 的 pumpPages）。
	st.pageWanted = nil
	st.pageHits = nil

	// 输入框会随内容长高（多行输入），版面得先给它让位。
	// 用 buildInputBox 算而不是自己数，是为了和渲染、光标三处永远一致。
	bodyH := h - 4 - buildInputBox(st, w, h).rows
	if bodyH < bodyMinH {
		bodyH = bodyMinH
	}

	// 插件自带的一页自己排版，占满整块正文区 —— 别让宿主的分栏去猜它想要什么。
	full := st.tab == tabPanel

	// 够宽就分两栏。
	two := w >= sidebarMinW && !full
	leftW, rightW := w-2, 0
	if two {
		rightW = sidebarW
		leftW = w - 3 - rightW
	}

	var leftRows, rightRows []rowFn
	switch st.tab {
	case tabPanel:
		leftRows = pageRows(st, leftW, bodyH)
	default:
		if st.attachOpen {
			// 附件面板借聊天区的两栏：左边挑文件，右边看已选
			leftRows = attachBrowseRows(st, leftW, bodyH)
			rightRows = attachSelectedRows(st, rightW, bodyH)
		} else {
			leftRows = chatRows(st, leftW, bodyH)
			if two {
				rightRows = sideRows(st, rightW, bodyH)
			}
		}
	}

	for i := 0; i < bodyH; i++ {
		lr, rr := at(leftRows, i), at(rightRows, i)
		if two {
			c.twoCol(leftW, rightW, lr, rr)
		} else {
			c.oneCol(lr)
		}
	}

	c.rows = append(c.rows, borderSplit(w, leftW, rightW, color))
	renderFooter(c, st)
	c.rows = append(c.rows, borderBottom(w, hintFor(st), color))

	// 浮窗最后画：它盖在版面之上，所以最后一步才贴（见 widget.go）
	drawWidgets(c, st)
	return c
}

func at(rows []rowFn, i int) rowFn {
	if i < 0 || i >= len(rows) {
		return nil
	}
	return rows[i]
}

func titleRight(st *state) string {
	if st.coreVersion == "" {
		return st.pluginID
	}
	return "core v" + st.coreVersion + " · " + st.pluginID
}

func hintFor(st *state) string {
	switch st.tab {
	case tabPanel:
		// 这一行由插件自己给（render 的 note）：它比谁都清楚自己这一页怎么用。
		// 插件没给就退回一句通用说明，而不是留空 —— 空行看起来像界面坏了。
		if st.pageNote != "" {
			return st.pageNote
		}
		return "这一页由「" + pageRefName(st) + "」插件自己绘制 · Tab 切页 · Ctrl+C 复制 · Ctrl+Q 退出"
	default:
		// 底部这一行优先说刚发生过什么：复制、导出、失败原因……
		// 那些结果没别的地方能显示，而这一行本来就空着。
		if st.notice != "" {
			return st.notice
		}
		if st.attachOpen {
			return "点文件名加入附件 · 点目录进去 · ↑↓ 选择 · 回车加选中项 · Esc 关闭 · 关掉后点「发送」送出"
		}
		if st.editing != "" {
			return "回车换行 · 点「提交」存为新版本（旧版本与它后面那条时间线都保留）· Esc 放弃修改"
		}
		return "回车换行 · 点「发送」送出（Ctrl+Enter 同）· 点消息框上的「修改 / 重新生成 / 分享」· 滚轮翻历史 · Tab 切页"
	}
}

// ---------------------------------------------------------------------------
// 标签栏
// ---------------------------------------------------------------------------

// tabTarget 是标签栏上的一个标签：内置的一页，或者插件自带的一页。
type tabTarget struct {
	kind   tabKind
	svc    string // kind == tabPanel 时：提供这一页的服务
	panel  string // kind == tabPanel 时：页面 id
	label  string
	action string // 点击动作
}

// matches 判断这个标签是不是"现在正看着的那一页"。
func (t tabTarget) matches(st *state) bool {
	if st.tab != t.kind {
		return false
	}
	if t.kind != tabPanel {
		return true
	}
	return st.pageSvc == t.svc && st.pageID == t.panel
}

// allTabsOf 返回当前这一屏有哪些标签：内置几页 + 插件报上来的每一页。
//
// 这是"装一个插件就多一页"的落点：新页面从哪来、叫什么，界面一概不问，
// 清单里有什么就画什么。
func allTabsOf(st *state) []tabTarget {
	out := make([]tabTarget, 0, len(allTabs)+len(st.pages))
	for _, t := range allTabs {
		out = append(out, tabTarget{kind: t, label: t.label(), action: "tab:" + t.key()})
	}
	for _, p := range st.pages {
		label := firstNonEmpty(p.title, p.id)
		if st.tab == tabPanel && st.pageSvc == p.svc && st.pageID == p.id && st.pageTitle != "" {
			label = st.pageTitle // 插件可以随状态改标签（比如带上条数）
		}
		out = append(out, tabTarget{
			kind: tabPanel, svc: p.svc, panel: p.id,
			// 名字太长会把标签栏撑爆，插件给多长都得先夹一刀
			label:  truncDisplay(label, 8),
			action: "tab:page:" + p.svc + "/" + p.id,
		})
	}
	return out
}

func renderTabs(c *canvas, st *state) {
	c.row(func(l *lineBuilder, y int) {
		l.styled(cBorder, "│")
		x := 1
		for _, t := range allTabsOf(st) {
			label, style := " "+t.label+" ", cDim
			if t.matches(st) {
				style = cTabSel
			}
			wid := dispWidth(label)
			// 放不下就不画：画到框外的标签热区还在、人却看不见，点上去"时灵时不灵"。
			// 插件给的名字长短不一，所以这一道必须挡在这儿。
			if x+wid+2 > c.w-1 {
				break
			}
			l.styled(style, label)
			c.box(t.action, x, y, wid)
			x += wid
			l.styled("", "  ")
			x += 2
		}

		// 「重启」：请核心重新加载所有插件（插件的启停 / 删除都是重启后生效）。
		//
		// 放在标签栏，是因为它属于整个界面，不属于任何一页。
		// 窗口太窄就不画：标签加上右侧汇总已经占满一行，
		// 为了让出按钮而挤掉汇总不值得 —— 宁可少一个按钮。
		if c.w >= restartMinW {
			x = c.button(l, y, x, "core:restart", "重启", false)
		}

		// 右侧放一眼可见的汇总
		summary := st.summary()
		if summary != "" && dispWidth(summary)+x+2 < c.w {
			l.pad(c.w - 1 - dispWidth(summary) - 1)
			l.styled(cDim, summary)
			l.styled("", " ")
		}
		l.pad(c.w - 1)
		l.styled(cBorder, "│")
	})
}

func (st *state) summary() string {
	switch st.tab {
	case tabPanel:
		return "插件页面 · " + truncDisplay(pageRefName(st), 20)
	default:
		if st.convOK {
			return fmt.Sprintf("消息 %d · 会话 %d", countRealMsgs(st), len(st.sessions))
		}
		return fmt.Sprintf("服务 %d/%d", countAlive(st.services), len(st.services))
	}
}

// countRealMsgs 数的是会话树里的消息，不含本地系统提示 —— 那个数字要和侧栏对得上。
func countRealMsgs(st *state) int {
	n := 0
	for _, m := range st.msgs {
		if m.id != "" {
			n++
		}
	}
	return n
}

func countAlive(ss []serviceStatus) int {
	n := 0
	for _, s := range ss {
		if s.alive {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// 聊天页
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 聊天页：每条消息一个框
//
//	┌─ 你 · 15:03:12 ─────────────────────┐
//	│ 帮我看下 fs-tool 的权限声明          │
//	│ [修改] [重新生成] [分享]     ‹ 2/3 › │
//	└──────────────────────────────────────┘
//
// 同一个父节点下的多个版本互为「兄弟」：框下角的 ‹ n/n › 在它们之间翻页。
// 翻到哪个版本，就从哪个版本往后接着走 —— 后面的消息因此翻页隐没，
// 但旧的那一整条时间线一个节点都没删，翻回去就原样重现。
// ---------------------------------------------------------------------------

func chatRows(st *state, w, bodyH int) []rowFn {
	bw := w - 2
	if bw < 24 {
		bw = 24
	}

	var all []rowFn
	if st.convOK && len(st.msgs) == 0 && !st.busy {
		all = append(all, textRow(plainCell(""), w))
		all = append(all, textRow(newCell(segment{cDim, "  这条会话还是空的"}), w))
		all = append(all, textRow(newCell(segment{cDim, truncDisplay(
			"  在下面输入即可开始；右侧「新建」可以另开一条会话", w-2)}), w))
	}
	for i, m := range st.msgs {
		if i > 0 {
			all = append(all, textRow(plainCell(""), w))
		}
		all = append(all, msgBoxRows(m, bw)...)
		// 消息里提到的图片直接画在它下面
		all = append(all, msgImageRows(st, m, w)...)
	}
	// 等回复时占住 AI 下一条消息的位置：有过程就画过程，
	// 一个事件都还没到（或压根没装 agent）时退回原来的转圈框。
	if st.busy || st.trace != nil {
		all = append(all, textRow(plainCell(""), w))
		if rows := traceRows(st, bw); len(rows) > 0 {
			all = append(all, rows...)
		} else {
			all = append(all, msgBusyRows(st, bw)...)
		}
	}

	// 贴底显示，滚轮向上翻历史
	start := len(all) - bodyH - st.scroll
	if start < 0 {
		start = 0
	}
	if start > len(all) {
		start = len(all)
	}
	rows := append([]rowFn{}, all[start:]...)
	if len(rows) > bodyH {
		rows = rows[:bodyH]
	}

	// 底部对齐：不足时在顶部补空行
	pad := make([]rowFn, 0, bodyH)
	for i := len(rows); i < bodyH; i++ {
		pad = append(pad, textRow(plainCell(""), w))
	}
	return append(pad, rows...)
}

// thumbEntry 是一张图渲染好的样子。
type thumbEntry struct {
	lines  []string // nil 且 failed=false 表示还在读
	failed bool
}

// msgImageRows 把消息里提到的图片画在消息下面。
//
// 这是"AI 发图"真正的落点：模型只要在回答里写出保存路径
// （"已保存到 E:\AI资料\cat.png"），这里就能把那张图直接画出来 ——
// 不需要模型配合任何特殊格式，它本来就会说文件在哪。
func msgImageRows(st *state, m msgView, w int) []rowFn {
	// 用户消息里的 [附件: xxx] 只有文件名、没有路径，认不出就认不出，
	// 宁可少画一张，也不能去猜一个可能不存在的路径。
	paths := findImagePaths(m.text, 2)
	if len(paths) == 0 {
		return nil
	}
	if st.thumbs == nil {
		st.thumbs = map[string]*thumbEntry{}
	}

	var out []rowFn
	for _, p := range paths {
		e, ok := st.thumbs[p]
		if !ok {
			e = &thumbEntry{}
			st.thumbs[p] = e
			if !isVectorPath(p) {
				// 矢量图不必去读：标准库解不了 SVG，读了也只是白跑一次 RPC
				st.thumbWanted = append(st.thumbWanted, p)
			}
		}
		out = append(out, textRow(plainCell(""), w))
		out = append(out, imgCardRows(st, p, e, w)...)
	}
	return out
}

// imgCardRows 画一张图片卡片：文件名 + 「打开」按钮 + 缩略图。
func imgCardRows(st *state, path string, e *thumbEntry, w int) []rowFn {
	var out []rowFn

	// 标题行：图标 + 文件名 + 「打开」（点了用系统看图程序打开）
	out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
		l.styled(cAccent, "  ▣ ")
		nameW := w - 14
		if nameW < 8 {
			nameW = 8
		}
		l.styled(cDim, truncDisplay(filepath.Base(path), nameW))
		cx := x + l.width + 1
		if cx+8 < w {
			_ = c.button(l, y, cx, "openimg:"+path, "打开", true)
		}
		l.pad(w)
	})

	// 路径行（可点：点路径也是打开）
	out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
		l.styled(cDim, "  "+truncDisplay(path, w-4))
		l.pad(w)
		c.box("openimg:"+path, x, y, w)
	})

	// 矢量图：能显示卡片、能点开，但不画缩略图
	if isVectorPath(path) {
		out = append(out, textRow(newCell(segment{cDim,
			"  矢量图（SVG）· 点「打开」用浏览器查看，放大不糊"}), w))
		return out
	}
	if e.failed {
		out = append(out, textRow(newCell(segment{cBad, "  （这张图读不出来：可能已被删除，或不是图片）"}), w))
		return out
	}
	if e.lines == nil {
		out = append(out, textRow(newCell(segment{cDim, "  正在读图…"}), w))
		return out
	}
	// 缩略图：每行都是一段自带转义的彩色块，宽度由我们明说（见 lineBuilder.raw）
	for _, imgLine := range e.lines {
		line := imgLine
		out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
			l.styled("", "  ")
			l.raw(line, thumbCols)
			l.pad(w)
		})
	}
	return out
}

// msgBoxRows 把一个消息框展开成若干行。
func msgBoxRows(m msgView, bw int) []rowFn {
	inner := msgInner(bw)
	rows := []rowFn{msgTopRow(m, bw)}

	style := ""
	if m.role == "系统" || m.role == "错误" {
		style = cDim
	}
	for _, ln := range wrapText(m.text, inner) {
		rows = append(rows, msgTextRow(ln, bw, style))
	}
	// 生成失败也是一条可以「重新生成」的消息，把原因摊在框里而不是塞进状态栏
	if m.err != "" {
		for _, ln := range wrapText("生成失败："+m.err, inner) {
			rows = append(rows, msgTextRow(ln, bw, cBad))
		}
	}
	if m.trunc {
		rows = append(rows, msgTextRow("（内容过长，已按上限截断）", bw, cDim))
	}
	if m.id != "" {
		rows = append(rows, msgActionRow(m, bw))
	}
	return append(rows, msgBottomRow(bw))
}

// msgInner 返回框内可用的文本宽度。
//
// 布局：空格 + │ + 空格 + 文本 + 空格 + │，合计恰好 bw 列。
func msgInner(bw int) int {
	inner := bw - 5
	if inner < 4 {
		inner = 4
	}
	return inner
}

// msgTopRow 画框顶：┌─ 角色 · 时刻 ────┐
func msgTopRow(m msgView, bw int) rowFn {
	code, label := roleStyle(m.role)

	var parts []string
	if at := shortClock(m.at); at != "" {
		parts = append(parts, at)
	}
	if m.via != "" {
		parts = append(parts, m.via)
	}
	tail := ""
	if len(parts) > 0 {
		tail = " · " + strings.Join(parts, " · ")
	}
	// 留够「─┐」和至少一个填充横线，剩下的才是标题尾巴的位置
	tail = truncDisplay(tail, maxInt(1, bw-9))

	return func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		l.styled(cBorder, "╭─ ")
		l.styled(code+cBold, label)
		l.styled(cDim, tail)
		used := dispWidth("╭─ ") + dispWidth(label) + dispWidth(tail)
		fill := bw - used - 3
		if fill < 1 {
			fill = 1
		}
		l.styled(cBorder, " "+strings.Repeat("─", fill)+"╮")
	}
}

// msgTextRow 画框内的一行文本。
func msgTextRow(text string, bw int, style string) rowFn {
	body := padDisplay(truncDisplay(text, msgInner(bw)), msgInner(bw))
	return func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		l.styled(cBorder, "│")
		l.styled("", " ")
		l.styled(style, body)
		l.styled("", " ")
		l.styled(cBorder, "│")
	}
}

// msgBottomRow 画框底。
func msgBottomRow(bw int) rowFn {
	return func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		l.styled(cBorder, "╰"+strings.Repeat("─", maxInt(1, bw-3))+"╯")
	}
}

// msgActionRow 是框内的操作行：左边三个按钮，右边版本翻页组。
//
// 按钮全部立即生效、不需要二次确认 —— 这三件事都不会毁掉任何东西：
// 修改和重新生成都是「加一个兄弟版本」，分享只是复制文本。
func msgActionRow(m msgView, bw int) rowFn {
	return func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		l.styled(cBorder, "│")
		l.styled("", " ")

		start := x + 3    // 文本区起点
		end := x + bw - 2 // 文本区右边界（不含）
		cx := start
		cx = c.button(l, y, cx, "msg:"+m.id+":edit", "修改", true)
		cx = c.button(l, y, cx, "msg:"+m.id+":regen", "重新生成", true)
		cx = c.button(l, y, cx, "msg:"+m.id+":share", "分享", true)
		cx -= 2 // button 尾部自带 2 列间隔，右对齐时不算它

		if m.total > 1 {
			pagerActionRow(c, l, m, y, end, cx)
		}

		l.pad(end)
		l.styled("", " ")
		l.styled(cBorder, "│")
	}
}

// pagerActionRow 把 ‹ n/n › 靠右画出来。放不下就不画 —— 挤成一团比不显示更糟。
func pagerActionRow(c *canvas, l *lineBuilder, m msgView, y, end, cx int) {
	label := fmt.Sprintf("%d/%d", m.version, m.total)
	pw := 1 + 1 + dispWidth(label) + 1 + 1 // ‹ + 空格 + n/n + 空格 + ›
	px := end - pw
	if px < cx+2 {
		return
	}
	l.pad(px)
	l.styled(cAccent+cBold, "‹")
	l.styled("", " ")
	c.box("page:"+m.id+":prev", px, y, 2)
	l.styled(cDim, label)
	l.styled("", " ")
	l.styled(cAccent+cBold, "›")
	c.box("page:"+m.id+":next", px+pw-2, y, 2)
}

// msgBusyRows 是「正在等回复」的占位框，占的就是 AI 下一条消息的位置。
func msgBusyRows(st *state, bw int) []rowFn {
	spin := string(spinnerFrames[st.spinner%len(spinnerFrames)])
	return []rowFn{
		msgTopRow(msgView{role: "assistant"}, bw),
		msgTextRow(spin+" 正在生成…", bw, cDim),
		msgBottomRow(bw),
	}
}

// shortClock 把时刻裁成 时:分:秒。
//
// conversation.v1 存的是 RFC3339（要跨重启读回来），本地提示用的是 15:04:05，
// 两种都得认。
func shortClock(at string) string {
	at = strings.TrimSpace(at)
	if len(at) >= 19 && at[4] == '-' && at[10] == 'T' {
		return at[11:19]
	}
	return at
}

// sideRows 是聊天页的右侧栏：会话（可点击切换）+ 服务链路 + 事件。
func sideRows(st *state, w, bodyH int) []rowFn {
	var out []rowFn
	if rows := sessRows(st, w); len(rows) > 0 {
		out = append(out, rows...)
		out = append(out, textRow(plainCell(""), w))
	}
	out = append(out, textRow(newCell(segment{cDim + cBold, " 服务链路"}), w))

	for _, s := range st.services {
		svc := s
		mark, style := "✗", cBad
		if svc.alive {
			mark, style = "✓", cOK
		}
		out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
			l.put(newCell(
				segment{style, " " + mark + " "},
				segment{"", truncDisplay(svc.name, w-5)},
			).fit(w))
			c.box("svc:"+svc.name, x, y, w)
		})
		out = append(out, textRow(newCell(segment{cDim, "    " + truncDisplay(
			firstNonEmpty(svc.provider, svc.note), w-5)}), w))
	}

	out = append(out, textRow(plainCell(""), w),
		textRow(newCell(segment{cDim + cBold, " 事件总线"}), w))
	if len(st.events) == 0 {
		out = append(out, textRow(newCell(segment{cDim, "  （暂无）"}), w))
	}
	for _, e := range st.events {
		out = append(out, textRow(newCell(segment{cDim, " " + truncDisplay(e, w-2)}), w))
	}
	return out
}

// maxSessRows 是侧栏里最多列出几条会话。
//
// 会话列表再长也不该把「服务链路」挤出屏幕 —— 那是这一页除聊天之外最有用的信息。
const maxSessRows = 5

// sessRows 是侧栏顶部的会话区：点名字切换，点「新建 / 删除」管理。
//
// 「分享整条」导出的是**当前时间线**：翻页隐没的旧版本按设计不进分享内容，
// 分享的永远是屏幕上看得见的那条对话。
func sessRows(st *state, w int) []rowFn {
	if !st.convOK {
		return nil
	}
	var out []rowFn
	out = append(out, textRow(newCell(segment{cDim + cBold, " 会话"}), w))

	shown := st.sessions
	if len(shown) > maxSessRows {
		shown = shown[len(shown)-maxSessRows:]
	}
	// 当前会话必须看得见：看不到自己正在哪条会话里，比列表顺序重要
	if st.sessID != "" && !sessShown(shown, st.sessID) {
		if cur, ok := sessByID(st.sessions, st.sessID); ok {
			shown = append(shown, cur)
		}
	}

	room := maxInt(4, w-11)
	for _, s := range shown {
		sess := s
		out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
			style, mark := "", " "
			if sess.id == st.sessID {
				style, mark = cAccent+cBold, "▸"
			}
			l.put(newCell(
				segment{style, " " + mark + " "},
				segment{style, padDisplay(truncDisplay(sess.name, room), room)},
				segment{cDim, fmt.Sprintf("%d", sess.nodes)},
			).fit(w))
			c.box("sess:"+sess.id, x, y, w)
		})
	}
	if extra := len(st.sessions) - len(shown); extra > 0 {
		out = append(out, textRow(newCell(segment{cDim,
			fmt.Sprintf("  （另有 %d 条会话）", extra)}), w))
	}

	out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		cx := c.button(l, y, x+1, "btn:sessnew", "新建", true)
		if st.confirmSessDel {
			cx = c.button(l, y, cx, "btn:sessdelok", "确认删除", true)
			_ = c.button(l, y, cx, "btn:sessdelcancel", "取消", false)
			return
		}
		_ = c.button(l, y, cx, "btn:sessdel", "删除", false)
	})
	out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		_ = c.button(l, y, x+1, "btn:seshare", "分享整条", false)
	})
	return out
}

func sessShown(list []sessionInfo, id string) bool {
	_, ok := sessByID(list, id)
	return ok
}

func sessByID(list []sessionInfo, id string) (sessionInfo, bool) {
	for _, s := range list {
		if s.id == id {
			return s, true
		}
	}
	return sessionInfo{}, false
}

// ---------------------------------------------------------------------------
// 插件页整块搬去了插件管理器自己身上（plugins/plugin-manager/panel.go）——
// 清单、详情、启停、删除都是它自己的事，界面不该再留一份副本。
// 界面这边只留"扫一遍插件目录"这一件事：认出自带页面的插件（ui.page.*），
// 以及说清侧栏里某个服务是谁在提供（见 pluginInfo）。
// ---------------------------------------------------------------------------

// 「文件」页整块搬去了 workspace 插件自己身上（见 plugins/workspace/panel.go）——
// 那一页是"挑工作目录"，属于工作区插件自己的事，界面不该替它画。
// 搬过去之后：改那一页不用碰这个插件，换个 UI 插件它也照样在。

// 「模型」页搬去了 llm、预算搬去了 agent（见那两个插件的 panel.go）。

// humanSize 把字节数说成一眼能读的大小。
func humanSize(n int64) string {
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

// ---------------------------------------------------------------------------
// 底栏与输入行
// ---------------------------------------------------------------------------

// promptFor 返回聊天输入行的提示符。
//
// 抽成函数是必须的：renderFooter 与 cursorPos 都要用它算光标位置，
// 两处各写一份的话，改一处漏一处光标就会偏 —— 而偏了看起来就是"输入框坏了"。
func promptFor(st *state) string {
	if st.editing != "" {
		return " 修改 › "
	}
	return " 你 › "
}

func promptStyle(st *state) string {
	if st.editing != "" {
		return cWarn + cBold
	}
	return cAccent + cBold
}

func renderFooter(c *canvas, st *state) {
	if st.tab != tabChat {
		c.row(func(l *lineBuilder, y int) {
			l.styled(cBorder, "│")
			style := cDim
			if st.status != "" {
				style = cOK
				if !st.statusOK {
					style = cBad
				}
			}
			l.styled(style, " "+truncDisplay(oneLine(st.status), c.w-3))
			l.pad(c.w - 1)
			l.styled(cBorder, "│")
		})
		return
	}

	// 分享面板临时占住输入行：它是个一次性的选择，选完就散，不该另开一块常驻区域
	if st.shareOpen {
		renderShareBar(c, st)
		return
	}
	if st.attachOpen {
		renderAttachBar(c, st)
		return
	}

	box := buildInputBox(st, c.w, c.h)
	for k := 0; k < box.rows; k++ {
		idx := box.view.from + k
		first := k == 0
		c.row(func(l *lineBuilder, y int) {
			l.styled(cBorder, "│")
			if first {
				l.styled(promptStyle(st), box.prompt)
			} else {
				// 续行只留出提示符的宽度：文字仍然对齐在同一列上
				l.styled("", strings.Repeat(" ", box.promptW))
			}

			used := 0
			if idx < len(box.view.lines) {
				base := box.view.starts[idx]
				for i, r := range box.view.lines[idx] {
					if used+runeWidth(r) > box.textW {
						break
					}
					style := ""
					if st.selOn {
						// 选中段的字符反白：鼠标拖出来的选择，肉眼要看得见边界
						if ii := base + i; ii >= st.selA && ii < st.selB {
							style = cSelBG + cBold
						}
					}
					l.styled(style, string(r))
					used += runeWidth(r)
				}
			}

			// 按钮只画在第一条可见行上。它们的宽度已经在折行时预留过，
			// 所以文字不可能顶到下面把它们盖住 —— 热区可见即可点。
			if first && box.btns.ok {
				cx := c.button(l, y, box.btns.attX, "btn:attach", box.btns.attTxt, true)
				_ = c.button(l, y, cx, "btn:send", box.btns.sendTxt, true)
			}

			// 每条可见行都登记输入区热区：点第几列就把光标放到第几列（tui.go 的 caret），
			// 拖过文字会选中。右边界正好停在按钮左侧，不会抢走按钮的点击。
			for col := 0; col < box.textW; col++ {
				c.box(fmt.Sprintf("caret:chat:%d", col), 1+box.promptW+col, y, 1)
			}
			// 提示符与行尾空白也归输入框：点它们聚焦即可，不挪光标
			c.box("focus:chat", 1, y, c.w-2)
			l.pad(c.w - 1)
			l.styled(cBorder, "│")
		})
	}
}

// ---------------------------------------------------------------------------
// 附件面板：在聊天区里挑文件，让它随下一条消息一起发给 AI
//
// 借的是聊天区那两栏（左挑右看），所以不用另发明一套布局。
// 面板是模态的：打开时聊天消息暂时让位，关掉就原样回来。
// ---------------------------------------------------------------------------

func attachBrowseRows(st *state, w, bodyH int) []rowFn {
	var body []rowFn

	body = append(body, textRow(newCell(
		segment{cDim + cBold, " 挑文件"},
		segment{cDim, "  点名字加入 · 点目录进去 · 回车选中 · Esc 关闭"},
	), w))

	if st.attachBusy && len(st.attachEntries) == 0 {
		body = append(body, textRow(newCell(segment{cDim, "  正在读取…"}), w))
	}
	if len(st.attachEntries) == 0 && !st.attachBusy {
		body = append(body, textRow(newCell(segment{cDim, "  （空目录，或还没有读取）"}), w))
	}

	for i, e := range st.attachEntries {
		entry, idx := e, i
		body = append(body, func(c *canvas, l *lineBuilder, y, x int) {
			mark, style := "·", cDim
			if entry.Dir {
				mark, style = "▸", cAccent
			}
			if idx == st.attachSel {
				style = cSelBG + cBold + style
			}
			l.styled(style, " "+mark+" ")
			l.styled(style, padDisplay(truncDisplay(entry.Name, w-16), w-16))
			if !entry.Dir {
				l.styled(cDim, truncDisplay(humanSize(entry.Size), 10))
			}
			l.pad(w)
			if entry.Dir {
				c.box("atchdir:"+entry.Name, x, y, w)
				return
			}
			c.box("atchfile:"+entry.Name, x, y, w)
		})
	}
	return windowRows(body, st.attachScroll, bodyH)
}

func attachSelectedRows(st *state, w, bodyH int) []rowFn {
	if w <= 0 {
		return nil
	}
	var out []rowFn
	add := func(s, style string) {
		out = append(out, textRow(newCell(segment{style, " " + truncDisplay(s, w-2)}), w))
	}

	add("已选 "+fmt.Sprintf("(%d)", len(st.attachments)), cDim+cBold)
	if len(st.attachments) == 0 {
		add("", "")
		add("还没有选文件。", cDim)
		add("图片会原样发给模型看图，", cDim)
		add("文本会把内容放进正文。", cDim)
		return out
	}

	for i, a := range st.attachments {
		idx := i
		out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
			icon := "·"
			switch a.Type {
			case "image":
				icon = "▣"
			case "text":
				icon = "≡"
			}
			l.styled(cAccent, " "+icon+" ")
			nameW := w - 14
			if nameW < 6 {
				nameW = 6
			}
			l.styled("", padDisplay(truncDisplay(a.Name, nameW), nameW))
			cx := x + 3 + nameW
			_ = c.button(l, y, cx, "atchdel:"+fmt.Sprint(idx), "移除", true)
			l.pad(w)
		})
	}
	return out
}

// attachBarWidth 是附件面板底部按钮行需要的最小宽度。
const attachBarWidth = 68

// renderAttachBar 画附件面板的底部按钮行。
func renderAttachBar(c *canvas, st *state) {
	c.row(func(l *lineBuilder, y int) {
		l.styled(cBorder, "│")
		title := " 附件 "
		l.styled(cAccent+cBold, title)
		if c.w < attachBarWidth {
			l.styled(cDim, truncDisplay("（窗口太窄，按 Esc 关闭）", maxInt(0, c.w-3-dispWidth(title))))
			l.pad(c.w - 1)
			l.styled(cBorder, "│")
			return
		}
		cx := 1 + dispWidth(title)
		cx = c.button(l, y, cx, "btn:attachup", "向上一级", true)
		if len(st.attachments) > 0 {
			cx = c.button(l, y, cx, "btn:attachclear", fmt.Sprintf("清空(%d)", len(st.attachments)), true)
		}
		_ = c.button(l, y, cx, "btn:attachclose", "完成，回到聊天", false)
		l.pad(c.w - 1)
		l.styled(cBorder, "│")
	})
}

// shareBarWidth 是分享面板需要的最小宽度；再窄就只留一句提示，不画按钮。
const shareBarWidth = 76

func renderShareBar(c *canvas, st *state) {
	c.row(func(l *lineBuilder, y int) {
		l.styled(cBorder, "│")
		title := " 分享整条时间线 "
		l.styled(cAccent+cBold, title)
		if c.w < shareBarWidth {
			l.styled(cDim, truncDisplay("（窗口太窄，按 Esc 关闭）", maxInt(0, c.w-3-dispWidth(title))))
			l.pad(c.w - 1)
			l.styled(cBorder, "│")
			return
		}
		cx := c.button(l, y, 1+dispWidth(title), "btn:sharecopy", "复制到剪贴板", true)
		cx = c.button(l, y, cx, "btn:sharefile", "导出 .md 到工作区", true)
		_ = c.button(l, y, cx, "btn:shareclose", "取消", false)
		l.pad(c.w - 1)
		l.styled(cBorder, "│")
	})
}

// ---------------------------------------------------------------------------
// 输入区（多行）
// ---------------------------------------------------------------------------

// maxInputRows 是输入框长到几行就不再长。
//
// 这是"写一条消息"的地方，不是写文档的地方；再长就会把上面的对话挤没。
// 超出的部分靠滚动看，而滚动窗口始终跟着光标走。
const maxInputRows = 6

// bodyMinH 是聊天区至少要留下的行数：输入框最多只长到不挤垮它。
const bodyMinH = 3

// inputMinTextW 是输入框里至少要给文字留的宽度。
// 留不够就宁可不画按钮（窄屏上文字比按钮重要），发送靠 Ctrl+Enter。
const inputMinTextW = 12

// inputView 是把输入内容折行之后的结果。
//
// 折行只为了**显示**：st.input 里真正的换行符只有用户按回车插进去的那些。
// 所以窗口变窄不会改变消息内容 —— 软折出来的换行绝不会被当成内容发出去。
type inputView struct {
	lines  [][]rune // 折好的显示行（不含换行符）
	starts []int    // 每行第一个 rune 在 st.input 里的下标
	rows   int      // 显示行总数
	from   int      // 第一条可见行（滚动窗口）
	cur    int      // 光标所在显示行
	col    int      // 光标在该行内的显示列（0 基）
}

// visibleRows 是这块输入实际占屏幕几行。
func (iv inputView) visibleRows() int {
	if iv.rows > maxInputRows {
		return maxInputRows
	}
	if iv.rows < 1 {
		return 1
	}
	return iv.rows
}

// indexAt 返回"第 line 行、显示列不超过 col"处的 rune 下标。
// col 超过该行长度就落到行尾 —— ↑↓ 和 Home/End 都靠它换算光标。
func (iv inputView) indexAt(line, col int) int {
	if line < 0 || line >= len(iv.lines) {
		return iv.starts[len(iv.starts)-1]
	}
	acc := 0
	for i, r := range iv.lines[line] {
		if acc >= col {
			return iv.starts[line] + i
		}
		acc += runeWidth(r)
	}
	return iv.starts[line] + len(iv.lines[line])
}

// buildInputView 把输入内容按可用宽度折成显示行，并标出光标在哪一行哪一列。
func buildInputView(st *state, width int) inputView {
	if width < 1 {
		width = 1
	}
	iv := inputView{lines: [][]rune{{}}, starts: []int{0}}
	line, w := 0, 0
	newline := func() {
		line++
		w = 0
		iv.lines = append(iv.lines, []rune{})
		iv.starts = append(iv.starts, 0) // 行首下标由调用处补上，两者必须同步增长
	}
	place := func(r rune) {
		if w+runeWidth(r) > width && w > 0 {
			newline()
		}
		iv.lines[line] = append(iv.lines[line], r)
		w += runeWidth(r)
	}
	for i, r := range st.input {
		if i == st.cursor {
			iv.cur, iv.col = line, w
		}
		if r == '\n' {
			newline()
			iv.starts[line] = i + 1
			continue
		}
		before := line
		place(r)
		if line != before {
			// 软折产生的新行：它的第一个字符就是当前这个 r
			iv.starts[line] = i
		}
	}
	if st.cursor >= len(st.input) {
		iv.cur, iv.col = line, w // 光标在末尾（打字的位置）
	}
	iv.rows = len(iv.lines)

	// 滚动窗口跟着光标走：可见行里看不到光标就把它带进来
	if v := iv.visibleRows(); iv.cur >= iv.from+v {
		iv.from = iv.cur - v + 1
	}
	return iv
}

// inputButtons 是输入行右侧那两个按钮的位置与文字。
type inputButtons struct {
	ok      bool // 这一屏画不画按钮
	resv    int  // 按钮一共占掉多少列（含与文字之间的那 1 列空隙）
	attX    int
	attTxt  string
	sendX   int
	sendTxt string
}

// inputButtonsFor 决定右侧按钮放哪、给文字留多宽。
//
// 按钮宽度必须从文字的折行宽度里**预先扣掉**：不扣的话第一行文字会顶到按钮下面
// 把它们盖住 —— 热区还在，但用户看不见，也就点不到「发送」。而「发送」是现在
// 唯一无歧义的送出口，不能时有时无。
func inputButtonsFor(st *state, w int) (inputButtons, int) {
	var b inputButtons
	b.attTxt = "附件"
	if n := len(st.attachments); n > 0 {
		b.attTxt = fmt.Sprintf("附件 %d", n)
	}
	b.sendTxt = "发送"
	if st.editing != "" {
		b.sendTxt = "提交" // 修改态：这个按钮是把这条消息存成一个新版本
	}

	full := w - 2 - dispWidth(promptFor(st))
	// button 画的其实是 " label "，比 label 宽 2 列；两个按钮之间再隔 2 列。
	b.sendX = w - dispWidth(b.sendTxt) - 4
	b.attX = b.sendX - dispWidth(b.attTxt) - 4
	b.resv = w - b.attX // 一直到右边界都归按钮（含与文字之间的那 1 列空隙）

	if full-b.resv < inputMinTextW {
		return b, full // 窄屏：不画按钮，文字优先
	}
	b.ok = true
	return b, full - b.resv
}

// inputBox 是输入区这一帧的完整测算结果。
type inputBox struct {
	prompt  string
	promptW int
	textW   int // 文字可用宽度（折行就按它算）
	btns    inputButtons
	view    inputView
	rows    int // 实际占屏幕几行
}

// buildInputBox 把横向分配、折行结果与占用行数一次算清。
//
// renderFrame（给版面让位）、renderFooter（画）、cursorSeq（摆光标）三处都必须用
// 同一份结果：各算一份的话，光标就会摆到别的行上去 —— 而偏了看起来就是"输入框坏了"。
func buildInputBox(st *state, w, h int) inputBox {
	box := inputBox{prompt: promptFor(st)}
	box.promptW = dispWidth(box.prompt)
	if box.promptW < 0 {
		box.promptW = 0
	}

	// 非聊天页、或面板占着聊天区时，这里只是一行普通状态栏
	if st.tab != tabChat || st.shareOpen || st.attachOpen {
		box.textW = maxInt(1, w-2-box.promptW)
		box.rows = 1
		return box
	}

	textW := w - 2 - box.promptW
	box.btns, textW = inputButtonsFor(st, w)
	box.textW = maxInt(1, textW)
	box.view = buildInputView(st, box.textW)

	rows := box.view.visibleRows()
	if limit := h - 4 - bodyMinH; rows > limit {
		rows = limit
	}
	box.rows = maxInt(1, rows)
	return box
}

// cursorCol 返回光标在屏幕上的列（1 基）。
func (b inputBox) cursorCol() int {
	// 第 1 列是边框，接着是提示符，再往后才是文字
	return b.promptW + b.view.col + 2
}

// cursorRow 返回光标在屏幕上的行（1 基）。输入区占的是最下面那几行。
func (b inputBox) cursorRow(h int) int {
	return h - b.rows + (b.view.cur - b.view.from)
}

// cursorPos 返回光标在输入行内的列偏移（0 基）。单测用它比较左右移动的位移。
func cursorPos(st *state, w int) int {
	box := buildInputBox(st, w, maxInt(1, st.h))
	return box.promptW + 1 + box.view.col
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func textRow(cl cell, w int) rowFn {
	return func(c *canvas, l *lineBuilder, y, x int) {
		l.put(cl.fit(w))
	}
}

// windowRows 按偏移取一段行，不足则补空行。
func windowRows(rows []rowFn, offset, height int) []rowFn {
	if offset < 0 {
		offset = 0
	}
	if offset > len(rows) {
		offset = len(rows)
	}
	vis := rows[offset:]
	if len(vis) > height {
		vis = vis[:height]
	}
	out := append([]rowFn{}, vis...)
	for len(out) < height {
		out = append(out, nil)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
