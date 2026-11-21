package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// 插件自带页面（ui.page.*）在宿主这一侧的规矩
//
// 这一层要验的不是"某个页面长什么样"，而是**那几条约定有没有被守住**：
//   - 插件报了什么，标签栏上就有什么
//   - 插件送来的行被贴成恰好 w 列（歪了宿主自己兜住）
//   - 点击原样送回插件，动作名不解析
//   - 键盘给了插件，但 Tab 这条退路留在宿主手里（Ctrl+C 是复制，Ctrl+Q 是退出）
//   - 插件说"我变了"，宿主就得重新要一帧
// ---------------------------------------------------------------------------

const demoPageSvc = "ui.page.demo.v1"

// seedPage 让假后端报一个"自带界面"的插件。
func seedPage(fb *fakeBackend, lines ...string) {
	fb.pages = map[string][]plugin.PanelInfo{
		demoPageSvc: {{ID: "demo", Title: "演示", Order: 1}},
	}
	fb.pageLines = map[string][]string{demoPageSvc + "/demo": lines}
	fb.plugins = append(fb.plugins, pluginInfo{
		ID: "com.official.demo", Provides: []string{demoPageSvc}, Alive: []string{demoPageSvc},
	})
}

// openDemoPage 走到那一页上，并把第一帧要回来。
//
// 渲染是两拍的：这一帧登记需求、返回占位，主循环把需求派出去，帧回来再重绘。
// 用例必须照着真实节奏走，不能直接往缓存里塞东西。
func openDemoPage(t *testing.T, a *app, fb *fakeBackend) {
	t.Helper()
	a.st.plugins = fb.plugins
	a.loadPages()
	drain(t, a) // 页面清单
	a.openPage(demoPageSvc, "demo")

	layout(a, 110, 30) // 登记需求，这一帧是占位
	a.pumpPages()
	drain(t, a) // 帧回来了
	layout(a, 110, 30)
}

// frameAt 取"这个尺寸下、插件那一帧已经回来"的整帧。
//
// 只在缓存未命中时才去要帧：命中还硬等一条结果，等的是永远不会来的东西。
func frameAt(t *testing.T, a *app, w, h int) string {
	t.Helper()
	layout(a, w, h)
	if len(a.st.pageWanted) == 0 {
		return a.canv.frame()
	}
	a.pumpPages()
	drain(t, a)
	layout(a, w, h)
	return a.canv.frame()
}

// 插件被删掉，它那一页必须跟着消失 —— 连着帧缓存与在途请求一起，不留残留。
//
// 这条曾经是坏的，而且是两处一起坏：
//  1. 页面清单只在"为空"时才补问（那是为了接住启动竞态），插件一删清单就不空了，
//     于是再也没人去问第二次；
//  2. 就算重新问了，手上那份属于它的帧还留着，下一帧照旧画出来。
//
// 合起来的现象就是：插件删了，标签还在，点进去还是它那一页。
func TestDeletedPluginLosesItsPage(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "插件画的第一行")
	a := newTestApp(fb)

	// 第一轮扫描：认出这一页
	a.applyOutcome(outcome{kind: "plugins", plugins: fb.plugins})
	drain(t, a) // 页面清单
	if len(a.st.pages) != 1 {
		t.Fatalf("应认出 1 页，实际 %+v", a.st.pages)
	}

	// 站到那一页上，并把帧拿回来
	a.openPage(demoPageSvc, "demo")
	layout(a, 110, 30)
	a.pumpPages()
	drain(t, a)
	layout(a, 110, 30)
	if len(a.st.pageCache) == 0 {
		t.Fatal("这一帧应该已经回来了")
	}
	// 再塞一份"在途"的：插件被杀时正有一个请求飞在路上，回来不该被收下
	a.st.pagePending[pageCacheKey(demoPageSvc+"/demo", 110, 30, 0)] = true

	// 插件被删掉了：下一次扫描里没有它
	fb.plugins = nil
	a.applyOutcome(outcome{kind: "plugins", plugins: nil})
	drain(t, a) // 这一轮的页面清单

	if len(a.st.pages) != 0 {
		t.Fatalf("插件删了，它那一页也该没：%+v", a.st.pages)
	}
	for k := range a.st.pageCache {
		t.Fatalf("帧缓存里不该还留着它的帧：%s", k)
	}
	for k := range a.st.pagePending {
		t.Fatalf("在途请求也要作废，否则回来的帧又会被当成新的存下：%s", k)
	}
	if a.st.tab != tabChat {
		t.Fatalf("正看着的那一页没了就该退回聊天，实际 %v", a.st.tab)
	}
	if !strings.Contains(a.st.notice, "不在了") {
		t.Fatalf("退回聊天要说明为什么，实际 %q", a.st.notice)
	}
	layout(a, 110, 30)
	if strings.Contains(a.canv.frame(), "演示") {
		t.Fatalf("标签栏上不该还有它：\n%s", a.canv.frame())
	}
}

