package main

// 功能页的原生绘制与交互。所有数据来自 bridge 快照，所有动作走 bridge 方法。
// 采用与聊天页一致的绝对定位 + Exact 约束 + 背景先画内容后画的模式。

import (
	"image"
	"image/color"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ---------- 通用表单辅助 ----------

// pageHeader 页面标题 + 说明。
func (u *UI) pageHeader(gtx layout.Context, title, subtitle string) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return label(gtx, u.th, title, unit.Sp(20), cText)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(3)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return label(gtx, u.th, subtitle, unit.Sp(11), cTextFaint)
		}),
	)
}

// noteLine 底部提示（错误/状态）。
func (u *UI) noteLine(gtx layout.Context, note string) layout.Dimensions {
	if strings.TrimSpace(note) == "" {
		return layout.Dimensions{}
	}
	return label(gtx, u.th, "· "+note, unit.Sp(11), cWarn)
}

// textField 一行表单：左侧标签 + 右侧输入框，返回输入框尺寸。
func (u *UI) textField(gtx layout.Context, title string, ed *widget.Editor, hint string, secret bool) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = 0
			return label(gtx, u.th, title, unit.Sp(13), cTextDim)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return u.inputField(gtx, ed, hint, secret)
		}),
	)
}

func (u *UI) inputField(gtx layout.Context, ed *widget.Editor, hint string, secret bool) layout.Dimensions {
	h := gtx.Dp(fieldHeight)
	gtx.Constraints.Min.Y = h
	gtx.Constraints.Max.Y = h
	return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		e := material.Editor(u.th, ed, hint)
		e.TextSize = unit.Sp(13)
		e.Color = cText
		e.HintColor = cTextFaint
		macro := op.Record(gtx.Ops)
		dims := e.Layout(gtx)
		body := macro.Stop()
		dims.Size.Y = h
		rr := clip.RRect{Rect: image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, h)}, SE: gtx.Dp(radiusMd), SW: gtx.Dp(radiusMd), NE: gtx.Dp(radiusMd), NW: gtx.Dp(radiusMd)}
		paint.FillShape(gtx.Ops, cPanel, rr.Op(gtx.Ops))
		body.Add(gtx.Ops)
		return dims
	})
}

// actionButton 主行动按钮。
func (u *UI) actionButton(gtx layout.Context, b *widget.Clickable, txt string, primary bool) layout.Dimensions {
	w := gtx.Dp(unit.Dp(92))
	h := gtx.Dp(fieldHeight)
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	bg := cPanelAlt
	if primary {
		bg = cAccentDim
	}
	if b.Hovered() {
		bg = cAccent
	}
	rr := clip.RRect{Rect: image.Rectangle{Max: image.Pt(w, h)}, SE: gtx.Dp(radiusMd), SW: gtx.Dp(radiusMd), NE: gtx.Dp(radiusMd), NW: gtx.Dp(radiusMd)}
	paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
	dims := layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return label(gtx, u.th, txt, unit.Sp(12), cText)
	})
	b.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return dims })
	return dims
}

// chip 小标签按钮。
func (u *UI) chip(gtx layout.Context, b *widget.Clickable, txt string, active bool) layout.Dimensions {
	h := gtx.Dp(unit.Dp(28))
	gtx.Constraints.Min.Y = h
	gtx.Constraints.Max.Y = h
	return layout.Inset{Right: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		macro := op.Record(gtx.Ops)
		textDims := label(gtx, u.th, txt, unit.Sp(11), cText)
		body := macro.Stop()
		w := textDims.Size.X + gtx.Dp(20)
		gtx.Constraints = layout.Exact(image.Pt(w, h))
		bg := cPanelAlt
		if active {
			bg = cAccentDim
		} else if b.Hovered() {
			bg = cPanel
		}
		rr := clip.RRect{Rect: image.Rectangle{Max: image.Pt(w, h)}, SE: gtx.Dp(14), SW: gtx.Dp(14), NE: gtx.Dp(14), NW: gtx.Dp(14)}
		paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
		stack := op.Offset(image.Pt(gtx.Dp(10), (h-textDims.Size.Y)/2)).Push(gtx.Ops)
		body.Add(gtx.Ops)
		stack.Pop()
		b.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(w, h)} })
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

// sectionTitle 分组标题（DSH 设置分区标题 15/600）。
func (u *UI) sectionTitle(gtx layout.Context, txt string) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, unit.Sp(15), txt)
			l.Color = cText
			l.Font.Weight = 600
			return l.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = 0
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(8))}
		}),
	)
}

// ============================================================
// 模型与 API（对齐 DSH ModelsSection）
// ============================================================
const (
	mfBaseURL = iota
	mfAPIKey
	mfModel
	mfSystem
	mfVisionModel
	mfCount
)

// ensureModelEditors 保证模型表单的编辑器已就绪。
//
// 打开设置的那一帧，侧栏按钮的点击是在事件处理末尾才生效的，分区事件
// （handleSettingsModalEvents → handleModelEvents）还没机会跑，而绘制已经
// 开始了；此时切片为空，直接取下标会 panic（index out of range [0]）。
func (u *UI) ensureModelEditors() {
	if u.modelInit && len(u.modelEditors) == mfCount {
		return
	}
	u.modelEditors = make([]widget.Editor, mfCount)
	for i := range u.modelEditors {
		u.modelEditors[i].SingleLine = i != mfSystem
	}
}

// loadModelDraft 把某条服务商读进编辑卡草稿。
//
// API 密钥不预填：DSH 的编辑卡从不把已保存的密钥读进输入框，只把它遮罩后
// 放在 placeholder 里；留空保存就表示"不改密钥"。
func (u *UI) loadModelDraft(p modelProviderView) {
	u.ensureModelEditors()
	u.modelEditors[mfBaseURL].SetText(p.BaseURL)
	u.modelEditors[mfAPIKey].SetText("")
	u.modelEditors[mfModel].SetText(p.Model)
	u.modelEditors[mfSystem].SetText(p.SystemPrompt)
	u.modelEditors[mfVisionModel].SetText(p.VisionModel)
}

func (u *UI) handleModelEvents(gtx layout.Context) {
	c := u.bridge.get().ModelCfg
	u.ensureModelEditors()
	u.modelInit = true

	// 每张服务商卡片：编辑 / 设为当前 / 删除（点删除后卡片内出现确认行）。
	for i := range c.Providers {
		p := c.Providers[i]
		if clickAt(&u.modelEditBtns, i).Clicked(gtx) {
			if u.modelEditID == p.ID {
				u.modelEditID = ""
			} else {
				u.modelEditID = p.ID
				u.loadModelDraft(p)
				u.modelCustomOpen = false
				u.modelAddOpen = false
			}
			u.modelRemoveID = ""
		}
		if clickAt(&u.modelUseBtns, i).Clicked(gtx) && !p.Active {
			u.bridge.setActiveProvider(p.ID)
		}
		if clickAt(&u.modelDeleteBtns, i).Clicked(gtx) {
			u.modelRemoveID = p.ID
		}
		if clickAt(&u.modelCancelBtns, i).Clicked(gtx) {
			u.modelRemoveID = ""
		}
		if clickAt(&u.modelConfirmBtns, i).Clicked(gtx) {
			if u.modelEditID == p.ID {
				u.modelEditID = ""
			}
			u.modelRemoveID = ""
			u.bridge.removeProvider(p.ID)
		}
	}
	if u.modelEditCancel.Clicked(gtx) {
		u.modelEditID = ""
	}
	// 点击「本机发现」里的一条：回填进添加卡片，等同原面板的「连接」按钮。
	for i := range c.Discovered {
		if clickAt(&u.modelFoundBtns, i).Clicked(gtx) {
			f := c.Discovered[i]
			u.modelAddOpen = true
			u.modelEditID = ""
			u.modelRemoveID = ""
			u.modelAddMode = 1
			u.modelAddPresetOpen = false
			u.modelAddProvider = f.Provider
			if u.modelAddProvider == "" {
				u.modelAddProvider = "openai"
			}
			u.modelAddLabel = f.Name
			u.modelAddBase.SetText(f.BaseURL)
			u.modelAddModel.SetText(f.Model)
			u.modelAddKey.SetText("")
		}
	}
	if u.modelCustomFold.Clicked(gtx) {
		u.modelCustomOpen = !u.modelCustomOpen
	}
	if u.modelDiscover.Clicked(gtx) {
		u.bridge.discoverModels()
	}
	if u.modelSave.Clicked(gtx) && u.modelEditID != "" {
		var cur modelProviderView
		for _, p := range c.Providers {
			if p.ID == u.modelEditID {
				cur = p
				break
			}
		}
		cur.BaseURL = strings.TrimSpace(u.modelEditors[mfBaseURL].Text())
		cur.APIKey = strings.TrimSpace(u.modelEditors[mfAPIKey].Text())
		cur.Model = strings.TrimSpace(u.modelEditors[mfModel].Text())
		cur.SystemPrompt = u.modelEditors[mfSystem].Text()
		cur.VisionModel = strings.TrimSpace(u.modelEditors[mfVisionModel].Text())
		u.bridge.saveProvider(u.modelEditID, cur)
		u.modelEditID = ""
	}

	// 添加模型提供商卡片。
	if u.modelAddBtn.Clicked(gtx) {
		u.modelAddOpen = true
		u.modelEditID = ""
		u.modelAddMode = 0
		if len(c.Presets) == 0 {
			u.modelAddMode = 1
		}
		u.modelAddPicked = 0
		u.modelAddPresetOpen = false
		u.modelAddKey.SetText("")
		u.modelAddBase.SetText("")
		u.modelAddModel.SetText("")
		u.modelAddProvider = "openai"
		u.modelAddLabel = ""
		if len(c.Presets) > 0 {
			u.modelAddBase.SetText(c.Presets[0].BaseURL)
			if len(c.Presets[0].Models) > 0 {
				u.modelAddModel.SetText(c.Presets[0].Models[0])
			}
		}
	}
	if u.modelAddCancel.Clicked(gtx) {
		u.modelAddOpen = false
	}
	if u.modelAddModeDir.Clicked(gtx) {
		u.modelAddMode = 0
		if p, ok := addPresetAt(u.modelAddPicked, c); ok {
			u.modelAddBase.SetText(p.BaseURL)
			u.modelAddModel.SetText("")
			if len(p.Models) > 0 {
				u.modelAddModel.SetText(p.Models[0])
			}
		}
	}
	if u.modelAddModeCustom.Clicked(gtx) {
		u.modelAddMode = 1
		// 切到自定义模式时清空目录带过来的地址与模型，避免看起来像"已填好"。
		u.modelAddBase.SetText("")
		u.modelAddModel.SetText("")
		u.modelAddProvider = "openai"
		u.modelAddLabel = ""
	}
	if u.modelAddMode == 0 {
		if u.modelAddPresetPick.Clicked(gtx) {
			u.modelAddPresetOpen = !u.modelAddPresetOpen
		}
		if len(u.modelAddPresetRows) < len(c.Presets) {
			u.modelAddPresetRows = append(u.modelAddPresetRows, make([]widget.Clickable, len(c.Presets)-len(u.modelAddPresetRows))...)
		}
		if u.modelAddPresetOpen {
			for i := range c.Presets {
				if u.modelAddPresetRows[i].Clicked(gtx) {
					u.modelAddPicked = i
					u.modelAddPresetOpen = false
					u.modelAddBase.SetText(c.Presets[i].BaseURL)
					u.modelAddModel.SetText("")
					if len(c.Presets[i].Models) > 0 {
						u.modelAddModel.SetText(c.Presets[i].Models[0])
					}
					break
				}
			}
		}
	}
	if u.modelAddApply.Clicked(gtx) {
		prov := u.modelAddProvider
		if prov == "" {
			prov = "openai"
		}
		e := modelProviderView{
			Provider: prov,
			Label:    strings.TrimSpace(u.modelAddLabel),
			BaseURL:  strings.TrimSpace(u.modelAddBase.Text()),
			APIKey:   strings.TrimSpace(u.modelAddKey.Text()),
			Model:    strings.TrimSpace(u.modelAddModel.Text()),
		}
		if u.modelAddMode == 0 {
			if p, ok := addPresetAt(u.modelAddPicked, c); ok {
				e.ID, e.Preset, e.Label, e.Provider = p.Key, p.Key, p.Label, p.Provider
				e.BaseURL = p.BaseURL
				if len(p.Models) > 0 {
					e.Model = p.Models[0]
				}
			}
		}
		if e.Label == "" {
			e.Label = "自定义服务商"
		}
		if e.ID == "" {
			e.ID = "custom"
			e.Preset = ""
		}
		u.bridge.addProvider(e)
		u.modelAddOpen = false
	}
}

