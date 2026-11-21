package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"aiwork/sdk/go/plugin"
)

// backend 是界面需要的能力。抽成接口是为了能用假实现驱动整个主循环。
type backend interface {
	probeServices() []serviceStatus
	scanPlugins() ([]pluginInfo, error)
	wsBrowse(path string) (wsListing, error)
	readAttachment(path string) (attachment, error)
	readImageBytes(path string) ([]byte, error)
	openPath(path string) (string, error)
	// 插件自带的页面（ui.page.*）。界面只借一块地方给它：问清单、要素材、
	// 转发点击与按键 —— 至于页面上有什么、点了会怎样，这里一概不知道。
	pageList(services []string) ([]pagePanel, error)
	pageRender(svc, id string, w, h, scroll int) (plugin.PanelRender, error)
	pageAction(svc, id, action, arg string) (string, error)
	pageKey(svc, id, kind, text string) (pageKeyResult, error)
	// 插件自带的浮窗（ui.widget.*）：问有哪些、要素材。位置归界面管，内容归插件。
	widgetList(svc string) ([]plugin.WidgetInfo, error)
	widgetRender(svc, id string) (plugin.WidgetRender, error)
	widgetPos(svc, id string) (x, y int, fold bool)

	chat(text string) (string, string, error)

	// 链式对话（conversation.v1）。装了它，聊天页就是可回滚、可翻版本的消息树；
	// 没装则退回上面那条 chat 的直线路径。
	convSupported() bool
	convList() (convSnapshot, error)
	convNew() (convSnapshot, error)
	convDelete(sessionID string) (convSnapshot, error)
	convSwitch(sessionID string) (convSnapshot, error)
	convSend(sessionID, text string, atts []attachment) (convSnapshot, error)
	convEdit(sessionID, nodeID, text string) (convSnapshot, error)
	convRegenerate(sessionID, nodeID string) (convSnapshot, error)
	convPage(sessionID, nodeID string, delta int) (convSnapshot, error)
	convMarkdown(sessionID string) (string, error)
	convExport(sessionID string) (string, error)

	// 重启核心：插件的启停 / 删除都只做标记、标着"重启后生效"（规范 2.6），
	// 得有个地方能按这一下。
	restart() (string, error)
}

// convSnapshot 是 conversation.v1 的一次返回：一条时间线 + 会话清单。
type convSnapshot struct {
	sessionID string
	name      string
	sessions  []sessionInfo
	msgs      []msgView
	note      string // 插件给的提示（例如「末尾那条生成失败了，可以重新生成」）
}

// wsListing 是一次目录浏览的结果（文件页用）。
type wsListing struct {
	Path    string
	Parent  string
	Root    string // 当前工作区，可能为空
	Entries []dirEntry
	Note    string
}

// outcome 是后台任务完成后投递给主循环的结果。
//
// 一个结构体装所有类型，是因为投递通道只有一个 —— 界面线程顺序消费，
// 天然避免了并发改状态。
type outcome struct {
	kind     string
	reply    string
	via      string
	note     string
	err      error
	services []serviceStatus
	plugins  []pluginInfo
	quiet    bool // 自动刷新不覆盖底部状态
	ws       *wsListing
	conv     *convSnapshot
	att      *attachment

	// 插件自带页面
	pages    []pagePanel
	pageKey  string  // 这次回来的是哪一份缓存键
	pageReq  pageReq // 它是为多大地方、滚到哪儿要的
	pageOut  plugin.PanelRender
	pageNote string // 动作返回的一句话

	// 缩略图读完之后回传的内容
	thumbPath  string
	thumbLines []string
	thumbFail  bool

	// 插件自带浮窗（ui.widget.*）
	widgetSvc   string
	widgetInfos []plugin.WidgetInfo
	widgetIdx   int
	widgetOut   plugin.WidgetRender
}

// app 驱动整个界面。
//
// 所有 state 的修改都发生在 loop 这一个 goroutine 上：外部（RPC、后台任务）
// 通过 post / results 投递，因此界面状态不需要任何锁。
type app struct {
	plug backend
	st   *state
	term *terminal

	canv       *canvas
	lastFrame  string
	lastCursor string

	events    <-chan event
	stopInput func()
	apply     chan func()
	results   chan outcome

	// pageKeys 是"送给插件页面的按键"队列。
	//
	// 必须是一条队列、一个消费者：两个并发的 RPC 谁先写进管道是不确定的，
	// 各起一个 goroutine 打字就会串成乱码（见 startPageKeys）。
	pageKeys chan pageKeyMsg

	// rescanSeq 是防抖用的序号：每次插件状态变化都让它前进，
	// 只有最后一个计时器真正去刷新（见 scheduleRescan）。
	rescanSeq int

	// holdEOF 是**开发期测试钩子**：输入源读到 EOF 后不退出，继续渲染。
	//
	// 用途：把 AIWORK_UI_TTY 指向一个预置好的文件时，文件是瞬间读完的，
	// 界面会在后台数据（插件清单、LLM 配置）回来之前就退出，没法观察到加载完成后的画面。
	// 打开它就能"喂完输入后停下来截图"。真终端下永远不会走到这条路。
	holdEOF bool

	done     chan struct{}
	stopOnce sync.Once

	// stopped 在主循环真正退出时关闭。
	//
	// 退出要还原终端，而主循环此刻可能正画到一半 —— 这时候写还原序列，
	// 会有半帧界面留在主屏上。谁来还原都得先等这个信号。
	stopped chan struct{}
}

func newApp(term *terminal, plug backend, coreVersion, apiVersion, pluginID string) *app {
	return &app{
		plug: plug,
		term: term,
		st: &state{
			coreVersion: coreVersion,
			apiVersion:  apiVersion,
			pluginID:    pluginID,
			tab:         tabChat,
			focus:       "chat",
			status:      "正在读取插件与配置…",
			msgs: []msgView{{
				role: "系统",
				at:   nowHM(),
				text: "极光界面已就绪。\n" +
					"· 上方标签可点击：聊天 + 各插件自带的一页（插件 / 工作区 / 角色 / 模型…），装一个带界面的插件就多一页\n" +
					"· 每条消息都是一个框：框上有「修改 / 重新生成 / 分享」，下角的 ‹ n/n › 翻版本\n" +
					"· 修改或重新生成会开出一条新时间线，旧的那条一条都不会删\n" +
					"· 回车换行，点「发送」或 Ctrl+Enter 送出 · Ctrl+C 复制 · Ctrl+Q 退出",
			}},
		},
		apply:    make(chan func(), 64),
		results:  make(chan outcome, 16),
		pageKeys: make(chan pageKeyMsg, 256),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
		holdEOF:  os.Getenv("AIWORK_UI_HOLD_ON_EOF") != "",
	}
}

// Start 进入事件循环（阻塞，应在 goroutine 中调用）。
func (a *app) Start() {
	a.events, a.stopInput = startInput(a.term, a.holdEOF)
	a.startPageKeys()
	defer a.Stop()

	a.kickLoads()
	a.loop()
}

// Stop 让事件循环退出。可重复调用。
func (a *app) Stop() {
	a.stopOnce.Do(func() {
		close(a.done)
		if a.stopInput != nil {
			a.stopInput()
		}
	})
}

// waitStopped 等主循环真的退出，最多等 d。
//
// 退出前要还原终端，而主循环可能正画到一半：那时写还原序列会把半帧界面
// 留在主屏上。所以还原之前先等一下；等不到（卡住了）也得往下走 ——
// 留半帧也比把终端扔在原始模式里强。
func (a *app) waitStopped(d time.Duration) {
	select {
	case <-a.stopped:
	case <-time.After(d):
	}
}

// post 把一次状态修改投递到界面线程。
func (a *app) post(fn func()) {
	select {
	case a.apply <- fn:
	case <-a.done:
	}
}

// rescanDebounce 是插件状态变化后的合并窗口。
//
// 启动时十几个插件会在两百毫秒内接连激活。每个事件都去扫一遍插件目录，
// 就是十几次文件读取加几十次服务查找 —— 等安静下来再扫一次就够。
const rescanDebounce = 300 * time.Millisecond

// rescanInterval 是"周期兜底重扫"的间隔。
//
// 只补 plugin.deleted / plugin.state 事件盖不到的洞：直接删插件目录、或者事件
// 在订阅之前就丢了。扫描本身很轻（读十几个 manifest + 探测服务），3 秒一次
// 用户无感，却能让"删了却还赖着"的页面最多多活 3 秒。
const rescanInterval = 3 * time.Second

