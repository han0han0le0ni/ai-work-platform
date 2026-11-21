package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// fakeAPI 顶替核心：目录树、文件读写、事件都记下来。
//
// 界面逻辑（点了什么会怎样、画成什么样）不该为了测一下就去连真磁盘 ——
// 这也是把 managerAPI 抽出来的原因。
// ---------------------------------------------------------------------------

type fakeAPI struct {
	root   string             // core.fs.list "." 解析出来的基准目录
	tree   map[string][]entry // 路径 -> 目录条目
	files  map[string]string  // 路径 -> 文件内容
	gone   []string           // 删掉的路径
	moved  [][2]string        // 改过的名
	events []string
	live   map[string]bool // 服务名 -> 当前有提供者
	perms  map[string]bool

	unloads       []string // core.unload 收到的插件 id
	unloadStopped bool     // 假 unload 是否报告"已停"
}

func (f *fakeAPI) ID() string { return "com.official.plugin-manager" }

func (f *fakeAPI) Can(p string) bool { return f.perms[p] }

func (f *fakeAPI) Consume(name string) (string, error) {
	if f.live[name] {
		return "someone", nil
	}
	return "", fmt.Errorf("服务未找到: %s", name)
}

func (f *fakeAPI) Invoke(service, method string, args any) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func (f *fakeAPI) Emit(event string, payload any) error {
	f.events = append(f.events, event)
	return nil
}

func (f *fakeAPI) Log(level, msg string, meta map[string]any) {}

func (f *fakeAPI) Capability(method string, params any) (json.RawMessage, error) {
	if method == "core.unload" {
		b, _ := json.Marshal(params)
		var in struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(b, &in)
		f.unloads = append(f.unloads, in.ID)
		resp, _ := json.Marshal(map[string]any{"ok": true, "id": in.ID, "stopped": f.unloadStopped})
		return resp, nil
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakeAPI) FSList(path string) (json.RawMessage, error) {
	if path == "." {
		return json.Marshal(map[string]any{"path": f.root, "entries": []entry{}})
	}
	ents, ok := f.tree[path]
	if !ok {
		return nil, fmt.Errorf("没有这个目录: %s", path)
	}
	return json.Marshal(map[string]any{"path": path, "entries": ents})
}

func (f *fakeAPI) FSRead(path string) (json.RawMessage, error) {
	s, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("没有这个文件: %s", path)
	}
	return json.Marshal(map[string]any{"path": path, "content": s})
}

func (f *fakeAPI) FSWrite(path, content string, appendMode, mkdir bool) (json.RawMessage, error) {
	if f.files == nil {
		f.files = map[string]string{}
	}
	f.files[path] = content
	return json.RawMessage(`{"ok":true}`), nil
}

// FSDelete 真的从父目录的清单里摘掉它：删除之后 exists() 必须报"不在了"，
// 否则这一页会以为插件目录还在，把一次干净删除说成"被占用、下次启动再删"。
func (f *fakeAPI) FSDelete(path string, recursive bool) (json.RawMessage, error) {
	f.gone = append(f.gone, path)
	delete(f.files, path)
	delete(f.tree, path)
	f.dropFromParent(path)
	return json.Marshal(map[string]any{"path": path, "existed": true, "deleted": true})
}

