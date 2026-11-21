package main

import (
	"strings"
	"testing"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// 「模型接入」页的规矩
//
// 这一层要验的不是"配色好不好看"，而是几件会让人配错的事：
//   - 每一行都不超过宿主给的宽度（超一列，右边的竖线就歪了）
//   - 点一个平台，地址与默认模型真的填进去了
//   - 协议跟着端点走（点 Ollama 原生就真的是 ollama，不是"本地"这个词）
//   - 云端与本地严格分开（本机应用不许出现在云端清单里）
//   - 密钥框永远不装明文，"留空=不改动"才成立
//
// 有些路径要走核心（扫描 / 取模型 / 真发一次请求），这里不碰：
// 那些是 llm.v1 自己的事，由它自己的用例覆盖；这一页只管把意图传对。
// ---------------------------------------------------------------------------

// testPanel 造一个"已经读过配置"的面板。
//
// 渲染路径不该再问核心（配置在读的时候就快照下来了），所以这样就能直接画 ——
// 这也是为什么要把它做成快照：每帧都去问，等于画一帧打几次核心。
func testPanel() *llmPanel {
	p := newLLMPanel()
	p.loaded = true
	p.hasKey = true
	p.keyMask = "sk-a****cdef"
	// 读回来那一份也要一起摆好：保存是拿它比对的（只提交变过的字段），
	// 少了它，测试里的"点一下就保存"会以为字段全被改过。
	p.loadedProvider = p.provider
	p.loadedBaseURL = p.baseURL.Value
	p.loadedModel = p.model.Value
	return &p
}

func renderPanel(p *llmPanel, w, h, scroll int) plugin.PanelRender {
	return p.render(&LLM{}, plugin.PanelRequest{Panel: panelID, W: w, H: h, Scroll: scroll})
}

func panelText(res plugin.PanelRender) string { return plainOf(strings.Join(res.Lines, "\n")) }

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

// 每一行都不能超过宿主给的宽度：超一列，人家右边的竖线就歪了，而且一歪一整屏。
func TestPanelRenderFitsExactly(t *testing.T) {
	for _, sz := range [][2]int{{46, 10}, {60, 20}, {100, 30}, {88, 24}, {160, 40}} {
		p := testPanel()
		res := renderPanel(p, sz[0], sz[1], 0)
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
			t.Fatal("这一页要能上下选、打字，必须声明要键盘")
		}
	}
}

func TestPanelRenderTooSmall(t *testing.T) {
	res := renderPanel(testPanel(), 30, 6, 0)
	if len(res.Lines) != 6 {
		t.Fatalf("行数应等于高度，实际 %d", len(res.Lines))
	}
	if !strings.Contains(panelText(res), "太小") {
		t.Fatalf("应说明窗口放不下：%v", res.Lines)
	}
}

// 云端：知名平台与"自定义/冷门平台"分开摆，右边是密钥、模型、协议、保存。
func TestPanelCloudShowsPresetsAndDetail(t *testing.T) {
	p := testPanel()
	res := renderPanel(p, 100, 34, 0)
	text := panelText(res)

	for _, want := range []string{
		"模型接入", "① 接入方式", "云端", "本地",
		"② 知名平台", "DeepSeek", "Kimi", "自定义 / 冷门平台",
		"当前连接", "API Key", "端点与协议", "OpenAI 兼容", "保存并生效", "测试连接", "识图模型",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("画面里应出现 %q：\n%s", want, text)
		}
	}
	// 云端清单里不许混进本机应用：混进去用户会以为 Ollama 也要密钥
	if strings.Contains(text, "LM Studio") {
		t.Fatalf("云端那一栏不该出现本机应用：\n%s", text)
	}
}

