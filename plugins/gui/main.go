// Command gui 是原生桌面界面插件（provides: gui.v1）。
//
// 它和 webui 一样作为核心加载的插件进程运行，但用 Gio 自绘一个原生窗口，
// 不依赖浏览器、不依赖网页技术。中文界面、暗色主题、三栏工作台布局。
//
// 线程模型（关键）：
//   - Gio 要求窗口与事件循环跑在程序的主 goroutine（app.Main）。
//   - SDK 的 Serve 是一个阻塞的 JSON-RPC 主循环，必须在另一个 goroutine 跑。
//     它只用 os.Stdin/os.Stdout，不占用主线程，两者互不干扰。
package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"aiwork/sdk/go/plugin"
	"gioui.org/app"
	"gioui.org/op"
)

// guiPlugin 实现 SDK 的 Plugin 接口。窗口逻辑在 main 的 Gio goroutine 里，
// 这里只负责注册服务、订阅事件、持有数据桥。
type guiPlugin struct {
	bridge *bridge
	ready  chan struct{} // Activate 建好 bridge 后 close；窗口 goroutine 等它
	once   sync.Once
}

func (g *guiPlugin) Activate(ctx *plugin.Context) error {
	g.bridge = newBridge(ctx)
	g.bridge.subscribe()
	_ = ctx.Provide("gui.v1", map[string]any{
		"kind":        "native",
		"description": "原生桌面界面（Gio）",
	})
	// 通知窗口 goroutine：bridge 已就绪，可以建 UI 了。
	g.once.Do(func() { close(g.ready) })
	// 首帧数据：异步拉，避免卡住 Activate。
	go g.bridge.refresh()
	// 其他插件可能比 GUI 晚激活：稍后再补探测一次服务链路/模型。
	go func() {
		time.Sleep(2 * time.Second)
		g.bridge.reprobe()
	}()
	return nil
}

func (g *guiPlugin) Handle(service, method string, args json.RawMessage) (any, error) {
	return map[string]any{"ok": true}, nil
}

func (g *guiPlugin) Deactivate() error { return nil }

func main() {
	g := &guiPlugin{ready: make(chan struct{})}

	// JSON-RPC 主循环放后台；Gio 窗口留在主线程。
	go func() {
		if err := plugin.Serve(g); err != nil {
			os.Exit(1)
		}
	}()

	runWindow(g)
}

// runWindow 在主 goroutine 跑 Gio 应用；窗口创建放在单独的 goroutine 是 Gio 的标准范式。
func runWindow(g *guiPlugin) {
	go func() {
		// 等 Activate 建好 bridge 再建 UI，避免空指针竞态。
		<-g.ready
		w := new(app.Window)
		w.Option(
			app.Title("AI 工作平台"),
			app.Size(1280, 800),
			app.MinSize(960, 600),
			// 关闭系统原生标题栏（浅色主题的白色横条）。改为自绘暗色标题栏。
			app.Decorated(false),
		)
		ui := newUI(g.bridge, w)
		g.bridge.setOnChange(func() { ui.invalidate() })
		g.bridge.setOnPluginState(func() {
			// 服务陆续就绪后补拉：当前页 + 插件 UI 贡献（侧栏 / 顶栏 / 脚部）。
			// 贡献缓存在后台线程置失效，真正的汇总在 UI 线程下一帧发起。
			ui.reloadActive()
			g.bridge.invalidateContribs()
			ui.markPanelsDirty()
		})
		// 启动时发现一次 ui.page.* 页面（异步，避免卡第一帧）。
		go func() {
			ui.markPanelsDirty()
		}()
		// 侧栏要展示工作区与今日用量：首帧后就异步拉取。
		go g.bridge.loadWorkspaceRoot()
		go g.bridge.loadUsage()

		var ops op.Ops
		for {
			switch e := w.Event().(type) {
			case app.DestroyEvent:
				os.Exit(0)
			case app.ConfigEvent:
				// 跟踪最大化状态，供自绘标题栏切换“还原/最大化”图标。
				ui.maximized = e.Config.Mode == app.Maximized
			case app.FrameEvent:
				gtx := app.NewContext(&ops, e)
				ui.layout(gtx)
				e.Frame(gtx.Ops)
			}
		}
	}()
	app.Main()
}
