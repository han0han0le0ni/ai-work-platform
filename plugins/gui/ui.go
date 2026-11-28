package main

import (
	"image"
	"image/color"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// pageKind 区分原生页与合成页（ui.page.* 兜底）。
type pageKind int

const (
	pageNative pageKind = iota // 原生绘制的功能页
	pageChat                   // 聊天页（原生，带输入框）
	pagePanel                  // 合成页：渲染插件 ui.page.* 的文本画面
)

// page 是左侧导航里的一项。
type page struct {
	key     string // 稳定标识：原生页用固定名，合成页用 service/panel
	label   string // 导航按钮上的 1~2 字
	title   string // 页面标题
	icon    string // 导航图标（插件贡献带入；空则按 key 取内置图标）
	service string // 依赖的插件服务；插件不在线时这一页整个消失（DSH 行为）
	kind    pageKind
	svc     string // 合成页所属服务
	panel   string // 合成页 panel id
	click   widget.Clickable
}

// navIcon 返回导航行的图标：贡献条目自带图标，原生页用内置符号。
func (p page) navIcon() string {
	if p.icon != "" {
		return p.icon
	}
	return pageIcon(p.key)
}

// UI 是全部界面状态。Gio 是即时模式：每帧从 bridge 读快照、重建控件树。
type UI struct {
	bridge *bridge
	window *app.Window
	th     *material.Theme

	// 输入框（保留跨帧状态）
	editor widget.Editor
	send   widget.Clickable

	// 左侧导航：原生页 + 合成页，统一由此列表驱动
	pages      []page
	activeKey  string
	pageLoaded map[string]bool // 合成页是否已首次拉取画面

	// 会话列表里的可点项（按快照索引重建）
	sessionClicks []widget.Clickable
	newSession    widget.Clickable

	// DSH 风格宽侧栏里的会话与工作区入口（与主内容区互不干扰）
	sidebarSessionClicks []widget.Clickable
	sidebarWsClicks      []widget.Clickable
	sidebarList          layout.List

	// 右侧详情面板默认收起，顶栏按钮可展开（对齐 DSH 的宽内容区）
	showRight   bool
	toggleRight widget.Clickable

	// 插件页：分段 + 搜索 + 卡片；点卡片进入插件详情页。
	pluginTab          int
	pluginTabBtns      []widget.Clickable
	pluginSearch       widget.Editor
	pluginOpenBtns     []widget.Clickable
	pluginDetailLinks  []widget.Clickable
	pluginDetailID     string
	pluginDetailBack   widget.Clickable
	pluginDetailToggle widget.Clickable
	pluginDetailDel    widget.Clickable
	pluginDelConfirm   bool

	// 聊天欢迎页的快捷提问
	chatSuggestBtns []widget.Clickable

	// 右侧面板里的插件可点项
	pluginClicks []widget.Clickable

	// 自绘标题栏的窗口按钮
	winMin    widget.Clickable
	winMax    widget.Clickable
	winClose  widget.Clickable
	maximized bool

	// 各功能页的交互控件（跨帧状态）
	// 模型与 API：沿用 DSH ModelsSection 的结构 —— 服务商卡片 + 行内编辑卡
	// + 一张「添加模型提供商」卡片（目录预设 / 自定义 API 两种模式）。
	modelEditors     []widget.Editor // 编辑卡里的字段草稿
	modelInit        bool
	modelEditID      string // 正在编辑的服务商 id（"" 表示都收起）
	modelEditBtns    []widget.Clickable
	modelUseBtns     []widget.Clickable
	modelDeleteBtns  []widget.Clickable
	modelConfirmBtns []widget.Clickable
	modelCancelBtns  []widget.Clickable
	modelRemoveID    string // 待确认删除的服务商 id
	modelEditCancel  widget.Clickable
	modelSave        widget.Clickable
	modelDiscover    widget.Clickable
	modelCustomOpen  bool // 编辑卡里的「自定义设置」折叠
	modelCustomFold  widget.Clickable

	modelAddOpen       bool // 添加卡片展开
	modelAddMode       int  // 0 目录预设 / 1 自定义 API
	modelAddBtn        widget.Clickable
	modelAddCancel     widget.Clickable
	modelAddApply      widget.Clickable
	modelAddModeDir    widget.Clickable
	modelAddModeCustom widget.Clickable
	modelAddPresetPick widget.Clickable // 目录模式的服务商下拉
	modelAddPresetOpen bool
	modelAddPresetRows []widget.Clickable
	modelAddPicked     int
	modelAddKey        widget.Editor
	modelAddBase       widget.Editor
	modelAddModel      widget.Editor
	// 从本机扫描结果回填时带的协议与名称（自定义模式用）。
	modelAddProvider string
	modelAddLabel    string
	modelFoundBtns   []widget.Clickable

	roleActivateBtns []widget.Clickable
	roleDeleteBtns   []widget.Clickable
	roleSaveBtn      widget.Clickable
	roleNewBtn       widget.Clickable
	roleEditors      []widget.Editor // 角色编辑输入
	roleInit         bool
	showRoleEditor   bool
	editingRoleID    string

	budgetSave    widget.Clickable
	budgetEditors []widget.Editor
	budgetInit    bool

	imagePromptEditor widget.Editor
	imageGenBtn       widget.Clickable
	imageEditors      []widget.Editor // 服务商/地址/模型/密钥/尺寸
	imageInit         bool

	pluginToggleBtns []widget.Clickable
	pluginsLoaded    bool // 插件页首次绘制时补一次加载（直接以插件页启动时不会触发 onPageEnter）

	restartBtn widget.Clickable // 顶栏「重启」按钮
	restarting bool             // 已请求重启：先画遮罩，再让核心杀掉本插件

	// 插件事件来自后台线程；页面导航的增删必须回到 UI 线程做，
	// 否则 refreshPanelPages 改切片会和绘制读切片竞争。
	panelsDirty atomic.Bool

	// 插件 UI 贡献（DSH 式 slot）：脚部 / 顶栏动作的点击控件按快照下标对齐；
	// contribLoading 防止加载未完成时每帧重复发起；contribSeenRev 用来判断
	// 后台汇总是否已经回来。
	contribBtns    []widget.Clickable
	contribLoading bool
	contribSeenRev int

	workspacePickBtn   widget.Clickable
	workspaceParentBtn widget.Clickable
	workspaceDirBtns   []widget.Clickable

	usageRefreshBtn widget.Clickable
	settingsRestart widget.Clickable // 设置弹层「重启后台与全部插件」
	usageResetBtn   widget.Clickable

	// 设置弹层（对齐 DSH SettingsRoot：800×800 居中面板 + 左侧 188 分区导航）
	showSettings bool
	settingsSec  int
	// 遮罩拆成弹层外的四块：点外面才关闭，点弹层里面不会误关。
	settingsMaskT widget.Clickable
	settingsMaskB widget.Clickable
	settingsMaskL widget.Clickable
	settingsMaskR widget.Clickable
	settingsClose widget.Clickable
	settingsNav   []widget.Clickable

	// 合成页：当前画面的渲染结果与命中区（按当前页重建）
	panelView     panelView
	panelHits     []widget.Clickable
	panelScroll   gesture.Scroll
	panelScrollPx int

	// 设置里的「插件规则」：内容来自插件管理器面板服务，纯文档。
	rulesLoaded bool
	rulesFrame  panelFrame

	// 会话列表滚动
	sessionList layout.List
	chatList    layout.List
	pluginList  layout.List
	panelList   layout.List

	// 功能页统一滚动容器：每页一个持久 List，滚动位置跨帧保持。
	// 之前各页用每帧新建的局部 layout.List，滚动位置每帧归零，等于滚不动。
	pageScroll map[string]*layout.List

	// 输入焦点请求
	focusInput bool

	// 输入区选择器：工作模式 / 模型 / 推理程度 / 工作区 / 权限。
	// 选择只作用于下一次发送，不写进会话，随时可改。
	workMode     string
	reasoning    string
	permission   string
	composerMenu int
	menuPills    []widget.Clickable
	menuRows     []widget.Clickable
	menuBackdrop widget.Clickable
	// composerCardX 是输入卡片左边缘的窗口坐标；pillOffset 记录每个选择器
	// 相对卡片左边缘的 x。下拉菜单据此贴在触发的 chip 上（对齐 DSH popover）。
	composerCardX int
	composerBaseX int // 中栏在窗口里的左边缘（输入区面板的原点）
	pillOffset    [6]int

	// 附件：选完的文件先挂在输入框上方，发送时随消息一起走，不进会话树。
	atts         []attachFile
	attachBtn    widget.Clickable
	attachRemove []widget.Clickable
}

// 原生页稳定 key。
const (
	keyChat      = "chat"
	keyRole      = "role"
	keyBudget    = "budget"
	keyImageGen  = "imagegen"
	keyPlugins   = "plugins"
	keyWorkspace = "workspace"
	keyUsage     = "usage"
	keySettings  = "settings"
)

// chatSuggestions 对照 DSH 欢迎页的快捷提问；点击后填入输入框，不直接发送。
var chatSuggestions = []string{
	"帮我解释这段代码",
	"整理一份项目说明文档",
	"生一张科技风格的封面图",
	"当前工作区里有哪些文件？",
}

func newUI(b *bridge, w *app.Window) *UI {
	u := &UI{
		bridge:     b,
		window:     w,
		th:         newTheme(),
		activeKey:  keyChat,
		pageLoaded: make(map[string]bool),
		pageScroll: make(map[string]*layout.List),
		// 默认：工具模式、工作区内修改。推理程度默认跟随模型。
		workMode:   "tools",
		permission: "workspace",
	}
	u.editor.SingleLine = false
	u.editor.Submit = true
	u.chatList.ScrollToEnd = true
	// layout.List 的零值是 Horizontal！不显式设 Vertical 会被当成横向列表，
	// 子项拿到的约束会变成 Min/Max.Y 固定、X 无限（0..1e6），
	// 表现就是内容全挤在一条横线上、宽度铺到 1e6。所有列表都必须在这里定轴。
	u.sidebarList.Axis = layout.Vertical
	u.sessionList.Axis = layout.Vertical
	u.chatList.Axis = layout.Vertical
	u.pluginList.Axis = layout.Vertical
	u.panelList.Axis = layout.Vertical
	// 固定原生页；模型接入已并入设置弹层，不再单独占一项。
	u.pages = u.liveNativePages()
	return u
}

// nativePages 是壳子自带的固定页。
// service 非空的页依赖对应插件：插件停用/退出后，这一页连同侧栏入口一起消失。
// 对话与设置是壳子自身能力，不依赖具体插件，始终保留。
var nativePages = []page{
	{key: keyChat, label: "聊", title: "对话", kind: pageChat},
	{key: keyRole, label: "角", title: "角色", kind: pageNative, service: "roleplay.v1"},
	{key: keyImageGen, label: "图", title: "文生图", kind: pageNative, service: "imagegen.v1"},
	{key: keyBudget, label: "预", title: "预算与工具", kind: pageNative, service: "agent.v1"},
	{key: keyPlugins, label: "插", title: "插件管理", kind: pageNative, service: "pluginManager.v1"},
	{key: keyWorkspace, label: "件", title: "工作区", kind: pageNative, service: "workspace.v1"},
	{key: keyUsage, label: "量", title: "用量", kind: pageNative, service: "usage.v1"},
	{key: keySettings, label: "设", title: "设置", kind: pageNative},
}

// liveNativePages 按插件服务是否在线过滤固定页。
func (u *UI) liveNativePages() []page {
	out := make([]page, 0, len(nativePages))
	for _, p := range nativePages {
		if p.service != "" && !u.bridge.hasService(p.service) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// pageIndex 返回某个原生 key 在导航列表中的下标，找不到返回 0。
func (u *UI) pageIndex(key string) int {
	for i := range u.pages {
		if u.pages[i].key == key {
			return i
		}
	}
	return 0
}

// activePage 返回当前页，找不到时回退到第一页。
func (u *UI) activePage() page {
	for _, p := range u.pages {
		if p.key == u.activeKey {
			return p
		}
	}
	return u.pages[0]
}

// reloadActive 重新拉取当前页数据（插件状态变化时调用，从后台线程执行）。
func (u *UI) reloadActive() {
	p := u.activePage()
	switch p.key {
	case keyRole:
		u.bridge.loadRoles()
	case keyBudget:
		u.bridge.loadBudget()
	case keyImageGen:
		u.bridge.loadImageGen()
	case keyPlugins:
		u.bridge.loadPluginMgr()
	case keyWorkspace:
		u.bridge.loadWorkspaceRoot()
	case keyUsage:
		u.bridge.loadUsage()
	case keySettings:
		u.modelInit = false
		u.bridge.loadModelConfig()
	}
	if p.kind == pagePanel {
		u.pageLoaded[p.key] = false
	}
}

// invalidate 请求 Gio 重绘下一帧。
func (u *UI) invalidate() { u.window.Invalidate() }

// attachExtraH 是附件 chip 行额外占用的高度（没有附件时为 0）。
func (u *UI) attachExtraH(gtx layout.Context) int {
	if len(u.atts) == 0 {
		return 0
	}
	// chip 24 + 上间距 8 + 工具行前间距多留 8，保证正文高度不被挤小。
	return gtx.Dp(unit.Dp(40))
}

// pickAttachments 弹系统文件选择框，选中的文件由后台线程读好后送回来。
func (u *UI) pickAttachments() {
	root := u.bridge.get().Workspace.Root
	u.bridge.pickAttachments(root)
}

// removeAttachment 从输入区移除一个附件。
func (u *UI) removeAttachment(i int) {
	if i < 0 || i >= len(u.atts) {
		return
	}
	u.atts = append(u.atts[:i], u.atts[i+1:]...)
}

// layout 是每帧入口。
func (u *UI) layout(gtx layout.Context) {
	// 后台选完的附件在这里并入界面状态（必须在 UI 线程改切片）。
	if got := u.bridge.drainAttachments(); len(got) > 0 {
		for _, a := range got {
			if len(u.atts) >= maxAttachments {
				break
			}
			dup := false
			for _, old := range u.atts {
				if old.Path == a.Path {
					dup = true
					break
				}
			}
			if !dup {
				u.atts = append(u.atts, a)
			}
		}
	}

	// 处理点击 / 提交事件
	u.handleEvents(gtx)

	// 全局背景：铺满整个约束区域
	fill(gtx, cBg)

	// 整窗尺寸固定
	W, H := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	navW := gtx.Dp(navWidth)
	// 右栏宽度按 DSH columns.ts 求解：默认占窗口 45%，夹在 300 ~ 窗口 70%。
	rightW := 0
	if u.showRight {
		rightW = int(float64(W) * panelDefRatio)
		if rightW < gtx.Dp(panelWidth) {
			rightW = gtx.Dp(panelWidth)
		}
		if maxR := int(float64(W) * panelMaxRatio); rightW > maxR {
			rightW = maxR
		}
	}
	titleH := gtx.Dp(titleBarHeight)
	barH := gtx.Dp(topBarHeight)
	// 有附件时输入卡片多一行 chip。
	inputH := gtx.Dp(inputMinHeight) + u.attachExtraH(gtx)
	// DSH CENTER_MIN = 400：右栏在场时中栏先保住 400，再决定右栏让出多少。
	midW := W - navW - rightW
	if midW < gtx.Dp(unit.Dp(400)) {
		midW = gtx.Dp(unit.Dp(400))
		rightW = W - navW - midW
		if rightW < 0 {
			rightW = 0
		}
	}
	// 内容区高度去掉标题栏；下面所有面板整体下移 titleH。
	bodyH := H - titleH
	if bodyH < 0 {
		bodyH = 0
	}
	chatH := bodyH - barH - inputH
	if chatH < 0 {
		chatH = 0
	}

	// 纯绝对定位：每个面板画在确定的像素矩形，互不干扰。
	// 用 layout.Flex 的 Rigid 会被录制-重放，嵌套时布局尺寸计算异常；
	// 这里直接用 op.Offset 定位，谁也不依赖谁的返回值。
	// 自绘标题栏横跨整窗，替代系统原生白色标题栏。
	// 直接画在主 ops（不经过 place 的宏录制），避免标题栏内部按钮的 op.Offset
	// 因宏嵌套而失效。标题栏本就位于 (0,0)，无需再偏移。
	tgtx := gtx
	tgtx.Constraints = layout.Exact(image.Pt(W, titleH))
	u.drawTitleBar(tgtx)
	u.place(gtx, 0, titleH, navW, bodyH, func(g layout.Context) { u.drawSidebar(g) })

	ap := u.activePage()
	// DSH 风格：侧栏已包含会话入口，内容区从侧栏右侧开始，不再单独占一列。
	contentX := navW
	contentW := midW

	// 中间列：顶栏 / 内容 / 输入框
	u.place(gtx, contentX, titleH, contentW, barH, func(g layout.Context) { u.drawTopBar(g) })
	u.place(gtx, contentX, titleH+barH, contentW, chatH, func(g layout.Context) {
		u.drawActivePage(g, ap)
	})
	if ap.kind == pageChat {
		u.composerBaseX = contentX
		u.place(gtx, contentX, titleH+barH+chatH, contentW, inputH, func(g layout.Context) { u.drawInput(g) })
	}
	if rightW > 0 {
		u.place(gtx, navW+midW, titleH, rightW, bodyH, func(g layout.Context) { u.drawRightPanel(g) })
	}

	// 输入区下拉菜单盖在页面之上、设置弹层之下。
	if u.composerMenu != 0 {
		u.drawComposerMenu(gtx)
	}

	// 设置弹层盖在页面上、重启提示之下。
	if u.showSettings {
		u.drawSettingsModal(gtx)
	}

	// 重启提示盖在所有面板之上：核心 250ms 后就会动手杀进程，
	// 必须先让用户看到这句话，否则画面就是毫无征兆地闪一下。
	if u.restarting {
		u.drawRestartOverlay(gtx)
	}

	// 插件 UI 贡献汇总回来了：重建侧栏导航与顶栏/脚部动作。
	if snapNow := u.bridge.get(); snapNow.ContribsRev != u.contribSeenRev {
		u.contribSeenRev = snapNow.ContribsRev
		u.contribLoading = false
		u.panelsDirty.Store(true)
	}

	// 插件页面导航有变化：在 UI 线程重建，避免后台线程改切片。
	if u.panelsDirty.Swap(false) {
		u.refreshPanelPages()
	}
}

// markPanelsDirty 请求下一帧重建插件页面导航（可安全地从后台线程调用）。
func (u *UI) markPanelsDirty() {
	u.panelsDirty.Store(true)
	u.invalidate()
}

// requestRestart 请求核心重启：重启后台与全部插件。
// 本界面也是插件，会被一起杀掉；新核心起来后会重新拉起本插件，窗口自动回来。
func (u *UI) requestRestart() {
	if u.restarting {
		return
	}
	u.restarting = true
	u.window.Invalidate()
	go func() {
		// 留一点时间把"正在重启"这一帧画出来，再发请求。
		time.Sleep(300 * time.Millisecond)
		if _, err := u.bridge.ctx.Capability("core.restart", map[string]any{}); err != nil {
			u.bridge.ctx.Log("error", "重启请求失败: "+err.Error(), nil)
		}
	}()
}

// openSettings 打开设置弹层：重置到第一分区，并重新拉取模型配置。
func (u *UI) openSettings() {
	u.showSettings = true
	u.settingsSec = 0
	u.modelInit = false
	u.rulesLoaded = false // 每次打开都重新拉一次规则，插件升级后内容能刷新
	if l := u.pageScroll["settings-modal"]; l != nil {
		l.Position = layout.Position{}
	}
	u.bridge.loadModelConfig()
	u.window.Invalidate()
}

func (u *UI) closeSettings() {
	u.showSettings = false
	u.window.Invalidate()
}

// handleSettingsModalEvents 只处理弹层内部控件。遮罩与关闭按钮都能关，
// 分区导航切左栏，其余交给模型表单。
func (u *UI) handleSettingsModalEvents(gtx layout.Context) {
	maskHit := u.settingsMaskT.Clicked(gtx) || u.settingsMaskB.Clicked(gtx) ||
		u.settingsMaskL.Clicked(gtx) || u.settingsMaskR.Clicked(gtx)
	if maskHit || u.settingsClose.Clicked(gtx) {
		u.closeSettings()
		return
	}
	for _, i := range u.liveSettingsSections() {
		if clickAt(&u.settingsNav, i).Clicked(gtx) && u.settingsSec != i {
			u.settingsSec = i
			// 从侧栏移进来的两个分区：切进来时按需加载数据。
			switch i {
			case settingsBudgetSec:
				u.budgetInit = false
				u.bridge.loadBudget()
			case settingsUsageSec:
				u.bridge.loadUsage()
			}
			// 换分区回到顶部：各分区共用同一个滚动容器，不重置会停在上一段的中间。
			if l := u.pageScroll["settings-modal"]; l != nil {
				l.Position = layout.Position{}
			}
		}
	}
	switch u.settingsSec {
	case 0:
		u.handleModelEvents(gtx)
	case 1:
		if u.settingsRestart.Clicked(gtx) {
			u.requestRestart()
		}
	case settingsBudgetSec:
		u.handleBudgetEvents(gtx)
	case settingsUsageSec:
		u.handleUsageEvents(gtx)
	}
}

// drawRestartOverlay 全屏半透明遮罩 + 提示文字。
func (u *UI) drawRestartOverlay(gtx layout.Context) layout.Dimensions {
	sz := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, color.NRGBA{R: 8, G: 10, B: 14, A: 210}, clip.Rect{Max: sz}.Op())
	gtx.Constraints = layout.Exact(sz)
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return u.pill(gtx, "⟳ 正在重启后台与全部插件…", cPanelAlt, cText)
	})
}