// 点一个平台：地址与默认模型真的填进去，并说清密钥去哪申请。
func TestPanelPresetFillsAddressAndProvider(t *testing.T) {
	p := testPanel()
	renderPanel(p, 100, 34, 0) // 先画一帧：控件环是画的时候建的
	p.action(&LLM{}, plugin.PanelRequest{Panel: panelID, Action: "preset", Arg: "deepseek"})

	if p.presetKey != "deepseek" || p.provider != "openai" {
		t.Fatalf("预设应把协议与选中项一起定下来，实际 %q / %q", p.presetKey, p.provider)
	}
	if got := p.baseURL.Value; got != "https://api.deepseek.com/v1" {
		t.Fatalf("地址应自动填好，实际 %q", got)
	}
	if got := p.model.Value; got != "deepseek-chat" {
		t.Fatalf("默认模型应填第一个，实际 %q", got)
	}
	// 点完就该去粘密钥，所以焦点要落在密钥框上
	if idx := p.ctlIndex("field", fieldKey); p.focus != idx {
		t.Fatalf("焦点应落到密钥框（%d），实际 %d", idx, p.focus)
	}
	if !strings.Contains(p.note, "platform.deepseek.com/api_keys") {
		t.Fatalf("应说清密钥去哪申请，实际 %q", p.note)
	}
}

// 本地：扫描入口、已知应用、默认端口图例、手填出路 —— 一个都不能少。
//
// 「扫不到」是常态，所以手填的入口必须常驻，而不是"扫到了才出现"。
func TestPanelLocalModeShowsScanAndLegend(t *testing.T) {
	p := testPanel()
	renderPanel(p, 100, 34, 0)
	p.action(&LLM{}, plugin.PanelRequest{Panel: panelID, Action: "mode", Arg: "local"})

	text := panelText(renderPanel(p, 100, 34, 0))
	for _, want := range []string{
		"系统扫描本机", "扫描本机", "还没扫到本机 AI 服务",
		"已知本地应用", "Ollama", "LM Studio",
		// 图例那一行在窄栏里会被截断，所以这里只验它画出来了；
		// 端口内容由 TestLocalLegendHasDefaultPorts 逐条验。
		"可连的格式（默认端口）", "Ollama:11434",
		// 说明文字会折行，所以挑不会正好折在中间的片段来断言
		"自定义本地服务", "格式不受限", "Ollama 原生（/api）",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("本地那一栏应出现 %q：\n%s", want, text)
		}
	}
	// 反过来：本机那一栏不该出现公网平台，否则分组就没意义了
	if strings.Contains(text, "DeepSeek") {
		t.Fatalf("本地那一栏不该出现云端平台：\n%s", text)
	}
}

// 扫描结果回填：协议、地址、以及它自报的第一个模型。
func TestPanelLocalHitConnectFillsForm(t *testing.T) {
	p := testPanel()
	p.mode = "local"
	p.localScanned = true
	p.localFound = []localHit{{
		Key: "lmstudio", Label: "LM Studio", Provider: "openai",
		BaseURL: "http://127.0.0.1:1234/v1", Alive: true,
		Models: []string{"qwen2.5-7b"}, MS: 12,
	}}
	res := renderPanel(p, 100, 34, 0)
	text := panelText(res)
	for _, want := range []string{"LM Studio", "连接", "http://127.0.0.1:1234/v1", "端口 1234", "qwen2.5-7b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("扫描结果里应出现 %q：\n%s", want, text)
		}
	}

	p.action(&LLM{}, plugin.PanelRequest{Panel: panelID, Action: "connect", Arg: "lmstudio"})
	if p.baseURL.Value != "http://127.0.0.1:1234/v1" || p.provider != "openai" {
		t.Fatalf("回填不对：%q / %q", p.baseURL.Value, p.provider)
	}
	if p.model.Value != "qwen2.5-7b" {
		t.Fatalf("应回填它自报的第一个模型，实际 %q", p.model.Value)
	}
}

