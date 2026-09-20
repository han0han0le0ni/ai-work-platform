package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 假 backend：界面逻辑不依赖核心，可以完整地在单测里驱动
// ---------------------------------------------------------------------------

type fakeBackend struct {
	mu sync.Mutex

	services []serviceStatus
	plugins  []pluginInfo
	cfg      llmConfig
	models   []string

	saved         []string
	chats         []string
	saves         int
	tests         int
	modelCalls    int
	lastModelArgs []string
	saveEr        error

	toggles []string // 记录 "id=true/false"
	deletes []string
	// 目录名也要记下来：同一个 ID 可能在目录里躺了两份，
	// 启停 / 删除必须落在用户选中的那一份上
	toggleDirs []string
	deleteDirs []string

	browsed    []string // 每次 browse 传下去的路径
	setRoots   []string // 每次 setRoot 传下去的路径
	fakeBrowse wsListing

	budgetSteps     int
	budgetUnlimited bool
	budgetSaved     []int // configure 收到的 maxSteps

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

	// 浮窗位置与重启
	floatSaves [][3]int // 每次浮窗停下：[x, y, 是否收成一条]
	restarts   int      // 点了几次「重启」
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

// saveFloat 记下浮窗停下的地方（x、y、有没有收成一条）。
func (f *fakeBackend) saveFloat(x, y int, fold bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	folded := 0
	if fold {
		folded = 1
	}
	f.floatSaves = append(f.floatSaves, [3]int{x, y, folded})
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

func (f *fakeBackend) setPluginEnabled(id, dir string, enabled bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toggles = append(f.toggles, fmt.Sprintf("%v", enabled))
	f.toggleDirs = append(f.toggleDirs, dir)
	verb := "已禁用"
	if enabled {
		verb = "已启用"
	}
	return verb + " " + id, nil
}

func (f *fakeBackend) deletePlugin(id, dir string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, id)
	f.deleteDirs = append(f.deleteDirs, dir)
	return "已删除 " + id, nil
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

func (f *fakeBackend) wsSetRoot(path string) (wsListing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setRoots = append(f.setRoots, path)
	ls := f.fakeBrowse
	ls.Path = path
	ls.Root = path
	ls.Note = "工作区已设为 " + path
	return ls, nil
}

func (f *fakeBackend) loadAgentBudget() (int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.budgetSteps, f.budgetUnlimited, nil
}

func (f *fakeBackend) saveAgentBudget(steps int, unlimited bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if unlimited {
		steps = -1
	}
	f.budgetSaved = append(f.budgetSaved, steps)
	f.budgetUnlimited = unlimited
	f.budgetSteps = steps
	if unlimited {
		return "已设为不限制", nil
	}
	return "已设为最多 " + strconv.Itoa(steps) + " 轮", nil
}

func (f *fakeBackend) loadLLM() (llmConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg, nil
}

func (f *fakeBackend) saveLLM(provider, baseURL, model, apiKey, visionModel string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves++
	f.saved = []string{provider, baseURL, model, apiKey, visionModel}
	return "已保存", f.saveEr
}

func (f *fakeBackend) testLLM() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tests++
	return "连接正常", nil
}

func (f *fakeBackend) listModels(provider, baseURL, apiKey string) ([]string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modelCalls++
	f.lastModelArgs = []string{provider, baseURL, apiKey}
	return append([]string{}, f.models...), "已获取", nil
}

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

func (f *fakeBackend) snapshot() (saves, tests int, chats []string, saved []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saves, f.tests, append([]string{}, f.chats...), append([]string{}, f.saved...)
}

// newTestApp 组装一个不接核心、不碰终端的界面实例。
func newTestApp(fb *fakeBackend) *app {
	a := &app{
		plug:    fb,
		apply:   make(chan func(), 8),
		results: make(chan outcome, 8),
		done:    make(chan struct{}),
	}
	a.st = &state{
		coreVersion: "1.0.0",
		apiVersion:  "1",
		pluginID:    "com.official.workbench",
		tab:         tabChat,
		focus:       "chat",
		fields:      defaultFields(),
		// 预算已读过：切到设置页不再自动回读。
		// 真实启动会自己去读（见 kickLoads），测试里由用例显式驱动，
		// 否则每个切页的测试都会在结果通道里收到一条计划外的 budget。
		budgetLoaded: true,
	}
	return a
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
		ID: "com.official.workspace", Name: "Workspace Files",
		Provides: []string{"workspace.v1"}, Dead: []string{"workspace.v1"},
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
		ID: "com.official.workspace", Name: "Workspace Files",
		Provides: []string{"workspace.v1"}, Alive: []string{"workspace.v1"},
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

	// 刷新会把插件清单与服务链路两条结果投回界面
	drain(t, a)
	drain(t, a)

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

// 启动那次可能撞上 agent 尚未就绪，所以进设置页要能补读一次
func TestSettingsTabLazyLoadsBudget(t *testing.T) {
	fb := &fakeBackend{budgetSteps: 9}
	a := newTestApp(fb)
	a.st.budgetLoaded = false

	a.setTab(tabSettings)
	if !a.st.budgetBusy {
		t.Fatal("没读到过预算时，进设置页应补读一次")
	}
}

// layout 只做渲染，不进事件循环。
func layout(a *app, w, h int) {
	a.st.w, a.st.h = w, h
	a.canv = renderFrame(a.st, w, h, false)
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
		{
			ID: "com.official.llm", Name: "LLM", Version: "1.0.0",
			Description: "LLM 插件：把消息与工具定义翻译成模型调用（OpenAI 兼容 / Ollama / 离线 mock）",
			Runtime:     "binary", Entry: "main",
			Provides: []string{"llm.v1"}, Permissions: []string{"network"},
			Alive:  []string{"llm.v1"},
			Compat: ">=1.0 <2.0",
		},
		{
			ID: "com.official.session", Name: "Session", Version: "1.0.0",
			Runtime: "binary", Entry: "main",
			Provides: []string{"session.v1"}, Consumes: []string{"agent.v1"},
			Alive: []string{"session.v1"},
		},
		{
			ID: "com.official.fs-tool", Name: "File Tool", Version: "1.0.0",
			Runtime: "binary", Entry: "main",
			Provides: []string{"readFile.v1"},
			Dead:     []string{"readFile.v1"},
		},
	}
	fb.cfg = llmConfig{Provider: "openai", BaseURL: "https://api.openai.com/v1",
		Model: "gpt-4o-mini", APIKey: "sk-t****cdef"}
	fb.models = []string{"deepseek-chat", "deepseek-reasoner"}
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
			a.st.provider = "openai"
			a.st.wsRoot = fb.fakeBrowse.Root
			a.st.wsPath = fb.fakeBrowse.Path
			a.st.wsParent = fb.fakeBrowse.Parent
			a.st.wsEntries = fb.fakeBrowse.Entries
			a.st.wsSel = 1

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

func TestClickSwitchesTab(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	layout(a, 100, 30)

	x, y := findBox(t, a, "tab:settings")
	if quit := a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}}); quit {
		t.Fatal("点击标签不应退出")
	}
	if a.st.tab != tabSettings {
		t.Fatalf("点击「设置」后应切到设置页，实际是 %v", a.st.tab)
	}

	layout(a, 100, 30)
	x, y = findBox(t, a, "tab:plugins")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})
	if a.st.tab != tabPlugins {
		t.Fatalf("点击「插件」后应切到插件页，实际是 %v", a.st.tab)
	}
}

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

