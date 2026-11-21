package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// fakeAPI 顶替核心的能力代理：设工作目录、弹选择框、发事件都记下来。
//
// 这一页的逻辑（点了会怎样、画成什么样）不该为了测一下就去连真磁盘，
// 所以把插件需要的那几样能力换成假的。
// ---------------------------------------------------------------------------

type fakeAPI struct {
	cfg    map[string]any
	events []string
	err    error
	picked string // PickFolder 返回的目录；空 = 用户取消
}

func (f *fakeAPI) FSList(path string) (json.RawMessage, error) {
	if f.err != nil {
		return nil, f.err
	}
	// setRootPath 靠 FSList 校验"这个目录存在"，返回的 path 原样回显即可
	return json.Marshal(map[string]any{"path": path, "entries": []any{}})
}

func (f *fakeAPI) PickFolder(initial, title string) (json.RawMessage, error) {
	if f.picked == "" {
		return json.Marshal(map[string]any{"path": "", "cancelled": true})
	}
	return json.Marshal(map[string]any{"path": f.picked, "cancelled": false})
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

func seedPanel(t *testing.T) (*Workspace, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{}
	w := &Workspace{api: api}
	return w, api
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

// 每一行都不能超过宿主给的宽度：超一列，人家右边的竖线就歪了。
// 这是"插件自带界面"最要紧的一条不变量 —— 它由 SDK 的 Block 保证。
func TestPanelRenderFitsExactly(t *testing.T) {
	w, _ := seedPanel(t)
	for _, sz := range [][2]int{{40, 8}, {60, 16}, {100, 30}, {24, 7}} {
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

// 页面上要有标题、系统选择按钮，以及当前工作区。
func TestPanelShowsCurrentRoot(t *testing.T) {
	w, _ := seedPanel(t)
	w.root = "E:\\proj"
	got := w.panel.render(w, panelRequest{W: 80, H: 20})
	text := plainOf(strings.Join(got.Lines, "\n"))

	for _, want := range []string{"工作区文件", "E:\\proj", "系统选择", "当前工作区"} {
		if !strings.Contains(text, want) {
			t.Fatalf("画面里应出现 %q：\n%s", want, text)
		}
	}
	if got.Note == "" {
		t.Fatal("底部那句用法说明不该是空的：宿主拿它当这一页的提示行")
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

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------

// 「系统选择」：弹出的系统对话框给了个目录，就该当工作区设上并存配置。
func TestPanelSysPickSetsRoot(t *testing.T) {
	w, api := seedPanel(t)
	api.picked = "sub"

	note := w.panel.action(w, panelRequest{Action: "syspick"})
	if w.root != "sub" {
		t.Fatalf("系统选择的目录应设为工作区，实际 %q", w.root)
	}
	if api.cfg["root"] != "sub" {
		t.Fatalf("要存进配置，实际 %+v", api.cfg)
	}
	if !strings.Contains(note, "工作区已设为") {
		t.Fatalf("应给一句确认，实际 %q", note)
	}
	if !w.panel.ok {
		t.Fatal("成功时那句反馈不该标成失败")
	}
}

// 「系统选择」取消：不该动工作目录，如实说取消了。
func TestPanelSysPickCancelled(t *testing.T) {
	w, _ := seedPanel(t)
	note := w.panel.action(w, panelRequest{Action: "syspick"})
	if w.root != "" {
		t.Fatalf("取消时不该改工作目录，实际 %q", w.root)
	}
	if !strings.Contains(note, "取消") {
		t.Fatalf("应说明已取消，实际 %q", note)
	}
}

// 设不上（目录不存在）时要如实报错，而不是默默算了。
func TestPanelSetRootReportsFailure(t *testing.T) {
	w, api := seedPanel(t)
	api.picked = "gone"
	api.err = fmt.Errorf("没有这个目录")

	note := w.panel.sysPick(w)
	if note == "" || w.panel.ok {
		t.Fatalf("失败应给出原因并标红，实际 %q ok=%v", note, w.panel.ok)
	}
	if w.root != "" {
		t.Fatalf("失败时不该改工作目录，实际 %q", w.root)
	}
}

// ---------------------------------------------------------------------------
// 清单
// ---------------------------------------------------------------------------

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

// manifest 里声明的自带界面服务名必须和代码里的 panelService 一致：
// workbench 靠扫描 manifest 的 provides 来发现页面，名字对不上这一页就凭空消失。
func TestManifestDeclaresPanelService(t *testing.T) {
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatalf("读 manifest.json 失败: %v", err)
	}
	var m struct {
		Provides []string `json:"provides"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析 manifest.json 失败: %v", err)
	}
	for _, svc := range m.Provides {
		if svc == panelService {
			return
		}
	}
	t.Fatalf("manifest.json 的 provides 里没有声明 %s（workbench 靠它发现这一页），实际 %v",
		panelService, m.Provides)
}
