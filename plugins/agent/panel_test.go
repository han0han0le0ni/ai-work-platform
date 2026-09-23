package main

import (
	"strings"
	"testing"

	"aiwork/sdk/go/plugin"
)

// 一个"已经读过配置"的面板：渲染路径不该再问核心，所以这样就能直接画。
func testAgentPanel() *agentPanel {
	p := newAgentPanel()
	p.loaded = true
	p.steps = 6
	p.unlimited = false
	p.stepsDraft.SetValue("6")
	p.timeoutMs = 240000
	p.toolLimit = 8000
	p.toolCount = 12
	p.hasLLM = true
	p.hasTool = true
	return &p
}

func renderAgentPanel(p *agentPanel, w, h, scroll int) plugin.PanelRender {
	return p.render(&Agent{}, plugin.PanelRequest{Panel: panelID, W: w, H: h, Scroll: scroll})
}

func agentText(res plugin.PanelRender) string { return plainOf(strings.Join(res.Lines, "\n")) }

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

func TestAgentPanelRenderFitsExactly(t *testing.T) {
	for _, sz := range [][2]int{{40, 8}, {60, 16}, {100, 20}} {
		res := renderAgentPanel(testAgentPanel(), sz[0], sz[1], 0)
		if len(res.Lines) != sz[1] {
			t.Fatalf("%dx%d：应有 %d 行，实际 %d", sz[0], sz[1], sz[1], len(res.Lines))
		}
		for i, ln := range res.Lines {
			if got := plugin.DisplayWidth(plainOf(ln)); got > sz[0] {
				t.Fatalf("%dx%d：第 %d 行宽 %d 超 %d：%q", sz[0], sz[1], i, got, sz[0], ln)
			}
		}
		if !res.WantKeys {
			t.Fatal("这一页要能上下选、打字，必须声明要键盘")
		}
	}
}

// 预算这一页：上限 / 不限制、步数、应用预算，外加只读的编排快照。
func TestAgentPanelShowsBudgetAndSnapshot(t *testing.T) {
	text := agentText(renderAgentPanel(testAgentPanel(), 80, 20, 0))
	for _, want := range []string{
		"预算", "工具调用预算", "限制步数", "不限制", "步数", "应用预算",
		"当前编排状态", "LLM 已连接", "工具执行器 已连接", "可用工具 12 个", "超时 240 秒",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("画面里应出现 %q：\n%s", want, text)
		}
	}
}

// 不限制：选上之后步数框退位成一句说明，而不是还让人填一个没意义的数字。
func TestAgentPanelUnlimitedToggle(t *testing.T) {
	p := testAgentPanel()
	renderAgentPanel(p, 80, 20, 0)
	p.action(&Agent{}, plugin.PanelRequest{Panel: panelID, Action: "chip", Arg: "unlimited"})
	if !p.unlimited {
		t.Fatal("应切到不限制")
	}
	text := agentText(renderAgentPanel(p, 80, 20, 0))
	if !strings.Contains(text, "（不限制）") {
		t.Fatalf("不限制时步数框应退位：\n%s", text)
	}
}

// 步数框只收数字；打字、退格都只落在它里面。
func TestAgentPanelStepsOnlyDigits(t *testing.T) {
	p := testAgentPanel()
	renderAgentPanel(p, 80, 20, 0)
	p.action(&Agent{}, plugin.PanelRequest{Panel: panelID, Action: "caret", Arg: "steps:0"})
	for _, r := range "1x2" {
		p.key(&Agent{}, "rune", string(r))
	}
	// 光标在第 0 列：'1' 插到最前 → "16"，'x' 被拒，'2' 插到 '1' 后 → "126"
	if p.stepsDraft.Value != "126" {
		t.Fatalf("非数字应被拒收，实际 %q", p.stepsDraft.Value)
	}
	p.key(&Agent{}, "backspace", "")
	if p.stepsDraft.Value != "16" {
		t.Fatalf("退格应只退一个，实际 %q", p.stepsDraft.Value)
	}
}

// 没填步数就点应用：要如实说，而不是悄悄存个 0 或报错。
func TestAgentPanelSaveValidation(t *testing.T) {
	p := testAgentPanel()
	p.stepsDraft.Clear()
	renderAgentPanel(p, 80, 20, 0)
	note := p.action(&Agent{}, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "save"})
	if !strings.Contains(note, "正整数") {
		t.Fatalf("应说明要填正整数，实际 %q", note)
	}
}

// 没改动就点应用：要说一声，而不是白发一次请求。
func TestAgentPanelSaveWithoutChange(t *testing.T) {
	p := testAgentPanel() // steps=6, draft="6" —— 没改
	renderAgentPanel(p, 80, 20, 0)
	note := p.action(&Agent{}, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "save"})
	if !strings.Contains(note, "没有可保存的改动") {
		t.Fatalf("应如实说没有改动，实际 %q", note)
	}
}

func TestAgentPanelList(t *testing.T) {
	var a Agent
	list := a.panel.list()
	if len(list) != 1 || list[0].ID != panelID || list[0].Title == "" {
		t.Fatalf("页面要有稳定的 id 和给人看的标题：%+v", list)
	}
	if !strings.HasPrefix(panelService, "ui.page.") {
		t.Fatalf("服务名要守约定，实际 %q", panelService)
	}
}