func TestClickPluginRowSelectsAndShowsDetail(t *testing.T) {
	fb := &fakeBackend{}
	seed(fb)
	a := newTestApp(fb)
	a.st.plugins = fb.plugins
	a.st.tab = tabPlugins
	layout(a, 120, 34)

	// 动作名是行号、不是插件 ID：两份插件可能同 ID，重名的话后一份永远点不中。
	// seed 里第 3 份（下标 2）就是 fs-tool。
	x, y := findBox(t, a, "plugin:2")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})
	if got := a.st.plugins[a.st.selectedPlugin].ID; got != "com.official.fs-tool" {
		t.Fatalf("应选中 com.official.fs-tool，实际选中 %q", got)
	}
	if a.st.statusOK {
		t.Fatal("选中的插件未运行，状态不应显示为正常")
	}

	// 主循环每处理完一条输入都会重绘，测试里手动补一次
	layout(a, 120, 34)

	// 详情面板要真的写出这个插件的关键信息
	frame := a.canv.frame()
	for _, want := range []string{"com.official.fs-tool", "readFile.v1", "未运行", "binary"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("详情面板缺少 %q", want)
		}
	}
}

// 目录里躺着两份 ID 相同的插件时（x.plugin 与 x.plugin.disabled），两条都得能选中。
//
// 动作名曾经就是插件 ID：两份登记出两个同名的框，命中永远落在前一个上，
// 后一条就成了怎么点都没反应的死行 —— 用户看到的就是"这个插件选不中"。
func TestClickSecondCopyOfSamePluginIDSelectsIt(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.tab = tabPlugins
	a.st.plugins = []pluginInfo{
		{ID: "com.official.svgdraw", Version: "1.0.0", Dir: "svgdraw.plugin",
			Alive: []string{"svgdraw.v1"}},
		{ID: "com.official.svgdraw", Version: "1.0.0", Dir: "svgdraw.plugin.disabled",
			Disabled: true, Alive: []string{"svgdraw.v1"}},
	}
	layout(a, 120, 34)

	x, y := findBox(t, a, "plugin:1")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	if got := a.st.selectedPlugin; got != 1 {
		t.Fatalf("点第二份就该选中第二份，实际下标 %d", got)
	}
	if got := a.st.plugins[a.st.selectedPlugin].Dir; got != "svgdraw.plugin.disabled" {
		t.Fatalf("选中的应是禁用那份，实际 %q", got)
	}
}

// 同 ID 有两份时，删除只能落在选中的那份上：只按 ID 找会删掉另一份。
func TestDeleteTargetsTheSelectedCopy(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.st.tab = tabPlugins
	a.st.plugins = []pluginInfo{
		{ID: "com.official.svgdraw", Version: "1.0.0", Dir: "svgdraw.plugin",
			Alive: []string{"svgdraw.v1"}},
		{ID: "com.official.svgdraw", Version: "1.0.0", Dir: "svgdraw.plugin.disabled", Disabled: true},
	}
	layout(a, 120, 34)

	x, y := findBox(t, a, "plugin:1")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})
	a.press("del")        // 第一下只是进入确认态
	a.press("delconfirm") // 确认态下才有这个按钮

	waitFor(t, "删除请求发出", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.deleteDirs) == 1
	})
	fb.mu.Lock()
	dir := fb.deleteDirs[0]
	fb.mu.Unlock()
	if dir != "svgdraw.plugin.disabled" {
		t.Fatalf("删的应是选中的禁用那份，实际删的是 %q", dir)
	}
}

// 详情面板要能直接启停插件：当前启用 → 按钮显示「禁用」，点一下就传 enabled=false
func TestClickToggleDisablesSelectedPlugin(t *testing.T) {
	fb := &fakeBackend{}
	seed(fb)
	a := newTestApp(fb)
	a.st.tab = tabPlugins
	a.st.plugins = []pluginInfo{
		{ID: "com.official.fs-tool", Version: "1.0.0", Alive: []string{"readFile.v1"}},
	}
	layout(a, 120, 34)

	x, y := findBox(t, a, "btn:toggle")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	waitFor(t, "启停请求发出", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.toggles) == 1
	})
	fb.mu.Lock()
	got := fb.toggles[0]
	fb.mu.Unlock()
	if got != "false" {
		t.Fatalf("已启用的插件点击后应传 enabled=false，实际 %q", got)
	}

	// 按钮上的字要跟着状态变：禁用后应显示「启用」
	a.st.plugins[0].Disabled = true
	layout(a, 120, 34)
	if !strings.Contains(a.canv.frame(), "已禁用") {
		t.Fatal("禁用后详情应显示已禁用状态")
	}
}