// ============================================================
// 角色页
// ============================================================
const (
	rfName = iota
	rfDesc
	rfPersonality
	rfScenario
	rfGreeting
	rfMemory
	rfCount
)

func (u *UI) handleRoleEvents(gtx layout.Context) {
	c := u.bridge.get().Roles
	if len(u.roleActivateBtns) < len(c.Roles) {
		u.roleActivateBtns = append(u.roleActivateBtns, make([]widget.Clickable, len(c.Roles)-len(u.roleActivateBtns))...)
	}
	if len(u.roleDeleteBtns) < len(c.Roles) {
		u.roleDeleteBtns = append(u.roleDeleteBtns, make([]widget.Clickable, len(c.Roles)-len(u.roleDeleteBtns))...)
	}
	for i, r := range c.Roles {
		if u.roleActivateBtns[i].Clicked(gtx) {
			if r.Active {
				u.bridge.deactivateRole()
			} else {
				u.bridge.activateRole(r.ID)
			}
		}
		if u.roleDeleteBtns[i].Clicked(gtx) {
			u.bridge.deleteRole(r.ID)
		}
	}
	if u.roleNewBtn.Clicked(gtx) {
		u.showRoleEditor = true
		u.editingRoleID = ""
		u.resetRoleEditors(roleCard{})
	}
	if u.roleSaveBtn.Clicked(gtx) && u.showRoleEditor {
		u.bridge.saveRole(roleCard{
			ID:   u.editingRoleID,
			Name: u.roleEditors[rfName].Text(), Description: u.roleEditors[rfDesc].Text(),
			Personality: u.roleEditors[rfPersonality].Text(), Scenario: u.roleEditors[rfScenario].Text(),
			Greeting: u.roleEditors[rfGreeting].Text(), Memory: u.roleEditors[rfMemory].Text(),
		})
		u.showRoleEditor = false
	}
}

func (u *UI) resetRoleEditors(c roleCard) {
	u.roleEditors = make([]widget.Editor, rfCount)
	for i := range u.roleEditors {
		u.roleEditors[i].SingleLine = i != rfMemory
	}
	u.roleEditors[rfName].SetText(c.Name)
	u.roleEditors[rfDesc].SetText(c.Description)
	u.roleEditors[rfPersonality].SetText(c.Personality)
	u.roleEditors[rfScenario].SetText(c.Scenario)
	u.roleEditors[rfGreeting].SetText(c.Greeting)
	u.roleEditors[rfMemory].SetText(c.Memory)
}

// ============================================================
// 设置弹层：对齐 DSH SettingsRoot.module.css
//
//	面板 800×800 居中（窗口不足时四周留 24），圆角 28
//	左导航 188，导航格 40 高、圆角 12、格间距 4
//	右侧选项区 padding 0 24 24，整体可滚动
//
// ============================================================
var (
	settingsSections = []string{"模型与 API", "运行", "数据目录", "预算与工具", "用量", "插件规则"}
	settingsIcons    = []string{"◈", "⟳", "▤", "◎", "◔", "§"}
)

// settingsRulesSec 是「插件规则」在设置左栏里的下标。
// 它由插件管理器提供内容，插件不在线时整个分区隐藏。
const (
	settingsBudgetSec = 3
	settingsUsageSec  = 4
	settingsRulesSec  = 5
	rulesPanelService = "ui.page.pluginmanager.v1"
	rulesPanelID      = "rules"
)

// liveSettingsSections 返回当前可用的设置分区下标。
func (u *UI) liveSettingsSections() []int {
	out := make([]int, 0, len(settingsSections))
	for i := range settingsSections {
		if i == settingsRulesSec && !u.bridge.hasService(rulesPanelService) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// normalizeSettingsSec 防止停在一个已经隐藏的分区上。
func (u *UI) normalizeSettingsSec() {
	for _, i := range u.liveSettingsSections() {
		if i == u.settingsSec {
			return
		}
	}
	u.settingsSec = 0
}

func (u *UI) drawSettingsModal(gtx layout.Context) {
	W, H := gtx.Constraints.Max.X, gtx.Constraints.Max.Y

	// 遮罩：半透明铺满（DSH 暗色 --dsw-alias-bg-mask-1 = 黑 50%）。
	paint.FillShape(gtx.Ops, color.NRGBA{A: 0x80}, clip.Rect{Max: image.Pt(W, H)}.Op())

	pw, ph := gtx.Dp(unit.Dp(800)), gtx.Dp(unit.Dp(800))
	if max := W - 2*gtx.Dp(24); pw > max {
		pw = max
	}
	if max := H - 2*gtx.Dp(24); ph > max {
		ph = max
	}
	if pw < gtx.Dp(360) || ph < gtx.Dp(260) {
		return
	}
	px, py := (W-pw)/2, (H-ph)/2

	// 只在弹层外面放关闭热区：整块铺满会把弹层内部的点击也吞掉，
	// 点左栏分区也会被当成点遮罩直接关掉。
	maskArea := func(x, y, w, h int, b *widget.Clickable) {
		if w <= 0 || h <= 0 {
			return
		}
		u.place(gtx, x, y, w, h, func(g layout.Context) {
			g.Constraints = layout.Exact(image.Pt(w, h))
			b.Layout(g, func(layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(w, h)}
			})
		})
	}
	maskArea(0, 0, W, py, &u.settingsMaskT)
	maskArea(0, py+ph, W, H-py-ph, &u.settingsMaskB)
	maskArea(0, py, px, ph, &u.settingsMaskL)
	maskArea(px+pw, py, W-px-pw, ph, &u.settingsMaskR)

	u.place(gtx, px, py, pw, ph, func(g layout.Context) {
		r := g.Dp(unit.Dp(radiusPanel))
		rr := clip.RRect{Rect: image.Rectangle{Max: image.Pt(pw, ph)}, SE: r, SW: r, NE: r, NW: r}
		paint.FillShape(g.Ops, cPanelAlt, rr.Op(g.Ops))
		st := rr.Push(g.Ops)
		defer st.Pop()

		headerH := g.Dp(unit.Dp(54))
		navW := g.Dp(unit.Dp(188))
		u.place(g, 0, 0, pw, headerH, func(h layout.Context) { u.settingsModalHeader(h) })
		u.place(g, 0, headerH, navW, ph-headerH, func(n layout.Context) { u.settingsModalNav(n) })
		u.place(g, navW, headerH, pw-navW, ph-headerH, func(o layout.Context) { u.settingsModalOptions(o) })
	})
}

func (u *UI) settingsModalHeader(gtx layout.Context) layout.Dimensions {
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return layout.Inset{Left: unit.Dp(24), Right: unit.Dp(14), Top: unit.Dp(20), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Start}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, unit.Sp(16), "设置")
				l.Color = cText
				l.Font.Weight = 500
				return l.Layout(gtx)
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d := gtx.Dp(unit.Dp(28))
				gtx.Constraints = layout.Exact(image.Pt(d, d))
				return u.settingsClose.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					bg := cPanelAlt
					if u.settingsClose.Hovered() {
						bg = cHover
					}
					drawRBoxFill(gtx, bg, unit.Dp(radiusSm), d, d)
					gtx.Constraints = layout.Exact(image.Pt(d, d))
					return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						l := material.Label(u.th, unit.Sp(18), "×")
						l.Color = cTextDim
						return l.Layout(gtx)
					})
				})
			}),
		)
	})
}

func (u *UI) settingsModalNav(gtx layout.Context) layout.Dimensions {
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		children := make([]layout.FlexChild, 0, len(settingsSections))
		for _, i := range u.liveSettingsSections() {
			i := i
			name := settingsSections[i]
			icon := settingsIcons[i]
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				rowH := gtx.Dp(unit.Dp(40))
				// 多给 4dp，让 Inset 的 Bottom 变成格间距，格子本身仍是 40。
				gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, rowH+gtx.Dp(4)))
				return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					bw := gtx.Constraints.Max.X
					b := clickAt(&u.settingsNav, i)
					bg := cPanelAlt
					if u.settingsSec == i {
						bg = cNavActive
					} else if b.Hovered() {
						bg = cHover
					}
					return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						drawRBoxFill(gtx, bg, unit.Dp(radiusMd), bw, rowH)
						gtx.Constraints = layout.Exact(image.Pt(bw, rowH))
						// DSH .navCell：padding 9 16 9 12，图标 + 文字 gap 8，文字 14/22。
						return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return label(gtx, u.th, icon, unit.Sp(15), cText)
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
										return label(gtx, u.th, name, unit.Sp(14), cText)
									})
								}),
							)
						})
					})
				})
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

