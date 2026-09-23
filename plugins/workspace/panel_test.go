package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// fakeAPI 顶替核心的能力代理：列目录、存配置、发事件都记下来。
//
// 这一页的逻辑（点了什么会怎样、画成什么样）不该为了测一下就去连真磁盘，
// 所以把插件需要的那几样能力换成假的 —— 这也是把 panelCtx 抽出来的原因。
// ---------------------------------------------------------------------------

type fakeAPI struct {
	dirs   map[string][]item // 路径 -> 条目
	cfg    map[string]any
	events []string
	err    error
}

func (f *fakeAPI) FSList(path string) (json.RawMessage, error) {
	if f.err != nil {
		return nil, f.err
	}
	items, ok := f.dirs[path]
	if !ok {
		return nil, fmt.Errorf("没有这个目录: %s", path)
	}
	ents := make([]map[string]any, 0, len(items))
	for _, it := range items {
		ents = append(ents, map[string]any{"name": it.Name, "dir": it.Dir, "size": it.Size})
	}
	return json.Marshal(map[string]any{"path": path, "entries": ents})
}

func (f *fakeAPI) SetConfig(key string, value any) error {
	if f.cfg == nil {
		f.cfg = map[string]any{}
	}
	f.cfg[key] = value
	return nil
}

func (f *fakeAPI) Emit(event string, payload any) error {
	f.events = append(f.events, event)
	return nil
}

func (f *fakeAPI) Log(level, msg string, meta map[string]any) {}

// seedPanel 造一个"已经挂在 E:\proj 上"的插件，目录里有两个子目录和一个文件。
func seedPanel(t *testing.T) (*Workspace, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{dirs: map[string][]item{
		".": {
			{Name: "sub", Dir: true},
			{Name: "src", Dir: true},
			{Name: "README.md", Size: 2048},
		},
		"sub": {
			{Name: "a.txt", Size: 11},
		},
	}}
	w := &Workspace{api: api}
	w.panel.field.SetValue(".")
	return w, api
}

// 每一行都不能超过宿主给的宽度：超一列，人家右边的竖线就歪了。
// 这是"插件自带界面"最要紧的一条不变量 —— 它由 SDK 的 Block 保证。
func TestPanelRenderFitsExactly(t *testing.T) {
	w, _ := seedPanel(t)
	for _, sz := range [][2]int{{40, 10}, {60, 16}, {100, 30}, {24, 7}} {
		got := w.panel.render(w, panelRequest{W: sz[0], H: sz[1]})
		if len(got.Lines) != sz[1] {
			t.Fatalf("%dx%d：应有 %d 行，实际 %d", sz[0], sz[1], sz[1], len(got.Lines))
		}
		for i, ln := range got.Lines {
			if w := plugin.DisplayWidth(plainOf(ln)); w > sz[0] {
				t.Fatalf("%dx%d：第 %d 行宽 %d，超过 %d：%q", sz[0], sz[1], i, w, sz[0], ln)
			}
		}
		for _, h := range got.Hits {
			if h.Y < 0 || h.Y >= sz[1] || h.X < 0 || h.X+h.W > sz[0] {
				t.Fatalf("%dx%d：热区越界 %+v", sz[0], sz[1], h)
			}
		}
	}
}

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
				j++ // 跳过终止字节（m / H 之类），否则它会被当成可见字符
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// 窗口太小时要认怂，而不是画一屏垃圾。
func TestPanelRenderTooSmall(t *testing.T) {
	w, _ := seedPanel(t)
	got := w.panel.render(w, panelRequest{W: 10, H: 3})
	if len(got.Lines) != 3 {
		t.Fatalf("行数应等于高度，实际 %d", len(got.Lines))
	}
	if !strings.Contains(plainOf(strings.Join(got.Lines, "\n")), "太小") {
		t.Fatalf("应说明窗口放不下：%v", got.Lines)
	}
}

