// Command usage-monitor 是官方「用量监控」插件（provides: usage.v1）。
//
// 它回答三个问题：
//
//  1. DeepSeek 账户里还剩多少钱 —— GET /user/balance
//  2. 这台机器一共烧掉多少 token
//  3. 缓存命中率是多少
//
// 第 2、3 项来自 llm 插件每次模型调用后 emit 的 llm.usage 事件（计量桥见
// plugins/llm/usage_bridge.go）：命中 / 未命中的 token 数只出现在 DeepSeek 的
// 响应体里，解析它的机会只属于真正发请求的那一方，事后无法补算。
//
// 界面：插件自己打开控制终端，在屏幕右上角画一个小浮层 —— **不抢 ui.v1**。
// 代价是工作台每帧全屏重绘会把浮层盖掉，所以浮层按固定节奏重画（默认 1 秒）。
// 想做成"零冲突"的浮层就得改工作台的渲染循环，那是 ui.v1 所有者的活，
// 对一个可插拔的监控插件来说不值得。
package main

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"aiwork/sdk/go/plugin"
)

const serviceName = "usage.v1"

// Usage 实现 plugin.Plugin。
type Usage struct {
	ctx *plugin.Context

	// mu 保护 cfg / totals / balance / keySource / dirty / claimed
	mu        sync.Mutex
	cfg       config
	totals    Totals
	balance   Balance
	keySource string
	dirty     bool

	// claimed 表示界面（工作台）已经认领了浮窗：从此只广播数据，不再自己画。
	claimed bool

	// balanceMu 让余额查询串行：定时器与 usage.v1.balance 可能同时来。
	balanceMu sync.Mutex

	overlay *overlay

	stop     chan struct{}
	stopOnce sync.Once
}

func main() { plugin.Serve(&Usage{}) }

// Activate 注册 usage.v1、订阅用量事件、打开浮层。
func (u *Usage) Activate(ctx *plugin.Context) error {
	u.ctx = ctx
	u.stop = make(chan struct{})

	if err := ctx.Provide(serviceName, map[string]any{
		"description": "用量监控：DeepSeek 余额、token 消耗与缓存命中率",
		"methods":     []string{"snapshot", "balance", "configure", "reset", "report", "ping"},
	}); err != nil {
		return err
	}

	u.load()

	// llm 插件每完成一次模型调用就发这个事件（装了计量桥才有）。
	ctx.On("llm.usage", u.onUsage)

	// 界面（工作台）可以接管浮窗：它才是终端的所有者，鼠标也在它手里。
	//
	// 收到认领之后我就不再自己写终端了 —— 两个进程对着同一块屏幕各画一份，
	// 结果只会是互相盖、谁都闪，而拖动这种事本来也只有收得到鼠标的那一方能做。
	ctx.On("ui.overlay.claim", func(json.RawMessage) {
		u.mu.Lock()
		already := u.claimed
		u.claimed = true
		u.mu.Unlock()
		if !already {
			u.log("info", "浮窗已由界面接管，本插件停止自绘（改为广播用量）")
		}
	})

	u.overlay = openOverlay(u)

	// 先定一次密钥来源，激活日志里才看得出用的是谁的 key。
	_, _ = u.credentials()

	u.startLoops()

	ctx.Log("info", "用量监控已就绪", map[string]any{
		"overlay":     u.overlay != nil,
		"keySource":   u.keySource,
		"permissions": ctx.Permissions,
	})
	return nil
}

// Handle 处理 core.invoke。
func (u *Usage) Handle(service, method string, args json.RawMessage) (any, error) {
	if service != serviceName {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "用量监控不提供服务 %s", service)
	}
	switch method {
	case "snapshot":
		return u.snapshot(), nil
	case "balance":
		return u.refreshBalance()
	case "configure":
		return u.configure(args)
	case "reset":
		return u.reset()
	case "report":
		return u.report(args)
	case "ping":
		return map[string]any{"ok": true, "pluginId": u.ctx.PluginID}, nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "usage.v1 不支持方法 %s", method)
	}
}

