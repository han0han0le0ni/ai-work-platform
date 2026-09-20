package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// 状态
// ---------------------------------------------------------------------------

type tabKind int

const (
	tabChat tabKind = iota
	tabPlugins
	tabFiles
	tabSettings
)

var allTabs = []tabKind{tabChat, tabPlugins, tabFiles, tabSettings}

func (t tabKind) key() string {
	switch t {
	case tabPlugins:
		return "plugins"
	case tabFiles:
		return "files"
	case tabSettings:
		return "settings"
	default:
		return "chat"
	}
}

func (t tabKind) label() string {
	switch t {
	case tabPlugins:
		return "插件"
	case tabFiles:
		return "文件"
	case tabSettings:
		return "设置"
	default:
		return "聊天"
	}
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

// pluginInfo 是界面上展示的一个插件。
//
// 数据来自磁盘上的 manifest.json：核心没有"列出插件"的接口（规范 1.5 只有 6 个），
// 界面就像普通插件那样，经能力代理把 plugins/ 目录读出来。好处是**新装的插件会自动出现**，
// 界面不需要认识任何具体插件。
type pluginInfo struct {
	ID          string
	Name        string
	Version     string
	Description string
	Runtime     string
	Entry       string
	Dir         string // 目录名，例如 demo.plugin；被禁用时带 .disabled 后缀
	Disabled    bool   // 目录带 .disabled：重启后核心不再加载它
	Self        bool   // 是不是插件管理器自己（不可停用 / 不可删除）
	Provides    []string
	Consumes    []string
	Permissions []string
	Deps        []string
	Compat      string
	Alive       []string // 声明里现在能发现到的服务
	Dead        []string // 声明了但发现不到的服务（没启动 / 跳过了 / 崩了）
	ReadErr     string
}

func (p pluginInfo) running() bool { return len(p.Alive) > 0 }

type formField struct {
	key    string
	label  string
	value  string
	hint   string
	secret bool

	// dirty 表示用户手动改过这个字段。
	//
	// 后台刷新（启动时读配置、保存后回读）不许覆盖用户正在输入的内容 ——
	// 否则粘了一半密钥时来一次刷新，前功尽弃。
	dirty bool
}

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

	// trace 是「AI 在干什么」的过程记录：模型在想什么、调了哪些工具、卡在哪。
	//
	// 它由事件总线喂进来（见 activity.go），生成结束后**不**立刻清掉 ——
	// 留着让人回看，下一次发消息时才换新的。
	trace *traceView

	// 用量浮窗（见 float.go）。
	//
	// 数据是 usage-monitor 广播来的；位置归用户 —— 拖到哪儿就停在哪儿，
	// -1 表示"还没拖过"，这时贴在右上角。
	usage     usageSnapshot
	usageOK   bool
	floatX    int
	floatY    int
	floatFold bool
	floatDrag *floatDrag

	// floatSave 把浮窗位置写回插件配置。界面线程不做 RPC，所以由插件层注入。
	floatSave func(x, y int, fold bool)

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
	services       []serviceStatus
	plugins        []pluginInfo
	pluginScroll   int
	selectedPlugin int
	pluginBusy     bool // 启停 / 删除进行中：按钮置灰，防重复点击
	confirmDelete  bool // 删除的二次确认：删除不可逆，必须再点一次

	// 设置页
	provider  string // llm.v1 的 provider：mock / openai / ollama
	presetKey string // 当前选中的服务预设
	fields    []formField
	focus     string
	status    string
	statusOK  bool

	// 模型选择
	modelList  []string // 可点选的模型（来自预设或服务端）
	modelsNote string   // 模型区的一句话说明
	modelsBusy bool

	// 设置页 · AI 预算（工具调用轮数上限，可设为不限制）
	budgetUnlimited bool
	budgetLoaded    bool
	budgetBusy      bool
	budgetNote      string

	// 文件页：挑工作目录（点目录进入）或直接填路径
	wsRoot    string     // 当前工作目录
	wsPath    string     // 正在浏览的目录
	wsParent  string     // 上一级
	wsEntries []dirEntry // 当前目录条目
	wsSel     int        // 选中行
	wsScroll  int
	wsBusy    bool // 正在读目录
	wsSetBusy bool // 正在设置工作区（与读目录分开：读着的时候也该能按回车设置）

	events []string
}