// drawActivePage 按当前页类型分发内容区绘制。
func (u *UI) drawActivePage(gtx layout.Context, ap page) {
	switch ap.key {
	case keyChat:
		u.drawChat(gtx)
	case keyRole:
		u.drawRolePage(gtx)
	case keyBudget:
		u.drawBudgetPage(gtx)
	case keyImageGen:
		u.drawImageGenPage(gtx)
	case keyPlugins:
		u.drawPluginsPage(gtx)
	case keyWorkspace:
		u.drawWorkspacePage(gtx)
	case keyUsage:
		u.drawUsagePage(gtx)
	default:
		if ap.kind == pagePanel {
			u.drawPanelPage(gtx, ap)
		} else {
			fill(gtx, cBg)
		}
	}
}

// place 在 (x,y) 处放置一个 w×h 的面板。面板绘制被录制后以 Offset 重放到确定位置。
func (u *UI) place(gtx layout.Context, x, y, w, h int, draw func(layout.Context)) {
	if w <= 0 || h <= 0 {
		return
	}
	macro := op.Record(gtx.Ops)
	c := gtx
	c.Constraints = layout.Exact(image.Pt(w, h))
	draw(c)
	body := macro.Stop()
	off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
	body.Add(gtx.Ops)
	off.Pop()
}