func (u *UI) settingsModalOptions(gtx layout.Context) layout.Dimensions {
	u.normalizeSettingsSec()
	return u.vscroll(gtx, "settings-modal", func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(24), Right: unit.Dp(24), Bottom: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			switch u.settingsSec {
			case 1:
				return u.settingsRuntimeSection(gtx)
			case 2:
				return u.settingsDataSection(gtx)
			case settingsBudgetSec:
				return u.settingsBudgetSection(gtx)
			case settingsUsageSec:
				return u.settingsUsageSection(gtx)
			case settingsRulesSec:
				return u.settingsRulesSection(gtx)
			}
			return u.settingsModelsSection(gtx)
		})
	})
}

// settingsRulesSection 插件设计规则：内容由插件管理器的面板服务提供。
// 纯文档、没有命中区，直接按行渲染，滚动交给外层滚轮。
func (u *UI) settingsRulesSection(gtx layout.Context) layout.Dimensions {
	if !u.rulesLoaded {
		u.rulesLoaded = true
		u.rulesFrame = u.bridge.renderPanelPage(panelPage{
			Service: rulesPanelService, ID: rulesPanelID, Title: "规则",
		}, 60, 200, 0)
	}
	frame := u.rulesFrame
	// 面板按请求高度补空行（这里要了 200 行），尾部全是空串。
	// 不裁掉的话设置区底下会挂一大片空白，滚轮能一直往下滚。
	lines := frame.Lines
	for len(lines) > 0 && strings.TrimSpace(stripANSI(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.settingsHeading(gtx, "插件设计规则", "写插件前先读：该做什么、不该做什么。")
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
	}
	lineH := gtx.Dp(unit.Dp(19))
	for _, raw := range lines {
		txt := stripANSI(raw)
		if strings.TrimSpace(txt) == "" {
			txt = " "
		}
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = 0
			l := material.Label(u.th, unit.Sp(11), txt)
			l.Color = cText
			l.MaxLines = 1
			d := l.Layout(gtx)
			if d.Size.Y < lineH {
				d.Size.Y = lineH
			}
			return d
		}))
	}
	if frame.Note != "" {
		children = append(children,
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.noteLine(gtx, frame.Note)
			}),
		)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// settingsModelsSection 模型与 API。
//
// 结构逐条对齐 DSH 的 ModelsSection.module.css / ModelsSection.tsx：
//
//	标题 16/500 + 说明 14 tertiary
//	服务商卡片：0.5px 描边 + 卡片底 + xl 圆角，行内「名称 + 密钥状态点 + 编辑」
//	编辑卡：module 底色、14/16 内距；字段 label 12/500 + 32 高输入框
//	       「自定义设置」折叠（顶部细分隔线 + ▸/▾）
//	       底部右对齐「取消 / 保存」，按钮 36 高
//	添加入口：整行虚线 44 高；展开后是「目录预设 / 自定义 API」分段 + 表单
func (u *UI) settingsModelsSection(gtx layout.Context) layout.Dimensions {
	u.ensureModelEditors()
	c := u.bridge.get().ModelCfg
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.settingsHeadingMd(gtx, "模型与 API", "填入提供商的 API 密钥即可使用其模型。")
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
	}
	for i := range c.Providers {
		i := i
		if i > 0 {
			children = append(children, layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout))
		}
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			list := u.bridge.get().ModelCfg.Providers
			if i >= len(list) {
				return layout.Dimensions{}
			}
			return u.modelProviderCard(gtx, list[i], i)
		}))
	}
	children = append(children,
		layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if u.modelAddOpen {
				return u.modelAddCard(gtx, c)
			}
			if len(c.Presets) == 0 {
				return layout.Dimensions{}
			}
			return u.modelAddEntry(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.modelNote(gtx, c.Note) }),
	)
	if len(c.Discovered) > 0 {
		rows := make([]layout.Widget, 0, len(c.Discovered)*2)
		for i := range c.Discovered {
			f := c.Discovered[i]
			txt := f.Name + " · " + f.Model
			sub := f.BaseURL
			if i > 0 {
				rows = append(rows, func(gtx layout.Context) layout.Dimensions { return u.hairline(gtx, 0) })
			}
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				b := clickAt(&u.modelFoundBtns, i)
				return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					w := gtx.Constraints.Max.X
					macro := op.Record(gtx.Ops)
					dims := layout.Inset{Top: unit.Dp(9), Bottom: unit.Dp(9)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								full := gtx.Constraints.Max.X
								gtx.Constraints.Min.X = 0
								d := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return label(gtx, u.th, txt, unit.Sp(13), cText)
									}),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return label(gtx, u.th, sub, unit.Sp(11), cTextFaint)
									}),
								)
								return layout.Dimensions{Size: image.Pt(full, d.Size.Y)}
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return label(gtx, u.th, "连接 ›", unit.Sp(12), cTextFaint)
							}),
						)
					})
					body := macro.Stop()
					if b.Hovered() {
						drawRBoxFill(gtx, cHover, unit.Dp(radiusSm), w, dims.Size.Y)
					}
					body.Add(gtx.Ops)
					return dims
				})
			})
		}
		children = append(children,
			layout.Rigid(layout.Spacer{Height: unit.Dp(18)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.settingsGroupTitle(gtx, "本机发现")
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return vstack(gtx, rows)
			}),
		)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// settingsHeadingMd DSH ModelsSection 的标题：16/500 + 14 tertiary 说明。
func (u *UI) settingsHeadingMd(gtx layout.Context, title, intro string) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, unit.Sp(16), title)
			l.Color = cText
			l.Font.Weight = 500
			return l.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, unit.Sp(14), intro)
			l.Color = cTextFaint
			l.WrapPolicy = text.WrapWords
			return l.Layout(gtx)
		}),
	)
}

// modelProviderCard 一条服务商：行头（名称 / 状态 / 操作）+ 行内编辑卡 + 删除确认行。
func (u *UI) modelProviderCard(gtx layout.Context, p modelProviderView, i int) layout.Dimensions {
	w := gtx.Constraints.Max.X
	macro := op.Record(gtx.Ops)
	dims := layout.Inset{Left: unit.Dp(14), Right: unit.Dp(14), Top: unit.Dp(12), Bottom: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, u.th, p.Label, unit.Sp(14), cText)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return u.modelDot(gtx, p.HasKey)
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						txt, col := "", cTextFaint
						switch {
						case p.Active:
							txt, col = "使用中", cOk
						case p.Preset == "":
							txt = "自定义"
						}
						if txt == "" {
							return layout.Dimensions{}
						}
						return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return label(gtx, u.th, txt, unit.Sp(11), col)
						})
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Dimensions{Size: gtx.Constraints.Min}
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if p.Active {
							return layout.Dimensions{}
						}
						return u.modelMiniButton(gtx, clickAt(&u.modelUseBtns, i), "设为当前")
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return u.modelMiniButton(gtx, clickAt(&u.modelEditBtns, i), "编辑")
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return u.modelMiniButton(gtx, clickAt(&u.modelDeleteBtns, i), "删除")
						})
					}),
				)
			}),
		}
		if u.modelRemoveID == p.ID {
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return label(gtx, u.th, "移除后，已保存的密钥会一起删除。", unit.Sp(12), cWarn)
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return layout.Dimensions{Size: gtx.Constraints.Min}
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return u.modelMiniButton(gtx, clickAt(&u.modelCancelBtns, i), "取消")
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return u.modelMiniButton(gtx, clickAt(&u.modelConfirmBtns, i), "移除")
							})
						}),
					)
				})
			}))
		}
		if u.modelEditID == p.ID {
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return u.modelEditorCard(gtx, p)
				})
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
	body := macro.Stop()
	drawRBoxOutline(gtx, cPanelAlt, cBorder, unit.Dp(radiusLg), w, dims.Size.Y)
	body.Add(gtx.Ops)
	return layout.Dimensions{Size: image.Pt(w, dims.Size.Y)}
}

// modelEditorCard 行内编辑卡：密钥 + 自定义设置折叠 + 右对齐操作行。
func (u *UI) modelEditorCard(gtx layout.Context, p modelProviderView) layout.Dimensions {
	w := gtx.Constraints.Max.X
	macro := op.Record(gtx.Ops)
	dims := layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16), Top: unit.Dp(14), Bottom: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Baseline}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, u.th, p.Label, unit.Sp(14), cText)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if p.Provider == "" || p.Provider == p.Label {
							return layout.Dimensions{}
						}
						return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return label(gtx, u.th, p.Provider, unit.Sp(12), cTextFaint)
						})
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.modelField(gtx, "API 密钥", func(gtx layout.Context) layout.Dimensions {
					return u.modelInput(gtx, &u.modelEditors[mfAPIKey], maskKey(p.APIKey))
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.modelFoldRow(gtx) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !u.modelCustomOpen {
					return layout.Dimensions{}
				}
				return u.modelAdvancedFields(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Dimensions{Size: gtx.Constraints.Min}
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.modelButton(gtx, &u.modelEditCancel, "取消", false)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.modelButton(gtx, &u.modelSave, "保存", true)
					}),
				)
			}),
		)
	})
	body := macro.Stop()
	drawRBoxFill(gtx, cAiBub, unit.Dp(radiusLg), w, dims.Size.Y)
	body.Add(gtx.Ops)
	return layout.Dimensions{Size: image.Pt(w, dims.Size.Y)}
}

// modelAdvancedFields 「自定义设置」展开后的字段，顺序对齐 DSH：
// 接口地址 → 模型目录（这里拆成模型名 / 识图模型）→ 系统提示词。
func (u *UI) modelAdvancedFields(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.modelField(gtx, "API 地址", func(gtx layout.Context) layout.Dimensions {
				return u.modelInput(gtx, &u.modelEditors[mfBaseURL], "https://…")
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.modelField(gtx, "模型", func(gtx layout.Context) layout.Dimensions {
				return u.modelInput(gtx, &u.modelEditors[mfModel], "deepseek-chat")
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.modelField(gtx, "识图模型", func(gtx layout.Context) layout.Dimensions {
				return u.modelInput(gtx, &u.modelEditors[mfVisionModel], "留空表示不支持识图")
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.modelField(gtx, "系统提示词", func(gtx layout.Context) layout.Dimensions {
				return u.modelTextArea(gtx, &u.modelEditors[mfSystem], "例如：回答保持简洁，先给结论。")
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.modelLinkButton(gtx, &u.modelDiscover, "从本机扫描可用服务")
		}),
	)
}

// modelFoldRow 编辑卡里的「自定义设置」折叠行。
func (u *UI) modelFoldRow(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.hairline(gtx, 0)
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			h := gtx.Dp(unit.Dp(22))
			w := gtx.Constraints.Max.X
			gtx.Constraints = layout.Exact(image.Pt(w, h))
			return u.modelCustomFold.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				arrow := "▸"
				if u.modelCustomOpen {
					arrow = "▾"
				}
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, u.th, arrow, unit.Sp(10), cTextFaint)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := material.Label(u.th, unit.Sp(12), "自定义设置")
						l.Color = cTextDim
						l.Font.Weight = 500
						return l.Layout(gtx)
					}),
				)
			})
		}),
	)
}