// Deactivate 关掉浮层与后台循环。
//
// 这里**不发任何 RPC**：核心收到 core.shutdown 时已经不再处理请求，
// 在这里 SetConfig 会把退出拖到超时。统计由后台每 5 秒落一次盘，
// 最坏情况只丢最后几秒计数。
func (u *Usage) Deactivate() error {
	u.stopOnce.Do(func() {
		if u.stop != nil {
			close(u.stop)
		}
	})
	if u.overlay != nil {
		u.overlay.close()
	}
	if u.ctx != nil {
		u.ctx.Log("info", "用量监控退出", nil)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 配置与持久化
// ---------------------------------------------------------------------------

type config struct {
	APIKey         string `json:"apiKey,omitempty"`
	BaseURL        string `json:"baseUrl,omitempty"`
	RefreshSeconds int    `json:"refreshSeconds,omitempty"`
	OverlayOff     bool   `json:"overlayOff,omitempty"`
	OverlayMs      int    `json:"overlayMs,omitempty"`
}

// load 读回配置与累计统计（都在本插件的配置命名空间里）。
func (u *Usage) load() {
	var cfg config
	if raw, err := u.ctx.GetConfig("config"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}

	var totals Totals
	if raw, err := u.ctx.GetConfig("totals"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &totals)
	}
	if totals.ByModel == nil {
		totals.ByModel = map[string]*ModelTotals{}
	}
	if totals.Since == "" {
		totals.Since = nowClock()
	}

	u.mu.Lock()
	u.cfg = cfg
	u.totals = totals
	u.mu.Unlock()
}

// save 落盘累计统计。没有变化且 !force 时直接返回，省一次 RPC。
func (u *Usage) save(force bool) {
	u.mu.Lock()
	if !u.dirty && !force {
		u.mu.Unlock()
		return
	}
	u.dirty = false
	totals := u.totals
	u.mu.Unlock()

	raw, err := json.Marshal(totals)
	if err != nil {
		return
	}
	_ = u.ctx.SetConfig("totals", json.RawMessage(raw))
}

func (u *Usage) overlayInterval() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	ms := u.cfg.OverlayMs
	if ms < 200 {
		ms = 1000
	}
	return time.Duration(ms) * time.Millisecond
}

func (u *Usage) balanceInterval() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	sec := u.cfg.RefreshSeconds
	if sec <= 0 {
		sec = 60
	}
	return time.Duration(sec) * time.Second
}

// broadcast 把当前用量送出去。
//
// 界面接管之后是广播事件（画归它）；没人接管时才自己往终端上画 ——
// 那时候没有工作台，这扇小窗是唯一能看到用量的地方。
func (u *Usage) broadcast() {
	u.mu.Lock()
	claimed := u.claimed
	off := u.cfg.OverlayOff
	u.mu.Unlock()

	if claimed {
		// 界面在等这份数据画浮窗：发不出去它不是"少一条事件"，而是浮窗永远不出现。
		// 所以这里不吞错误 —— 静默失败最难查。
		if err := u.ctx.Emit("usage.snapshot", u.snapshot()); err != nil {
			u.log("warn", "广播用量失败："+err.Error())
		}
		return
	}
	if off || u.overlay == nil {
		return
	}
	u.overlay.render()
}

// startLoops 起三个后台循环：重画浮层 / 轮询余额 / 统计落盘。
func (u *Usage) startLoops() {
	// ① 浮层：工作台一重绘就把浮层盖掉，所以按固定节奏补画。
	//
	//	界面接管之后改成广播快照 —— 画的事归它（见 broadcast）。
	go func() {
		u.broadcast()
		for {
			select {
			case <-u.stop:
				return
			case <-time.After(u.overlayInterval()):
				u.broadcast()
			}
		}
	}()

	// ② 余额：先查一次，之后按 refreshSeconds 轮询。
	go func() {
		if _, err := u.refreshBalance(); err != nil {
			u.log("warn", "首次查询余额失败: "+err.Error())
		}
		for {
			select {
			case <-u.stop:
				return
			case <-time.After(u.balanceInterval()):
				if _, err := u.refreshBalance(); err != nil {
					u.log("warn", "刷新余额失败: "+err.Error())
				}
			}
		}
	}()

	// ③ 落盘：最多丢 5 秒的计数。
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-u.stop:
				return
			case <-t.C:
				u.save(false)
			}
		}
	}()
}

func (u *Usage) log(level, msg string) {
	if u.ctx != nil {
		u.ctx.Log(level, msg, nil)
	}
}

// ---------------------------------------------------------------------------
// 计量：订阅 llm.usage
// ---------------------------------------------------------------------------

// usageEvent 是 llm 插件上报的一次调用（与 plugins/llm/usage_bridge.go 对应）。
type usageEvent struct {
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	PromptTokens     int    `json:"promptTokens,omitempty"`
	CompletionTokens int    `json:"completionTokens,omitempty"`
	TotalTokens      int    `json:"totalTokens,omitempty"`
	CacheHitTokens   int    `json:"cacheHitTokens,omitempty"`
	CacheMissTokens  int    `json:"cacheMissTokens,omitempty"`
	Ms               int64  `json:"ms,omitempty"`
	OK               bool   `json:"ok"`
	Err              string `json:"err,omitempty"`
	At               string `json:"at,omitempty"`
}

func (u *Usage) onUsage(payload json.RawMessage) {
	var ev usageEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return
	}
	u.applyEvent(&ev)
}