func (f *fakeAPI) FSMove(from, to string) (json.RawMessage, error) {
	f.moved = append(f.moved, [2]string{from, to})
	if ents, ok := f.tree[from]; ok {
		f.tree[to] = ents
		delete(f.tree, from)
	}
	dir, base := filepath.Dir(from), filepath.Base(from)
	if ents, ok := f.tree[dir]; ok {
		for i := range ents {
			if ents[i].Name == base {
				ents[i].Name = filepath.Base(to)
			}
		}
		f.tree[dir] = ents
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (f *fakeAPI) FSMkdir(path string, recursive bool) (json.RawMessage, error) {
	if f.tree == nil {
		f.tree = map[string][]entry{}
	}
	if _, ok := f.tree[path]; !ok {
		f.tree[path] = []entry{}
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (f *fakeAPI) dropFromParent(path string) {
	dir, base := filepath.Dir(path), filepath.Base(path)
	ents, ok := f.tree[dir]
	if !ok {
		return
	}
	out := make([]entry, 0, len(ents))
	for _, e := range ents {
		if e.Name != base {
			out = append(out, e)
		}
	}
	f.tree[dir] = out
}

// ---------------------------------------------------------------------------
// 夹具：一个装着四个插件的运行时目录
// ---------------------------------------------------------------------------

func seedManager(t *testing.T) (*Manager, *fakeAPI) {
	t.Helper()
	root := "testroot"
	pj := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }

	api := &fakeAPI{
		root:  root,
		tree:  map[string][]entry{},
		files: map[string]string{},
		live:  map[string]bool{"ui.v1": true, "pluginManager.v1": true},
		perms: map[string]bool{"filesystem:delete": true},
	}
	api.tree[root] = []entry{
		{Name: "plugins", Dir: true}, {Name: "plugin-configs", Dir: true}, {Name: "logs", Dir: true},
	}
	api.tree[pj("plugin-configs")] = []entry{}
	api.tree[pj("logs")] = []entry{}
	api.tree[pj("plugins")] = []entry{
		{Name: "workbench.plugin", Dir: true},
		{Name: "pm.plugin", Dir: true},
		{Name: "demo.plugin", Dir: true},
		{Name: "old.plugin.disabled", Dir: true},
		{Name: "broken.plugin", Dir: true},
	}
	man := func(m map[string]any) string {
		blob, _ := json.Marshal(m)
		return string(blob)
	}
	api.files[pj("plugins", "workbench.plugin", "manifest.json")] = man(map[string]any{
		"id": "com.official.workbench", "name": "Workbench", "version": "1.1.0",
		"runtime": "binary", "entry": "main", "provides": []string{"ui.v1"},
		"permissions": []string{"filesystem:read"}, "compat": map[string]string{"core": ">=1.0 <2.0"},
		"description": "可点击的工作台界面",
	})
	api.files[pj("plugins", "pm.plugin", "manifest.json")] = man(map[string]any{
		"id": "com.official.plugin-manager", "name": "Plugin Manager", "version": "1.1.0",
		"runtime": "binary", "entry": "main", "provides": []string{"pluginManager.v1"},
	})
	api.files[pj("plugins", "demo.plugin", "manifest.json")] = man(map[string]any{
		"id": "com.official.demo", "name": "Demo", "version": "1.0.0",
		"runtime": "binary", "entry": "main",
		"provides": []string{"demo.v1"}, "consumes": []string{"ui.v1"},
	})
	api.files[pj("plugins", "old.plugin.disabled", "manifest.json")] = man(map[string]any{
		"id": "com.official.old", "name": "Old", "version": "0.9.0",
		"runtime": "binary", "entry": "main", "provides": []string{"old.v1"},
	})
	// broken.plugin 故意不放 manifest.json：读不出归属的那种

	return &Manager{api: api, root: root}, api
}

func pmRender(m *Manager, w, h, scroll int) plugin.PanelRender {
	return m.panel.render(m, plugin.PanelRequest{Panel: panelID, W: w, H: h, Scroll: scroll})
}

// pmPick 选中某一个插件（按 ID 或目录名），返回它的下标。
//
// 不写死下标：清单顺序是插件管理器排的（按 ID、读不出 manifest 的排最前），
// 用例不该把"第几个是谁"当成契约。
func pmPick(t *testing.T, m *Manager, id string) int {
	t.Helper()
	for i, it := range m.panel.items {
		if it.info.ID == id || it.info.Dir == id {
			m.panel.action(m, plugin.PanelRequest{Panel: panelID, Action: "pick", Arg: fmt.Sprint(i)})
			return i
		}
	}
	t.Fatalf("清单里没有 %q：%+v", id, m.panel.items)
	return -1
}

func pmText(res plugin.PanelRender) string { return plainOf(strings.Join(res.Lines, "\n")) }

// plainOf 去掉样式码，只留看得见的字（宽度要按可见内容算）。
func plainOf(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 2
			for j < len(s) && s[j] < 0x40 {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 用例
// ---------------------------------------------------------------------------

// 每一行都不能超过宿主给的宽度：超一列，人家右边的竖线就歪了。
func TestPanelRenderFitsExactly(t *testing.T) {
	m, _ := seedManager(t)
	for _, sz := range [][2]int{{46, 10}, {60, 20}, {100, 30}, {84, 24}, {160, 40}} {
		res := pmRender(m, sz[0], sz[1], 0)
		if len(res.Lines) != sz[1] {
			t.Fatalf("%dx%d：应有 %d 行，实际 %d", sz[0], sz[1], sz[1], len(res.Lines))
		}
		for i, ln := range res.Lines {
			if got := plugin.DisplayWidth(plainOf(ln)); got > sz[0] {
				t.Fatalf("%dx%d：第 %d 行宽 %d，超过 %d：%q", sz[0], sz[1], i, got, sz[0], ln)
			}
		}
		for _, hit := range res.Hits {
			if hit.Y < 0 || hit.Y >= sz[1] || hit.X < 0 || hit.X+hit.W > sz[0] {
				t.Fatalf("%dx%d：热区越界 %+v", sz[0], sz[1], hit)
			}
		}
		if !res.WantKeys {
			t.Fatal("这一页要能上下选、回车按，必须声明要键盘")
		}
	}
}

// 窗口太小时要认怂，而不是画一屏垃圾。
func TestPanelRenderTooSmall(t *testing.T) {
	m, _ := seedManager(t)
	res := pmRender(m, 20, 4, 0)
	if len(res.Lines) != 4 {
		t.Fatalf("行数应等于高度，实际 %d", len(res.Lines))
	}
	if !strings.Contains(pmText(res), "太小") {
		t.Fatalf("应说明窗口放不下：%v", res.Lines)
	}
}

// 清单一上来就要说清楚：装了几个、运行几个、各自什么状态。
func TestPanelRenderListsPlugins(t *testing.T) {
	m, _ := seedManager(t)
	res := pmRender(m, 100, 30, 0)
	text := pmText(res)

	for _, want := range []string{
		"插件 5 · 运行 2", // workbench 在跑；pm 自己没声明服务；demo 没在跑；old 禁用；broken 读不出
		"com.official.workbench", "v1.1.0",
		"com.official.demo", "v1.0.0",
		"com.official.old", "（禁用）",
		"broken.plugin", "（读不出）",
		"详情",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("画面里应出现 %q：\n%s", want, text)
		}
	}
}

// 详情要摊开 manifest 里真正有用的东西：入口、权限、提供/消费、兼容范围。
func TestPanelDetailShowsManifest(t *testing.T) {
	m, api := seedManager(t)
	pmRender(m, 100, 30, 0)
	pmPick(t, m, "com.official.workbench")
	res := pmRender(m, 100, 30, 0)
	text := pmText(res)

	for _, want := range []string{
		"目录   workbench.plugin", "版本   v1.1.0", "入口   main",
		"提供的服务", "✓ ui.v1", "权限", "filesystem:read", "兼容核心 >=1.0 <2.0",
		"可点击的工作台界面",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("详情里应出现 %q：\n%s", want, text)
		}
	}

	// 服务没人在提供时要标 ✗，而不是含糊地说"运行中"
	api.live = map[string]bool{}
	m.panel.action(m, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "reload"})
	res = pmRender(m, 100, 30, 0)
	if !strings.Contains(pmText(res), "✗ ui.v1") {
		t.Fatalf("没人提供时应标 ✗：\n%s", pmText(res))
	}
}

// 宽屏分两栏（有竖线分隔），窄屏一列 —— 两栏各自都放不下东西时，
// 硬分只会让两边都看不清。
func TestPanelWideIsTwoColumn(t *testing.T) {
	m, _ := seedManager(t)
	wide := pmText(pmRender(m, 100, 30, 0))
	narrow := pmText(pmRender(m, 60, 30, 0))
	if !strings.Contains(wide, "│") {
		t.Fatalf("宽屏应有分栏竖线：\n%s", wide)
	}
	if strings.Contains(narrow, "│") {
		t.Fatalf("窄屏不该分栏：\n%s", narrow)
	}
}

// 宽屏两栏并排时，详情区的热区不能还落在左栏列表的列上。
//
// 这是用户报过的真实 bug：detailLines 里的按钮按「从第 0 列开始」登记绝对坐标，
// 两栏并排后文本是顺序追加的所以位置对，热区却还留在列表那一列 —— 禁用/启用、
// 删除按钮"看得见、点不中"。
func TestPanelWideDetailHitsAreShifted(t *testing.T) {
	m, _ := seedManager(t)
	pmRender(m, 100, 30, 0)
	pmPick(t, m, "com.official.demo") // demo 当前启用，详情里会有「禁用」按钮
	res := pmRender(m, 100, 30, 0)

	rightStart := pmListW + 2

	foundToggle := false
	for i := range res.Hits {
		h := &res.Hits[i]
		switch {
		case h.Action == "btn" && h.Arg == "toggle":
			foundToggle = true
			if h.X < rightStart {
				t.Fatalf("详情区「禁用/启用」热区 x=%d 还落在列表（应 >= %d）", h.X, rightStart)
			}
		case h.Action == "pick":
			// 列表的 pick 热区必须整个待在左栏，不许越到详情
			if h.X+h.W > pmListW {
				t.Fatalf("列表 pick 热区越界到详情：%+v", h)
			}
		}
	}
	if !foundToggle {
		t.Fatal("应有「禁用/启用」按钮的热区")
	}
}

// 读不出 manifest 的目录：如实说、并且不给启停与删除 —— 认不出归属就不该动它。
func TestPanelUnreadableIsRefused(t *testing.T) {
	m, api := seedManager(t)
	pmRender(m, 100, 30, 0)
	pmPick(t, m, "broken.plugin")

	res := pmRender(m, 100, 30, 0)
	text := pmText(res)
	if !strings.Contains(text, "认不出它归属谁") {
		t.Fatalf("应说明为什么不能动它：\n%s", text)
	}
	// 断言"按钮不存在"，而不是断言文字：说明里那句"拒绝启停与删除"本身也含这几个字
	for _, h := range res.Hits {
		if h.Action == "btn" && (h.Arg == "toggle" || h.Arg == "del") {
			t.Fatalf("读不出 manifest 的目录不该有 %s 入口：%+v", h.Arg, res.Hits)
		}
	}
	// 就算动作照样发过来（比如手快点到别处），业务那一层也必须挡住：
	// 界面不给入口是第一道，scan/setEnabled 自己的校验是第二道。
	note := m.panel.action(m, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "toggle"})
	if !strings.Contains(note, "拒绝启停") {
		t.Fatalf("应如实说明被拒绝的原因，实际 %q", note)
	}
	if len(api.moved) != 0 {
		t.Fatalf("不该发生任何改名，实际 %v", api.moved)
	}
}

// 插件管理器自己：不能停用也不能删除 —— 删掉之后没有东西能把它装回来。
func TestPanelSelfIsProtected(t *testing.T) {
	m, _ := seedManager(t)
	pmRender(m, 100, 30, 0)
	pmPick(t, m, "com.official.plugin-manager")

	text := pmText(pmRender(m, 100, 30, 0))
	if !strings.Contains(text, "不能停用，也不能删除") {
		t.Fatalf("应说明它是受保护的：\n%s", text)
	}
	if strings.Contains(text, "删除插件") {
		t.Fatalf("不该给出删除入口：\n%s", text)
	}
}

// 启停 = 改目录名的后缀（核心只加载 *.plugin），并且要通知宿主重画。
func TestPanelToggleRenamesDirectory(t *testing.T) {
	m, api := seedManager(t)
	pmRender(m, 100, 30, 0)
	api.events = nil

	pmPick(t, m, "com.official.demo")
	note := m.panel.action(m, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "toggle"})

	if len(api.moved) != 1 {
		t.Fatalf("应发生一次改名，实际 %v", api.moved)
	}
	if got := filepath.Base(api.moved[0][1]); got != "demo.plugin.disabled" {
		t.Fatalf("禁用后目录名应带 .disabled，实际 %q", got)
	}
	if !strings.Contains(note, "禁用") {
		t.Fatalf("应回一句说明，实际 %q", note)
	}
	if !hasEvent(api.events, panelChangedEvent) {
		t.Fatal("状态变了必须通知宿主重画，否则界面停在旧清单上")
	}
	// 重画之后这一份应标成禁用态
	if !strings.Contains(pmText(pmRender(m, 100, 30, 0)), "（禁用）") {
		t.Fatalf("重画后应看到禁用标记：\n%s", pmText(pmRender(m, 100, 30, 0)))
	}
}