// 帧还没回来时，屏幕上不能留着上一帧的可点区域。
//
// 换了滚动位置就是要重新要一帧，而那一帧要跨一次进程；这中间的占位画面上
// 若还挂着上一帧的热区，点下去就是把动作发给"已经翻过去的那一屏"的内容。
func TestStalePageHitsAreDroppedWhileFrameIsMissing(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "第一行", "第二行")
	a := newTestApp(fb)
	openDemoPage(t, a, fb)

	if len(a.st.pageHits) == 0 {
		t.Fatal("帧回来之后应当登记了热区")
	}
	a.st.pageScroll = 2 // 换个滚动位置：这份缓存必然未命中
	layout(a, 110, 30)
	if len(a.st.pageHits) != 0 {
		t.Fatalf("占位画面上不该还留着上一帧的热区：%+v", a.st.pageHits)
	}
}

// "谁带页面"这组服务没变时不该反复去问 —— 每次扫描都问一轮是白花花的 RPC。
func TestPageListNotRerequestedWhenUnchanged(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "第一行")
	a := newTestApp(fb)

	a.applyOutcome(outcome{kind: "plugins", plugins: fb.plugins})
	drain(t, a) // 第一轮：服务从无到有，问一次

	a.applyOutcome(outcome{kind: "plugins", plugins: fb.plugins})
	select {
	case out := <-a.results:
		t.Fatalf("清单没变就不该再问一轮，却收到 %s", out.kind)
	default:
	}
}

// 界面必须订阅 plugin.deleted：不订阅的话，删除这个动作根本传不到界面耳朵里，
// 上面那套"删了就收拾干净"的逻辑一次也不会跑。
func TestDeletedPluginEventIsWatchedAndTriggersRescan(t *testing.T) {
	watched := false
	for _, name := range watchedEvents() {
		if name == "plugin.deleted" {
			watched = true
		}
	}
	if !watched {
		t.Fatalf("没订阅 plugin.deleted，删插件之后界面不会重扫：%v", watchedEvents())
	}

	fb := &fakeBackend{}
	seedPage(fb, "第一行")
	a := newTestApp(fb)
	w := &Aurora{}
	w.app = a

	w.onEvent("plugin.deleted", json.RawMessage(`{"pluginId":"com.official.demo","dir":"demo.plugin"}`))
	drainApply(t, a) // 事件日志那一条
	drainApply(t, a) // 安排重扫那一条（它会起一个去抖计时器）

	// 去抖（300ms）之后，重扫任务才会被投回界面线程；把它取出来执行
	fired := false
	waitFor(t, "删除事件之后安排重扫", func() bool {
		select {
		case fn := <-a.apply:
			fn()
			fired = true
			return true
		default:
			return false
		}
	})
	if !fired {
		t.Fatal("应安排一次重扫")
	}
}

