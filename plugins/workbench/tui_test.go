package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// 假 backend：界面逻辑不依赖核心，可以完整地在单测里驱动
// ---------------------------------------------------------------------------

type fakeBackend struct {
	mu sync.Mutex

	services []serviceStatus
	plugins  []pluginInfo

	chats []string

	toggles []string // 记录 "id=true/false"
	deletes []string
	// 目录名也要记下来：同一个 ID 可能在目录里躺了两份，
	// 启停 / 删除必须落在用户选中的那一份上
	toggleDirs []string
	deleteDirs []string

	browsed    []string // 每次 browse 传下去的路径
	fakeBrowse wsListing

	// 插件自带页面
	pages        map[string][]plugin.PanelInfo // 服务名 -> 它报的页面
	pageLines    map[string][]string           // "服务/页面id" -> 要画的行
	pageRawLines []string                      // 非 nil 时原样送出（模拟插件画得不对）
	pageNote     string                        // 渲染与动作共用的那句话
	pageWantKeys bool
	pageChatHit  bool // 额外登记一个 @ui:chat 热区
	pageMax      int
	pageErr      error
	pageRenders  []string // "服务|宽|高|滚动"
	pageActions  []string // "服务|页面|动作|参数"
	pageKeys     []string // "服务|页面|按键|文本"
	pageKeyRes   pageKeyResult
	pageWantCopy bool

	// 链式对话（conversation.v1）。默认关闭：不装插件的降级链路也要一直测到，
	// 需要走消息树的用例显式打开 convOn。
	convOn    bool
	convSess  []*fakeSess
	convCurID string
	convSeq   int

	convSends   []string
	convEdits   []string
	convRegens  []string
	convPages   []string
	convDels    []string
	convNews    int
	convExports int

	// 附件：readAttachment 收到的路径、以及最后一条消息带上的附件
	attReads    []string
	attSent     []attachment
	attSends    []int // 每次 convSend 带的附件个数
	fakeAtt     attachment
	fakeAttErrs map[string]error

	// 图片：读缩略图与"交给系统打开"
	imgReads   []string
	opened     []string
	fakeImg    []byte
	fakeImgErr error
	openErr    error

	// 浮窗（ui.widget.*）与重启
	widgetInfos   []plugin.WidgetInfo
	widgetOut     plugin.WidgetRender
	widgetPosX    int
	widgetPosY    int
	widgetPosFold bool
	restarts      int // 点了几次「重启」
}

// errTest 是测试里通用的"读不了"错误。
var errTest = fmt.Errorf("读不了")

// readImageBytes 模拟核心读图：测试里塞进 fakeImg 的字节。
func (f *fakeBackend) readImageBytes(path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.imgReads = append(f.imgReads, path)
	if f.fakeImgErr != nil {
		return nil, f.fakeImgErr
	}
	if f.fakeImg == nil {
		return nil, fmt.Errorf("没有测试图")
	}
	return f.fakeImg, nil
}

// openPath 记录"交给系统打开"的路径。
func (f *fakeBackend) openPath(path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, path)
	if f.openErr != nil {
		return "", f.openErr
	}
	return "已交给系统打开：" + path, nil
}

// widgetList / widgetRender 是浮窗（ui.widget.*）的假实现。
func (f *fakeBackend) widgetList(svc string) ([]plugin.WidgetInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.widgetInfos, nil
}

func (f *fakeBackend) widgetRender(svc, id string) (plugin.WidgetRender, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.widgetOut, nil
}

func (f *fakeBackend) widgetPos(svc, id string) (int, int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.widgetPosX, f.widgetPosY, f.widgetPosFold
}

// restart 单测里不真重启核心：只记下"按了这一下"。
func (f *fakeBackend) restart() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts++
	return "正在重启核心（测试）", nil
}

