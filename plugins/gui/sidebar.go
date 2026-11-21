package main

// DSH 风格侧栏（对照 dsh-client-ui-sidebar/src/client/SidebarRoot.module.css）：
//
//	root          padding 6px 8px，底色 sidebar-fill
//	.logoRow      height 60，品牌 + 折叠按钮
//	.newSession   height 38，radius 12，0.5px 描边，居中 14/500
//	.navCell      height 42，radius 12，icon 16 + label 14，gap 6
//	.scrollRegion 只有中段滚动；底部账户/设置常驻
//
// 实现上用「固定头 + 可滚中段 + 固定脚」三段绝对定位，
// 中段单独交给持久 layout.List，滚轮只作用在导航列表上。

import (
	"image"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

func pageIcon(key string) string {
	switch key {
	case keyChat:
		return "◈"
	case keyRole:
		return "☺"
	case keyImageGen:
		return "▣"
	case keyBudget:
		return "◉"
	case keyPlugins:
		return "⌘"
	case keyWorkspace:
		return "▤"
	case keyUsage:
		return "¥"
	case keySettings:
		return "⚙"
	}
	return "·"
}

const (
	sidebarHeaderH = 106 // 品牌 60 + 新建对话 46
	sidebarFooterH = 90  // 账户 40 + 设置行 42 + 上下 4+4
	// sidebarActionRowH 是脚部插件动作每行占的高度：42 行高 + 上下各 4。
	sidebarActionRowH = 50
)

func (u *UI) drawSidebar(gtx layout.Context) layout.Dimensions {
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	fill(gtx, cPanel)

	snap := u.bridge.get()
	// 只显示目录，和 DSH 的工作区列表一致。
	dirs := make([]int, 0, 4)
	for i, e := range snap.Workspace.Entries {
		if e.Dir {
			dirs = append(dirs, i)
			if len(dirs) == 4 {
				break
			}
		}
	}

	headerH := gtx.Dp(unit.Dp(sidebarHeaderH))
	footerH := gtx.Dp(unit.Dp(sidebarFooterH)) + len(u.contribsOf(slotFooterAction))*gtx.Dp(unit.Dp(sidebarActionRowH))
	midH := h - headerH - footerH
	if midH < 0 {
		midH = 0
	}

	// 固定头部：品牌 + 新建对话
	u.place(gtx, 0, 0, w, headerH, func(g layout.Context) {
		layout.Flex{Axis: layout.Vertical}.Layout(g,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sidebarBrand(gtx, w) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sidebarNewSession(gtx, w) }),
		)
	})

	// 中段：原生页 / 插件页 / 会话 / 工作区，可滚动
	if midH > 0 {
		u.place(gtx, 0, headerH, w, midH, func(g layout.Context) {
			u.sidebarList.Layout(g, 1, func(gtx layout.Context, _ int) layout.Dimensions {
				children := make([]layout.FlexChild, 0, len(u.pages)+len(snap.Sessions)+len(dirs)+4)
				// 原生页（设置不放这里，它在底部常驻）。
				hasPanel := false
				for i := range u.pages {
					if u.pages[i].kind == pagePanel {
						hasPanel = true
						continue
					}
					if u.pages[i].key == keySettings {
						continue
					}
					// 插件管理已由插件管理器注册到顶栏（header.action）：
					// 同一个入口不再在侧栏重复一遍。
					if u.pages[i].key == keyPlugins {
						continue
					}
					// 预算与工具、用量已移入设置弹层，不再占侧栏。
					if u.pages[i].key == keyBudget || u.pages[i].key == keyUsage {
						continue
					}
					idx := i
					children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						p := &u.pages[idx]
						return u.sidebarNavItem(gtx, w, p, p.key == u.activeKey)
					}))
				}
				// 插件页单独分组，避免和固定功能页混在一起。
				if hasPanel {
					children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.sidebarSection(gtx, w, "插件页面")
					}))
					for i := range u.pages {
						if u.pages[i].kind != pagePanel {
							continue
						}
						idx := i
						children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							p := &u.pages[idx]
							return u.sidebarNavItem(gtx, w, p, p.key == u.activeKey)
						}))
					}
				}
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.sidebarSection(gtx, w, "会话")
				}))
				for i, s := range snap.Sessions {
					if i >= 6 {
						break
					}
					idx, item := i, s
					children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.sidebarSessionItem(gtx, w, idx, item)
					}))
				}
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.sidebarSection(gtx, w, "工作区")
				}))
				for _, orig := range dirs {
					idx := orig
					children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.sidebarWorkspaceItem(gtx, w, idx, snap.Workspace.Entries[idx])
					}))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
			})
		})
	}

	// 固定脚部：插件的脚部动作 + 账户 + 设置（DSH 把设置触发器钉在侧栏底）
	u.place(gtx, 0, h-footerH, w, footerH, func(g layout.Context) {
		layout.Flex{Axis: layout.Vertical}.Layout(g,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sidebarFooterActions(gtx, w) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sidebarAccount(gtx, w, snap) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sidebarSettings(gtx, w) }),
		)
	})

	// 侧栏与中栏之间的 0.5px 分隔线（DSH border-right）。
	paint.FillShape(gtx.Ops, cBorder, clip.Rect{Min: image.Pt(w-1, 0), Max: image.Pt(w, h)}.Op())
	return layout.Dimensions{Size: image.Pt(w, h)}
}

