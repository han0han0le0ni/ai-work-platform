package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// backend 是界面需要的能力。抽成接口是为了能用假实现驱动整个主循环。
type backend interface {
	probeServices() []serviceStatus
	scanPlugins() ([]pluginInfo, error)
	// 启停与删除都要带目录名：目录里可能躺着两份 ID 相同的插件
	// （例如一份 x.plugin、一份 x.plugin.disabled），只认 ID 会动到另一份上去。
	setPluginEnabled(id, dir string, enabled bool) (string, error)
	deletePlugin(id, dir string) (string, error)
	wsBrowse(path string) (wsListing, error)
	wsSetRoot(path string) (wsListing, error)
	readAttachment(path string) (attachment, error)
	readImageBytes(path string) ([]byte, error)
	openPath(path string) (string, error)
	loadAgentBudget() (steps int, unlimited bool, err error)
	saveAgentBudget(steps int, unlimited bool) (string, error)
	loadLLM() (llmConfig, error)
	saveLLM(provider, baseURL, model, apiKey, visionModel string) (string, error)
	testLLM() (string, error)
	listModels(provider, baseURL, apiKey string) ([]string, string, error)
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

	// 浮窗位置要写进插件配置，而界面自己没有 ctx、不碰存储：交给 backend。
	saveFloat(x, y int, fold bool)
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

// llmConfig 是 llm.v1 providers 返回的配置快照。
type llmConfig struct {
	Provider string
	BaseURL  string
	Model    string
	APIKey   string // 打码后的值，界面永远拿不到明文
	// VisionModel 是"带图片时改用哪个模型"；空 = 跟主模型一样。
	VisionModel string
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
	models   []string
	cfg      *llmConfig
	apiKey   string // 保存失败时把用户输入的密钥还回输入框
	quiet    bool   // 回读配置时不覆盖底部状态
	ws       *wsListing
	budget   *agentBudget
	conv     *convSnapshot
	att      *attachment

	// 缩略图读完之后回传的内容
	thumbPath  string
	thumbLines []string
	thumbFail  bool
}

// agentBudget 是 agent.v1 的编排预算快照。
type agentBudget struct {
	Steps     int
	Unlimited bool
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

// defaultFields 是界面上的输入框集合。
//
// 抽成函数是必须的：测试里那份副本漏掉过一个新字段，结果文件页测试全绿而真机是空的。
// 只有一份来源，才不会飘。
func defaultFields() []formField {
	return []formField{
		{key: "baseUrl", label: "Base URL"},
		{key: "model", label: "模型"},
		{key: "visionModel", label: "识图模型"},
		{key: "apiKey", label: "API Key", secret: true},
		{key: "wsPath", label: "工作区"},
		{key: "agentSteps", label: "工具上限"},
	}
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
				text: "工作台已就绪。\n" +
					"· 上方标签可点击：聊天 / 插件 / 文件 / 设置\n" +
					"· 每条消息都是一个框：框上有「修改 / 重新生成 / 分享」，下角的 ‹ n/n › 翻版本\n" +
					"· 修改或重新生成会开出一条新时间线，旧的那条一条都不会删\n" +
					"· 「插件」页可以点击每个插件看它的服务、权限、依赖",
			}},
			fields: defaultFields(),

			// 浮窗还没被拖过：贴右上角（见 float.go 的 floatOrigin）
			floatX: -1,
			floatY: -1,
		},
		apply:   make(chan func(), 64),
		results: make(chan outcome, 16),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
		holdEOF: os.Getenv("AIWORK_UI_HOLD_ON_EOF") != "",
	}
}

// Start 进入事件循环（阻塞，应在 goroutine 中调用）。
func (a *app) Start() {
	a.events, a.stopInput = startInput(a.term, a.holdEOF)
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
		}
	}
}

// kickLoads 在后台把三份数据拉回来，绝不阻塞首帧渲染。
func (a *app) kickLoads() {
	go func() {
		a.results <- outcome{kind: "services", services: a.plug.probeServices()}
	}()
	go a.refreshPlugins()
	go a.reloadLLM(false)
	// 这三个内部各自起 goroutine；必须在这里同步调用 ——
	// kickLoads 跑在主循环启动之前，界面状态只能由这一个 goroutine 碰。
	a.loadConversation()
	a.reloadBudget()
	a.browseTo("") // 文件页先列出默认目录，切过去就能点
}

