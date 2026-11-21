package main

import (
	"fmt"
	"image"
	"image/color"
	"strconv"
	"strings"

	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// drawTitleBar 自绘标题栏：替代系统原生标题栏。
// 对齐 DSH 的 Windows caption 行：只留一条可拖动条 + 右侧窗口按钮，
// 品牌在侧栏顶部，不在 caption 里重复。
func (u *UI) drawTitleBar(gtx layout.Context) layout.Dimensions {
	h := gtx.Dp(titleBarHeight)
	W := gtx.Constraints.Max.X
	gtx.Constraints = layout.Exact(image.Pt(W, h))
	fill(gtx, cPanel)

	btnW := gtx.Dp(unit.Dp(46))
	dragW := W - btnW*3
	if dragW < 0 {
		dragW = 0
	}
	u.titleDragArea(gtx, dragW, h, func(g layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(dragW, h)}
	})

	// 右侧三个窗口按钮：几何图形，直接在主 ops 上以绝对坐标绘制。
	u.drawWinButton(gtx, dragW, btnW, h, &u.winMin, "min")
	if u.maximized {
		u.drawWinButton(gtx, dragW+btnW, btnW, h, &u.winMax, "restore")
	} else {
		u.drawWinButton(gtx, dragW+btnW, btnW, h, &u.winMax, "max")
	}
	u.drawWinButton(gtx, dragW+2*btnW, btnW, h, &u.winClose, "close")

	// 底部分隔线。
	paint.FillShape(gtx.Ops, cBorder, clip.Rect{Min: image.Pt(0, h-1), Max: image.Pt(W, h)}.Op())
	return layout.Dimensions{Size: image.Pt(W, h)}
}

// titleDragArea 在 (0,0,w,h) 区域绘制内容并注册为窗口可拖动区。
func (u *UI) titleDragArea(gtx layout.Context, w, h int, content layout.Widget) {
	if w <= 0 || h <= 0 {
		return
	}
	macro := op.Record(gtx.Ops)
	c := gtx
	c.Constraints = layout.Exact(image.Pt(w, h))
	content(c)
	body := macro.Stop()
	area := clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops)
	body.Add(gtx.Ops)
	system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
	area.Pop()
}

// drawWinButton 在标题栏绝对坐标 (x,0,w,h) 画一个窗口按钮并注册点击区。
// kind: min / max / restore / close。用几何线条绘制，不依赖字体里的特殊符号。
func (u *UI) drawWinButton(gtx layout.Context, x, w, h int, b *widget.Clickable, kind string) {
	if w <= 0 {
		return
	}
	stack := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)

	// 背景：hover 时高亮，关闭按钮 hover 显示红色。
	bg := color.NRGBA{}
	if b.Hovered() {
		bg = cPanel
		if kind == "close" {
			bg = cErr
		}
	}
	if bg.A > 0 {
		fillDim(gtx, bg, image.Pt(w, h))
	}

	// 图标：画在按钮中心，尺寸约 12×12px。
	fg := cText
	cx, cy := w/2, h/2
	r := gtx.Dp(unit.Dp(6)) // 半边长
	t := gtx.Dp(unit.Dp(1)) // 线宽
	bar := clip.Rect{Min: image.Pt(cx-r, cy-t/2), Max: image.Pt(cx+r, cy+t/2)}
	switch kind {
	case "min":
		paint.FillShape(gtx.Ops, fg, bar.Op())
	case "max":
		paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx-r, cy-r), Max: image.Pt(cx+r, cy-r+t)}.Op()) // 上
		paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx-r, cy+r-t), Max: image.Pt(cx+r, cy+r)}.Op()) // 下
		paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx-r, cy-r), Max: image.Pt(cx-r+t, cy+r)}.Op()) // 左
		paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx+r-t, cy-r), Max: image.Pt(cx+r, cy+r)}.Op()) // 右
	case "restore":
		paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx-r+3, cy-r), Max: image.Pt(cx+r, cy-r+t)}.Op())
		paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx-r+3, cy+r-t), Max: image.Pt(cx+r, cy+r)}.Op())
		paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx-r+3, cy-r), Max: image.Pt(cx-r+3+t, cy+r)}.Op())
		paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx+r-t, cy-r+3), Max: image.Pt(cx+r, cy+r)}.Op())
		paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx-r, cy-r+3), Max: image.Pt(cx+r-t, cy-r+3+t)}.Op())
		paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx-r, cy+r), Max: image.Pt(cx+r-t, cy+r+t)}.Op())
	case "close":
		d := r - 1
		// 两条斜线用若干小方块近似，避免引入 f32 path 依赖。
		for i := -d; i <= d; i += t {
			paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx+i, cy+i), Max: image.Pt(cx+i+t, cy+i+t)}.Op())
			paint.FillShape(gtx.Ops, fg, clip.Rect{Min: image.Pt(cx+i, cy-i), Max: image.Pt(cx+i+t, cy-i+t)}.Op())
		}
	}

	// 点击区：与图标共用当前偏移栈。
	b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
	stack.Pop()
}