// 协议跟着端点走：点「Ollama 原生」就该是 ollama，而不是"因为在本地所以叫本地"。
func TestPanelWireChipChangesProtocol(t *testing.T) {
	p := testPanel()
	renderPanel(p, 100, 34, 0)
	p.action(&LLM{}, plugin.PanelRequest{Panel: panelID, Action: "wire", Arg: "ollama"})
	if p.provider != "ollama" {
		t.Fatalf("协议应切到 ollama，实际 %q", p.provider)
	}
	text := panelText(renderPanel(p, 100, 34, 0))
	if !strings.Contains(text, "Ollama 原生") {
		t.Fatalf("顶部当前连接要跟着变：\n%s", text)
	}
}

// 打字只落进聚焦的那个框；没聚焦时不该改到任何框。
func TestPanelTypingGoesToFocusedField(t *testing.T) {
	p := testPanel()
	renderPanel(p, 100, 34, 0)

	// 没聚焦：敲字不改任何东西（否则用户以为在给页面加批注，其实改了配置）
	p.key(&LLM{}, "rune", "X")
	if p.baseURL.Value != "" || p.model.Value != "" {
		t.Fatalf("没聚焦时不该改到任何框：%q / %q", p.baseURL.Value, p.model.Value)
	}

	// 聚焦地址框：点进去（caret 动作）再打字
	p.action(&LLM{}, plugin.PanelRequest{Panel: panelID, Action: "caret", Arg: fieldBaseURL + ":0"})
	for _, r := range "http://x/v1" {
		p.key(&LLM{}, "rune", string(r))
	}
	if p.baseURL.Value != "http://x/v1" {
		t.Fatalf("打字应进地址框，实际 %q", p.baseURL.Value)
	}
	// 退格只退一个字符
	p.key(&LLM{}, "backspace", "")
	if p.baseURL.Value != "http://x/v" {
		t.Fatalf("退格不该多退，实际 %q", p.baseURL.Value)
	}
	// Esc 退出聚焦：之后打字不再落进来
	p.key(&LLM{}, "esc", "")
	p.key(&LLM{}, "rune", "Z")
	if p.baseURL.Value != "http://x/v" {
		t.Fatalf("Esc 之后不该还能打字进去，实际 %q", p.baseURL.Value)
	}
}

// 一个字都没改就点保存：要说"没有可保存的改动"，而不是白发一次请求。
func TestPanelSaveWithoutChangesSaysSo(t *testing.T) {
	p := testPanel()
	renderPanel(p, 100, 34, 0)
	note := p.action(&LLM{}, plugin.PanelRequest{Panel: panelID, Action: "btn", Arg: "save"})
	if !strings.Contains(note, "没有可保存的改动") {
		t.Fatalf("应如实说没有改动，实际 %q", note)
	}
}

// 焦点跑到看不见的地方时，页面要自己把它滚出来。
//
// 只有这一页自己知道环上第 n 个控件画在第几行 —— 宿主替不了它。
func TestPanelFocusScrollsIntoView(t *testing.T) {
	p := testPanel()
	renderPanel(p, 60, 12, 0) // 矮窗口：内容一定装不下
	for i := 0; i < len(p.ring); i++ {
		p.key(&LLM{}, "down", "")
	}
	res := renderPanel(p, 60, 12, 0)
	if res.Scroll == 0 {
		t.Fatalf("焦点在内容底部时滚动位置应跟过去，实际 %d（环长 %d）", res.Scroll, len(p.ring))
	}
	row, ok := p.rowOf[p.focus]
	if !ok {
		t.Fatalf("最后聚焦的那个控件应当有行号记录：focus=%d ring=%d", p.focus, len(p.ring))
	}
	if row < res.Scroll || row >= res.Scroll+10 {
		t.Fatalf("焦点行 %d 不在可视区 [%d,%d)", row, res.Scroll, res.Scroll+10)
	}
}