// 删除不可逆：第一次点只进确认态，并且把"会删掉哪三个文件、谁在依赖它"摊开。
func TestPanelDeleteShowsPlanThenRemoves(t *testing.T) {
	m, api := seedManager(t)
	pmRender(m, 100, 30, 0)

	// 选中 workbench：demo 在消费 ui.v1，删它应该给出警告
	pmPick(t, m, "com.official.workbench")

	note := m.panel.action(m, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "del"})
	if !m.panel.confirmDel {
		t.Fatal("第一次点应进入确认态")
	}
	if !strings.Contains(note, "再点一次") {
		t.Fatalf("应说明还要再点一次，实际 %q", note)
	}
	text := pmText(pmRender(m, 100, 30, 0))
	for _, want := range []string{"将删除", "插件目录", "配置", "日志", "ui.v1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("确认态应摊开 %q：\n%s", want, text)
		}
	}
	if len(api.gone) != 0 {
		t.Fatal("第一次点不该真的删")
	}

	m.panel.action(m, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "del"})
	if len(api.gone) != 3 {
		t.Fatalf("应删掉目录 + 配置 + 日志三项，实际 %v", api.gone)
	}
	for _, it := range m.panel.items {
		if it.info.ID == "com.official.workbench" {
			t.Fatalf("删完清单里不该还有它：%+v", m.panel.items)
		}
	}
}

