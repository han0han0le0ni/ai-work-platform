// Package host 是宿主内核：把所有零件粘成一台“只转发 JSON-RPC”的机器。
//
// 它只做三件事：加载插件、管理生命周期、格式化兜底。
// 它不认识 AI、LLM、工具、UI。
package host

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aiwork/core/capability"
	"aiwork/core/config"
	"aiwork/core/format"
	"aiwork/core/jsonrpc"
	"aiwork/core/loader"
	"aiwork/core/logs"
	"aiwork/core/process"
	"aiwork/core/registry"
)

// 核心自身的版本信息。
const (
	CoreVersion = "1.0.0"
	APIVersion  = "1"
)

// InitTimeout 是插件响应 core.init 的硬性时限。
const InitTimeout = 5 * time.Second

// officialPrefix 是官方插件的 ID 前缀。
//
// 少数"掀桌子"级别的能力（比如重启核心）只认这个前缀：
// 它们动的是整个系统，不该交给第三方插件。
const officialPrefix = "com.official."

// CoreConfig 是 core.json 的模型。格式化不会删除它。
type CoreConfig struct {
	CoreVersion     string `json:"coreVersion"`
	APIVersion      string `json:"apiVersion"`
	ControlHost     string `json:"controlHost"`
	ControlPort     int    `json:"controlPort"`
	ControlToken    string `json:"controlToken"`
	LogLevel        string `json:"logLevel"`
	InvokeTimeoutMs int    `json:"invokeTimeoutMs"`

	// 能力代理策略（规范 3.5）
	FSRoot         string `json:"fsRoot"`         // 空 = 不限制路径，仅受权限控制
	NetTimeoutMs   int    `json:"netTimeoutMs"`   // core.net.request 单次超时
	SpawnTimeoutMs int    `json:"spawnTimeoutMs"` // core.process.spawn 单次超时
}

// Options 是宿主内核的启动选项。
type Options struct {
	BaseFlags   []string
	BuiltinPath string
}

// Host 是核心内核。
type Host struct {
	dir  string
	opts Options
	cfg  CoreConfig

	logs *logs.Logger
	conf *config.Store
	reg  *registry.Registry
	bus  *registry.Bus
	sup  *process.Supervisor
	caps *capability.Proxy

	// skipped 记录“被跳过的插件所提供的服务” -> 跳过原因。
	// 消费者调用这些服务时返回精确错误码，而不是笼统的 -32000。
	skipped map[string]loader.Skip

	paused      atomic.Bool
	control     interface{ Close() error }
	controlOnce sync.Once
	stopOnce    sync.Once
	stopped     chan struct{}
}

// New 创建宿主内核：准备运行时目录、core.json、日志与内存表。
func New(dir string, opts Options) (*Host, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	dir = abs

	for _, sub := range []string{"plugins", "plugin-configs", "logs", "pending-delete"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("创建运行时目录 %s 失败: %w", sub, err)
		}
	}

	cfg, err := loadCoreConfig(filepath.Join(dir, "core.json"))
	if err != nil {
		return nil, err
	}
	lg, err := logs.New(filepath.Join(dir, "logs"))
	if err != nil {
		return nil, err
	}
	conf, err := config.New(filepath.Join(dir, "plugin-configs"))
	if err != nil {
		return nil, err
	}

	h := &Host{
		dir:     dir,
		opts:    opts,
		cfg:     cfg,
		logs:    lg,
		conf:    conf,
		reg:     registry.New(),
		bus:     registry.NewBus(),
		sup:     process.NewSupervisor(),
		caps:    capability.New(capabilityOptions(cfg, dir), lg.Core),
		stopped: make(chan struct{}),
	}
	lg.Core("info", "核心 v%s 启动（apiVersion=%s，运行时=%s）", CoreVersion, APIVersion, dir)
	lg.Core("info", "能力代理：fsRoot=%q netTimeout=%dms spawnTimeout=%dms（权限在 manifest 声明，未声明即拒绝）",
		cfg.FSRoot, cfg.NetTimeoutMs, cfg.SpawnTimeoutMs)
	return h, nil
}