// 插件报了一页，标签栏上就多一个标签，点了就能进去。
func TestPluginPageBecomesATabAndOpens(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "插件画的第一行")
	a := newTestApp(fb)
	a.st.plugins = fb.plugins
	a.loadPages()
	drain(t, a)

	if len(a.st.pages) != 1 || a.st.pages[0].title != "演示" {
		t.Fatalf("应读到 1 页「演示」，实际 %+v", a.st.pages)
	}
	layout(a, 110, 30)
	if !strings.Contains(a.canv.frame(), "演示") {
		t.Fatalf("标签栏上应出现这一页：\n%s", a.canv.frame())
	}

	x, y := findBox(t, a, "tab:page:"+demoPageSvc+"/demo")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})
	if a.st.tab != tabPanel || a.st.pageID != "demo" {
		t.Fatalf("点标签应切到插件页，实际 tab=%v pageID=%q", a.st.tab, a.st.pageID)
	}
	// Tab 也要能走到插件页上（内置页 + 插件页是同一串；现在内置页只剩聊天）
	a.st.tab = tabChat
	a.switchTab(1)
	if a.st.tab != tabPanel {
		t.Fatalf("从聊天往后切应到插件页，实际 %v", a.st.tab)
	}
}

// 插件送来的行必须被贴成**恰好 w 列**：多了少了都会把宿主的边框顶歪。
//
// 这条用例故意让插件画歪（宽度离谱、还带样式码），宿主必须自己兜住 ——
// 界面的版面不能由插件的算术正确与否决定。
func TestPluginPageBadLinesArePatched(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb)
	fb.pageRawLines = []string{"\033[31m" + strings.Repeat("很宽的内容", 30)}
	a := newTestApp(fb)
	openDemoPage(t, a, fb)

	for _, sz := range [][2]int{{60, 18}, {110, 30}} {
		frame := frameAt(t, a, sz[0], sz[1])
		lines := strings.Split(frame, "\n")
		if len(lines) != sz[1] {
			t.Fatalf("%dx%d：行数 %d，应为 %d", sz[0], sz[1], len(lines), sz[1])
		}
		for i, ln := range lines {
			if got := dispWidth(ln); got != sz[0] {
				t.Fatalf("%dx%d：第 %d 行宽 %d，应为 %d\n%q", sz[0], sz[1], i, got, sz[0], ln)
			}
			// 无色渲染下不该出现转义序列：插件的样式码要由宿主剥掉
			if strings.ContainsRune(ln, 0x1b) {
				t.Fatalf("%dx%d：第 %d 行漏出了样式码\n%q", sz[0], sz[1], i, ln)
			}
		}
	}
}

// 点插件画的东西 = 把动作原样送回插件。
func TestPluginPageClickSendsActionUnchanged(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	a := newTestApp(fb)
	openDemoPage(t, a, fb)

	x, y := findBox(t, a, "panel:0")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})
	drain(t, a) // 动作结果

	fb.mu.Lock()
	got := append([]string{}, fb.pageActions...)
	fb.mu.Unlock()
	want := demoPageSvc + "|demo|hello|world"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("动作名与参数应原样送回插件，实际 %v（期望 %q）", got, want)
	}
}

// @ui: 开头的是留给宿主的口子：插件想跟界面说句话时用，界面不必认识插件。
func TestPluginPageHostActionChat(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	fb.pageChatHit = true
	a := newTestApp(fb)
	openDemoPage(t, a, fb)

	x, y := findBox(t, a, "panel:1")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	if a.st.tab != tabChat {
		t.Fatalf("@ui:chat 应把用户带到聊天页，实际 %v", a.st.tab)
	}
	if got := string(a.st.input); got != "插件塞进来的话" {
		t.Fatalf("那段文字应进输入框，实际 %q", got)
	}
	fb.mu.Lock()
	n := len(fb.pageActions)
	fb.mu.Unlock()
	if n != 0 {
		t.Fatal("宿主自己的动作不该再转给插件")
	}
}