// Esc 撤销删除确认 —— 不可逆的操作必须留一条反悔的路。
func TestPanelDeleteConfirmCanBeCancelled(t *testing.T) {
	m, api := seedManager(t)
	pmRender(m, 100, 30, 0)
	m.panel.action(m, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "del"})
	if !m.panel.confirmDel {
		t.Fatal("应进入确认态")
	}
	m.panel.key(m, "esc")
	if m.panel.confirmDel {
		t.Fatal("Esc 应取消确认")
	}
	if len(api.gone) != 0 {
		t.Fatal("取消之后不该有任何删除")
	}
}

// 「重新扫描」：外部往 plugins/ 里放了一个新插件，点一下就该看见。
func TestPanelReloadPicksUpNewPlugin(t *testing.T) {
	m, api := seedManager(t)
	pmRender(m, 100, 30, 0)
	before := len(m.panel.items)

	blob, _ := json.Marshal(map[string]any{
		"id": "com.official.fresh", "name": "Fresh", "version": "2.0.0",
		"runtime": "binary", "entry": "main",
	})
	api.files[filepath.Join(m.root, "plugins", "fresh.plugin", "manifest.json")] = string(blob)
	api.tree[filepath.Join(m.root, "plugins")] = append(
		api.tree[filepath.Join(m.root, "plugins")], entry{Name: "fresh.plugin", Dir: true})

	note := m.panel.action(m, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "reload"})
	if len(m.panel.items) != before+1 {
		t.Fatalf("重新扫描应看到新插件，实际 %d → %d", before, len(m.panel.items))
	}
	if !strings.Contains(pmText(pmRender(m, 100, 30, 0)), "com.official.fresh") {
		t.Fatalf("新插件应出现在清单里：\n%s", pmText(pmRender(m, 100, 30, 0)))
	}
	if !strings.Contains(note, "已重新扫描") {
		t.Fatalf("应回一句确认，实际 %q", note)
	}
}