// 一页上去先读一次目录，标题、当前工作目录、条目都要画出来。
func TestPanelRenderReadsAndShowsEntries(t *testing.T) {
	w, _ := seedPanel(t)
	w.root = "E:\\proj"
	got := w.panel.render(w, panelRequest{W: 80, H: 20})
	text := plainOf(strings.Join(got.Lines, "\n"))

	for _, want := range []string{"工作区文件", "E:\\proj", "sub", "src", "README.md", "3 项"} {
		if !strings.Contains(text, want) {
			t.Fatalf("画面里应出现 %q：\n%s", want, text)
		}
	}
	if got.Note == "" {
		t.Fatal("底部那句用法说明不该是空的：宿主拿它当这一页的提示行")
	}
	if !got.WantKeys {
		t.Fatal("这一页要能打字、要能上下选，必须声明要键盘")
	}
}

// 点目录 = 进去：换的是浏览目录，不是工作目录。
func TestPanelActionOpensDirectory(t *testing.T) {
	w, api := seedPanel(t)
	w.panel.render(w, panelRequest{W: 80, H: 20}) // 先读一次，把条目装进来
	api.events = nil

	// 条目顺序由插件排序决定，用例不写死下标，按名字找
	idx := indexOfItem(t, w.panel.items, "sub")
	if !strings.Contains(w.panel.action(w, panelRequest{Action: "open", Arg: strconv.Itoa(idx)}), "sub") {
		t.Fatalf("点 sub 应进去，实际停在 %q", w.panel.path)
	}
	if !strings.HasSuffix(w.panel.path, "sub") {
		t.Fatalf("浏览路径应变成 sub，实际 %q", w.panel.path)
	}
	if len(w.panel.items) != 1 || w.panel.items[0].Name != "a.txt" {
		t.Fatalf("进去之后应列出 sub 里的东西，实际 %+v", w.panel.items)
	}
	if !hasEvent(api.events, panelChangedEvent) {
		t.Fatal("状态变了必须通知宿主重画，否则界面停在旧内容上")
	}
}

// 点文件 = 只是选中，不该跳进任何地方。
func TestPanelActionPicksFile(t *testing.T) {
	w, _ := seedPanel(t)
	w.panel.render(w, panelRequest{W: 80, H: 20})
	before := w.panel.path

	idx := indexOfItem(t, w.panel.items, "README.md")
	w.panel.action(w, panelRequest{Action: "pick", Arg: strconv.Itoa(idx)})
	if w.panel.sel != idx {
		t.Fatalf("应选中第 %d 项，实际 %d", idx, w.panel.sel)
	}
	if w.panel.path != before {
		t.Fatalf("点文件不该换目录，实际 %q", w.panel.path)
	}
	// 越界的下标不该出事
	w.panel.action(w, panelRequest{Action: "open", Arg: "99"})
	if w.panel.path != before {
		t.Fatalf("越界下标不该有副作用，实际 %q", w.panel.path)
	}
}

// 「设为工作区」：真的设上、存进配置、并给一句确认。
func TestPanelActionSetRoot(t *testing.T) {
	w, api := seedPanel(t)
	w.panel.field.SetValue("sub")

	note := w.panel.action(w, panelRequest{Action: "setroot"})
	if w.root != "sub" {
		t.Fatalf("工作目录应设为 sub，实际 %q", w.root)
	}
	if api.cfg["root"] != "sub" {
		t.Fatalf("要存进配置（下次启动仍然记得），实际 %+v", api.cfg)
	}
	if !strings.Contains(note, "工作区已设为") {
		t.Fatalf("应给一句确认，实际 %q", note)
	}
	if !w.panel.ok {
		t.Fatal("成功时那句反馈不该标成失败")
	}
}

// 设不上（路径不存在）时要如实报错，而不是默默算了。
func TestPanelSetRootReportsFailure(t *testing.T) {
	w, api := seedPanel(t)
	api.err = fmt.Errorf("没有这个目录")

	note := w.panel.action(w, panelRequest{Action: "setroot"})
	if note == "" || w.panel.ok {
		t.Fatalf("失败应给出原因并标红，实际 %q ok=%v", note, w.panel.ok)
	}
	if w.root != "" {
		t.Fatalf("失败时不该改工作目录，实际 %q", w.root)
	}
}