// scheduleRescan 在插件状态变化后安排一次快照刷新。
//
// 为什么必须有这一步：插件是按目录名字母序逐个激活的，界面自己也是其中一个。
// 排在我后面的插件（比如 workspace.plugin，works 排在 workb 之后）激活时，
// 我早就扫完目录了 —— 手快的那一次扫描看不到它，于是它被永久显示成「未运行」，
// 非要用户手点一次「重新扫描」才对得上。
func (a *app) scheduleRescan() {
	a.post(func() {
		a.rescanSeq++
		seq := a.rescanSeq
		go func() {
			select {
			case <-time.After(rescanDebounce):
			case <-a.done:
				return
			}
			a.post(func() {
				if a.rescanSeq != seq {
					return // 这期间又有插件动静，交给后面那一次去刷
				}
				a.rescan(true)
			})
		}()
	})
}

// rescan 重新探测链路与服务清单。
//
// quiet=true 是自动刷新：不动底部状态栏。背景行为不该盖掉用户正在看的提示
// （比如刚保存完的「密钥已写入」）。手动点「重新扫描」才是有反馈的那一次。
func (a *app) rescan(quiet bool) {
	if !quiet {
		a.st.status = "正在重新扫描…"
		a.st.statusOK = true
	}
	go func() {
		ps, err := a.plug.scanPlugins()
		a.results <- outcome{kind: "plugins", plugins: ps, err: err, quiet: quiet}
	}()
	go func() {
		a.results <- outcome{kind: "services", services: a.plug.probeServices()}
	}()
}

// PushNotice 让别的插件往对话区推一条提示（ui.v1.notify）。
//
// 它是本地消息（id 为空），所以只画成一个不带按钮的框，也不会进会话树；
// 下一次时间线刷新时它就随整屏一起被换掉。
func (a *app) PushNotice(text string) {
	a.post(func() {
		a.st.msgs = append(a.st.msgs, msgView{role: "系统", at: nowHM(), text: text})
	})
}

// ---------------------------------------------------------------------------
// 主循环
// ---------------------------------------------------------------------------

func (a *app) loop() {
	ticker := time.NewTicker(110 * time.Millisecond)
	defer ticker.Stop()
	// 周期兜底重扫：插件目录可能被"外部"改动（比如直接在文件系统里删了某个
	// 插件的目录），那一下没有 plugin.deleted 事件，界面不会知道。每隔几秒
	// 主动扫一次，最多延迟一个周期就能把消失插件的页面摘掉。
	rescanTicker := time.NewTicker(rescanInterval)
	defer rescanTicker.Stop()
	defer close(a.stopped) // 退出已生效：外面可以安全地还原终端了

	a.render()
	for {
		select {
		case ev, ok := <-a.events:
			if !ok {
				return // 输入源结束（例如重定向文件读到 EOF）
			}
			if quit := a.handle(ev); quit {
				return
			}
			a.render()

		case out := <-a.results:
			a.applyOutcome(out)
			a.render()

		case fn := <-a.apply:
			fn()
			a.render()

		case <-a.done:
			return

		case <-ticker.C:
			if a.st.busy {
				a.st.spinner++
				a.render()
			}

		case <-rescanTicker.C:
			a.scheduleRescan()
		}
	}
}

// kickLoads 在后台把三份数据拉回来，绝不阻塞首帧渲染。
func (a *app) kickLoads() {
	go func() {
		a.results <- outcome{kind: "services", services: a.plug.probeServices()}
	}()
	go a.refreshPlugins()
	// 内部各自起 goroutine；必须在这里同步调用 ——
	// kickLoads 跑在主循环启动之前，界面状态只能由这一个 goroutine 碰。
	a.loadConversation()
	// 插件自带的页面：清单是从插件清单（manifest）里挑出来的，已经扫好了，
	// 这里补上各页自报的标题。问不到也不要紧 —— 服务快照回来时会再补问一次。
	a.loadPages()
}

func (a *app) refreshPlugins() {
	ps, err := a.plug.scanPlugins()
	a.results <- outcome{kind: "plugins", plugins: ps, err: err}
}

// ---------------------------------------------------------------------------
// 链式对话
//
// 全部异步：一次 send / regenerate 可能跑几分钟（模型还要调工具），
// 期间界面必须还能翻版本、切会话、滚历史。状态改动照例只发生在主循环上。
// ---------------------------------------------------------------------------

