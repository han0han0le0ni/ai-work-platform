package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// 「模型接入」页：这一页住在 llm 插件自己身上
//
// 以前它长在界面插件里（workbench/settings.go + presets.go），连"哪个平台说的是
// 哪套协议、密钥去哪申请、本机应用默认端口是多少"这种纯 LLM 知识都存了一份在
// 界面那边。而 llm 插件本来就有 providers / configure / models / discover ——
// 两份知识迟早飘开，飘开的表现是"界面填得好好的，插件却连不上"。
//
// 现在的分工与其它自带页面一样：
//
//	这个文件   画成"文本行 + 可点区域"，自己处理点击与按键
//	UI 插件    只借一块 w×h 的地方、把点击和按键转过来
//
// 三条从原页面带过来的硬约定（它们都是踩过的坑，不是装饰）：
//
//  1. **云端与本地严格分开**。位置本身就是分组暗示：把本机应用混进云端清单，
//     用户会以为 Ollama 也要密钥。
//  2. **「扫不到」不算失败**。多数机器上本来就没有本机 AI 服务，所以手填入口
//     常驻在扫描结果下面，提示里也直接指向它 —— 标红只会让人以为弄坏了什么。
//  3. **协议跟着端点走，不跟着「本地」这个词走**。LM Studio 说的是 OpenAI 兼容，
//     Ollama 才是原生 /api；选错的表现是"连不上"，状态码拦不住。
// ---------------------------------------------------------------------------

const (
	// panelService 是这一页的服务名：ui.page.<插件自己的名字>.v1。
	panelService = "ui.page.llm.v1"
	panelID      = "llm"

	// panelChangedEvent 是"我这一页变了，重新画一遍"。
	panelChangedEvent = "ui.panel.changed"

	// llmWideMin 到多宽才分两栏（左：连哪儿 / 右：凭什么连）。
	llmWideMin = 88
	llmLeftW   = 46

	// 四个输入框的名字。它们同时是动作参数（caret:baseUrl:12）。
	fieldBaseURL = "baseUrl"
	fieldModel   = "model"
	fieldKey     = "apiKey"
	fieldVision  = "visionModel"
)

// llmCtl 是环上的一个控件。kind 决定回车/上下键怎么解释。
type llmCtl struct {
	kind string // mode / preset / connect / wire / model / field / btn
	arg  string
}

// llmEntry 是内容里的一行：怎么画 + 它上面有哪几个控件。
//
// 一行可以有多个控件（一排 chip 就是），所以这里收的是一个列表：
// "焦点必须看得见"要按行算，而一行里的每个 chip 都该能把行滚出来。
type llmEntry struct {
	draw func(b *plugin.Block, y int)
	ctls []int
}

// llmPanel 是这一页的全部界面状态。
type llmPanel struct {
	mu sync.Mutex

	loaded bool
	stale  atomic.Bool

	// mode 是"云端 / 本地"这个总开关。它只影响左栏看哪一组，不影响右栏。
	mode      string
	presetKey string // 选中的服务预设（靠它高亮 chip、决定密钥提示）
	provider  string // 协议：openai / ollama / mock

	// 四个输入框。密钥框**永远不装明文**：从服务端拿回来的只有打码值，
	// 而框里留空表示"不改动"（见 save）。
	baseURL     plugin.Field
	model       plugin.Field
	apiKey      plugin.Field
	visionModel plugin.Field

	modelList  []string
	modelsNote string

	// hasKey / keyMask 是"当前配没配密钥"的快照。
	//
	// 必须在读配置时一起取下来，**渲染路径里不能再问**：l.settings() 每次要走
	// 好几趟核心 RPC，而渲染是每帧都跑的 —— 那样光是画这一页就在不停地打核心。
	hasKey  bool
	keyMask string

	// 读回来那一份配置。保存时拿它比对，**只提交真正变过的字段**：
	// 没变也提交等于白写一遍配置，而且"没改动"这句提示就永远说不出口了。
	loadedProvider string
	loadedBaseURL  string
	loadedModel    string

	localFound    []localHit
	localScanning bool
	localScanned  bool
	localNote     string

	note string
	ok   bool
	busy bool // 有一个动作在途（扫描 / 取模型 / 保存 / 测试）

	// ring 与 rowOf 在每次渲染时重建：布局变了（换模式、扫描出结果），
	// 控件的位置就变了，只有画的时候才知道谁在第几行。
	ring  []llmCtl
	focus int
	rowOf map[int]int
}

func newLLMPanel() llmPanel {
	return llmPanel{mode: "cloud", provider: "openai", focus: -1}
}

// ---------------------------------------------------------------------------
// 与宿主对接
// ---------------------------------------------------------------------------

func (p *llmPanel) list() []plugin.PanelInfo {
	return []plugin.PanelInfo{{ID: panelID, Title: "模型", Order: 20}}
}

func (l *LLM) handlePanel(method string, args json.RawMessage) (any, error) {
	switch method {
	case "list":
		return map[string]any{"panels": l.panel.list()}, nil
	case "render":
		var in plugin.PanelRequest
		_ = json.Unmarshal(args, &in)
		if in.Panel != "" && in.Panel != panelID {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "llm 没有页面 %s", in.Panel)
		}
		return l.panel.render(l, in), nil
	case "action":
		var in plugin.PanelRequest
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
		}
		return map[string]any{"note": l.panel.action(l, in)}, nil
	case "key":
		var in plugin.PanelRequest
		_ = json.Unmarshal(args, &in)
		copyText, exit := l.panel.key(l, in.Kind, in.Text)
		out := map[string]any{"ok": true}
		if copyText != "" {
			out["copy"] = copyText
		}
		if exit {
			out["exit"] = true
		}
		return out, nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%s 不支持方法 %s", panelService, method)
	}
}

// changed 通知宿主"这一页变了，重新画一遍"。
func (p *llmPanel) changed(l *LLM) {
	if l == nil || l.ctx == nil {
		return
	}
	_ = l.ctx.Emit(panelChangedEvent, map[string]any{
		"service": panelService,
		"panel":   panelID,
	})
}

