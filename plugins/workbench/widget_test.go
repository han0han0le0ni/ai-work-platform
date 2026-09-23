package main

import (
	"testing"

	"aiwork/sdk/go/plugin"
)

// seedWidget 让假后端报一个"自带浮窗"的插件。
func seedWidget(fb *fakeBackend) {
	fb.plugins = append(fb.plugins, pluginInfo{
		ID: "com.official.usage", Provides: []string{"ui.widget.usage.v1"}, Alive: []string{"ui.widget.usage.v1"},
	})
	fb.widgetInfos = []plugin.WidgetInfo{{ID: "usage", Title: "用量", W: 30, H: 7}}
	fb.widgetOut = plugin.WidgetRender{
		Title: "用量",
		Lines: []string{"┌─ 用量 ┐", "│ 余额 ¥1 │", "└──────┘"},
	}
}

// 从插件清单里挑"自带浮窗"的服务。
func TestWidgetServices(t *testing.T) {
	got := widgetServices([]pluginInfo{
		{Alive: []string{"usage.v1", "ui.widget.usage.v1"}},
		{Alive: []string{"ui.page.demo.v1"}},
	})
	if len(got) != 1 || got[0] != "ui.widget.usage.v1" {
		t.Fatalf("只该挑出 ui.widget.*，实际 %v", got)
	}
}

// 发现浮窗服务 → 问清单 → 要内容，一路走完。
func TestWidgetDiscoveryAndRender(t *testing.T) {
	fb := &fakeBackend{}
	seedWidget(fb)
	a := newTestApp(fb)

	a.applyOutcome(outcome{kind: "plugins", plugins: fb.plugins})
	drain(t, a) // 浮窗清单
	drain(t, a) // 浮窗内容

	if len(a.st.widgets) != 1 {
		t.Fatalf("应发现 1 个浮窗，实际 %+v", a.st.widgets)
	}
	w := a.st.widgets[0]
	if w.svc != "ui.widget.usage.v1" || w.title != "用量" || !w.vis {
		t.Fatalf("浮窗信息不对：%+v", w)
	}
	if len(w.lines) == 0 {
		t.Fatal("浮窗内容应已拉回来")
	}
}

// 按在浮窗上能抓住、拖动、松手后位置落在屏幕内。
func TestWidgetDrag(t *testing.T) {
	fb := &fakeBackend{}
	seedWidget(fb)
	a := newTestApp(fb)
	a.applyOutcome(outcome{kind: "plugins", plugins: fb.plugins})
	drain(t, a)
	drain(t, a)
	layout(a, 100, 30)

	x, y := a.st.widgets[0].origin(100, 30)
	if !a.grabWidget(x+1, y) {
		t.Fatal("按在浮窗上应抓住")
	}
	a.dragWidget(x+8, y+5)
	a.dropWidget()

	if a.st.widgets[0].x <= 0 || a.st.widgets[0].y <= 0 {
		t.Fatal("拖过之后位置应是正数")
	}
	if a.st.widgets[0].x > 100 || a.st.widgets[0].y > 30 {
		t.Fatalf("浮窗不该跑到屏幕外：%+v", a.st.widgets[0])
	}
}

// 没拖动就松手，是"轻点收起"；拖出界要被夹回来。
func TestWidgetFoldAndClamp(t *testing.T) {
	fb := &fakeBackend{}
	seedWidget(fb)
	a := newTestApp(fb)
	a.applyOutcome(outcome{kind: "plugins", plugins: fb.plugins})
	drain(t, a)
	drain(t, a)
	layout(a, 100, 30)

	x, y := a.st.widgets[0].origin(100, 30)
	a.grabWidget(x+1, y)
	a.dropWidget() // 没拖：toggle 收起
	if !a.st.widgets[0].fold {
		t.Fatal("轻点应切换成收起")
	}

	// 拖到屏幕外：夹回来
	a.grabWidget(x+1, y)
	a.dragWidget(-50, -50)
	a.dropWidget()
	if a.st.widgets[0].x < 1 || a.st.widgets[0].y < 1 {
		t.Fatalf("位置应被夹在屏幕内：%+v", a.st.widgets[0])
	}
}

// 浮窗说"我变了"，内容要重新拉一帧。
func TestWidgetChangedRefreshes(t *testing.T) {
	fb := &fakeBackend{}
	seedWidget(fb)
	a := newTestApp(fb)
	a.applyOutcome(outcome{kind: "plugins", plugins: fb.plugins})
	drain(t, a)
	drain(t, a)

	before := a.st.widgets[0].lines
	fb.mu.Lock()
	fb.widgetOut = plugin.WidgetRender{Title: "用量", Lines: []string{"新的一行"}}
	fb.mu.Unlock()

	a.invalidateWidget("ui.widget.usage.v1", "usage")
	drain(t, a) // 重新要的一帧

	if len(a.st.widgets[0].lines) != 1 || a.st.widgets[0].lines[0] != "新的一行" {
		t.Fatalf("内容应刷新，之前 %v 现在 %v", before, a.st.widgets[0].lines)
	}
}

// 插件没了，浮窗也跟着摘掉。
func TestWidgetGoneWithPlugin(t *testing.T) {
	fb := &fakeBackend{}
	seedWidget(fb)
	a := newTestApp(fb)
	a.applyOutcome(outcome{kind: "plugins", plugins: fb.plugins})
	drain(t, a)
	drain(t, a)
	if len(a.st.widgets) != 1 {
		t.Fatalf("应先有 1 个浮窗，实际 %d", len(a.st.widgets))
	}

	// 插件没了：清单里没有 ui.widget.* 服务
	a.applyOutcome(outcome{kind: "plugins", plugins: nil})
	if len(a.st.widgets) != 0 {
		t.Fatalf("插件没了浮窗也该没，实际 %+v", a.st.widgets)
	}
}