// 上下键选插件、回车落到按钮上；焦点跑出可视区时自己把详情滚过去。
func TestPanelKeysAndFocusScroll(t *testing.T) {
	m, _ := seedManager(t)
	pmRender(m, 60, 12, 0) // 矮窗口：详情装不下，必然要滚

	// 一路下键：焦点先过顶栏两个按钮才进列表，落到列表项上就顺手选中它
	steps := 0
	for m.panel.focus != m.panel.ringList(1) && steps < m.panel.ringLen()+2 {
		m.panel.key(m, "down")
		steps++
	}
	if m.panel.focus != m.panel.ringList(1) {
		t.Fatalf("一路下键应能走到第 2 个插件，实际焦点 %d", m.panel.focus)
	}
	if m.panel.sel != 1 {
		t.Fatalf("焦点落到列表项上时应当选中它，实际 %d", m.panel.sel)
	}
	m.panel.key(m, "up")
	if m.panel.sel != 0 {
		t.Fatalf("上键应回到第 1 个插件，实际 %d", m.panel.sel)
	}

	// 焦点落在"详情最下面那个按钮"上时，滚动位置必须跟过去 ——
	// 只有这一页自己知道哪个控件画在第几行，宿主替不了它。
	//
	// 直接设焦点而不是一路按下去：那样子会经过列表、顺手换好几个插件，
	// 换到最后一个是"读不出 manifest"的那份，而它压根没有删除按钮。
	pmPick(t, m, "com.official.demo")
	m.panel.setFocus(m.panel.ringDel())
	res := pmRender(m, 60, 12, 0)
	if res.Scroll == 0 {
		t.Fatalf("焦点在详情底部时滚动位置应跟过去，实际 %d", res.Scroll)
	}
	row, ok := m.panel.rowOf[m.panel.ringDel()]
	if !ok {
		t.Fatalf("删除按钮应当有行号记录：%v", m.panel.rowOf)
	}
	if row < res.Scroll || row >= res.Scroll+(12-2) {
		t.Fatalf("焦点行 %d 不在可视区 [%d,%d)", row, res.Scroll, res.Scroll+10)
	}
}