// 键盘：打字进输入框、退格删回来、Esc 收起焦点，上下键选条目。
func TestPanelKeyEditing(t *testing.T) {
	w, _ := seedPanel(t)
	w.panel.field.Clear()
	w.panel.focus = true

	w.panel.key(w, "rune", "a")
	w.panel.key(w, "rune", "中")
	if w.panel.field.Value != "a中" {
		t.Fatalf("应打字进输入框，实际 %q", w.panel.field.Value)
	}
	w.panel.key(w, "backspace", "")
	if w.panel.field.Value != "a" {
		t.Fatalf("退格应删掉一个字符，实际 %q", w.panel.field.Value)
	}
	w.panel.key(w, "left", "")
	w.panel.key(w, "delete", "")
	if w.panel.field.Value != "" {
		t.Fatalf("删除光标处字符后应为空，实际 %q", w.panel.field.Value)
	}

	// 没聚焦时打字不该改内容（用户的键盘该归列表用）
	w.panel.key(w, "esc", "")
	if w.panel.focus {
		t.Fatal("Esc 应收起焦点")
	}
	w.panel.key(w, "rune", "x")
	if w.panel.field.Value != "" {
		t.Fatalf("没聚焦时不该改输入框，实际 %q", w.panel.field.Value)
	}
}

func TestPanelKeyMovesSelection(t *testing.T) {
	w, _ := seedPanel(t)
	w.panel.render(w, panelRequest{W: 80, H: 20}) // 3 个条目

	w.panel.key(w, "down", "")
	if w.panel.sel != 1 {
		t.Fatalf("下键应选到第 1 项，实际 %d", w.panel.sel)
	}
	w.panel.key(w, "up", "")
	w.panel.key(w, "up", "") // 已经在第一项，不该变成负数
	if w.panel.sel != 0 {
		t.Fatalf("上键到头应停在第 0 项，实际 %d", w.panel.sel)
	}
}

// 点输入框的某一列 = 把光标放到那一列。
func TestPanelActionCaretPlacesCursor(t *testing.T) {
	w, _ := seedPanel(t)
	w.panel.field.SetValue("abcdef")
	w.panel.render(w, panelRequest{W: 80, H: 20}) // 画一次才知道窗口在哪

	w.panel.action(w, panelRequest{Action: "caret", Arg: "2"})
	if !w.panel.focus {
		t.Fatal("点输入框应同时聚焦")
	}
	if got := w.panel.field.Cur(); got != 2 {
		t.Fatalf("点在第二列，光标应在下标 2，实际 %d", got)
	}
}

// 一个插件报的页面清单：id / 标题 / 顺序，别的一概不用给。
func TestPanelList(t *testing.T) {
	w := &Workspace{}
	list := w.panel.list()
	if len(list) != 1 {
		t.Fatalf("应报 1 页，实际 %d", len(list))
	}
	if list[0].ID != panelID || list[0].Title == "" {
		t.Fatalf("页面要有稳定的 id 和给人看的标题：%+v", list[0])
	}
}

// 滚动位置由插件夹取并回报：宿主按它显示进度，两边不能各说各话。
func TestPanelRenderClampsScroll(t *testing.T) {
	w, _ := seedPanel(t)
	w.panel.render(w, panelRequest{W: 80, H: 20})

	// 条目只有 3 个，窗口又高，根本滚不动
	got := w.panel.render(w, panelRequest{W: 80, H: 20, Scroll: 99})
	if got.Scroll != 0 || got.MaxScroll != 0 {
		t.Fatalf("没得滚时应回报 0/0，实际 %d/%d", got.Scroll, got.MaxScroll)
	}

	// 窗口高只放得下 1 行条目：能滚，且滚过头会被夹住。
	// 选中项必须留在窗口里 —— 所以先把选中的挪到最后一个，再要求滚到底。
	w.panel.sel = len(w.panel.items) - 1
	got = w.panel.render(w, panelRequest{W: 80, H: 6, Scroll: 99})
	if got.MaxScroll != 2 || got.Scroll != 2 {
		t.Fatalf("应夹到 2/2，实际 %d/%d", got.Scroll, got.MaxScroll)
	}
}

// indexOfItem 按名字找一个条目的下标。
//
// 不写死下标：条目顺序是插件自己排的，用例不该把"第几个是目录"当成契约。
func indexOfItem(t *testing.T, items []item, name string) int {
	t.Helper()
	for i, it := range items {
		if it.Name == name {
			return i
		}
	}
	t.Fatalf("条目里没有 %q：%+v", name, items)
	return -1
}

func hasEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}
