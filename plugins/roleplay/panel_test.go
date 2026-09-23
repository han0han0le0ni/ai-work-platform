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
// fakeAPI 顶替核心：配置读写、事件、服务调用都记下来。
//
// 界面逻辑（点了什么会怎样、画成什么样）不该为了测一下就去连真核心 ——
// 这也是把 pluginAPI 抽出来的原因。
// ---------------------------------------------------------------------------

type fakeAPI struct {
	cfg     map[string]json.RawMessage
	events  []string
	invoked []string
	ready   map[string]bool
	writeEr error
}

func (f *fakeAPI) Consume(name string) (string, error) {
	if f.ready != nil && f.ready[name] {
		return "com.official.fake", nil
	}
	return "", fmt.Errorf("未安装 %s", name)
}

func (f *fakeAPI) Invoke(service, method string, args any) (json.RawMessage, error) {
	f.invoked = append(f.invoked, service+"."+method)
	return json.RawMessage(`{}`), nil
}

func (f *fakeAPI) GetConfig(key string) (json.RawMessage, error) {
	v, ok := f.cfg[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (f *fakeAPI) SetConfig(key string, value any) error {
	if f.writeEr != nil {
		return f.writeEr
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if f.cfg == nil {
		f.cfg = map[string]json.RawMessage{}
	}
	f.cfg[key] = raw
	return nil
}

func (f *fakeAPI) Emit(event string, payload any) error {
	f.events = append(f.events, event)
	return nil
}

func (f *fakeAPI) Log(level, msg string, meta map[string]any) {}

// seedRoleplay 造一个"已经有两张卡、第一张启用中"的插件。
func seedRoleplay(t *testing.T) (*Roleplay, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{
		cfg:   map[string]json.RawMessage{},
		ready: map[string]bool{"agent.v1": true, "toolExecutor.v1": true},
	}
	st := &store{
		Seq:      2,
		ActiveID: "c1",
		Persona:  persona{Name: "阿远", Desc: "常来买旧书的大学生"},
		Characters: []Character{
			{ID: "c1", Name: "林晚", Description: "书店店主", Personality: "冷淡",
				Scenario: "雨夜", Greeting: "雨还没停？", Memory: "- 爱喝美式\n- 住在城南",
				Updated: "2026-09-22 20:15"},
			{ID: "c2", Name: "阿远", Description: "送信的", Updated: "2026-09-21 09:00"},
		},
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	api.cfg[configKey] = raw
	return &Roleplay{api: api}, api
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

func renderPanel(t *testing.T, r *Roleplay, w, h, scroll int) plugin.PanelRender {
	t.Helper()
	return r.panel.render(r, plugin.PanelRequest{Panel: panelID, W: w, H: h, Scroll: scroll})
}

func panelText(res plugin.PanelRender) string {
	return plainOf(strings.Join(res.Lines, "\n"))
}

// 每一行都不能超过宿主给的宽度：超一列，人家右边的竖线就歪了。
func TestPanelRenderFitsExactly(t *testing.T) {
	r, _ := seedRoleplay(t)
	for _, sz := range [][2]int{{50, 10}, {60, 16}, {100, 30}, {44, 8}} {
		res := renderPanel(t, r, sz[0], sz[1], 0)
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
			t.Fatal("这一页要能打字、要能上下选，必须声明要键盘")
		}
	}
}

// 窗口太小时要认怂，而不是画一屏垃圾。
func TestPanelRenderTooSmall(t *testing.T) {
	r, _ := seedRoleplay(t)
	res := renderPanel(t, r, 20, 4, 0)
	if len(res.Lines) != 4 {
		t.Fatalf("行数应等于高度，实际 %d", len(res.Lines))
	}
	if !strings.Contains(panelText(res), "太小") {
		t.Fatalf("应说明窗口放不下：%v", res.Lines)
	}
}

// 一上来就该看到：列表、启用标记、字段、身份、链路。
func TestPanelRenderShowsListAndCard(t *testing.T) {
	r, _ := seedRoleplay(t)
	res := renderPanel(t, r, 100, 30, 0)
	text := panelText(res)

	for _, want := range []string{
		"角色 2", "启用中：林晚",
		"林晚", "阿远", "2 条", "更新于 09-22 20:15",
		"角色名", "人物设定", "性格", "场景", "开场白", "记忆",
		"我的名字", "我的身份", "保存身份",
		"新建", "保存角色", "停用", "删除", "预览提示词", "用开场白起头",
		"agent ✓", "remember ✓",
		"共 2 条",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("画面里应出现 %q：\n%s", want, text)
		}
	}
	// 表单里装的是启用中的那张卡
	if got := r.panel.fields[rpFieldName].f.Value; got != "林晚" {
		t.Fatalf("表单应装第一张卡，实际 %q", got)
	}
	if !strings.Contains(text, "爱喝美式") {
		t.Fatalf("记忆应看得见：\n%s", text)
	}
}

// 点列表里的名字 = 换人，表单跟着换。
func TestPanelActionListSwitchesCard(t *testing.T) {
	r, api := seedRoleplay(t)
	api.events = nil
	renderPanel(t, r, 100, 30, 0)

	note := r.panel.action(r, plugin.PanelRequest{Panel: panelID, Action: "list", Arg: "1"})
	if got := r.panel.fields[rpFieldName].f.Value; got != "阿远" {
		t.Fatalf("切到第二张后表单应是阿远，实际 %q", got)
	}
	if !strings.Contains(note, "阿远") {
		t.Fatalf("应回一句「已切到…」，实际 %q", note)
	}
	if !hasEvent(api.events, panelChangedEvent) {
		t.Fatal("状态变了必须通知宿主重画，否则界面停在旧内容上")
	}
}

// 打字进当前字段；保存之后存储里就是新值。
func TestPanelKeyTypesAndSaves(t *testing.T) {
	r, _ := seedRoleplay(t)
	renderPanel(t, r, 100, 30, 0)

	// 点字段 = 聚焦（arg 是字段下标）
	r.panel.action(r, plugin.PanelRequest{Panel: panelID, Action: "field", Arg: strconv.Itoa(rpFieldName)})
	r.panel.key(r, "end", "")
	for _, ch := range "（改）" {
		r.panel.key(r, "rune", string(ch))
	}
	if got := r.panel.fields[rpFieldName].f.Value; got != "林晚（改）" {
		t.Fatalf("打字应进当前字段，实际 %q", got)
	}

	r.panel.key(r, "ctrl+enter", "")
	st := r.load()
	if got := st.find("c1").Name; got != "林晚（改）" {
		t.Fatalf("保存后存储里应是新名字，实际 %q", got)
	}
	// 没动过记忆 → 记忆不该被提交（否则模型刚 remember 的那条会被抹掉）
	if got := st.find("c1").Memory; !strings.Contains(got, "爱喝美式") {
		t.Fatalf("没动过的记忆不该被覆盖，实际 %q", got)
	}
}

// 动过记忆就得提交。
func TestPanelSaveSendsTouchedMemory(t *testing.T) {
	r, _ := seedRoleplay(t)
	renderPanel(t, r, 100, 30, 0)

	r.panel.action(r, plugin.PanelRequest{Panel: panelID, Action: "field", Arg: strconv.Itoa(rpFieldMemory)})
	r.panel.key(r, "end", "")
	r.panel.key(r, "rune", "！")
	r.panel.key(r, "ctrl+enter", "")

	if got := r.load().find("c1").Memory; !strings.HasSuffix(got, "！") {
		t.Fatalf("动过的记忆应提交，实际 %q", got)
	}
}

// 删除不可逆：第一次点只进确认态（按钮改名），第二次才真删。
func TestPanelDeleteNeedsTwoPresses(t *testing.T) {
	r, _ := seedRoleplay(t)
	renderPanel(t, r, 100, 30, 0)

	r.panel.action(r, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "del"})
	if !r.panel.confirmDel {
		t.Fatal("第一次点应进入确认态")
	}
	if got := r.load().find("c1"); got == nil {
		t.Fatal("第一次点不该真的删")
	}
	res := renderPanel(t, r, 100, 30, 0)
	if !strings.Contains(panelText(res), "确认删除") {
		t.Fatalf("确认态下按钮应改名：\n%s", panelText(res))
	}

	r.panel.action(r, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "del"})
	if got := r.load().find("c1"); got != nil {
		t.Fatal("第二次点应真的删掉")
	}
	if r.panel.curID() != "c2" {
		t.Fatalf("删完应落到还活着的那张，实际 %q", r.panel.curID())
	}
}

// 预览：显示真正会发出去的那段提示词，Esc 回到表单。
func TestPanelPreviewShowsPrompt(t *testing.T) {
	r, _ := seedRoleplay(t)
	renderPanel(t, r, 100, 30, 0)

	r.panel.action(r, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "preview"})
	if !r.panel.preview {
		t.Fatal("预览应已打开")
	}
	res := renderPanel(t, r, 100, 30, 0)
	text := panelText(res)
	if !strings.Contains(text, "接下来你要扮演「林晚」") {
		t.Fatalf("预览应显示拼好的提示词：\n%s", text)
	}
	if !strings.Contains(text, "返回编辑") {
		t.Fatalf("预览里应有回来的路：\n%s", text)
	}
	found := false
	for _, hit := range res.Hits {
		if hit.Action == "close-preview" {
			found = true
		}
	}
	if !found {
		t.Fatal("「返回编辑」必须有热区")
	}

	r.panel.key(r, "esc", "")
	if r.panel.preview {
		t.Fatal("Esc 应回到表单")
	}
	if !strings.Contains(panelText(renderPanel(t, r, 100, 30, 0)), "角色名") {
		t.Fatal("Esc 后应看到表单")
	}
}