// pageSubtitle 顶栏里当前页的一句话说明（对齐 DSH 页面标题下的副标题）。
func pageSubtitle(key string) string {
	switch key {
	case keyChat:
		return "万物皆插件 · 本地对话工作台"
	case keyRole:
		return "给 AI 设定人设，启用后对当前会话生效"
	case keyBudget:
		return "限制工具调用步数，防止跑飞"
	case keyImageGen:
		return "输入画面描述，图片保存到工作区"
	case keyPlugins:
		return "安装、启用和配置插件"
	case keyWorkspace:
		return "AI 读写文件的根目录"
	case keyUsage:
		return "账户余额与本机累计消耗"
	case keySettings:
		return "模型、API 与运行选项"
	}
	return "万物皆插件"
}

// drawTopBar 会话头（对照 DSH ConversationRoot .header）：
// 无 tab 时 10px 上内边 + 30px 标题行 + 10px 下内边 = 50px，
// 左 20 / 右 28 内边，底边 0.5px 分隔线；右侧是 28px 工具栏控件。
func (u *UI) drawTopBar(gtx layout.Context) layout.Dimensions {
	h := gtx.Dp(topBarHeight)
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	fill(gtx, cBg)

	// 底部分隔线
	defer paint.FillShape(gtx.Ops, cBorder, clip.Rect{Min: image.Pt(0, h-1), Max: image.Pt(gtx.Constraints.Max.X, h)}.Op())

	snap := u.bridge.get()
	ap := u.activePage()
	gtx.Constraints.Min.Y = gtx.Dp(unit.Dp(30))
	gtx.Constraints.Max.Y = gtx.Dp(unit.Dp(30))
	return layout.Inset{Left: unit.Dp(20), Right: unit.Dp(28), Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, unit.Sp(15), ap.title)
				l.Color = cText
				l.Font.Weight = 500
				return l.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, pageSubtitle(ap.key), unit.Sp(12), cTextFaint)
				})
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
		}
		// 插件注册在 header.action 的动作，排在固定工具之前。
		for _, i := range u.headerActions() {
			idx := i
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					c := u.bridge.get().Contribs
					if idx >= len(c) {
						return layout.Dimensions{}
					}
					b := clickAt(&u.contribBtns, idx)
					bg := cPanel
					if b.Hovered() {
						bg = cHover
					}
					txt := c[idx].Label
					if c[idx].Icon != "" {
						txt = c[idx].Icon + " " + txt
					}
					return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return u.pill(gtx, txt, bg, cTextDim)
					})
				})
			}))
		}
		children = append(children,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				txt := "▤ 详情"
				if u.showRight {
					txt = "▤ 收起"
				}
				bg := cPanel
				if u.toggleRight.Hovered() {
					bg = cHover
				}
				return u.toggleRight.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return u.pill(gtx, txt, bg, cTextDim)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					bg := cPanel
					if u.restartBtn.Hovered() {
						bg = cHover
					}
					return u.restartBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return u.pill(gtx, "⟳ 重启", bg, cTextDim)
					})
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					model := snap.Model
					if model == "" {
						model = "未连接模型"
					}
					return u.pill(gtx, "◈ "+model, cPanel, cTextDim)
				})
			}),
		)
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
	})
}