// 键盘：插件要就给，但 Tab 这条退路永远留在宿主手里（Ctrl+C 是复制）。
func TestPluginPageKeysGoToPluginButHostKeepsExits(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	fb.pageWantKeys = true
	a := newTestApp(fb)
	openDemoPage(t, a, fb)
	a.startPageKeys() // 真实启动时由 Start 起，测试里手动

	if !a.st.pageWantKeys {
		t.Fatalf("这一页声明了要键盘，宿主应当记下来（tab=%v pageID=%q pages=%d）",
			a.st.tab, a.st.pageID, len(a.st.pages))
	}
	a.handle(event{kind: evKey, key: keyRune, r: '你'})
	waitFor(t, "按键送到插件", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.pageKeys) == 1
	})
	fb.mu.Lock()
	got := fb.pageKeys[0]
	fb.mu.Unlock()
	if got != demoPageSvc+"|demo|rune|你" {
		t.Fatalf("按键应原样转给插件，实际 %q", got)
	}

	// Tab 是宿主切页；这一页没声明 WantCopy，Ctrl+C 也不转给插件（留给宿主复制）。
	fb.mu.Lock()
	n := len(fb.pageKeys)
	fb.mu.Unlock()
	a.handle(event{kind: evKey, key: keyTab})
	a.handle(event{kind: evKey, key: keyCtrlC})
	if a.st.tab == tabPanel {
		t.Fatal("Tab 应该切走，而不是被插件吃掉")
	}
	fb.mu.Lock()
	after := len(fb.pageKeys)
	fb.mu.Unlock()
	if after != n {
		t.Fatalf("Tab / Ctrl+C 不该转给插件，实际多出 %d 条", after-n)
	}
}

// 插件没要键盘，就一个字符都不该转过去。
func TestPluginPageWithoutWantKeysIgnoresKeys(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	fb.pageWantKeys = false
	a := newTestApp(fb)
	openDemoPage(t, a, fb)
	a.startPageKeys()

	a.handle(event{kind: evKey, key: keyRune, r: 'x'})
	fb.mu.Lock()
	n := len(fb.pageKeys)
	fb.mu.Unlock()
	if n != 0 {
		t.Fatalf("没要键盘的页面不该收到按键，实际 %d 条", n)
	}
}

// 插件说"我变了"，宿主就得把那一页的帧作废、重新要一份。
//
// 少了这一步，页面会一直停在旧内容上 —— 那看起来就是"点了没反应"。
func TestPluginPageChangedEventForcesNewFrame(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "第一版")
	a := newTestApp(fb)
	openDemoPage(t, a, fb)

	fb.mu.Lock()
	before := len(fb.pageRenders)
	fb.mu.Unlock()

	w := &Aurora{app: a}
	w.onEvent(pageChangedEvent, []byte(`{"service":"`+demoPageSvc+`","panel":"demo"}`))
	for len(a.apply) > 0 {
		drainApply(t, a)
	}
	layout(a, 110, 30)
	a.pumpPages()
	drain(t, a)

	fb.mu.Lock()
	after := len(fb.pageRenders)
	fb.mu.Unlock()
	if after != before+1 {
		t.Fatalf("作废后应重新要一帧，实际 %d → %d", before, after)
	}
}

// 画不出来就如实说，而且**只试一次**：每帧都去问一次等于把插件按在地上打。
func TestPluginPageRenderErrorIsCachedNotRetried(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	fb.pageErr = errors.New("插件炸了")
	a := newTestApp(fb)
	openDemoPage(t, a, fb)

	if !strings.Contains(a.canv.frame(), "没能画出来") {
		t.Fatalf("渲染失败应如实显示：\n%s", a.canv.frame())
	}

	fb.mu.Lock()
	before := len(fb.pageRenders)
	fb.mu.Unlock()
	layout(a, 110, 30)
	a.pumpPages()
	fb.mu.Lock()
	after := len(fb.pageRenders)
	fb.mu.Unlock()
	if after != before {
		t.Fatalf("失败的那一帧也要缓存，实际又问了 %d 次", after-before)
	}
}