// 删除不可逆：第一次点只是进入确认态，再点「确认删除」才真的动手
func TestDeleteNeedsTwoClicks(t *testing.T) {
	fb := &fakeBackend{}
	seed(fb)
	a := newTestApp(fb)
	a.st.tab = tabPlugins
	a.st.plugins = []pluginInfo{{ID: "com.official.fs-tool", Version: "1.0.0"}}
	layout(a, 120, 34)

	x, y := findBox(t, a, "btn:del")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	if !a.st.confirmDelete {
		t.Fatal("点「删除插件」应进入确认态，而不是直接删")
	}
	fb.mu.Lock()
	n := len(fb.deletes)
	fb.mu.Unlock()
	if n != 0 {
		t.Fatalf("未确认前不能真的删除，实际已删 %d 次", n)
	}

	// 确认态下界面要给「确认删除」和「取消」两个出口
	layout(a, 120, 34)
	findBox(t, a, "btn:delconfirm")
	findBox(t, a, "btn:delcancel")

	cx, cy := findBox(t, a, "btn:delconfirm")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: cx, Y: cy, Press: true}})
	waitFor(t, "删除请求发出", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.deletes) == 1
	})
	if a.st.confirmDelete {
		t.Fatal("确认后应退出确认态")
	}
}

// 换选中 / 按 Esc 都要撤掉上一次的删除确认：
// 否则在 A 上点了删除、切到 B 再随手一点，删掉的就是 B 了
func TestSwitchingPluginCancelsPendingDelete(t *testing.T) {
	fb := &fakeBackend{}
	seed(fb)
	a := newTestApp(fb)
	a.st.tab = tabPlugins
	a.st.plugins = []pluginInfo{
		{ID: "com.a", Version: "1.0.0"},
		{ID: "com.b", Version: "1.0.0"},
	}
	layout(a, 120, 34)

	a.press("del")
	if !a.st.confirmDelete {
		t.Fatal("应已进入确认态")
	}
	a.selectPlugin(1)
	if a.st.confirmDelete {
		t.Fatal("换选中插件必须撤掉删除确认")
	}

	a.press("del")
	a.handle(event{kind: evKey, key: keyEsc})
	if a.st.confirmDelete {
		t.Fatal("Esc 应撤掉删除确认")
	}
	fb.mu.Lock()
	n := len(fb.deletes)
	fb.mu.Unlock()
	if n != 0 {
		t.Fatalf("取消后不应真的删除，实际 %d 次", n)
	}
}

// 插件管理器和界面自己必须受保护：删掉它们就再也管不了插件了
func TestProtectedPluginsCannotBeRemoved(t *testing.T) {
	fb := &fakeBackend{}
	seed(fb)
	a := newTestApp(fb)
	a.st.tab = tabPlugins
	a.st.plugins = []pluginInfo{{ID: managerPluginID, Version: "1.0.0", Self: true}}
	layout(a, 120, 34)

	if _, err := findBoxQuiet(a, "btn:del"); err == nil {
		t.Fatal("受保护的插件不应出现删除按钮")
	}
	a.press("toggle")
	fb.mu.Lock()
	n := len(fb.toggles)
	fb.mu.Unlock()
	if n != 0 {
		t.Fatal("受保护的插件不应被启停")
	}
	if a.st.statusOK {
		t.Fatal("拒绝时应给出失败状态的提示")
	}
}

// ---------------------------------------------------------------------------
// 文件页：指定工作区，可选可填
// ---------------------------------------------------------------------------

// 点目录名进入子目录：浏览路径要接上这个名字
func TestFilePageClickEntersDirectory(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.st.tab = tabFiles
	a.st.wsPath = "/tmp/proj"
	a.st.wsEntries = []dirEntry{
		{Name: "src", Dir: true},
		{Name: "README.md", Size: 1024},
	}
	layout(a, 120, 34)

	x, y := findBox(t, a, "wsdir:src")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	waitFor(t, "browse 被调用", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.browsed) == 1
	})
	fb.mu.Lock()
	got := fb.browsed[0]
	fb.mu.Unlock()
	if got != "/tmp/proj/src" {
		t.Fatalf("进入子目录应拼成 /tmp/proj/src，实际 %q", got)
	}
}

// 上一次目录还没读完时再点一次：要给一句回话，不能默默吞掉
func TestFilePageClickWhileBusySaysWait(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.tab = tabFiles
	a.st.wsPath = "/tmp/proj"
	a.st.wsEntries = []dirEntry{{Name: "src", Dir: true}}
	a.st.wsBusy = true
	layout(a, 120, 34)

	x, y := findBox(t, a, "wsdir:src")
	a.click(x, y)
	if !strings.Contains(a.st.status, "稍等") {
		t.Fatalf("目录还在读时点击应提示稍等，实际 %q", a.st.status)
	}
}

// 点文件只选中，不该跳目录
func TestFilePageClickFileJustSelects(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.st.tab = tabFiles
	a.st.wsPath = "/tmp/proj"
	a.st.wsEntries = []dirEntry{{Name: "a.txt", Size: 2048}}
	layout(a, 120, 34)

	x, y := findBox(t, a, "wsfile:a.txt")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	if len(a.st.wsEntries) != 1 || !strings.Contains(a.st.status, "a.txt") {
		t.Fatalf("点文件应只是选中并提示，实际状态 %q", a.st.status)
	}
	fb.mu.Lock()
	n := len(fb.browsed)
	fb.mu.Unlock()
	if n != 0 {
		t.Fatal("点文件不应触发目录跳转")
	}
}