// say 记一句反馈并通知宿主重画。
func (p *llmPanel) say(l *LLM, note string, ok bool) string {
	p.note, p.ok = note, ok
	p.changed(l)
	return note
}

// ---------------------------------------------------------------------------
// 读配置
// ---------------------------------------------------------------------------

// load 把当前配置读进这一页。
//
// 直接调 l.settings()：同一个进程里，没必要绕一圈 providers 再解析一遍 map。
func (p *llmPanel) load(l *LLM) {
	s := l.settings()
	p.provider = firstNonEmpty(s.Provider, "mock")
	p.baseURL.SetValue(s.BaseURL)
	p.model.SetValue(s.Model)
	p.visionModel.SetValue(s.VisionModel)
	// 明文拿不回来，所以输入框留空，把打码值留给提示行去说。
	// 这样"留空 = 不改动"就是自然成立的，不需要另立一个"改没改过"的标记。
	p.apiKey.Clear()
	p.hasKey = s.APIKey != ""
	p.keyMask = ""
	if p.hasKey {
		p.keyMask = maskKey(s.APIKey)
	}
	p.loadedProvider = p.provider
	p.loadedBaseURL = strings.TrimSpace(s.BaseURL)
	p.loadedModel = strings.TrimSpace(s.Model)

	// 选中状态从配置反推：改过配置的人再进来，看到的仍然是"我连的是哪个平台"。
	p.presetKey = presetByBaseURL(s.BaseURL)
	p.mode = "cloud"
	if isLocalURL(s.BaseURL) {
		p.mode = "local"
	}
	if p.presetKey != "" {
		if preset, ok := presetByKey(p.presetKey); ok && preset.Group == groupLocal {
			p.mode = "local"
		}
	}
	p.note = "当前：" + wireLabel(p.provider) + " · " + firstNonEmpty(s.Model, "未指定模型")
	p.ok = true
	p.loaded = true
	p.stale.Store(false)
}