// 一个插件报的页面清单：插件 + 规则两页，id 稳定、标题给人看。
func TestPanelList(t *testing.T) {
	var m Manager
	list := m.panel.list()
	if len(list) != 2 {
		t.Fatalf("应有「插件」「规则」两页：%+v", list)
	}
	if list[0].ID != panelID || list[0].Title == "" {
		t.Fatalf("第一页要有稳定的 id 和标题：%+v", list[0])
	}
	if list[1].ID != rulesPanelID || list[1].Title == "" {
		t.Fatalf("第二页要有稳定的 id 和标题：%+v", list[1])
	}
	if !strings.HasPrefix(panelService, "ui.page.") {
		t.Fatalf("服务名要守约定（ui.page.<名字>.v1），实际 %q", panelService)
	}
}

func hasEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

// 删除一个没在运行的插件：目录、配置、日志当场删干净，就不该再留一条
// "重启时白删一遍"的待重启记录 —— 那是垃圾。
func TestRemoveCleansUpWithoutPendingWhenDone(t *testing.T) {
	m, api := seedManager(t)

	// demo 没在运行（live 里没有 demo.v1），目录删得掉
	out, err := m.remove(json.RawMessage(`{"id":"com.official.demo"}`))
	if err != nil {
		t.Fatalf("删除应成功：%v", err)
	}
	res, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("返回值应是对象：%T", out)
	}
	if res["status"] != "removed" {
		t.Fatalf("应是一次干净删除，实际 %v", res["status"])
	}
	if _, exists := api.files[filepath.Join("testroot", "pending-restart.json")]; exists {
		t.Fatal("删干净了就不该写 pending-restart.json")
	}
}