// 一份知识只能有一处：目录里的本机应用与扫描候选表，**两边都提到的 key 必须一致**。
//
// 这两份数据一个是"点一下直接填好"，一个是"扫到之后显示什么"。飘开的表现是：
// 扫到 Ollama，点「连接」填进去的地址却跟目录里点出来的不一样。
//
// 方向是单侧的，不是一一对应：扫描候选可以比目录多（比如"其它 OpenAI 兼容服务"
// 这种兜底条目，只能扫出来、没有固定的默认端口，所以不进目录）；但目录里每个
// "已知本地应用"都必须能被扫到 —— 否则它就成了一个"点下去却连不上"的假入口。
func TestLocalCatalogMatchesScanCandidates(t *testing.T) {
	cands := localCandidates()
	byKey := map[string]localCandidate{}
	for _, c := range cands {
		byKey[c.Key] = c
	}
	for _, it := range presetsOfGroup(groupLocal) {
		if it.Key == "mock" {
			continue // 离线 mock 不是本机应用，它只是验证链路用的
		}
		c, ok := byKey[it.Key]
		if !ok {
			t.Fatalf("目录里的本机应用 %s 扫描候选表里没有：它成了一个点下去却连不上的假入口", it.Key)
		}
		if c.BaseURL != it.BaseURL || c.Provider != it.Provider {
			t.Fatalf("%s 在目录里是 %s/%s，扫描候选表里却是 %s/%s —— 两份数据飘开了",
				it.Key, it.BaseURL, it.Provider, c.BaseURL, c.Provider)
		}
	}
}

// 默认端口图例：给"我知道自己装了什么"的人一眼对照。
func TestLocalLegendHasDefaultPorts(t *testing.T) {
	got := strings.Join(localLegend(), " · ")
	for _, want := range []string{"Ollama:11434", "LM Studio:1234", "vLLM:8000"} {
		if !strings.Contains(got, want) {
			t.Fatalf("图例里应有 %q，实际 %q", want, got)
		}
	}
	if strings.Contains(got, "mock") {
		t.Fatalf("离线 mock 不是本机应用格式，不该进图例：%q", got)
	}
}

// 页面的 id 与标题：宿主靠 id 分发，靠标题在标签栏上显示。
func TestPanelList(t *testing.T) {
	var l LLM
	list := l.panel.list()
	if len(list) != 1 || list[0].ID != panelID || list[0].Title == "" {
		t.Fatalf("页面要有稳定的 id 和给人看的标题：%+v", list)
	}
	if !strings.HasPrefix(panelService, "ui.page.") {
		t.Fatalf("服务名要守约定（ui.page.<名字>.v1），实际 %q", panelService)
	}
}

// 宽屏两栏并排时，右栏的热区不能还落在左栏的列上。
//
// 这是用户报过的真实 bug：entryBuilder 里的按钮 / chip / 输入框都按
// 「从第 0 列开始」登记绝对坐标，两栏并排后文本是顺序追加的所以位置对，
// 热区却还留在左栏那一列 —— 右栏按钮"看得见、点不中"。
func TestPanelWideRightColumnHitsAreShifted(t *testing.T) {
	p := testPanel()
	res := renderPanel(p, 100, 40, 0) // 100 >= llmWideMin，走两栏
	rightStart := llmLeftW + 2

	foundSave := false
	for i := range res.Hits {
		h := &res.Hits[i]
		switch {
		case h.Action == "btn" && h.Arg == "save":
			foundSave = true
			if h.X < rightStart {
				t.Fatalf("右栏「保存并生效」热区 x=%d 还落在左栏（应 >= %d）", h.X, rightStart)
			}
		case h.Action == "mode" && h.Arg == "cloud":
			// 左栏的 chip 必须整个待在左栏，不许越到右栏
			if h.X+h.W > llmLeftW {
				t.Fatalf("左栏「云端」chip 热区越界到右栏：%+v", h)
			}
		}
	}
	if !foundSave {
		t.Fatal("应有「保存并生效」按钮的热区")
	}
}