// keyHint 说清密钥框现在是什么状态。
//
// 框里是空的，所以这句必须自己说：不说的话，用户看着空框会以为密钥丢了。
func (p *llmPanel) keyHint() string {
	if p.hasKey {
		if p.keyMask != "" {
			return "已配置 " + p.keyMask + "；要换就直接粘新的，减号清空"
		}
		return "已配置；要换就直接粘新的，减号清空"
	}
	return "未配置：点一下这个框再粘贴"
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

func (p *llmPanel) render(l *LLM, req plugin.PanelRequest) plugin.PanelRender {
	p.mu.Lock()
	defer p.mu.Unlock()

	w, h := req.W, req.H
	if w < 1 || h < 1 {
		return plugin.PanelRender{}
	}
	if !p.loaded || p.stale.Swap(false) {
		p.load(l)
	}
	b := plugin.NewBlock(w, h)
	if w < 46 || h < 8 {
		b.At(0).Text(plugin.SWarn, " 窗口太小，放不下这一页")
		return b.Render(0, 0, "把窗口拉大些")
	}

	// 控件环与行号每帧重建：布局会变（换模式、扫描出结果、chip 折行），
	// 谁在第几个、在第几行，只有画的时候才知道。
	p.ring, p.rowOf = nil, map[int]int{}

	const top = 2
	p.drawHeader(b, w)
	ch := h - top

	wide := w >= llmWideMin
	leftW, rightW := w, w
	if wide {
		leftW = llmLeftW
		rightW = w - leftW - 2
	}
	left := p.leftColumn(l, leftW)
	right := p.rightColumn(l, rightW)

	// 行数 = 两栏里较长的那一个。焦点环里的控件对应到行号（滚动用）。
	// wide 两栏并排时 left[i] 与 right[i] 都在第 i 行；单栏时右栏叠在
	// 左栏 + 分隔线之后。
	var doc []llmEntry
	rows := 0
	if wide {
		rows = maxInt(len(left), len(right))
		for i := 0; i < rows; i++ {
			if i < len(left) {
				for _, c := range left[i].ctls {
					p.rowOf[c] = i
				}
			}
			if i < len(right) {
				for _, c := range right[i].ctls {
					p.rowOf[c] = i
				}
			}
		}
	} else {
		// 单栏时中间加一条分隔线：不隔开，「API Key」小节看起来像是
		// 上面「自定义本地服务」的一部分，而它们其实是两回事（连哪儿 / 凭什么连）。
		doc = append(doc, left...)
		doc = append(doc, llmEntry{draw: func(b *plugin.Block, y int) {
			b.At(y).Text(plugin.SBorder, strings.Repeat("─", maxInt(1, w)))
		}})
		doc = append(doc, right...)
		rows = len(doc)
		for i, e := range doc {
			for _, c := range e.ctls {
				p.rowOf[c] = i
			}
		}
	}

	maxScroll := maxInt(0, rows-ch)
	scroll := clampInt(req.Scroll, 0, maxScroll)
	// 焦点必须看得见：只有这一页自己知道环上第 n 个控件画在第几行
	if row, ok := p.rowOf[p.focus]; ok {
		if row < scroll {
			scroll = row
		} else if row >= scroll+ch {
			scroll = row - ch + 1
		}
		scroll = clampInt(scroll, 0, maxScroll)
	}

	if wide {
		return p.renderWide(left, right, leftW, rightW, b, scroll, maxScroll, top, ch)
	}

	for i := 0; i < ch; i++ {
		if idx := scroll + i; idx < len(doc) {
			doc[idx].draw(b, top+i)
		}
	}

	res := b.Render(scroll, maxScroll, llmHint(p.mode))
	res.WantKeys = true
	res.WantCopy = true // 有密钥 / 地址 / 模型输入框：Ctrl+C 交给这一页复制
	return res
}

// renderWide 在宽屏（两栏并排）时渲染。
//
// 两栏各自在独立的 Block 里画（坐标都从第 0 列起），再拼成一行：
// 左栏行 + 分隔线 + 右栏行。右栏的热区 x 要整体加上左栏宽 + 2 的偏移 ——
// 这是「按钮点不灵」的根子：entryBuilder 里的按钮 / chip / 输入框都按
// 「从第 0 列开始」登记绝对坐标，若直接顺序追加到同一行，文本画对了、
// 热区却还落在左栏那一列，点上自然没反应。
func (p *llmPanel) renderWide(left, right []llmEntry, leftW, rightW int, b *plugin.Block, scroll, maxScroll, top, ch int) plugin.PanelRender {
	lb := plugin.NewBlock(leftW, ch)
	rb := plugin.NewBlock(rightW, ch)
	for i := 0; i < ch; i++ {
		idx := scroll + i
		if idx < len(left) {
			left[idx].draw(lb, i)
		}
		if idx < len(right) {
			right[idx].draw(rb, i)
		}
	}

	ll := lb.Lines()
	rl := rb.Lines()
	sep := plugin.Style(plugin.SBorder).S("│") + " "

	// 头部（前 top 行）已经在 b 里画好，直接取出来用。
	head := b.Lines()
	lines := make([]string, 0, top+ch)
	lines = append(lines, head[:top]...)
	for i := 0; i < ch; i++ {
		lines = append(lines, ll[i]+sep+rl[i])
	}

	hits := append([]plugin.Hit{}, b.Hits()...)
	for _, h := range lb.Hits() {
		h.Y += top
		hits = append(hits, h)
	}
	for _, h := range rb.Hits() {
		h.Y += top
		h.X += leftW + 2
		hits = append(hits, h)
	}

	res := plugin.PanelRender{
		Lines:     lines,
		Hits:      hits,
		Scroll:    scroll,
		MaxScroll: maxScroll,
		Note:      llmHint(p.mode),
	}
	res.WantKeys = true
	res.WantCopy = true
	return res
}

// drawHeader 画顶栏：连的是什么、刚才那一下的结果。
//
// 结果行钉在这儿而不是丢在页尾：表单是竖着长长的，保存之后那句"已保存"
// 常常落在可视区之外 —— 那看起来就是"点了没反应"。
func (p *llmPanel) drawHeader(b *plugin.Block, w int) {
	head := " 模型接入"
	b.At(0).Text(plugin.SAccent+plugin.SBold, head)
	cur := wireLabel(p.provider)
	if m := strings.TrimSpace(p.model.Value); m != "" {
		cur += " · " + m
	} else {
		cur += " · 未指定模型"
	}
	room := maxInt(0, w-plugin.DisplayWidth(head)-2)
	b.At(0).Text(plugin.SDim, "  "+plugin.Truncate(cur, room))

	if p.note != "" {
		st := plugin.Style(plugin.SDim)
		if !p.ok {
			st = plugin.Style(plugin.SBad)
		}
		b.At(1).Text(st, " "+plugin.Truncate(plugin.OneLine(p.note), maxInt(0, w-2)))
	}
}

// leftColumn 是左栏：连哪儿。
func (p *llmPanel) leftColumn(l *LLM, w int) []llmEntry {
	e := &entryBuilder{p: p, w: w}
	e.blank()
	e.section("① 接入方式", "云端 / 本地")
	e.chips(w,
		p.chip("云端", "mode", "cloud", p.mode == "cloud"),
		p.chip("本地", "mode", "local", p.mode == "local"),
	)
	if p.mode == "local" {
		e.text("   本地：连这台机器上跑的模型，多数不要密钥", plugin.SDim)
		e.add(p.localSection(l, w)...)
		return e.out
	}
	e.text("   云端：连公网上的大模型服务，需要 API Key", plugin.SDim)
	e.blank()
	e.add(p.cloudSection(w)...)
	return e.out
}

// cloudSection 是云端那一栏：② 知名平台 + ③ 自定义 / 冷门平台。
func (p *llmPanel) cloudSection(w int) []llmEntry {
	e := &entryBuilder{p: p, w: w}
	e.section("② 知名平台", "点一下，地址与模型自动填好")
	var chips []chipSpec
	for _, it := range presetsOfGroup(groupCloud) {
		chips = append(chips, p.chip(it.Label, "preset", it.Key, p.presetKey == it.Key))
	}
	e.chips(w, chips...)

	// 选中之后"接下来要做什么"就摊在点击处：用哪套协议、地址是什么、去哪申请密钥。
	if it, ok := presetByKey(p.presetKey); ok && it.Group != groupLocal {
		room := maxInt(8, w-4)
		e.text("   "+plugin.Truncate(it.Label+" · "+wireLabel(it.Provider)+" · "+
			firstNonEmpty(it.BaseURL, "（地址待填）"), room), plugin.SAccent)
		note := "密钥按该服务的要求填"
		switch {
		case it.KeyURL != "":
			note = "密钥在 " + it.KeyURL + " 申请"
		case !it.NeedKey:
			note = "它不需要密钥，直接点「保存并生效」即可"
		}
		e.text("   "+plugin.Truncate(note, room), plugin.SDim)
	} else {
		e.note("点一个平台即可开始；点完到右栏粘贴密钥。", 2)
	}
	e.blank()

	e.section("③ 自定义 / 冷门平台", "自建网关 / 代理站")
	var niche []chipSpec
	for _, it := range presetsOfGroup(groupNiche) {
		niche = append(niche, p.chip(it.Label, "preset", it.Key, p.presetKey == it.Key))
	}
	e.chips(w, niche...)
	e.note("选它之后，到右栏「端点与协议」里填 Base URL 与模型名（可点「获取可用模型」照抄）。"+
		"密钥按该服务的要求填。", 2)
	return e.out
}

// localSection 是本地那一栏：② 系统扫描 + ③ 已知本地应用 + ④ 自定义本地服务。
func (p *llmPanel) localSection(l *LLM, w int) []llmEntry {
	e := &entryBuilder{p: p, w: w}
	e.section("② 系统扫描本机", "看这台机器上有没有 AI")
	label := "扫描本机"
	if p.localScanning {
		label = "正在扫描…"
	}
	e.row(func(b *plugin.Block, y int, e *entryBuilder) {
		cx := b.Button(y, 1, label, "btn", "scan", false)
		b.At(y).Text(plugin.SDim, " "+plugin.Truncate(p.localNote, maxInt(0, w-cx-2)))
	}, p.ctl("btn", "scan"))

	for _, hit := range p.localFound {
		e.add(localHitEntries(p, hit, w)...)
	}
	if len(p.localFound) == 0 && !p.localScanning {
		// 扫不到不是故障：多数机器上本来就没有本机 AI 服务。所以这句是中性提示
		// 加出路，而不是一个红色失败 —— 标红只会让人以为自己弄坏了什么。
		e.text("   ✓ 还没扫到本机 AI 服务", plugin.SWarn)
		e.note("这在多数机器上是正常的。往下点一个「已知本地应用」，"+
			"或到「自定义本地服务」手动输入地址与模型。", 4)
	}
	if p.localScanned {
		e.blank()
	}

	e.section("③ 已知本地应用", "点一下直接填好，不用扫描")
	var chips []chipSpec
	for _, it := range presetsOfGroup(groupLocal) {
		chips = append(chips, p.chip(localLegendName(it), "preset", it.Key, p.presetKey == it.Key))
	}
	e.chips(w, chips...)
	e.legend(localLegend())
	e.blank()

	e.section("④ 自定义本地服务", "扫不到就手填")
	e.note("格式不受限：只要它说的是 OpenAI 兼容（/v1）或 Ollama 原生（/api），这里就能连。"+
		"到右栏「端点与协议」选好协议，再填 Base URL 与模型名。", 4)
	return e.out
}

// localHitEntries 画一条扫描结果：一行信息（含「连接」按钮）、一行地址与端口。
//
// 地址单独占一行是有意的：连不上时它是唯一需要照抄的东西，和名字挤在同一行
// 只会被截掉尾巴。
func localHitEntries(p *llmPanel, hit localHit, w int) []llmEntry {
	e := &entryBuilder{p: p, w: w}
	e.row(func(b *plugin.Block, y int, _ *entryBuilder) {
		// 先给按钮留位置再排版：不留的话窄屏上按钮会被挤出屏幕，
		// 而「连接」正是这条结果唯一的用法。
		room := maxInt(6, w-10)
		meta := wireLabel(hit.Provider)
		if n := len(hit.Models); n > 0 {
			meta += fmt.Sprintf(" · %d 个模型", n)
		}
		if hit.Note != "" {
			meta += " · " + hit.Note
		}
		nameW := clampInt(room-8, 4, 18)
		name := plugin.Truncate(hit.Label, nameW)
		b.At(y).Text(plugin.SOK, " ✓ ")
		b.At(y).Text(plugin.SBold, name)
		if left := room - plugin.DisplayWidth(name) - 3; left > 1 {
			b.At(y).Text(plugin.SDim, " "+plugin.Truncate(meta, left-1))
		}
		b.Button(y, w-8, "连接", "connect", hit.Key, true)
	}, p.ctl("connect", hit.Key))

	// 第二行：地址 + 端口 + 探测耗时。后两样是排查"为什么连不上"的第一手材料。
	tail := ""
	if port := portOf(hit.BaseURL); port != "" {
		tail += " · 端口 " + port
	}
	if hit.MS > 0 {
		tail += fmt.Sprintf(" · %dms", hit.MS)
	}
	e.text("   "+plugin.Truncate(hit.BaseURL+tail, maxInt(4, w-5)), plugin.SDim)
	if len(hit.Models) > 0 {
		e.text("   模型 "+plugin.Truncate(strings.Join(hit.Models, " · "), maxInt(4, w-7)), plugin.SDim)
	}
	return e.out
}

// rightColumn 是右栏：凭什么连。
//
// 顺序 = 眼睛从上到下要走的顺序：先看现在连的是什么，再密钥、再模型、
// 再端点与协议（自建 / 冷门 / 本地手填时才动），最后保存、测试与识图。
func (p *llmPanel) rightColumn(l *LLM, w int) []llmEntry {
	e := &entryBuilder{p: p, w: w}
	blank := func() { e.blank() }

	// 当前连接：改动前先看清现在连的是什么 —— 不看就改，常常是"改完发现
	// 连不上了，也不知道之前是什么"。
	e.section("当前连接", "改动前先看看")
	e.text("   "+plugin.Truncate(wireLabel(p.provider)+" · "+
		firstNonEmpty(strings.TrimSpace(p.model.Value), "未指定模型"), maxInt(4, w-5)), plugin.SAccent)
	e.text("   "+plugin.Truncate(firstNonEmpty(strings.TrimSpace(p.baseURL.Value), "（地址未填）")+
		" · "+p.keyState(), maxInt(4, w-5)), plugin.SDim)
	blank()

	e.section("API Key", "留空=不改动；减号=清空")
	e.field(fieldKey, &p.apiKey, "密钥", p.keyHint())
	blank()

	// 模型：先给几个常用的（来自预设或扫描结果），点「获取可用模型」要一份真实清单
	// —— 模型迭代比客户端发版快得多，写死的清单很快就过期。
	e.section("模型", "点按钮要一份真实清单")
	if len(p.modelList) > 0 {
		var chips []chipSpec
		cur := strings.TrimSpace(p.model.Value)
		for _, m := range p.modelList {
			chips = append(chips, p.chip(m, "model", m, m == cur))
		}
		e.chips(w, chips...)
	}
	e.row(func(b *plugin.Block, y int, _ *entryBuilder) {
		label := "获取可用模型"
		if p.busy {
			label = "获取中…"
		}
		cx := b.Button(y, 1, label, "btn", "getmodels", p.modelsNote != "")
		b.At(y).Text(plugin.SDim, " "+plugin.Truncate(p.modelsNote, maxInt(0, w-cx-2)))
	}, p.ctl("btn", "getmodels"))
	blank()

	// 端点与协议：协议单独摆出来，是因为它最容易踩也最难自查 ——
	// 地址完全正确，只是协议不匹配；Ollama 被按 OpenAI 问回 404 纯文本，
	// 而 LM Studio 被按 Ollama 问更绝：回 HTTP 200 加一句 error，状态码拦不住。
	e.section("端点与协议", "协议选错会连不上")
	e.chips(w,
		p.chip("OpenAI 兼容", "wire", "openai", p.provider == "openai"),
		p.chip("Ollama 原生", "wire", "ollama", p.provider == "ollama"),
		p.chip("离线 mock", "wire", "mock", p.provider == "mock"),
	)
	e.field(fieldBaseURL, &p.baseURL, "地址", "例：https://api.deepseek.com/v1")
	e.field(fieldModel, &p.model, "模型", "例：deepseek-chat")
	blank()

	e.row(func(b *plugin.Block, y int, _ *entryBuilder) {
		cx := b.Button(y, 1, "保存并生效", "btn", "save", true)
		label := "测试连接"
		if p.busy {
			label = "请稍候…"
		}
		b.Button(y, cx, label, "btn", "test", false)
	}, p.ctl("btn", "save"), p.ctl("btn", "test"))
	blank()

	// 识图模型：文字和看图分开配 —— 日常聊天用便宜快的，发了图才切到会看图的。
	e.section("识图模型", "留空=跟随主配置；减号=清空")
	e.field(fieldVision, &p.visionModel, "识图", "带图片时改用这个")
	return e.out
}

// keyState 是"当前连接"那行里的密钥状态（只说有没有，不说值）。
func (p *llmPanel) keyState() string {
	if p.hasKey {
		if p.keyMask != "" {
			return "密钥 " + p.keyMask
		}
		return "密钥已配置"
	}
	return "未配置密钥"
}

// ---------------------------------------------------------------------------
// 画一行的小工具
// ---------------------------------------------------------------------------

// chipSpec 是一个待画的 chip。
type chipSpec struct {
	label  string
	action string
	arg    string
	on     bool
	ctl    int
}

func (p *llmPanel) chip(label, action, arg string, on bool) chipSpec {
	return chipSpec{label: label, action: action, arg: arg, on: on, ctl: p.ctl(action, arg)}
}

// ctl 往环上登记一个控件，返回它的下标。
func (p *llmPanel) ctl(kind, arg string) int {
	p.ring = append(p.ring, llmCtl{kind: kind, arg: arg})
	return len(p.ring) - 1
}

// entryBuilder 攒一行行内容，顺便把控件记进环里。
type entryBuilder struct {
	p   *llmPanel
	w   int
	out []llmEntry
}

// add 追加内容。
//
// 刻意**不**返回切片：append 返回的是新切片，调用方一忘了接住，写进去的东西
// 就等于没写（底层数组可能被就地改了，但 e.out 的长度还是旧的）。
func (e *entryBuilder) add(list ...llmEntry) { e.out = append(e.out, list...) }

func (e *entryBuilder) blank() {
	e.out = append(e.out, llmEntry{draw: func(*plugin.Block, int) {}})
}

// text 画一行静态文字（不参与焦点环）。
func (e *entryBuilder) text(s, style string) {
	line := s
	e.out = append(e.out, llmEntry{draw: func(b *plugin.Block, y int) {
		b.At(y).Text(plugin.Style(style), plugin.Truncate(line, e.w))
	}})
}

// note 画一段缩进的说明文字，按栏宽自动折行。
func (e *entryBuilder) note(s string, indent int) {
	for _, ln := range plugin.Wrap(s, maxInt(8, e.w-2-indent)) {
		e.text(" "+strings.Repeat(" ", indent)+ln, plugin.SDim)
	}
}

// section 画小节标题：左边名字，右边一句话说明。
//
// 说明不是装饰：这一页每个按钮都对应一个"该填什么"的判断，判断依据就写在这儿。
func (e *entryBuilder) section(title, note string) {
	line := " " + title + "  " + note
	e.out = append(e.out, llmEntry{draw: func(b *plugin.Block, y int) {
		b.At(y).Text(plugin.SDim+plugin.SBold, plugin.Truncate(line, e.w))
	}})
}

// legend 画"可连的格式（默认端口）"那一行。
func (e *entryBuilder) legend(parts []string) {
	if len(parts) == 0 {
		return
	}
	text := " 可连的格式（默认端口）——" + strings.Join(parts, " · ")
	e.text(plugin.Truncate(text, e.w), plugin.SDim)
}

// chips 把一排 chip 按可用宽度折行；每一行都记进环里。
func (e *entryBuilder) chips(w int, items ...chipSpec) {
	var cur []chipSpec
	used := 2 // 左侧留白
	flush := func() {
		if len(cur) == 0 {
			return
		}
		batch := append([]chipSpec{}, cur...)
		var ctls []int
		for _, it := range batch {
			ctls = append(ctls, it.ctl)
		}
		e.out = append(e.out, llmEntry{ctls: ctls, draw: func(b *plugin.Block, y int) {
			x := 1
			for _, it := range batch {
				nx := b.Chip(y, x, it.label, it.action, it.arg, it.on)
				if nx == x {
					break // 放不下就停：宁可少画，也不能画到框外
				}
				x = nx
			}
		}})
		cur, used = nil, 2
	}
	for _, it := range items {
		need := plugin.DisplayWidth(it.label) + 4
		if it.on {
			need += 2 // 选中态前面多一个 ●
		}
		if used+need > w-1 {
			flush()
		}
		cur = append(cur, it)
		used += need + 2
	}
	flush()
}

// row 画一行"带控件的自定义内容"。
func (e *entryBuilder) row(draw func(b *plugin.Block, y int, e *entryBuilder), ctls ...int) {
	e.out = append(e.out, llmEntry{ctls: ctls, draw: func(b *plugin.Block, y int) {
		draw(b, y, e)
	}})
}

// field 画一个输入框：标签 + 框 + 提示。
//
// 框里为空、又有提示时，提示写在框外而不是框里 —— 框里的空是"不改动"的意思，
// 往里塞一行灰字反而会让人以为那是个预填值。
func (e *entryBuilder) field(name string, f *plugin.Field, label, hint string) {
	p := e.p
	ctl := p.ctl("field", name)
	e.out = append(e.out, llmEntry{ctls: []int{ctl}, draw: func(b *plugin.Block, y int) {
		focused := p.focus == ctl
		st := plugin.Style("")
		if focused {
			st = plugin.Style(plugin.SSel)
		}
		// 标签占固定宽度，四个框的左边框才会对齐
		b.At(y).Text(st, " "+plugin.Pad(label, 6))
		inner := maxInt(8, e.w-20)
		b.At(y).Text(st, "[")
		used := f.Draw(b.At(y), inner, focused, "")
		b.At(y).Text(st, plugin.Pad("", maxInt(0, inner-used)))
		b.At(y).Text(st, "]")
		// 点框内任意一列 → 把光标放到那一列（要的就是"点哪改哪"）
		for col := 0; col < inner; col++ {
			b.Hit(y, 8+col, 1, "caret", name+":"+strconv.Itoa(col))
		}
		if !focused && strings.TrimSpace(f.Value) == "" && hint != "" {
			b.At(y).Text(plugin.SDim, " "+plugin.Truncate(hint, maxInt(0, e.w-8-inner-3)))
		}
	}})
}

// localLegend 是"能连哪些本地应用、默认端口是多少"的图例。
//
// 给"我知道自己装了什么"的人看：不用扫描、也不用点，一眼就能对照自己的应用
// 找端口。表里没有的走「自定义本地服务」。
func localLegend() []string {
	var parts []string
	for _, it := range presetsOfGroup(groupLocal) {
		if it.Key == "mock" {
			continue // 离线 mock 不是本机应用格式，它只是验证链路用的
		}
		if port := portOf(it.BaseURL); port != "" {
			parts = append(parts, localLegendName(it)+":"+port)
			continue
		}
		parts = append(parts, localLegendName(it))
	}
	return parts
}

// localLegendName 是图例里用的短名。
//
// 只有一个名字需要缩短：text-generation-webui 太长，摊在图例里换行会被从中间
// 劈开（"text-generatio / n-webui"），看着像打错了字。列表里的选项名保持全称。
func localLegendName(it providerPreset) string {
	if it.Key == "textgen" {
		return "text-gen-webui"
	}
	return it.Label
}

// llmHint 是底部那行用法说明（宿主照抄）。
func llmHint(mode string) string {
	if mode == "local" {
		return "点平台/应用即自动填好 · 扫不到就手填 · Tab 切页 · Ctrl+C 复制 · Ctrl+Q 退出"
	}
	return "点平台 → 地址与模型自动填好 → 粘密钥 → 保存并生效 · Tab 切页 · Ctrl+C 复制 · Ctrl+Q 退出"
}

// ---------------------------------------------------------------------------
// 焦点环
// ---------------------------------------------------------------------------

func (p *llmPanel) moveFocus(delta int) {
	if len(p.ring) == 0 {
		p.focus = -1
		return
	}
	if p.focus < 0 {
		p.focus = 0
	} else {
		p.focus = clampInt(p.focus+delta, 0, len(p.ring)-1)
	}
}

// focusedField 返回当前聚焦的输入框（没聚焦在框上时返回 nil）。
func (p *llmPanel) focusedField() (*plugin.Field, string) {
	if p.focus < 0 || p.focus >= len(p.ring) {
		return nil, ""
	}
	c := p.ring[p.focus]
	if c.kind != "field" {
		return nil, ""
	}
	switch c.arg {
	case fieldBaseURL:
		return &p.baseURL, fieldBaseURL
	case fieldModel:
		return &p.model, fieldModel
	case fieldKey:
		return &p.apiKey, fieldKey
	case fieldVision:
		return &p.visionModel, fieldVision
	}
	return nil, ""
}

// ---------------------------------------------------------------------------
// 按键
// ---------------------------------------------------------------------------

// key 处理一次按键。
//
// 只处理这一页自己的事：Tab（切页）由宿主扣下，Ctrl+C 是复制，退出是 Ctrl+Q。
// 否则用户会被困在这一页里出不去。
func (p *llmPanel) key(l *LLM, kind, text string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.loaded || p.stale.Swap(false) {
		p.load(l)
	}

	// Ctrl+C：只复制。没选中就返回空，退出是 Ctrl+Q（宿主管），不在这里。
	if kind == "ctrl+c" {
		return p.selection(), false
	}

	if f, name := p.focusedField(); f != nil {
		switch kind {
		case "rune":
			for _, r := range text {
				f.Insert(r)
			}
			p.say(l, "", true)
			return "", false
		case "backspace":
			f.Backspace()
			p.say(l, "", true)
			return "", false
		case "delete":
			f.Delete()
			p.say(l, "", true)
			return "", false
		case "left":
			f.Left()
			return "", false
		case "right":
			f.Right()
			return "", false
		case "home":
			f.Home()
			return "", false
		case "end":
			f.End()
			return "", false
		case "enter":
			// 回车走下一个控件：用键盘也能一路填完再按「保存并生效」
			p.moveFocus(1)
			p.say(l, "", true)
			return "", false
		case "esc":
			// 从框里退出来，而不是直接取消整页 —— 层级最浅的那一步
			p.focus = -1
			p.say(l, "", true)
			return "", false
		case "up":
			p.moveFocus(-1)
			p.say(l, "", true)
			return "", false
		case "down":
			p.moveFocus(1)
			p.say(l, "", true)
			return "", false
		}
		_ = name
		return "", false
	}

	switch kind {
	case "up":
		p.moveFocus(-1)
		p.say(l, "", true)
	case "down":
		p.moveFocus(1)
		p.say(l, "", true)
	case "enter":
		p.activate(l)
	case "esc":
		p.focus = -1
		p.say(l, "", true)
	}
	return "", false
}

// selection 返回当前选中的文字：哪个字段里有选中段就取哪个。
func (p *llmPanel) selection() string {
	for _, f := range []*plugin.Field{&p.baseURL, &p.model, &p.apiKey, &p.visionModel} {
		if f.HasSel() {
			return f.Selection()
		}
	}
	return ""
}

// activate 回车落在不同控件上有不同的意思。
func (p *llmPanel) activate(l *LLM) {
	if p.focus < 0 || p.focus >= len(p.ring) {
		p.moveFocus(1)
		p.say(l, "", true)
		return
	}
	c := p.ring[p.focus]
	switch c.kind {
	case "field":
		return // 已经在框里了，回车由上面那条分支处理
	case "btn", "mode", "preset", "wire", "model", "connect":
		p.apply(l, plugin.PanelRequest{Action: c.kind, Arg: c.arg})
	}
}

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------

// action 处理一次点击。
func (p *llmPanel) action(l *LLM, req plugin.PanelRequest) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.apply(l, req)
}

