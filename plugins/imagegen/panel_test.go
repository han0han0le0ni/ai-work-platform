package main

import (
	"strings"
	"testing"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// 「文生图」配置页的规矩
//
// 和 llm 的「模型」页同一套约定，只是这一页更小：没有云端/本地之分、没有扫描、
// 没有识图模型，就是"选服务商 + 填密钥模型尺寸 + 保存"。
// ---------------------------------------------------------------------------

func testPanel() *igPanel {
	p := newIGPanel()
	p.loaded = true
	p.hasKey = true
	p.keyMask = "sk-a****cdef"
	p.baseURL.SetValue("https://api.openai.com/v1")
	p.model.SetValue("dall-e-3")
	p.size.SetValue("1024x1024")
	p.loadedProvider = "openai"
	p.loadedBaseURL = "https://api.openai.com/v1"
	p.loadedModel = "dall-e-3"
	p.loadedSize = "1024x1024"
	p.loadedDir = ""
	return &p
}

func renderPanel(p *igPanel, w, h, scroll int) plugin.PanelRender {
	return p.render(&Provider{}, plugin.PanelRequest{Panel: panelID, W: w, H: h, Scroll: scroll})
}

func panelText(res plugin.PanelRender) string { return plainOf(strings.Join(res.Lines, "\n")) }

// plainOf 去掉样式码，只留看得见的字。
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

// 每一行都不能超过宿主给的宽度；行数等于高度；热区不能越界；要键盘。
func TestPanelRenderFitsExactly(t *testing.T) {
	for _, sz := range [][2]int{{40, 8}, {60, 18}, {100, 26}, {160, 30}} {
		res := renderPanel(testPanel(), sz[0], sz[1], 0)
		if len(res.Lines) != sz[1] {
			t.Fatalf("%dx%d：应有 %d 行，实际 %d", sz[0], sz[1], sz[1], len(res.Lines))
		}
		for i, ln := range res.Lines {
			if got := plugin.DisplayWidth(plainOf(ln)); got > sz[0] {
				t.Fatalf("%dx%d：第 %d 行宽 %d 超 %d：%q", sz[0], sz[1], i, got, sz[0], ln)
			}
		}
		for _, hit := range res.Hits {
			if hit.Y < 0 || hit.Y >= sz[1] || hit.X < 0 || hit.X+hit.W > sz[0] {
				t.Fatalf("%dx%d：热区越界 %+v", sz[0], sz[1], hit)
			}
		}
		if !res.WantKeys {
			t.Fatal("这一页要能上下选、打字，必须声明要键盘")
		}
	}
}

// 服务商、字段、保存按钮都要在；密钥框不该回显明文。
func TestPanelShowsPresetsAndFields(t *testing.T) {
	text := panelText(renderPanel(testPanel(), 100, 26, 0))
	for _, want := range []string{
		"文生图", "服务商", "OpenAI", "通义万相", "SiliconFlow", "智谱", "本地 SD",
		"当前连接", "地址", "模型", "尺寸", "目录", "密钥", "保存并生效",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("画面里应出现 %q：\n%s", want, text)
		}
	}
	// 密钥只显示打码值，绝不回显明文
	if strings.Contains(text, "sk-a****cdef") == false && !strings.Contains(text, "已配置") {
		t.Fatalf("应显示密钥已配置的状态：\n%s", text)
	}
}

// 点一个服务商：地址与默认模型自动填好，焦点落到密钥框。
func TestPanelPresetFillsAddressAndModel(t *testing.T) {
	p := testPanel()
	renderPanel(p, 100, 26, 0) // 先画一帧：控件环是画的时候建的
	p.action(&Provider{}, plugin.PanelRequest{Panel: panelID, Action: "preset", Arg: "siliconflow"})

	if p.presetKey != "siliconflow" {
		t.Fatalf("应选中 siliconflow，实际 %q", p.presetKey)
	}
	if got := p.baseURL.Value; got != "https://api.siliconflow.cn/v1" {
		t.Fatalf("地址应自动填好，实际 %q", got)
	}
	if got := p.model.Value; got != "Kwai-Kolors/Kolors" {
		t.Fatalf("默认模型应填好，实际 %q", got)
	}
	if idx := p.ctlIndex("field", fieldKey); p.focus != idx {
		t.Fatalf("焦点应落到密钥框（%d），实际 %d", idx, p.focus)
	}
}