// pill 统一的 28px 小控件（DSH header utilities / 菜单触发器尺寸）。
func (u *UI) pill(gtx layout.Context, txt string, bg, fg color.NRGBA) layout.Dimensions {
	h := gtx.Dp(unit.Dp(28))
	gtx.Constraints.Min.Y = h
	gtx.Constraints.Max.Y = h

	// 先录制文字并量宽，再按宽度画胶囊。
	macro := op.Record(gtx.Ops)
	l := material.Label(u.th, unit.Sp(13), txt)
	l.Color = fg
	l.Font.Weight = 500
	textDims := l.Layout(gtx)
	textOps := macro.Stop()

	padX := gtx.Dp(unit.Dp(4))
	box := image.Pt(textDims.Size.X+padX*2, h)
	drawRBoxFill(gtx, bg, unit.Dp(radiusSm), box.X, box.Y)
	stack := op.Offset(image.Pt(padX, (h-textDims.Size.Y)/2)).Push(gtx.Ops)
	textOps.Add(gtx.Ops)
	stack.Pop()
	return layout.Dimensions{Size: box}
}

// chatColumnW 聊天正文列宽（DSH --dsh-chat-content-width）：
// clamp(680, 中栏宽 * 0.64, 920)。
func (u *UI) chatColumnW(gtx layout.Context) int {
	avail := gtx.Constraints.Max.X
	w := int(float64(avail) * 0.64)
	if min := gtx.Dp(unit.Dp(680)); w < min {
		w = min
	}
	if max := gtx.Dp(unit.Dp(chatMaxWidth)); w > max {
		w = max
	}
	if w > avail {
		w = avail
	}
	return w
}

// drawChat 中间对话区（对照 DSH ChatView / ConversationRoot）：
// 正文列居中，最大 920dp；助手消息不带气泡，用户消息右对齐气泡（最大 82% 列宽）。
func (u *UI) drawChat(gtx layout.Context) layout.Dimensions {
	fill(gtx, cBg)
	snap := u.bridge.get()

	if len(snap.Path) == 0 && !snap.Thinking {
		// 空状态：DSH hero —— 26/32 wt500 标题 + 居中快捷提问。
		if len(u.chatSuggestBtns) < len(chatSuggestions) {
			u.chatSuggestBtns = append(u.chatSuggestBtns, make([]widget.Clickable, len(chatSuggestions)-len(u.chatSuggestBtns))...)
		}
		return layout.Inset{Left: unit.Dp(24), Right: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				suggestions := make([]layout.FlexChild, 0, len(chatSuggestions))
				for i := range chatSuggestions {
					idx := i
					suggestions = append(suggestions, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.chip(gtx, clickAt(&u.chatSuggestBtns, idx), chatSuggestions[idx], false)
					}))
				}
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := material.Label(u.th, unit.Sp(30), "✦")
						l.Color = cAccent
						return l.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := material.Label(u.th, unit.Sp(26), "有什么可以帮你？")
						l.Color = cText
						l.Font.Weight = 500
						return l.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, u.th, "输入消息开始对话 · Enter 发送", unit.Sp(13), cTextFaint)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(20)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, suggestions...)
					}),
				)
			})
		})
	}

	return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16), Top: unit.Dp(16), Bottom: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return u.centerWidth(gtx, u.chatColumnW(gtx), func(gtx layout.Context) layout.Dimensions {
			count := len(snap.Path)
			if snap.Thinking {
				count++
			}
			return u.chatList.Layout(gtx, count, func(gtx layout.Context, i int) layout.Dimensions {
				if i < len(snap.Path) {
					return u.messageRow(gtx, snap.Path[i])
				}
				return u.thinkingRow(gtx, snap.Trace)
			})
		})
	})
}