// apply 是真正干活的那个：**调用方必须已经持有锁**。
//
// 拆出它是因为回车走的是按键那条路（key 已经握着锁），而它要做的又正是
// "按一下按钮" —— 同一个动作两条入口，各锁一次就是自己等自己。
func (p *llmPanel) apply(l *LLM, req plugin.PanelRequest) string {
	if !p.loaded || p.stale.Swap(false) {
		p.load(l)
	}
	switch req.Action {
	case "caret":
		// 参数是 "字段名:列号"：点哪一列就把光标放到那一列
		name, colText, ok := strings.Cut(req.Arg, ":")
		if !ok {
			return ""
		}
		if f := p.fieldByName(name); f != nil {
			f.SetCursor(f.CaretAtCol(atoiSafe(colText)))
			f.SelStart(f.Cur()) // 按下：落点也是拖选锚点
		}
		p.focus = p.ctlIndex("field", name)
		return ""

	case "sel":
		// 按住拖动：宿主把拖到的列号转过来，选区从锚点扩过去
		name, colText, ok := strings.Cut(req.Arg, ":")
		if !ok {
			return ""
		}
		if f := p.fieldByName(name); f != nil {
			f.SelExtend(f.CaretAtCol(atoiSafe(colText)))
			if f.HasSel() {
				p.say(l, fmt.Sprintf("已选中 %d 个字符：Ctrl+C 复制，Esc 取消", len([]rune(f.Selection()))), true)
			}
		}
		p.focus = p.ctlIndex("field", name)
		return ""

	case "mode":
		p.mode = req.Arg
		p.focus = p.ctlIndex("mode", req.Arg)
		if req.Arg == "local" {
			return p.say(l, "本地：连这台机器上跑的模型，多数不要密钥", true)
		}
		return p.say(l, "云端：连公网上的大模型服务，需要 API Key", true)

	case "preset":
		p.focus = p.ctlIndex("preset", req.Arg)
		return p.applyPreset(l, req.Arg)

	case "connect":
		p.focus = p.ctlIndex("connect", req.Arg)
		return p.connectLocal(l, req.Arg)

	case "wire":
		p.provider = req.Arg
		p.focus = p.ctlIndex("wire", req.Arg)
		return p.say(l, "协议："+wireLabel(req.Arg), true)

	case "model":
		p.model.SetValue(req.Arg)
		p.focus = p.ctlIndex("model", req.Arg)
		return p.say(l, "模型："+req.Arg+"（记得点「保存并生效」）", true)

	case "btn":
		switch req.Arg {
		case "scan":
			return p.scan(l)
		case "getmodels":
			return p.fetchModels(l)
		case "save":
			return p.save(l)
		case "test":
			return p.test(l)
		}
	}
	return ""
}