// 「填」这条路：输入框打路径 → 回车 → 设为工作区
func TestFilePageTypePathAndEnterSetsRoot(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.setTab(tabFiles)

	// 进这一页就落在路径框上，不用先点输入框
	if a.st.focus != "wsPath" {
		t.Fatalf("切到文件页应自动聚焦路径框，实际 %q", a.st.focus)
	}
	typeString(a, "E:/my-project")
	a.handle(event{kind: evKey, key: keyEnter})

	waitFor(t, "setRoot 被调用", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.setRoots) == 1
	})
	fb.mu.Lock()
	got := fb.setRoots[0]
	fb.mu.Unlock()
	if got != "E:/my-project" {
		t.Fatalf("应把输入框里的路径设为工作区，实际 %q", got)
	}
}

// 「选」这条路：输入框空着时，「设为工作区」用当前浏览到的目录
func TestFilePageSetRootFallsBackToBrowsedDir(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.st.tab = tabFiles
	a.st.wsPath = "/tmp/proj/src"
	if f := a.st.field("wsPath"); f != nil {
		f.value = ""
	}
	layout(a, 120, 34)

	x, y := findBox(t, a, "btn:wsset")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	waitFor(t, "setRoot 被调用", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.setRoots) == 1
	})
	fb.mu.Lock()
	got := fb.setRoots[0]
	fb.mu.Unlock()
	if got != "/tmp/proj/src" {
		t.Fatalf("输入框为空时应回退到当前浏览目录，实际 %q", got)
	}
}

// 进页面时的懒加载正在读目录，用户这时候按回车也必须生效。
//
// 这条是回归测试：曾经"设置工作区"和"读目录"共用一个 busy 标志，
// 结果刚进页面就按回车完全没反应 —— 而那正是最常见的操作顺序。
func TestEnterWorksWhileBrowseInFlight(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.setTab(tabFiles)
	if !a.st.wsBusy {
		t.Fatal("进文件页应触发一次目录读取")
	}

	typeString(a, "/tmp/typed")
	a.handle(event{kind: evKey, key: keyEnter})

	waitFor(t, "setRoot 仍被调用", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.setRoots) == 1
	})
	fb.mu.Lock()
	got := fb.setRoots[0]
	fb.mu.Unlock()
	if got != "/tmp/typed" {
		t.Fatalf("应设置输入框里的路径，实际 %q", got)
	}
}

// 浏览到的目录要回填进输入框：点着挑和手填看到的必须是同一个路径
func TestBrowseUpdatesPathField(t *testing.T) {
	fb := &fakeBackend{}
	fb.fakeBrowse = wsListing{Path: "/somewhere/else", Parent: "/somewhere"}
	a := newTestApp(fb)
	a.st.tab = tabFiles

	a.applyOutcome(outcome{kind: "ws", ws: &fb.fakeBrowse, note: "ok"})

	if got := a.fieldValue("wsPath"); got != "/somewhere/else" {
		t.Fatalf("浏览后输入框应回填路径，实际 %q", got)
	}
	if a.st.wsParent != "/somewhere" {
		t.Fatalf("应记住上一级，实际 %q", a.st.wsParent)
	}
}

// 界面上要真的显示工作区与目录条目
func TestFilePageRendersRootAndEntries(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.st.tab = tabFiles
	a.st.wsRoot = "/tmp/proj"
	a.st.wsPath = "/tmp/proj"
	a.st.wsEntries = []dirEntry{
		{Name: "src", Dir: true},
		{Name: "notes.txt", Size: 1536},
	}
	layout(a, 120, 34)

	frame := a.canv.frame()
	for _, want := range []string{"/tmp/proj", "src", "notes.txt", "1.5 KB", "设为工作区"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("文件页缺少 %q\n%s", want, frame)
		}
	}
}

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
	a.setTab(tabFiles)
	if got := cursorSeq(a.st, 100, 30); !strings.Contains(got, "?25l") {
		t.Fatalf("非聊天页应隐藏光标，实际 %q", got)
	}
}

// ---------------------------------------------------------------------------
// 工具调用预算：可选自定义，也可选不限制
// ---------------------------------------------------------------------------

// 点「不限制」再应用：写给 agent 的必须是 -1
func TestBudgetUnlimited(t *testing.T) {
	fb := &fakeBackend{budgetSteps: 6}
	a := newTestApp(fb)
	a.setTab(tabSettings)
	layout(a, 120, 46)

	x, y := findBox(t, a, "budget:unlimited")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})
	if !a.st.budgetUnlimited {
		t.Fatal("点「不限制」应选中它")
	}

	bx, by := findBox(t, a, "btn:budgetsave")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: bx, Y: by, Press: true}})

	waitFor(t, "预算写下去", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.budgetSaved) == 1
	})
	fb.mu.Lock()
	got := fb.budgetSaved[0]
	fb.mu.Unlock()
	if got != -1 {
		t.Fatalf("不限制应写成 -1，实际 %d", got)
	}
}

// 自定义步数：填多少就写多少（不再被硬上限截断）
func TestBudgetCustomSteps(t *testing.T) {
	fb := &fakeBackend{budgetSteps: 6}
	a := newTestApp(fb)
	a.setTab(tabSettings)
	layout(a, 120, 46)

	if f := a.st.field("agentSteps"); f != nil {
		f.value = ""
		f.dirty = true
	}
	a.st.focus = "agentSteps"
	typeString(a, "40")
	a.press("budgetsave")

	waitFor(t, "预算写下去", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.budgetSaved) == 1
	})
	fb.mu.Lock()
	got := fb.budgetSaved[0]
	fb.mu.Unlock()
	if got != 40 {
		t.Fatalf("应写入用户填的 40，实际 %d", got)
	}
}

// 填了非数字就别写下去，并且要说清楚
func TestBudgetRejectsBadInput(t *testing.T) {
	fb := &fakeBackend{budgetSteps: 6}
	a := newTestApp(fb)
	a.setTab(tabSettings)

	if f := a.st.field("agentSteps"); f != nil {
		f.value = "abc"
		f.dirty = true
	}
	a.press("budgetsave")

	fb.mu.Lock()
	n := len(fb.budgetSaved)
	fb.mu.Unlock()
	if n != 0 {
		t.Fatal("非法输入不应写下去")
	}
	if a.st.statusOK {
		t.Fatal("应给出失败提示")
	}
}