// readAttachment 模拟核心读文件：默认造一个文本附件。
func (f *fakeBackend) readAttachment(path string) (attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attReads = append(f.attReads, path)
	if f.fakeAttErrs != nil {
		if err, ok := f.fakeAttErrs[path]; ok {
			return attachment{}, err
		}
	}
	att := f.fakeAtt
	if att.Name == "" {
		name := path
		if i := strings.LastIndexAny(path, `/\`); i >= 0 {
			name = path[i+1:]
		}
		att = attachment{Name: name, Path: path, Type: "text", Text: "内容", Size: 6}
	}
	if att.Path == "" {
		att.Path = path
	}
	return att, nil
}

// 角色页那一整套假实现搬去了 roleplay 插件自己的用例里（plugins/roleplay/panel_test.go）——
// 谁实现谁测试：界面这边连"角色卡"是什么都不该知道。

func (f *fakeBackend) probeServices() []serviceStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.services
}

func (f *fakeBackend) scanPlugins() ([]pluginInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plugins, nil
}

func (f *fakeBackend) wsBrowse(path string) (wsListing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.browsed = append(f.browsed, path)
	ls := f.fakeBrowse
	if ls.Path == "" {
		ls.Path = firstNonEmpty(path, "/browse-default")
	}
	ls.Note = "已读取 " + ls.Path
	return ls, nil
}

// ---------------------------------------------------------------------------
// 插件自带页面（ui.page.*）的假实现
//
// 刻意照着契约来：渲染结果一定是一块**恰好 w×h** 的画面，热区坐标是面板内的，
// 动作名与参数原样收下。宿主与插件之间的约定就只有这几条。
// ---------------------------------------------------------------------------

// pageList 刻意不受 pageErr 影响：pageErr 模拟的是"渲染/动作出错"，
// 而清单读得到恰恰说明插件本身活着 —— 两件事分开，用例才好各自验证。
func (f *fakeBackend) pageList(services []string) ([]pagePanel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []pagePanel
	for _, svc := range services {
		for _, p := range f.pages[svc] {
			out = append(out, pagePanel{svc: svc, id: p.ID, title: p.Title, order: p.Order})
		}
	}
	return out, nil
}

func (f *fakeBackend) pageRender(svc, id string, w, h, scroll int) (plugin.PanelRender, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageRenders = append(f.pageRenders, fmt.Sprintf("%s|%d|%d|%d", svc, w, h, scroll))
	if f.pageErr != nil {
		return plugin.PanelRender{}, f.pageErr
	}
	if w < 1 || h < 1 {
		return plugin.PanelRender{}, nil
	}
	if f.pageRawLines != nil {
		// 原样送出：连宽度都不修 —— 用来验证"插件画歪了，宿主自己兜住"
		return plugin.PanelRender{
			Lines:     f.pageRawLines,
			Hits:      []plugin.Hit{{Y: 0, X: 0, W: 6, Action: "hello", Arg: "world"}},
			Scroll:    scroll,
			MaxScroll: f.pageMax,
			Note:      f.pageNote,
			WantKeys:  f.pageWantKeys,
		}, nil
	}
	b := plugin.NewBlock(w, h)
	for i, ln := range f.pageLines[svc+"/"+id] {
		b.At(i).Text("", ln)
	}
	hw := 6
	if hw > w {
		hw = w
	}
	b.Hit(0, 0, hw, "hello", "world")
	if f.pageChatHit && h > 1 {
		b.Hit(1, 0, 8, "@ui:chat", "插件塞进来的话")
	}
	out := b.Render(scroll, f.pageMax, f.pageNote)
	out.WantKeys = f.pageWantKeys
	out.WantCopy = f.pageWantCopy
	return out, nil
}

func (f *fakeBackend) pageAction(svc, id, action, arg string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageActions = append(f.pageActions, svc+"|"+id+"|"+action+"|"+arg)
	if f.pageErr != nil {
		return "", f.pageErr
	}
	return f.pageNote, nil
}

func (f *fakeBackend) pageKey(svc, id, kind, text string) (pageKeyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageKeys = append(f.pageKeys, svc+"|"+id+"|"+kind+"|"+text)
	return f.pageKeyRes, f.pageErr
}

// loadAgentBudget / saveAgentBudget / loadLLM / saveLLM / testLLM / listModels /
// discoverLocal 的假实现随「设置」页一起搬走了：那些能力现在是 agent / llm
// 插件自己的事，界面不再转发，测试里也不再需要模拟它们。

func (f *fakeBackend) chat(text string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chats = append(f.chats, text)
	return `{"content":"收到："` + `"` + text + `"}`, "session.v1", nil
}

// ---------------------------------------------------------------------------
// 假 conversation.v1：消息树的最小可用版
//
// 只保留界面真正依赖的语义：一条消息有多个版本（alts），
// 改 / 重新生成都是在后面追加一个版本并把当前版本切过去，下游随之隐没。
// ---------------------------------------------------------------------------

// fakeNode 是一条消息：alts 是它的全部版本，cur 是当前翻到第几个。
type fakeNode struct {
	id   string
	role string
	at   string
	via  string
	alts []string
	cur  int
}

type fakeSess struct {
	id   string
	name string
	seq  int
	msgs []fakeNode
}

func (s *fakeSess) indexOf(id string) int {
	for i := range s.msgs {
		if s.msgs[i].id == id {
			return i
		}
	}
	return -1
}

func (f *fakeBackend) convNodeLocked(s *fakeSess, role, text, via string) fakeNode {
	s.seq++
	return fakeNode{
		id:   "n" + strconv.Itoa(s.seq),
		role: role,
		at:   "2026-09-18T10:00:00+08:00",
		via:  via,
		alts: []string{text},
	}
}

// convSessionLocked 找会话，找不到返回 nil。
func (f *fakeBackend) convSessionLocked(id string) *fakeSess {
	for _, s := range f.convSess {
		if s.id == id {
			return s
		}
	}
	return nil
}

// convPickLocked 取本次调用要操作的会话：指定优先，没指定就取当前，
// 一条都没有就现造一条 —— 与真插件的 Resolve 同义。
func (f *fakeBackend) convPickLocked(id string) *fakeSess {
	if s := f.convSessionLocked(id); s != nil {
		f.convCurID = s.id
		return s
	}
	if f.convCurID != "" {
		if s := f.convSessionLocked(f.convCurID); s != nil {
			return s
		}
	}
	f.convSeq++
	s := &fakeSess{id: "s" + strconv.Itoa(f.convSeq), name: "会话 " + strconv.Itoa(f.convSeq)}
	f.convSess = append(f.convSess, s)
	f.convCurID = s.id
	return s
}

func (f *fakeBackend) convSnapLocked(note string) convSnapshot {
	s := f.convPickLocked(f.convCurID)
	snap := convSnapshot{sessionID: s.id, name: s.name, note: note}
	for i := range s.msgs {
		n := s.msgs[i]
		snap.msgs = append(snap.msgs, msgView{
			id:      n.id,
			role:    n.role,
			at:      n.at,
			text:    n.alts[n.cur],
			version: n.cur + 1,
			total:   len(n.alts),
			via:     n.via,
		})
	}
	for _, o := range f.convSess {
		snap.sessions = append(snap.sessions, sessionInfo{
			id: o.id, name: o.name, nodes: len(o.msgs), updated: "2026-09-18T10:00:00+08:00",
		})
	}
	return snap
}

func (f *fakeBackend) convSupported() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.convOn
}

func (f *fakeBackend) convList() (convSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.convSnapLocked(""), nil
}

func (f *fakeBackend) convNew() (convSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convNews++
	f.convSeq++
	s := &fakeSess{id: "s" + strconv.Itoa(f.convSeq), name: "会话 " + strconv.Itoa(f.convSeq)}
	f.convSess = append(f.convSess, s)
	f.convCurID = s.id
	return f.convSnapLocked("已新建 " + s.name), nil
}

func (f *fakeBackend) convDelete(sessionID string) (convSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convDels = append(f.convDels, sessionID)
	var kept []*fakeSess
	for _, s := range f.convSess {
		if s.id != sessionID {
			kept = append(kept, s)
		}
	}
	f.convSess = kept
	if f.convCurID == sessionID {
		f.convCurID = ""
	}
	return f.convSnapLocked("已删除会话 " + sessionID), nil
}

func (f *fakeBackend) convSwitch(sessionID string) (convSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.convSessionLocked(sessionID) == nil {
		return convSnapshot{}, fmt.Errorf("找不到会话 %s", sessionID)
	}
	f.convCurID = sessionID
	return f.convSnapLocked("已切到 " + sessionID), nil
}

func (f *fakeBackend) convSend(sessionID, text string, atts []attachment) (convSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.convPickLocked(sessionID)
	f.convSends = append(f.convSends, text)
	f.attSends = append(f.attSends, len(atts))
	f.attSent = append([]attachment{}, atts...)
	s.msgs = append(s.msgs,
		f.convNodeLocked(s, "user", text, ""),
		f.convNodeLocked(s, "assistant", "收到："+text, "conversation.v1"))
	return f.convSnapLocked(""), nil
}

func (f *fakeBackend) convEdit(sessionID, nodeID, text string) (convSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.convPickLocked(sessionID)
	f.convEdits = append(f.convEdits, nodeID+"="+text)
	i := s.indexOf(nodeID)
	if i < 0 {
		return convSnapshot{}, fmt.Errorf("找不到消息 %s", nodeID)
	}
	role := s.msgs[i].role
	s.msgs[i].alts = append(s.msgs[i].alts, text)
	s.msgs[i].cur = len(s.msgs[i].alts) - 1
	s.msgs = s.msgs[:i+1] // 下游翻页隐没
	if role == "user" {
		// 改用户说的话 = 重新问一遍，要补一条回复
		s.msgs = append(s.msgs, f.convNodeLocked(s, "assistant", "收到："+text, "conversation.v1"))
	}
	return f.convSnapLocked(""), nil
}

func (f *fakeBackend) convRegenerate(sessionID, nodeID string) (convSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.convPickLocked(sessionID)
	f.convRegens = append(f.convRegens, nodeID)
	i := s.indexOf(nodeID)
	if i < 0 {
		return convSnapshot{}, fmt.Errorf("找不到消息 %s", nodeID)
	}
	if s.msgs[i].role == "assistant" {
		// 点 AI 的消息：换个答案，是同一个位置的又一个版本
		s.msgs[i].alts = append(s.msgs[i].alts, "重答："+s.msgs[i].alts[s.msgs[i].cur])
		s.msgs[i].cur = len(s.msgs[i].alts) - 1
		s.msgs = s.msgs[:i+1]
		return f.convSnapLocked(""), nil
	}
	// 点用户的消息：重新问一遍，它下面那条回复多出一个版本
	if i+1 < len(s.msgs) && s.msgs[i+1].role == "assistant" {
		r := &s.msgs[i+1]
		r.alts = append(r.alts, "重答："+r.alts[r.cur])
		r.cur = len(r.alts) - 1
		s.msgs = s.msgs[:i+2]
		return f.convSnapLocked(""), nil
	}
	reply := f.convNodeLocked(s, "assistant", "重答："+s.msgs[i].alts[s.msgs[i].cur], "conversation.v1")
	s.msgs = append(s.msgs[:i+1], reply)
	return f.convSnapLocked(""), nil
}

func (f *fakeBackend) convPage(sessionID, nodeID string, delta int) (convSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.convPickLocked(sessionID)
	f.convPages = append(f.convPages, fmt.Sprintf("%s=%+d", nodeID, delta))
	i := s.indexOf(nodeID)
	if i < 0 {
		return convSnapshot{}, fmt.Errorf("找不到消息 %s", nodeID)
	}
	s.msgs[i].cur = clamp(s.msgs[i].cur+delta, 0, len(s.msgs[i].alts)-1)
	return f.convSnapLocked(""), nil
}

func (f *fakeBackend) convMarkdown(sessionID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.convPickLocked(sessionID)
	var b strings.Builder
	b.WriteString("# " + s.name + "\n")
	for i := range s.msgs {
		n := s.msgs[i]
		fmt.Fprintf(&b, "\n### %s (%d/%d)\n\n%s\n", n.role, n.cur+1, len(n.alts), n.alts[n.cur])
	}
	return b.String(), nil
}

func (f *fakeBackend) convExport(sessionID string) (string, error) {
	md, err := f.convMarkdown(sessionID)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	f.convExports++
	f.mu.Unlock()
	return fmt.Sprintf("已导出到 conversations/%s.md（%d 字）", sessionID, len([]rune(md))), nil
}

// convMsgOf 让用例断言界面上某条消息的当前状态（版本号、正文）。
func convMsgOf(t *testing.T, a *app, id string) msgView {
	t.Helper()
	m, ok := a.msgByID(id)
	if !ok {
		t.Fatalf("界面上没有消息 %s", id)
	}
	return m
}

func (f *fakeBackend) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.chats...)
}

// newTestApp 组装一个不接核心、不碰终端的界面实例。
func newTestApp(fb *fakeBackend) *app {
	a := &app{
		plug:    fb,
		apply:   make(chan func(), 8),
		results: make(chan outcome, 8),
		// 按键队列：真实启动由 newApp 建，测试这边也要有 ——
		// 少了它就是一条 nil channel，往它上面发等于静默丢弃。
		pageKeys: make(chan pageKeyMsg, 64),
		done:     make(chan struct{}),
	}
	a.st = &state{
		coreVersion: "1.0.0",
		apiVersion:  "1",
		pluginID:    "com.official.workbench",
		tab:         tabChat,
		focus:       "chat",
	}
	return a
}

// layout 只做渲染，不进事件循环。
func layout(a *app, w, h int) {
	a.st.w, a.st.h = w, h
	a.canv = renderFrame(a.st, w, h, false)
}

func sendNow(t *testing.T, a *app) {
	t.Helper()
	a.press("send")
}

// findBox 找到某个动作的热区中心点。
func findBox(t *testing.T, a *app, action string) (int, int) {
	t.Helper()
	for _, b := range a.canv.boxes {
		if b.action == action {
			return b.x + b.w/2, b.y
		}
	}
	t.Fatalf("界面上没有动作 %q 的热区", action)
	return 0, 0
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func typeString(a *app, s string) {
	for _, r := range s {
		a.handle(event{kind: evKey, key: keyRune, r: r})
	}
}

func seed(fb *fakeBackend) {
	fb.services = []serviceStatus{
		{name: "session.v1", alive: true, provider: "com.official.session"},
		{name: "sandbox.v1", note: "未安装"},
	}
	fb.plugins = []pluginInfo{
		{ID: "com.official.llm", Provides: []string{"llm.v1"}, Alive: []string{"llm.v1"}},
		{ID: "com.official.session", Provides: []string{"session.v1"}, Alive: []string{"session.v1"}},
		{ID: "com.official.fs-tool", Provides: []string{"readFile.v1"}},
	}
	// 文件页也要有内容，否则排版不变量测不到真实情况
	fb.fakeBrowse = wsListing{
		Path:   "E:\\my-project",
		Parent: "E:\\",
		Root:   "E:\\my-project",
		Entries: []dirEntry{
			{Name: "src", Dir: true},
			{Name: "一个很长很长的中文目录名", Dir: true},
			{Name: "README.md", Size: 2048},
			{Name: "logo-with-a-very-long-name.png", Size: 1520000},
		},
	}
}

// 界面和插件是同一个目录按字母序逐个激活的，界面自己也在队列里。
//
// 排在我后面的插件（workspace.plugin 的 works 排在 workbench 的 workb 之后）
// 激活时，我早就扫完目录了 —— 不补这一次，它会被永久显示成「未运行」，
// 非要用户手点一次「重新扫描」才对得上。
func TestPluginStateEventTriggersQuietRescan(t *testing.T) {
	fb := &fakeBackend{}
	// 启动那一刻的快照：workspace 还没注册
	fb.services = []serviceStatus{{name: "workspace.v1", note: "未安装"}}
	fb.plugins = []pluginInfo{{
		ID: "com.official.workspace", Provides: []string{"workspace.v1"},
	}}

	a := newTestApp(fb)
	a.applyOutcome(outcome{kind: "services", services: fb.services})
	a.applyOutcome(outcome{kind: "plugins", plugins: fb.plugins, quiet: true})
	if a.st.plugins[0].running() {
		t.Fatal("前置条件：启动快照里 workspace 应该是未运行的")
	}
	a.st.status = "用户正在看的东西"
	a.st.statusOK = true

	// 插件这时才激活，数据变了
	fb.mu.Lock()
	fb.services = []serviceStatus{{name: "workspace.v1", alive: true, provider: "com.official.workspace"}}
	fb.plugins = []pluginInfo{{
		ID: "com.official.workspace", Provides: []string{"workspace.v1"}, Alive: []string{"workspace.v1"},
	}}
	fb.mu.Unlock()

	// 走真实接线：核心发来 plugin.state，而不是直接调 app 的内部方法
	w := &Workbench{app: a}
	w.onEvent("plugin.state", []byte(`{"pluginId":"com.official.workspace","state":"active"}`))

	// onEvent 会立刻投两件事：侧栏记一行事件、推进防抖序号。
	// 取净它们（不写死条数，免得以后多投一条就把用例搞挂）。
	for len(a.apply) > 0 {
		drainApply(t, a)
	}
	// 防抖窗口过去后，计时器才把真正的那次刷新投回来
	waitFor(t, "防抖窗口结束后投回刷新", func() bool { return len(a.apply) > 0 })
	drainApply(t, a)

	// 把补扫投回来的结果都收掉，收到"新的那一份真的盖上来"为止
	// （插件清单、服务链路，可能还有插件页面清单 —— 条数不写死）。
	//
	// 判据必须是新数据本身：光看"两个切片非空"是不行的，它们在补扫之前
	// 就已经非空了，那样这条用例会立刻通过、什么也没验到。
	waitFor(t, "补扫结果回到界面", func() bool {
		for len(a.results) > 0 {
			drain(t, a)
		}
		return len(a.st.plugins) == 1 && a.st.plugins[0].running() &&
			len(a.st.services) == 1 && a.st.services[0].alive
	})

	if !a.st.plugins[0].running() {
		t.Fatal("插件激活后应自动补扫，而不是等用户手点「重新扫描」")
	}
	if len(a.st.services) == 0 || !a.st.services[0].alive {
		t.Fatal("服务链路也要跟着刷新")
	}
	// 自动刷新是背景行为：不许盖掉用户正在看的提示
	if a.st.status != "用户正在看的东西" {
		t.Fatalf("自动刷新不该动状态栏，实际 %q", a.st.status)
	}
}

// 手动点「重新扫描」那一次要出声：用户按了按钮就得有反馈。
func TestManualRescanIsLoud(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.rescan(false)
	if a.st.status != "正在重新扫描…" {
		t.Fatalf("手动扫描应先给出「正在重新扫描…」，实际 %q", a.st.status)
	}
}

// ---------------------------------------------------------------------------
// 排版不变量：每行显示宽度精确等于终端宽度，行数精确等于高度
// ---------------------------------------------------------------------------

func TestFrameGeometry(t *testing.T) {
	sizes := [][2]int{{44, 12}, {60, 18}, {96, 30}, {120, 40}, {200, 50}}
	// 用 allTabs 而不是自己列一遍：新加的页面会自动被这条不变量覆盖
	tabs := allTabs

	for _, sz := range sizes {
		for _, tab := range tabs {
			fb := &fakeBackend{}
			seed(fb)
			a := newTestApp(fb)
			a.st.tab = tab
			// 塞点长内容，专挑截断与补空的边界
			a.st.msgs = []msgView{
				{id: "n1", role: "user", at: "2026-09-18T10:00:00+08:00",
					text: "读一下 " + strings.Repeat("很长的路径/", 20), version: 1, total: 1},
				{id: "n2", role: "assistant", at: "2026-09-18T10:00:01+08:00",
					text: strings.Repeat("中文与 emoji 🚀 混排 ", 30), version: 2, total: 3, via: "agent.v1"},
				{id: "n3", role: "错误", at: "2026-09-18T10:00:02+08:00",
					text: "[-32000] 服务未找到: very.long.service.name.v1", version: 1, total: 1,
					err: "服务未找到"},
			}
			a.st.convOK = true
			a.st.sessID = "s1"
			a.st.sessions = []sessionInfo{
				{id: "s1", name: "会话 1", nodes: 3, updated: "2026-09-18T10:00:02+08:00"},
				{id: "s2", name: "一个很长很长很长的会话名", nodes: 0},
			}
			a.st.input = []rune(strings.Repeat("输入很长的内容 ", 15))
			a.st.cursor = len(a.st.input)
			a.st.events = []string{"10:00:03 chat.message {\"text\":\"hi\"}"}
			a.st.services = fb.services
			a.st.plugins = fb.plugins

			layout(a, sz[0], sz[1])
			frame := a.canv.frame()

			// 无颜色渲染时不该出现任何转义序列：出现了就说明样式码被当成文本，
			// 既会被算进显示宽度，也会原样打到屏幕上。
			if strings.ContainsRune(frame, 0x1b) {
				t.Fatalf("%dx%d %s：无颜色渲染下出现了转义序列\n%q", sz[0], sz[1], tab.label(), frame)
			}

			lines := strings.Split(frame, "\n")
			if len(lines) != sz[1] {
				t.Fatalf("%dx%d %s：行数 %d，期望 %d", sz[0], sz[1], tab.label(), len(lines), sz[1])
			}
			for i, ln := range lines {
				if got := dispWidth(ln); got != sz[0] {
					t.Fatalf("%dx%d %s：第 %d 行宽度 %d，期望 %d\n%q",
						sz[0], sz[1], tab.label(), i, got, sz[0], ln)
				}
			}
		}
	}
}

func TestTinyWindowDegrades(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	layout(a, 30, 8)
	if !strings.Contains(a.canv.frame(), "窗口太小") {
		t.Fatal("窗口过小时应给出可读提示，而不是画出错位的框")
	}
}

// ---------------------------------------------------------------------------
// 点击
// ---------------------------------------------------------------------------

// 点标签切页的行为由 panel_test.go 的 TestPluginPageBecomesATabAndOpens 覆盖
// （内置页现在只剩聊天，没有第二个标签可点）。

// 点标签栏上的「重启」要真的去请核心重启。
//
// 插件的启停 / 删除都只做标记、标着"重启后生效"（规范 2.6），
// 这个按钮就是让那些标记落地的地方，所以它必须真的发出请求。
func TestClickRestartRequestsCoreRestart(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	layout(a, 100, 30)

	x, y := findBox(t, a, "core:restart")
	if quit := a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}}); quit {
		t.Fatal("点「重启」不该退出界面")
	}
	waitFor(t, "重启请求发出", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return fb.restarts == 1
	})
}

// ---------------------------------------------------------------------------
// 插件页整块搬进了插件管理器自己身上
//
// 这一页以前长在这儿的界面里（列表 / 详情 / 启停 / 删除 / 二次确认，一整套用例），
// 现在由 pluginManager 自己绘制（plugins/plugin-manager/panel.go），
// 用例也跟着搬到了那边。
//
// 界面这边只留"扫一遍插件目录"这件事的用例：认出自带页面的插件（ui.page.*）、
// 说清侧栏里某个服务是谁在提供。
// ---------------------------------------------------------------------------
func TestHumanSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
	}
	for _, c := range cases {
		if got := humanSize(c.n); got != c.want {
			t.Fatalf("humanSize(%d) = %q，期望 %q", c.n, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 光标：左右移动必须真的带动终端光标
// ---------------------------------------------------------------------------

// 移动光标不改变任何文字，所以"画面没变就不重绘"的优化会把光标留在原地。
// 这条锁住光标定位序列本身会跟着变。
func TestCursorSequenceFollowsArrowKeys(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.w, a.st.h = 100, 30
	a.setTab(tabChat)
	typeString(a, "hello")

	atEnd := cursorSeq(a.st, 100, 30)
	a.handle(event{kind: evKey, key: keyLeft})
	afterLeft := cursorSeq(a.st, 100, 30)
	if afterLeft == atEnd {
		t.Fatal("左移光标后定位序列必须变化，否则终端光标不会跟着动")
	}

	a.handle(event{kind: evKey, key: keyRight})
	if back := cursorSeq(a.st, 100, 30); back != atEnd {
		t.Fatalf("右移回原位应还原到原来的序列：%q vs %q", back, atEnd)
	}
}

// 中文字符占两列，光标列位置要按显示宽度算，不能按字符个数算
func TestCursorColumnUsesDisplayWidth(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.w, a.st.h = 100, 30
	a.setTab(tabChat)
	typeString(a, "中a")

	end := cursorPos(a.st, 100)
	a.moveCursor(-1) // 移到 'a' 之前
	beforeA := cursorPos(a.st, 100)
	if end-beforeA != 1 {
		t.Fatalf("'a' 宽 1 列，实际位移 %d", end-beforeA)
	}

	a.moveCursor(-1) // 移到 '中' 之前
	beforeHan := cursorPos(a.st, 100)
	if beforeA-beforeHan != 2 {
		t.Fatalf("'中' 宽 2 列，实际位移 %d", beforeA-beforeHan)
	}
}

// 非聊天页不显示光标
func TestCursorHiddenOutsideChat(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.setTab(tabPanel)
	if got := cursorSeq(a.st, 100, 30); !strings.Contains(got, "?25l") {
		t.Fatalf("非聊天页应隐藏光标，实际 %q", got)
	}
}

func TestEnterInsertsNewlineInsteadOfSending(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.setTab(tabChat)
	layout(a, 100, 30)

	typeString(a, "第一行")
	a.handle(event{kind: evKey, key: keyEnter})
	typeString(a, "第二行")

	if got := string(a.st.input); got != "第一行\n第二行" {
		t.Fatalf("回车应插入换行，实际输入框是 %q", got)
	}
	if chats := fb.snapshot(); len(chats) != 0 {
		t.Fatalf("回车不该发送，实际发出 %d 条", len(chats))
	}

	// 点「发送」才真的送出去，而且换行要原样留在消息里
	layout(a, 100, 30)
	x, y := findBox(t, a, "btn:send")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	waitFor(t, "点「发送」后送出", func() bool {
		chats := fb.snapshot()
		return len(chats) == 1
	})
	if chats := fb.snapshot(); chats[0] != "第一行\n第二行" {
		t.Fatalf("发出去的应是含换行的原文，实际 %q", chats[0])
	}
	if len(a.st.input) != 0 {
		t.Fatalf("发送后输入框应清空，实际 %q", string(a.st.input))
	}
}

// 空输入点「发送」：什么都不发生，也不该崩。
func TestSendWithEmptyInputDoesNothing(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.setTab(tabChat)
	layout(a, 100, 30)

	x, y := findBox(t, a, "btn:send")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	if chats := fb.snapshot(); len(chats) != 0 {
		t.Fatalf("空输入不该发出消息，实际 %d 条", len(chats))
	}
}

// Ctrl+Enter 是键盘用户的送出口：回车已经变成换行，总得留一条不离开手的路。
func TestCtrlEnterSends(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.setTab(tabChat)
	layout(a, 100, 30)

	typeString(a, "键盘发送")
	a.handle(event{kind: evKey, key: keyCtrlEnter})

	waitFor(t, "Ctrl+Enter 送出", func() bool {
		chats := fb.snapshot()
		return len(chats) == 1
	})
}

// 修改态的按钮叫「提交」：同一个位置做的是另一件事，别让人以为在发新消息。
func TestEditModeButtonSaysSubmit(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.setTab(tabChat)
	a.st.editing = "n1"
	layout(a, 100, 34)

	if _, err := findBoxQuiet(a, "btn:send"); err != nil {
		t.Fatal("修改态也必须有一个提交按钮，否则改完没法提交")
	}
	if !strings.Contains(a.canv.frame(), "提交") {
		t.Fatalf("修改态按钮应写「提交」，实际帧：\n%s", a.canv.frame())
	}
}

// 输入框长成多行：版面给它让位，光标跟着落到正确那一行。
func TestMultilineInputGrowsAndCursorFollows(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.setTab(tabChat)
	layout(a, 100, 30)

	empty := cursorSeq(a.st, 100, 30)
	if !strings.HasPrefix(empty, "\033[29;") {
		t.Fatalf("单行输入时光标在第 29 行，实际 %q", empty)
	}

	typeString(a, "甲")
	a.handle(event{kind: evKey, key: keyEnter})
	typeString(a, "乙")
	a.handle(event{kind: evKey, key: keyUp}) // 回到第一行

	// 输入区变成两行之后，第一行被顶到第 28 行
	first := cursorSeq(a.st, 100, 30)
	if !strings.HasPrefix(first, "\033[28;") {
		t.Fatalf("两行输入时第一行的光标应在第 28 行，实际 %q", first)
	}

	// 帧里要真的看到两行：提示符 + 甲，以及续行 + 乙
	layout(a, 100, 30)
	rows := strings.Split(a.canv.frame(), "\n")
	ok := false
	for i, r := range rows {
		if strings.Contains(r, "你 ›") && strings.Contains(r, "甲") &&
			i+1 < len(rows) && strings.Contains(rows[i+1], "乙") {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("输入区应画成两行（甲 / 乙），实际帧：\n%s", a.canv.frame())
	}
}

// ↑↓ 在多行输入里走光标；Home/End 只在当前行内走。
func TestArrowKeysWalkInputLines(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.setTab(tabChat)
	layout(a, 100, 30)

	typeString(a, "第一行")
	a.handle(event{kind: evKey, key: keyEnter})
	typeString(a, "第二行")
	if a.st.cursor != len(a.st.input) {
		t.Fatalf("前置条件：光标应在末尾，实际 %d", a.st.cursor)
	}

	a.handle(event{kind: evKey, key: keyUp})
	if got := string(a.st.input[:a.st.cursor]); got != "第一行" {
		t.Fatalf("↑ 应把光标带到第一行（第一行更短，夹到行尾），实际停在 %q", got)
	}
	a.handle(event{kind: evKey, key: keyDown})
	if a.st.cursor != len(a.st.input) {
		t.Fatalf("↓ 应回到第二行行尾，实际下标 %d", a.st.cursor)
	}

	a.handle(event{kind: evKey, key: keyHome})
	if got := string(a.st.input[:a.st.cursor]); got != "第一行\n" {
		t.Fatalf("Home 应到当前行（第二行）行首，实际停在 %q", got)
	}
	a.handle(event{kind: evKey, key: keyEnd})
	if a.st.cursor != len(a.st.input) {
		t.Fatalf("End 应到当前行行尾，实际下标 %d", a.st.cursor)
	}
}

// 文字再长也不能压住「发送」：它现在是唯一无歧义的送出口。
// 折行宽度必须预先扣掉按钮那几列，否则第一行文字会顶到按钮下面 —— 热区还在，人看不见。
func TestSendButtonStaysClickableWithLongInput(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.setTab(tabChat)
	layout(a, 70, 30)

	typeString(a, strings.Repeat("很长的一段话", 30)) // 远超一行，必然折行并滚动
	layout(a, 70, 30)

	x, y := findBox(t, a, "btn:send")
	if got := a.canv.hit(x, y); got != "btn:send" {
		t.Fatalf("「发送」那一列被别的热区占了：%q", got)
	}
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	waitFor(t, "长文本也能点发送", func() bool {
		chats := fb.snapshot()
		return len(chats) == 1
	})
}

func findBoxQuiet(a *app, action string) (int, error) {
	for _, b := range a.canv.boxes {
		if b.action == action {
			return b.x, nil
		}
	}
	return 0, fmt.Errorf("未找到 %s", action)
}

// 点侧栏里的某个服务：说清楚它此刻是谁在提供。
//
// 以前这一下会跳到「插件」页并选中提供者。那一页现在由插件管理器自带，
// 而"跳进去并选中某个插件"要面板协议为某一页开特例 —— 不值当，
// 所以这一下退回它本来就能说清的那件事。
func TestClickServiceSaysWhoProvidesIt(t *testing.T) {
	fb := &fakeBackend{}
	seed(fb)
	a := newTestApp(fb)
	a.st.services = fb.services
	a.st.plugins = fb.plugins
	layout(a, 120, 34)

	x, y := findBox(t, a, "svc:session.v1")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	if a.st.tab != tabChat {
		t.Fatalf("点服务不该跳页，实际 %v", a.st.tab)
	}
	if !a.st.statusOK {
		t.Fatalf("session.v1 有提供者，状态应为正常：%q", a.st.status)
	}
	if !strings.Contains(a.st.status, "session.v1") || !strings.Contains(a.st.status, "com.official.session") {
		t.Fatalf("状态栏应说明这个服务由谁提供，实际 %q", a.st.status)
	}
}

// ---------------------------------------------------------------------------
// 聊天与键盘
// ---------------------------------------------------------------------------

func TestChatSendAndRender(t *testing.T) {
	fb := &fakeBackend{}
	seed(fb)
	a := newTestApp(fb)
	layout(a, 100, 30)

	typeString(a, "你好")
	sendNow(t, a)

	waitFor(t, "chat 被调用", func() bool {
		chats := fb.snapshot()
		return len(chats) == 1
	})
	chats := fb.snapshot()
	if chats[0] != "你好" {
		t.Fatalf("发送内容错误：%q", chats[0])
	}

	// 主循环外手动消费一次结果，验证渲染
	a.applyOutcome(outcome{kind: "chat", reply: `{"content":"收到"}`, via: "session.v1"})
	layout(a, 100, 30)
	frame := a.canv.frame()
	if !strings.Contains(frame, "收到") {
		t.Fatal("回复应出现在对话区")
	}
}

func TestMouseWheelAndScroll(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	for i := 0; i < 60; i++ {
		a.st.msgs = append(a.st.msgs, msgView{
			id: fmt.Sprintf("n%d", i), role: "user", at: "10:00:00",
			text: fmt.Sprintf("第 %d 条", i), version: 1, total: 1,
		})
	}
	layout(a, 100, 30)

	a.handle(event{kind: evMouse, mouse: mouseEvent{Wheel: -1}}) // 上滚
	if a.st.scroll != 1 {
		t.Fatalf("上滚应让 scroll 增加，实际 %d", a.st.scroll)
	}
	a.handle(event{kind: evMouse, mouse: mouseEvent{Wheel: 1}}) // 下滚
	a.handle(event{kind: evMouse, mouse: mouseEvent{Wheel: 1}}) // 到底后夹住
	if a.st.scroll != 0 {
		t.Fatalf("下滚到底后 scroll 应为 0，实际 %d", a.st.scroll)
	}
}

// Ctrl+C 专职复制，绝不退出；退出是 Ctrl+Q。
func TestCtrlCAndQ(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	if a.handle(event{kind: evKey, key: keyCtrlC}) {
		t.Fatal("Ctrl+C 不该退出（它是复制键）")
	}
	if !a.handle(event{kind: evKey, key: keyCtrlQ}) {
		t.Fatal("Ctrl+Q 应请求退出")
	}
}

// ---------------------------------------------------------------------------
// 鼠标序列解析：这是自动化验证点击的唯一入口
// ---------------------------------------------------------------------------

func TestParseSGRMouse(t *testing.T) {
	feed := func(p *eventParser, s string) []event {
		var out []event
		for i := 0; i < len(s); i++ {
			out = append(out, p.feed(s[i])...)
		}
		return out
	}

	p := &eventParser{}
	evs := feed(p, "\033[<0;12;5M")
	if len(evs) != 1 || evs[0].kind != evMouse {
		t.Fatalf("应解析出一个鼠标事件，实际 %+v", evs)
	}
	if evs[0].mouse.X != 11 || evs[0].mouse.Y != 4 || !evs[0].mouse.Press {
		t.Fatalf("SGR 坐标应从 1 基转 0 基：%+v", evs[0].mouse)
	}

	p = &eventParser{}
	evs = feed(p, "\033[<64;3;4M")
	if len(evs) != 1 || evs[0].mouse.Wheel != -1 {
		t.Fatalf("滚轮向上应解析为 Wheel=-1：%+v", evs)
	}
	if evs[0].mouse.Y != 3 {
		t.Fatalf("滚轮坐标应转 0 基：%+v", evs[0].mouse)
	}

	// 按住左键移动：必须送到界面，否则浮窗拖不动（以前这一段是被丢掉的）
	p = &eventParser{}
	if evs = feed(p, "\033[<32;12;5M"); len(evs) != 1 || !evs[0].mouse.Drag {
		t.Fatalf("拖动应解析成一个 Drag 事件：%+v", evs)
	}
	if evs[0].mouse.X != 11 || evs[0].mouse.Y != 4 {
		t.Fatalf("拖动坐标应转 0 基：%+v", evs[0].mouse)
	}

	// 松手同样要给：拖完得知道什么时候停（没在拖的时候界面不理它）
	p = &eventParser{}
	if evs = feed(p, "\033[<0;12;5m"); len(evs) != 1 || !evs[0].mouse.Rel {
		t.Fatalf("松手应解析成一个 Rel 事件：%+v", evs)
	}
	if evs[0].mouse.X != 11 || evs[0].mouse.Y != 4 {
		t.Fatalf("松手坐标应转 0 基：%+v", evs[0].mouse)
	}

	// 按键与鼠标混排：顺序不能乱
	p = &eventParser{}
	evs = feed(p, "\033[A\033[<0;2;2M哈")
	if len(evs) != 3 {
		t.Fatalf("应解析出 上/点击/字符 三个事件，实际 %+v", evs)
	}
	if evs[0].key != keyUp || evs[1].kind != evMouse || evs[2].r != '哈' {
		t.Fatalf("事件顺序错乱：%+v", evs)
	}
}

// ---------------------------------------------------------------------------
// 后端数据解析
// ---------------------------------------------------------------------------

// 读不到 manifest 的目录不该被界面静静跳过。
//
// 用例本身搬去了插件页（plugins/plugin-manager/panel_test.go 里那一份会
// 把"读不出、拒绝启停与删除"整页画出来）；界面这边只保留它仍要负责的那一段：
// 扫目录时不能因为一份 manifest 坏了就整份失败 —— 其余插件还得能认出来。
func TestBrokenManifestSkipsOnlyItself(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.plugins = []pluginInfo{
		{ID: "com.broken", ReadErr: "读取失败: access denied", Dir: "broken.plugin"},
		{ID: "com.official.workspace", Dir: "workspace.plugin",
			Provides: []string{"ui.page.workspace.v1"}, Alive: []string{"ui.page.workspace.v1"}},
	}
	layout(a, 120, 34)

	// 坏的那份不影响好的那份被认出来（标签栏上该出现它自带的那一页）
	if got := pageServices(a.st.plugins); len(got) != 1 || got[0] != "ui.page.workspace.v1" {
		t.Fatalf("坏 manifest 不该影响其它插件被认出来，实际 %v", got)
	}
}

// 链式对话：点得到、翻得动、旧时间线不消失
// ---------------------------------------------------------------------------

// drain 从结果通道取一条并应用，做的是主循环里那一步。
func drain(t *testing.T, a *app) outcome {
	t.Helper()
	select {
	case out := <-a.results:
		a.applyOutcome(out)
		return out
	case <-time.After(2 * time.Second):
		t.Fatal("等待后台结果超时")
		return outcome{}
	}
}

// drainApply 取一条投递给界面线程的状态修改并执行（分享、导出走这条）。
func drainApply(t *testing.T, a *app) {
	t.Helper()
	select {
	case fn := <-a.apply:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("等待界面更新超时")
	}
}

// newConvApp 起一个装了 conversation.v1 的界面，并完成首次拉取。
func newConvApp(t *testing.T, fb *fakeBackend) *app {
	t.Helper()
	fb.convOn = true
	a := newTestApp(fb)
	a.loadConversation()
	drain(t, a)
	return a
}

func TestConvSendRendersClickableBoxes(t *testing.T) {
	fb := &fakeBackend{}
	a := newConvApp(t, fb)
	layout(a, 110, 34)

	typeString(a, "你好")
	sendNow(t, a)
	drain(t, a)
	layout(a, 110, 34)

	if len(fb.convSends) != 1 || fb.convSends[0] != "你好" {
		t.Fatalf("应走 conversation.v1 发消息，实际 %v", fb.convSends)
	}
	frame := a.canv.frame()
	if !strings.Contains(frame, "收到：你好") {
		t.Fatal("AI 的回复应出现在对话区")
	}
	// 用户与 AI 的消息都必须是一个可点的框
	for _, action := range []string{
		"msg:n1:edit", "msg:n1:regen", "msg:n1:share",
		"msg:n2:edit", "msg:n2:regen", "msg:n2:share",
	} {
		findBox(t, a, action)
	}
}

// 改前一条消息：生成一个新版本，后面那段一起隐没；旧版本仍在，靠 < > 翻回来。
func TestConvEditMakesNewVersionAndKeepsOldOne(t *testing.T) {
	fb := &fakeBackend{}
	a := newConvApp(t, fb)
	layout(a, 110, 34)

	typeString(a, "你好")
	sendNow(t, a)
	drain(t, a)
	layout(a, 110, 34)

	x, y := findBox(t, a, "msg:n1:edit")
	a.click(x, y)
	if a.st.editing != "n1" {
		t.Fatalf("点了「修改」应进入编辑态，实际 %q", a.st.editing)
	}
	if got := string(a.st.input); got != "你好" {
		t.Fatalf("编辑态应把原文放进输入行，实际 %q", got)
	}

	// 把原文删掉，换成新说法
	for i := 0; i < len([]rune("你好")); i++ {
		a.handle(event{kind: evKey, key: keyBackspace})
	}
	typeString(a, "换个问法")
	sendNow(t, a)
	drain(t, a)
	layout(a, 110, 34)

	if len(fb.convEdits) != 1 || fb.convEdits[0] != "n1=换个问法" {
		t.Fatalf("应把修改提交成新版本，实际 %v", fb.convEdits)
	}
	m := convMsgOf(t, a, "n1")
	if m.version != 2 || m.total != 2 {
		t.Fatalf("改完应是第 2/2 个版本，实际 %d/%d", m.version, m.total)
	}
	if m.text != "换个问法" {
		t.Fatalf("当前版本应显示新说法，实际 %q", m.text)
	}
	// 框下角出现 < n/n > ，点得到
	findBox(t, a, "page:n1:prev")
	findBox(t, a, "page:n1:next")
	if !strings.Contains(a.canv.frame(), "2/2") {
		t.Fatal("应显示版本标识 2/2")
	}
}

// 翻回旧版本：内容原样重现，一条都没少。
func TestConvPagerRestoresOldTimeline(t *testing.T) {
	fb := &fakeBackend{}
	a := newConvApp(t, fb)
	layout(a, 110, 34)

	typeString(a, "你好")
	sendNow(t, a)
	drain(t, a)
	layout(a, 110, 34)

	x, y := findBox(t, a, "msg:n1:regen")
	a.click(x, y)
	drain(t, a)
	layout(a, 110, 34)
	if m := convMsgOf(t, a, "n2"); m.total != 2 {
		t.Fatalf("重新生成后应有 2 个版本，实际 %d", m.total)
	}

	x, y = findBox(t, a, "page:n2:prev")
	a.click(x, y)
	drain(t, a)
	layout(a, 110, 34)

	if len(fb.convPages) != 1 || fb.convPages[0] != "n2=-1" {
		t.Fatalf("应发出往前翻一页的请求，实际 %v", fb.convPages)
	}
	m := convMsgOf(t, a, "n2")
	if m.version != 1 || m.text != "收到：你好" {
		t.Fatalf("翻回去应重新看到旧回复，实际 %d/%d %q", m.version, m.total, m.text)
	}
}

func TestConvSessionNewAndDeleteNeedsTwoClicks(t *testing.T) {
	fb := &fakeBackend{}
	a := newConvApp(t, fb)
	layout(a, 110, 34)

	x, y := findBox(t, a, "btn:sessnew")
	a.click(x, y)
	drain(t, a)
	if fb.convNews != 1 {
		t.Fatalf("点「新建」应新建一条会话，实际 %d", fb.convNews)
	}
	cur := a.st.sessID

	layout(a, 110, 34)
	x, y = findBox(t, a, "btn:sessdel")
	a.click(x, y)
	if !a.st.confirmSessDel {
		t.Fatal("第一次点「删除」应只进入确认态")
	}
	if len(fb.convDels) != 0 {
		t.Fatal("没确认之前不能真删")
	}

	layout(a, 110, 34)
	x, y = findBox(t, a, "btn:sessdelok")
	a.click(x, y)
	drain(t, a)
	if len(fb.convDels) != 1 || fb.convDels[0] != cur {
		t.Fatalf("确认后应删掉当前会话，实际 %v", fb.convDels)
	}
}

// 切会话：另一条会话的消息原样还在。
func TestConvSwitchSessionRestoresItsMessages(t *testing.T) {
	fb := &fakeBackend{}
	a := newConvApp(t, fb)

	typeString(a, "第一条会话")
	sendNow(t, a)
	drain(t, a)
	first := a.st.sessID

	a.newSession()
	drain(t, a)
	typeString(a, "第二条会话")
	sendNow(t, a)
	drain(t, a)

	a.switchSession(first)
	drain(t, a)
	if len(fb.convSends) != 2 || fb.convSends[0] != "第一条会话" {
		t.Fatalf("发送记录不对：%v", fb.convSends)
	}
	if len(a.st.msgs) != 2 || a.st.msgs[0].text != "第一条会话" {
		t.Fatalf("切回来应看到原来那条时间线，实际 %d 条", len(a.st.msgs))
	}
}

// 分享面板：点「导出 .md」落到工作区。
func TestConvSharePanelExportsMarkdown(t *testing.T) {
	fb := &fakeBackend{}
	a := newConvApp(t, fb)
	layout(a, 110, 34)

	typeString(a, "你好")
	sendNow(t, a)
	drain(t, a)
	layout(a, 110, 34)

	x, y := findBox(t, a, "btn:seshare")
	a.click(x, y)
	if !a.st.shareOpen {
		t.Fatal("点「分享整条」应打开分享面板")
	}
	layout(a, 110, 34)
	if !strings.Contains(a.canv.frame(), "复制") {
		t.Fatal("分享面板应给出复制与导出两条路")
	}

	x, y = findBox(t, a, "btn:sharefile")
	a.click(x, y)
	if a.st.shareOpen {
		t.Fatal("选了导出应关掉分享面板")
	}
	drainApply(t, a)
	if fb.convExports != 1 {
		t.Fatalf("应导出一次，实际 %d", fb.convExports)
	}
	if !strings.Contains(a.st.notice, "已导出") {
		t.Fatalf("导出后应给出落点提示，实际 %q", a.st.notice)
	}
}

// 没装 conversation.v1：退回原来那条直线聊天，按钮一个都不出现。
func TestConvOffFallsBackToLinearChat(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	layout(a, 110, 34)

	a.loadConversation()
	drain(t, a)
	if a.st.convOK {
		t.Fatal("没装插件时不该认为可以走消息树")
	}

	typeString(a, "你好")
	sendNow(t, a)
	waitFor(t, "走降级链路", func() bool {
		chats := fb.snapshot()
		return len(chats) == 1
	})
	if len(fb.convSends) != 0 {
		t.Fatal("没装插件时不该调用 conversation.v1")
	}
	for _, b := range a.canv.boxes {
		if strings.HasPrefix(b.action, "msg:") || strings.HasPrefix(b.action, "page:") {
			t.Fatalf("降级链路上不该有消息按钮：%s", b.action)
		}
	}
}

// ---------------------------------------------------------------------------
// 热区与画面必须对齐
// ---------------------------------------------------------------------------

// sliceDisplay 取从显示列 from 起、宽度 n 的那段文字。
func sliceDisplay(s string, from, n int) string {
	var out []rune
	col := 0
	for _, r := range s {
		w := runeWidth(r)
		if col >= from && col+w <= from+n {
			out = append(out, r)
		}
		col += w
	}
	return string(out)
}

// assertHitboxesAligned 检查每个按钮的热区盖住的正是它画出来的那段文字。
//
// 抓的是一类很隐蔽的错位：button / chip 返回的「下一个 x」算进了按钮之间的 2 列间隔，
// 要是那 2 列没被画出来，从第二个按钮起热区就比文字偏右、并随按钮个数逐个累积 ——
// 用户的感觉就是「按钮点上去时灵时不灵」，有时还会点到隔壁那个。
//
// findBox 那类用例永远发现不了它：它们拿热区自己的坐标去点，热区偏到哪儿都能命中。
func assertHitboxesAligned(t *testing.T, a *app, where string) {
	t.Helper()
	for _, b := range a.canv.boxes {
		if b.label == "" {
			continue // 手工登记的热区（如翻页的 ‹ ›）不绑定标签
		}
		got := sliceDisplay(a.canv.rows[b.y], b.x, b.w)
		if !strings.Contains(got, strings.Trim(b.label, " ")) {
			t.Fatalf("%s：动作 %s 的热区盖住的是 %q，可它画的是 %q —— 画在哪和点哪对不上",
				where, b.action, got, b.label)
		}
	}
}

func TestHitboxesCoverTheirLabels(t *testing.T) {
	for _, sz := range [][2]int{{120, 34}, {100, 30}, {84, 26}} {
		fb := &fakeBackend{}
		seed(fb)
		a := newTestApp(fb)
		a.st.plugins = fb.plugins
		a.st.sessID = "s1"
		a.st.sessions = []sessionInfo{{id: "s1", name: "会话 1", nodes: 2}}
		a.st.msgs = []msgView{
			{id: "n1", role: "user", at: "10:00:00", text: "你好", version: 2, total: 2},
			{id: "n2", role: "assistant", at: "10:00:01", text: "收到", version: 1, total: 1},
		}
		where := fmt.Sprintf("%dx%d", sz[0], sz[1])

		for _, tab := range allTabs {
			a.st.tab = tab
			layout(a, sz[0], sz[1])
			assertHitboxesAligned(t, a, where+" "+tab.label())
		}

		// 分享面板的三个按钮
		a.st.tab = tabChat
		a.st.shareOpen = true
		layout(a, sz[0], sz[1])
		assertHitboxesAligned(t, a, where+" 分享面板")

		// 会话删除的二次确认
		a.st.shareOpen = false
		a.st.confirmSessDel = true
		layout(a, sz[0], sz[1])
		assertHitboxesAligned(t, a, where+" 会话删除确认")
	}
}