// vscroll 把整页内容放进一个可滚动列表。
// layout.List 给子项 Max.Y = 1e6，所以单个超高子项也能滚（Gio list.go:118）。
// 因此页面内容里不能再出现竖向 Flexed —— Flexed 会吃掉这 1e6，必须用 Rigid。
// key 用于区分页面，让每页各自记住滚动位置。
func (u *UI) vscroll(gtx layout.Context, key string, content layout.Widget) layout.Dimensions {
	l := u.pageScroll[key]
	if l == nil {
		l = &layout.List{Axis: layout.Vertical}
		u.pageScroll[key] = l
	}
	return l.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
		// DSH 设置分区统一 max-width 760；功能页共用这条阅读宽度轴。
		return u.centerWidth(gtx, gtx.Dp(760), content)
	})
}

// centerWidth 在可用宽度内居中一列，最大宽度 maxW（对齐 DSH 的
// --dsh-chat-content-width：正文列 920、输入卡片 952）。
// 只约束 X，不碰 Y——调用点可能在 vscroll 的子项里，Y 是 1e6。
func (u *UI) centerWidth(gtx layout.Context, maxW int, content layout.Widget) layout.Dimensions {
	W := gtx.Constraints.Max.X
	w := W
	if maxW > 0 && w > maxW {
		w = maxW
	}
	c := gtx
	c.Constraints.Min.X = 0
	c.Constraints.Max.X = w
	macro := op.Record(gtx.Ops)
	dims := content(c)
	body := macro.Stop()
	off := op.Offset(image.Pt((W-w)/2, 0)).Push(gtx.Ops)
	body.Add(gtx.Ops)
	off.Pop()
	dims.Size.X = W
	return dims
}