// 页面所属的插件没了：退回聊天，并说一句为什么 —— 留在一片空白上更让人发懵。
func TestPluginPageGoneFallsBackToChat(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	a := newTestApp(fb)
	openDemoPage(t, a, fb)

	fb.mu.Lock()
	fb.pages = nil
	fb.plugins = nil
	fb.mu.Unlock()
	a.st.plugins = nil
	a.loadPages()
	drain(t, a)

	if a.st.tab != tabChat {
		t.Fatalf("插件不在了应退回聊天，实际 %v", a.st.tab)
	}
	if !strings.Contains(a.st.notice, "不在了") {
		t.Fatalf("应说明原因，实际 %q", a.st.notice)
	}
}

// 滚动位置以插件夹过的为准：宿主按越界的位置去要帧，只会白跑。
func TestPluginPageScrollFollowsPluginClamp(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	fb.pageMax = 4
	a := newTestApp(fb)
	openDemoPage(t, a, fb)

	if a.st.pageMax != 4 {
		t.Fatalf("应记下插件报的滚动上限，实际 %d", a.st.pageMax)
	}
	a.scrollBy(10)
	if a.st.pageScroll != 4 {
		t.Fatalf("滚轮应被夹在插件给的上限内，实际 %d", a.st.pageScroll)
	}
}

// 渲染是同步跑的：一次都不能在渲染里等插件（那会把每一帧都拖住）。
//
// 这条用"渲染前后请求数不变"来钉：渲染只登记需求，真正发出去的是主循环。
func TestPluginPageRenderNeverBlocksOnPlugin(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	a := newTestApp(fb)
	openDemoPage(t, a, fb)

	fb.mu.Lock()
	before := len(fb.pageRenders)
	fb.mu.Unlock()

	// 换尺寸 → 缓存未命中 → 这一帧只能是占位，绝不能顺手去问插件
	layout(a, 90, 24)
	fb.mu.Lock()
	after := len(fb.pageRenders)
	fb.mu.Unlock()
	if after != before {
		t.Fatalf("渲染期间不该有插件调用，实际多出 %d 次", after-before)
	}
	if !strings.Contains(a.canv.frame(), "正在向插件要这一帧") {
		t.Fatalf("没缓存时应画占位：\n%s", a.canv.frame())
	}
}

// 页面声明了"要自己复制"（WantCopy），Ctrl+C 才转给它复制，而不是退出。
func TestPluginPageWantCopyForwardsCtrlC(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	fb.pageWantKeys = true
	fb.pageWantCopy = true
	a := newTestApp(fb)
	openDemoPage(t, a, fb)
	a.startPageKeys()

	if quit := a.handle(event{kind: evKey, key: keyCtrlC}); quit {
		t.Fatal("WantCopy 时 Ctrl+C 不该让宿主退出")
	}
	waitFor(t, "Ctrl+C 转给插件", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.pageKeys) == 1
	})
	fb.mu.Lock()
	got := fb.pageKeys[0]
	fb.mu.Unlock()
	if got != demoPageSvc+"|demo|ctrl+c|" {
		t.Fatalf("Ctrl+C 应转成 ctrl+c 给插件，实际 %q", got)
	}
}

// 页面没声明要复制，Ctrl+C 也不退出、不被插件吃掉；退出是 Ctrl+Q。
func TestPluginPageCtrlCWithoutWantCopy(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	fb.pageWantKeys = true
	fb.pageWantCopy = false
	a := newTestApp(fb)
	openDemoPage(t, a, fb)
	a.startPageKeys()

	if quit := a.handle(event{kind: evKey, key: keyCtrlC}); quit {
		t.Fatal("没声明 WantCopy 时 Ctrl+C 也不该退出（它是复制键）")
	}
	fb.mu.Lock()
	n := len(fb.pageKeys)
	fb.mu.Unlock()
	if n != 0 {
		t.Fatalf("Ctrl+C 不该转给插件，实际 %d 条", n)
	}
	if quit := a.handle(event{kind: evKey, key: keyCtrlQ}); !quit {
		t.Fatal("Ctrl+Q 应退出")
	}
}