// modelAddEntry 服务商列表末尾的虚线添加入口（DSH 44 高、整行、虚线）。
func (u *UI) modelAddEntry(gtx layout.Context) layout.Dimensions {
	w := gtx.Constraints.Max.X
	h := gtx.Dp(unit.Dp(44))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return u.modelAddBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if u.modelAddBtn.Hovered() {
			drawRBoxFill(gtx, cHover, unit.Dp(radiusLg), w, h)
		}
		drawDashedOutline(gtx, cBorder, w, h)
		gtx.Constraints = layout.Exact(image.Pt(w, h))
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, "＋", unit.Sp(14), cTextDim)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, "添加模型提供商", unit.Sp(14), cTextDim)
				}),
			)
		})
	})
}

// modelAddCard 添加卡片：模式分段 + 说明 + 表单 + 右对齐操作行。
func (u *UI) modelAddCard(gtx layout.Context, c modelConfigView) layout.Dimensions {
	w := gtx.Constraints.Max.X
	macro := op.Record(gtx.Ops)
	dims := layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16), Top: unit.Dp(14), Bottom: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		children := make([]layout.FlexChild, 0, 14)
		if len(c.Presets) > 0 {
			children = append(children,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.modelSegmented(gtx)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			)
		}
		hint := "从内置目录中选择服务商，填入其 API 密钥即可使用。"
		if u.modelAddMode == 1 {
			hint = "连接中转站、自部署服务或其他 OpenAI 兼容接口，需填写 API 地址与模型。"
		}
		children = append(children,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return label(gtx, u.th, hint, unit.Sp(12), cTextFaint)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
		)
		if u.modelAddMode == 0 {
			p, ok := addPresetAt(u.modelAddPicked, c)
			label2 := "未选择"
			if ok {
				label2 = p.Label
			}
			children = append(children,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.modelField(gtx, "提供商", func(gtx layout.Context) layout.Dimensions {
						return u.settingsSelect(gtx, &u.modelAddPresetPick, label2, u.modelAddPresetOpen)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !u.modelAddPresetOpen || len(c.Presets) == 0 {
						return layout.Dimensions{}
					}
					return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return u.modelPresetDropdown(gtx, c.Presets, p.BaseURL)
					})
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !ok {
						return layout.Dimensions{}
					}
					info := "接口地址 " + p.BaseURL
					model := ""
					if len(p.Models) > 0 {
						model = p.Models[0]
					}
					if model != "" {
						info += " · 模型 " + model
					}
					return label(gtx, u.th, info, unit.Sp(12), cTextFaint)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			)
		} else {
			children = append(children,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.modelField(gtx, "API 地址", func(gtx layout.Context) layout.Dimensions {
						return u.modelInput(gtx, &u.modelAddBase, "https://…")
					})
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.modelField(gtx, "模型", func(gtx layout.Context) layout.Dimensions {
						return u.modelInput(gtx, &u.modelAddModel, "deepseek-chat")
					})
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			)
		}
		children = append(children,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.modelField(gtx, "API 密钥", func(gtx layout.Context) layout.Dimensions {
					return u.modelInput(gtx, &u.modelAddKey, "输入 API 密钥")
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if u.modelAddMode != 1 {
					return layout.Dimensions{}
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.modelLinkButton(gtx, &u.modelDiscover, "从本机扫描可用服务")
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Dimensions{Size: gtx.Constraints.Min}
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.modelButton(gtx, &u.modelAddCancel, "取消", false)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.modelButton(gtx, &u.modelAddApply, "添加", true)
					}),
				)
			}),
		)
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
	body := macro.Stop()
	drawRBoxFill(gtx, cAiBub, unit.Dp(radiusLg), w, dims.Size.Y)
	body.Add(gtx.Ops)
	return layout.Dimensions{Size: image.Pt(w, dims.Size.Y)}
}

// modelSegmented 添加卡片的模式切换：两段各自贴合文字宽度（DSH 的分段控件）。
func (u *UI) modelSegmented(gtx layout.Context) layout.Dimensions {
	h := gtx.Dp(unit.Dp(28))
	labels := []string{"第三方模型提供商", "自定义模型 API"}
	btns := []*widget.Clickable{&u.modelAddModeDir, &u.modelAddModeCustom}
	children := make([]layout.FlexChild, 0, 3)
	for i := range labels {
		i := i
		macro := op.Record(gtx.Ops)
		textDims := label(gtx, u.th, labels[i], unit.Sp(12), cText)
		body := macro.Stop()
		w := textDims.Size.X + gtx.Dp(20)
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(w, h))
			return btns[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				bg := color.NRGBA{}
				if u.modelAddMode == i {
					bg = cNavActive
				} else if btns[i].Hovered() {
					bg = cHover
				}
				if bg.A != 0 {
					drawRBoxFill(gtx, bg, unit.Dp(radiusSm), w, h)
				}
				stack := op.Offset(image.Pt((w-textDims.Size.X)/2, (h-textDims.Size.Y)/2)).Push(gtx.Ops)
				body.Add(gtx.Ops)
				stack.Pop()
				return layout.Dimensions{Size: image.Pt(w, h)}
			})
		}))
		if i == 0 {
			children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout))
		}
	}
	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, children...)
}

// addPresetAt 返回目录模式下选中的预设（下标越界时退回第一个）。
func addPresetAt(picked int, c modelConfigView) (presetView, bool) {
	if len(c.Presets) == 0 {
		return presetView{}, false
	}
	if picked >= 0 && picked < len(c.Presets) {
		return c.Presets[picked], true
	}
	return c.Presets[0], true
}

// modelField 一组字段：12/500 标签 + 6 间距 + 控件。
func (u *UI) modelField(gtx layout.Context, name string, control layout.Widget) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, unit.Sp(12), name)
			l.Color = cTextDim
			l.Font.Weight = 500
			return l.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
		layout.Rigid(control),
	)
}

// modelInput DSH 输入框：32 高、md 圆角、layer-1 底色。
func (u *UI) modelInput(gtx layout.Context, ed *widget.Editor, hint string) layout.Dimensions {
	h := gtx.Dp(unit.Dp(32))
	w := gtx.Constraints.Max.X
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	drawRBoxOutline(gtx, cBg, cBorder, unit.Dp(radiusMd), w, h)
	layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		e := material.Editor(u.th, ed, hint)
		e.TextSize = unit.Sp(14)
		e.Color = cText
		e.HintColor = cTextFaint
		return e.Layout(gtx)
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// modelTextArea 多行输入（系统提示词）。
func (u *UI) modelTextArea(gtx layout.Context, ed *widget.Editor, hint string) layout.Dimensions {
	h := gtx.Dp(unit.Dp(84))
	w := gtx.Constraints.Max.X
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	drawRBoxOutline(gtx, cBg, cBorder, unit.Dp(radiusMd), w, h)
	layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10), Top: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		e := material.Editor(u.th, ed, hint)
		e.TextSize = unit.Sp(14)
		e.Color = cText
		e.HintColor = cTextFaint
		return e.Layout(gtx)
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// modelMiniButton 服务商行里的紧凑按钮（28 高、12sp，对齐 DSH 的 compact Button）。
func (u *UI) modelMiniButton(gtx layout.Context, b *widget.Clickable, txt string) layout.Dimensions {
	h := gtx.Dp(unit.Dp(28))
	macro := op.Record(gtx.Ops)
	textDims := label(gtx, u.th, txt, unit.Sp(12), cText)
	body := macro.Stop()
	w := textDims.Size.X + gtx.Dp(20)
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		bg := color.NRGBA{}
		if b.Hovered() {
			bg = cHover
		}
		drawRBoxOutline(gtx, bg, cBorder, unit.Dp(radiusSm), w, h)
		stack := op.Offset(image.Pt(gtx.Dp(10), (h-textDims.Size.Y)/2)).Push(gtx.Ops)
		body.Add(gtx.Ops)
		stack.Pop()
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// modelButton 编辑卡 / 添加卡的底部按钮（DSH 36 高、14sp）。
func (u *UI) modelButton(gtx layout.Context, b *widget.Clickable, txt string, primary bool) layout.Dimensions {
	h := gtx.Dp(unit.Dp(36))
	macro := op.Record(gtx.Ops)
	textDims := label(gtx, u.th, txt, unit.Sp(14), cText)
	body := macro.Stop()
	w := textDims.Size.X + gtx.Dp(28)
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if primary {
			bg := cAccentDim
			if b.Hovered() {
				bg = cAccent
			}
			drawRBoxFill(gtx, bg, unit.Dp(radiusMd), w, h)
		} else {
			bg := color.NRGBA{}
			if b.Hovered() {
				bg = cHover
			}
			drawRBoxOutline(gtx, bg, cBorder, unit.Dp(radiusMd), w, h)
		}
		stack := op.Offset(image.Pt((w-textDims.Size.X)/2, (h-textDims.Size.Y)/2)).Push(gtx.Ops)
		body.Add(gtx.Ops)
		stack.Pop()
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// modelLinkButton 无边框的文字按钮（扫描本机），对齐 DSH 的 linkButton。
func (u *UI) modelLinkButton(gtx layout.Context, b *widget.Clickable, txt string) layout.Dimensions {
	h := gtx.Dp(unit.Dp(28))
	macro := op.Record(gtx.Ops)
	textDims := label(gtx, u.th, txt, unit.Sp(12), cTextFaint)
	body := macro.Stop()
	w := textDims.Size.X + gtx.Dp(20)
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if b.Hovered() {
			drawRBoxFill(gtx, cHover, unit.Dp(radiusSm), w, h)
		}
		stack := op.Offset(image.Pt(gtx.Dp(10), (h-textDims.Size.Y)/2)).Push(gtx.Ops)
		body.Add(gtx.Ops)
		stack.Pop()
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// modelDot 密钥状态点：已配置绿、缺失红（DSH 8px credentialDot）。
func (u *UI) modelDot(gtx layout.Context, ok bool) layout.Dimensions {
	d := gtx.Dp(unit.Dp(8))
	gtx.Constraints = layout.Exact(image.Pt(d, d))
	col := cErr
	if ok {
		col = cOk
	}
	paint.FillShape(gtx.Ops, col, clip.Ellipse(image.Rectangle{Max: image.Pt(d, d)}).Op(gtx.Ops))
	return layout.Dimensions{Size: image.Pt(d, d)}
}

// modelNote 底部状态行：保存成功用绿色，其余沿用警告色。
func (u *UI) modelNote(gtx layout.Context, note string) layout.Dimensions {
	if strings.TrimSpace(note) == "" {
		return layout.Dimensions{}
	}
	col := cWarn
	if strings.Contains(note, "已保存") {
		col = cOk
	}
	return label(gtx, u.th, "· "+note, unit.Sp(12), col)
}

// maskKey 已保存密钥的遮罩显示；不把密钥读进输入框，只放 placeholder。
func maskKey(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return "输入 API 密钥"
	}
	if len(k) <= 10 {
		return "••••••"
	}
	return k[:3] + "••••" + k[len(k)-4:]
}

// drawDashedOutline 虚线描边：Gio 没有虚线 API，用短线段沿四条边铺。
func drawDashedOutline(gtx layout.Context, col color.NRGBA, w, h int) {
	seg, gap := gtx.Dp(unit.Dp(4)), gtx.Dp(unit.Dp(4))
	t := gtx.Dp(unit.Dp(1))
	if t < 1 {
		t = 1
	}
	for x := 0; x < w; x += seg + gap {
		ww := seg
		if x+ww > w {
			ww = w - x
		}
		if ww <= 0 {
			break
		}
		paint.FillShape(gtx.Ops, col, clip.Rect{Min: image.Pt(x, 0), Max: image.Pt(x+ww, t)}.Op())
		paint.FillShape(gtx.Ops, col, clip.Rect{Min: image.Pt(x, h-t), Max: image.Pt(x+ww, h)}.Op())
	}
	for y := 0; y < h; y += seg + gap {
		hh := seg
		if y+hh > h {
			hh = h - y
		}
		if hh <= 0 {
			break
		}
		paint.FillShape(gtx.Ops, col, clip.Rect{Min: image.Pt(0, y), Max: image.Pt(t, y+hh)}.Op())
		paint.FillShape(gtx.Ops, col, clip.Rect{Min: image.Pt(w-t, y), Max: image.Pt(w, y+hh)}.Op())
	}
}

// settingsHeading DSH 分区标题：18/600 + 13 tertiary 说明。
func (u *UI) settingsHeading(gtx layout.Context, title, intro string) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, unit.Sp(18), title)
			l.Color = cText
			l.Font.Weight = 600
			return l.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, unit.Sp(13), intro)
			l.Color = cTextFaint
			l.WrapPolicy = text.WrapWords
			return l.Layout(gtx)
		}),
	)
}