// ctlIndex 找一个控件在环上的下标（找不到返回 -1）。
func (p *llmPanel) ctlIndex(kind, arg string) int {
	for i, c := range p.ring {
		if c.kind == kind && c.arg == arg {
			return i
		}
	}
	return -1
}

func (p *llmPanel) fieldByName(name string) *plugin.Field {
	switch name {
	case fieldBaseURL:
		return &p.baseURL
	case fieldModel:
		return &p.model
	case fieldKey:
		return &p.apiKey
	case fieldVision:
		return &p.visionModel
	}
	return nil
}

// applyPreset 应用一个服务预设：填好地址与默认模型，焦点直接送到密钥框。
func (p *llmPanel) applyPreset(l *LLM, key string) string {
	it, ok := presetByKey(key)
	if !ok {
		return ""
	}
	p.presetKey = it.Key
	p.provider = it.Provider
	p.modelList = append([]string{}, it.Models...)
	p.modelsNote = ""
	p.baseURL.SetValue(it.BaseURL)
	p.model.SetValue(firstOf(it.Models))
	// 焦点一定落在密钥上：接下来要做的就是粘贴
	p.focus = p.ctlIndex("field", fieldKey)

	note := "已选 " + it.Label + "：粘贴 API Key 就行"
	switch {
	case it.Hint != "":
		note = "已选 " + it.Label + "：" + it.Hint
	case !it.NeedKey:
		note = "已选 " + it.Label + "：它不需要密钥，点「保存并生效」即可"
	case it.KeyURL != "":
		note = "已选 " + it.Label + "：粘贴 API Key 就行（密钥在 " + it.KeyURL + " 申请）"
	}
	// 本地应用的"怎么起、默认在哪"也要带出来 —— 连不上时这几行就是排查线索
	if it.Note != "" {
		note += " · " + it.Note
	}
	return p.say(l, note, true)
}

