package main

import (
	"fmt"
	"strconv"
	"strings"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// 插件自带的页面（ui.page.*）：宿主只管借地方，画什么由插件说了算
//
// 以前每加一个功能插件，都要在这个界面里加一页（标签、字段、按钮、动作分支
// 一整套），"界面不认识角色扮演"这句话其实并不成立。现在的分工是：
//
//	插件   自己把画面画成"文本行 + 可点区域"，自己处理点击与按键
//	界面   只提供一块 w×h 的地方、把点击和按键转过去、把行原样贴上去
//
// 于是装一个带界面的插件就多一个标签页，这个文件一行都不用改。
//
// 约定：插件在 manifest 的 provides 里加一个 ui.page.<名字>.v1，并在它上面
// 实现 list / render / action / key。**不用统一的 ui.page.v1** 是因为服务名
// 在注册表里是一对一的，第二个插件声明同名服务会被拒 —— 那"自带界面"就只
// 容得下一个插件了。
//
// 这一层刻意不认识的：插件的业务、它的状态、它想画什么。宿主只认识四条规则 ——
// 行不能比给定宽度宽、可点区域用面板内坐标、动作名原样送回、变了要喊一声。
// ---------------------------------------------------------------------------

// pageServicePrefix 是"插件自带页面"的服务名前缀。
const pageServicePrefix = "ui.page."

// pageChangedEvent 是插件通知"我这一页变了，重新画一遍"的事件名。
//
// 少了它，界面就会一直停在旧内容上：宿主只在收到它（或换了尺寸、滚动位置）
// 时才重新要一帧 —— 那看起来就是"点了没反应"。
const pageChangedEvent = "ui.panel.changed"

// hostActionPrefix 是留给宿主自己的动作前缀。
//
// 插件定义的动作名原样送回插件，唯独 @ui: 开头的是"我想让界面做点什么"
// （比如把一段文字放进聊天输入框）。这样插件不用去猜界面的内部结构，
// 界面也不必知道插件在干什么。
const hostActionPrefix = "@ui:"

// pagePanel 是宿主记下的"某个插件提供了某一页"。
type pagePanel struct {
	svc   string // 提供它的服务名
	id    string // 页面 id（一个插件可以提供多页）
	title string
	order int
}

// ref 是这一页的全局标识：缓存键、动作分发都用它。
func (p pagePanel) ref() string { return p.svc + "/" + p.id }

// pageReq 是一次渲染请求：哪一页、多大地方、滚到哪儿。
type pageReq struct {
	svc, id string
	w, h    int
	scroll  int
}

// pageRender 是插件渲染回来的一帧，连带"它是为多大地方、滚到哪儿画的"。
//
// 位置信息必须一起存：缓存是按 (页, 宽, 高, 滚动) 分的，光存内容对不上号。
type pageRender struct {
	svc, id string
	w, h    int
	scroll  int
	out     plugin.PanelRender
}

// pageKeyMsg 是一次要转给插件的按键。
type pageKeyMsg struct {
	svc, id string
	kind    string
	text    string
}

// pageHitRef 是这一帧登记的一个可点区域。
type pageHitRef struct {
	svc, id string
	hit     plugin.Hit
}

// pageCacheKey 是缓存的键：尺寸或滚动位置一变，画面就得重新要。
func pageCacheKey(ref string, w, h, scroll int) string {
	return fmt.Sprintf("%s|%d|%d|%d", ref, w, h, scroll)
}

// pageOf 按 ref 找当前清单里的那一页。
func pageOf(pages []pagePanel, ref string) (pagePanel, bool) {
	for _, p := range pages {
		if p.ref() == ref {
			return p, true
		}
	}
	return pagePanel{}, false
}

// activePage 返回当前显示的那一页。
func (st *state) activePage() (pagePanel, bool) {
	return pageOf(st.pages, st.pageSvc+"/"+st.pageID)
}

// pageRefName 是给用户看的页面标识（找不到就退回服务名）。
func pageRefName(st *state) string {
	if p, ok := st.activePage(); ok && p.title != "" {
		return p.title
	}
	return firstNonEmpty(st.pageID, st.pageSvc)
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

// pageRows 画插件自带的一页。
//
// 画的是插件**上一帧**返回的内容：渲染是同步的、每帧都要跑，而去插件那儿要
// 一帧要走一次 RPC —— 不能为了画一帧就问一次。所以这里的规矩是"有缓存就画，
// 没有就先画占位、顺手把需求记下来"，由主循环在渲染之后统一去要（见 pumpPages）。
// 缩略图走的是同一套路。
func pageRows(st *state, w, h int) []rowFn {
	if w <= 0 || h <= 0 {
		return nil
	}
	// 热区先清空：**只有真的画出一帧**才重新登记。否则一页没了（插件被删）
	// 或者帧被作废时，屏幕上还留着上一次那些可点区域 —— 点下去会把动作
	// 发给一个已经不在的插件。
	st.pageHits = nil

	if _, ok := st.activePage(); !ok {
		// 清单里已经没有这一页了（插件被删或被禁用）
		return pageNoteRows(st, w, h, "这一页所属的插件不在了", false)
	}

	key := pageCacheKey(st.pageSvc+"/"+st.pageID, w, h, st.pageScroll)
	v, ok := st.pageCache[key]
	if !ok {
		st.pageWanted = append(st.pageWanted, pageReq{
			svc: st.pageSvc, id: st.pageID, w: w, h: h, scroll: st.pageScroll,
		})
		msg := "正在向插件要这一帧…"
		if st.pagesBusy {
			msg = "正在读取插件页面…"
		}
		return pageNoteRows(st, w, h, msg, true)
	}

	// 热区：动作名用**这一帧的下标**，不把插件的动作名拼进动作串。
	// 动作名是插件自由定义的，可能带冒号、竖线、空格，拼进去会被点击分发切错。
	// （插件页用行号、这里用下标，是同一个道理。）
	byRow := map[int][]int{}
	for _, hit := range v.out.Hits {
		if hit.Y < 0 || hit.Y >= h || hit.W <= 0 {
			continue
		}
		idx := len(st.pageHits)
		st.pageHits = append(st.pageHits, pageHitRef{svc: v.svc, id: v.id, hit: hit})
		byRow[hit.Y] = append(byRow[hit.Y], idx)
	}
	if v.out.Title != "" {
		st.pageTitle = v.out.Title
	}
	st.pageNote = v.out.Note
	st.pageWantKeys = v.out.WantKeys
	st.pageWantCopy = v.out.WantCopy

	rows := make([]rowFn, 0, h)
	for y := 0; y < h; y++ {
		line := ""
		if y < len(v.out.Lines) {
			line = v.out.Lines[y]
		}
		rows = append(rows, pageLineRow(line, w, byRow[y], st))
	}
	return rows
}

// pageLineRow 把插件给的一行贴到屏幕上。
//
// 宽度必须严丝合缝：插件算错了行宽，右边的竖线就会跟着歪，而且一歪一整屏。
// 所以这里先量一遍 —— 量出来不对就剥掉样式按纯文本重排。宁可难看，不能歪。
func pageLineRow(line string, w int, hits []int, st *state) rowFn {
	return func(c *canvas, l *lineBuilder, y, x int) {
		if plainWidth(line) != w {
			line = padDisplay(cutDisplay(stripANSI(line), w), w)
		} else if !c.color {
			// 无色终端下把样式码剥掉：留着就是满屏 [38;5;111m 这样的乱码
			line = stripANSI(line)
		}
		l.raw(line, w)
		for _, i := range hits {
			h := st.pageHits[i].hit
			c.box("panel:"+strconv.Itoa(i), x+h.X, y, h.W)
		}
	}
}

// pageNoteRows 画一段居中的说明（占位、错误、空态都用它）。
func pageNoteRows(st *state, w, h int, msg string, dim bool) []rowFn {
	style := cBad
	if dim {
		style = cDim
	}
	rows := make([]rowFn, 0, h)
	rows = append(rows, textRow(newCell(segment{style, " " + truncDisplay(msg, maxInt(0, w-2))}), w))
	for len(rows) < h {
		rows = append(rows, nil)
	}
	return rows
}

// pageErrorView 把一次失败做成一帧，好走和正常帧一样的缓存与渲染。
//
// 不这么做的话，失败要么被静默吞掉（用户看到一片空白，以为界面坏了），
// 要么每帧都重试（把插件按在地上反复打）。
func pageErrorView(req pageReq, err error) *pageRender {
	b := plugin.NewBlock(maxInt(1, req.w), maxInt(1, req.h))
	b.At(0).Text(plugin.SBad, " "+plugin.Truncate("这一页没能画出来："+err.Error(), maxInt(0, req.w-2)))
	b.At(1).Text(plugin.SDim, " 插件可能已经不在了，或者它的 render 出了错")
	return &pageRender{
		svc: req.svc, id: req.id, w: req.w, h: req.h, scroll: req.scroll,
		out: plugin.PanelRender{
			Lines: b.Lines(),
			Note:  "这一帧是界面替你兜的底：插件没有正常回答",
		},
	}
}

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------

// pageHit 处理一次面板内的点击。
//
// 动作名与参数都由插件定义，界面一概不解析 —— 唯一例外是 @ui: 开头的那几个，
// 那是留给"插件想跟界面说句话"的口子。
func (a *app) pageHit(ref pageHitRef) {
	if strings.HasPrefix(ref.hit.Action, hostActionPrefix) {
		a.pageHostAction(ref.hit)
		return
	}
	svc, id, action, arg := ref.svc, ref.id, ref.hit.Action, ref.hit.Arg
	go func() {
		note, err := a.plug.pageAction(svc, id, action, arg)
		a.results <- outcome{kind: "pageact", note: note, err: err}
	}()
}

// pageHostAction 处理宿主自己的那几个动作。
//
// 插件定义的动作一律原样送回插件；只有 @ui: 开头的是"我想让界面做点什么"。
// 这两个口子都刻意很窄：插件不该知道聊天页长什么样，也不该自己去改输入框；
// 而"重启核心"影响的是整个平台（连界面自己都会被收掉），只有界面知道
// 该先往屏幕上写一句话再动手。
func (a *app) pageHostAction(hit plugin.Hit) {
	switch strings.TrimPrefix(hit.Action, hostActionPrefix) {
	case "chat":
		text := hit.Arg
		if strings.TrimSpace(text) == "" {
			return
		}
		a.setTab(tabChat)
		a.st.input = []rune(text)
		a.st.cursor = len(a.st.input)
		a.st.notice = "插件把这段文字放进了输入框：改完点「发送」"
		a.st.noticeOK = true

	case "restart":
		a.restart()
	}
}

// pageKey 把一次按键转给当前这一页。返回 false 表示"这次不归它管"。
//
// Tab（切页）永远留给宿主 —— 那是用户仅有的退路，交给插件的话，一个写坏的
// 页面就能把人困在里面。Ctrl+C 是复制（无论页面声明没声明 WantCopy，都不退出）。
func (a *app) pageKey(ev event) bool {
	if a.st.tab != tabPanel || a.st.pageID == "" || !a.st.pageWantKeys {
		return false
	}
	// Ctrl+C：页面声明了"有可选的文本，交给我复制"（WantCopy）就转给它；
	// 没声明的页面留给宿主处理（复制或提示，见 handle）。退出是 Ctrl+Q。
	if ev.key == keyCtrlC {
		if !a.st.pageWantCopy {
			return false
		}
		a.sendPageKey(a.st.pageSvc, a.st.pageID, "ctrl+c", "")
		return true
	}
	kind, text, ok := pageKeyOf(ev)
	if !ok {
		return false
	}
	a.sendPageKey(a.st.pageSvc, a.st.pageID, kind, text)
	return true
}

// sendPageKey 把一次按键放进转给插件的那条队列。
func (a *app) sendPageKey(svc, id, kind, text string) {
	select {
	case a.pageKeys <- pageKeyMsg{svc: svc, id: id, kind: kind, text: text}:
	default:
		// 队列满了只能丢：宁可少一个字符，也不能把界面主循环卡在插件身上
	}
}

// pageKeyOf 把界面的事件翻成插件看得懂的按键名。
//
// 插件拿到的是"退格""回车"这样的语义，不是终端字节 —— 各家终端的转义序列
// 不一样，这层脏活留给界面，插件不该知道。
func pageKeyOf(ev event) (kind, text string, ok bool) {
	switch ev.key {
	case keyRune:
		return "rune", string(ev.r), true
	case keyEnter:
		return "enter", "", true
	case keyCtrlEnter:
		return "ctrl+enter", "", true
	case keyBackspace:
		return "backspace", "", true
	case keyDelete:
		return "delete", "", true
	case keyLeft:
		return "left", "", true
	case keyRight:
		return "right", "", true
	case keyUp:
		return "up", "", true
	case keyDown:
		return "down", "", true
	case keyHome:
		return "home", "", true
	case keyEnd:
		return "end", "", true
	case keyEsc:
		return "esc", "", true
	case keyCtrlL:
		return "ctrl+l", "", true
	}
	return "", "", false
}

// ---------------------------------------------------------------------------
// 取帧
// ---------------------------------------------------------------------------

// pumpPages 把这一帧渲染时"想要但还没有"的页面帧都派出去要。
//
// 与缩略图同一个理由：渲染是同步的、每帧都要跑，而向插件要一帧要走一次 RPC，
// 所以只能反着来 —— 渲染时只登记需求，渲染完由这里统一发起，回来再重绘一帧。
func (a *app) pumpPages() {
	if len(a.st.pageWanted) == 0 {
		return
	}
	wanted := a.st.pageWanted
	a.st.pageWanted = nil
	if a.st.pagePending == nil {
		a.st.pagePending = map[string]bool{}
	}
	for _, req := range wanted {
		key := pageCacheKey(req.svc+"/"+req.id, req.w, req.h, req.scroll)
		if a.st.pagePending[key] {
			continue
		}
		a.st.pagePending[key] = true
		go func(req pageReq, key string) {
			out, err := a.plug.pageRender(req.svc, req.id, req.w, req.h, req.scroll)
			a.results <- outcome{
				kind: "page", pageKey: key, pageReq: req, pageOut: out, err: err,
			}
		}(req, key)
	}
}

// pageKeyResult 是插件对一次按键的回应。
//
// 复制不是插件能自己做的：它没有剪贴板权限，得请宿主动手。所以 Ctrl+C 时
// 插件有选择就回一个 copy（宿主写剪贴板），没选择就回空。
//
// exit 是防御性保留的：官方插件都不再回 exit（退出走宿主的 Ctrl+Q），但万一
// 有第三方插件还想用"按键请求退出"这个口子，宿主照样认。
type pageKeyResult struct {
	copyText string
	exit     bool
}

// startPageKeys 起一条"按顺序把按键送给插件"的专职 goroutine。
//
// 为什么不能一个键起一个 goroutine：两个并发的 RPC 谁先写进管道是不确定的，
// 打字就会串成乱码。一条队列、一个消费者，顺序才有保证。
func (a *app) startPageKeys() {
	go func() {
		for {
			select {
			case msg := <-a.pageKeys:
				res, err := a.plug.pageKey(msg.svc, msg.id, msg.kind, msg.text)
				if err != nil {
					a.results <- outcome{kind: "status", note: "页面没收到按键：" + err.Error(), err: err}
					continue
				}
				if res.copyText != "" {
					a.post(func() {
						if a.copyToClipboard(res.copyText) {
							a.tell(fmt.Sprintf("已复制 %d 个字符", len([]rune(res.copyText))), true)
						} else {
							a.tell("复制失败：内容过长或终端不支持剪贴板", false)
						}
					})
				}
				if res.exit {
					a.Stop()
				}
			case <-a.done:
				return
			}
		}
	}()
}

// loadPages 去问一遍"有哪些插件自带页面"。
//
// 只问一次（启动时、以及在清单变化后补一次）。哪些插件报了页面是从已经扫好的
// manifest 里挑出来的，这里只补上各页自报的标题与顺序。
func (a *app) loadPages() {
	if a.st.pagesBusy {
		return
	}
	a.st.pagesBusy = true
	go func() {
		list, err := a.plug.pageList(pageServices(a.st.plugins))
		a.results <- outcome{kind: "pages", pages: list, err: err}
	}()
}

// pageServices 从已扫描的插件清单里挑出"自带页面"的服务。
//
// 从 manifest 里发现而不是靠事件广播：插件是逐个启动的，广播先到还是订阅先到
// 全看运气；而插件清单一向扫得到，服务名也一向写在里面。
func pageServices(ps []pluginInfo) []string {
	var out []string
	for _, p := range ps {
		for _, svc := range p.Alive {
			if strings.HasPrefix(svc, pageServicePrefix) {
				out = append(out, svc)
			}
		}
	}
	return out
}

// dropPlugin 处理"一个插件被删掉"：记住它，并把它的页面当场下线。
//
// 不能只靠重扫：删除是重启生效的，正在运行的插件目录此刻还在，扫描照样能看到
// 它、把它的页面加回来。所以这里做两件重扫做不了的事 ——
//  1. 把"它被删了"记进 droppedPlugins，之后每一轮扫描结果都把它滤掉；
//  2. 立刻把它的 ui.page.* 页面从清单里摘掉，帧缓存、在途请求一起作废。
func (a *app) dropPlugin(pluginID, dir string) {
	if a.st.droppedPlugins == nil {
		a.st.droppedPlugins = map[string]bool{}
	}
	if pluginID != "" {
		a.st.droppedPlugins[pluginID] = true
	}
	if dir != "" {
		a.st.droppedPlugins[dir] = true
	}

	// 找出它提供的 ui.page.* 服务
	var pageSvcs []string
	for _, p := range a.st.plugins {
		if p.ID == pluginID || p.Dir == dir {
			for _, svc := range p.Provides {
				if strings.HasPrefix(svc, pageServicePrefix) {
					pageSvcs = append(pageSvcs, svc)
				}
			}
		}
	}
	if len(pageSvcs) == 0 {
		return
	}
	drop := make(map[string]bool, len(pageSvcs))
	for _, svc := range pageSvcs {
		drop[svc] = true
	}
	kept := a.st.pages[:0]
	for _, p := range a.st.pages {
		if drop[p.svc] {
			a.invalidatePage(p.svc, p.id)
			continue
		}
		kept = append(kept, p)
	}
	a.st.pages = kept

	// 正看着它的页 → 退回聊天
	if a.st.tab == tabPanel {
		if _, ok := a.st.activePage(); !ok {
			name := pageRefName(a.st)
			a.setTab(tabChat)
			a.st.notice = "「" + name + "」这一页的插件不在了，已回到聊天"
			a.st.noticeOK = false
		}
	}
}

// sameStrings 比较两组字符串是否一模一样（不看顺序：插件扫描的顺序会变，
// 但"谁带页面"这件事没变时不该重新去问一轮）。
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	n := make(map[string]int, len(a))
	for _, s := range a {
		n[s]++
	}
	for _, s := range b {
		n[s]--
		if n[s] < 0 {
			return false
		}
	}
	return true
}

// invalidatePage 丢掉某一页的所有缓存帧：它说自己变了。
//
// 连在途的也一起作废（pending 清掉），否则一份"变化之前"的帧回来会被当成新的存进去。
func (a *app) invalidatePage(svc, id string) {
	for k := range a.st.pageCache {
		if pageKeyMatches(k, svc, id) {
			delete(a.st.pageCache, k)
		}
	}
	for k := range a.st.pagePending {
		if pageKeyMatches(k, svc, id) {
			delete(a.st.pagePending, k)
		}
	}
}

// pageKeyAlive 判断一份缓存帧（的键）是不是还对应清单里活着的一页。
//
// 缓存键是 "<服务>/<页面 id>|<宽>|<高>|<滚动>"，所以取第一个竖线之前那段就够了。
func pageKeyAlive(key string, alive map[string]bool) bool {
	ref, _, _ := strings.Cut(key, "|")
	return alive[ref]
}

// pageKeyMatches 判断一份缓存帧是不是属于某一页。
//
// 插件报了服务名就精确比对；只报了页面 id 就按 id 兜底 ——
// 两个插件的页面重名时最多多刷一页，不会刷错东西。
func pageKeyMatches(key, svc, id string) bool {
	head, _, ok := strings.Cut(key, "|")
	if !ok {
		return false
	}
	if svc != "" {
		return head == svc+"/"+id
	}
	_, pid, ok := strings.Cut(head, "/")
	return ok && pid == id
}

// openPage 切到某个插件自带的一页。
func (a *app) openPage(svc, id string) {
	a.clearSelAll()
	a.st.drag = nil
	a.st.tab = tabPanel
	a.st.pageSvc, a.st.pageID = svc, id
	a.st.pageScroll = 0
	a.st.pageMax = 0
	a.st.pageTitle = ""
	a.st.pageNote = ""
	a.st.focus = ""
	if p, ok := pageOf(a.st.pages, svc+"/"+id); ok && p.title != "" {
		a.st.pageTitle = p.title
	}
	// 状态栏交给这一页自己：它给的那句提示（render 的 note）比"这一页是插件画的"
	// 有用得多，会随下一帧贴上来。
	a.st.status = ""
	a.st.statusOK = true
}