// 在插件的输入字段上按下并拖动，要一路转成 sel 动作发给插件。
func TestPluginPageDragForwardsSel(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	a := newTestApp(fb)

	// 手工构造：一个 caret 热区在 (8,0)、宽 6，字段名 path
	a.st.pageHits = []pageHitRef{{
		svc: demoPageSvc, id: "demo",
		hit: plugin.Hit{Y: 0, X: 8, W: 6, Action: "caret", Arg: "path:0"},
	}}
	a.canv = &canvas{w: 100, h: 30, color: false}
	a.canv.boxes = append(a.canv.boxes, hitbox{x: 8, y: 0, w: 6, action: "panel:0"})

	// 按下落在 (9,0)：记下"正在 path 字段上拖"
	a.pressStart(9, 0)
	if a.st.drag == nil || a.st.drag.target != "panel" || a.st.drag.field != "path" {
		t.Fatalf("按下应记下 panel 拖选，实际 %+v", a.st.drag)
	}

	// 拖到 (12,0)：同一字段，转发 sel
	a.dragSelect(12, 0)
	waitFor(t, "sel 转给插件", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		for _, s := range fb.pageActions {
			if s == demoPageSvc+"|demo|sel|path:0" {
				return true
			}
		}
		return false
	})

	// 松手：拖选状态清掉
	a.release(12, 0)
	if a.st.drag != nil {
		t.Fatal("松手后应清掉拖选状态")
	}
}

// 插件回 exit（防御性口子：官方插件已不再回，退出走宿主 Ctrl+Q），宿主仍认。
func TestPluginPageKeyExitStopsApp(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "有一行")
	fb.pageWantKeys = true
	fb.pageWantCopy = true
	fb.pageKeyRes = pageKeyResult{exit: true}
	a := newTestApp(fb)
	openDemoPage(t, a, fb)
	a.startPageKeys()

	a.handle(event{kind: evKey, key: keyCtrlC})
	waitFor(t, "exit 触发退出", func() bool {
		select {
		case <-a.done:
			return true
		default:
			return false
		}
	})
}

// 删除是"重启生效"的：正在运行的插件目录此刻还在（删不掉、只标了待重启删除），
// 但它的页面得**当场消失**，不能等目录真没了才消失 —— 否则用户删完看到标签还在，
// 会以为没删掉。这是用户报过的真实 bug。
func TestDeletedPluginPageGoneEvenWhileDirRemains(t *testing.T) {
	fb := &fakeBackend{}
	seedPage(fb, "角色画的一行")
	a := newTestApp(fb)

	a.applyOutcome(outcome{kind: "plugins", plugins: fb.plugins})
	drain(t, a) // 页面清单
	if len(a.st.pages) != 1 {
		t.Fatalf("应认出 1 页，实际 %+v", a.st.pages)
	}

	// 删除事件：目录还在（deferred），但插件被删了
	w := &Aurora{app: a}
	w.onEvent("plugin.deleted", json.RawMessage(`{"pluginId":"com.official.demo","dir":"demo.plugin"}`))
	drainApply(t, a) // 事件日志
	drainApply(t, a) // dropPlugin

	if len(a.st.pages) != 0 {
		t.Fatalf("删了插件，它那一页应立即没：%+v", a.st.pages)
	}
	if !a.st.droppedPlugins["com.official.demo"] || !a.st.droppedPlugins["demo.plugin"] {
		t.Fatalf("应记住被删插件：%v", a.st.droppedPlugins)
	}

	// 重扫：目录还在（扫描结果里仍有它），页面也不能回来
	a.applyOutcome(outcome{kind: "plugins", plugins: fb.plugins})
	drain(t, a)
	if len(a.st.pages) != 0 {
		t.Fatalf("重扫后页面也不该回来：%+v", a.st.pages)
	}
}