// connectLocal 把一条扫描结果回填进表单。
//
// 它填的是"连这个服务需要什么"：协议、地址、以及服务自报的第一个模型。
// 剩下的（要不要密钥、要不要换模型）用户自己看着办 —— 界面不替他下结论。
func (p *llmPanel) connectLocal(l *LLM, key string) string {
	for _, hit := range p.localFound {
		if hit.Key != key {
			continue
		}
		p.presetKey = key
		p.provider = firstNonEmpty(hit.Provider, "openai")
		p.baseURL.SetValue(hit.BaseURL)
		if len(hit.Models) > 0 {
			p.modelList = append([]string{}, hit.Models...)
			p.model.SetValue(hit.Models[0])
		}
		return p.say(l, "已回填 "+hit.Label+" 的地址与协议；确认后点「保存并生效」", true)
	}
	return ""
}

// scan 扫描本机常见 AI 端点。
func (p *llmPanel) scan(l *LLM) string {
	if p.localScanning {
		return ""
	}
	p.localScanning = true
	p.localScanned = true
	p.localNote = "正在扫描本机常见 AI 端点…"
	p.say(l, "正在扫描本机常见 AI 端点…", true)

	out, err := l.discover(nil)
	if err != nil {
		p.localScanning = false
		p.localNote = ""
		return p.say(l, "扫描失败："+err.Error(), false)
	}
	found, note := decodeDiscover(out)
	p.localScanning = false
	p.localFound = found
	p.localNote = note
	return p.say(l, note, true)
}