// 读回来的值要显示在输入框里
func TestBudgetShowsLoadedValue(t *testing.T) {
	fb := &fakeBackend{budgetSteps: 12}
	a := newTestApp(fb)
	a.applyOutcome(outcome{kind: "budget", budget: &agentBudget{Steps: 12}})

	if got := a.fieldValue("agentSteps"); got != "12" {
		t.Fatalf("应显示 12，实际 %q", got)
	}
	if !strings.Contains(a.st.budgetNote, "12") {
		t.Fatalf("说明文字应带上当前值，实际 %q", a.st.budgetNote)
	}

	// 不限制时输入框留空，用提示文字表达
	a.applyOutcome(outcome{kind: "budget", budget: &agentBudget{Unlimited: true}})
	if f := a.st.field("agentSteps"); f == nil || f.value != "" {
		t.Fatalf("不限制时输入框应留空，实际 %+v", f)
	}
	if !strings.Contains(a.st.budgetNote, "不限制") {
		t.Fatalf("说明文字应写明不限制，实际 %q", a.st.budgetNote)
	}
}

func findBoxQuiet(a *app, action string) (int, error) {
	for _, b := range a.canv.boxes {
		if b.action == action {
			return b.x, nil
		}
	}
	return 0, fmt.Errorf("未找到 %s", action)
}

func TestClickServiceJumpsToItsPlugin(t *testing.T) {
	fb := &fakeBackend{}
	seed(fb)
	a := newTestApp(fb)
	a.st.services = fb.services
	a.st.plugins = fb.plugins
	layout(a, 120, 34)

	x, y := findBox(t, a, "svc:session.v1")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	if a.st.tab != tabPlugins {
		t.Fatal("点击侧栏服务后应跳到插件页")
	}
	if got := a.st.plugins[a.st.selectedPlugin].ID; got != "com.official.session" {
		t.Fatalf("应选中 session.v1 的提供者，实际选中 %q", got)
	}
	if !a.st.statusOK {
		t.Fatalf("session.v1 有提供者，状态应为正常：%q", a.st.status)
	}
	if !strings.Contains(a.st.status, "session.v1") {
		t.Fatalf("状态栏应说明点了哪个服务，实际 %q", a.st.status)
	}
}

// ---------------------------------------------------------------------------
// 设置页：输入 + 保存
// ---------------------------------------------------------------------------

func TestSettingsTypingAndSave(t *testing.T) {
	fb := &fakeBackend{}
	seed(fb)
	a := newTestApp(fb)
	a.st.tab = tabSettings
	a.st.provider = "mock"
	a.st.plugins = fb.plugins
	layout(a, 120, 34)

	// 点 API Key 输入框
	x, y := findBox(t, a, "focus:apiKey")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})
	if a.st.focus != "apiKey" {
		t.Fatalf("点击后应聚焦 apiKey，实际 %q", a.st.focus)
	}

	typeString(a, "sk-test-1234567890abcdef")
	if got := a.fieldValue("apiKey"); got != "sk-test-1234567890abcdef" {
		t.Fatalf("输入未落到字段上：%q", got)
	}

	// 退格删掉一个字符，确认编辑生效
	a.handle(event{kind: evKey, key: keyBackspace})
	if got := a.fieldValue("apiKey"); got != "sk-test-1234567890abcde" {
		t.Fatalf("退格未生效：%q", got)
	}

	// 点选 provider
	layout(a, 120, 34)
	px, py := findBox(t, a, "preset:ollama")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: px, Y: py, Press: true}})
	if a.st.provider != "ollama" {
		t.Fatalf("点选服务预设未生效：%q", a.st.provider)
	}

	// 保存
	layout(a, 120, 34)
	bx, by := findBox(t, a, "btn:save")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: bx, Y: by, Press: true}})

	waitFor(t, "保存被调用", func() bool {
		n, _, _, _ := fb.snapshot()
		return n == 1
	})
	_, _, _, saved := fb.snapshot()
	if saved[0] != "ollama" {
		t.Fatalf("provider 应为 ollama，实际 %q", saved[0])
	}
	if saved[3] != "sk-test-1234567890abcde" {
		t.Fatalf("apiKey 传递错误：%q", saved[3])
	}

	// 明文不能在界面上留下 —— 但要等保存结果回来才清（失败时必须保留）
	var res outcome
	select {
	case res = <-a.results:
	case <-time.After(2 * time.Second):
		t.Fatal("没等到保存结果")
	}
	a.applyOutcome(res)
	if got := a.fieldValue("apiKey"); got != "" {
		t.Fatalf("保存成功后输入框应清空，实际 %q", got)
	}
}

