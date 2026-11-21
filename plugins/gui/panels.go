package main

import (
	"image"
	"image/color"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// drawNavRail 最左侧图标导航栏（用文字符号代替图标，Gio 无内置图标字体则自绘简单形状）。
func (u *UI) drawNavRail(gtx layout.Context) layout.Dimensions {
	w := gtx.Dp(navWidth)
	h := gtx.Constraints.Max.Y
	gtx.Constraints = layout.Exact(image.Pt(w, h))

	fill(gtx, cPanel)

	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, unit.Sp(20), "✦")
				l.Color = cAccent
				return l.Layout(gtx)
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(18)}.Layout),
	}
	for i := range u.pages {
		p := &u.pages[i]
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.navButton(gtx, &p.click, p.label, p.key == u.activeKey)
		}))
	}
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx, children...)
}

func (u *UI) navButton(gtx layout.Context, b *widget.Clickable, txt string, active bool) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		size := image.Pt(gtx.Dp(unit.Dp(40)), gtx.Dp(unit.Dp(38)))
		gtx.Constraints = layout.Exact(size)
		bg := cPanel
		if active {
			bg = cAccentDim
		} else if b.Hovered() {
			bg = cPanelAlt
		}
		// 选中项左侧的强调竖条
		rr := clip.RRect{
			Rect: image.Rectangle{Max: size},
			SE:   gtx.Dp(8), SW: gtx.Dp(8), NE: gtx.Dp(8), NW: gtx.Dp(8),
		}
		paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
		if active {
			paint.FillShape(gtx.Ops, cAccent, clip.Rect{Min: image.Pt(0, 8), Max: image.Pt(gtx.Dp(3), size.Y-8)}.Op())
		}
		col := cTextDim
		if active {
			col = cText
		}
		dims := layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return label(gtx, u.th, txt, unit.Sp(14), col)
		})
		b.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return dims })
		return dims
	})
}

// drawSessionPanel 会话列表（左侧第二栏）。
func (u *UI) drawSessionPanel(gtx layout.Context) layout.Dimensions {
	w := gtx.Dp(sessionWidth)
	h := gtx.Constraints.Max.Y
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	fill(gtx, cPanelAlt)

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		// 顶部：标题 + 新建按钮
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(12), Top: unit.Dp(16), Bottom: unit.Dp(12)}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return label(gtx, u.th, "会话", unit.Sp(13), cTextDim)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return u.smallButton(gtx, &u.newSession, "+")
						}),
					)
				})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			snap := u.bridge.get()
			if len(u.sessionClicks) < len(snap.Sessions) {
				u.sessionClicks = append(u.sessionClicks, make([]widget.Clickable, len(snap.Sessions)-len(u.sessionClicks))...)
			}
			return u.sessionList.Layout(gtx, len(snap.Sessions), func(gtx layout.Context, i int) layout.Dimensions {
				s := snap.Sessions[i]
				return u.sessionItem(gtx, &u.sessionClicks[i], s, s.ID == snap.SessionID)
			})
		}),
	)
}

func (u *UI) smallButton(gtx layout.Context, b *widget.Clickable, txt string) layout.Dimensions {
	size := image.Pt(gtx.Dp(unit.Dp(26)), gtx.Dp(unit.Dp(26)))
	gtx.Constraints = layout.Exact(size)
	bg := cPanel
	if b.Hovered() {
		bg = cAccentDim
	}
	rr := clip.RRect{Rect: image.Rectangle{Max: size}, SE: gtx.Dp(6), SW: gtx.Dp(6), NE: gtx.Dp(6), NW: gtx.Dp(6)}
	paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
	col := cAccent
	dims := layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return label(gtx, u.th, txt, unit.Sp(16), col)
	})
	b.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return dims })
	return dims
}

func (u *UI) sessionItem(gtx layout.Context, b *widget.Clickable, s sessionInfo, active bool) layout.Dimensions {
	return layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		bg := cPanelAlt
		if active {
			bg = cAccentDim
		} else if b.Hovered() {
			bg = cPanel
		}
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(8), Top: unit.Dp(9), Bottom: unit.Dp(9)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				// 会话项固定高度：避免 List 项的 Min 约束把它撑满整列。
				gtx.Constraints.Min.Y = 0
				return drawBox(gtx, bg, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							name := s.Name
							if strings.TrimSpace(name) == "" {
								name = "新会话"
							}
							l := material.Label(u.th, unit.Sp(13), truncate(name, 18))
							l.Color = cText
							return l.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							note := s.Updated
							if s.Nodes > 0 {
								note = strconv.Itoa(s.Nodes) + " 条消息"
							}
							l := material.Label(u.th, unit.Sp(10), truncate(note, 20))
							l.Color = cTextFaint
							return l.Layout(gtx)
						}),
					)
				})
			})
		})
	})
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

var _ = color.NRGBA{}