// settingsGroupTitle 分区内的小标题（13/600）。
func (u *UI) settingsGroupTitle(gtx layout.Context, txt string) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, unit.Sp(13), txt)
			l.Color = cTextDim
			l.Font.Weight = 600
			return l.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
	)
}

// presetLabel 当前服务商的显示名：优先按接口地址认预设，
// 因为多个预设可能共用同一个 provider，只按 provider 会认错。
func presetLabel(c modelConfigView) string {
	cur := strings.TrimRight(c.BaseURL, "/")
	if cur != "" {
		for _, p := range c.Presets {
			if strings.TrimRight(p.BaseURL, "/") == cur {
				return p.Label
			}
		}
	}
	if c.Current == "" {
		return "未选择"
	}
	for _, p := range c.Presets {
		if p.Provider == c.Current {
			return p.Label
		}
	}
	return c.Current
}

// settingsSelect 下拉选择器外观：与输入框同尺寸，右侧一个 ⌄。
func (u *UI) settingsSelect(gtx layout.Context, b *widget.Clickable, txt string, open bool) layout.Dimensions {
	h := gtx.Dp(unit.Dp(34))
	w := gtx.Constraints.Max.X
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		bg := cBg
		if b.Hovered() || open {
			bg = cHover
		}
		drawRBoxOutline(gtx, bg, cBorder, unit.Dp(radiusSm), w, h)
		gtx.Constraints = layout.Exact(image.Pt(w, h))
		return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, txt, unit.Sp(13), cText)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: gtx.Constraints.Min}
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, "⌄", unit.Sp(12), cTextFaint)
				}),
			)
		})
	})
}

// modelPresetDropdown 添加卡片里的服务商列表：固定高度 + 内嵌滚动。
// 选中标记按行下标判断（同一条目即同一个预设）。
func (u *UI) modelPresetDropdown(gtx layout.Context, presets []presetView, activeBase string) layout.Dimensions {
	w := gtx.Constraints.Max.X
	h := gtx.Dp(unit.Dp(224))
	cur := strings.TrimRight(activeBase, "/")
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			macro := op.Record(gtx.Ops)
			inner := gtx
			inner.Constraints = layout.Exact(image.Pt(w, h))
			l := u.pageScroll["model-add-presets"]
			if l == nil {
				l = &layout.List{Axis: layout.Vertical}
				u.pageScroll["model-add-presets"] = l
			}
			l.Layout(inner, len(presets), func(gtx layout.Context, i int) layout.Dimensions {
				base := strings.TrimRight(presets[i].BaseURL, "/")
				return u.settingsDropdownRow(gtx, clickAt(&u.modelAddPresetRows, i), presets[i].Label, cur != "" && base == cur)
			})
			body := macro.Stop()
			drawRBoxOutline(gtx, cPanelAlt, cBorder, unit.Dp(radiusMd), w, h)
			body.Add(gtx.Ops)
			return layout.Dimensions{Size: image.Pt(w, h)}
		}),
	)
}

// settingsDropdownRow 下拉里的一个选项。
func (u *UI) settingsDropdownRow(gtx layout.Context, b *widget.Clickable, txt string, active bool) layout.Dimensions {
	h := gtx.Dp(unit.Dp(32))
	w := gtx.Constraints.Max.X
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if b.Hovered() {
			fillDim(gtx, cHover, image.Pt(w, h))
		}
		col := cTextDim
		if active {
			col = cText
		}
		gtx.Constraints = layout.Exact(image.Pt(w, h))
		return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, txt, unit.Sp(13), col)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: gtx.Constraints.Min}
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !active {
						return layout.Dimensions{}
					}
					return label(gtx, u.th, "✓", unit.Sp(12), cAccent)
				}),
			)
		})
	})
}

// hairline 分隔线；inset 为左右留白（0 表示通栏）。
func (u *UI) hairline(gtx layout.Context, inset int) layout.Dimensions {
	h := gtx.Dp(unit.Dp(1))
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	In := unit.Dp(inset)
	return layout.Inset{Left: In, Right: In}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		w := gtx.Constraints.Max.X
		paint.FillShape(gtx.Ops, cBorder, clip.Rect{Max: image.Pt(w, h)}.Op())
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

func (u *UI) settingsRuntimeSection(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, "后台进程", unit.Sp(14), cText)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.actionButton(gtx, &u.settingsRestart, "⟳ 重启", false)
				}),
			)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.noteLine(gtx, "插件启停已热生效；改完设置或排查问题时，可整体重启后台与全部插件。")
		}),
	)
}

func (u *UI) settingsDataSection(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.noteLine(gtx, "插件配置：runtime/plugin-configs/<插件ID>.json")
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.noteLine(gtx, "插件程序：runtime/plugins/<插件名>.plugin/")
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.noteLine(gtx, "运行日志：runtime/logs/")
		}),
	)
}

// settingsBudgetSection 预算与工具：从侧栏页移入设置。
func (u *UI) settingsBudgetSection(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.settingsHeadingMd(gtx, "预算与工具", "限制单次任务的步数、超时与工具结果长度，并查看可用工具。")
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.budgetContent(gtx) }),
	)
}

// settingsUsageSection 用量：从侧栏页移入设置。
func (u *UI) settingsUsageSection(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.settingsHeadingMd(gtx, "用量", "余额来自当前模型账户；请求与 Token 统计由 usage.v1 累计。")
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.usageContent(gtx) }),
	)
}

func (u *UI) drawRolePage(gtx layout.Context) layout.Dimensions {
	fill(gtx, cBg)
	c := u.bridge.get().Roles
	return u.vscroll(gtx, keyRole, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(24), Right: unit.Dp(24), Top: unit.Dp(24), Bottom: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			children := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return u.actionButton(gtx, &u.roleNewBtn, "+ 新建角色", true)
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
					)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			}
			if u.showRoleEditor {
				children = append(children,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sectionTitle(gtx, "编辑角色") }),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.textField(gtx, "名称", &u.roleEditors[rfName], "角色名", false)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(rowGap)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.textField(gtx, "人物设定", &u.roleEditors[rfDesc], "它是谁", false)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(rowGap)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.textField(gtx, "性格", &u.roleEditors[rfPersonality], "说话方式", false)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(rowGap)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.textField(gtx, "场景", &u.roleEditors[rfScenario], "在哪、在做什么", false)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(rowGap)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.textField(gtx, "开场白", &u.roleEditors[rfGreeting], "第一句话", false)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(rowGap)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						ed := &u.roleEditors[rfMemory]
						h := gtx.Dp(unit.Dp(56))
						gtx.Constraints.Min.Y = h
						gtx.Constraints.Max.Y = h
						return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							e := material.Editor(u.th, ed, "长期记忆，一行一条…")
							e.TextSize = unit.Sp(13)
							e.Color = cText
							e.HintColor = cTextFaint
							macro := op.Record(gtx.Ops)
							dims := e.Layout(gtx)
							body := macro.Stop()
							dims.Size.Y = h
							rr := clip.RRect{Rect: image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, h)}, SE: gtx.Dp(radiusMd), SW: gtx.Dp(radiusMd), NE: gtx.Dp(radiusMd), NW: gtx.Dp(radiusMd)}
							paint.FillShape(gtx.Ops, cPanel, rr.Op(gtx.Ops))
							body.Add(gtx.Ops)
							return dims
						})
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return u.actionButton(gtx, &u.roleSaveBtn, "保存角色", true)
							}),
						)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
				)
			}
			children = append(children,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sectionTitle(gtx, "角色列表") }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					items := make([]layout.Widget, 0, len(c.Roles))
					for i := 0; i < len(c.Roles); i++ {
						i := i
						items = append(items, func(gtx layout.Context) layout.Dimensions {
							r := c.Roles[i]
							gtx.Constraints.Min.Y = 0
							return u.roleRow(gtx, i, r)
						})
					}
					return vstack(gtx, items)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.noteLine(gtx, c.Note) }),
			)
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	})
}

func (u *UI) roleRow(gtx layout.Context, i int, r roleCard) layout.Dimensions {
	return layout.Inset{Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return drawRBox(gtx, cPanel, unit.Dp(8), func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(10), Top: unit.Dp(10), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				tag := ""
				fg := cTextDim
				if r.Active {
					tag = " [启用中]"
					fg = cOk
				}
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return label(gtx, u.th, r.Name+tag, unit.Sp(13), fg)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								note := r.Greeting
								if len([]rune(note)) > 30 {
									note = string([]rune(note)[:30]) + "…"
								}
								if note == "" {
									note = "无开场白 · 记忆 " + strconv.Itoa(r.MemoryCount) + " 条"
								}
								return label(gtx, u.th, note, unit.Sp(10), cTextFaint)
							}),
						)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						toggle := "启用"
						if r.Active {
							toggle = "停用"
						}
						return u.actionButton(gtx, clickAt(&u.roleActivateBtns, i), toggle, false)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return u.actionButton(gtx, clickAt(&u.roleDeleteBtns, i), "删除", false)
						})
					}),
				)
			})
		})
	})
}