// 打字只落进聚焦的框；Esc 退出聚焦后不再落进去。
func TestPanelTypingGoesToFocusedField(t *testing.T) {
	p := testPanel()
	renderPanel(p, 100, 26, 0)

	p.key(&Provider{}, "rune", "X")
	if p.baseURL.Value != "https://api.openai.com/v1" {
		t.Fatalf("没聚焦时不该改到任何框：%q", p.baseURL.Value)
	}

	p.action(&Provider{}, plugin.PanelRequest{Panel: panelID, Action: "caret", Arg: fieldSize + ":0"})
	for _, r := range "1024x1792" {
		p.key(&Provider{}, "rune", string(r))
	}
	if p.size.Value != "1024x17921024x1024" {
		t.Fatalf("打字应进尺寸框，实际 %q", p.size.Value)
	}
	p.key(&Provider{}, "esc", "")
	p.key(&Provider{}, "rune", "Z")
	if p.size.Value != "1024x17921024x1024" {
		t.Fatalf("Esc 之后不该还能打字进去，实际 %q", p.size.Value)
	}
}

// 一个字都没改就点保存：如实说，而不是白发一次请求。
func TestPanelSaveWithoutChangesSaysSo(t *testing.T) {
	p := testPanel()
	renderPanel(p, 100, 26, 0)
	note := p.action(&Provider{}, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "save"})
	if !strings.Contains(note, "没有可保存的改动") {
		t.Fatalf("应如实说没有改动，实际 %q", note)
	}
}

// 页面清单：稳定的 id 与标题，服务名守 ui.page.* 约定。
func TestPanelList(t *testing.T) {
	var g Provider
	list := g.panel.list()
	if len(list) != 1 || list[0].ID != panelID || list[0].Title == "" {
		t.Fatalf("页面要有稳定的 id 和给人看的标题：%+v", list)
	}
	if !strings.HasPrefix(panelService, "ui.page.") {
		t.Fatalf("服务名要守约定，实际 %q", panelService)
	}
}

// 预设目录是唯一的服务商来源：页面 chip 与 defaultModel/defaultBaseURL 要一致，
// 不能"页面点出来一个、配置兜底又是另一个"。
func TestPresetCatalogIsConsistent(t *testing.T) {
	if len(imagePresets) != 5 {
		t.Fatalf("预设目录应有 5 个服务商，实际 %d", len(imagePresets))
	}
	for _, it := range imagePresets {
		if it.Key == "" || it.BaseURL == "" || it.Model == "" {
			t.Fatalf("预设不能缺字段：%+v", it)
		}
		if got := defaultModel(it.Key); got != it.Model {
			t.Fatalf("%s 的默认模型在目录里是 %q，兜底却是 %q —— 飘开了", it.Key, it.Model, got)
		}
		if got := defaultBaseURL(it.Key); got != it.BaseURL {
			t.Fatalf("%s 的默认地址在目录里是 %q，兜底却是 %q —— 飘开了", it.Key, it.BaseURL, got)
		}
	}
	// 历史别名也要继续认：老配置里写的 dashscope / bigmodel 不该突然失效
	if got := defaultModel("dashscope"); got != "wanx-v1" {
		t.Fatalf("dashscope 别名应指向通义万相，实际 %q", got)
	}
	if got := defaultModel("bigmodel"); got != "cogview-3" {
		t.Fatalf("bigmodel 别名应指向智谱，实际 %q", got)
	}
}

// 按住拖出一段字，Ctrl+C 应把选中的那段交出去复制。
func TestPanelDragSelectThenCopy(t *testing.T) {
	p := testPanel()
	renderPanel(p, 100, 26, 0) // 画一帧，字段的窗口/热区才建立

	// 地址框里按下第 0 列、拖到第 4 列：选中 "http"
	p.action(&Provider{}, plugin.PanelRequest{Panel: panelID, Action: "caret", Arg: fieldBaseURL + ":0"})
	p.action(&Provider{}, plugin.PanelRequest{Panel: panelID, Action: "sel", Arg: fieldBaseURL + ":4"})

	copyText, exit := p.key(&Provider{}, "ctrl+c", "")
	if exit {
		t.Fatal("有选择时 Ctrl+C 不该请宿主退出")
	}
	if copyText != "http" {
		t.Fatalf("应复制选中的 http，实际 %q", copyText)
	}
}

// 没拖选就按 Ctrl+C：没有可复制的，返回空且不退出（退出是 Ctrl+Q，宿主管）。
func TestPanelCtrlCWithoutSelectionReturnsEmpty(t *testing.T) {
	p := testPanel()
	renderPanel(p, 100, 26, 0)

	copyText, exit := p.key(&Provider{}, "ctrl+c", "")
	if copyText != "" {
		t.Fatalf("没选中不该有复制内容，实际 %q", copyText)
	}
	if exit {
		t.Fatal("没选中时 Ctrl+C 不该请求退出（它是复制键）")
	}
}