// report 允许别的插件/命令行直接上报一次用量（与事件同一条路径）。
func (u *Usage) report(args json.RawMessage) (any, error) {
	var ev usageEvent
	if len(args) > 0 {
		if err := json.Unmarshal(args, &ev); err != nil {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
		}
	}
	u.applyEvent(&ev)
	return u.snapshot(), nil
}

func (u *Usage) applyEvent(ev *usageEvent) {
	if ev.At == "" {
		ev.At = nowClock()
	}
	if ev.TotalTokens == 0 {
		ev.TotalTokens = ev.PromptTokens + ev.CompletionTokens
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	u.totals.Requests++
	u.totals.PromptTokens += ev.PromptTokens
	u.totals.CompletionTokens += ev.CompletionTokens
	u.totals.TotalTokens += ev.TotalTokens
	u.totals.CacheHitTokens += ev.CacheHitTokens
	u.totals.CacheMissTokens += ev.CacheMissTokens
	if !ev.OK {
		u.totals.Failed++
		if ev.Err != "" {
			u.totals.LastErr = ev.Err
		}
	} else {
		u.totals.LastErr = ""
	}
	u.totals.LastAt = ev.At
	u.totals.LastModel = ev.Model

	key := ev.Model
	if key == "" {
		key = ev.Provider
	}
	if key == "" {
		key = "unknown"
	}
	m := u.totals.ByModel[key]
	if m == nil {
		m = &ModelTotals{}
		u.totals.ByModel[key] = m
	}
	m.Requests++
	m.PromptTokens += ev.PromptTokens
	m.CompletionTokens += ev.CompletionTokens
	m.CacheHitTokens += ev.CacheHitTokens
	m.CacheMissTokens += ev.CacheMissTokens

	u.dirty = true
}

// ---------------------------------------------------------------------------
// 对外快照
// ---------------------------------------------------------------------------

func (u *Usage) snapshot() map[string]any {
	u.mu.Lock()
	totals := u.totals
	balance := u.balance
	cfg := u.cfg
	keySource := u.keySource
	u.mu.Unlock()

	return map[string]any{
		"balance":   balance,
		"totals":    totals,
		"hitRate":   hitRate(totals),
		"keySource": keySource,
		"overlay":   !cfg.OverlayOff,
		"updatedAt": nowClock(),
		"note": "余额来自 DeepSeek GET /user/balance；token 与命中率来自 llm.usage 事件，" +
			"实时累计且重启不清零（reset 可清零）。" +
			"命中率 = 命中 token / (命中 + 未命中)。",
	}
}

// reset 清零累计统计（余额保留）。
func (u *Usage) reset() (any, error) {
	u.mu.Lock()
	u.totals = Totals{ByModel: map[string]*ModelTotals{}, Since: nowClock()}
	u.dirty = true
	u.mu.Unlock()

	u.save(true)
	if u.overlay != nil {
		u.overlay.render()
	}
	return u.snapshot(), nil
}

type configureParams struct {
	APIKey         string `json:"apiKey,omitempty"`
	BaseURL        string `json:"baseUrl,omitempty"`
	RefreshSeconds int    `json:"refreshSeconds,omitempty"`
	Overlay        *bool  `json:"overlay,omitempty"`
	OverlayMs      int    `json:"overlayMs,omitempty"`
}

// configure 改配置。apiKey 传一个减号表示清空（与设置页同一套约定）。
func (u *Usage) configure(args json.RawMessage) (any, error) {
	var in configureParams
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
		}
	}

	u.mu.Lock()
	if s := strings.TrimSpace(in.APIKey); s != "" {
		if s == "-" {
			u.cfg.APIKey = ""
		} else {
			u.cfg.APIKey = s
		}
	}
	if s := strings.TrimSpace(in.BaseURL); s != "" {
		u.cfg.BaseURL = s
	}
	if in.RefreshSeconds > 0 {
		u.cfg.RefreshSeconds = in.RefreshSeconds
	}
	if in.OverlayMs >= 200 {
		u.cfg.OverlayMs = in.OverlayMs
	}
	if in.Overlay != nil {
		u.cfg.OverlayOff = !*in.Overlay
	}
	cfg := u.cfg
	u.mu.Unlock()

	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "序列化配置失败: %v", err)
	}
	if err := u.ctx.SetConfig("config", json.RawMessage(raw)); err != nil {
		return nil, err
	}

	// 改完立刻生效：重查一次余额、重画一次浮层。
	_, _ = u.refreshBalance()
	if u.overlay != nil {
		u.overlay.render()
	}
	return u.snapshot(), nil
}

func (u *Usage) setKeySource(s string) {
	u.mu.Lock()
	u.keySource = s
	u.mu.Unlock()
}
