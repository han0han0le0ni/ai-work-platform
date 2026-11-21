package main

import (
	"encoding/json"
	"strconv"
	"strings"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// 通用浮窗：插件声明 ui.widget.<名字>.v1，内容由插件 render，宿主负责画、拖、收起。
//
// 和页面（ui.page.*）的区别：页面占一整块标签页，浮窗浮在任意位置、不被翻页
// 影响。以前"用量浮窗"是界面为 usage-monitor 硬编码的（数据靠 usage.snapshot
// 事件、画面写死在 float.go）；现在反过来 —— 任意插件都能有一个可拖的浮窗，
// 内容它自己 render，界面只借一块地方 + 记住拖到哪儿。
// ---------------------------------------------------------------------------

const (
	widgetServicePrefix = "ui.widget."
	widgetChangedEvent  = "ui.widget.changed"
	widgetGrabPrefix    = "widget:" // 浮窗热区动作：widget:<下标>
)

// widgetView 是一个浮窗在界面侧的状态。
type widgetView struct {
	svc   string
	id    string
	title string
	w, h  int

	x, y int // -1 = 还没拖过，贴右上角
	fold bool
	vis  bool

	lines []string     // 缓存的展开态内容（插件 render 回来的）
	hits  []plugin.Hit // 浮窗上的可点区域（坐标相对浮窗左上角）
	dirty bool         // 内容可能过期，下一帧重新 render
}

// widgetDrag 是一次拖动浮窗的中间状态。
type widgetDrag struct {
	idx    int
	gx, gy int
	moved  bool
}

// widgetServices 从插件清单里挑出"自带浮窗"的服务。
func widgetServices(ps []pluginInfo) []string {
	var out []string
	for _, p := range ps {
		for _, svc := range p.Alive {
			if strings.HasPrefix(svc, widgetServicePrefix) {
				out = append(out, svc)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 几何
// ---------------------------------------------------------------------------

// widgetOrigin 算出浮窗左上角，并夹在屏幕内。没拖过就贴右上角（避开标题行）。
func (v *widgetView) origin(screenW, screenH int) (int, int) {
	fw, fh := v.size()
	x, y := v.x, v.y
	if x <= 0 || y <= 0 {
		x, y = screenW-fw-1, 1
	}
	return clampInt(x, 1, maxInt(1, screenW-fw-1)), clampInt(y, 1, maxInt(1, screenH-fh-1))
}

// size 是当前形态的宽高：收起是一条，展开是插件报的 w×h。
func (v *widgetView) size() (int, int) {
	if v.fold {
		return widgetFoldW(v.title), 1
	}
	w, h := v.w, v.h
	if w < 6 {
		w = 6
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

// widgetFoldW 是收起后那一条的宽：标题 + 两个空格 + 边框。
func widgetFoldW(title string) int {
	return dispWidth(" "+title+" ") + 2
}

// chip 是收起后那一条：只留标题，挡不住什么。
func (v *widgetView) chip(color bool) string {
	w := widgetFoldW(v.title)
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	body := truncDisplay(" "+v.title+" ", inner)
	l := newLine(color)
	l.styled(cBtnBG+cBold+cAccent2, " "+padDisplay(body, inner)+" ")
	return l.String()
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

// drawWidgets 把每个浮窗贴到画布上，并登记热区。浮窗盖在版面之上，热区排最前。
func drawWidgets(c *canvas, st *state) {
	if len(st.widgets) == 0 {
		return
	}
	// 先把行数补满到 h：frame() 补的空行若晚于浮窗出现，浮窗就贴不上去了
	_ = c.frame()

	for i := range st.widgets {
		v := &st.widgets[i]
		if !v.vis {
			continue
		}
		fw, fh := v.size()
		x, y := v.origin(c.w, c.h)
		lines := v.curLines(c.color)
		c.overlay(x, y, fw, lines)

		grab := widgetGrabPrefix + strconv.Itoa(i)
		boxes := make([]hitbox, 0, fh)
		for row := 0; row < fh; row++ {
			boxes = append(boxes, hitbox{x: x, y: y + row, w: fw, action: grab})
		}
		// 浮窗自己的可点区域（若有）
		for _, h := range v.hits {
			if h.Y < 0 || h.Y >= fh || h.W <= 0 {
				continue
			}
			boxes = append(boxes, hitbox{x: x + h.X, y: y + h.Y, w: h.W, action: "widgetact:" + strconv.Itoa(i) + ":" + h.Action, label: h.Arg})
		}
		c.boxes = append(boxes, c.boxes...)
	}
}

// curLines 返回当前该画的那几行：收起就一条 chip，展开就用缓存的内容。
func (v *widgetView) curLines(color bool) []string {
	if v.fold {
		return []string{v.chip(color)}
	}
	if len(v.lines) == 0 {
		// 还没要到内容：画个占位，别让浮窗凭空消失
		return []string{v.padLine(color, " …")}
	}
	out := make([]string, 0, len(v.lines))
	for _, ln := range v.lines {
		out = append(out, v.padLine(color, ln))
	}
	return out
}

// padLine 把插件给的一行校正到恰好 fw 列：宽了裁、窄了补，样式码先剥掉再量。
func (v *widgetView) padLine(color bool, ln string) string {
	fw, _ := v.size()
	pw := dispWidth(stripANSI(ln))
	switch {
	case pw == fw:
		if color {
			return ln
		}
		return stripANSI(ln)
	case pw > fw:
		return padDisplay(cutDisplay(stripANSI(ln), fw), fw)
	default:
		return stripANSI(ln) + strings.Repeat(" ", fw-pw)
	}
}

// ---------------------------------------------------------------------------
// 拖动 / 挪动
// ---------------------------------------------------------------------------

// grabWidget 按下时若落在某浮窗上就抓住它，返回 true 表示这次按下归浮窗。
func (a *app) grabWidget(x, y int) bool {
	if a.canv == nil {
		return false
	}
	action := a.canv.hit(x, y)
	if !strings.HasPrefix(action, widgetGrabPrefix) {
		return false
	}
	idx := atoiSafe(strings.TrimPrefix(action, widgetGrabPrefix))
	if idx < 0 || idx >= len(a.st.widgets) {
		return false
	}
	v := &a.st.widgets[idx]
	fx, fy := v.origin(a.st.w, a.st.h)
	a.st.widgetDrag = &widgetDrag{idx: idx, gx: x - fx, gy: y - fy}
	return true
}

// dragWidget 跟着鼠标走。
func (a *app) dragWidget(x, y int) {
	d := a.st.widgetDrag
	if d == nil {
		return
	}
	v := &a.st.widgets[d.idx]
	fw, fh := v.size()
	a.setWidget(d.idx, x-d.gx, y-d.gy, fw, fh)
	d.moved = true
}

// dropWidget 松手：拖过就是挪位置，没拖动就是一次轻点 —— 收起 / 展开。
func (a *app) dropWidget() {
	d := a.st.widgetDrag
	if d == nil {
		return
	}
	a.st.widgetDrag = nil
	if !d.moved {
		a.st.widgets[d.idx].fold = !a.st.widgets[d.idx].fold
	}
	a.saveWidget(d.idx)
}

// setWidget 挪到新位置，并夹在屏幕内。
func (a *app) setWidget(idx, x, y, fw, fh int) {
	v := &a.st.widgets[idx]
	v.x = clampInt(x, 1, maxInt(1, a.st.w-fw-1))
	v.y = clampInt(y, 1, maxInt(1, a.st.h-fh-1))
}

// nudgeWidget 是 Alt+方向键：挪当前第一个可见浮窗。
func (a *app) nudgeWidget(dx, dy int) {
	for i := range a.st.widgets {
		if a.st.widgets[i].vis {
			fw, fh := a.st.widgets[i].size()
			fx, fy := a.st.widgets[i].origin(a.st.w, a.st.h)
			a.setWidget(i, fx+dx, fy+dy, fw, fh)
			a.saveWidget(i)
			return
		}
	}
}

// saveWidget 把某个浮窗的位置落盘（回调由插件层注入，界面线程不做 RPC）。
func (a *app) saveWidget(idx int) {
	if a.st.widgetSave == nil || idx < 0 || idx >= len(a.st.widgets) {
		return
	}
	v := a.st.widgets[idx]
	a.st.widgetSave(v.svc, v.id, v.x, v.y, v.fold)
}

// ---------------------------------------------------------------------------
// 状态同步：发现 → 加载 → 内容刷新
// ---------------------------------------------------------------------------

// syncWidgets 把当前插件清单里"自带浮窗"的服务和手上的浮窗列表对齐。
//
// 服务多了就补、少了就摘（插件被删 / 崩了，浮窗也跟着没）。跑在界面线程。
func (a *app) syncWidgets() {
	want := widgetServices(a.st.plugins)
	have := make(map[string]bool)
	for i := range a.st.widgets {
		have[a.st.widgets[i].svc] = true
	}
	// 摘掉已经不存在的服务
	kept := a.st.widgets[:0]
	for _, v := range a.st.widgets {
		if containsString(want, v.svc) {
			kept = append(kept, v)
		}
	}
	a.st.widgets = kept
	// 补上新的服务（下一轮拉清单）
	for _, svc := range want {
		if have[svc] {
			continue
		}
		a.loadWidgets(svc)
	}
}

// loadWidgets 问一个服务有哪些浮窗，并要第一帧内容。跑在界面线程，RPC 丢后台。
func (a *app) loadWidgets(svc string) {
	go func() {
		infos, err := a.plug.widgetList(svc)
		if err != nil {
			a.results <- outcome{kind: "widgets", widgetSvc: svc, err: err}
			return
		}
		a.results <- outcome{kind: "widgets", widgetSvc: svc, widgetInfos: infos}
	}()
}

// applyWidgets 把某个服务的浮窗清单落进状态，并标记要拉内容。
func (a *app) applyWidgets(svc string, infos []plugin.WidgetInfo) {
	for _, info := range infos {
		if strings.TrimSpace(info.ID) == "" {
			continue
		}
		a.st.widgets = append(a.st.widgets, widgetView{
			svc: svc, id: info.ID, title: firstNonEmpty(info.Title, info.ID),
			w: info.W, h: info.H, x: -1, y: -1, vis: true, dirty: true,
		})
		// 读上次拖到的位置（异步：界面线程不做 RPC）
		go func(svc, id string) {
			x, y, fold := a.plug.widgetPos(svc, id)
			a.post(func() { a.restoreWidgetPos(svc, id, x, y, fold) })
		}(svc, info.ID)
	}
	a.pumpWidgets()
}

// restoreWidgetPos 把读回来的位置落到对应浮窗上。
func (a *app) restoreWidgetPos(svc, id string, x, y int, fold bool) {
	for i := range a.st.widgets {
		if a.st.widgets[i].svc == svc && a.st.widgets[i].id == id {
			a.st.widgets[i].x, a.st.widgets[i].y, a.st.widgets[i].fold = x, y, fold
			return
		}
	}
}

// pumpWidgets 把"内容还没要"的浮窗统一去要一帧。跑在界面线程，RPC 丢后台。
func (a *app) pumpWidgets() {
	for i := range a.st.widgets {
		v := &a.st.widgets[i]
		if !v.dirty {
			continue
		}
		v.dirty = false
		go func(idx int, svc, id string) {
			out, err := a.plug.widgetRender(svc, id)
			a.results <- outcome{kind: "widgetrender", widgetIdx: idx, widgetOut: out, err: err}
		}(i, v.svc, v.id)
	}
}

// applyWidgetRender 把一帧浮窗内容落进状态。
func (a *app) applyWidgetRender(idx int, out plugin.WidgetRender, err error) {
	if idx < 0 || idx >= len(a.st.widgets) {
		return
	}
	v := &a.st.widgets[idx]
	if err != nil {
		v.lines = []string{" 浮窗没画出来：" + err.Error()}
		v.hits = nil
		return
	}
	if out.Title != "" {
		v.title = out.Title
	}
	v.lines = out.Lines
	v.hits = out.Hits
}

// invalidateWidget 把某个浮窗标记为"内容过期"，下一帧重新要。
func (a *app) invalidateWidget(svc, id string) {
	for i := range a.st.widgets {
		if (svc == "" || a.st.widgets[i].svc == svc) && (id == "" || a.st.widgets[i].id == id) {
			a.st.widgets[i].dirty = true
		}
	}
	a.pumpWidgets()
}

// widgetChangedRef 从事件载荷里取出"哪个浮窗变了"。
func widgetChangedRef(payload json.RawMessage) (svc, id string) {
	var in struct {
		Service string `json:"service"`
		ID      string `json:"id"`
	}
	_ = json.Unmarshal(payload, &in)
	return in.Service, in.ID
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