// decodeDiscover 把 discover 的返回转成认识的形状。
//
// 它返回的是 any（接口的通用形状）；这里刻意绕一次 JSON 而不是断言内部结构 ——
// 页面不该依赖 discover 内部那份 map 的键名，那是它自己的实现细节。
func decodeDiscover(out any) ([]localHit, string) {
	blob, err := json.Marshal(out)
	if err != nil {
		return nil, "解析扫描结果失败"
	}
	var res struct {
		Found []localHit `json:"found"`
		Note  string     `json:"note"`
	}
	if err := json.Unmarshal(blob, &res); err != nil {
		return nil, "解析扫描结果失败：" + err.Error()
	}
	if strings.TrimSpace(res.Note) == "" {
		res.Note = fmt.Sprintf("发现 %d 个本机 AI 服务", len(res.Found))
	}
	return res.Found, res.Note
}

// fetchModels 让 llm 去服务端要一份真实的模型清单。
func (p *llmPanel) fetchModels(l *LLM) string {
	if p.busy {
		return ""
	}
	p.busy = true
	p.modelsNote = "正在获取…"
	p.say(l, "正在向服务端要模型清单…", true)
	defer func() { p.busy = false }()

	params := map[string]any{}
	if s := strings.TrimSpace(p.provider); s != "" {
		params["provider"] = s
	}
	if s := strings.TrimSpace(p.baseURL.Value); s != "" {
		params["baseUrl"] = s
	}
	if s := strings.TrimSpace(p.apiKey.Value); s != "" && s != "-" {
		params["apiKey"] = s
	}
	blob, _ := json.Marshal(params)
	out, err := l.models(json.RawMessage(blob))
	if err != nil {
		p.modelsNote = "获取失败"
		return p.say(l, "获取模型失败："+err.Error(), false)
	}
	var res struct {
		Models []string `json:"models"`
		Note   string   `json:"note"`
	}
	if b, err := json.Marshal(out); err == nil {
		_ = json.Unmarshal(b, &res)
	}
	p.modelList = res.Models
	if len(res.Models) == 0 {
		p.modelsNote = "服务端没报模型名"
		return p.say(l, "服务端没报出模型名，请手动填写模型名", false)
	}
	p.modelsNote = res.Note
	if p.modelsNote == "" {
		p.modelsNote = fmt.Sprintf("已获取 %d 个，点一个即可", len(res.Models))
	}
	return p.say(l, p.modelsNote, true)
}