func capabilityOptions(cfg CoreConfig, runtimeDir string) capability.Options {
	return capability.Options{
		FSRoot:       cfg.FSRoot,
		BaseDir:      runtimeDir,
		PendingDir:   filepath.Join(runtimeDir, "pending-delete"),
		NetTimeout:   time.Duration(cfg.NetTimeoutMs) * time.Millisecond,
		SpawnTimeout: time.Duration(cfg.SpawnTimeoutMs) * time.Millisecond,
	}
}

func loadCoreConfig(path string) (CoreConfig, error) {
	var cfg CoreConfig
	changed := false

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("core.json 解析失败: %w", err)
		}
	case os.IsNotExist(err):
		changed = true
	default:
		return cfg, err
	}

	if cfg.CoreVersion != CoreVersion {
		cfg.CoreVersion = CoreVersion
		changed = true
	}
	if cfg.APIVersion != APIVersion {
		cfg.APIVersion = APIVersion
		changed = true
	}
	if cfg.ControlHost == "" {
		cfg.ControlHost = "127.0.0.1"
		changed = true
	}
	if cfg.ControlPort == 0 {
		cfg.ControlPort = 9123
		changed = true
	}
	if cfg.ControlToken == "" {
		cfg.ControlToken = randomToken()
		changed = true
	}
	if cfg.InvokeTimeoutMs == 0 {
		cfg.InvokeTimeoutMs = 300000
		changed = true
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
		changed = true
	}
	if cfg.NetTimeoutMs == 0 {
		cfg.NetTimeoutMs = 30000
		changed = true
	}
	if cfg.SpawnTimeoutMs == 0 {
		cfg.SpawnTimeoutMs = 30000
		changed = true
	}

	if changed {
		blob, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return cfg, err
		}
		if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// 格式化通道需要的能力（实现 format.Host）
// ---------------------------------------------------------------------------

// RuntimeDir 返回运行时目录。
func (h *Host) RuntimeDir() string { return h.dir }

// Pause 立即停止调度。
func (h *Host) Pause() { h.paused.Store(true) }

// KillAllPlugins 停止所有插件子进程。
func (h *Host) KillAllPlugins(grace time.Duration) { h.sup.StopAll(grace) }

// ResetMemory 清空服务注册表、事件监听表、插件状态表。
func (h *Host) ResetMemory() {
	h.reg = registry.New()
	h.bus = registry.NewBus()
	h.sup = process.NewSupervisor()
	h.caps.Reset()
	h.skipped = nil
}

// Reexec 用自身镜像重启核心。
func (h *Host) Reexec(extra []string) error {
	h.CloseControl()
	args := append([]string{}, h.opts.BaseFlags...)
	args = append(args, extra...)
	return format.ExecSelf(args)
}

// Logf 写核心日志。
func (h *Host) Logf(level, formatStr string, args ...any) {
	h.logs.Core(level, formatStr, args...)
}

// ---------------------------------------------------------------------------
// 查询接口
// ---------------------------------------------------------------------------

// CoreConfig 返回核心配置。
func (h *Host) CoreConfig() CoreConfig { return h.cfg }

// Store 返回插件配置存储。
func (h *Host) Store() *config.Store { return h.conf }

// Logs 返回日志器。
func (h *Host) Logs() *logs.Logger { return h.logs }

// Registry 返回服务注册表快照。
func (h *Host) Registry() map[string]registry.Service { return h.reg.Snapshot() }

// Plugins 返回插件列表。
func (h *Host) Plugins() []*process.Plugin { return h.sup.List() }

// PluginsDir 返回插件目录。
func (h *Host) PluginsDir() string { return filepath.Join(h.dir, "plugins") }

// PluginsExist 判断 plugins/ 下是否有可加载的插件。
func (h *Host) PluginsExist() bool {
	entries, err := os.ReadDir(h.PluginsDir())
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if e.IsDir() && strings.HasSuffix(name, ".plugin") {
			return true
		}
		if !e.IsDir() && strings.HasSuffix(name, ".zip") {
			return true
		}
	}
	return false
}