func (u *UI) sidebarBrand(gtx layout.Context, w int) layout.Dimensions {
	h := gtx.Dp(unit.Dp(60))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, unit.Sp(20), "✦")
				l.Color = cAccent
				return l.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					l := material.Label(u.th, unit.Sp(16), "万物皆插件")
					l.Color = cText
					l.Font.Weight = 600
					return l.Layout(gtx)
				})
			}),
		)
	})
}

func (u *UI) sidebarNewSession(gtx layout.Context, w int) layout.Dimensions {
	h := gtx.Dp(unit.Dp(38))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		bw := gtx.Constraints.Max.X
		bg := cPanelAlt
		border := cBorder
		if u.newSession.Hovered() {
			bg = cHover
		}
		return u.newSession.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			drawRBoxOutline(gtx, bg, border, unit.Dp(radiusMd), bw, h)
			gtx.Constraints = layout.Exact(image.Pt(bw, h))
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, unit.Sp(14), "+  新建对话")
				l.Color = cText
				l.Font.Weight = 500
				return l.Layout(gtx)
			})
		})
	})
}

func (u *UI) sidebarNavItem(gtx layout.Context, w int, p *page, active bool) layout.Dimensions {
	h := gtx.Dp(unit.Dp(42))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	// DSH 的行内绘制缩进 3px：左侧留 8 根 padding + 3 paint inset。
	return layout.Inset{Left: unit.Dp(11), Right: unit.Dp(11)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		bw := gtx.Constraints.Max.X
		bg := cPanel
		if active {
			bg = cNavActive
		} else if p.click.Hovered() {
			bg = cHover
		}
		return p.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			drawRBoxFill(gtx, bg, unit.Dp(radiusMd), bw, h)
			gtx.Constraints = layout.Exact(image.Pt(bw, h))
			return layout.Inset{Left: unit.Dp(9), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				// DSH 侧栏导航行文字统一用 label-primary，选中只换底色。
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, u.th, p.navIcon(), unit.Sp(14), cText)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(9)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return label(gtx, u.th, p.title, unit.Sp(14), cText)
						})
					}),
				)
			})
		})
	})
}

func (u *UI) sidebarSection(gtx layout.Context, w int, title string) layout.Dimensions {
	h := gtx.Dp(unit.Dp(32))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return layout.Inset{Left: unit.Dp(20), Top: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return label(gtx, u.th, title, unit.Sp(12), cTextFaint)
	})
}

func (u *UI) sidebarSessionItem(gtx layout.Context, w, i int, s sessionInfo) layout.Dimensions {
	h := gtx.Dp(unit.Dp(34))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return layout.Inset{Left: unit.Dp(11), Right: unit.Dp(11)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		b := clickAt(&u.sidebarSessionClicks, i)
		bw := gtx.Constraints.Max.X
		bg := cPanel
		if b.Hovered() {
			bg = cHover
		}
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			drawRBoxFill(gtx, bg, unit.Dp(radiusMd), bw, h)
			gtx.Constraints = layout.Exact(image.Pt(bw, h))
			return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				name := s.Name
				if name == "" {
					name = "新会话"
				}
				return label(gtx, u.th, truncate(name, 16), unit.Sp(13), cTextDim)
			})
		})
	})
}

func (u *UI) sidebarWorkspaceItem(gtx layout.Context, w, orig int, e wsEntry) layout.Dimensions {
	h := gtx.Dp(unit.Dp(32))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return layout.Inset{Left: unit.Dp(11), Right: unit.Dp(11)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		b := clickAt(&u.sidebarWsClicks, orig)
		bw := gtx.Constraints.Max.X
		bg := cPanel
		if b.Hovered() {
			bg = cHover
		}
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			drawRBoxFill(gtx, bg, unit.Dp(radiusMd), bw, h)
			gtx.Constraints = layout.Exact(image.Pt(bw, h))
			return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return label(gtx, u.th, "▸  "+truncate(e.Name, 18), unit.Sp(13), cTextDim)
			})
		})
	})
}