// floatDrag 是一次拖动浮窗的中间状态。
type floatDrag struct {
	gx, gy int  // 按下处相对浮窗左上角的偏移：抓哪儿就从哪儿跟，浮窗不会先跳一下
	moved  bool // 真的挪过 —— 松手时据此区分"拖动"和"轻点收起"
}

// dirEntry 是文件页里的一行：一个文件或目录。
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

// field 按 key 找字段。
func (st *state) field(key string) *formField {
	for i := range st.fields {
		if st.fields[i].key == key {
			return &st.fields[i]
		}
	}
	return nil
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

	c.rows = append(c.rows, borderTop(w, "AI 工作平台 · 工作台", titleRight(st), color))
	renderTabs(c, st)

	bodyH := h - 5

	// 设置页表单是全宽的；聊天/插件页在够宽时才分栏
	two := st.tab != tabSettings && w >= sidebarMinW
	leftW, rightW := w-2, 0
	if two {
		rightW = sidebarW
		leftW = w - 3 - rightW
	}

	var leftRows, rightRows []rowFn
	switch st.tab {
	case tabPlugins:
		leftRows = pluginListRows(st, leftW, bodyH)
		rightRows = pluginDetailRows(st, rightW, bodyH)
	case tabFiles:
		leftRows = filesRows(st, leftW, bodyH)
		rightRows = fileDetailRows(st, rightW, bodyH)
	case tabSettings:
		leftRows = settingsRows(st, w-2, bodyH)
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

	// 浮窗最后画：它盖在版面之上，所以最后一步才贴（见 float.go）
	drawFloat(c, st)
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
	case tabPlugins:
		return "单击插件看详情 · 可点「启用/禁用」与「删除」（删除要二次确认） · 重启后生效 · Ctrl+C 退出"
	case tabFiles:
		return "填路径后回车 · 点目录进入 · 点「向上一级」返回 · 走到哪点「设为工作区」 · Ctrl+C 退出"
	case tabSettings:
		return "点服务 → 粘密钥 → 保存 · Tab 换输入框 · Esc 后 Tab 切页 · Ctrl+C 退出"
	default:
		// 底部这一行优先说刚发生过什么：复制、导出、失败原因……
		// 那些结果没别的地方能显示，而这一行本来就空着。
		if st.notice != "" {
			return st.notice
		}
		if st.attachOpen {
			return "点文件名加入附件 · 点目录进去 · ↑↓ 选择 · 回车加选中项 · Esc 关闭 · 关掉后回车发送"
		}
		if st.editing != "" {
			return "回车 = 提交为新版本（旧版本与它后面那条时间线都保留） · Esc = 放弃修改"
		}
		return "Enter 发送 · 点消息框上的「修改 / 重新生成 / 分享」· 点 ‹ n/n › 翻版本 · 滚轮翻历史 · 拖浮窗挪位 · Alt+方向键也可挪 · Tab 切页"
	}
}

// ---------------------------------------------------------------------------
// 标签栏
// ---------------------------------------------------------------------------