// ============================================================
// 预算与工具页
// ============================================================
const (
	bfMaxSteps = iota
	bfTimeout
	bfToolLimit
	bfSystem
	bfCount
)

func (u *UI) handleBudgetEvents(gtx layout.Context) {
	c := u.bridge.get().Budget
	// 等 agent.v1 的 info 回来再填编辑器，避免先用空快照填成 0。
	if !u.budgetInit && c.Loaded {
		u.budgetEditors = make([]widget.Editor, bfCount)
		for i := range u.budgetEditors {
			u.budgetEditors[i].SingleLine = i != bfSystem
		}
		u.budgetEditors[bfMaxSteps].SetText(strconv.Itoa(c.MaxSteps))
		u.budgetEditors[bfTimeout].SetText(strconv.Itoa(c.TimeoutMs))
		u.budgetEditors[bfToolLimit].SetText(strconv.Itoa(c.ToolLimit))
		u.budgetEditors[bfSystem].SetText(c.System)
		u.budgetInit = true
	}
	if u.budgetSave.Clicked(gtx) && u.budgetInit {
		atoi := func(i int, def int) int {
			n, e := strconv.Atoi(strings.TrimSpace(u.budgetEditors[i].Text()))
			if e != nil {
				return def
			}
			return n
		}
		u.bridge.saveBudget(budgetView{
			MaxSteps: atoi(bfMaxSteps, c.MaxSteps), TimeoutMs: atoi(bfTimeout, c.TimeoutMs),
			ToolLimit: atoi(bfToolLimit, c.ToolLimit), System: u.budgetEditors[bfSystem].Text(),
		})
	}
}

func (u *UI) drawBudgetPage(gtx layout.Context) layout.Dimensions {
	fill(gtx, cBg)
	return u.vscroll(gtx, keyBudget, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(24), Right: unit.Dp(24), Top: unit.Dp(24), Bottom: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return u.budgetContent(gtx)
		})
	})
}

// budgetContent 预算与工具的表单本体；设置弹层里复用同一份内容。
func (u *UI) budgetContent(gtx layout.Context) layout.Dimensions {
	c := u.bridge.get().Budget
	// 数据没回来之前编辑器还没建，先占位，避免按下标取空切片。
	if !u.budgetInit || len(u.budgetEditors) < bfCount {
		return label(gtx, u.th, "正在读取预算配置…", unit.Sp(11), cTextFaint)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.textField(gtx, "最大步数", &u.budgetEditors[bfMaxSteps], "-1 不限", false)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(rowGap)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.textField(gtx, "超时(毫秒)", &u.budgetEditors[bfTimeout], "300000", false)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(rowGap)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.textField(gtx, "结果上限(字符)", &u.budgetEditors[bfToolLimit], "8000", false)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(rowGap)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sectionTitle(gtx, "系统提示词") }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			ed := &u.budgetEditors[bfSystem]
			h := gtx.Dp(unit.Dp(72))
			gtx.Constraints.Min.Y = h
			gtx.Constraints.Max.Y = h
			return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				e := material.Editor(u.th, ed, "全局系统提示词…")
				e.TextSize = unit.Sp(13)
				e.Color = cText
				e.HintColor = cTextFaint
				macro := op.Record(gtx.Ops)
				dims := e.Layout(gtx)
				body := macro.Stop()
				dims.Size.Y = h
				rr := clip.RRect{Rect: image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, h)}, SE: gtx.Dp(radiusMd), SW: gtx.Dp(radiusMd), NE: gtx.Dp(radiusMd), NW: gtx.Dp(radiusMd)}
				paint.FillShape(gtx.Ops, cPanel, rr.Op(gtx.Ops))
				body.Add(gtx.Ops)
				return dims
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.actionButton(gtx, &u.budgetSave, "保存", true) }),
			)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.noteLine(gtx, c.Note) }),
		layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sectionTitle(gtx, "可用工具") }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			tools := c.Tools
			items := make([]layout.Widget, 0, len(tools))
			for i := 0; i < len(tools); i++ {
				i := i
				items = append(items, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.Y = 0
					return layout.Inset{Bottom: unit.Dp(3)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return label(gtx, u.th, "· "+tools[i], unit.Sp(11), cTextDim)
					})
				})
			}
			return vstack(gtx, items)
		}),
	)
}

// ============================================================
// 文生图页
// ============================================================
const (
	ifBaseURL = iota
	ifModel
	ifAPIKey
	ifSize
	ifCount
)

func (u *UI) handleImageGenEvents(gtx layout.Context) {
	c := u.bridge.get().ImageGen
	if !u.imageInit {
		u.imageEditors = make([]widget.Editor, ifCount)
		for i := range u.imageEditors {
			u.imageEditors[i].SingleLine = true
		}
		u.imageEditors[ifBaseURL].SetText(c.BaseURL)
		u.imageEditors[ifModel].SetText(c.Model)
		u.imageEditors[ifAPIKey].SetText(c.APIKey)
		u.imageEditors[ifSize].SetText(c.Size)
		u.imagePromptEditor.SingleLine = false
		u.imageInit = true
	}
	if u.imageGenBtn.Clicked(gtx) && !c.Busy {
		prompt := strings.TrimSpace(u.imagePromptEditor.Text())
		if prompt == "" {
			return
		}
		// 配置有改动则先保存，再生成。
		v := u.bridge.get().ImageGen
		v.BaseURL = u.imageEditors[ifBaseURL].Text()
		v.Model = u.imageEditors[ifModel].Text()
		v.APIKey = u.imageEditors[ifAPIKey].Text()
		v.Size = u.imageEditors[ifSize].Text()
		u.bridge.patch(func(s *snapshot) { s.ImageGen = v })
		u.bridge.saveImageGenQuiet()
		u.bridge.generateImage(prompt)
	}
}

func (b *bridge) saveImageGenQuiet() { b.saveImageGen(b.get().ImageGen) }

func (u *UI) drawImageGenPage(gtx layout.Context) layout.Dimensions {
	fill(gtx, cBg)
	c := u.bridge.get().ImageGen
	return u.vscroll(gtx, keyImageGen, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(24), Right: unit.Dp(24), Top: unit.Dp(24), Bottom: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.textField(gtx, "接口地址", &u.imageEditors[ifBaseURL], "https://…", false)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(rowGap)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.textField(gtx, "模型", &u.imageEditors[ifModel], "模型名", false)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(rowGap)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.textField(gtx, "API 密钥", &u.imageEditors[ifAPIKey], "sk-…", true)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(rowGap)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.textField(gtx, "尺寸", &u.imageEditors[ifSize], "1024x1024", false)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sectionTitle(gtx, "画面描述") }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					ed := &u.imagePromptEditor
					h := gtx.Dp(unit.Dp(72))
					gtx.Constraints.Min.Y = h
					gtx.Constraints.Max.Y = h
					return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						e := material.Editor(u.th, ed, "描述你想生成的画面…")
						e.TextSize = unit.Sp(13)
						e.Color = cText
						e.HintColor = cTextFaint
						macro := op.Record(gtx.Ops)
						dims := e.Layout(gtx)
						body := macro.Stop()
						dims.Size.Y = h
						rr := clip.RRect{Rect: image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, h)}, SE: gtx.Dp(radiusMd), SW: gtx.Dp(radiusMd), NE: gtx.Dp(radiusMd), NW: gtx.Dp(radiusMd)}
						paint.FillShape(gtx.Ops, cPanel, rr.Op(gtx.Ops))
						body.Add(gtx.Ops)
						return dims
					})
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					txt := "生成图片"
					if c.Busy {
						txt = "生成中…"
					}
					return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.actionButton(gtx, &u.imageGenBtn, txt, true) }),
					)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.noteLine(gtx, c.Message) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.noteLine(gtx, c.Note) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					items := make([]layout.Widget, 0, len(c.LastPaths))
					for i := 0; i < len(c.LastPaths); i++ {
						i := i
						items = append(items, func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Min.Y = 0
							return layout.Inset{Bottom: unit.Dp(3)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return label(gtx, u.th, "▣ "+c.LastPaths[i], unit.Sp(10), cTextDim)
							})
						})
					}
					return vstack(gtx, items)
				}),
			)
		})
	})
}

// ============================================================
// 插件管理页（DSH 风格：分段筛选 + 搜索 + 分类标签 + 卡片网格）
// ============================================================
var pluginTabs = []string{"已安装", "已启用", "已停用"}

func (u *UI) filteredPluginItems() ([]pluginMgrItem, []int) {
	items := u.bridge.get().PluginMgr.Items
	q := strings.ToLower(strings.TrimSpace(u.pluginSearch.Text()))
	shown := make([]pluginMgrItem, 0, len(items))
	idxs := make([]int, 0, len(items))
	for i, p := range items {
		if u.pluginTab == 1 && p.Disabled {
			continue
		}
		if u.pluginTab == 2 && !p.Disabled {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(p.Name+" "+p.ID+" "+p.Description), q) {
			continue
		}
		shown = append(shown, p)
		idxs = append(idxs, i)
	}
	return shown, idxs
}

// pluginDetailItem 取当前详情页对应的插件。
func (u *UI) pluginDetailItem() (pluginMgrItem, bool) {
	for _, it := range u.bridge.get().PluginMgr.Items {
		if it.ID == u.pluginDetailID {
			return it, true
		}
	}
	return pluginMgrItem{}, false
}

func (u *UI) handlePluginEvents(gtx layout.Context) {
	u.pluginSearch.SingleLine = true

	// 详情页：只处理返回、启停与删除。
	if u.pluginDetailID != "" {
		if u.pluginDetailBack.Clicked(gtx) {
			u.pluginDetailID = ""
			u.pluginDelConfirm = false
			return
		}
		it, ok := u.pluginDetailItem()
		if !ok {
			return
		}
		if !it.Protected && u.pluginDetailToggle.Clicked(gtx) {
			u.bridge.togglePlugin(it.ID, it.Disabled)
			return
		}
		if !it.Protected && u.pluginDetailDel.Clicked(gtx) {
			if !u.pluginDelConfirm {
				u.pluginDelConfirm = true
				return
			}
			u.pluginDelConfirm = false
			u.pluginDetailID = ""
			u.bridge.deletePlugin(it.ID)
		}
		return
	}

	c := u.bridge.get().PluginMgr
	if len(u.pluginTabBtns) < len(pluginTabs) {
		u.pluginTabBtns = append(u.pluginTabBtns, make([]widget.Clickable, len(pluginTabs)-len(u.pluginTabBtns))...)
	}
	for i := range pluginTabs {
		if u.pluginTabBtns[i].Clicked(gtx) {
			u.pluginTab = i
		}
	}

	if len(u.pluginToggleBtns) < len(c.Items) {
		u.pluginToggleBtns = append(u.pluginToggleBtns, make([]widget.Clickable, len(c.Items)-len(u.pluginToggleBtns))...)
	}
	shown, idxs := u.filteredPluginItems()
	for i, p := range shown {
		orig := idxs[i]
		if clickAt(&u.pluginOpenBtns, orig).Clicked(gtx) ||
			clickAt(&u.pluginDetailLinks, orig).Clicked(gtx) {
			u.pluginDetailID = p.ID
			u.pluginDelConfirm = false
			return
		}
		if u.pluginToggleBtns[orig].Clicked(gtx) {
			u.bridge.togglePlugin(p.ID, p.Disabled)
			return
		}
	}
}