// messageRow 一条消息：用户右对齐气泡，助手直接排在底色上。
func (u *UI) messageRow(gtx layout.Context, m msgView) layout.Dimensions {
	isUser := m.Role == "user"
	if isUser {
		return layout.Inset{Bottom: unit.Dp(bubbleGap)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			// Gio 的 Flex 只按「最宽子项」对齐交叉轴，不会铺满整行；
			// 想让气泡靠右，必须用一个 Flexed 空位把气泡挤到右侧。
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					// DSH .userStack：max-width = 列宽 * 0.702，且不超过 82%。
					gtx.Constraints.Max.X = int(float64(gtx.Constraints.Max.X) * 0.82)
					gtx.Constraints.Min.X = 0
					return drawRBox(gtx, cUserBub, unit.Dp(radiusLg), func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(14), Top: unit.Dp(10), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							l := material.Label(u.th, unit.Sp(14), m.Text)
							l.Color = cText
							l.WrapPolicy = text.WrapWords
							return l.Layout(gtx)
						})
					})
				}),
			)
		})
	}
	return layout.Inset{Bottom: unit.Dp(bubbleGap)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, "AI", unit.Sp(12), cAccent)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, unit.Sp(14), m.Text)
				l.Color = cText
				l.WrapPolicy = text.WrapWords
				return l.Layout(gtx)
			}),
		)
	})
}

// thinkingRow 模型生成中的过程块（思考 + 工具调用），比正文更淡。
func (u *UI) thinkingRow(gtx layout.Context, t traceState) layout.Dimensions {
	return layout.Inset{Bottom: unit.Dp(bubbleGap)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return label(gtx, u.th, "AI · 运行中", unit.Sp(12), cTextFaint)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return drawRBox(gtx, cAiBub, unit.Dp(radiusMd), func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Top: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						l := material.Label(u.th, unit.Sp(13), thinkingText(t))
						l.Color = cTextDim
						l.WrapPolicy = text.WrapWords
						return l.Layout(gtx)
					})
				})
			}),
		)
	})
}

func thinkingText(t traceState) string {
	var b strings.Builder
	b.WriteString("思考中")
	if t.Round > 0 {
		b.WriteString(" · 第 ")
		b.WriteString(strconv.Itoa(t.Round))
		b.WriteString(" 轮")
	}
	if t.Phase != "" {
		b.WriteString(" · ")
		b.WriteString(t.Phase)
	}
	b.WriteString("…")
	if strings.TrimSpace(t.Think) != "" {
		b.WriteString("\n")
		b.WriteString(truncate(t.Think, 120))
	}
	for _, s := range t.Steps {
		b.WriteString("\n✓ ")
		b.WriteString(s.Tool)
		if s.Done {
			b.WriteString(" · 完成")
		}
	}
	return b.String()
}

// wrapText 按宽度折行（简单按 rune 数，中文按 2 列估算留给 Gio 自己排版）。
func wrapText(s string, width int) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		r := []rune(ln)
		for len(r) > width {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		out = append(out, string(r))
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// drawInput 底部输入卡片（对照 DSH InputBar .card）：
// 最大宽 952dp 居中，radius 28，0.5px 描边；上排输入、下排工具行 + 34px 圆形发送。
func (u *UI) drawInput(gtx layout.Context) layout.Dimensions {
	h := gtx.Dp(inputMinHeight)
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	fill(gtx, cBg)

	return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		// 输入卡片 = 正文列 + 32，且不超过 952（DSH --dsh-composer-card-max-width）。
		cardW := u.chatColumnW(gtx) + gtx.Dp(unit.Dp(32))
		if max := gtx.Dp(unit.Dp(composerMaxW)); cardW > max {
			cardW = max
		}
		return u.centerWidth(gtx, cardW, func(gtx layout.Context) layout.Dimensions {
			w := gtx.Constraints.Max.X
			ch := gtx.Constraints.Max.Y
			gtx.Constraints = layout.Exact(image.Pt(w, ch))
			drawRBoxOutline(gtx, cPanelAlt, cBorder, unit.Dp(radiusPanel), w, ch)
			return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(8), Top: unit.Dp(12), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.composerEditor(gtx) }),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.composerTools(gtx) }),
				)
			})
		})
	})
}

// composerEditor 输入区：14/24 正文，最多约 3 行后内部滚动由 Editor 负责。
func (u *UI) composerEditor(gtx layout.Context) layout.Dimensions {
	minH := gtx.Dp(unit.Dp(36))
	gtx.Constraints.Min.Y = minH
	ed := material.Editor(u.th, &u.editor, "输入消息，Enter 发送…")
	ed.TextSize = unit.Sp(14)
	ed.Color = cText
	ed.HintColor = cTextFaint
	return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return ed.Layout(gtx)
	})
}