// sidebarAccount 脚部账户行。DSH 的 footArea 不带分隔线，这里也保持干净。
func (u *UI) sidebarAccount(gtx layout.Context, w int, snap snapshot) layout.Dimensions {
	h := gtx.Dp(unit.Dp(40))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return layout.Inset{Left: unit.Dp(18), Right: unit.Dp(14), Top: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d := image.Pt(gtx.Dp(unit.Dp(22)), gtx.Dp(unit.Dp(22)))
				gtx.Constraints = layout.Exact(d)
				paint.FillShape(gtx.Ops, cNavActive, clip.Ellipse(image.Rectangle{Max: d}).Op(gtx.Ops))
				return layout.Dimensions{Size: d}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(9)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							balance := snap.Usage.Total
							if balance == "" {
								balance = "—"
							}
							return label(gtx, u.th, "余额  "+balance+" "+snap.Usage.Currency, unit.Sp(13), cText)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							alive := 0
							for _, s := range snap.Services {
								if s.Alive {
									alive++
								}
							}
							return label(gtx, u.th, strconv.Itoa(alive)+" 个服务在线", unit.Sp(11), cTextFaint)
						}),
					)
				})
			}),
		)
	})
}

// sidebarSettings 底部常驻设置入口，对齐 DSH SettingsRoot .triggerRow/.trigger：
//
//	行高 42、圆角 12、上下各留 4；图标 16 + 文字 14/22、gap 8；
//	背景透明，仅悬浮时填充（打开弹层不算选中态，弹层自己会盖住）。
func (u *UI) sidebarSettings(gtx layout.Context, w int) layout.Dimensions {
	h := gtx.Dp(unit.Dp(42))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return layout.Inset{Left: unit.Dp(11), Right: unit.Dp(11), Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		idx := u.pageIndex(keySettings)
		b := &u.pages[idx].click
		bw := gtx.Constraints.Max.X
		bg := cPanel
		if b.Hovered() {
			bg = cHover
		}
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			drawRBoxFill(gtx, bg, unit.Dp(radiusMd), bw, h)
			gtx.Constraints = layout.Exact(image.Pt(bw, h))
			return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, u.th, pageIcon(keySettings), unit.Sp(15), cText)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return label(gtx, u.th, "设置", unit.Sp(14), cText)
						})
					}),
				)
			})
		})
	})
}

// sidebarFooterActions 画插件注册在 sidebar.footer.action 插槽里的动作行。
// 与设置入口同尺寸（42 高、圆角 12、仅 hover 填充），排在设置之上。
func (u *UI) sidebarFooterActions(gtx layout.Context, w int) layout.Dimensions {
	snap := u.bridge.get()
	children := make([]layout.FlexChild, 0, 4)
	for i := range snap.Contribs {
		if !strings.EqualFold(snap.Contribs[i].Slot, slotFooterAction) {
			continue
		}
		idx := i
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.sidebarContributionRow(gtx, w, idx)
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// sidebarContributionRow 画一行插件动作（图标 + 文字），点击由 handleContributionClicks 处理。
func (u *UI) sidebarContributionRow(gtx layout.Context, w, idx int) layout.Dimensions {
	h := gtx.Dp(unit.Dp(42))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	snap := u.bridge.get()
	if idx < 0 || idx >= len(snap.Contribs) {
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	c := snap.Contribs[idx]
	return layout.Inset{Left: unit.Dp(11), Right: unit.Dp(11), Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		b := clickAt(&u.contribBtns, idx)
		bw := gtx.Constraints.Max.X
		bg := cPanel
		if b.Hovered() {
			bg = cHover
		}
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			drawRBoxFill(gtx, bg, unit.Dp(radiusMd), bw, h)
			gtx.Constraints = layout.Exact(image.Pt(bw, h))
			icon := c.Icon
			if icon == "" {
				icon = "·"
			}
			return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, u.th, icon, unit.Sp(15), cText)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return label(gtx, u.th, c.Label, unit.Sp(14), cText)
						})
					}),
				)
			})
		})
	})
}

// 保证 widget 包被引用（列表项里的 Clickable 类型来自它）。
var _ = widget.Clickable{}
