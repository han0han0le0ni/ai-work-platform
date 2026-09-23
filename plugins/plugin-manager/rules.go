package main

import "aiwork/sdk/go/plugin"

// ---------------------------------------------------------------------------
// 「规则」页：把插件开发规范摊给人看。
//
// 这些规则不是这一页发明的 —— 核心在注册点 / manifest 校验点强制执行
// （core/registry/naming.go 的服务名、事件名，core/loader/manifest.go 的
// id / version / 权限名）。这一页只把它们按"怎么写才合法"摊开，方便
// 人（和写插件的 AI）照着填，省得撞上 -32602 再回来翻文档。
// ---------------------------------------------------------------------------

// rulesPanelID 是「规则」页的页面 id。
const rulesPanelID = "rules"

// rulesSection 是规则页的一段：标题 + 正文行。
type rulesSection struct {
	head string
	body []string
}

// rulesSections 是规则页的完整内容。顺序即显示顺序。
var rulesSections = []rulesSection{
	{head: "目录与安装", body: []string{
		"plugins/<名字>.plugin/",
		"目录名 = id 最后一段：com.ai.calc → calc.plugin",
	}},
	{head: "manifest 必填字段", body: []string{
		"id          反向域名     com.example.echo",
		"version     语义化版本   1.0.0",
		"apiVersion  协议版本     当前 1",
		"entry       入口文件     main / main.py / main.js",
		"runtime     运行时       binary python node java wasm",
		"compat.core 核心范围     >=1.0 <2.0",
	}},
	{head: "服务名（provides）", body: []string{
		"必须带版本后缀 .vN",
		"对：llm.v1   toolExecutor.v1",
		"错：llm（没有 .v1，注册被拒 -32602）",
	}},
	{head: "事件名", body: []string{
		"必须带命名空间 <命名空间>.<动作>",
		"对：chat.message   tool.before   core.log",
		"错：message（没有命名空间）",
	}},
	{head: "权限名（permissions）", body: []string{
		"小写 + 冒号分隔",
		"对：filesystem:read   network",
		"错：FileRead（大写，manifest 校验失败）",
	}},
	{head: "已知权限", body: []string{
		"network",
		"filesystem:read / filesystem:write / filesystem:delete",
		"process:spawn",
		"shell:open",
		"sandbox:use",
	}},
	{head: "自带界面（ui.page.*）", body: []string{
		"在 provides 里声明 ui.page.<名字>.v1",
		"实现 list / render / action / key 四个方法",
		"界面自动把它合成到标签栏，不用改界面",
	}},
}

// rulesRows 把规则内容铺成"样式 + 文本"的行表，供 render 逐行画。
//
// 铺在这里而不是 render 里，是为了让"一行占多宽"这件事和"哪几行是一个
// 分类"分开：render 只负责按滚动位置截一段出来画。
func rulesRows() []ruleRow {
	rows := []ruleRow{{style: plugin.SAccent + plugin.SBold, text: " 插件规则"}}
	rows = append(rows, ruleRow{})
	for _, sec := range rulesSections {
		rows = append(rows, ruleRow{style: plugin.SDim + plugin.SBold, text: " " + sec.head})
		for _, ln := range sec.body {
			rows = append(rows, ruleRow{text: "   " + ln})
		}
		rows = append(rows, ruleRow{})
	}
	return rows
}

// ruleRow 是规则页里的一行。
type ruleRow struct {
	style string
	text  string
}

// renderRules 画「规则」页。
//
// 这一页是静态文档：没有按钮、没有键盘焦点，只有滚轮翻阅。滚动由宿主管
// （它传 scroll 进来、按我们报的 maxScroll 夹滚轮），这里只把 scroll 起的
// 那一段截出来画。
func renderRules(req plugin.PanelRequest) plugin.PanelRender {
	w, h := req.W, req.H
	if w < 1 || h < 1 {
		return plugin.PanelRender{}
	}
	b := plugin.NewBlock(w, h)
	if w < 40 || h < 6 {
		b.At(0).Text(plugin.SWarn, " 窗口太小，放不下这一页")
		return b.Render(0, 0, "把窗口拉大些")
	}

	rows := rulesRows()
	maxScroll := maxInt(0, len(rows)-h)
	scroll := clampInt(req.Scroll, 0, maxScroll)
	for i := 0; i < h; i++ {
		if idx := scroll + i; idx < len(rows) {
			b.At(i).Text(plugin.Style(rows[idx].style), plugin.Truncate(rows[idx].text, w))
		}
	}
	// 这一页没有按钮、没有可编辑字段，键盘交给宿主的滚轮翻页即可。
	res := b.Render(scroll, maxScroll, "滚轮翻阅 · 这些规则由核心在注册与加载时强制执行")
	return res
}