func TestSaveWithoutChangesIsRejected(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.st.tab = tabSettings
	for i := range a.st.fields {
		a.st.fields[i].value = ""
	}
	layout(a, 120, 34)
	bx, by := findBox(t, a, "btn:save")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: bx, Y: by, Press: true}})

	time.Sleep(80 * time.Millisecond)
	if n, _, _, _ := fb.snapshot(); n != 0 {
		t.Fatal("没有任何改动时不该调用 saveLLM")
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
	a.handle(event{kind: evKey, key: keyEnter})

	waitFor(t, "chat 被调用", func() bool {
		_, _, chats, _ := fb.snapshot()
		return len(chats) == 1
	})
	_, _, chats, _ := fb.snapshot()
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

func TestTabKeyCyclesPages(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	layout(a, 100, 30)

	// 按 allTabs 绕一圈回到起点：页序变了也不用改测试
	for i := 1; i <= len(allTabs); i++ {
		want := allTabs[i%len(allTabs)]
		a.handle(event{kind: evKey, key: keyTab})
		if a.st.tab != want {
			t.Fatalf("第 %d 次 Tab 应切到 %s，实际 %s", i, want.label(), a.st.tab.label())
		}
		// 进入设置页会自动聚焦 API Key，此时 Tab 的语义变成"换输入框"
		// （见 TestTabMovesFocusWhileEditingSettings），要离开就先 Esc 取消聚焦
		if a.st.tab == tabSettings {
			a.handle(event{kind: evKey, key: keyEsc})
		}
	}
	if a.st.tab != tabChat {
		t.Fatalf("绕一圈后应回到聊天页，实际 %s", a.st.tab.label())
	}
}

// prevTab 返回 allTabs 里某一页的前一页，避免测试写死页序。
func prevTab(t tabKind) tabKind {
	for i, v := range allTabs {
		if v == t {
			return allTabs[(i-1+len(allTabs))%len(allTabs)]
		}
	}
	return allTabs[0]
}

// 切到设置页时，底部提示必须换成这一页自己的状态，
// 否则上一个页面的提示会一直挂着，看起来像刚在这里发生过。
func TestSettingsTabShowsOwnStatus(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.provider = "ollama"

	// 路径一：键盘切页（从设置页的前一页按 Tab 过来）
	a.setTab(prevTab(tabSettings))
	a.handle(event{kind: evKey, key: keyTab})
	if a.st.tab != tabSettings {
		t.Fatalf("应切到设置页，实际 %v", a.st.tab)
	}
	if !strings.Contains(a.st.status, "ollama") {
		t.Fatalf("设置页底部应显示 provider，实际 %q", a.st.status)
	}

	// 路径二：鼠标点标签，两条路必须一致
	a.setTab(tabChat)
	layout(a, 100, 30)
	x, y := findBox(t, a, "tab:settings")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})
	if a.st.tab != tabSettings {
		t.Fatalf("点击标签应切到设置页，实际 %v", a.st.tab)
	}
	if !strings.Contains(a.st.status, "ollama") {
		t.Fatalf("点击切页后底部也应显示 provider，实际 %q", a.st.status)
	}
}

func TestCtrlCQuits(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	if !a.handle(event{kind: evKey, key: keyCtrlC}) {
		t.Fatal("Ctrl+C 应请求退出")
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

func TestManifestReadFailureIsVisible(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.plugins = []pluginInfo{{ID: "com.broken", ReadErr: "读取失败: access denied"}}
	a.st.tab = tabPlugins
	a.st.selectedPlugin = 0
	layout(a, 120, 34)

	frame := a.canv.frame()
	if !strings.Contains(frame, "读取失败") {
		t.Fatal("读不到 manifest 的插件必须显式提示，不能静静消失")
	}
	if !strings.Contains(frame, "com.broken") {
		t.Fatal("读不到 manifest 也要把目录名显示出来")
	}
}

// ---------------------------------------------------------------------------
// 设置页：粘贴不该被当成导航，输入不该没有落点
// ---------------------------------------------------------------------------

// 没聚焦任何输入框时打字，也要落到 API Key 上 ——
// 用户进设置页就是来粘密钥的，不该因为"没先点一下输入框"而毫无反应。
func TestTypingOnSettingsLandsOnApiKey(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.setTab(tabSettings)
	a.st.focus = ""

	a.handle(event{kind: evKey, key: keyRune, r: 'k'})
	if got := a.fieldValue("apiKey"); got != "k" {
		t.Fatalf("没聚焦时输入应落到 API Key，实际 %q", got)
	}
}

// 粘贴内容里混进制表符/换行时，绝不能触发"切页"或"发送"。
func TestPastedControlCharsDoNotSwitchPages(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.setTab(tabSettings)

	// 用户抱怨的现象：粘一次密钥，界面被弹回聊天页
	a.handle(event{kind: evKey, key: keyRune, r: '\t', paste: true})
	a.handle(event{kind: evKey, key: keyRune, r: '\n', paste: true})
	if a.st.tab != tabSettings {
		t.Fatalf("粘贴里的控制字符不该切页，实际跳到了 %v 页", a.st.tab)
	}

	// 正常字符照样要进输入框
	a.handle(event{kind: evKey, key: keyRune, r: 's', paste: true})
	a.handle(event{kind: evKey, key: keyRune, r: 'k', paste: true})
	if got := a.fieldValue("apiKey"); got != "sk" {
		t.Fatalf("粘贴的字符应进 API Key 框，实际 %q", got)
	}
}

// 正在编辑时 Tab 用来换输入框，而不是切页。
func TestTabMovesFocusWhileEditingSettings(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.setTab(tabSettings)
	if a.st.focus != "apiKey" {
		t.Fatalf("切到设置页应自动聚焦 API Key（省一次点击），实际 %q", a.st.focus)
	}

	a.handle(event{kind: evKey, key: keyTab})
	if a.st.tab != tabSettings {
		t.Fatal("编辑状态下按 Tab 不该切页")
	}
	if a.st.focus == "apiKey" {
		t.Fatal("Tab 应把焦点移到下一个控件")
	}

	// 取消聚焦后，Tab 才恢复成切页
	a.st.focus = ""
	a.handle(event{kind: evKey, key: keyTab})
	if a.st.tab != tabChat {
		t.Fatalf("取消聚焦后 Tab 应切页，实际 %v", a.st.tab)
	}
}

// 点一下服务，地址和模型都要自动填好，焦点直接送到 API Key。
func TestPresetFillsEverythingAndFocusesKey(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.setTab(tabSettings)
	layout(a, 120, 40)

	x, y := findBox(t, a, "preset:deepseek")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: x, Y: y, Press: true}})

	if got := a.fieldValue("baseUrl"); got != "https://api.deepseek.com/v1" {
		t.Fatalf("预设应自动填好地址，实际 %q", got)
	}
	if got := a.fieldValue("model"); got != "deepseek-chat" {
		t.Fatalf("预设应自动填好默认模型，实际 %q", got)
	}
	if a.st.provider != "openai" {
		t.Fatalf("DeepSeek 走 OpenAI 兼容接口，provider 应为 openai，实际 %q", a.st.provider)
	}
	if a.st.focus != "apiKey" {
		t.Fatalf("选完服务焦点应在 API Key 上，实际 %q", a.st.focus)
	}
	if len(a.st.modelList) == 0 {
		t.Fatal("预设应带出可点选的默认模型")
	}
	if !a.st.statusOK || !strings.Contains(a.st.status, "粘贴") {
		t.Fatalf("状态栏应告诉用户下一步是粘贴密钥，实际 %q", a.st.status)
	}

	// 本地 Ollama 不需要密钥，提示要说清楚
	layout(a, 120, 40)
	ox, oy := findBox(t, a, "preset:ollama")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: ox, Y: oy, Press: true}})
	if a.st.provider != "ollama" {
		t.Fatalf("本地预设的 provider 应为 ollama，实际 %q", a.st.provider)
	}
	if !strings.Contains(a.st.status, "不需要密钥") {
		t.Fatalf("应说明 Ollama 不需要密钥，实际 %q", a.st.status)
	}
}

