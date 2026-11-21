package main

import (
	"image"
	"strings"

	"gioui.org/gesture"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// panelView 是当前合成页的渲染状态。
type panelView struct {
	frame  panelFrame
	scroll int
	note   string
	w, h   int // 上次渲染使用的网格尺寸（列/行）
}

// 已被原生页精确覆盖的 service/panel 组合：不再重复加合成页。
// 用组合而非整个 service：plugin-manager 同时提供"插件"和"规则"两页，
// 前者有原生页，后者要靠合成器兜底。
var nativeCoveredPages = map[string]bool{
	"ui.page.llm.v1/llm":               true, // 模型接入页
	"ui.page.roleplay.v1/roleplay":     true, // 角色页
	"ui.page.agent.v1/agent":           true, // 预算页
	"ui.page.imagegen.v1/imagegen":     true, // 文生图页
	"ui.page.pluginmanager.v1/plugins": true, // 插件管理页
	"ui.page.pluginmanager.v1/rules":   true, // 插件设计规则已并入设置弹层
	"ui.page.workspace.v1/workspace":   true, // 工作区页
}

// refreshPanelPages 重建侧栏导航里的插件页。
//
// 模仿 DSH：壳子只读插件注册的 sidebar.nav 条目（ui.contrib.v1），
// 插件页的 key / 标题 / 图标 / 排序全部来自条目本身；老的面板若没人显式
// 认领，加载器已经折算成同样的条目，所以这里不需要再区分"新老插件"。
// 每次插件变化都整表重建：只追加会让卸载掉的插件页永远留在侧栏。
func (u *UI) refreshPanelPages() {
	snap := u.bridge.get()
	if !snap.ContribsLoaded {
		// 贡献还没汇总完：保留旧页，先触发一次加载，避免侧栏闪空。
		if !u.contribLoading {
			u.contribLoading = true
			u.bridge.loadContributions()
		}
		return
	}

	// 固定页按服务在线情况重建：插件停用/退出后，它的入口跟着消失（DSH 行为）。
	u.pages = u.liveNativePages()

	seen := make(map[string]bool, 4)
	for _, c := range snap.Contribs {
		if !strings.EqualFold(c.Slot, slotSidebarNav) || c.Page == nil {
			continue
		}
		if c.Page.Native != "" {
			continue // 指向内置页，页面已经存在
		}
		svcPanel := c.Page.Service + "/" + c.Page.Panel
		if nativeCoveredPages[svcPanel] {
			continue
		}
		key := "panel:" + svcPanel
		if seen[key] {
			continue
		}
		seen[key] = true
		label := "页"
		r := []rune(c.Label)
		if len(r) > 0 {
			label = string(r[0])
		}
		title := c.Label
		if title == "" {
			title = c.ID
		}
		u.pages = append(u.pages, page{
			key: key, label: label, title: title, icon: c.Icon,
			kind: pagePanel, svc: c.Page.Service, panel: c.Page.Panel,
		})
	}

	// 当前停在一个已经消失的插件页上：退回对话，避免留在空白页。
	if u.activeKey != keyChat {
		alive := false
		for _, p := range u.pages {
			if p.key == u.activeKey {
				alive = true
				break
			}
		}
		if !alive {
			delete(u.pageLoaded, u.activeKey)
			u.activeKey = keyChat
			u.reloadActive()
		}
	}
}

// panelOf 从导航项找到对应的 panelPage 描述。
func (u *UI) panelOf(p page) panelPage {
	return panelPage{Service: p.svc, ID: p.panel, Title: p.title}
}

// ensurePanel 首次进入合成页时拉一次画面。网格尺寸按可用宽度估算。
func (u *UI) ensurePanel(p page) {
	if u.pageLoaded[p.key] {
		return
	}
	u.pageLoaded[p.key] = true
	cols, rows := 72, 24
	u.panelView.scroll = 0
	u.panelView.frame = u.bridge.renderPanelPage(u.panelOf(p), cols, rows, 0)
}

// handlePanelEvents 处理合成页的命中点击与滚轮滚动。
func (u *UI) handlePanelEvents(gtx layout.Context, p page) {
	changed := false
	// 滚轮：gesture.Scroll 给出像素位移，换算成"行"再交给插件重渲染。
	if u.panelView.frame.MaxScroll > 0 {
		lineH := gtx.Dp(unit.Dp(19))
		d := u.panelScroll.Update(gtx.Metric, gtx.Source, gtx.Now, gesture.Vertical,
			pointer.ScrollRange{Min: -1e6, Max: 1e6}, pointer.ScrollRange{Min: -1e6, Max: 1e6})
		if d != 0 {
			u.panelScrollPx += d
			step := u.panelScrollPx / lineH
			if step != 0 {
				u.panelScrollPx -= step * lineH
				next := u.panelView.scroll + step
				if next < 0 {
					next = 0
				}
				if next > u.panelView.frame.MaxScroll {
					next = u.panelView.frame.MaxScroll
				}
				if next != u.panelView.scroll {
					u.panelView.scroll = next
					changed = true
				}
			}
		}
	}
	if len(u.panelHits) < len(u.panelView.frame.Hits) {
		u.panelHits = append(u.panelHits, make([]widget.Clickable, len(u.panelView.frame.Hits)-len(u.panelHits))...)
	}
	for i, hit := range u.panelView.frame.Hits {
		if i < len(u.panelHits) && u.panelHits[i].Clicked(gtx) {
			note := u.bridge.panelAction(u.panelOf(p), p.panel, hit.Action, hit.Arg)
			u.panelView.note = note
			changed = true
		}
	}
	if changed {
		u.panelView.frame = u.bridge.renderPanelPage(u.panelOf(p), u.panelView.w, u.panelView.h, u.panelView.scroll)
	}
}

// drawPanelPage 把合成页的文本画面画成等宽终端风格（剥掉 ANSI 样式码）。
// 命中区叠在对应字符位置上，点击转发给插件。
func (u *UI) drawPanelPage(gtx layout.Context, p page) layout.Dimensions {
	fill(gtx, cBg)
	frame := u.panelView.frame
	if len(frame.Hits) > len(u.panelHits) {
		u.panelHits = append(u.panelHits, make([]widget.Clickable, len(frame.Hits)-len(u.panelHits))...)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(20), Top: unit.Dp(8), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return label(gtx, u.th, "插件自带页面 · 点击可操作 · 鼠标滚轮翻页", unit.Sp(10), cTextFaint)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lineH := gtx.Dp(unit.Dp(19))
				charW := gtx.Dp(unit.Dp(8))
				visible := gtx.Constraints.Max.Y / lineH
				if visible < 1 {
					visible = 1
				}
				gtx.Constraints.Min.Y = 0
				// 记录网格尺寸供命中重渲染使用。
				u.panelView.w = gtx.Constraints.Max.X / charW
				u.panelView.h = visible
				// 整片内容区接收滚轮事件（事件由 handlePanelEvents 消费）。
				area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
				u.panelScroll.Add(gtx.Ops)
				area.Pop()
				list := &u.panelList
				return list.Layout(gtx, len(frame.Lines), func(gtx layout.Context, i int) layout.Dimensions {
					gtx.Constraints.Min.Y = 0
					txt := stripANSI(frame.Lines[i])
					if txt == "" {
						txt = " "
					}
					return u.panelLine(gtx, i, txt, lineH, charW, frame.Hits)
				})
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16), Bottom: unit.Dp(12), Top: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				note := frame.Note
				if u.panelView.note != "" {
					note = u.panelView.note
				}
				return label(gtx, u.th, note, unit.Sp(10), cTextFaint)
			})
		}),
	)
}

// panelLine 画一行终端文本，并把这一行的命中区叠加上去。
func (u *UI) panelLine(gtx layout.Context, row int, txt string, lineH, charW int, hits []panelHit) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	l := material.Label(u.th, unit.Sp(11), txt)
	l.Color = cText
	_ = l.Layout(gtx)
	roman := macro.Stop()
	gtx.Constraints.Min.Y = 0
	dims := layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, lineH)}
	roman.Add(gtx.Ops)
	// 叠加命中区：用透明矩形承载点击，并轻微高亮 hover。
	for i, hit := range hits {
		if hit.Y != row || i >= len(u.panelHits) {
			continue
		}
		x0 := hit.X * charW
		w := hit.W * charW
		if w <= 0 {
			w = charW
		}
		stack := op.Offset(image.Pt(x0, 0)).Push(gtx.Ops)
		if u.panelHits[i].Hovered() {
			paint.FillShape(gtx.Ops, cAccentDim, clip.Rect{Max: image.Pt(w, lineH)}.Op())
		}
		u.panelHits[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(w, lineH)}
		})
		stack.Pop()
	}
	return dims
}

// ensure strings 使用（stripANSI 在别处用到，这里防止误删）。
var _ = strings.TrimSpace
