package main

import (
	"image/color"
	"os"

	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// 配色取自本机 DSH 的暗色令牌（@deepseek-ai/dsh-client-ui-theme
// src/styles/design-platform.css 里 body[data-ds-dark-theme] 一节）：
//
//	--dsw-alias-bg-base              rgb(21,21,23)   #151517
//	--dsw-specific-sidebar-fill      rgb(27,27,28)   #1B1B1C
//	--dsw-alias-bg-layer-2/输入框     rgb(44,44,46)   #2C2C2E
//	--dsw-alias-label-primary        rgb(249,250,251)
//	--dsw-alias-label-secondary      rgb(207,211,214)
//	--dsw-alias-label-tertiary       rgb(173,178,184)
//	--dsw-alias-border-l2            rgba(255,255,255,0.12)
//	--dsw-alias-interactive-bg-hover rgba(255,255,255,0.08)
//	--dsw-specific-sidebar-nav-item-active rgb(67,69,74)
//	--dsw-alias-state-business-primary     rgb(122,170,255)
var (
	cBg        = color.NRGBA{R: 0x15, G: 0x15, B: 0x17, A: 0xFF} // 主背景 bg-base
	cPanel     = color.NRGBA{R: 0x1B, G: 0x1B, B: 0x1C, A: 0xFF} // 侧栏 sidebar-fill
	cPanelAlt  = color.NRGBA{R: 0x2C, G: 0x2C, B: 0x2E, A: 0xFF} // 卡片 / 输入框 layer-2
	cBorder    = color.NRGBA{R: 0x2E, G: 0x2E, B: 0x30, A: 0xFF} // 0.5px 分隔线（白 12% 叠底）
	cHover     = color.NRGBA{R: 0x28, G: 0x28, B: 0x2A, A: 0xFF} // 悬浮底（白 8% 叠底）
	// cHoverLift 是输入卡片等浅一层面板上的悬浮底（白 8% 叠在 #2C2C2E 上），
	// 对应 DSH --dsw-alias-interactive-bg-hover。
	cHoverLift = color.NRGBA{R: 0x3A, G: 0x3A, B: 0x3C, A: 0xFF}
	// cBorderLift 是浅色面板上的描边（白 12% 叠在 #2C2C2E 上），
	// 对应 DSH --dsw-alias-border-l2，输入卡片用它才看得出卡片边缘。
	cBorderLift = color.NRGBA{R: 0x45, G: 0x45, B: 0x47, A: 0xFF}
	// 输入框左侧圆形「+」按钮（DSH --dsw-specific-selector 及其 hover）。
	cBtnFill  = color.NRGBA{R: 0x35, G: 0x36, B: 0x38, A: 0xFF}
	cBtnHover = color.NRGBA{R: 0x40, G: 0x41, B: 0x43, A: 0xFF}
	cNavActive = color.NRGBA{R: 0x43, G: 0x45, B: 0x4A, A: 0xFF} // 侧栏选中项
	cText      = color.NRGBA{R: 0xF9, G: 0xFA, B: 0xFB, A: 0xFF} // 主文字
	cTextDim   = color.NRGBA{R: 0xCF, G: 0xD3, B: 0xD6, A: 0xFF} // 次要文字
	cTextFaint = color.NRGBA{R: 0xAD, G: 0xB2, B: 0xB8, A: 0xFF} // 提示文字
	cAccent    = color.NRGBA{R: 0x7A, G: 0xAA, B: 0xFF, A: 0xFF} // 强调蓝（deepseek-400）
	cAccentDim = color.NRGBA{R: 0x3A, G: 0x4C, B: 0x6E, A: 0xFF} // 强调蓝（暗底）
	cUserBub   = color.NRGBA{R: 0x2C, G: 0x2C, B: 0x2E, A: 0xFF} // 用户气泡（layer-2）
	cAiBub     = color.NRGBA{R: 0x1F, G: 0x1F, B: 0x21, A: 0xFF} // 过程块（比底色略亮）
	cOk        = color.NRGBA{R: 0x4E, G: 0xC9, B: 0xB0, A: 0xFF} // 正常/在线绿
	cWarn      = color.NRGBA{R: 0xE0, G: 0xA4, B: 0x5C, A: 0xFF} // 警告橙
	cErr       = color.NRGBA{R: 0xE0, G: 0x6B, B: 0x6B, A: 0xFF} // 错误红
)

// 布局尺寸对齐 DSH（dsh-client-ui-layout/src/client/columns.ts 与各模块 CSS）：
//
//	SIDEBAR_DEFAULT 280 / SIDEBAR_MIN 264 / SIDEBAR_MAX 420
//	RIGHTBAR_MIN 300 / RIGHTBAR_DEFAULT_RATIO 0.45 / RIGHTBAR_MAX_RATIO 0.7
//	会话头 header 10px 上内边 + 30px 标题行 + 10px 下内边（无 tab 时）
//	聊天正文列 clamp(680, 64%, 920)，输入卡片 = 正文列 + 32（≤952）
//	圆角：sm 8 / md 12 / lg 16 / panel 28
const (
	navWidth       = 280 // 侧栏默认宽
	navWidthMin    = 264
	navWidthMax    = 420
	sessionWidth   = 240 // 保留兼容；主布局不再单独占一列
	panelWidth     = 300 // 右侧详情面板最小宽（DSH RIGHTBAR_MIN）
	panelMaxRatio  = 0.70
	panelDefRatio  = 0.45
	titleBarHeight = 36  // 自绘标题栏（Windows caption 行）
	topBarHeight   = 50  // 10 上 + 30 标题行 + 10 下
	chatMaxWidth   = 920 // 聊天正文列上限
	composerMaxW   = chatMaxWidth + 32
	// 输入卡片（对齐 DSH InputBar）：10 上内边 + 46 文本 + 12 间距 + 34 工具行
	// + 8 下内边 + 8 卡片底距 = 118。
	inputMinHeight = 118
	bubbleGap      = 12  // 消息间距（DSH --dsh-chat-flow-gap）
	fieldHeight    = 36  // 表单控件高
	rowGap         = 12
	radiusSm       = 8
	radiusMd       = 12
	radiusLg       = 16
	radiusPanel    = 28
)

// newTheme 建一个基于暗色配色的 material 主题。
//
// 字体必须显式注册：Gio 默认只带 Go 字族，中文靠系统回退，
// 回退是按整段 run 选的，混合 ASCII 的标签会整段落到缺字的字体上，
// 表现就是「设置」只画出「置」、图标变成方框（tofu）。
// 这里把微软雅黑（正文/中文）和 Segoe UI Symbol（几何符号）按序拼进集合，
// 中文与 ◈ ☺ ⌘ 之类符号都有确定来源。
func newTheme() *material.Theme {
	th := material.NewTheme()
	th.Fg = cText
	th.Bg = cBg
	th.TextSize = unit.Sp(14)
	faces := gofont.Collection()
	for _, p := range []string{
		`C:\Windows\Fonts\msyh.ttc`,
		`C:\Windows\Fonts\msyhbd.ttc`,
		`C:\Windows\Fonts\seguisym.ttf`,
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		ff, err := opentype.ParseCollection(data)
		if err != nil {
			continue
		}
		faces = append(faces, ff...)
	}
	th.Shaper = text.NewShaper(text.WithCollection(faces))
	return th
}