// 「获取可用模型」把服务端返回的清单变成可点选项。
func TestFetchModelsThenPickOne(t *testing.T) {
	fb := &fakeBackend{}
	seed(fb)
	a := newTestApp(fb)
	a.setTab(tabSettings)
	a.st.modelList = nil
	layout(a, 120, 40)

	bx, by := findBox(t, a, "btn:getmodels")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: bx, Y: by, Press: true}})

	var out outcome
	select {
	case out = <-a.results:
	case <-time.After(2 * time.Second):
		t.Fatal("没有等到模型列表结果")
	}
	a.applyOutcome(out)

	fb.mu.Lock()
	calls := fb.modelCalls
	fb.mu.Unlock()
	if calls != 1 {
		t.Fatalf("应调用一次 listModels，实际 %d", calls)
	}
	if len(a.st.modelList) != 2 {
		t.Fatalf("应拿到 2 个模型，实际 %d", len(a.st.modelList))
	}

	layout(a, 120, 40)
	mx, my := findBox(t, a, "model:deepseek-reasoner")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: mx, Y: my, Press: true}})
	if got := a.fieldValue("model"); got != "deepseek-reasoner" {
		t.Fatalf("点模型应写进输入框，实际 %q", got)
	}
}

// 选完服务、粘完密钥、点保存：四项参数要一起送出去。
func TestSaveAfterPresetSendsAllFields(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.setTab(tabSettings)
	a.applyPreset("deepseek")
	a.insertRune('s', true)
	a.insertRune('k', true)
	a.insertRune('-', true)
	a.insertRune('x', true)

	layout(a, 120, 40)
	bx, by := findBox(t, a, "btn:save")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: bx, Y: by, Press: true}})

	waitFor(t, "保存被调用", func() bool {
		n, _, _, _ := fb.snapshot()
		return n == 1
	})
	_, _, _, saved := fb.snapshot()
	want := []string{"openai", "https://api.deepseek.com/v1", "deepseek-chat", "sk-x"}
	for i := range want {
		if saved[i] != want[i] {
			t.Fatalf("第 %d 项应为 %q，实际 %q（全部：%v）", i, want[i], saved[i], saved)
		}
	}
}

// ---------------------------------------------------------------------------
// 括号粘贴：把"粘贴"和"逐键输入"分开
// ---------------------------------------------------------------------------

func TestBracketedPasteParsing(t *testing.T) {
	seq := "\033[200~sk-abc\t\n\033[201~"
	p := &eventParser{}
	var evs []event
	for i := 0; i < len(seq); i++ {
		evs = append(evs, p.feed(seq[i])...)
	}

	if len(evs) != 8 {
		t.Fatalf("sk-abc\\t\\n 应解析出 8 个字符事件，实际 %d：%+v", len(evs), evs)
	}
	for i, ev := range evs {
		if !ev.paste {
			t.Fatalf("第 %d 个事件应带 paste 标记：%+v", i, ev)
		}
	}
	if evs[0].r != 's' || evs[6].r != '\t' || evs[7].r != '\n' {
		t.Fatalf("字符顺序不对：%+v", evs)
	}

	// 真正的 Tab 键必须仍然是 keyTab，否则设置页就没法用 Tab 换输入框了
	p2 := &eventParser{}
	real := p2.feed('\t')
	if len(real) != 1 || real[0].key != keyTab || real[0].paste {
		t.Fatalf("真正的 Tab 应解析为 keyTab，实际 %+v", real)
	}
}

// ---------------------------------------------------------------------------
// 保存密钥：不能"看起来消失了"
// ---------------------------------------------------------------------------

// 读配置回来必须真的写进输入框。
// 曾经这里用 range 遍历切片改字段 —— 改的是元素副本，等于什么都没做。
func TestApplyLLMPopulatesFields(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.applyLLM(llmConfig{
		Provider: "openai",
		BaseURL:  "https://api.deepseek.com/v1",
		Model:    "deepseek-chat",
		APIKey:   "sk-t****cdef",
	}, true)

	if got := a.fieldValue("baseUrl"); got != "https://api.deepseek.com/v1" {
		t.Fatalf("地址没被写进输入框：%q", got)
	}
	if got := a.fieldValue("model"); got != "deepseek-chat" {
		t.Fatalf("模型没被写进输入框：%q", got)
	}
	if f := a.st.field("apiKey"); f == nil || !strings.Contains(f.hint, "sk-t****cdef") {
		t.Fatal("密钥框应提示「已配置（打码）」")
	}
	if a.st.presetKey != "deepseek" {
		t.Fatalf("应按地址还原服务选中状态，实际 %q", a.st.presetKey)
	}
	if len(a.st.modelList) == 0 {
		t.Fatal("应带出该服务可点选的模型")
	}
}

// 后台刷新（启动加载、保存后回读）不许冲掉用户正在输入的内容。
func TestBackgroundRefreshDoesNotClobberTyping(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.setTab(tabSettings)
	a.insertRune('s', false)
	a.insertRune('k', false)

	a.applyLLM(llmConfig{
		Provider: "openai",
		BaseURL:  "https://api.openai.com/v1",
		Model:    "gpt-4o-mini",
		APIKey:   "sk-o****1234",
	}, true)

	if got := a.fieldValue("apiKey"); got != "sk" {
		t.Fatalf("后台刷新把正在输入的密钥冲掉了：%q", got)
	}
}