func renderTabs(c *canvas, st *state) {
	c.row(func(l *lineBuilder, y int) {
		l.styled(cBorder, "│")
		x := 1
		for _, t := range allTabs {
			label, style := " "+t.label()+" ", cDim
			if st.tab == t {
				label, style = "["+t.label()+"]", cAccent+cBold
			}
			l.styled(style, label)
			c.box("tab:"+t.key(), x, y, dispWidth(label))
			x += dispWidth(label)
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
	case tabPlugins:
		return fmt.Sprintf("插件 %d/%d", countRunning(st.plugins), len(st.plugins))
	case tabFiles:
		if st.wsRoot == "" {
			return "工作区：未指定"
		}
		return "工作区：" + truncDisplay(st.wsRoot, 40)
	case tabSettings:
		return "当前 provider：" + st.provider
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

func countRunning(ps []pluginInfo) int {
	n := 0
	for _, p := range ps {
		if p.running() {
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
		l.styled(cBorder, "┌─ ")
		l.styled(code+cBold, label)
		l.styled(cDim, tail)
		used := dispWidth("┌─ ") + dispWidth(label) + dispWidth(tail)
		fill := bw - used - 3
		if fill < 1 {
			fill = 1
		}
		l.styled(cBorder, " "+strings.Repeat("─", fill)+"┐")
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
		l.styled(cBorder, "└"+strings.Repeat("─", maxInt(1, bw-3))+"┘")
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
// 插件页
// ---------------------------------------------------------------------------

// countSameID 数一数目录里躺着几份这个 ID。
//
// 正常只有一份；装了 x.plugin 又留着 x.plugin.disabled 就是两份，
// 而两份的 ID 一模一样 —— 不把目录名摆出来，用户分不清自己点的是哪份。
func countSameID(ps []pluginInfo, id string) int {
	n := 0
	for _, p := range ps {
		if p.ID == id {
			n++
		}
	}
	return n
}

func pluginListRows(st *state, w, bodyH int) []rowFn {
	var body []rowFn

	body = append(body, func(c *canvas, l *lineBuilder, y, x int) {
		x = c.button(l, y, x, "btn:rescan", "重新扫描", false)
		l.styled(cDim, truncDisplay(st.status, w-dispWidth(" 重新扫描 ")-4))
	})

	if len(st.plugins) == 0 {
		body = append(body, textRow(newCell(segment{cDim, "  没有读到插件目录"}), w))
	}

	for i, p := range st.plugins {
		pl, idx := p, i
		body = append(body, func(c *canvas, l *lineBuilder, y, x int) {
			// 禁用的那份不算"运行中"：这一份重启后根本不会被加载，
			// 服务是同 ID 的另一份在提供。画成 ✓ 就成了两条一模一样的插件。
			mark, style := "✗", cBad
			switch {
			case pl.Disabled:
				mark, style = "✗", cDim
			case pl.running():
				mark, style = "✓", cOK
			}
			if idx == st.selectedPlugin {
				style = cSelBG + cBold + style
			}
			id := truncDisplay(pl.ID, w-16)
			if pl.Disabled {
				id = truncDisplay(pl.ID+"（禁用）", w-16)
			}
			l.styled(style, " "+mark+" ")
			l.styled(style, padDisplay(id, w-16))
			l.styled(cDim, truncDisplay("v"+pl.Version, 10))
			l.pad(w)
			// 动作名用行号而不是插件 ID：两份 ID 相同的插件会登记出两个同名的框，
			// 命中时永远落在前一个上 —— 后一份就成了点不动的死行。
			c.box("plugin:"+strconv.Itoa(idx), x, y, w)
		})
	}

	return windowRows(body, st.pluginScroll, bodyH)
}

func pluginDetailRows(st *state, w, bodyH int) []rowFn {
	if w <= 0 {
		return nil
	}
	if len(st.plugins) == 0 {
		return []rowFn{textRow(newCell(segment{cDim, "  未选中插件"}), w)}
	}
	i := st.selectedPlugin
	if i < 0 || i >= len(st.plugins) {
		i = 0
	}
	p := st.plugins[i]

	var out []rowFn
	add := func(s string, style string) {
		out = append(out, textRow(newCell(segment{style, " " + truncDisplay(s, w-2)}), w))
	}
	add("插件详情", cDim+cBold)
	if p.ReadErr != "" {
		add("读取失败："+p.ReadErr, cBad)
		return out
	}

	add(p.ID, cAccent+cBold)
	if p.Name != "" && p.Name != p.ID {
		add(p.Name, "")
	}
	if p.Dir != "" {
		add("目录   "+p.Dir, cDim)
	}
	// 目录里躺着两份 ID 相同的插件时（一份 .plugin、一份 .plugin.disabled），
	// 光看名字分不出谁是谁，得把目录名和"只对这份生效"说清楚。
	if n := countSameID(st.plugins, p.ID); n > 1 {
		out = append(out, textRow(plainCell(""), w))
		for _, ln := range wrapText("目录里有 "+strconv.Itoa(n)+
			" 份 ID 相同的插件，启停 / 删除只对选中的这一份生效", w-2) {
			add(ln, cWarn)
		}
	}
	add("版本   v"+p.Version, "")
	add("运行时 "+p.Runtime, "")
	add("入口   "+p.Entry, "")
	if p.Description != "" {
		out = append(out, textRow(plainCell(""), w))
		for _, ln := range wrapText(p.Description, w-2) {
			add(ln, cDim)
		}
	}

	out = append(out, textRow(plainCell(""), w))
	add("提供的服务", cDim+cBold)
	if len(p.Provides) == 0 {
		add("（无）", cDim)
	}
	for _, s := range p.Provides {
		style := cBad
		if contains(p.Alive, s) {
			style = cOK
		}
		add("✓ "+s, style)
	}
	if len(p.Dead) > 0 {
		add("未运行的服务", cWarn)
		for _, s := range p.Dead {
			add("✗ "+s, cDim)
		}
	}
	add("状态   "+pluginStateText(p), stateStyle(p))

	// 启停：改目录名的后缀（核心只加载 *.plugin），所以同样是重启后生效
	out = append(out, textRow(plainCell(""), w), textRow(newCell(
		segment{cDim + cBold, " 启停"}), w))
	out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		if p.Self {
			l.styled(cDim, truncDisplay("插件管理器自己，不可停用也不可删除", w-2))
			return
		}
		label, note := "禁用", " 当前：已启用，重启后照常加载"
		if p.Disabled {
			label, note = "启用", " 当前：已禁用，重启后不再加载"
		}
		cx := c.button(l, y, x+1, "btn:toggle", label, !st.pluginBusy)
		l.styled(cDim, truncDisplay(note, maxInt(0, w-cx-2)))
	})

	// 删除不可逆：点一次只是进入确认态，再点一次"确认删除"才真的动手
	out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
		if p.Self {
			return
		}
		l.styled("", " ")
		if st.confirmDelete {
			cx := c.button(l, y, x+1, "btn:delconfirm", "确认删除", true)
			_ = c.button(l, y, cx, "btn:delcancel", "取消", false)
			return
		}
		cx := c.button(l, y, x+1, "btn:del", "删除插件", !st.pluginBusy)
		l.styled(cDim, truncDisplay(" 连配置与日志一起清除", maxInt(0, w-cx-2)))
	})

	if len(p.Permissions) > 0 {
		out = append(out, textRow(plainCell(""), w))
		add("权限", cDim+cBold)
		for _, s := range p.Permissions {
			add("· "+s, "")
		}
	}
	if len(p.Consumes) > 0 {
		out = append(out, textRow(plainCell(""), w))
		add("消费", cDim+cBold)
		for _, s := range p.Consumes {
			add("· "+s, "")
		}
	}
	if len(p.Deps) > 0 {
		out = append(out, textRow(plainCell(""), w))
		add("依赖", cDim+cBold)
		for _, s := range p.Deps {
			add("· "+s, "")
		}
	}
	if p.Compat != "" {
		out = append(out, textRow(plainCell(""), w))
		add("兼容核心 "+p.Compat, cDim)
	}
	return out
}

func pluginStateText(p pluginInfo) string {
	switch {
	case p.Disabled:
		return "已禁用（重启后不再加载）"
	case p.running() && len(p.Dead) == 0:
		return "运行中"
	case p.running():
		return "部分可用"
	case len(p.ReadErr) > 0:
		return "无法读取"
	default:
		return "未运行"
	}
}

func stateStyle(p pluginInfo) string {
	if p.running() {
		return cOK
	}
	return cBad
}

// ---------------------------------------------------------------------------
// 文件页：挑一个文件夹当工作区
//
// 两条路都留着：
//   - 选：点目录名进去、点「..」退出来，走到哪儿点「设为工作区」就是哪儿
//   - 填：路径输入框直接打字或粘贴绝对路径，回车即可
//
// 终端里没有系统文件对话框，所以"选"就是这样一个可点的目录浏览器。
// ---------------------------------------------------------------------------

func filesRows(st *state, w, bodyH int) []rowFn {
	var body []rowFn

	// ① 路径：输入框直接填，下面的列表用来点选
	body = append(body, textRow(newCell(
		segment{cDim + cBold, " ① 路径"},
		segment{cDim, "  直接粘贴，或点下面的目录进去挑"},
	), w))
	body = append(body, fieldRow(st, w, "wsPath"))
	body = append(body, func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		cx := c.button(l, y, x+1, "btn:wsset", "设为工作区", !st.wsSetBusy)
		cx = c.button(l, y, cx, "btn:wsrefresh", "刷新", !st.wsBusy)
		if st.wsParent != "" {
			_ = c.button(l, y, cx, "btn:wsup", "向上一级", !st.wsBusy)
		}
	})
	body = append(body, textRow(plainCell(""), w))

	// ② 目录内容：目录可点进去，文件只是列出来看
	body = append(body, textRow(newCell(
		segment{cDim + cBold, " ② 目录"},
		segment{cDim, "  " + truncDisplay(firstNonEmpty(st.wsPath, "（未知）"), maxInt(8, w-14))},
	), w))

	if st.wsBusy && len(st.wsEntries) == 0 {
		body = append(body, textRow(newCell(segment{cDim, "  正在读取…"}), w))
	}
	if len(st.wsEntries) == 0 && !st.wsBusy {
		body = append(body, textRow(newCell(segment{cDim, "  （空目录，或还没有读取）"}), w))
	}

	for i, e := range st.wsEntries {
		entry, idx := e, i
		body = append(body, func(c *canvas, l *lineBuilder, y, x int) {
			mark, style := "·", cDim
			if entry.Dir {
				mark, style = "▸", cAccent
			}
			if idx == st.wsSel {
				style = cSelBG + cBold + style
			}
			l.styled(style, " "+mark+" ")
			l.styled(style, padDisplay(truncDisplay(entry.Name, w-16), w-16))
			if !entry.Dir {
				l.styled(cDim, truncDisplay(humanSize(entry.Size), 10))
			}
			l.pad(w)
			// 目录点进去，文件只是选中看看
			if entry.Dir {
				c.box("wsdir:"+entry.Name, x, y, w)
				return
			}
			c.box("wsfile:"+entry.Name, x, y, w)
		})
	}
	return windowRows(body, st.wsScroll, bodyH)
}

// fileDetailRows 是文件页的右栏：当前工作区与选中条目。
func fileDetailRows(st *state, w, bodyH int) []rowFn {
	if w <= 0 {
		return nil
	}
	var out []rowFn
	add := func(s, style string) {
		out = append(out, textRow(newCell(segment{style, " " + truncDisplay(s, w-2)}), w))
	}

	add("工作区", cDim+cBold)
	if st.wsRoot == "" {
		add("未指定", cWarn)
		add("", "")
		add("指定之后，workspace.v1 的所有操作", cDim)
		add("都会被锁在这个目录里。", cDim)
	} else {
		add(st.wsRoot, cOK+cBold)
	}

	if st.wsSel >= 0 && st.wsSel < len(st.wsEntries) {
		e := st.wsEntries[st.wsSel]
		out = append(out, textRow(plainCell(""), w))
		add("选中", cDim+cBold)
		add(e.Name, "")
		if e.Dir {
			add("类型   目录", "")
			add("", "")
			add("点它可以进去，走到目标后", cDim)
			add("再点「设为工作区」。", cDim)
		} else {
			add("类型   文件", "")
			add(fmt.Sprintf("大小   %s", humanSize(e.Size)), "")
		}
	}
	return out
}

// budgetSummary 把预算说成人话。
func budgetSummary(b agentBudget) string {
	if b.Unlimited {
		return "当前：不限制，由模型自己决定何时停"
	}
	return fmt.Sprintf("当前：最多 %d 轮工具调用", b.Steps)
}

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
// 设置页
// ---------------------------------------------------------------------------

func settingsRows(st *state, w, bodyH int) []rowFn {
	var out []rowFn
	blank := func() { out = append(out, textRow(plainCell(""), w)) }

	section := func(title, note string) {
		out = append(out, textRow(newCell(
			segment{cDim + cBold, " " + title},
			segment{cDim, "  " + note},
		), w))
	}

	// ① 选服务：地址与默认模型一起填好，用户只需要再粘个密钥
	section("① 选服务", "点一下，地址和模型自动填好")
	var presets []chipItem
	for _, p := range providerPresets {
		presets = append(presets, chipItem{
			action:   "preset:" + p.Key,
			label:    p.Label,
			selected: st.presetKey == p.Key,
		})
	}
	out = append(out, chipRows(presets, w)...)
	blank()

	// ② 模型：先给几个常用的，点「获取可用模型」向服务端要真实清单
	section("② 模型", "点「获取可用模型」向服务端要一份真实清单")
	var models []chipItem
	selectedModel := ""
	if f := st.field("model"); f != nil {
		selectedModel = f.value
	}
	for _, m := range st.modelList {
		models = append(models, chipItem{action: "model:" + m, label: m, selected: m == selectedModel})
	}
	if len(models) > 0 {
		out = append(out, chipRows(models, w)...)
	}
	out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		x = c.button(l, y, x+1, "btn:getmodels", "获取可用模型", st.modelsBusy)
		l.styled(cDim, truncDisplay(st.modelsNote, maxInt(0, w-x-2)))
	})
	blank()

	// ③ API Key：切到这一页时焦点默认就在这儿，粘完直接保存
	section("③ API Key", "直接粘贴；留空=不改动，输入一个减号=清空")
	out = append(out, fieldRow(st, w, "apiKey"))
	if hint := keyHintLine(st); hint != "" {
		out = append(out, textRow(newCell(segment{cDim, "    " + truncDisplay(hint, w-6)}), w))
	}
	blank()

	// 高级：一般不用动
	section("高级", "自己搭的兼容接口改这里；改完点保存立即生效")
	out = append(out, fieldRow(st, w, "baseUrl"), fieldRow(st, w, "model"))
	blank()

	// 识图模型：文字和看图分开配 —— 日常聊天用便宜快的，发了图才切到会看图的
	section("识图模型", "带图片时改用这个；留空 = 跟上面一样，输入减号 = 清空")
	out = append(out, fieldRow(st, w, "visionModel"))
	blank()

	out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		x = c.button(l, y, x+1, "btn:save", "保存并生效", true)
		_ = c.button(l, y, x, "btn:test", "测试连接", false)
	})
	blank()

	// ④ AI 预算：模型反复调工具是常态，但"最多几轮"该由用户说了算
	section("④ 工具调用预算", "超了就自动收手；选「不限制」则由模型自己决定何时停")
	out = append(out, fieldRow(st, w, "agentSteps"))
	out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		cx := x + 1
		cx = c.chip(l, y, cx, "budget:limited", "限制步数", !st.budgetUnlimited)
		_ = c.chip(l, y, cx, "budget:unlimited", "不限制", st.budgetUnlimited)
	})
	out = append(out, func(c *canvas, l *lineBuilder, y, x int) {
		l.styled("", " ")
		cx := c.button(l, y, x+1, "btn:budgetsave", "应用预算", !st.budgetBusy)
		l.styled(cDim, truncDisplay(" "+st.budgetNote, maxInt(0, w-cx-2)))
	})
	return out
}