// composerTools 工具行：左提示、右 34px 圆形发送（DSH .row + .primary）。
func (u *UI) composerTools(gtx layout.Context) layout.Dimensions {
	h := gtx.Dp(unit.Dp(34))
	gtx.Constraints.Min.Y = h
	gtx.Constraints.Max.Y = h
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return label(gtx, u.th, "Enter 发送 · Shift+Enter 换行", unit.Sp(12), cTextFaint)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.sendCircle(gtx) }),
	)
}

func (u *UI) sendCircle(gtx layout.Context) layout.Dimensions {
	d := gtx.Dp(unit.Dp(34))
	gtx.Constraints = layout.Exact(image.Pt(d, d))
	bg := cAccent
	if u.send.Hovered() {
		bg = color.NRGBA{R: 0x9C, G: 0xC2, B: 0xFF, A: 0xFF}
	}
	paint.FillShape(gtx.Ops, bg, clip.Ellipse(image.Rectangle{Max: image.Pt(d, d)}).Op(gtx.Ops))
	dims := layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		l := material.Label(u.th, unit.Sp(16), "↑")
		l.Color = cBg
		l.Font.Weight = 700
		return l.Layout(gtx)
	})
	u.send.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return dims })
	return dims
}

// drawRightPanel 右侧信息面板（服务链路 + 插件）。
func (u *UI) drawRightPanel(gtx layout.Context) layout.Dimensions {
	w := gtx.Dp(panelWidth)
	h := gtx.Constraints.Max.Y
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	fill(gtx, cPanel)

	snap := u.bridge.get()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16), Top: unit.Dp(16), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				alive := 0
				for _, s := range snap.Services {
					if s.Alive {
						alive++
					}
				}
				col := cTextFaint
				if alive > 0 && alive < len(snap.Services) {
					col = cWarn
				} else if len(snap.Services) > 0 && alive == len(snap.Services) {
					col = cOk
				}
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, u.th, "服务链路", unit.Sp(13), cTextDim)
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, u.th, fmt.Sprintf("%d/%d 在线", alive, len(snap.Services)), unit.Sp(10), col)
					}),
				)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16), Bottom: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				list := layout.List{Axis: layout.Vertical}
				return list.Layout(gtx, len(snap.Services), func(gtx layout.Context, i int) layout.Dimensions {
					s := snap.Services[i]
					dot := cErr
					nameCol := cTextFaint
					sub := s.Note
					if s.Alive {
						dot = cOk
						nameCol = cTextDim
						sub = s.Provider
					}
					if sub == "" {
						sub = "未就绪"
					}
					return layout.Inset{Bottom: unit.Dp(7)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										d := image.Pt(gtx.Dp(unit.Dp(7)), gtx.Dp(unit.Dp(7)))
										gtx.Constraints = layout.Exact(d)
										paint.FillShape(gtx.Ops, dot, clip.Ellipse(image.Rectangle{Max: d}).Op(gtx.Ops))
										return layout.Dimensions{Size: d}
									}),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
											return label(gtx, u.th, s.Name, unit.Sp(11), nameCol)
										})
									}),
								)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Left: unit.Dp(15), Top: unit.Dp(1)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return label(gtx, u.th, sub, unit.Sp(10), cTextFaint)
								})
							}),
						)
					})
				})
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(16), Top: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return label(gtx, u.th, "插件", unit.Sp(13), cTextDim)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				if len(u.pluginClicks) < len(snap.Plugins) {
					u.pluginClicks = append(u.pluginClicks, make([]widget.Clickable, len(snap.Plugins)-len(u.pluginClicks))...)
				}
				return u.pluginList.Layout(gtx, len(snap.Plugins), func(gtx layout.Context, i int) layout.Dimensions {
					p := snap.Plugins[i]
					name := p.Name
					if name == "" {
						name = p.ID
					}
					col := cText
					if !p.Alive {
						col = cTextFaint
					}
					return layout.Inset{Bottom: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return label(gtx, u.th, "· "+name, unit.Sp(11), col)
					})
				})
			})
		}),
	)
}