func (u *UI) drawPluginsPage(gtx layout.Context) layout.Dimensions {
	fill(gtx, cBg)
	// 直接以插件页启动时不会走 onPageEnter，这里补一次首帧加载，
	// 否则会出现「共 0 个」的空列表。
	if !u.pluginsLoaded {
		u.pluginsLoaded = true
		u.bridge.loadPluginMgr()
	}
	shown, idxs := u.filteredPluginItems()
	total := len(u.bridge.get().PluginMgr.Items)

	// 详情页替换整个列表页，返回后回到列表。
	if u.pluginDetailID != "" {
		return u.drawPluginDetailPage(gtx)
	}

	return u.vscroll(gtx, keyPlugins, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(24), Right: unit.Dp(24), Top: unit.Dp(18), Bottom: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			children := []layout.FlexChild{
				// 分段控件
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = 0
					tabs := make([]layout.FlexChild, 0, len(pluginTabs))
					for i := range pluginTabs {
						idx := i
						tabs = append(tabs, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return u.chip(gtx, clickAt(&u.pluginTabBtns, idx), pluginTabs[idx], u.pluginTab == idx)
						}))
					}
					return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, tabs...)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				// 搜索框（占满宽度）
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.inputField(gtx, &u.pluginSearch, "搜索插件名称、ID 或描述…", false)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, "本页显示 "+strconv.Itoa(len(shown))+" 个插件 · 共 "+strconv.Itoa(total)+" 个 · 点卡片查看详情", unit.Sp(11), cTextFaint)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
				// 两列卡片网格
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if len(shown) == 0 {
						return layout.Inset{Top: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return label(gtx, u.th, "没有匹配的插件，换个关键词或切回「已安装」。", unit.Sp(13), cTextFaint)
						})
					}
					rows := (len(shown) + 1) / 2
					items := make([]layout.Widget, 0, rows)
					for row := 0; row < rows; row++ {
						row := row
						items = append(items, func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Min.Y = 0
							return layout.Inset{Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								cards := make([]layout.FlexChild, 0, 2)
								for col := 0; col < 2; col++ {
									idx := row*2 + col
									if idx >= len(shown) {
										// 奇数个时补空位，最后一张卡保持半宽，不被拉成整行。
										cards = append(cards, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
											return layout.Dimensions{Size: gtx.Constraints.Min}
										}))
										continue
									}
									pos := idx
									cards = append(cards, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
										return u.pluginCard(gtx, shown[pos],
											clickAt(&u.pluginOpenBtns, idxs[pos]),
											clickAt(&u.pluginDetailLinks, idxs[pos]),
											clickAt(&u.pluginToggleBtns, idxs[pos]))
									}))
								}
								return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, cards...)
							})
						})
					}
					return vstack(gtx, items)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.noteLine(gtx, u.bridge.get().PluginMgr.Note)
				}),
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	})
}

// toggleSwitch 插件启停开关（对齐 DSH 设置项右侧的 switch）。
func (u *UI) toggleSwitch(gtx layout.Context, b *widget.Clickable, on bool) layout.Dimensions {
	w, h := gtx.Dp(unit.Dp(36)), gtx.Dp(unit.Dp(20))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		track := color.NRGBA{R: 0x43, G: 0x45, B: 0x4A, A: 0xFF}
		if on {
			track = cAccent
		} else if b.Hovered() {
			track = cNavActive
		}
		r := h / 2
		paint.FillShape(gtx.Ops, track, clip.RRect{
			Rect: image.Rectangle{Max: image.Pt(w, h)}, SE: r, SW: r, NE: r, NW: r,
		}.Op(gtx.Ops))
		kx := 2
		if on {
			kx = w - h + 2
		}
		paint.FillShape(gtx.Ops, cText, clip.Ellipse(image.Rect(kx, 2, kx+h-4, h-2)).Op(gtx.Ops))
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

// clampWidth 按可用像素宽估算可容纳的字数（CJK 按 1 字≈字号宽），超出加省略号。
func clampWidth(gtx layout.Context, txt string, sp int, avail int) string {
	per := avail / gtx.Dp(unit.Dp(sp))
	if per < 4 {
		per = 4
	}
	return truncate(txt, per)
}

// pluginCard 插件卡片：圆角 20、0.5px 描边、固定 168 高。
// 标题/描述/标签全部按可用宽度截断并做硬裁剪，窄窗口下不会互相压盖。
// 卡片上半区可点，进入插件详情页；右下角开关单独响应，两者不冲突。
func (u *UI) pluginCard(gtx layout.Context, p pluginMgrItem, open, detail, btn *widget.Clickable) layout.Dimensions {
	h := gtx.Dp(unit.Dp(168))
	w := gtx.Constraints.Max.X
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	layout.Inset{Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		w := gtx.Constraints.Max.X
		gtx.Constraints = layout.Exact(image.Pt(w, h))
		border := cBorder
		if open.Hovered() {
			border = cNavActive
		}
		drawRBoxOutline(gtx, cPanelAlt, border, unit.Dp(20), w, h)
		return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(12), Top: unit.Dp(12), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			inner := gtx.Constraints.Max.X
			name := p.Name
			if name == "" {
				name = p.ID
			}
			state, stateCol := "已启用", cOk
			if p.Disabled {
				state, stateCol = "已停用", cTextFaint
			}
			tags := make([]string, 0, 3)
			for i, s := range p.Provides {
				if i >= 3 {
					break
				}
				tags = append(tags, s)
			}
			desc := strings.TrimSpace(p.Description)
			if desc == "" {
				desc = "（无描述）"
			}
			title := clampWidth(gtx, name, 14, inner-gtx.Dp(unit.Dp(44)))
			desc = clampWidth(gtx, desc, 12, inner*2) // 两行预算
			tagLine := clampWidth(gtx, strings.Join(tags, "  "), 11, inner)
			lineH := gtx.Dp(unit.Dp(18))
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				// 上半区：标题 / 描述 / 能力标签，整块可点进入详情。
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return open.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										d := image.Pt(gtx.Dp(unit.Dp(26)), gtx.Dp(unit.Dp(26)))
										gtx.Constraints = layout.Exact(d)
										paint.FillShape(gtx.Ops, cAccentDim, clip.RRect{Rect: image.Rectangle{Max: d}, SE: gtx.Dp(7), SW: gtx.Dp(7), NE: gtx.Dp(7), NW: gtx.Dp(7)}.Op(gtx.Ops))
										initial := "·"
										if r := []rune(name); len(r) > 0 {
											initial = string(r[0])
										}
										return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
											return label(gtx, u.th, initial, unit.Sp(12), cText)
										})
									}),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return layout.Inset{Left: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
											st := clip.Rect{Max: image.Pt(inner-gtx.Dp(unit.Dp(54)), gtx.Dp(unit.Dp(20)))}.Push(gtx.Ops)
											defer st.Pop()
											l := material.Label(u.th, unit.Sp(14), title)
											l.Color = cText
											l.Font.Weight = 500
											l.MaxLines = 1
											return l.Layout(gtx)
										})
									}),
									layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return label(gtx, u.th, "v"+p.Version, unit.Sp(10), cTextFaint)
									}),
								)
							}),
							layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								st := clip.Rect{Max: image.Pt(inner, lineH*2)}.Push(gtx.Ops)
								defer st.Pop()
								c := gtx
								c.Constraints.Min = image.Point{}
								c.Constraints.Max = image.Pt(inner, lineH*2)
								l := material.Label(u.th, unit.Sp(12), desc)
								l.Color = cTextDim
								l.WrapPolicy = text.WrapWords
								l.MaxLines = 2
								return l.Layout(c)
							}),
							layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if tagLine == "" {
									return layout.Dimensions{}
								}
								st := clip.Rect{Max: image.Pt(inner, gtx.Dp(unit.Dp(16)))}.Push(gtx.Ops)
								defer st.Pop()
								l := material.Label(u.th, unit.Sp(11), tagLine)
								l.Color = cTextFaint
								l.MaxLines = 1
								return l.Layout(gtx)
							}),
						)
					})
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							// 状态 + 「详情 ›」整块可点：之前只有卡片上半区能进详情，
							// 用户点这一行（最像入口的地方）毫无反应。
							return detail.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return label(gtx, u.th, state, unit.Sp(11), stateCol)
									}),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return layout.Inset{Left: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
											col := cTextFaint
											if detail.Hovered() {
												col = cAccent
											}
											return label(gtx, u.th, "详情 ›", unit.Sp(11), col)
										})
									}),
								)
							})
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if p.Protected {
								return label(gtx, u.th, "核心组件", unit.Sp(11), cTextFaint)
							}
							return u.toggleSwitch(gtx, btn, !p.Disabled)
						}),
					)
				}),
			)
		})
	})
	// 必须返回分配到的完整尺寸：Flex 按返回值推进下一个子项，返回内容尺寸
	// 会让右列卡片提前起排、整行右侧留出一截空白。
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// drawPluginDetailPage 单个插件的详情：基本信息、描述、能力、依赖、权限、目录与删除。
// 从列表点卡片（或卡片上的「详情 ›」）进入，返回后回到列表。
func (u *UI) drawPluginDetailPage(gtx layout.Context) layout.Dimensions {
	fill(gtx, cBg)
	it, ok := u.pluginDetailItem()
	if !ok {
		// 插件被删掉了：退回列表，避免停在空白页。
		u.pluginDetailID = ""
		return layout.Dimensions{Size: gtx.Constraints.Max}
	}
	name := it.Name
	if name == "" {
		name = it.ID
	}
	state, stateCol := "已启用", cOk
	if it.Disabled {
		state, stateCol = "已停用", cTextFaint
	}

	return u.vscroll(gtx, "plugin-detail", func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(24), Right: unit.Dp(24), Top: unit.Dp(18), Bottom: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			tags := func(list []string) layout.Dimensions {
				if len(list) == 0 {
					return label(gtx, u.th, "—", unit.Sp(12), cTextFaint)
				}
				items := make([]layout.Widget, 0, len(list))
				for _, s := range list {
					txt := s
					items = append(items, func(gtx layout.Context) layout.Dimensions {
						return u.tagPill(gtx, txt)
					})
				}
				return flow(gtx, gtx.Dp(unit.Dp(6)), items)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.chip(gtx, &u.pluginDetailBack, "← 返回插件列表", false)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							d := image.Pt(gtx.Dp(unit.Dp(34)), gtx.Dp(unit.Dp(34)))
							gtx.Constraints = layout.Exact(d)
							paint.FillShape(gtx.Ops, cAccentDim, clip.RRect{Rect: image.Rectangle{Max: d}, SE: gtx.Dp(9), SW: gtx.Dp(9), NE: gtx.Dp(9), NW: gtx.Dp(9)}.Op(gtx.Ops))
							initial := "·"
							if r := []rune(name); len(r) > 0 {
								initial = string(r[0])
							}
							return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return label(gtx, u.th, initial, unit.Sp(15), cText)
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Left: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										l := material.Label(u.th, unit.Sp(20), name)
										l.Color = cText
										l.Font.Weight = 600
										return l.Layout(gtx)
									}),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return label(gtx, u.th, it.ID+" · v"+it.Version, unit.Sp(12), cTextFaint)
									}),
								)
							})
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return label(gtx, u.th, state, unit.Sp(12), stateCol)
						}),
					)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if it.Protected {
						return label(gtx, u.th, "核心组件，不可停用", unit.Sp(12), cTextFaint)
					}
					txt := "停用插件"
					if it.Disabled {
						txt = "启用插件"
					}
					return u.actionButton(gtx, &u.pluginDetailToggle, txt, true)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if it.Protected {
						return layout.Dimensions{}
					}
					txt := "删除插件"
					if u.pluginDelConfirm {
						txt = "确认删除"
					}
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return u.actionButton(gtx, &u.pluginDetailDel, txt, false)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							hint := " 目录、配置、日志一起清除"
							if u.pluginDelConfirm {
								hint = " 再点一次确认，不可撤销"
							}
							return layout.Inset{Left: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return label(gtx, u.th, hint, unit.Sp(11), cTextFaint)
							})
						}),
					)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(20)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.sectionTitle(gtx, "基本信息")
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					ver := it.Version
					if ver == "" {
						ver = "—"
					} else {
						ver = "v" + ver
					}
					rows := []layout.Widget{
						func(gtx layout.Context) layout.Dimensions { return u.kvRow(gtx, "版本", ver) },
						func(gtx layout.Context) layout.Dimensions { return u.kvRow(gtx, "运行时", orDash(it.Runtime)) },
						func(gtx layout.Context) layout.Dimensions { return u.kvRow(gtx, "入口", orDash(it.Entry)) },
						func(gtx layout.Context) layout.Dimensions { return u.kvRow(gtx, "兼容核心", orDash(it.Compat)) },
						func(gtx layout.Context) layout.Dimensions { return u.kvRow(gtx, "状态", state) },
					}
					return vstack(gtx, rows)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(18)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.sectionTitle(gtx, "描述")
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					desc := strings.TrimSpace(it.Description)
					if desc == "" {
						desc = "（无描述）"
					}
					l := material.Label(u.th, unit.Sp(13), desc)
					l.Color = cTextDim
					l.WrapPolicy = text.WrapWords
					return l.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(18)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sectionTitle(gtx, "提供的能力") }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return tags(it.Provides) }),
				layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sectionTitle(gtx, "依赖的服务") }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return tags(it.Consumes) }),
				layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sectionTitle(gtx, "权限") }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return tags(it.Permissions) }),
				layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sectionTitle(gtx, "安装目录") }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					path := it.DirPath
					if path == "" {
						path = it.Dir
					}
					if path == "" {
						path = "—"
					}
					return u.kvRow(gtx, "路径", path)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.noteLine(gtx, u.bridge.get().PluginMgr.Note)
				}),
			)
		})
	})
}