// clickAt 取第 i 个可点项，不够长就补齐。
// 快照由后台协程更新，绘制时的条目数可能比事件处理时多；
// 绘制里直接取下标会 index out of range（侧栏/工作区都踩过），必须按需扩容。
func clickAt(s *[]widget.Clickable, i int) *widget.Clickable {
	for len(*s) <= i {
		*s = append(*s, widget.Clickable{})
	}
	return &(*s)[i]
}

// vstack 把一串子项竖排。页面内不要再嵌套 layout.List：
// 内层 list 会拿到 1e6 视口、没有滚动范围，却会吞掉滚轮事件，
// 导致外层页面滚不动。滚动统一交给 vscroll。
func vstack(gtx layout.Context, items []layout.Widget) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(items))
	for _, it := range items {
		children = append(children, layout.Rigid(it))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// flow 简易流式布局：子项按可用宽度自动换行，用于标签/chips，
// 避免单行横向 Flex 超出内容宽度被截断。
func flow(gtx layout.Context, gap int, items []layout.Widget) layout.Dimensions {
	maxX := gtx.Constraints.Max.X
	x, y, lineH := 0, 0, 0
	for _, it := range items {
		m := op.Record(gtx.Ops)
		d := it(gtx)
		call := m.Stop()
		if x > 0 && x+d.Size.X > maxX {
			x = 0
			y += lineH + gap
			lineH = 0
		}
		off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		off.Pop()
		x += d.Size.X + gap
		if d.Size.Y > lineH {
			lineH = d.Size.Y
		}
	}
	return layout.Dimensions{Size: image.Pt(maxX, y+lineH)}
}

// handleEvents 处理所有控件事件。
func (u *UI) handleEvents(gtx layout.Context) {
	// 自绘标题栏的窗口按钮。
	if u.winClose.Clicked(gtx) {
		u.window.Perform(system.ActionClose)
	}
	if u.winMin.Clicked(gtx) {
		u.window.Perform(system.ActionMinimize)
	}
	if u.winMax.Clicked(gtx) {
		if u.maximized {
			u.window.Perform(system.ActionUnmaximize)
		} else {
			u.window.Perform(system.ActionMaximize)
		}
	}
	// 顶栏：展开/收起右侧详情面板。
	if u.toggleRight.Clicked(gtx) {
		u.showRight = !u.showRight
	}
	// 顶栏「重启」：必须在这里（绘制之前）读取点击。
	// Clickable.Layout 内部会把事件队列里积压的点击全部读走，
	// 在绘制回调里再调 Clicked 永远是 false —— 之前那个"点了没反应"就是它。
	if u.restartBtn.Clicked(gtx) {
		u.requestRestart()
	}
	// 设置弹层打开时，遮罩吃掉其余页面的输入；只处理弹层内部事件。
	if u.showSettings {
		u.handleSettingsModalEvents(gtx)
		return
	}
	// 输入区的下拉菜单打开时，它盖住页面：先处理菜单项，再吃掉其余点击。
	if u.composerMenu != 0 {
		u.handleComposerMenu(gtx)
		return
	}

	// 插件注册的脚部 / 顶栏动作：任何页面都能点（贡献条目自带页面目标或动作）。
	u.handleContributionClicks(gtx)

	// 左侧导航切换
	for i := range u.pages {
		if u.pages[i].click.Clicked(gtx) {
			if u.pages[i].key == keySettings {
				u.openSettings()
				break
			}
			u.activeKey = u.pages[i].key
			u.onPageEnter(u.pages[i])
			break
		}
	}

	ap := u.activePage()
	snap := u.bridge.get()

	// 侧栏会话入口：在任何页面都可以点，点了回到对话页。
	if len(u.sidebarSessionClicks) < len(snap.Sessions) {
		u.sidebarSessionClicks = append(u.sidebarSessionClicks, make([]widget.Clickable, len(snap.Sessions)-len(u.sidebarSessionClicks))...)
	}
	for i := range snap.Sessions {
		if i < len(u.sidebarSessionClicks) && u.sidebarSessionClicks[i].Clicked(gtx) {
			u.bridge.switchSession(snap.Sessions[i].ID)
			u.activeKey = keyChat
			break
		}
	}
	// 侧栏工作区目录：点击进入工作区页并 browsing 到该目录。
	if len(u.sidebarWsClicks) < len(snap.Workspace.Entries) {
		u.sidebarWsClicks = append(u.sidebarWsClicks, make([]widget.Clickable, len(snap.Workspace.Entries)-len(u.sidebarWsClicks))...)
	}
	for i, e := range snap.Workspace.Entries {
		if e.Dir && i < len(u.sidebarWsClicks) && u.sidebarWsClicks[i].Clicked(gtx) {
			u.activeKey = keyWorkspace
			u.onPageEnter(u.pages[u.pageIndex(keyWorkspace)])
			u.bridge.browseWorkspace(e.Path)
			break
		}
	}

	if ap.kind == pageChat {
		// 输入区选择器：点开对应下拉。
		for i := 1; i <= 5; i++ {
			if clickAt(&u.menuPills, i-1).Clicked(gtx) {
				u.openComposerMenu(i)
				return
			}
		}
		// 附件：+ 选文件，附件 chip 点击移除。
		if u.attachBtn.Clicked(gtx) {
			u.pickAttachments()
			return
		}
		for i := range u.atts {
			if clickAt(&u.attachRemove, i).Clicked(gtx) {
				u.removeAttachment(i)
				return
			}
		}
		if u.newSession.Clicked(gtx) {
			u.bridge.newSession()
		}
		// 欢迎页快捷提问：填入输入框，用户可继续编辑再发送。
		if len(u.chatSuggestBtns) < len(chatSuggestions) {
			u.chatSuggestBtns = append(u.chatSuggestBtns, make([]widget.Clickable, len(chatSuggestions)-len(u.chatSuggestBtns))...)
		}
		for i := range chatSuggestions {
			if i < len(u.chatSuggestBtns) && u.chatSuggestBtns[i].Clicked(gtx) {
				u.editor.SetText(chatSuggestions[i])
				u.focusInput = true
			}
		}
		// 输入框提交 / 发送按钮
		send := u.send.Clicked(gtx)
		for {
			ev, ok := u.editor.Update(gtx)
			if !ok {
				break
			}
			if _, submitted := ev.(widget.SubmitEvent); submitted {
				send = true
			}
		}
		if send {
			text := strings.TrimSpace(u.editor.Text())
			// 只带附件不写字也是合理用法（"看看这个"）。
			if text != "" || len(u.atts) > 0 {
				u.bridge.send(text, u.workMode, u.reasoning, u.permission, u.atts)
				u.editor.SetText("")
				u.atts = nil
			}
		}
		return
	}

	// 功能页交互
	switch ap.key {
	case keySettings:
		u.handleModelEvents(gtx)
		if u.settingsRestart.Clicked(gtx) {
			u.requestRestart()
		}
	case keyRole:
		u.handleRoleEvents(gtx)
	case keyBudget:
		u.handleBudgetEvents(gtx)
	case keyImageGen:
		u.handleImageGenEvents(gtx)
	case keyPlugins:
		u.handlePluginEvents(gtx)
	case keyWorkspace:
		u.handleWorkspaceEvents(gtx, snap)
	case keyUsage:
		u.handleUsageEvents(gtx)
	default:
		if ap.kind == pagePanel {
			u.handlePanelEvents(gtx, ap)
		}
	}
}

// onPageEnter 进入页面时按需触发数据加载。
func (u *UI) onPageEnter(p page) {
	switch p.key {
	case keyRole:
		u.roleInit = false
		u.bridge.loadRoles()
	case keyBudget:
		u.budgetInit = false
		u.bridge.loadBudget()
	case keyImageGen:
		u.imageInit = false
		u.bridge.loadImageGen()
	case keyPlugins:
		// 每次从侧栏进入插件页都回到列表，不停在上次看的详情上。
		u.pluginDetailID = ""
		u.pluginDelConfirm = false
		u.bridge.loadPluginMgr()
	case keyWorkspace:
		u.bridge.loadWorkspaceRoot()
	case keyUsage:
		u.bridge.loadUsage()
	case keySettings:
		u.modelInit = false
		u.bridge.loadModelConfig()
	}
	if p.kind == pagePanel {
		u.ensurePanel(p)
	}
}

// ---------- 绘图辅助 ----------

func fill(gtx layout.Context, col color.NRGBA) {
	// 用 Max：各绘制函数都已先设 Exact(w,h)，此时 Min==Max 即目标尺寸。
	// 绝不能用 Min 或"Min 为 0 时回退 Max"——在 Flex 的 Rigid 里 Min 恒为 0，
	// 回退会画出铺满剩余空间的巨幅背景，盖住后面重放的内容。
	paint.FillShape(gtx.Ops, col, clip.Rect{Max: gtx.Constraints.Max}.Op())
}

// drawBox 画一个"背景在下、内容在上"的块。
// Gio 后画的 op 覆盖先画的：直接 layout 内容再画背景会把文字盖住。
// 这里先把内容录制成宏、量出尺寸，画完背景再重放内容，顺序就对了。
func drawBox(gtx layout.Context, col color.NRGBA, content layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := content(gtx)
	body := macro.Stop()
	paint.FillShape(gtx.Ops, col, clip.Rect{Max: dims.Size}.Op())
	body.Add(gtx.Ops)
	return dims
}

// drawRBox 同 drawBox，但背景是圆角。
func drawRBox(gtx layout.Context, col color.NRGBA, radius unit.Dp, content layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := content(gtx)
	body := macro.Stop()
	r := gtx.Dp(radius)
	rr := clip.RRect{Rect: image.Rectangle{Max: dims.Size}, SE: r, SW: r, NE: r, NW: r}
	paint.FillShape(gtx.Ops, col, rr.Op(gtx.Ops))
	body.Add(gtx.Ops)
	return dims
}

// drawRBoxFill 画一块已知尺寸的圆角填充（不依赖内容尺寸）。
func drawRBoxFill(gtx layout.Context, col color.NRGBA, radius unit.Dp, w, h int) {
	r := gtx.Dp(radius)
	rr := clip.RRect{Rect: image.Rectangle{Max: image.Pt(w, h)}, SE: r, SW: r, NE: r, NW: r}
	paint.FillShape(gtx.Ops, col, rr.Op(gtx.Ops))
}

// drawRBoxOutline 圆角描边：先铺一层描边色，再内缩 1px 铺背景色。
// Gio 没有圆角描边 API，用双层填充近似 DSH 的 0.5px 描边。
func drawRBoxOutline(gtx layout.Context, bg, border color.NRGBA, radius unit.Dp, w, h int) {
	r := gtx.Dp(radius)
	paint.FillShape(gtx.Ops, border, clip.RRect{
		Rect: image.Rectangle{Max: image.Pt(w, h)}, SE: r, SW: r, NE: r, NW: r,
	}.Op(gtx.Ops))
	r2 := r - 1
	if r2 < 0 {
		r2 = 0
	}
	paint.FillShape(gtx.Ops, bg, clip.RRect{
		Rect: image.Rect(1, 1, w-1, h-1), SE: r2, SW: r2, NE: r2, NW: r2,
	}.Op(gtx.Ops))
}

// fillDim 按给定尺寸画背景（用于列表项等按内容尺寸而非约束尺寸绘制的场景）。
func fillDim(gtx layout.Context, col color.NRGBA, size image.Point) {
	paint.FillShape(gtx.Ops, col, clip.Rect{Max: size}.Op())
}

func label(gtx layout.Context, th *material.Theme, txt string, size unit.Sp, col color.NRGBA) layout.Dimensions {
	l := material.Label(th, size, txt)
	l.Color = col
	l.Font.Weight = font.Normal
	return l.Layout(gtx)
}

func centerLabel(gtx layout.Context, th *material.Theme, txt string, size unit.Sp, col color.NRGBA) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, txt, size, col)
	})
}