// fieldRow 渲染一个可编辑输入框：点一下聚焦，聚焦后直接打字或粘贴。
func fieldRow(st *state, w int, key string) rowFn {
	f := st.field(key)
	if f == nil {
		return nil
	}
	return func(c *canvas, l *lineBuilder, y, x int) {
		label := " " + padDisplay(f.label, 10)
		l.styled(cDim, label)
		fx := x + dispWidth(label)
		inner := w - dispWidth(label) - 4
		if inner < 4 {
			inner = 4
		}

		focused := st.focus == f.key
		style := cBorder
		if focused {
			style = cAccent + cBold
		}

		// 有输入就显示输入；没输入且未聚焦时，用灰色提示文字占位
		text := truncDisplay(f.value, inner)
		used := dispWidth(text)

		l.styled(style, "[")
		l.styled("", text)
		if focused {
			l.styled(cAccent2+cBold, "▏")
			used++
		}
		if used < inner {
			// 聚焦时也要显示提示：空的输入框里摆着「已配置 sk-****cdef」，
			// 比只有一个光标更让人确认"密钥还在，只是不回显"。
			if placeholder := f.hint; placeholder != "" && f.value == "" {
				ph := truncDisplay(placeholder, inner-used)
				l.styled(cDim, ph)
				used += dispWidth(ph)
			}
			if used < inner {
				l.styled("", strings.Repeat(" ", inner-used))
			}
		}
		l.styled(style, "]")
		c.box("focus:"+f.key, fx, y, inner+2)
	}
}