func (a *app) refreshPlugins() {
	ps, err := a.plug.scanPlugins()
	a.results <- outcome{kind: "plugins", plugins: ps, err: err}
}

// reloadLLM 回读一次配置。
//
// quiet=true 时不覆盖底部状态：保存后紧接着回读，不能让「密钥已写入」的确认
// 被「当前 provider」顶掉 —— 那正是用户看不出到底存没存上的原因。
func (a *app) reloadLLM(quiet bool) {
	cfg, err := a.plug.loadLLM()
	a.results <- outcome{kind: "llm", cfg: &cfg, err: err, quiet: quiet}
}

// reloadBudget 回读 agent 的编排预算。
func (a *app) reloadBudget() {
	a.st.budgetBusy = true
	go func() {
		steps, unlimited, err := a.plug.loadAgentBudget()
		a.results <- outcome{
			kind: "budget", budget: &agentBudget{Steps: steps, Unlimited: unlimited}, err: err,
		}
	}()
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
	case "budget":
		a.st.budgetBusy = false
		if out.err != nil {
			a.st.budgetLoaded = false
			a.st.budgetNote = "读取失败：" + out.err.Error()
			return
		}
		a.st.budgetLoaded = true
		a.st.budgetUnlimited = out.budget.Unlimited
		if f := a.st.field("agentSteps"); f != nil && !f.dirty {
			if out.budget.Unlimited {
				f.value = ""
				f.hint = "不限制"
			} else {
				f.value = fmt.Sprintf("%d", out.budget.Steps)
				f.hint = ""
			}
		}
		a.st.budgetNote = budgetSummary(*out.budget)

	case "services":
		a.st.services = out.services

	case "plugins":
		if out.err != nil {
			a.st.status = "读取插件目录失败：" + out.err.Error()
			a.st.statusOK = false
			return
		}
		a.st.plugins = out.plugins
		if a.st.selectedPlugin >= len(a.st.plugins) {
			a.st.selectedPlugin = 0
		}
		if !out.quiet {
			a.st.status = fmt.Sprintf("已读取 %d 个插件，%d 个在运行", len(out.plugins), countRunning(out.plugins))
			a.st.statusOK = true
		}

	case "llm":
		if out.err != nil {
			a.st.status = "读取 LLM 配置失败：" + out.err.Error()
			a.st.statusOK = false
			return
		}
		a.applyLLM(*out.cfg, !out.quiet)

	case "models":
		a.st.modelsBusy = false
		a.st.status = out.note
		a.st.statusOK = out.err == nil
		if out.err != nil {
			return
		}
		a.st.modelList = out.models
		// 只有一个模型就直接选上，省用户一次点击
		if len(out.models) == 1 {
			if f := a.st.field("model"); f != nil {
				f.value = out.models[0]
			}
		}

	case "saved":
		if out.err != nil {
			// 把用户输入的密钥还回输入框，只把失败原因说清楚
			if out.apiKey != "" {
				if f := a.st.field("apiKey"); f != nil {
					f.value = out.apiKey
					f.dirty = true
				}
			}
			a.st.status = out.note
			a.st.statusOK = false
			return
		}
		// 成功：清掉明文（密钥不该停在屏幕上），并把「已配置 ****」的提示挂上
		for i := range a.st.fields {
			a.st.fields[i].dirty = false
		}
		if f := a.st.field("apiKey"); f != nil {
			f.value = ""
			if out.apiKey != "" {
				f.hint = "已配置 " + maskForDisplay(out.apiKey)
			}
		}
		// 焦点从密钥框移开：否则用户盯着一个空框，会以为刚才白填了
		if a.st.focus == "apiKey" {
			a.st.focus = ""
		}
		a.st.status = out.note
		a.st.statusOK = true

	case "ws":
		a.st.wsBusy = false
		a.st.wsSetBusy = false
		if out.err != nil {
			a.st.status = firstNonEmpty(out.note, out.err.Error())
			a.st.statusOK = false
			return
		}
		if out.ws != nil {
			a.st.wsPath = out.ws.Path
			a.st.wsParent = out.ws.Parent
			a.st.wsEntries = out.ws.Entries
			a.st.wsRoot = out.ws.Root
			a.st.wsSel = 0
			a.st.wsScroll = 0
			// 浏览到的目录同步回输入框：这样"点着挑"和"手填"看到的永远是同一个路径
			if f := a.st.field("wsPath"); f != nil {
				f.value = out.ws.Path
				f.dirty = false
			}
		}
		a.st.status = firstNonEmpty(out.note, "目录已读取")
		a.st.statusOK = true

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

// applyLLM 把配置刷进表单。announce=false 时不动底部状态栏，
// 用于保存后的回读 —— 那时用户更需要看到「已写入」而不是「当前 provider」。
func (a *app) applyLLM(cfg llmConfig, announce bool) {
	a.st.provider = cfg.Provider

	if announce {
		shown := cfg.Provider
		if shown == "" {
			shown = "未配置"
		}
		a.st.status = "当前 provider：" + shown
		a.st.statusOK = true
	}

	// 必须按索引取指针：range 拿到的是元素副本，改副本不会写回切片
	for i := range a.st.fields {
		f := &a.st.fields[i]
		if f.dirty {
			continue // 用户改过的字段不许被后台刷新覆盖
		}
		switch f.key {
		case "baseUrl":
			f.value = cfg.BaseURL
			f.hint = ""
		case "model":
			f.value = cfg.Model
			f.hint = ""
		case "apiKey":
			// 明文拿不回来，只能显示打码值做提示；输入框留空表示"不改动"
			f.value = ""
			if cfg.APIKey == "" {
				f.hint = "未配置，点一下就能粘贴"
			} else {
				// 括号里的说明由区块标题统一交代，这里只留最重要的"已配置 + 打码值"
				f.hint = "已配置 " + cfg.APIKey
			}
		case "visionModel":
			// 与密钥一致：留空 = 不改动，所以当前值只放进提示里，不占输入框。
			// （若把值填进输入框，"留空=不改动"就永远不成立了）
			f.value = ""
			if cfg.VisionModel == "" {
				f.hint = "留空 = 跟主模型一样"
			} else {
				f.hint = "当前 " + cfg.VisionModel
			}
		}
	}

	// 按配置里的地址把界面上的服务选中状态还原出来
	a.st.presetKey = presetByBaseURL(cfg.BaseURL)
	a.st.modelList = nil
	if p, ok := presetByKey(a.st.presetKey); ok {
		a.st.modelList = append([]string{}, p.Models...)
	}

	// 当前模型如果不在列表里（比如自己填的），补到最前面，保证选中的那个看得见
	if m := a.fieldValue("model"); m != "" && !contains(a.st.modelList, m) {
		a.st.modelList = append([]string{m}, a.st.modelList...)
	}
	a.st.modelsNote = ""
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
			if a.grabFloat(ev.mouse.X, ev.mouse.Y) {
				return false
			}
			a.click(ev.mouse.X, ev.mouse.Y)
		case ev.mouse.Drag:
			a.dragFloat(ev.mouse.X, ev.mouse.Y)
		case ev.mouse.Rel:
			a.dropFloat()
		}
		return false
	}

	switch ev.key {
	case keyCtrlC:
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
		a.st.cursor = 0
	case keyEnd:
		a.st.cursor = len(a.st.input)
	case keyAltLeft:
		a.nudgeFloat(-2, 0)
	case keyAltRight:
		a.nudgeFloat(2, 0)
	case keyAltUp:
		a.nudgeFloat(0, -1)
	case keyAltDown:
		a.nudgeFloat(0, 1)
	case keyUp:
		if a.st.attachOpen {
			a.moveAttachSel(-1)
			return false
		}
		a.moveSelection(-1)
	case keyDown:
		if a.st.attachOpen {
			a.moveAttachSel(1)
			return false
		}
		a.moveSelection(1)
	case keyEnter:
		if a.st.attachOpen {
			a.addSelectedAttachment()
			return false
		}
		a.enter()
	case keyEsc:
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
		// 删除插件确认进行中：Esc 先撤掉确认，这也是最符合直觉的"反悔"键
		if a.st.confirmDelete {
			a.st.confirmDelete = false
			a.st.status = "已取消删除"
			a.st.statusOK = true
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

func (a *app) switchTab(delta int) {
	idx := 0
	for i, t := range allTabs {
		if t == a.st.tab {
			idx = i
		}
	}
	idx = (idx + delta + len(allTabs)) % len(allTabs)
	a.setTab(allTabs[idx])
}

// setTab 切页并重置焦点。
//
// 设置页会把底部提示改成配置状态：否则上一个页面的操作提示会一直挂在下面，
// 看起来像刚刚在这里发生过。
func (a *app) setTab(t tabKind) {
	a.st.tab = t
	if t == tabChat {
		a.st.focus = "chat"
		return
	}
	a.st.focus = ""
	if t == tabFiles {
		a.st.status = "当前工作区：" + firstNonEmpty(a.st.wsRoot, "未指定")
		a.st.statusOK = a.st.wsRoot != ""
		// 一进来就落在路径框上：选也好、填也好，都是从这一行开始
		if a.st.field("wsPath") != nil {
			a.st.focus = "wsPath"
		}
		// 还没读到东西就补一次：启动时那次可能撞上插件尚未就绪
		if len(a.st.wsEntries) == 0 && !a.st.wsBusy {
			a.browseTo(a.st.wsPath)
		}
		return
	}
	if t == tabSettings {
		a.st.status = "当前 provider：" + firstNonEmpty(a.st.provider, "未配置")
		a.st.statusOK = true
		// 切到设置页就先把 API Key 输入框聚焦：绝大多数人进来就是来粘密钥的，
		// 不该还要先点一下输入框才能打字。
		if a.st.field("apiKey") != nil {
			a.st.focus = "apiKey"
		}
		// agent 可能比界面晚就绪，启动那次没读到就在这里补一次
		if !a.st.budgetLoaded && !a.st.budgetBusy {
			a.reloadBudget()
		}
	}
}

// onTab 处理 Tab 键。
//
// 设置页正聚焦某个控件时，Tab 在控件之间移动，而不是切页 ——
// 这样即使粘贴内容里混进制表符，最多只是换个输入框，不会把整个页面弹走。
func (a *app) onTab() {
	if a.st.tab == tabSettings && a.st.focus != "" && a.st.focus != "none" {
		a.cycleFocus(1)
		return
	}
	a.switchTab(1)
}

// focusOrder 是设置页的焦点顺序（字段 + 按钮）。
func focusOrder(t tabKind) []string {
	if t != tabSettings {
		return nil
	}
	return []string{
		"baseUrl", "model", "apiKey", "btn:getmodels", "btn:save", "btn:test",
		"agentSteps", "budget:limited", "budget:unlimited", "btn:budgetsave",
	}
}

// cycleFocus 在当前页面的控件之间移动焦点。
func (a *app) cycleFocus(delta int) {
	keys := focusOrder(a.st.tab)
	if len(keys) == 0 {
		return
	}
	idx := -1
	for i, k := range keys {
		if k == a.st.focus {
			idx = i
		}
	}
	if idx < 0 {
		if delta > 0 {
			a.st.focus = keys[0]
		} else {
			a.st.focus = keys[len(keys)-1]
		}
		return
	}
	a.st.focus = keys[(idx+delta+len(keys))%len(keys)]
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
		for _, t := range allTabs {
			if t.key() == arg {
				a.setTab(t)
			}
		}

	case "svc":
		a.focusService(arg)

	case "plugin":
		// arg 是行号，不是插件 ID：目录里允许存在两份 ID 相同的插件
		// （x.plugin 与 x.plugin.disabled 就是），按 ID 找的话后一份永远选不中。
		if i := atoiSafe(arg); i >= 0 {
			a.selectPlugin(i)
		}

	case "wsdir":
		// 点目录进去：拼在浏览路径后面即可，分隔符的差异由插件侧解析
		a.browseTo(strings.TrimRight(a.st.wsPath, `/\`) + "/" + arg)

	case "wsfile":
		for i, e := range a.st.wsEntries {
			if e.Name == arg {
				a.st.wsSel = i
				a.st.status = "已选中文件 " + e.Name + "（" + humanSize(e.Size) + "）"
				a.st.statusOK = true
				break
			}
		}

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

	case "preset":
		a.applyPreset(arg)

	case "model":
		if f := a.st.field("model"); f != nil {
			f.value = arg
		}
		a.st.status = "已选模型：" + arg + "，点「保存并生效」写入"
		a.st.statusOK = true

	case "budget":
		a.pickBudgetMode(arg == "unlimited")

	case "core":
		// 标签栏上的「重启」：请核心重新加载插件（插件的启停 / 删除都是重启后生效）
		if arg == "restart" {
			a.restart()
		}

	case "focus":
		if arg == "none" {
			a.st.focus = ""
		} else {
			a.st.focus = arg
		}

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

// selectPlugin 选中第 i 个插件。
//
// 换选中时必须撤掉上一次的删除确认：否则在 A 上点了"删除插件"，
// 切到 B 再随手一点，删掉的就是 B 了。
func (a *app) selectPlugin(i int) {
	if i < 0 || i >= len(a.st.plugins) {
		return
	}
	a.st.selectedPlugin = i
	a.st.confirmDelete = false
	p := a.st.plugins[i]
	a.st.tab = tabPlugins
	a.st.status = "已选中 " + p.ID + " · " + pluginStateText(p)
	a.st.statusOK = p.running()
}

// selected 返回当前选中的插件。
func (a *app) selected() (pluginInfo, bool) {
	if a.st.selectedPlugin < 0 || a.st.selectedPlugin >= len(a.st.plugins) {
		return pluginInfo{}, false
	}
	return a.st.plugins[a.st.selectedPlugin], true
}

// focusService 点侧栏里的某个服务：跳到插件页并选中它的提供者。
func (a *app) focusService(name string) {
	for i, p := range a.st.plugins {
		if contains(p.Provides, name) {
			a.st.selectedPlugin = i
			a.st.tab = tabPlugins
			if p.running() {
				a.st.status = "服务 " + name + " 由 " + p.ID + " 提供"
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

	case "rescan":
		// 手动那一次要出声：用户点了按钮，就得看到「正在重新扫描…」
		a.rescan(false)

	case "getmodels":
		// 直接把表单里的值传下去：先试能不能连上，再去保存，顺序更顺手
		provider := a.st.provider
		baseURL := a.fieldValue("baseUrl")
		apiKey := a.fieldValue("apiKey")
		a.st.modelsBusy = true
		a.st.status = "正在向服务端要模型列表…"
		a.st.statusOK = true
		go func() {
			models, note, err := a.plug.listModels(provider, baseURL, apiKey)
			a.results <- outcome{kind: "models", models: models, note: note, err: err}
		}()

	case "save":
		provider := a.st.provider
		baseURL := a.fieldValue("baseUrl")
		model := a.fieldValue("model")
		apiKey := a.fieldValue("apiKey")
		visionModel := a.fieldValue("visionModel")
		if provider == "" && baseURL == "" && model == "" && apiKey == "" && visionModel == "" {
			a.st.status = "没有可保存的改动"
			a.st.statusOK = false
			return
		}
		a.st.status = "正在保存…"
		a.st.statusOK = true
		// 关键：这里**先不动输入框**。保存成功才清明文并给出打码确认；
		// 失败就把密钥原样留着让用户重试 —— 否则一次网络抖动就要重新粘一遍。
		go func() {
			note, err := a.plug.saveLLM(provider, baseURL, model, apiKey, visionModel)
			a.results <- outcome{kind: "saved", note: note, err: err, apiKey: apiKey}
			if err == nil {
				// 保存成功后回读一次，把服务端的打码值刷到界面上（quiet：别顶掉确认信息）
				a.reloadLLM(true)
			}
		}()

	case "test":
		a.st.status = "正在测试连接…"
		a.st.statusOK = true
		go func() {
			note, err := a.plug.testLLM()
			a.results <- outcome{kind: "status", note: note, err: err}
		}()

	case "toggle":
		a.toggleSelected()

	case "del":
		p, ok := a.selected()
		if !ok {
			return
		}
		// 删除不可逆，先要一次确认
		a.st.confirmDelete = true
		a.st.status = "删除 " + p.ID + " 不可撤销（连配置与日志一起），确认请点「确认删除」"
		a.st.statusOK = false

	case "delconfirm":
		a.st.confirmDelete = false
		a.deleteSelected()

	case "delcancel":
		a.st.confirmDelete = false
		a.st.status = "已取消删除"
		a.st.statusOK = true

	case "wsset":
		a.setWorkspaceRoot()

	case "wsrefresh":
		a.browseTo(a.st.wsPath)

	case "wsup":
		if a.st.wsParent != "" {
			a.browseTo(a.st.wsParent)
		} else {
			a.st.status = "已经在最顶层了"
			a.st.statusOK = true
		}

	case "budgetsave":
		a.saveBudget()

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

// pickBudgetMode 选中「限制步数 / 不限制」。选完还要点「应用预算」才写下去 ——
// 这和这一页其他地方的手感一致：先摆好，再点保存。
func (a *app) pickBudgetMode(unlimited bool) {
	a.st.budgetUnlimited = unlimited
	if unlimited {
		a.st.budgetNote = "已选「不限制」，点「应用预算」写入"
		return
	}
	a.st.budgetNote = "已选「限制步数」，点「应用预算」写入"
}

// saveBudget 把预算写给 agent。
func (a *app) saveBudget() {
	if a.st.budgetBusy {
		return
	}
	unlimited := a.st.budgetUnlimited
	steps := 0
	if !unlimited {
		n, err := strconv.Atoi(a.fieldValue("agentSteps"))
		if err != nil || n <= 0 {
			a.st.status = "工具上限要填一个正整数；不想限制就选「不限制」"
			a.st.statusOK = false
			return
		}
		steps = n
	}
	a.st.budgetBusy = true
	a.st.status = "正在应用预算…"
	a.st.statusOK = true
	go func() {
		note, err := a.plug.saveAgentBudget(steps, unlimited)
		a.results <- outcome{kind: "status", note: note, err: err}
		a.post(func() { a.reloadBudget() })
	}()
}

// browseTo 在文件页里切换当前浏览的目录。
//
// 界面可能比 workspace 插件先起来 —— 插件是逐个启动的，实测两者能差 9 毫秒。
// 那一次调用会撞上"服务未找到"，所以失败要退避重试几次，而不是把空列表摆在用户面前。
func (a *app) browseTo(path string) {
	if a.st.wsBusy {
		// 上一次还没回来。这里要是悄悄把点击吞掉，用户看到的就是"点了没反应" ——
		// 手快连点两下目录时特别明显。说一句，让他知道是等一下而不是点错了。
		a.st.status = "正在读取目录，稍等一下再点"
		a.st.statusOK = true
		return
	}
	a.st.wsBusy = true
	a.st.status = "正在读取目录…"
	a.st.statusOK = true
	go func() {
		var ls wsListing
		var err error
		for attempt := 0; attempt < 4; attempt++ {
			if ls, err = a.plug.wsBrowse(path); err == nil {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		a.results <- outcome{kind: "ws", ws: &ls, err: err, note: ls.Note}
	}()
}

// setWorkspaceRoot 把工作区设成输入框里的路径（留空则用当前浏览到的目录）。
//
// 这就是"可选可填"的合流点：点着挑也好、粘路径也好，最后都走这里。
func (a *app) setWorkspaceRoot() {
	// 只看"设置"自己是否在途，不看读目录 —— 进页面时的懒加载正好在读，
	// 用户这时候按回车必须照样生效，否则就是"回车没反应"。
	if a.st.wsSetBusy {
		return
	}
	path := a.fieldValue("wsPath")
	if path == "" {
		path = a.st.wsPath
	}
	if path == "" {
		a.st.status = "先填一个路径，或在下面的目录里点进去挑一个"
		a.st.statusOK = false
		return
	}
	a.st.wsSetBusy = true
	a.st.status = "正在设置工作区…"
	a.st.statusOK = true
	go func() {
		ls, err := a.plug.wsSetRoot(path)
		a.results <- outcome{kind: "ws", ws: &ls, err: err, note: ls.Note}
	}()
}

// toggleSelected 启停当前选中的插件。
func (a *app) toggleSelected() {
	if a.st.pluginBusy {
		return
	}
	p, ok := a.selected()
	if !ok {
		return
	}
	if p.Self {
		a.st.status = "这个插件不能停用：停掉之后就没法管理插件了"
		a.st.statusOK = false
		return
	}
	enable := p.Disabled // 当前是禁用的就启用，反之禁用
	verb := "禁用"
	if enable {
		verb = "启用"
	}
	a.st.pluginBusy = true
	a.st.status = "正在" + verb + " " + p.ID + "…"
	a.st.statusOK = true
	go func() {
		note, err := a.plug.setPluginEnabled(p.ID, p.Dir, enable)
		a.results <- outcome{kind: "status", note: note, err: err}
		a.finishPluginAction()
	}()
}

// deleteSelected 删除当前选中的插件。
func (a *app) deleteSelected() {
	if a.st.pluginBusy {
		return
	}
	p, ok := a.selected()
	if !ok {
		return
	}
	if p.Self {
		a.st.status = "这个插件不能删除"
		a.st.statusOK = false
		return
	}
	a.st.pluginBusy = true
	a.st.status = "正在删除 " + p.ID + "…"
	a.st.statusOK = true
	go func() {
		note, err := a.plug.deletePlugin(p.ID, p.Dir)
		a.results <- outcome{kind: "status", note: note, err: err}
		a.finishPluginAction()
	}()
}

// finishPluginAction 操作收尾：解除按钮置灰并重新扫描插件清单。
func (a *app) finishPluginAction() {
	a.post(func() { a.st.pluginBusy = false })
	a.refreshPlugins()
}

func (a *app) fieldValue(key string) string {
	if f := a.st.field(key); f != nil {
		return strings.TrimSpace(f.value)
	}
	return ""
}

// ---------------------------------------------------------------------------
// 编辑
// ---------------------------------------------------------------------------

// typeTarget 返回当前键入该落到哪个字段；返回 nil 表示落到聊天输入框。
//
// 设置页在没有任何输入框聚焦时，默认把输入落到 API Key 上：
// 用户进设置页十有八九就是来粘密钥的，不该因为"没先点一下输入框"而打字、粘贴都没反应。
func (a *app) typeTarget() *formField {
	if a.st.tab == tabChat {
		return nil
	}
	if f := a.st.field(a.st.focus); f != nil {
		return f
	}
	if a.st.tab == tabSettings {
		if f := a.st.field("apiKey"); f != nil {
			a.st.focus = "apiKey"
			return f
		}
	}
	// 文件页同理：进这一页十有八九就是要填路径，不该还要先点一下输入框
	if a.st.tab == tabFiles {
		if f := a.st.field("wsPath"); f != nil {
			a.st.focus = "wsPath"
			return f
		}
	}
	return nil
}

// insertRune 插入一个字符。
//
// 控制字符（制表符 / 换行 / 退格）一律丢弃 —— 它们多半是复制粘贴带进来的噪音，
// 而制表符在界面里是"切页"键，放进去就等于一次粘贴把用户弹回聊天页。
// 真正按下的 Tab / Enter 走的是 keyTab / keyEnter，不受这里影响。
func (a *app) insertRune(r rune, pasted bool) {
	if r < 0x20 || r == 0x7f || (r >= 0xD800 && r <= 0xDFFF) {
		return
	}

	f := a.typeTarget()
	if f == nil {
		if a.st.tab != tabChat || a.st.shareOpen || a.st.attachOpen {
			return // 面板占着聊天区时，打字不该落到任何地方
		}
		a.st.input = append(a.st.input, 0)
		copy(a.st.input[a.st.cursor+1:], a.st.input[a.st.cursor:])
		a.st.input[a.st.cursor] = r
		a.st.cursor++
		return
	}

	f.value += string(r)
	f.dirty = true
	if pasted && f.secret {
		// 粘进密钥框时给一句确认（只说长度，绝不回显内容）
		a.st.status = fmt.Sprintf("密钥框已收到 %d 个字符，点「保存并生效」写入", len([]rune(f.value)))
		a.st.statusOK = true
	}
}

func (a *app) editBackspace() {
	if f := a.typeTarget(); f != nil {
		rs := []rune(f.value)
		if len(rs) > 0 {
			f.value = string(rs[:len(rs)-1])
			f.dirty = true
		}
		return
	}
	if a.st.cursor > 0 {
		a.st.input = append(a.st.input[:a.st.cursor-1], a.st.input[a.st.cursor:]...)
		a.st.cursor--
	}
}

func (a *app) editDelete() {
	if f := a.typeTarget(); f != nil {
		f.value = ""
		f.dirty = true
		return
	}
	if a.st.cursor < len(a.st.input) {
		a.st.input = append(a.st.input[:a.st.cursor], a.st.input[a.st.cursor+1:]...)
	}
}

func (a *app) moveCursor(delta int) {
	if a.typeTarget() != nil {
		return
	}
	a.st.cursor += delta
	if a.st.cursor < 0 {
		a.st.cursor = 0
	}
	if a.st.cursor > len(a.st.input) {
		a.st.cursor = len(a.st.input)
	}
}

// moveSelection 上/下键：聊天页翻历史，插件页换选中，设置页换焦点。
func (a *app) moveSelection(delta int) {
	switch a.st.tab {
	case tabPlugins:
		if len(a.st.plugins) == 0 {
			return
		}
		next := a.st.selectedPlugin + delta
		if next < 0 {
			next = 0
		}
		if next >= len(a.st.plugins) {
			next = len(a.st.plugins) - 1
		}
		a.st.pluginScroll = clamp(next-2, 0, maxInt(0, len(a.st.plugins)-1))
		// 键盘选择也要同步状态栏，否则状态栏会停留在上一次点击的结果上
		a.selectPlugin(next)

	case tabFiles:
		// 上下键在目录条目之间移动；列表为空时把焦点交给输入框，方便直接打字
		if len(a.st.wsEntries) == 0 {
			a.st.focus = "wsPath"
			return
		}
		next := clamp(a.st.wsSel+delta, 0, len(a.st.wsEntries)-1)
		a.st.wsSel = next
		a.st.wsScroll = clamp(next-2, 0, maxInt(0, len(a.st.wsEntries)-1))
		e := a.st.wsEntries[next]
		if e.Dir {
			a.st.status = "目录 " + e.Name + "：点它 / 回车进入"
		} else {
			a.st.status = "文件 " + e.Name + "（" + humanSize(e.Size) + "）"
		}
		a.st.statusOK = true

	case tabSettings:
		// 上下键在设置页的控件之间移动（字段 + 按钮）
		if a.st.field(a.st.focus) == nil && !strings.HasPrefix(a.st.focus, "btn:") {
			a.st.focus = "apiKey"
			return
		}
		a.cycleFocus(delta)

	default:
		a.scrollBy(delta)
	}
}

// scrollBy 滚动当前页面（滚轮为正表示向下）。
func (a *app) scrollBy(delta int) {
	switch a.st.tab {
	case tabPlugins:
		a.st.pluginScroll = clamp(a.st.pluginScroll+delta, 0, maxInt(0, len(a.st.plugins)-1))
	case tabFiles:
		a.st.wsScroll = clamp(a.st.wsScroll+delta, 0, maxInt(0, len(a.st.wsEntries)-1))
	default:
		a.st.scroll = clamp(a.st.scroll-delta, 0, maxInt(0, len(a.st.msgs)*6))
	}
}

func (a *app) enter() {
	// 文件页：回车 = 把输入框里的路径设为工作区（填完不用再去点按钮）
	if a.st.tab == tabFiles {
		a.setWorkspaceRoot()
		return
	}
	if a.st.tab == tabSettings {
		// 焦点在按钮上：回车就是按下去
		if key, ok := strings.CutPrefix(a.st.focus, "btn:"); ok {
			a.press(key)
			return
		}
		// 焦点在「限制/不限制」上：回车就是选中它
		if key, ok := strings.CutPrefix(a.st.focus, "budget:"); ok {
			a.pickBudgetMode(key == "unlimited")
			return
		}
		// 在字段上：回车 = 交给下一个控件。
		// 刻意不清空焦点 —— 清空的话紧接着的粘贴又会没有落点，用户又得先点一下输入框。
		a.cycleFocus(1)
		a.st.status = "输入已确认，点「保存并生效」写入"
		a.st.statusOK = true
		return
	}
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
	// 渲染时登记了"要显示但还没读"的图片，这里派出去读（读完会重绘一帧）
	a.pumpThumbs()
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
	return fmt.Sprintf("\033[%d;%dH\033[?25h", h-1, cursorPos(st, w)+1)
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