// 「用开场白起头」是交给宿主去做的（@ui:chat）：插件不该知道聊天页长什么样。
func TestPanelGreetGoesThroughHost(t *testing.T) {
	r, _ := seedRoleplay(t)
	res := renderPanel(t, r, 100, 30, 0)

	found := false
	for _, hit := range res.Hits {
		if hit.Action == "@ui:chat" {
			found = true
			if hit.Arg != "雨还没停？" {
				t.Fatalf("应把开场白交给宿主，实际 %q", hit.Arg)
			}
		}
	}
	if !found {
		t.Fatalf("「用开场白起头」应挂在宿主的口子上：%+v", res.Hits)
	}
}

// 焦点跑到可视区外时，这一页自己把滚动位置挪过去 —— 宿主不知道页面上有什么。
func TestPanelFocusScrollsIntoView(t *testing.T) {
	r, _ := seedRoleplay(t)
	// 窗口矮到装不下整页
	res := renderPanel(t, r, 100, 12, 0)
	if res.MaxScroll == 0 {
		t.Fatal("这一页在矮窗口下应该能滚")
	}
	// 把焦点挪到"我的身份"（整页最后几个字段之一）
	r.panel.action(r, plugin.PanelRequest{Panel: panelID, Action: "field", Arg: strconv.Itoa(rpFieldMeDesc)})
	res = renderPanel(t, r, 100, 12, 0)
	if res.Scroll == 0 {
		t.Fatalf("焦点在页面底部时滚动位置应跟过去，实际 %d", res.Scroll)
	}
	row, ok := r.panel.rowOf[r.panel.focus]
	if !ok {
		t.Fatal("聚焦的控件应当有行号记录")
	}
	if row < res.Scroll || row >= res.Scroll+(12-5) {
		t.Fatalf("焦点行 %d 不在可视区 [%d,%d)", row, res.Scroll, res.Scroll+(12-5))
	}
}