// keyHintLine 给出「密钥去哪拿」的提示。
//
// 刻意不复用字段自己的提示（f.hint）：那个已经显示在输入框里了，
// 再写一遍就是上下两行同样的字。
func keyHintLine(st *state) string {
	p, ok := presetByKey(st.presetKey)
	switch {
	case ok && p.KeyURL != "":
		return "密钥在 " + p.KeyURL + " 申请"
	case ok && !p.NeedKey:
		return p.Label + " 不需要密钥"
	}
	return ""
}

// chipItem 是一个可点选项。
type chipItem struct {
	action   string
	label    string
	selected bool
}

// chipRows 把可点选项按可用宽度自动折行。
func chipRows(items []chipItem, w int) []rowFn {
	var rows []rowFn
	var cur []chipItem
	used := 2 // 左侧留白
	flush := func() {
		if len(cur) == 0 {
			return
		}
		batch := append([]chipItem{}, cur...)
		rows = append(rows, func(c *canvas, l *lineBuilder, y, x int) {
			l.styled("", " ")
			cx := x + 1
			for _, it := range batch {
				cx = c.chip(l, y, cx, it.action, it.label, it.selected)
			}
		})
		cur, used = nil, 2
	}
	for _, it := range items {
		wid := chipWidth(it)
		if len(cur) > 0 && used+wid > w {
			flush()
		}
		cur = append(cur, it)
		used += wid
	}
	flush()
	return rows
}