// save 保存配置。
//
// 只提交真正填过的字段：留空表示"不改动"，所以不会把已有密钥抹掉，
// 也不会因为"只想改主模型"就顺手清掉识图配置。
func (p *llmPanel) save(l *LLM) string {
	if p.busy {
		return ""
	}
	params := map[string]any{}
	if s := strings.TrimSpace(p.provider); s != "" && s != p.loadedProvider {
		params["provider"] = s
	}
	if s := strings.TrimSpace(p.baseURL.Value); s != "" && s != p.loadedBaseURL {
		params["baseUrl"] = s
	}
	if s := strings.TrimSpace(p.model.Value); s != "" && s != p.loadedModel {
		params["model"] = s
	}
	// 密钥：留空=不改动，减号=清空。框里从来不装明文，所以"空"只能解释成不改动。
	switch s := strings.TrimSpace(p.apiKey.Value); s {
	case "":
	case "-":
		params["apiKey"] = ""
	default:
		params["apiKey"] = s
	}
	// 识图模型：语义与密钥一致
	switch s := strings.TrimSpace(p.visionModel.Value); s {
	case "":
	case "-":
		params["visionModel"] = ""
	default:
		params["visionModel"] = s
	}
	if len(params) == 0 {
		return p.say(l, "没有可保存的改动", false)
	}

	p.busy = true
	defer func() { p.busy = false }()
	blob, _ := json.Marshal(params)
	if _, err := l.configure(json.RawMessage(blob)); err != nil {
		return p.say(l, "保存失败："+err.Error(), false)
	}
	// 保存成功后清掉明文并回读一次：框里不该继续留着刚粘的密钥。
	p.apiKey.Clear()
	p.load(l)
	return p.say(l, "已保存并生效：接下来每轮对话都按这份配置走", true)
}

// test 用当前配置真发一次请求。
//
// 它打的不是"配置写进去没有"，而是"这个地址 + 这套协议 + 这个模型到底通不通"——
// 后者才是用户真正想知道的那件事，而它只有发一次请求才知道。
func (p *llmPanel) test(l *LLM) string {
	if p.busy {
		return ""
	}
	p.busy = true
	p.say(l, "正在测试连接…", true)
	defer func() { p.busy = false }()

	out, err := l.chat(json.RawMessage(`{"messages":[{"role":"user","content":"回复 ok 两个字即可"}],"maxTokens":16}`))
	if err != nil {
		return p.say(l, "连接失败："+err.Error(), false)
	}
	var res struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Content  string `json:"content"`
	}
	if b, err := json.Marshal(out); err == nil {
		_ = json.Unmarshal(b, &res)
	}
	reply := strings.TrimSpace(res.Content)
	if rs := []rune(reply); len(rs) > 40 {
		reply = string(rs[:40]) + "…"
	}
	return p.say(l, fmt.Sprintf("连接正常（%s / %s）：%s", res.Provider, res.Model, reply), true)
}

// isLocalURL 判断地址是不是指向本机。
//
// 用它把左侧那一栏的初始状态定下来：打开这一页时看到的应该是"我连的那类服务"，
// 而不是永远从云端开始。
func isLocalURL(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, host := range []string{"127.0.0.1", "localhost", "0.0.0.0", "[::1]"} {
		if strings.Contains(s, host) {
			return true
		}
	}
	return false
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return -1
	}
	return n
}

// firstNonEmpty 取第一个非空（去空白后）的字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