// 模型调 remember 之后，页面必须自己刷新（存储变了，内容还是旧的 = 点了没反应）。
func TestPanelRememberMarksStale(t *testing.T) {
	r, api := seedRoleplay(t)
	renderPanel(t, r, 100, 30, 0)
	if r.panel.stale.Load() {
		t.Fatal("没改动之前不该是脏的")
	}

	if _, err := r.remember(json.RawMessage(`{"id":"c1","text":"喜欢靠窗的位置"}`)); err != nil {
		t.Fatal(err)
	}
	if !r.panel.stale.Load() {
		t.Fatal("remember 之后页面应变脏，下一帧重新读")
	}
	if !hasEvent(api.events, panelChangedEvent) {
		t.Fatal("remember 之后要通知宿主重画")
	}
	res := renderPanel(t, r, 100, 30, 0)
	if !strings.Contains(panelText(res), "喜欢靠窗的位置") {
		t.Fatalf("重画后应看到新记忆：\n%s", panelText(res))
	}
	if !strings.Contains(panelText(res), "3 条") {
		t.Fatalf("记忆条数应跟着变：\n%s", panelText(res))
	}
}

// 一个插件报的页面清单。
func TestPanelList(t *testing.T) {
	var r Roleplay
	list := r.panel.list()
	if len(list) != 1 || list[0].ID != panelID || list[0].Title == "" {
		t.Fatalf("页面要有稳定的 id 和给人看的标题：%+v", list)
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