// BuiltinUIPath 探测内置官方极简 UI 包的位置。
func (h *Host) BuiltinUIPath() string {
	if h.opts.BuiltinPath != "" {
		if st, err := os.Stat(h.opts.BuiltinPath); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(h.opts.BuiltinPath)
			return abs
		}
		return ""
	}
	candidates := []string{
		filepath.Join(h.dir, "builtin", "minimal-ui.zip"),
		filepath.Join(h.dir, "core", "builtin", "minimal-ui.zip"),
		filepath.Join(h.dir, "..", "core", "builtin", "minimal-ui.zip"),
		filepath.Join(h.dir, "..", "..", "core", "builtin", "minimal-ui.zip"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 生命周期
// ---------------------------------------------------------------------------

// Start 扫描并加载 plugins/ 下的所有插件。
func (h *Host) Start() error {
	h.cleanPendingDelete()

	// 规范 2.6：上次运行标记的插件变更，在这一刻生效。
	// 删除放在这里做：此刻没有任何插件在运行、日志也还没打开，
	// Windows 上不会出现“文件被占用，删不掉也移不走”的残骸。
	pending := loader.TakePendingRestart(h.dir)
	h.applyPending(pending)
	for _, c := range pending {
		h.logs.Core("info", "待重启生效的变更已应用：%s %s", c.Action, c.PluginID)
	}

	discs, err := loader.Scan(h.PluginsDir())
	if err != nil {
		return fmt.Errorf("扫描插件目录失败: %w", err)
	}
	plan := loader.BuildPlan(discs, CoreVersion, APIVersion)

	h.skipped = make(map[string]loader.Skip)
	for _, s := range plan.Skipped {
		for _, svc := range s.Provides {
			h.skipped[svc] = s
		}
		h.logs.Core("warn", "跳过插件 %s：%s", dirName(s), s.Reason)
		h.stateEvent(s.ID, process.StateDisabled)
	}
	for _, d := range plan.Order {
		if err := h.startPlugin(d); err != nil {
			h.logs.Core("error", "插件 %s 启动失败：%v", d.Manifest.ID, err)
			h.stateEvent(d.Manifest.ID, process.StateDead)
		}
	}

	services := h.reg.Names()
	h.logs.Core("info", "加载完成：%d 个插件运行中，%d 个被跳过，%d 个服务已注册",
		h.sup.Alive(), len(plan.Skipped), len(services))
	for _, s := range services {
		h.logs.Core("info", "  服务 %s <- %s", s, h.reg.Snapshot()[s].PluginID)
	}
	return nil
}

func dirName(s loader.Skip) string {
	if s.ID != "" {
		return s.ID
	}
	return filepath.Base(s.Dir)
}

func (h *Host) startPlugin(d loader.Discovery) error {
	m := d.Manifest
	bin, args, err := loader.ResolveEntry(d.Dir, m)
	if err != nil {
		return err
	}
	perms := m.Permissions
	if perms == nil {
		perms = []string{}
	}
	// 规范 3.4：权限在 manifest 中声明，核心启动时记录。MVP 阶段只记录，不强制。
	if unknown := m.UnknownPermissions(); len(unknown) > 0 {
		h.logs.Core("warn", "插件 %s 声明了核心未知的权限 %v（核心只记录，不强制）", m.ID, unknown)
	}

	p := process.New(process.Spec{ID: m.ID, Dir: d.Dir, Bin: bin, Args: args}, h.logs, h, h.onPluginState)
	h.sup.Add(p)

	// 规范 3.5：能力代理在插件启动前按其 manifest 声明授权，Activate 期间即可使用
	h.caps.Grant(m.ID, perms)

	if err := p.Start(); err != nil {
		h.caps.Revoke(m.ID)
		return err
	}

	initParams := map[string]any{
		"pluginId":        m.ID,
		"apiVersion":      APIVersion,
		"coreVersion":     CoreVersion,
		"configNamespace": m.ID,
		"permissions":     perms,
	}
	// core.init 约定使用 id 0
	resp, err := p.CallWithID(json.RawMessage("0"), "core.init", initParams, InitTimeout)
	if err != nil {
		h.logs.Core("error", "插件 %s core.init 失败：%v", m.ID, err)
		p.Stop(time.Second)
		return err
	}
	if resp.Error != nil {
		p.Stop(time.Second)
		return fmt.Errorf("core.init 被拒绝：%s", resp.Error.Error())
	}

	// 以 manifest 声明为准补齐服务注册（插件自身调用 core.provide 亦可，二者等价）
	for _, svc := range m.Provides {
		if err := h.reg.Provide(svc, m.ID, nil); err != nil {
			h.logs.Core("warn", "服务声明冲突：%v", err)
		}
	}
	h.logs.Core("info", "插件 %s v%s 已激活（provides=%v permissions=%v）", m.ID, m.Version, m.Provides, perms)
	p.SetState(process.StateActive)
	return nil
}

func (h *Host) onPluginState(p *process.Plugin, st process.State) {
	if st == process.StateDead {
		h.caps.Revoke(p.Spec.ID)
		for _, svc := range h.reg.RemovePlugin(p.Spec.ID) {
			h.logs.Core("warn", "服务 %s 已随插件 %s 失效", svc, p.Spec.ID)
		}
		h.bus.UnsubscribeAll(p.Spec.ID)
	}
	h.stateEvent(p.Spec.ID, st)
}

func (h *Host) stateEvent(pluginID string, st process.State) {
	if pluginID == "" {
		return
	}
	payload, _ := json.Marshal(map[string]any{"pluginId": pluginID, "state": string(st)})
	h.Emit("plugin.state", payload, "core")
}

// Run 启动插件、打开格式化控制通道、监听信号并阻塞。
func (h *Host) Run() error {
	if err := h.Start(); err != nil {
		return err
	}

	if closer, err := format.StartControlServer(h, h.cfg.ControlHost, h.cfg.ControlPort, h.cfg.ControlToken); err != nil {
		h.logs.Core("warn", "格式化控制通道不可用：%v", err)
	} else {
		h.control = closer
		h.logs.Core("info", "格式化控制通道：POST http://%s:%d/format（Header X-Format-Token）",
			h.cfg.ControlHost, h.cfg.ControlPort)
	}

	h.watchSignals()
	<-h.stopped
	return nil
}

// CloseControl 关闭格式化控制通道（重启前必须调用，否则端口会被占用）。
func (h *Host) CloseControl() {
	h.controlOnce.Do(func() {
		if h.control != nil {
			_ = h.control.Close()
		}
	})
}

// Shutdown 优雅退出：发 core.shutdown，宽限 3 秒后强杀。
func (h *Host) Shutdown() {
	h.stopOnce.Do(func() {
		h.paused.Store(true)
		h.logs.Core("info", "核心退出中：通知并停止所有插件")
		h.sup.StopAll(3 * time.Second)
		h.cleanPendingDelete()
		h.logs.Core("info", "核心已退出")
		h.logs.Close()
		close(h.stopped)
	})
}

// applyPending 执行上次运行标记的插件删除。
//
// 运行时删除一个**正在运行**的插件几乎必然失败：Windows 上可执行文件被占用，
// 而核心的日志器又持有 logs/<id>.log，导致既删不掉也移不走。
// 这里刚好相反 —— 插件还没启动、日志文件还没打开，删得干干净净。
// 这就是“删除重启生效”的实际含义，也是唯一能保证不留残骸的时机。
func (h *Host) applyPending(changes []loader.Change) {
	logf := func(level, format string, args ...any) {
		if h.logs != nil {
			h.logs.Core(level, format, args...)
		}
	}
	for _, c := range changes {
		switch c.Action {
		case "delete":
			h.applyPendingDelete(c, logf)
		case "enable", "disable":
			h.applyPendingToggle(c, logf)
		}
	}
}

// applyPendingDelete 删除上次运行标记为 delete 的插件（目录 + 配置 + 日志）。
func (h *Host) applyPendingDelete(c loader.Change, logf func(string, string, ...any)) {
	// 只能执行“声明了 filesystem:delete 的插件”发起的删除，
	// 否则一个只持有 filesystem:write 的插件写条记录就能让核心替它删东西。
	if !h.requesterHas(c.By, capability.PermFSDelete) {
		logf("warn", "忽略待删除项 %s：发起方 %q 不存在或未声明 filesystem:delete",
			c.PluginID, c.By)
		return
	}
	for _, p := range pendingPaths(h.dir, c, logf) {
		if err := os.RemoveAll(p); err != nil {
			logf("warn", "清除待删除项 %s 失败：%v", p, err)
			continue
		}
		logf("info", "已清除待删除项 %s", p)
	}
}

// applyPendingToggle 执行上次运行标记的启用 / 禁用。
//
// 运行中的插件目录被占用，连改名都会被拒，所以当时改不动的会记到这里 ——
// 此刻没有任何插件在运行，改名一定成功。
func (h *Host) applyPendingToggle(c loader.Change, logf func(string, string, ...any)) {
	if !h.requesterHas(c.By, capability.PermFSWrite) {
		logf("warn", "忽略 %s %s：发起方 %q 不存在或未声明 filesystem:write",
			c.Action, c.PluginID, c.By)
		return
	}
	if !safeName(c.Dir) {
		logf("warn", "忽略非法的目录名 %q", c.Dir)
		return
	}
	from := filepath.Join(h.dir, "plugins", c.Dir)
	to := from + loader.DisabledSuffix
	if c.Action == "enable" {
		to = strings.TrimSuffix(from, loader.DisabledSuffix)
	}
	if from == to {
		return
	}
	if _, err := os.Lstat(from); err != nil {
		logf("warn", "%s %s 失败：源目录已不存在", c.Action, c.Dir)
		return
	}
	if _, err := os.Lstat(to); err == nil {
		logf("warn", "%s %s 失败：目标 %s 已存在", c.Action, c.Dir, filepath.Base(to))
		return
	}
	if err := os.Rename(from, to); err != nil {
		logf("warn", "%s %s 失败：%v", c.Action, c.Dir, err)
		return
	}
	logf("info", "已%s插件 %s（%s）", c.Action, c.PluginID, filepath.Base(to))
}

// requesterHas 判断发起方插件是否仍声明着某项权限。
func (h *Host) requesterHas(pluginID, perm string) bool {
	if pluginID == "" {
		return false
	}
	entries, err := os.ReadDir(h.PluginsDir())
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := loader.LoadManifest(filepath.Join(h.PluginsDir(), e.Name()))
		if err != nil || m.ID != pluginID {
			continue
		}
		for _, p := range m.Permissions {
			if p == perm {
				return true
			}
		}
		return false
	}
	return false
}

// pendingPaths 列出一条 delete 记录对应的路径：插件目录 + 配置 + 日志。
// 目录名与插件 ID 都必须是单个名字，否则忽略 —— 防止记录被写成 ../.. 逃出运行时目录。
func pendingPaths(runtimeDir string, c loader.Change, logf func(string, string, ...any)) []string {
	var out []string
	if safeName(c.Dir) {
		out = append(out, filepath.Join(runtimeDir, "plugins", c.Dir))
	} else if c.Dir != "" {
		logf("warn", "忽略非法的待删除目录名 %q", c.Dir)
	}
	if safeName(c.PluginID) {
		out = append(out, filepath.Join(runtimeDir, "plugin-configs", c.PluginID+".json"))
		out = append(out, filepath.Join(runtimeDir, "logs", c.PluginID+".log"))
	} else if c.PluginID != "" {
		logf("warn", "忽略非法的待删除插件 ID %q", c.PluginID)
	}
	return out
}

// safeName 只允许不含路径分隔符、不含 .. 的单个名字。
func safeName(s string) bool {
	if s == "" || strings.Contains(s, "..") {
		return false
	}
	return !strings.ContainsAny(s, `/\`)
}

// cleanPendingDelete 清理上一次因文件锁而延迟删除的插件。
func (h *Host) cleanPendingDelete() {
	dir := filepath.Join(h.dir, "pending-delete")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(p); err != nil {
			h.logs.Core("warn", "清理 pending-delete/%s 失败：%v", e.Name(), err)
		} else {
			h.logs.Core("info", "已清理待删除项 pending-delete/%s", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// 6 个基础接口 + 秘密存储桩
// ---------------------------------------------------------------------------

type provideParams struct {
	Name string          `json:"name"`
	Meta json.RawMessage `json:"meta,omitempty"`
}

type consumeParams struct {
	Name string `json:"name"`
}

type invokeParams struct {
	Service string          `json:"service"`
	Method  string          `json:"method"`
	Args    json.RawMessage `json:"args,omitempty"`
	Caller  string          `json:"caller,omitempty"`
}

type onParams struct {
	Event string `json:"event"`
}

type emitParams struct {
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type configGetParams struct {
	Key string `json:"key"`
}

type configSetParams struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type logParams struct {
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Meta  map[string]any `json:"meta,omitempty"`
}

// HandleRequest 处理插件发来的请求（需要响应）。
func (h *Host) HandleRequest(p *process.Plugin, msg *jsonrpc.Message) {
	switch msg.Method {
	case "core.provide":
		var params provideParams
		if err := decodeParams(msg, &params); err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "%v", err))
			return
		}
		if params.Name == "" {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "缺少 name"))
			return
		}
		if err := h.reg.Provide(params.Name, p.Spec.ID, params.Meta); err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInternalError, "%v", err))
			return
		}
		h.logs.Core("info", "插件 %s 注册服务 %s", p.Spec.ID, params.Name)
		p.Respond(msg.ID, map[string]any{"ok": true, "name": params.Name})

	case "core.consume":
		var params consumeParams
		if err := decodeParams(msg, &params); err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "%v", err))
			return
		}
		svc, ok := h.reg.Lookup(params.Name)
		if !ok {
			p.RespondError(msg.ID, h.serviceNotFound(params.Name))
			return
		}
		target := h.sup.Get(svc.PluginID)
		if target == nil || !target.Alive() {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeProviderDead, "服务提供者已死: %s", params.Name))
			return
		}
		p.Respond(msg.ID, map[string]any{"pluginId": svc.PluginID, "alive": true})

	case "core.invoke":
		var params invokeParams
		if err := decodeParams(msg, &params); err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "%v", err))
			return
		}
		if params.Service == "" {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "缺少 service"))
			return
		}
		result, rpcErr := h.routeInvoke(p.Spec.ID, params)
		if rpcErr != nil {
			p.RespondError(msg.ID, rpcErr)
			return
		}
		p.Respond(msg.ID, result)

	case "core.on":
		var params onParams
		if err := decodeParams(msg, &params); err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "%v", err))
			return
		}
		if params.Event == "" {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "缺少 event"))
			return
		}
		if err := h.bus.Subscribe(params.Event, p.Spec.ID); err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "%v", err))
			return
		}
		p.Respond(msg.ID, map[string]any{"ok": true, "event": params.Event})

	case "core.getConfig":
		var params configGetParams
		if err := decodeParams(msg, &params); err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "%v", err))
			return
		}
		v, found, err := h.conf.Get(p.Spec.ID, params.Key)
		if err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInternalError, "读取配置失败: %v", err))
			return
		}
		if !found {
			p.Respond(msg.ID, map[string]any{"key": params.Key, "value": nil, "found": false})
			return
		}
		p.Respond(msg.ID, map[string]any{"key": params.Key, "value": v, "found": true})

	case "core.setConfig":
		var params configSetParams
		if err := decodeParams(msg, &params); err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "%v", err))
			return
		}
		if params.Key == "" {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "缺少 key"))
			return
		}
		if len(params.Value) == 0 {
			params.Value = json.RawMessage("null")
		}
		if err := h.conf.Set(p.Spec.ID, params.Key, params.Value); err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInternalError, "写入配置失败: %v", err))
			return
		}
		p.Respond(msg.ID, map[string]any{"ok": true})

	case "core.sandbox.run":
		// 接口预留：核心只定义接口，不实现。
		svc, ok := h.reg.Lookup("sandbox.v1")
		if !ok {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeSandboxUnavailable, "sandbox.unavailable"))
			return
		}
		target := h.sup.Get(svc.PluginID)
		if target == nil || !target.Alive() {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeSandboxUnavailable, "sandbox.unavailable"))
			return
		}
		resp, err := target.Call("core.invoke", map[string]any{
			"service":   "sandbox.v1",
			"method":    "run",
			"args":      normalizeRaw(msg.Params),
			"caller":    p.Spec.ID,
			"forwarded": true,
		}, h.invokeTimeout())
		if err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeSandboxUnavailable, "沙箱调用失败: %v", err))
			return
		}
		if resp.Error != nil {
			p.RespondError(msg.ID, resp.Error)
			return
		}
		p.Respond(msg.ID, resp.Result)

	case "core.getSecret", "core.setSecret":
		// 第二阶段能力：核心只定义接口，实际存储由秘密存储插件决定。
		svc, ok := h.reg.Lookup("secrets.v1")
		if !ok {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeServiceNotFound,
				"未安装秘密存储插件（secrets.v1）；MVP 阶段请由插件自身管理密钥"))
			return
		}
		target := h.sup.Get(svc.PluginID)
		if target == nil || !target.Alive() {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeProviderDead, "秘密存储插件已死"))
			return
		}
		method := "get"
		if msg.Method == "core.setSecret" {
			method = "set"
		}
		resp, err := target.Call("core.invoke", map[string]any{
			"service":   "secrets.v1",
			"method":    method,
			"args":      normalizeRaw(msg.Params),
			"caller":    p.Spec.ID,
			"forwarded": true,
		}, h.invokeTimeout())
		if err != nil {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeProviderDead, "秘密存储调用失败: %v", err))
			return
		}
		if resp.Error != nil {
			p.RespondError(msg.ID, resp.Error)
			return
		}
		p.Respond(msg.ID, resp.Result)

	case "core.restart":
		// 重启核心 = 把系统掀了重来：所有插件都会被杀掉，再按目录重新加载。
		//
		// 之所以要有这个方法：规范 2.6 规定插件的安装 / 删除 / 启停都只做标记，
		// 重启后才生效。界面上一堆"重启后生效"的提示，总得有地方能按这一下。
		//
		// 只认官方插件：这能力一按全系统都抖，第三方不该有。
		if !strings.HasPrefix(p.Spec.ID, officialPrefix) {
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodePermissionDenied,
				"只有官方插件可以重启核心（%s）", p.Spec.ID))
			return
		}
		h.logs.Core("warn", "插件 %s 请求重启核心", p.Spec.ID)
		p.Respond(msg.ID, map[string]any{"ok": true})
		// 先回话、再动手：发起者自己也会被杀，得让它先把结果送到界面上。
		// 多留两帧的余量（界面每 110ms 重画一次），让"正在重启"这句话真的显示出来 ——
		// 不然用户看到的就是屏幕毫无征兆地闪一下。
		go func() {
			time.Sleep(250 * time.Millisecond)
			h.Pause()
			// grace 只给 1 秒：收到 core.shutdown 的插件本就该自己退出，
			// 赖着不走的交给 SIGTERM / SIGKILL 兜底，不该拖住整条重启链路。
			h.KillAllPlugins(time.Second)
			if err := h.Reexec(nil); err != nil {
				h.logs.Core("error", "重启核心失败: %v", err)
			}
		}()

	default:
		// 规范 3.5 能力代理：文件 / 网络 / 系统调用由核心代为执行，
		// 权限在入口处强制校验，未声明即拒绝（默认拒绝）。
		if capability.Known(msg.Method) {
			result, rpcErr := h.caps.Dispatch(p.Spec.ID, msg.Method, msg.Params)
			if rpcErr != nil {
				p.RespondError(msg.ID, rpcErr)
				return
			}
			p.Respond(msg.ID, result)
			return
		}
		p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeMethodNotFound, "核心不支持方法 %s", msg.Method))
	}
}

// HandleNotification 处理插件发来的通知（无需响应）。
func (h *Host) HandleNotification(p *process.Plugin, msg *jsonrpc.Message) {
	switch msg.Method {
	case "core.emit":
		var params emitParams
		if err := decodeParams(msg, &params); err != nil || params.Event == "" {
			h.logs.Core("warn", "插件 %s 的 core.emit 参数非法", p.Spec.ID)
			return
		}
		// 事件总线是通道：注册点严格校验，发送点只告警不丢包（核心不解析、不过滤）
		if !registry.ValidEventName(params.Event) {
			h.logs.Core("warn", "插件 %s 发布了不合规的事件名 %q（应形如 <命名空间>.<动作>）",
				p.Spec.ID, params.Event)
		}
		h.Emit(params.Event, params.Payload, p.Spec.ID)

	case "core.log":
		var params logParams
		if err := decodeParams(msg, &params); err != nil {
			h.logs.Core("warn", "插件 %s 的 core.log 参数非法", p.Spec.ID)
			return
		}
		level := params.Level
		if level == "" {
			level = "info"
		}
		// 核心只写入 logs/<pluginId>.log，不分析、不过滤、不告警
		h.logs.Plugin(p.Spec.ID, level, params.Msg, params.Meta)
		// 同时以 core.log 事件广播，日志插件可自行接管处理
		payload, _ := json.Marshal(map[string]any{
			"pluginId": p.Spec.ID,
			"level":    level,
			"msg":      params.Msg,
			"meta":     params.Meta,
		})
		h.Emit("core.log", payload, "core")

	default:
		h.logs.Core("warn", "插件 %s 发送了未知通知 %s", p.Spec.ID, msg.Method)
	}
}

// routeInvoke 把 core.invoke 转发给目标服务提供者，并把响应 id 还原给调用者。
func (h *Host) routeInvoke(callerID string, params invokeParams) (json.RawMessage, *jsonrpc.Error) {
	if h.paused.Load() {
		return nil, jsonrpc.NewError(jsonrpc.CodeInternalError, "核心正在格式化，已停止调度")
	}
	svc, ok := h.reg.Lookup(params.Service)
	if !ok {
		return nil, h.serviceNotFound(params.Service)
	}
	if svc.PluginID == callerID {
		return nil, jsonrpc.NewError(jsonrpc.CodeInternalError, "不允许自调用服务: %s", params.Service)
	}
	target := h.sup.Get(svc.PluginID)
	if target == nil || !target.Alive() {
		return nil, jsonrpc.NewError(jsonrpc.CodeProviderDead, "服务提供者已死: %s（%s）", params.Service, svc.PluginID)
	}
	resp, err := target.Call("core.invoke", map[string]any{
		"service":   params.Service,
		"method":    params.Method,
		"args":      normalizeRaw(params.Args),
		"caller":    callerID,
		"forwarded": true,
	}, h.invokeTimeout())
	if err != nil {
		return nil, jsonrpc.NewError(jsonrpc.CodeProviderDead, "调用 %s 失败: %v", params.Service, err)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// serviceNotFound 在服务缺失时返回最精确的错误。
//
// 如果这个服务属于某个被跳过的插件，就说明真正的原因（依赖缺失 / 版本不兼容），
// 而不是笼统的“服务未找到”。
func (h *Host) serviceNotFound(name string) *jsonrpc.Error {
	if s, ok := h.skipped[name]; ok {
		return jsonrpc.NewError(s.Code, "服务 %s 不可用（插件 %s 未加载）：%s", name, s.ID, s.Reason)
	}
	return jsonrpc.NewError(jsonrpc.CodeServiceNotFound, "服务未找到: %s", name)
}

// Emit 把事件推送给所有订阅者。
func (h *Host) Emit(event string, payload json.RawMessage, origin string) {
	if event == "" || h.paused.Load() {
		return
	}
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	for _, id := range h.bus.Subscribers(event) {
		target := h.sup.Get(id)
		if target == nil || !target.Alive() {
			continue
		}
		_ = target.Notify("core.event", map[string]any{
			"event":   event,
			"payload": payload,
			"origin":  origin,
		})
	}
}

func (h *Host) invokeTimeout() time.Duration {
	if h.cfg.InvokeTimeoutMs <= 0 {
		return 0
	}
	return time.Duration(h.cfg.InvokeTimeoutMs) * time.Millisecond
}

func decodeParams(msg *jsonrpc.Message, v any) error {
	if len(msg.Params) == 0 || string(msg.Params) == "null" {
		return nil
	}
	return json.Unmarshal(msg.Params, v)
}

func normalizeRaw(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("{}")
	}
	return r
}