// 删除一个**正在运行**的插件：先经 core.unload 热卸载停掉它的进程，目录当场
// 删干净，不必再标"待重启删除"。这是规范 2.6 的例外 —— 卸载比加载安全。
func TestRemoveUnloadsRunningPlugin(t *testing.T) {
	m, api := seedManager(t)
	api.live["demo.v1"] = true // demo 正在运行
	api.unloadStopped = true   // 核心报告"已停掉进程"

	out, err := m.remove(json.RawMessage(`{"id":"com.official.demo"}`))
	if err != nil {
		t.Fatalf("删除应成功：%v", err)
	}

	// 先热卸载
	if len(api.unloads) != 1 || api.unloads[0] != "com.official.demo" {
		t.Fatalf("删除运行中的插件应先调 core.unload，实际 %v", api.unloads)
	}
	res := out.(map[string]any)
	if res["status"] != "removed" {
		t.Fatalf("热卸载后应是干净删除，实际 %v", res["status"])
	}
	if res["restartRequired"] != false {
		t.Fatalf("热卸载成功后不用重启，实际 %v", res["restartRequired"])
	}
	if _, exists := api.files[filepath.Join("testroot", "pending-restart.json")]; exists {
		t.Fatal("删干净了就不该写 pending-restart.json")
	}
}

// 界面插件**不能**热卸载：它的进程就是用户正在看的界面，当场杀掉的话用户连
// 删除结果都看不到（界面直接没了）。删除界面插件退回重启生效。
func TestRemoveSkipsUnloadForUIPlugin(t *testing.T) {
	m, api := seedManager(t)
	api.live["ui.v1"] = true // 界面正在运行
	api.unloadStopped = true // 就算核心能停，也不该去停它

	if _, err := m.remove(json.RawMessage(`{"id":"com.official.workbench"}`)); err != nil {
		t.Fatalf("删除应成功：%v", err)
	}
	if len(api.unloads) != 0 {
		t.Fatalf("界面插件不该热卸载，实际调了 %v", api.unloads)
	}
}

// 重复删除同一个插件，pending-restart.json 里不该堆重复记录。
func TestMarkPendingDedupes(t *testing.T) {
	m, api := seedManager(t)
	if err := m.markPending("delete", "com.official.demo", "demo.plugin"); err != nil {
		t.Fatalf("第一次记应成功：%v", err)
	}
	if err := m.markPending("delete", "com.official.demo", "demo.plugin"); err != nil {
		t.Fatalf("重复记应被去重而不是报错：%v", err)
	}

	blob := api.files[filepath.Join("testroot", "pending-restart.json")]
	var list []pendingChange
	if err := json.Unmarshal([]byte(blob), &list); err != nil {
		t.Fatalf("pending-restart.json 应可解析：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("重复删除应只有一条记录，实际 %d", len(list))
	}
}

// 「规则」页是静态文档：渲染符合尺寸契约，内容覆盖核心校验的那几类命名规范。
func TestRulesPageRenders(t *testing.T) {
	res := renderRules(plugin.PanelRequest{Panel: rulesPanelID, W: 70, H: 20, Scroll: 0})
	if len(res.Lines) != 20 {
		t.Fatalf("应有 20 行，实际 %d", len(res.Lines))
	}
	for i, ln := range res.Lines {
		if got := plugin.DisplayWidth(plainOf(ln)); got > 70 {
			t.Fatalf("第 %d 行宽 %d 超 70：%q", i, got, ln)
		}
	}

	// 内容按**完整文档**查（不经过渲染的窗口截断）：核心校验的那几类规范都得在
	var all []string
	for _, r := range rulesRows() {
		all = append(all, r.text)
	}
	text := strings.Join(all, "\n")
	for _, want := range []string{"插件规则", "服务名", "事件名", "权限名", "manifest", "ui.page", ".v1", "反向域名"} {
		if !strings.Contains(text, want) {
			t.Fatalf("规则页应提到 %q：\n%s", want, text)
		}
	}

	// 滚动到底：位置要夹在 MaxScroll 内，不能越界
	res = renderRules(plugin.PanelRequest{Panel: rulesPanelID, W: 70, H: 10, Scroll: 1000})
	if res.Scroll > res.MaxScroll {
		t.Fatalf("滚动位置应被夹在 MaxScroll 内：scroll=%d max=%d", res.Scroll, res.MaxScroll)
	}
}