// 保存失败时，密钥必须留在输入框里 —— 否则用户要重新粘一遍。
func TestSaveFailureKeepsEnteredKey(t *testing.T) {
	fb := &fakeBackend{saveEr: fmt.Errorf("network down")}
	a := newTestApp(fb)
	a.setTab(tabSettings)
	a.applyPreset("deepseek")
	for _, r := range "sk-abc" {
		a.insertRune(r, false)
	}
	layout(a, 120, 40)

	bx, by := findBox(t, a, "btn:save")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: bx, Y: by, Press: true}})

	var res outcome
	select {
	case res = <-a.results:
	case <-time.After(2 * time.Second):
		t.Fatal("没等到保存结果")
	}
	a.applyOutcome(res)

	if got := a.fieldValue("apiKey"); got != "sk-abc" {
		t.Fatalf("保存失败时密钥应留着让用户重试，实际 %q", got)
	}
	if a.st.statusOK {
		t.Fatal("保存失败时状态栏不该显示成功")
	}
}

// 保存成功：清掉明文，但必须留下「已配置 ****」的确认，并把焦点移开。
// 用户报的就是这条：点了保存，输入框空了，看不出到底存没存上。
func TestSaveSuccessClearsKeyButShowsConfirmation(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.setTab(tabSettings)
	a.applyPreset("deepseek")
	for _, r := range "sk-abcdef1234" {
		a.insertRune(r, false)
	}
	layout(a, 120, 40)

	bx, by := findBox(t, a, "btn:save")
	a.handle(event{kind: evMouse, mouse: mouseEvent{X: bx, Y: by, Press: true}})

	var res outcome
	select {
	case res = <-a.results:
	case <-time.After(2 * time.Second):
		t.Fatal("没等到保存结果")
	}
	if res.kind != "saved" {
		t.Fatalf("第一个结果应是 saved，实际 %q", res.kind)
	}
	a.applyOutcome(res)

	f := a.st.field("apiKey")
	if f == nil {
		t.Fatal("找不到密钥字段")
	}
	if f.value != "" {
		t.Fatalf("保存成功后不该继续显示明文，实际 %q", f.value)
	}
	if !strings.Contains(f.hint, "sk-a****1234") {
		t.Fatalf("应留下打码确认，实际 %q", f.hint)
	}
	if a.st.focus == "apiKey" {
		t.Fatal("焦点应移开，否则用户盯着空框以为白填了")
	}
	if !a.st.statusOK {
		t.Fatalf("状态栏应显示保存成功，实际 %q", a.st.status)
	}

	// 关键：这个提示必须真的能看见
	layout(a, 120, 40)
	if !strings.Contains(a.canv.frame(), "sk-a****1234") {
		t.Fatal("打码确认没有渲染到界面上")
	}
}

// 保存后的配置回读不许顶掉保存确认 —— 否则用户永远看不到「已写入」。
func TestQuietReloadKeepsSaveConfirmation(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.setTab(tabSettings)

	a.applyOutcome(outcome{
		kind:   "saved",
		note:   "已保存 4 项配置 · 密钥 sk-a****1234 已写入，界面不再显示明文",
		apiKey: "sk-abcdef1234",
	})
	if !strings.Contains(a.st.status, "已写入") {
		t.Fatalf("保存后应显示写入确认，实际 %q", a.st.status)
	}

	// 紧接着的回读（quiet=true）：不许覆盖
	a.applyOutcome(outcome{kind: "llm", cfg: &llmConfig{Provider: "openai"}, quiet: true})
	if !strings.Contains(a.st.status, "已写入") {
		t.Fatalf("回读把写入确认顶掉了，实际 %q", a.st.status)
	}

	// 启动时的加载（quiet=false）才更新状态栏
	a.applyOutcome(outcome{kind: "llm", cfg: &llmConfig{Provider: "ollama"}})
	if !strings.Contains(a.st.status, "ollama") {
		t.Fatalf("启动时的加载应更新状态栏，实际 %q", a.st.status)
	}
}

// 空的密钥框即使在聚焦状态也要显示提示。
func TestEmptyKeyFieldShowsHintEvenWhenFocused(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.fields[2].hint = "已配置 sk-a****1234（留空=不改动）"
	a.setTab(tabSettings) // 焦点落在 apiKey
	layout(a, 120, 40)

	if !strings.Contains(a.canv.frame(), "sk-a****1234") {
		t.Fatal("聚焦的空输入框也要显示「已配置」提示")
	}
}

// ---------------------------------------------------------------------------
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
	a.handle(event{kind: evKey, key: keyEnter})
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
	a.handle(event{kind: evKey, key: keyEnter})
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
	a.handle(event{kind: evKey, key: keyEnter})
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
	a.handle(event{kind: evKey, key: keyEnter})
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
	a.handle(event{kind: evKey, key: keyEnter})
	drain(t, a)
	first := a.st.sessID

	a.newSession()
	drain(t, a)
	typeString(a, "第二条会话")
	a.handle(event{kind: evKey, key: keyEnter})
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
	a.handle(event{kind: evKey, key: keyEnter})
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
	a.handle(event{kind: evKey, key: keyEnter})
	waitFor(t, "走降级链路", func() bool {
		_, _, chats, _ := fb.snapshot()
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
		a.st.wsPath = "/tmp/proj"
		a.st.wsParent = "/tmp"
		a.st.wsEntries = []dirEntry{{Name: "src", Dir: true}, {Name: "README.md", Size: 1024}}
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

		// 选中插件才有操作行
		a.st.tab = tabPlugins
		a.selectPlugin(0)
		layout(a, sz[0], sz[1])
		assertHitboxesAligned(t, a, where+" 插件详情")

		// 删除的二次确认：确认与取消是两个相邻按钮
		a.st.confirmDelete = true
		layout(a, sz[0], sz[1])
		assertHitboxesAligned(t, a, where+" 删除确认")

		// 分享面板的三个按钮
		a.st.confirmDelete = false
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