// orDash 空值显示为短横线。
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// kvRow 详情页的一行"标签 + 值"。
func (u *UI) kvRow(gtx layout.Context, k, v string) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(64))
			return label(gtx, u.th, k, unit.Sp(12), cTextFaint)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return label(gtx, u.th, v, unit.Sp(13), cText)
		}),
	)
}

// tagPill 静态小标签（能力 / 依赖 / 权限）。
func (u *UI) tagPill(gtx layout.Context, txt string) layout.Dimensions {
	return layout.Inset{Right: unit.Dp(6), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(unit.Dp(24))
		macro := op.Record(gtx.Ops)
		d := label(gtx, u.th, txt, unit.Sp(11), cTextDim)
		text := macro.Stop()
		w := d.Size.X + gtx.Dp(unit.Dp(16))
		gtx.Constraints = layout.Exact(image.Pt(w, h))
		drawRBoxFill(gtx, cPanelAlt, unit.Dp(12), w, h)
		st := op.Offset(image.Pt(gtx.Dp(unit.Dp(8)), (h-d.Size.Y)/2)).Push(gtx.Ops)
		text.Add(gtx.Ops)
		st.Pop()
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

// ============================================================
// 工作区页
// ============================================================
func (u *UI) handleWorkspaceEvents(gtx layout.Context, snap snapshot) {
	if u.workspacePickBtn.Clicked(gtx) {
		u.bridge.pickWorkspaceRoot()
	}
	if u.workspaceParentBtn.Clicked(gtx) && snap.Workspace.Parent != "" {
		u.bridge.browseWorkspace(snap.Workspace.Parent)
	}
	if len(u.workspaceDirBtns) < len(snap.Workspace.Entries) {
		u.workspaceDirBtns = append(u.workspaceDirBtns, make([]widget.Clickable, len(snap.Workspace.Entries)-len(u.workspaceDirBtns))...)
	}
	for i, e := range snap.Workspace.Entries {
		if e.Dir && u.workspaceDirBtns[i].Clicked(gtx) {
			u.bridge.browseWorkspace(e.Path)
		}
	}
}

func (u *UI) drawWorkspacePage(gtx layout.Context) layout.Dimensions {
	fill(gtx, cBg)
	c := u.bridge.get().Workspace
	// 侧栏点击可能在当前帧把活动页切到工作区，此时本帧还没跑 handleWorkspaceEvents；
	// 这里兜底补齐控件数组，避免列表项索引越界。
	if len(u.workspaceDirBtns) < len(c.Entries) {
		u.workspaceDirBtns = append(u.workspaceDirBtns, make([]widget.Clickable, len(c.Entries)-len(u.workspaceDirBtns))...)
	}
	return u.vscroll(gtx, keyWorkspace, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(24), Right: unit.Dp(24), Top: unit.Dp(24), Bottom: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					root := c.Root
					if !c.Set {
						root = "未设置"
					}
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return label(gtx, u.th, root, unit.Sp(12), cText)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return u.actionButton(gtx, &u.workspacePickBtn, "选择目录", true)
						}),
					)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.noteLine(gtx, c.Note) }),
				layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					back := "↑ 上级目录"
					if c.IsRoot {
						back = "↑ 工作区根目录"
					}
					return u.actionButton(gtx, &u.workspaceParentBtn, back, false)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, "当前: "+c.Path, unit.Sp(11), cTextDim)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					items := make([]layout.Widget, 0, len(c.Entries))
					for i := 0; i < len(c.Entries); i++ {
						i := i
						items = append(items, func(gtx layout.Context) layout.Dimensions {
							e := c.Entries[i]
							gtx.Constraints.Min.Y = 0
							icon := "▣"
							if e.Dir {
								icon = "▸"
							}
							size := ""
							if !e.Dir && e.Size > 0 {
								size = "  " + formatSize(e.Size)
							}
							return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								if e.Dir {
									return u.chip(gtx, clickAt(&u.workspaceDirBtns, i), icon+" "+e.Name, false)
								}
								return label(gtx, u.th, icon+" "+e.Name+size, unit.Sp(11), cTextDim)
							})
						})
					}
					return vstack(gtx, items)
				}),
			)
		})
	})
}

func formatSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + "B"
	}
	if n < 1024*1024 {
		return strconv.FormatInt(n/1024, 10) + "KB"
	}
	return strconv.FormatInt(n/(1024*1024), 10) + "MB"
}

// ============================================================
// 用量页
// ============================================================
func (u *UI) drawUsagePage(gtx layout.Context) layout.Dimensions {
	fill(gtx, cBg)
	return u.vscroll(gtx, keyUsage, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(24), Right: unit.Dp(24), Top: unit.Dp(24), Bottom: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return u.usageContent(gtx)
		})
	})
}

// handleUsageEvents 处理用量页/设置分区共用的两个按钮。
func (u *UI) handleUsageEvents(gtx layout.Context) {
	if u.usageRefreshBtn.Clicked(gtx) {
		u.bridge.refreshBalance()
	}
	if u.usageResetBtn.Clicked(gtx) {
		u.bridge.resetUsage()
	}
}

// usageContent 用量卡片本体；设置弹层里复用同一份内容。
func (u *UI) usageContent(gtx layout.Context) layout.Dimensions {
	c := u.bridge.get().Usage
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.statCard(gtx, "账户余额", c.Total+" "+c.Currency, c.BalanceOK)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.statCard(gtx, "累计请求", strconv.Itoa(c.Requests)+" 次（失败 "+strconv.Itoa(c.Failed)+"）", true)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.statCard(gtx, "累计 Token", strconv.Itoa(c.TotalTokens), true)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.statCard(gtx, "缓存命中率", c.HitRateStr+"（命中 "+strconv.Itoa(c.CacheHitTokens)+"）", true)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.actionButton(gtx, &u.usageRefreshBtn, "刷新余额", true)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Left: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return u.actionButton(gtx, &u.usageResetBtn, "清零统计", false)
					})
				}),
			)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.noteLine(gtx, c.Note) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if c.CheckedAt == "" {
				return layout.Dimensions{}
			}
			return label(gtx, u.th, "余额查询于 "+c.CheckedAt, unit.Sp(10), cTextFaint)
		}),
	)
}

func (u *UI) statCard(gtx layout.Context, title, value string, ok bool) layout.Dimensions {
	h := gtx.Dp(unit.Dp(56))
	gtx.Constraints.Min.Y = h
	gtx.Constraints.Max.Y = h
	return drawRBox(gtx, cPanel, unit.Dp(8), func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, title, unit.Sp(11), cTextDim)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					col := cText
					if !ok {
						col = cWarn
					}
					return label(gtx, u.th, value, unit.Sp(15), col)
				}),
			)
		})
	})
}

// 确保 text 包被使用（气泡里已用，这里仅占位防止误删）。
var _ = text.WrapWords