func chipWidth(it chipItem) int {
	text := "  " + it.label + "  "
	if it.selected {
		text = "● " + it.label + " "
	}
	return dispWidth(text) + 2
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
			l.styled(style, " "+truncDisplay(st.status, c.w-3))
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

	c.row(func(l *lineBuilder, y int) {
		l.styled(cBorder, "│")
		prompt := promptFor(st)
		avail := c.w - 2 - dispWidth(prompt)
		if avail < 1 {
			avail = 1
		}
		from, widths, _ := inputWindow(st, avail)
		l.styled(promptStyle(st), prompt)
		used := 0
		for i := from; i < len(st.input); i++ {
			if used+widths[i] > avail {
				break
			}
			l.styled("", string(st.input[i]))
			used += widths[i]
		}

		// 附件按钮贴在输入行右侧：只在剩下的宽度够放时才画，
		// 免得把用户正在打的字挤掉（那比没有按钮更烦人）。
		btnText := "附件"
		if n := len(st.attachments); n > 0 {
			btnText = fmt.Sprintf("附件 %d", n)
		}
		btnW := dispWidth(btnText) + 4
		btnX := -1
		if c.w-2-used >= btnW+2 {
			btnX = c.w - 2 - btnW
			_ = c.button(l, y, btnX, "btn:attach", btnText, true)
		}

		// 输入区的焦点框必须避开按钮：hit 返回的是**第一个**命中的热区，
		// 焦点框若盖住按钮，用户点「附件」会变成点输入框 —— 点不动。
		focusX := 1 + dispWidth(prompt)
		focusW := avail
		if btnX >= 0 {
			focusW = btnX - 1 - focusX
		}
		if focusW > 0 {
			c.box("focus:chat", focusX, y, focusW)
		}
		l.pad(c.w - 1)
		l.styled(cBorder, "│")
	})
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

// inputWindow 计算输入行的显示窗口：返回起始 rune 下标与每字符宽度。
func inputWindow(st *state, avail int) (from int, widths []int, cursorW int) {
	widths = make([]int, len(st.input))
	for i, r := range st.input {
		widths[i] = runeWidth(r)
		if i < st.cursor {
			cursorW += runeWidth(r)
		}
	}
	if avail < 1 {
		avail = 1
	}
	if cursorW < avail {
		return 0, widths, cursorW
	}
	target := cursorW - avail*3/4
	acc := 0
	for i := 0; i < len(st.input); i++ {
		if acc >= target {
			return i, widths, cursorW
		}
		acc += widths[i]
	}
	return len(st.input), widths, cursorW
}

// cursorPos 返回光标在输入行内的列偏移（0 基）。
func cursorPos(st *state, w int) int {
	prompt := promptFor(st)
	avail := w - 2 - dispWidth(prompt)
	from, widths, _ := inputWindow(st, avail)

	col := dispWidth(prompt) + 1
	for i := from; i < st.cursor && i < len(st.input); i++ {
		col += widths[i]
	}
	if col > w-2 {
		col = w - 2
	}
	return col
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