// loadConversation 探测并把当前时间线拉回来。
//
// 没装 conversation.v1 就明说一句，然后退回原有的一条直线聊天 ——
// 不能因为少装一个插件就让人以为聊天坏了。
//
// 探测要退避重试：界面可能比 conversation 插件先起来（插件是逐个启动的），
// 那一次调用会撞上「服务未找到」。跟 browseTo 一样，别把「没装插件」摆给用户看。
// 整段跑在自己的 goroutine 里，Ctrl+L 重新拉时间线时不会卡住主循环。
func (a *app) loadConversation() {
	go func() {
		for attempt := 0; attempt < 4; attempt++ {
			if a.plug.convSupported() {
				s, err := a.plug.convList()
				a.results <- outcome{kind: "conv", conv: &s, err: err}
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		a.results <- outcome{kind: "convoff"}
	}()
}

// convRun 跑一次会改动会话树的调用（发消息 / 修改 / 重新生成 / 翻版本 / 切会话）。
//
// wait=true 才显示「正在生成…」占位框：只有真的会去调模型的那几种才需要它，
// 翻版本、切会话是瞬间的事，摆一个转圈反而像卡住了。
// convBusy 在主循环里同步置位，所以连点两下也只会发出一次。
func (a *app) convRun(wait bool, fn func() (convSnapshot, error)) {
	if a.st.convBusy {
		a.st.notice = "上一步还没结束，稍等一下"
		a.st.noticeOK = false
		return
	}
	a.st.convBusy = true
	a.st.busy = wait
	a.st.spinner = 0
	if wait {
		// 真的会去调模型的那一次才开新过程：翻版本、切会话不该把上一次的记录冲掉
		a.st.resetTrace()
	}
	go func() {
		s, err := fn()
		a.results <- outcome{kind: "conv", conv: &s, err: err}
	}()
}

func (a *app) applyOutcome(out outcome) {
	switch out.kind {
	case "services":
		a.st.services = out.services

	case "plugins":
		if out.err != nil {
			// 自动刷新失败不打扰用户：下一轮还会再扫，而且这影响的只是
			// "标签栏能不能认出插件自带的那几页"，不影响手头正在做的事。
			if !out.quiet {
				a.st.status = "读取插件目录失败：" + out.err.Error()
				a.st.statusOK = false
			}
			return
		}
		a.st.plugins = out.plugins
		// 滤掉本次运行里"已经被删掉"的插件：删除是重启生效的，目录此刻可能
		// 还在（deferred），不滤掉的话下一轮扫描又会把它的页面加回来。
		if len(a.st.droppedPlugins) > 0 {
			kept := a.st.plugins[:0]
			for _, p := range a.st.plugins {
				if a.st.droppedPlugins[p.ID] || a.st.droppedPlugins[p.Dir] {
					continue
				}
				kept = append(kept, p)
			}
			a.st.plugins = kept
		}
		// 带页面的那组服务变过（插件装上了 / 被删掉了 / 崩了）才重新问一次清单。
		//
		// 判据必须是"变过"而不是"为空"：插件是逐个激活的，界面可能比它先起来，
		// 所以要补问；可删掉一个插件之后清单非空，按"为空才问"就再也不问了 ——
		// 那一页会永远挂在标签栏上，点进去只剩一句"插件不在了"。
		//
		// 比的是"有哪些服务声称自己带页面"，不是"问回来的页码"：后者对
		// 一个报了服务却一页都不给的插件会永远不相等，于是每次扫描都去问一轮。
		if svcs := pageServices(a.st.plugins); !sameStrings(svcs, a.st.pagesSeen) {
			a.st.pagesSeen = svcs
			a.loadPages()
		}
		// 浮窗清单也跟着插件清单对齐：谁声明了 ui.widget.* 就给谁挂浮窗
		a.syncWidgets()

	case "widgets":
		if out.err != nil {
			a.st.status = "读取浮窗清单失败：" + out.err.Error()
			a.st.statusOK = false
			return
		}
		a.applyWidgets(out.widgetSvc, out.widgetInfos)

	case "widgetrender":
		a.applyWidgetRender(out.widgetIdx, out.widgetOut, out.err)

	case "pages":
		a.st.pagesBusy = false
		if out.err != nil {
			// 没问成就不算"问过"：把判据清掉，下一轮扫描还会再试。
			// 不然一次抖动就会让标签栏空着，直到哪天有个插件的服务名变了为止。
			a.st.pagesSeen = nil
			a.st.status = "读取插件页面失败：" + out.err.Error()
			a.st.statusOK = false
			return
		}
		a.st.pages = out.pages
		a.st.pageTitle = ""
		// 清单变了，手上那些帧可能属于已经不在的页面：只留还活着的，其余连
		// **在途的**一起丢掉 —— 一份"删除之前"的帧回来又被当成新的存进去，
		// 下一帧还会照它画，那就成了"删了还在"。
		alive := make(map[string]bool, len(out.pages))
		for _, p := range out.pages {
			alive[p.ref()] = true
		}
		for k := range a.st.pageCache {
			if !pageKeyAlive(k, alive) {
				delete(a.st.pageCache, k)
			}
		}
		for k := range a.st.pagePending {
			if !pageKeyAlive(k, alive) {
				delete(a.st.pagePending, k)
			}
		}
		if a.st.tab == tabPanel {
			if _, ok := a.st.activePage(); !ok {
				// 正看着的那一页没了（插件被删或被禁用）：退回聊天。
				// 留在一块空白上，比什么都不做更让人摸不着头脑。
				name := pageRefName(a.st)
				a.setTab(tabChat)
				a.st.notice = "「" + name + "」这一页的插件不在了，已回到聊天"
				a.st.noticeOK = false
			}
		}

	case "page":
		delete(a.st.pagePending, out.pageKey)
		if a.st.pageCache == nil {
			a.st.pageCache = map[string]*pageRender{}
		}
		if out.err != nil {
			// 失败也存成一帧：每帧都重试等于把插件按在地上反复打
			a.st.pageCache[out.pageKey] = pageErrorView(out.pageReq, out.err)
			return
		}
		v := &pageRender{
			svc: out.pageReq.svc, id: out.pageReq.id,
			w: out.pageReq.w, h: out.pageReq.h, scroll: out.pageReq.scroll,
			out: out.pageOut,
		}
		a.st.pageCache[out.pageKey] = v

		// 滚动位置以插件夹过的那个为准：不认它的话，宿主会一直按一个越界的
		// 位置去要帧 —— 每次都白跑一趟，而且永远画不出新内容。
		if a.st.tab == tabPanel && a.st.pageSvc == v.svc && a.st.pageID == v.id {
			a.st.pageScroll = v.out.Scroll
			a.st.pageMax = v.out.MaxScroll
		}

	case "pageact":
		if out.err != nil {
			a.st.status = "页面操作失败：" + out.err.Error()
			a.st.statusOK = false
			return
		}
		if out.pageNote != "" {
			a.st.status = out.pageNote
			a.st.statusOK = true
		}

	case "attach":
		a.st.attachBusy = false
		if out.err != nil {
			a.st.notice = "读目录失败：" + out.err.Error()
			a.st.noticeOK = false
			return
		}
		if out.ws != nil {
			a.st.attachPath = out.ws.Path
			a.st.attachParent = out.ws.Parent
			a.st.attachEntries = out.ws.Entries
			a.st.attachSel = 0
			a.st.attachScroll = 0
		}

	case "attachdrop":
		a.st.attachBusy = false
		if out.err != nil {
			// 猜错了就直说，别让用户以为回车没反应 —— 内容还在输入框里，能改能重发
			a.st.notice = "这不像能读的文件（" + out.err.Error() + "）；再按一次回车就按文本发出去"
			a.st.noticeOK = false
			return
		}
		if out.att != nil {
			a.st.attachments = append(a.st.attachments, *out.att)
			a.st.input = nil
			a.st.cursor = 0
			a.st.notice = fmt.Sprintf("已把 %s 加成附件（共 %d 个）；再按一次回车发送",
				out.att.Name, len(a.st.attachments))
			a.st.noticeOK = true
		}

	case "thumb":
		if a.st.thumbs == nil {
			a.st.thumbs = map[string]*thumbEntry{}
		}
		a.st.thumbs[out.thumbPath] = &thumbEntry{lines: out.thumbLines, failed: out.thumbFail}

	case "openimg":
		if out.err != nil {
			a.st.notice = "打开失败：" + out.err.Error()
			a.st.noticeOK = false
			return
		}
		a.st.notice = out.note
		a.st.noticeOK = true

	case "attachone":
		a.st.attachBusy = false
		if out.err != nil {
			// 单个文件失败不该把整个面板关掉，用户还想挑下一个
			a.st.notice = "加不了这个文件：" + out.err.Error()
			a.st.noticeOK = false
			return
		}
		if out.att != nil {
			for _, have := range a.st.attachments {
				if have.Path == out.att.Path {
					a.st.notice = "这个文件已经在列表里了：" + out.att.Name
					a.st.noticeOK = true
					return
				}
			}
			a.st.attachments = append(a.st.attachments, *out.att)
			a.st.notice = fmt.Sprintf("已加入 %s（共 %d 个）", out.att.Name, len(a.st.attachments))
			a.st.noticeOK = true
		}

	case "status":
		a.st.status = out.note
		a.st.statusOK = out.err == nil

	case "chat":
		a.st.busy = false
		if out.err != nil {
			a.st.msgs = append(a.st.msgs, msgView{role: "错误", at: nowHM(), text: out.err.Error()})
			return
		}
		a.st.msgs = append(a.st.msgs, msgView{
			role: "assistant", at: nowHM(), via: out.via, text: humanize(out.reply, out.via),
		})
		a.st.scroll = 0

	case "convoff":
		a.st.convOK = false
		a.st.notice = "未安装对话插件（conversation.v1）：这里是线性聊天，消息不能修改或重新生成"
		a.st.noticeOK = false

	case "conv":
		a.st.convBusy = false
		a.st.busy = false
		if out.err != nil {
			a.st.notice = "对话操作失败：" + out.err.Error()
			a.st.noticeOK = false
			return
		}
		a.applyConv(*out.conv)
	}
}

// applyConv 把 conversation.v1 返回的一条时间线刷到界面上。
func (a *app) applyConv(s convSnapshot) {
	a.st.convOK = true
	a.st.sessID = s.sessionID
	a.st.sessions = s.sessions
	a.st.msgs = s.msgs
	if s.note != "" {
		// 插件比界面更清楚刚才发生了什么（例如「末尾那条生成失败了」）
		a.st.notice = s.note
		a.st.noticeOK = true
	}
}

// ---------------------------------------------------------------------------
// 输入分发
// ---------------------------------------------------------------------------

func (a *app) handle(ev event) (quit bool) {
	switch ev.kind {
	case evResize:
		return false
	case evMouse:
		if ev.mouse.Wheel != 0 {
			a.scrollBy(ev.mouse.Wheel)
			return false
		}
		switch {
		case ev.mouse.Press:
			// 浮窗盖在最上面：按在它身上就是"抓住它"，不是点它盖住的那个按钮
			if a.grabWidget(ev.mouse.X, ev.mouse.Y) {
				return false
			}
			a.pressStart(ev.mouse.X, ev.mouse.Y)
		case ev.mouse.Drag:
			if a.st.drag != nil {
				a.dragSelect(ev.mouse.X, ev.mouse.Y)
				return false
			}
			a.dragWidget(ev.mouse.X, ev.mouse.Y)
		case ev.mouse.Rel:
			a.release(ev.mouse.X, ev.mouse.Y)
		}
		return false
	}

	// 插件自带的页面自己收键盘：它说了要（WantKeys）就给它。Tab 永远留给宿主
	// 切页 —— 那是用户仅有的退路，交给插件的话，一个写坏的页面就能把人困住。
	// Ctrl+C 走 pageKey 自己判断：页面声明了"要复制"（WantCopy）就转给它复制，
	// 否则照旧留作宿主的退出（见 pageKey）。
	if ev.key != keyTab && a.pageKey(ev) {
		return false
	}

	switch ev.key {
	case keyCtrlC:
		// Ctrl+C 专职复制：有选中就复制；没选中只提示，绝不退出。
		// 退出是 Ctrl+Q —— 两个动作挤在一个键上，拖选想复制却因为没选中
		// 而把程序关了，是这里最吓人的误触。
		if a.hasSel() {
			a.copySelection()
		} else {
			a.tell("没有选中文字：拖动选中后再 Ctrl+C 复制（退出用 Ctrl+Q）", false)
		}
		return false
	case keyCtrlQ:
		// 退出：明确的退出键，不和复制抢 Ctrl+C
		return true
	case keyCtrlL:
		// Ctrl+L 原本是「清屏」。现在对话存在会话树里，清屏等于把用户自己写的东西
		// 从眼前抹掉 —— 改成重新拉一次时间线：本地的系统提示会被刷掉，树里一条不动。
		a.st.notice = ""
		a.st.events = nil
		a.loadConversation()
	case keyTab:
		a.onTab()
	case keyRune:
		a.insertRune(ev.r, ev.paste)
	case keyBackspace:
		a.editBackspace()
	case keyDelete:
		a.editDelete()
	case keyLeft:
		a.moveCursor(-1)
	case keyRight:
		a.moveCursor(1)
	case keyHome:
		a.moveInputEdge(false)
	case keyEnd:
		a.moveInputEdge(true)
	case keyAltLeft:
		a.nudgeWidget(-2, 0)
	case keyAltRight:
		a.nudgeWidget(2, 0)
	case keyAltUp:
		a.nudgeWidget(0, -1)
	case keyAltDown:
		a.nudgeWidget(0, 1)
	case keyUp:
		if a.st.attachOpen {
			a.moveAttachSel(-1)
			return false
		}
		// 输入框有多行时 ↑↓ 归光标：否则写了三行就再也回不到上一行去改
		if a.moveInputLine(-1) {
			return false
		}
		a.moveSelection(-1)
	case keyDown:
		if a.st.attachOpen {
			a.moveAttachSel(1)
			return false
		}
		if a.moveInputLine(1) {
			return false
		}
		a.moveSelection(1)
	case keyEnter:
		if a.st.attachOpen {
			a.addSelectedAttachment()
			return false
		}
		// 聊天页：回车是**换行**，不是发送。
		// 发送只走「发送」按钮，或 Ctrl+Enter —— 写长消息时敲个回车就发出去，
		// 是这里最烦人的一件事（顺手补一句就得重打一遍）。
		if a.st.tab == tabChat && !a.st.shareOpen {
			a.insertNewline()
			return false
		}
		a.enter()
	case keyCtrlEnter:
		// 键盘用户的送出口：手不离开键盘也能发
		if a.st.tab == tabChat && !a.st.shareOpen && !a.st.attachOpen {
			a.enter()
		}
	case keyEsc:
		// 有选中时 Esc 先撤选择 —— "反悔键"最浅的那一层
		if a.clearSelIfAny() {
			return false
		}
		// Esc 是「反悔键」，按最外层的临时状态往里收
		if a.st.attachOpen {
			a.closeAttach()
			a.st.notice = "已关闭附件面板（已选的附件还在）"
			a.st.noticeOK = true
			return false
		}
		if a.st.shareOpen {
			a.st.shareOpen = false
			a.st.notice = "已取消分享"
			a.st.noticeOK = true
			return false
		}
		if a.st.confirmSessDel {
			a.st.confirmSessDel = false
			a.st.notice = "已取消删除会话"
			a.st.noticeOK = true
			return false
		}
		if a.st.editing != "" {
			a.cancelEdit()
			return false
		}
		a.st.input = nil
		a.st.cursor = 0
		a.st.focus = ""
	}
	return false
}

// restart 请核心重启：重新加载所有插件。
//
// 插件的启用 / 禁用 / 删除都只是做了标记，规范 2.6 要求重启后才生效 ——
// 界面上那一堆"重启后生效"的提示，就靠这一下落地。
//
// 请求丢到后台发，别卡住界面主循环；结果回来写状态栏。
// 注意重启会连界面自己也杀掉（界面也是核心拉起来的插件），
// 所以状态通常来不及显示：核心起来后会再把界面拉起来。
func (a *app) restart() {
	// 先把话写在状态栏上，再去发请求。
	//
	// 核心一收到请求就开始停插件，界面随时会被收掉（core.shutdown → 立刻退出），
	// 等请求回来再提示，十有八九来不及画 —— 用户只看到屏幕闪了一下。
	a.post(func() {
		a.st.status = "正在重启核心…"
		a.st.statusOK = true
	})
	go func() {
		note, err := a.plug.restart()
		a.post(func() {
			if err != nil {
				a.st.status = "重启失败：" + err.Error()
				a.st.statusOK = false
				return
			}
			a.st.status = note
			a.st.statusOK = true
		})
	}()
}

// switchTab 在"内置页 + 插件页"里前后切一页。
//
// 走的是 allTabsOf 而不是固定的 allTabs：装了带界面的插件，Tab 就得能走到它。
func (a *app) switchTab(delta int) {
	list := allTabsOf(a.st)
	if len(list) == 0 {
		return
	}
	idx := 0
	for i, t := range list {
		if t.matches(a.st) {
			idx = i
		}
	}
	a.gotoTab(list[(idx+delta+len(list))%len(list)])
}

// gotoTab 切到某个标签上。
func (a *app) gotoTab(t tabTarget) {
	if t.kind == tabPanel {
		a.openPage(t.svc, t.panel)
		return
	}
	a.setTab(t.kind)
}

// goTab 处理一次标签点击。arg 要么是内置页的 key，要么是 "page:<服务>/<页面id>"。
func (a *app) goTab(arg string) {
	if ref, ok := strings.CutPrefix(arg, "page:"); ok {
		if svc, id, ok := strings.Cut(ref, "/"); ok {
			a.openPage(svc, id)
		}
		return
	}
	for _, t := range allTabs {
		if t.key() == arg {
			a.setTab(t)
		}
	}
}

// setTab 切页并重置焦点。
//
// 设置页会把底部提示改成配置状态：否则上一个页面的操作提示会一直挂在下面，
// 看起来像刚刚在这里发生过。
func (a *app) setTab(t tabKind) {
	// 切页就撤掉一切选择：选中是"正在改这个输入框"的临时状态，
	// 跟焦点一样不该跨页留着。
	a.clearSelAll()
	a.st.drag = nil
	a.st.tab = t
	if t == tabChat {
		a.st.focus = "chat"
		return
	}
	a.st.focus = ""
}

// onTab 处理 Tab 键。
//
// 内置页不再有表单了（设置页搬走了），所以 Tab 就是切页。
func (a *app) onTab() {
	a.switchTab(1)
}

// click 处理一次鼠标点击：按坐标反查渲染时登记的动作。
func (a *app) click(x, y int) {
	if a.canv == nil {
		return
	}
	action := a.canv.hit(x, y)
	if action == "" {
		return
	}
	kind, arg := action, ""
	if i := strings.IndexByte(action, ':'); i >= 0 {
		kind, arg = action[:i], action[i+1:]
	}

	switch kind {
	case "tab":
		a.goTab(arg)

	case "panel":
		// 插件页面里的点击：arg 是这一帧热区的下标（见 pageRows）
		if i := atoiSafe(arg); i >= 0 && i < len(a.st.pageHits) {
			a.pageHit(a.st.pageHits[i])
		}

	case "svc":
		a.focusService(arg)

	case "atchdir":
		a.browseTo2(arg, true)

	case "atchfile":
		a.browseTo2(arg, false)

	case "atchdel":
		if idx := atoiSafe(arg); idx >= 0 {
			a.removeAttachment(idx)
		}

	case "openimg":
		a.openImage(arg)

	case "core":
		// 标签栏上的「重启」：请核心重新加载插件（插件的启停 / 删除都是重启后生效）
		if arg == "restart" {
			a.restart()
		}

	case "caret":
		// 点在输入框的文字上：把光标放到点击的那一列
		a.setCaret(arg, y)

	case "msg":
		id, verb := splitLast(arg)
		a.msgAction(id, verb)

	case "page":
		id, verb := splitLast(arg)
		a.pageVersion(id, verb)

	case "sess":
		a.switchSession(arg)

	case "btn":
		a.press(arg)
	}
}

// ---------------------------------------------------------------------------
// 输入框的鼠标操作：点击定位光标、拖动选中
// ---------------------------------------------------------------------------

// setCaret 处理一次"点在输入框文字上"：把光标放到点击的那一列，并把焦点带过去。
// arg 是 "target:col"（target 是 chat 或字段 key，col 是内容区内的列号）。
func (a *app) setCaret(arg string, y int) {
	target, colS := splitLast(arg)
	col := atoiSafe(colS)
	if col < 0 {
		col = 0
	}

	if target == "chat" {
		if a.st.tab != tabChat || a.st.shareOpen || a.st.attachOpen {
			return
		}
		a.st.focus = "chat"
		box := buildInputBox(a.st, a.st.w, a.st.h)
		// 行号由 y 补上：输入区贴屏幕底部，第几行 = y 到输入区顶部的距离
		k := y - (a.st.h - box.rows)
		k = clampInt(k, 0, box.rows-1)
		idx := box.view.from + k
		col = clampInt(col, 0, box.textW)
		if idx < 0 || idx >= len(box.view.lines) {
			// 点在内容之外的空白行：光标落到底
			a.st.cursor = len(a.st.input)
			return
		}
		a.st.cursor = box.view.indexAt(idx, col)
	}
}

// pressStart 处理一次鼠标按下：按普通点击处理，若按在输入框文字上则同时记下拖动起点。
func (a *app) pressStart(x, y int) {
	a.st.drag = nil
	action := a.canv.hit(x, y)
	a.click(x, y)

	kind, arg := action, ""
	if i := strings.IndexByte(action, ':'); i >= 0 {
		kind, arg = action[:i], action[i+1:]
	}

	// 插件页面：点在某个输入字段上，也要能拖选。
	// 点击已经经 click -> pageHit 把 caret 转给了插件（插件据此落锚点），
	// 这里只记下"正在哪个字段上拖"，拖动时再把落点热区的列号转过去。
	if kind == "panel" {
		if i := atoiSafe(arg); i >= 0 && i < len(a.st.pageHits) {
			ref := a.st.pageHits[i]
			if ref.hit.Action == "caret" {
				field, _, _ := strings.Cut(ref.hit.Arg, ":")
				a.st.drag = &dragSel{target: "panel", svc: ref.svc, id: ref.id, field: field}
			}
		}
		return
	}
	if kind != "caret" {
		return
	}
	target, colS := splitLast(arg)
	col := atoiSafe(colS)
	fx := x - col
	if target == "chat" {
		fx = 1 + buildInputBox(a.st, a.st.w, a.st.h).promptW
	}
	anchor := -1
	if target == "chat" {
		anchor = a.st.cursor
	}
	if anchor < 0 {
		return
	}
	a.st.drag = &dragSel{target: target, anchor: anchor, fx: fx}
	a.applyDragSel(target, anchor, anchor)
}

// dragSelect 处理一次按住左键的移动：从起点往当前位置拖出一段选择。
func (a *app) dragSelect(x, y int) {
	d := a.st.drag
	if d == nil {
		return
	}
	if d.target == "panel" {
		a.dragPanelSel(d, x, y)
		return
	}
	idx := a.caretIndex(d.target, d.fx, x, y)
	if idx < 0 {
		return
	}
	if d.target == "chat" {
		a.st.cursor = idx
	}
	a.applyDragSel(d.target, d.anchor, idx)
}

// dragPanelSel 把一次插件页面内的拖动转成 sel 动作发给插件。
//
// 宿主不算列号：插件为字段的每一列都登记了 caret 热区，拖到哪个热区，
// 就把哪个热区自带的 "字段:列" 转过去。拖出字段、拖到别的字段，都不算。
func (a *app) dragPanelSel(d *dragSel, x, y int) {
	action := a.canv.hit(x, y)
	kind, arg := action, ""
	if i := strings.IndexByte(action, ':'); i >= 0 {
		kind, arg = action[:i], action[i+1:]
	}
	if kind != "panel" {
		return
	}
	i := atoiSafe(arg)
	if i < 0 || i >= len(a.st.pageHits) {
		return
	}
	ref := a.st.pageHits[i]
	if ref.hit.Action != "caret" {
		return
	}
	field, _, _ := strings.Cut(ref.hit.Arg, ":")
	if field != d.field {
		return // 拖到别的字段上了
	}
	go func() {
		note, err := a.plug.pageAction(ref.svc, ref.id, "sel", ref.hit.Arg)
		a.results <- outcome{kind: "pageact", note: note, err: err}
	}()
}

// release 处理一次松手：结束拖动（或浮窗拖动），给选中的结果一句反馈。
func (a *app) release(x, y int) {
	if a.st.drag == nil {
		a.dropWidget()
		return
	}
	d := a.st.drag
	a.st.drag = nil

	if d.target == "panel" {
		// 选择落在插件侧，字符数也在插件侧：收尾不用发东西，插件重绘时
		// 会自己把选中段画出来、并把"Ctrl+C 复制"写进它的说明行。
		return
	}

	n := 0
	if d.target == "chat" && a.st.selOn {
		n = a.st.selB - a.st.selA
	}
	if n > 0 {
		a.tell(fmt.Sprintf("已选中 %d 个字符：Ctrl+C 复制，Esc 取消", n), true)
	}
}

// caretIndex 把鼠标坐标换算成输入框内的 rune 下标；不在输入区返回 -1。
func (a *app) caretIndex(target string, fx, x, y int) int {
	col := x - fx
	if target == "chat" {
		if a.st.tab != tabChat || a.st.shareOpen || a.st.attachOpen {
			return -1
		}
		box := buildInputBox(a.st, a.st.w, a.st.h)
		k := y - (a.st.h - box.rows)
		k = clampInt(k, 0, box.rows-1)
		idx := box.view.from + k
		if idx < 0 || idx >= len(box.view.lines) {
			return len(a.st.input)
		}
		return box.view.indexAt(idx, clampInt(col, 0, box.textW))
	}
	return -1
}

// applyDragSel 把选区更新为 [lo, hi)。
func (a *app) applyDragSel(target string, a1, a2 int) {
	lo, hi := a1, a2
	if lo > hi {
		lo, hi = hi, lo
	}
	if target == "chat" {
		a.st.selOn = lo != hi
		a.st.selA, a.st.selB = lo, hi
	}
}

// hasSel 问"现在聊天输入框里有没有一段选中文字"。
func (a *app) hasSel() bool {
	return a.st.selOn
}

// selText 返回聊天输入框里当前选中段的文字。
func (a *app) selText() string {
	if a.st.selOn && a.st.selA < a.st.selB && a.st.selB <= len(a.st.input) {
		return string(a.st.input[a.st.selA:a.st.selB])
	}
	return ""
}

// clearSelAll 清掉输入框上的选择（切页、开始打字、复制完都走这里）。
func (a *app) clearSelAll() {
	a.st.selOn = false
}

// clearSelIfAny 取消当前选择；没有选择返回 false（调用方照常处理 Esc）。
func (a *app) clearSelIfAny() bool {
	if !a.hasSel() {
		return false
	}
	a.clearSelAll()
	a.tell("已取消选择", true)
	return true
}

// copySelection 把选中的文字写进系统剪贴板（OSC 52，见 util.go）。
func (a *app) copySelection() {
	text := a.selText()
	a.clearSelAll()
	if text == "" {
		return
	}
	if a.copyToClipboard(text) {
		a.tell(fmt.Sprintf("已复制 %d 个字符", len([]rune(text))), true)
		return
	}
	a.tell("复制失败：内容过长或终端不支持剪贴板", false)
}

// tell 在页面底部给一句反馈：聊天页用 notice（输入行上方那行），其它页用状态栏。
func (a *app) tell(msg string, ok bool) {
	if a.st.tab == tabChat {
		a.st.notice, a.st.noticeOK = msg, ok
		return
	}
	a.st.status, a.st.statusOK = msg, ok
}

// splitLast 从右边切一刀。
//
// 动作写成 "msg:n3:edit" 这种两段带参的形式：click 已经按第一个冒号切过一次，
// 剩下这段里 id 不含冒号（节点 ID 只有 n+数字），所以切最后一个冒号正好。
func splitLast(s string) (head, tail string) {
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// focusService 点侧栏里的某个服务：说清楚它此刻是谁在提供。
//
// 以前它会跳到「插件」页并选中提供者。那一页现在由插件管理器自带，
// 而"跳进去并选中某个插件"需要面板协议为某一页开特例（多带一个选中参数）——
// 不值当：这里退回它本来就能说清的那件事，要管理插件就去那个标签页。
func (a *app) focusService(name string) {
	for _, p := range a.st.plugins {
		if contains(p.Provides, name) {
			if p.running() {
				a.st.status = "服务 " + name + " 由 " + p.ID + " 提供（在插件页里能看到它）"
				a.st.statusOK = true
			} else {
				a.st.status = "服务 " + name + " 当前不可用（声明于 " + p.ID + "）"
				a.st.statusOK = false
			}
			return
		}
	}
	for _, s := range a.st.services {
		if s.name == name {
			a.st.status = name + "：" + firstNonEmpty(s.provider, s.note)
			a.st.statusOK = s.alive
			return
		}
	}
}

// ---------------------------------------------------------------------------
// 聊天页的点击动作
// ---------------------------------------------------------------------------

// msgAction 处理消息框上的三个按钮。
//
// 三个都不需要二次确认：修改和重新生成只是「在同一个父节点下再加一个兄弟版本」，
// 分享只是复制一段文本 —— 没有一个是破坏性的，旧版本一个节点都不会消失。
func (a *app) msgAction(id, verb string) {
	m, ok := a.msgByID(id)
	if !ok {
		return
	}
	switch verb {
	case "edit":
		a.startEdit(m)
	case "regen":
		a.regenerate(m)
	case "share":
		_, label := roleStyle(m.role)
		if a.copyToClipboard(m.text) {
			a.st.notice = fmt.Sprintf("已复制这条%s消息到剪贴板（%d 字）",
				label, len([]rune(m.text)))
			a.st.noticeOK = true
			return
		}
		a.st.notice = "内容太长，剪贴板塞不下；改用右侧「分享整条」导出成文件"
		a.st.noticeOK = false
	}
}

func (a *app) msgByID(id string) (msgView, bool) {
	for _, m := range a.st.msgs {
		if m.id == id {
			return m, true
		}
	}
	return msgView{}, false
}

// ---------------------------------------------------------------------------
// 附件
// ---------------------------------------------------------------------------

// openAttach 打开附件面板，并列出当前目录。
func (a *app) openAttach() {
	a.st.attachOpen = true
	a.st.shareOpen = false
	a.st.notice = "挑文件：点名字加入，点目录进去；回车加当前选中项，Esc 关闭"
	a.st.noticeOK = true
	if len(a.st.attachEntries) == 0 {
		a.browseAttach(a.st.attachPath)
	}
}

func (a *app) closeAttach() {
	a.st.attachOpen = false
	a.st.notice = ""
}

// browseAttach 列出某个目录供挑选（复用工作区的浏览能力）。
func (a *app) browseAttach(path string) {
	if a.st.attachBusy {
		return
	}
	a.st.attachBusy = true
	go func() {
		ls, err := a.plug.wsBrowse(path)
		a.results <- outcome{kind: "attach", ws: &ls, err: err}
	}()
}

// addAttachment 把一个路径读成附件并加进列表。
//
// 图片按 base64 读（二进制安全），文本按 UTF-8 读，其余只记元数据 ——
// 绝不把二进制当文本塞给模型，那只是一堆乱码。
func (a *app) addAttachment(path string) {
	if a.st.attachBusy {
		return
	}
	a.st.attachBusy = true
	go func() {
		att, err := a.plug.readAttachment(path)
		a.results <- outcome{kind: "attachone", att: &att, err: err}
	}()
}

// browseTo2 处理附件面板里的一次点击：目录就下去，文件就加进附件。
func (a *app) browseTo2(name string, isDir bool) {
	for i, e := range a.st.attachEntries {
		if e.Name != name {
			continue
		}
		a.st.attachSel = i
		full := strings.TrimRight(a.st.attachPath, `/\`) + "/" + name
		if isDir || e.Dir {
			a.browseAttach(full)
			return
		}
		a.addAttachment(full)
		return
	}
}

// attachUp 回到上一级目录。
func (a *app) attachUp() {
	if a.st.attachParent == "" {
		a.st.notice = "已经到最上一级了"
		a.st.noticeOK = true
		return
	}
	a.browseAttach(a.st.attachParent)
}

// moveAttachSel 在附件列表里上下移动。
func (a *app) moveAttachSel(delta int) {
	if len(a.st.attachEntries) == 0 {
		return
	}
	a.st.attachSel = clampInt(a.st.attachSel+delta, 0, len(a.st.attachEntries)-1)
}

// addSelectedAttachment 把当前高亮的那一项加进来（目录则进去）。
func (a *app) addSelectedAttachment() {
	if a.st.attachSel < 0 || a.st.attachSel >= len(a.st.attachEntries) {
		return
	}
	e := a.st.attachEntries[a.st.attachSel]
	a.browseTo2(e.Name, e.Dir)
}

// ---------------------------------------------------------------------------
// 拖拽：终端把拖进来的文件变成一段路径文字，这里把它认出来
//
// 终端的"拖拽"实际上是插入文件路径（含空格时带引号）。没有真正的拖放事件可监听，
// 所以只能在回车那一刻看：这一段输入是不是就是一个/几个存在的文件路径？
// 是就转成附件，不是就照常当文本发出去 —— 猜错也只是没生效，不会吃掉用户的话。
// ---------------------------------------------------------------------------

// addDroppedAttachment 把一个拖进来的路径读成附件。
//
// 与面板里的添加分开走一个 outcome：它成功时要顺手清空输入框，
// 而面板里的添加绝不能动用户正在打的字。
func (a *app) addDroppedAttachment(path string) {
	if a.st.attachBusy {
		return
	}
	a.st.attachBusy = true
	go func() {
		att, err := a.plug.readAttachment(path)
		a.results <- outcome{kind: "attachdrop", att: &att, err: err}
	}()
}

// maybeDroppedPath 在回车时判断：输入的是不是拖进来的路径。
// 是则转成附件并返回 true（这次回车不发送）。
func (a *app) maybeDroppedPath() bool {
	if len(a.st.attachments) > 0 {
		return false // 已经在挑附件了，就别再猜
	}
	paths := droppedPaths(string(a.st.input))
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		a.addDroppedAttachment(p)
	}
	return true
}

// droppedPaths 把一段输入解析成路径列表；只要有一段不像路径就整体放弃，
// 避免把「C:\ 是网盘吗」这种正常提问误吞成附件。
func droppedPaths(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "\r\n") {
		return nil
	}
	var out []string
	for _, part := range splitQuoted(s) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !looksLikePath(part) {
			return nil
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// lookeLikePath 认三种写法：盘符（C:\）、UNC（\\srv\share）、POSIX（/x）。
func looksLikePath(s string) bool {
	if strings.HasPrefix(s, `\\`) || strings.HasPrefix(s, "//") {
		return true
	}
	if strings.HasPrefix(s, "/") {
		return true
	}
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	return false
}

// splitQuoted 按双引号切分，引号内的空格不当分隔。
//
//	`"C:\my docs\a.png" D:\b.txt` → ["C:\my docs\a.png", "D:\b.txt"]
func splitQuoted(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			if inQuote {
				out = append(out, cur.String())
				cur.Reset()
				inQuote = false
				continue
			}
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			inQuote = true
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// ---------------------------------------------------------------------------
// 图片：读成缩略图、点了打开
// ---------------------------------------------------------------------------

// pumpThumbs 把这一帧渲染时"想要但还没有"的图片都派出去读。
//
// 渲染是同步的、读图要走核心（RPC），所以只能反着来：
// 渲染时只登记需求，渲染完由这里统一发起异步读取，读完再重绘一帧。
func (a *app) pumpThumbs() {
	if len(a.st.thumbWanted) == 0 {
		return
	}
	wanted := a.st.thumbWanted
	a.st.thumbWanted = nil
	color := true
	if a.term != nil {
		color = a.term.color
	}
	for _, p := range wanted {
		go func(path string) {
			data, err := a.plug.readImageBytes(path)
			if err != nil {
				a.results <- outcome{kind: "thumb", thumbPath: path, thumbFail: true}
				return
			}
			img, err := decodeImage(data)
			if err != nil {
				a.results <- outcome{kind: "thumb", thumbPath: path, thumbFail: true}
				return
			}
			a.results <- outcome{kind: "thumb", thumbPath: path,
				thumbLines: thumbnailLines(img, thumbCols, thumbRows, color)}
		}(p)
	}
}

// openImage 让系统默认程序打开这张图。
func (a *app) openImage(path string) {
	go func() {
		note, err := a.plug.openPath(path)
		a.results <- outcome{kind: "openimg", note: note, err: err}
	}()
}

// atoiSafe 只认纯数字，其余一律当非法（返回 -1）。
// 比 strconv 少一层 error 判断，而这里的输入只可能来自我们自己渲染的按钮。
func atoiSafe(s string) int {
	if s == "" {
		return -1
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// removeAttachment 移除一个已选附件。
func (a *app) removeAttachment(idx int) {
	if idx < 0 || idx >= len(a.st.attachments) {
		return
	}
	a.st.attachments = append(a.st.attachments[:idx], a.st.attachments[idx+1:]...)
	a.st.notice = fmt.Sprintf("已移除 1 个附件，还剩 %d 个", len(a.st.attachments))
	a.st.noticeOK = true
}

func (a *app) clearAttachments() {
	a.st.attachments = nil
	a.st.notice = "已清空附件"
	a.st.noticeOK = true
}

// startEdit 进入「修改」态：把这条消息的原文放进输入行，回车就提交成新版本。
func (a *app) startEdit(m msgView) {
	if !a.st.convOK {
		a.st.notice = "未安装对话插件（conversation.v1），改不了消息"
		a.st.noticeOK = false
		return
	}
	if a.st.convBusy {
		a.st.notice = "上一步还没结束，稍等一下再改"
		a.st.noticeOK = false
		return
	}
	if a.st.editing == "" {
		// 只备份一次：连点两条消息的「修改」时，别把备份覆盖成上一条的原文
		a.st.editPrev = string(a.st.input)
	}
	a.st.editing = m.id
	a.st.input = []rune(m.text)
	a.st.cursor = len(a.st.input)
	a.st.tab = tabChat
	a.st.focus = "chat"
	a.st.notice = "正在修改这条消息：回车提交成一个新版本，旧版本不会删"
	a.st.noticeOK = true
}

// cancelEdit 放弃修改，把输入行还给用户刚才没写完的内容。
func (a *app) cancelEdit() {
	a.st.editing = ""
	a.st.input = []rune(a.st.editPrev)
	a.st.cursor = len(a.st.input)
	a.st.editPrev = ""
	a.st.notice = "已取消修改"
	a.st.noticeOK = true
}

// regenerate 重新生成一条消息的回复。
//
// 点 AI 的消息：在它的父节点下再挂一个 assistant 兄弟（换个答案）。
// 点用户的消息：在它自己下面再挂一个 assistant 孩子（重新问一遍）。
// 两种情况都是「重开一条时间线」，原来那条原地不动。
func (a *app) regenerate(m msgView) {
	if !a.st.convOK {
		a.st.notice = "未安装对话插件（conversation.v1），不能重新生成"
		a.st.noticeOK = false
		return
	}
	sess, id := a.st.sessID, m.id
	a.st.notice = "正在重新生成…"
	a.st.noticeOK = true
	a.convRun(true, func() (convSnapshot, error) { return a.plug.convRegenerate(sess, id) })
}

// pageVersion 在同一条消息的多个版本之间翻页。
//
// 翻的是「父节点当前选中哪个孩子」，所以路径前缀完全不变 ——
// 屏幕上就是这条消息连同它后面的一整段一起换了个样子。
func (a *app) pageVersion(id, verb string) {
	if !a.st.convOK {
		return
	}
	delta := 1
	if verb == "prev" {
		delta = -1
	}
	sess := a.st.sessID
	a.convRun(false, func() (convSnapshot, error) { return a.plug.convPage(sess, id, delta) })
}

// switchSession 点侧栏里的会话名：切过去。
func (a *app) switchSession(id string) {
	if id == "" || id == a.st.sessID {
		return
	}
	a.st.confirmSessDel = false
	a.st.editing = ""
	a.st.scroll = 0
	sess := id
	a.convRun(false, func() (convSnapshot, error) { return a.plug.convSwitch(sess) })
}

// newSession 新开一条会话。旧的原地保留，随时点回来。
func (a *app) newSession() {
	a.st.confirmSessDel = false
	a.st.editing = ""
	a.st.scroll = 0
	a.convRun(false, func() (convSnapshot, error) { return a.plug.convNew() })
}

// deleteSession 删掉当前会话。
//
// 这是界面上唯一真正会丢东西的操作（删的是整条会话），所以要点两次。
func (a *app) deleteSession() {
	id := a.st.sessID
	if id == "" {
		return
	}
	a.st.confirmSessDel = false
	a.st.editing = ""
	a.st.scroll = 0
	a.convRun(false, func() (convSnapshot, error) { return a.plug.convDelete(id) })
}

// ---------------------------------------------------------------------------
// 分享
// ---------------------------------------------------------------------------

// openShare 打开分享面板（占住输入行）。
func (a *app) openShare() {
	if !a.st.convOK {
		a.st.notice = "未安装对话插件（conversation.v1），分享不了整条时间线"
		a.st.noticeOK = false
		return
	}
	a.st.shareOpen = true
	a.st.notice = "分享的是当前这条时间线：翻页隐没的旧版本不进分享内容"
	a.st.noticeOK = true
}

// copyAll 把整条时间线复制到系统剪贴板。
func (a *app) copyAll() {
	a.st.shareOpen = false
	sess := a.st.sessID
	a.st.notice = "正在整理分享内容…"
	a.st.noticeOK = true
	go func() {
		md, err := a.plug.convMarkdown(sess)
		a.post(func() { a.finishCopy(md, err) })
	}()
}

func (a *app) finishCopy(md string, err error) {
	if err != nil {
		a.st.notice = "生成分享内容失败：" + err.Error()
		a.st.noticeOK = false
		return
	}
	if !a.copyToClipboard(md) {
		a.st.notice = fmt.Sprintf("内容太长剪贴板塞不下（%d 字），改用「导出 .md 到工作区」",
			len([]rune(md)))
		a.st.noticeOK = false
		return
	}
	a.st.notice = fmt.Sprintf("整条时间线已复制到剪贴板（%d 字），粘到哪儿都是 Markdown",
		len([]rune(md)))
	a.st.noticeOK = true
}

// exportAll 把整条时间线写成 .md 落到工作区里。
func (a *app) exportAll() {
	a.st.shareOpen = false
	sess := a.st.sessID
	a.st.notice = "正在导出…"
	a.st.noticeOK = true
	go func() {
		note, err := a.plug.convExport(sess)
		a.post(func() {
			if err != nil {
				a.st.notice = "导出失败：" + err.Error()
				a.st.noticeOK = false
				return
			}
			a.st.notice = note
			a.st.noticeOK = true
		})
	}()
}

// copyToClipboard 把文本塞进系统剪贴板，成功返回 true。
//
// 走 OSC 52：由终端自己转交给系统剪贴板，既不需要剪贴板工具，也不需要任何权限。
// 终端不支持时用户只是没看到效果，不会报错，所以另外给了一条"导出成文件"的路。
func (a *app) copyToClipboard(text string) bool {
	seq := osc52(text)
	if seq == "" {
		return false
	}
	a.term.WriteRaw(seq)
	return true
}

func (a *app) press(btn string) {
	switch btn {
	case "attach":
		a.openAttach()

	case "attachclose":
		a.closeAttach()
		a.st.notice = "附件面板已关闭；回车发送时会带上已选附件"
		a.st.noticeOK = true

	case "attachup":
		a.attachUp()

	case "attachclear":
		a.clearAttachments()

	case "send":
		// 唯一的发送入口：点「发送」按钮走这里（回车现在只换行）
		a.enter()

	// 会话
	case "sessnew":
		a.newSession()

	case "sessdel":
		a.st.confirmSessDel = true
		a.st.notice = "删除会话「" + a.sessionName() + "」不可撤销（里面的消息一起删），确认请点「确认删除」"
		a.st.noticeOK = false

	case "sessdelok":
		a.deleteSession()

	case "sessdelcancel":
		a.st.confirmSessDel = false
		a.st.notice = "已取消删除会话"
		a.st.noticeOK = true

	// 分享
	case "seshare":
		a.openShare()

	case "sharecopy":
		a.copyAll()

	case "sharefile":
		a.exportAll()

	case "shareclose":
		a.st.shareOpen = false
		a.st.notice = "已取消分享"
		a.st.noticeOK = true
	}
}

// sessionName 返回当前会话的名字，取不到就退回 ID（确认删除时要说清删的是哪条）。
func (a *app) sessionName() string {
	if s, ok := sessByID(a.st.sessions, a.st.sessID); ok {
		return s.name
	}
	return a.st.sessID
}

// ---------------------------------------------------------------------------
// 编辑
// ---------------------------------------------------------------------------

// insertRune 插入一个字符。
//
// 控制字符一律丢弃 —— 它们多半是复制粘贴带进来的噪音，而制表符在界面里是"切页"键，
// 放进去就等于一次粘贴把用户弹回聊天页。**换行是例外**：输入框现在是多行的，
// 粘一段多行文本正是常见用法，那些换行就该原样成为换行。
// 真正按下的 Tab / Enter 走的是 keyTab / keyEnter，不受这里影响。
func (a *app) insertRune(r rune, pasted bool) {
	if r != '\n' && (r < 0x20 || r == 0x7f || (r >= 0xD800 && r <= 0xDFFF)) {
		return
	}
	if a.st.tab != tabChat || a.st.shareOpen || a.st.attachOpen {
		return // 面板占着聊天区时，打字不该落到任何地方
	}
	a.insertAtCursor(r)
}

// insertAtCursor 在聊天输入框的光标处插入一个字符。
func (a *app) insertAtCursor(r rune) {
	if a.st.selOn {
		// 选中时打字 = 替换选中段
		a.st.input = append(append([]rune{}, a.st.input[:a.st.selA]...),
			append([]rune{r}, a.st.input[a.st.selB:]...)...)
		a.st.cursor = a.st.selA + 1
		a.st.selOn = false
		return
	}
	a.st.input = append(a.st.input, 0)
	copy(a.st.input[a.st.cursor+1:], a.st.input[a.st.cursor:])
	a.st.input[a.st.cursor] = r
	a.st.cursor++
}

// insertNewline 在光标处插入一个换行 —— 回车现在干的就是这件事。
//
// 设置页的字段是单行的，换行塞进去只会变成一个看不见的字符，所以照样挡住。
func (a *app) insertNewline() {
	if a.st.tab != tabChat || a.st.shareOpen || a.st.attachOpen {
		return
	}
	a.insertAtCursor('\n')
}

// moveInputLine 让光标在输入框的显示行之间上下走；返回 false 表示"这次不归我管"。
//
// 只有输入框真的不止一行时才接管 ↑↓：否则原来那套"↑↓ 选消息框 / 滚历史"
// 会因为消息里多打了一个回车就整个失效，那才是更让人意外的事。
func (a *app) moveInputLine(delta int) bool {
	if !a.inputEditable() {
		return false
	}
	iv := buildInputBox(a.st, a.st.w, a.st.h).view
	if iv.rows < 2 {
		return false
	}
	// 已经在第一行还按 ↑：吃掉这次按键，别让光标跳出输入框
	a.st.cursor = iv.indexAt(clampInt(iv.cur+delta, 0, iv.rows-1), iv.col)
	return true
}

// moveInputEdge 把光标移到当前显示行的行首 / 行尾。
//
// 单行时它与"整个输入的开头 / 结尾"完全等价，多行时才是行内的首尾。
// 表单字段是单行的，Home / End 就直接到字段的开头 / 结尾。
func (a *app) moveInputEdge(toEnd bool) {
	if !a.inputEditable() {
		a.st.cursor = 0
		if toEnd {
			a.st.cursor = len(a.st.input)
		}
		return
	}
	iv := buildInputBox(a.st, a.st.w, a.st.h).view
	col := 0
	if toEnd {
		col = 1 << 30 // 超过行长就落到行尾
	}
	a.st.cursor = iv.indexAt(iv.cur, col)
	a.st.selOn = false
}

// inputEditable 表示"现在打字落在聊天输入框上"。
func (a *app) inputEditable() bool {
	return a.st.tab == tabChat && !a.st.shareOpen && !a.st.attachOpen
}

func (a *app) editBackspace() {
	if a.st.selOn {
		a.st.input = append(append([]rune{}, a.st.input[:a.st.selA]...), a.st.input[a.st.selB:]...)
		a.st.cursor = a.st.selA
		a.st.selOn = false
		return
	}
	if a.st.cursor > 0 {
		a.st.input = append(a.st.input[:a.st.cursor-1], a.st.input[a.st.cursor:]...)
		a.st.cursor--
	}
}

func (a *app) editDelete() {
	if a.st.selOn {
		a.st.input = append(append([]rune{}, a.st.input[:a.st.selA]...), a.st.input[a.st.selB:]...)
		a.st.cursor = a.st.selA
		a.st.selOn = false
		return
	}
	if a.st.cursor < len(a.st.input) {
		a.st.input = append(a.st.input[:a.st.cursor], a.st.input[a.st.cursor+1:]...)
	}
}

func (a *app) moveCursor(delta int) {
	a.st.cursor += delta
	if a.st.cursor < 0 {
		a.st.cursor = 0
	}
	if a.st.cursor > len(a.st.input) {
		a.st.cursor = len(a.st.input)
	}
	a.st.selOn = false
}

// moveSelection 上/下键：聊天页翻历史。
func (a *app) moveSelection(delta int) {
	a.scrollBy(delta)
}

// scrollBy 滚动当前页面（滚轮为正表示向下）。
func (a *app) scrollBy(delta int) {
	if a.st.tab == tabPanel {
		// 滚轮交给插件页面自己：滚到哪儿由它决定，我们只把"想滚多少"传下去，
		// 再按它夹过的位置记账（见 applyOutcome 的 page 分支）。
		a.st.pageScroll = clampInt(a.st.pageScroll+delta, 0, a.st.pageMax)
		return
	}
	a.st.scroll = clamp(a.st.scroll-delta, 0, maxInt(0, len(a.st.msgs)*6))
}

func (a *app) enter() {
	if a.st.tab != tabChat || a.st.shareOpen {
		return
	}
	// 拖进来的文件会变成一段路径文字，先认它一下；认出来就转成附件，这次不发送
	if a.maybeDroppedPath() {
		return
	}

	text := strings.TrimSpace(string(a.st.input))
	// 只发附件不写字是正当用法（"看这张图"），给个占位正文，
	// 免得屏幕上出现一条空消息、模型那头也只收到一张图没有指令。
	if text == "" && len(a.st.attachments) > 0 && a.st.editing == "" {
		text = "看看我发的附件。"
	}
	if text == "" {
		if a.st.editing != "" {
			a.st.notice = "修改后的内容不能为空；不想改就按 Esc"
			a.st.noticeOK = false
		}
		return
	}
	atts := a.st.attachments
	a.st.input = nil
	a.st.cursor = 0
	a.st.scroll = 0

	// 编辑态：回车 = 把这条消息提交成一个新版本
	if a.st.editing != "" {
		id := a.st.editing
		role := ""
		if m, ok := a.msgByID(id); ok {
			role = m.role
		}
		a.st.editing = ""
		a.st.editPrev = ""
		sess := a.st.sessID
		// 改用户说的话 = 重新问一遍，要调模型；改 AI 自己的话只是换个版本，不调
		a.convRun(role == "user", func() (convSnapshot, error) {
			return a.plug.convEdit(sess, id, text)
		})
		return
	}

	if a.st.convOK {
		sess := a.st.sessID
		a.convRun(true, func() (convSnapshot, error) { return a.plug.convSend(sess, text, atts) })
		// 发出去就清空：留着的话下一条消息会莫名其妙又带上同一张图
		a.st.attachments = nil
		return
	}

	// 没有 conversation.v1：退回原来那条直线聊天（这条路径不带附件，
	// 因为它直接调 llm.v1，没有透传附件的链路）
	if len(atts) > 0 {
		a.st.notice = "当前链路不支持附件（需要 conversation.v1 或 session.v1 + agent.v1）"
		a.st.noticeOK = false
	}
	a.st.attachments = nil
	a.st.msgs = append(a.st.msgs, msgView{role: "user", at: nowHM(), text: text})
	a.st.busy = true
	a.st.spinner = 0

	go func() {
		reply, via, err := a.plug.chat(text)
		a.results <- outcome{kind: "chat", reply: reply, via: via, err: err}
	}()
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

func (a *app) render() {
	w, h := a.term.Size()
	a.st.w, a.st.h = w, h

	a.canv = renderFrame(a.st, w, h, a.term.color)
	// 渲染时登记了"要显示但还没读"的东西，这里派出去读（读完会重绘一帧）：
	// 图片缩略图，以及插件自带页面的那一帧。
	a.pumpThumbs()
	a.pumpPages()
	frame := a.canv.frame()
	cursor := cursorSeq(a.st, w, h)

	// 光标位置必须一起比较：左右移动光标并不改变任何文字，
	// 只看画面的话这里会提前返回，终端光标就停在原地 —— 看起来像"左右键没反应"。
	if frame == a.lastFrame && cursor == a.lastCursor {
		return
	}
	a.lastFrame, a.lastCursor = frame, cursor

	a.term.WriteRaw("\033[H" + strings.ReplaceAll(frame, "\n", "\r\n") + cursor)
}

// cursorSeq 返回把终端光标摆到输入位置（或隐藏光标）的转义序列。
func cursorSeq(st *state, w, h int) string {
	if st.tab != tabChat || w < 44 || st.shareOpen || st.attachOpen {
		return "\033[?25l"
	}
	// 行与列都取自同一份测算：输入框是多行的，光标会出现在其中任意一行
	box := buildInputBox(st, w, h)
	return fmt.Sprintf("\033[%d;%dH\033[?25h", box.cursorRow(h), box.cursorCol())
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func nowHM() string { return time.Now().Format("15:04:05") }

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampInt 把 v 夹在 [lo, hi] 之间。
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

// humanize 把结构化响应变成一行人话：优先取主字段，其余字段缩进展示。
func humanize(reply, via string) string {
	clip := func(s string) string {
		rs := []rune(s)
		if len(rs) <= 4000 {
			return s
		}
		return string(rs[:4000]) + "\n…（内容过长，已截断）"
	}
	viaLine := func() string {
		if via == "" {
			return ""
		}
		return "\n    经由 " + via + " 返回"
	}

	if strings.TrimSpace(reply) == "" {
		return "（空响应）"
	}
	var plain string
	if jsonUnmarshal(reply, &plain) == nil {
		return clip(plain) + viaLine()
	}
	var obj map[string]any
	if jsonUnmarshal(reply, &obj) == nil {
		for _, key := range []string{"content", "echo", "text", "message", "reply", "answer"} {
			v, ok := obj[key].(string)
			if !ok || v == "" {
				continue
			}
			delete(obj, key)
			rest := prettyJSONAny(obj)
			if rest == "{}" || rest == "null" || rest == "" {
				return clip(v) + viaLine()
			}
			return clip(v) + "\n" + rest + viaLine()
		}
		return reply + viaLine()
	}
	return clip(reply) + viaLine()
}
