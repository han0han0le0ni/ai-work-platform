// Package capability 是核心的能力代理。
//
// 规范 3.5：文件、网络、系统调用由核心代理（第一阶段做基础版）。
//
// 第一阶段是**协作式**隔离：
//   - 权限在 manifest 中声明，核心在能力入口处强制校验，未声明即拒绝（-32002）
//   - 文件访问可被 core.json 的 fsRoot 限制在某个根目录内（含符号链接解析）
//   - 每次能力调用都写入审计日志
//
// 插件绕过代理直接调用系统 API 属于“不可信代码”问题，由第二阶段的沙箱插件负责。
// 核心保证“插件不会轻易搞死核心”，沙箱插件保证“不可信代码不会搞死插件”。
package capability

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"aiwork/core/jsonrpc"
)

// 权限名。与 manifest 声明、规范 3.4 权限模型一致。
const (
	PermNetwork      = "network"
	PermFSRead       = "filesystem:read"
	PermFSWrite      = "filesystem:write"
	PermFSDelete     = "filesystem:delete"
	PermProcessSpawn = "process:spawn"
	// PermShellOpen 只允许"用系统默认程序打开一个已存在的文件"。
	// 它刻意比 process:spawn 窄得多：命令与参数都由核心写死，插件只能给路径，
	// 所以 UI 这类插件可以拿它开图片，而不必拿到"能执行任意命令"的生杀大权。
	PermShellOpen = "shell:open"
)

// 能力方法与所需权限。这是核心暴露给插件的**能力面**，属于公开契约，只增不减。
var requirements = map[string]string{
	"core.fs.read":       PermFSRead,
	"core.fs.list":       PermFSRead,
	"core.fs.write":      PermFSWrite,
	"core.fs.delete":     PermFSDelete,
	"core.fs.move":       PermFSWrite,
	"core.fs.mkdir":      PermFSWrite,
	"core.net.request":   PermNetwork,
	"core.process.spawn": PermProcessSpawn,
	"core.shell.open":    PermShellOpen,
}

const (
	// maxOutputBytes 是单次能力调用的输出硬上限（受硬通道单行长度约束）。
	maxOutputBytes = 4 << 20
	// defaultReadLimit 是 core.fs.read 未指定 limit 时的默认返回上限。
	defaultReadLimit = 1 << 20
	// maxBinaryReadLimit 是以 base64 读取时的上限。
	//
	// 图片/音视频必须按 base64 读（按 UTF-8 读会把无效字节换成 U+FFFD，文件就毁了），
	// 而 base64 会膨胀约 1/3，所以原始字节的上限要留出余量，保证编码后仍在
	// maxOutputBytes（硬通道单行上限）之内。
	maxBinaryReadLimit = 3 << 20
)

// Options 是能力代理的策略，来自 core.json。
type Options struct {
	FSRoot       string        // 空 = 不限制路径（仅受权限控制）
	BaseDir      string        // 相对路径的基准目录；空 = 拒绝相对路径
	PendingDir   string        // 删除失败（文件被锁）时的中转目录；空 = 不兜底，直接报错
	NetTimeout   time.Duration // 单次网络请求超时
	SpawnTimeout time.Duration // 单次子进程超时
}

// Proxy 是能力代理。
type Proxy struct {
	opts  Options
	logf  func(level, format string, args ...any)
	mu    sync.RWMutex
	perms map[string]map[string]bool
}

// New 创建能力代理。logf 用于审计日志，可为 nil。
func New(opts Options, logf func(level, format string, args ...any)) *Proxy {
	if opts.NetTimeout <= 0 {
		opts.NetTimeout = 30 * time.Second
	}
	if opts.SpawnTimeout <= 0 {
		opts.SpawnTimeout = 30 * time.Second
	}
	return &Proxy{opts: opts, logf: logf, perms: make(map[string]map[string]bool)}
}

// Known 判断某方法是否属于能力代理。
func Known(method string) bool {
	_, ok := requirements[method]
	return ok
}

// Grant 按 manifest 声明授予权限。
func (p *Proxy) Grant(pluginID string, permissions []string) {
	set := make(map[string]bool, len(permissions))
	for _, perm := range permissions {
		set[perm] = true
	}
	p.mu.Lock()
	p.perms[pluginID] = set
	p.mu.Unlock()
}

// Revoke 插件卸载时收回全部权限。
func (p *Proxy) Revoke(pluginID string) {
	p.mu.Lock()
	delete(p.perms, pluginID)
	p.mu.Unlock()
}

// Reset 清空所有授权（格式化时调用）。
func (p *Proxy) Reset() {
	p.mu.Lock()
	p.perms = make(map[string]map[string]bool)
	p.mu.Unlock()
}

// Permissions 返回某插件当前被授予的权限（有序，便于日志）。
func (p *Proxy) Permissions(pluginID string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.perms[pluginID]))
	for perm := range p.perms[pluginID] {
		out = append(out, perm)
	}
	sort.Strings(out)
	return out
}

// Has 判断权限是否已授予。
func (p *Proxy) Has(pluginID, perm string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.perms[pluginID][perm]
}

// Require 校验权限，未授予则返回 -32002。
func (p *Proxy) Require(pluginID, perm string) *jsonrpc.Error {
	if p.Has(pluginID, perm) {
		return nil
	}
	return jsonrpc.NewError(jsonrpc.CodePermissionDenied,
		"权限不足：插件 %s 未在 manifest 中声明 %s", pluginID, perm)
}

// Dispatch 执行一次能力调用。默认拒绝：未知方法不代理。
func (p *Proxy) Dispatch(pluginID, method string, params json.RawMessage) (any, *jsonrpc.Error) {
	perm, ok := requirements[method]
	if !ok {
		return nil, jsonrpc.NewError(jsonrpc.CodeMethodNotFound, "核心不支持方法 %s", method)
	}
	if err := p.Require(pluginID, perm); err != nil {
		return nil, err
	}
	switch method {
	case "core.fs.read":
		return p.fsRead(pluginID, params)
	case "core.fs.list":
		return p.fsList(pluginID, params)
	case "core.fs.write":
		return p.fsWrite(pluginID, params)
	case "core.fs.delete":
		return p.fsDelete(pluginID, params)
	case "core.fs.move":
		return p.fsMove(pluginID, params)
	case "core.fs.mkdir":
		return p.fsMkdir(pluginID, params)
	case "core.net.request":
		return p.netRequest(pluginID, params)
	case "core.process.spawn":
		return p.spawn(pluginID, params)
	case "core.shell.open":
		return p.shellOpen(pluginID, params)
	}
	return nil, jsonrpc.NewError(jsonrpc.CodeMethodNotFound, "核心不支持方法 %s", method)
}

// ---------------------------------------------------------------------------
// 文件
// ---------------------------------------------------------------------------

func (p *Proxy) fsRead(pluginID string, params json.RawMessage) (any, *jsonrpc.Error) {
	var in struct {
		Path     string `json:"path"`
		Limit    int64  `json:"limit,omitempty"`
		Encoding string `json:"encoding,omitempty"` // "base64"：二进制安全，读图片/音视频用
	}
	if err := unmarshal(params, &in); err != nil {
		return nil, badParams(err)
	}
	if in.Path == "" {
		return nil, badParams(errors.New("缺少 path"))
	}
	asBase64 := strings.EqualFold(strings.TrimSpace(in.Encoding), "base64")
	abs, rpcErr := p.resolve(in.Path)
	if rpcErr != nil {
		return nil, rpcErr
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, failed("读取 %s 失败: %v", abs, err)
	}
	if info.IsDir() {
		return nil, badParams(fmt.Errorf("%s 是目录，请改用 core.fs.list", abs))
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, failed("读取 %s 失败: %v", abs, err)
	}
	ceiling := int64(defaultReadLimit)
	if asBase64 {
		ceiling = maxBinaryReadLimit
	}
	limit := in.Limit
	if limit <= 0 || limit > ceiling {
		limit = ceiling
	}
	truncated := false
	if int64(len(data)) > limit {
		data = data[:limit]
		truncated = true
	}
	p.audit(pluginID, "core.fs.read", abs)

	// 二进制必须走 base64：直接 string(data) 会把无效 UTF-8 字节换成 U+FFFD，
	// 图片会变成一个打不开的乱码文件。
	if asBase64 {
		return map[string]any{
			"path":      abs,
			"size":      info.Size(),
			"encoding":  "base64",
			"content":   base64.StdEncoding.EncodeToString(data),
			"truncated": truncated,
		}, nil
	}

	return map[string]any{
		"path":      abs,
		"size":      info.Size(),
		"content":   string(data),
		"truncated": truncated,
	}, nil
}

func (p *Proxy) fsWrite(pluginID string, params json.RawMessage) (any, *jsonrpc.Error) {
	var in struct {
		Path     string `json:"path"`
		Content  string `json:"content"`
		Append   bool   `json:"append,omitempty"`
		Mkdir    bool   `json:"mkdir,omitempty"`
		Encoding string `json:"encoding,omitempty"` // "base64"：写二进制（图片等）
	}
	if err := unmarshal(params, &in); err != nil {
		return nil, badParams(err)
	}
	if in.Path == "" {
		return nil, badParams(errors.New("缺少 path"))
	}
	asBase64 := strings.EqualFold(strings.TrimSpace(in.Encoding), "base64")
	abs, rpcErr := p.resolve(in.Path)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if in.Mkdir {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, failed("创建目录失败: %v", err)
		}
	}

	// 写二进制的对称面：读有 base64（fsRead），写也得有，
	// 否则生成的图片只能以字符串形式写下去 —— 无效 UTF-8 字节会被替换，图就废了。
	data := []byte(in.Content)
	if asBase64 {
		decoded, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil {
			return nil, badParams(fmt.Errorf("content 不是合法的 base64: %v", err))
		}
		data = decoded
	}

	flags := os.O_CREATE | os.O_WRONLY
	if in.Append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(abs, flags, 0o644)
	if err != nil {
		return nil, failed("写入 %s 失败: %v", abs, err)
	}
	defer f.Close()

	n, err := f.Write(data)
	if err != nil {
		return nil, failed("写入 %s 失败: %v", abs, err)
	}
	p.audit(pluginID, "core.fs.write", fmt.Sprintf("%s (%d 字节)", abs, n))

	return map[string]any{"path": abs, "size": n, "append": in.Append}, nil
}

// fsMkdir 创建目录（需 filesystem:write）。
//
// 已存在时不报错，只把 created 置为 false —— 建目录通常是"确保它在"，
// 而不是"它必须原来没有"。
func (p *Proxy) fsMkdir(pluginID string, params json.RawMessage) (any, *jsonrpc.Error) {
	var in struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive,omitempty"`
	}
	if err := unmarshal(params, &in); err != nil {
		return nil, badParams(err)
	}
	if in.Path == "" {
		return nil, badParams(errors.New("缺少 path"))
	}
	abs, rpcErr := p.resolve(in.Path)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if info, err := os.Stat(abs); err == nil {
		if !info.IsDir() {
			return nil, badParams(fmt.Errorf("%s 已存在且不是目录", abs))
		}
		return map[string]any{"path": abs, "created": false, "existed": true}, nil
	}

	var err error
	if in.Recursive {
		err = os.MkdirAll(abs, 0o755)
	} else {
		err = os.Mkdir(abs, 0o755)
	}
	if err != nil {
		return nil, failed("创建目录 %s 失败: %v", abs, err)
	}
	p.audit(pluginID, "core.fs.mkdir", abs)
	return map[string]any{"path": abs, "created": true, "existed": false}, nil
}

// fsDelete 删除文件或目录（需 filesystem:delete）。
//
// 返回 {path, existed, deleted, moved, pending?, error?}。
//
// 顺序很关键：**先整体搬走，再删副本**。
// Windows 上正在运行的插件，其可执行文件被占用，os.RemoveAll 会失败 ——
// 而且它失败时已经把能删的都删了，只留下删了一半的目录（manifest 没了、入口还在）。
// 所以目录一律先改名移进 pending-delete/：这一步是原子的，plugins/ 立刻干净，
// 之后再删副本；副本删不掉就留在待删除区，由核心在退出与下次启动时清理。
// 连改名都失败时（目录里有被占用的文件），**原样保留**并返回错误 ——
// 留下一个完整的目录，等下次启动时再删，也远好过留下一个残废的目录。
func (p *Proxy) fsDelete(pluginID string, params json.RawMessage) (any, *jsonrpc.Error) {
	var in struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive,omitempty"`
	}
	if err := unmarshal(params, &in); err != nil {
		return nil, badParams(err)
	}
	if in.Path == "" {
		return nil, badParams(errors.New("缺少 path"))
	}
	abs, rpcErr := p.resolve(in.Path)
	if rpcErr != nil {
		return nil, rpcErr
	}
	// 护栏：不允许一个插件把整个运行时目录、或它的一级子目录（plugins/ logs/ ...）删空。
	// 要删请删到具体条目，例如 plugins/echo.plugin。
	if rpcErr := p.guardRuntimeDir(abs); rpcErr != nil {
		return nil, rpcErr
	}

	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"path": abs, "existed": false, "deleted": false, "moved": false}, nil
		}
		return nil, failed("访问 %s 失败: %v", abs, err)
	}
	if info.IsDir() && !in.Recursive {
		return nil, badParams(fmt.Errorf("%s 是目录，删除目录需要 recursive=true", abs))
	}

	if info.IsDir() {
		return p.removeDir(pluginID, abs)
	}

	// 文件：先直接删，删不掉再搬
	if err := os.Remove(abs); err == nil {
		p.audit(pluginID, "core.fs.delete", abs)
		return map[string]any{"path": abs, "existed": true, "deleted": true, "moved": false}, nil
	} else if p.opts.PendingDir == "" {
		return nil, failed("删除 %s 失败: %v", abs, err)
	}
	dst, moveErr := p.moveToPending(abs)
	if moveErr != nil {
		return nil, failed("删除 %s 失败（被占用），且无法移入待删除区：%v", abs, moveErr)
	}
	p.audit(pluginID, "core.fs.delete", fmt.Sprintf("%s -> %s（文件被占用，延后清理）", abs, dst))
	return map[string]any{
		"path":    abs,
		"existed": true,
		"deleted": false,
		"moved":   true,
		"pending": dst,
	}, nil
}

// removeDir 删除目录：先整体搬进待删除区，再删副本。
func (p *Proxy) removeDir(pluginID, abs string) (any, *jsonrpc.Error) {
	if p.opts.PendingDir != "" {
		dst, err := p.moveToPending(abs)
		if err != nil {
			// 搬不动（目录里有文件被占用）：保持原样，把失败如实报上去。
			// 绝不退化为 RemoveAll —— 那会留下删了一半的目录。
			return nil, failed("目标被占用，已原样保留 %s：%v（下次启动时会在加载插件前重试）", abs, err)
		}
		if rmErr := os.RemoveAll(dst); rmErr != nil {
			// 已经从 plugins/ 消失了，副本留在待删除区，核心退出或启动时清理
			p.audit(pluginID, "core.fs.delete", fmt.Sprintf("%s -> %s（副本仍被占用，延后清理）", abs, dst))
			return map[string]any{
				"path": abs, "existed": true, "deleted": false, "moved": true,
				"pending": dst, "error": rmErr.Error(),
			}, nil
		}
		p.audit(pluginID, "core.fs.delete", fmt.Sprintf("%s（经待删除区）", abs))
		return map[string]any{"path": abs, "existed": true, "deleted": true, "moved": true}, nil
	}
	if err := os.RemoveAll(abs); err != nil {
		return nil, failed("删除 %s 失败: %v", abs, err)
	}
	p.audit(pluginID, "core.fs.delete", abs)
	return map[string]any{"path": abs, "existed": true, "deleted": true, "moved": false}, nil
}

// fsMove 重命名 / 移动文件或目录（需 filesystem:write）。
//
// 插件的启用与禁用就是靠它：核心只扫描 *.plugin 结尾的目录，
// 把 echo.plugin 改名为 echo.plugin.disabled 就等于禁用，改回来就是启用。
// 目标已存在时拒绝 —— 覆盖式改名会把两个插件搅在一起。
func (p *Proxy) fsMove(pluginID string, params json.RawMessage) (any, *jsonrpc.Error) {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := unmarshal(params, &in); err != nil {
		return nil, badParams(err)
	}
	if in.From == "" || in.To == "" {
		return nil, badParams(errors.New("缺少 from / to"))
	}
	from, rpcErr := p.resolve(in.From)
	if rpcErr != nil {
		return nil, rpcErr
	}
	to, rpcErr := p.resolve(in.To)
	if rpcErr != nil {
		return nil, rpcErr
	}
	for _, path := range []string{from, to} {
		if rpcErr := p.guardRuntimeDir(path); rpcErr != nil {
			return nil, rpcErr
		}
	}
	if _, err := os.Lstat(from); err != nil {
		if os.IsNotExist(err) {
			return nil, badParams(fmt.Errorf("源路径不存在：%s", from))
		}
		return nil, failed("访问 %s 失败: %v", from, err)
	}
	if _, err := os.Lstat(to); err == nil {
		return nil, badParams(fmt.Errorf("目标已存在：%s", to))
	} else if !os.IsNotExist(err) {
		return nil, failed("访问 %s 失败: %v", to, err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return nil, failed("创建目标目录失败: %v", err)
	}
	if err := os.Rename(from, to); err != nil {
		return nil, failed("移动 %s -> %s 失败: %v", from, to, err)
	}
	p.audit(pluginID, "core.fs.move", fmt.Sprintf("%s -> %s", from, to))
	return map[string]any{"from": from, "to": to, "moved": true}, nil
}

// guardRuntimeDir 拒绝删除运行时目录本身及其一级子目录。
func (p *Proxy) guardRuntimeDir(abs string) *jsonrpc.Error {
	base := p.opts.BaseDir
	if base == "" {
		return nil
	}
	root, err := filepath.Abs(base)
	if err != nil {
		return nil
	}
	if samePath(abs, root) {
		return badParams(fmt.Errorf("拒绝删除运行时目录本身：%s", abs))
	}
	if parent := filepath.Dir(abs); samePath(parent, root) {
		return badParams(fmt.Errorf("拒绝删除运行时的一级目录：%s（请删除到具体条目）", abs))
	}
	return nil
}

// moveToPending 把删不掉的目标改名移进 pending-delete/，供核心稍后清理。
//
// 落点必须真的没被占：Windows 的纳秒时钟精度只有 ~100ns，连续两次搬运
// 同名目录（删了又装、装了又删）可能算出同一个名字，后一次就直接失败。
func (p *Proxy) moveToPending(abs string) (string, error) {
	if err := os.MkdirAll(p.opts.PendingDir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(abs)
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("%s-%d", base, time.Now().UnixNano())
		if i > 0 {
			name = fmt.Sprintf("%s-%d-%d", base, time.Now().UnixNano(), i)
		}
		dst := filepath.Join(p.opts.PendingDir, name)
		if _, err := os.Lstat(dst); err == nil {
			continue // 撞名了，换一个
		}
		if err := os.Rename(abs, dst); err != nil {
			return "", err
		}
		return dst, nil
	}
	return "", fmt.Errorf("待删除区里 %s 的同名副本过多，请先清理 pending-delete/", base)
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func (p *Proxy) fsList(pluginID string, params json.RawMessage) (any, *jsonrpc.Error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := unmarshal(params, &in); err != nil {
		return nil, badParams(err)
	}
	if in.Path == "" {
		in.Path = "."
	}
	abs, rpcErr := p.resolve(in.Path)
	if rpcErr != nil {
		return nil, rpcErr
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, failed("列目录 %s 失败: %v", abs, err)
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		item := map[string]any{"name": e.Name(), "dir": e.IsDir()}
		if info, ierr := e.Info(); ierr == nil {
			item["size"] = info.Size()
		}
		out = append(out, item)
	}
	p.audit(pluginID, "core.fs.list", fmt.Sprintf("%s (%d 项)", abs, len(out)))
	return map[string]any{"path": abs, "entries": out}, nil
}

// resolve 解析路径并做围栏校验。
//
// 相对路径不会按核心进程的当前目录解析（那取决于运维怎么启动核心，行为不确定），
// 而是固定解析到 fsRoot（若配置）或 BaseDir（运行时目录）。
//
// 返回的错误已带正确分类：参数问题 → -32602，访问控制问题 → -32002。
func (p *Proxy) resolve(path string) (string, *jsonrpc.Error) {
	if !filepath.IsAbs(path) {
		base := p.opts.FSRoot
		if base == "" {
			base = p.opts.BaseDir
		}
		if base == "" {
			return "", badParams(fmt.Errorf("相对路径 %q 无法解析：核心未配置基准目录，请使用绝对路径", path))
		}
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", badParams(fmt.Errorf("路径非法: %v", err))
	}
	if p.opts.FSRoot == "" {
		return abs, nil
	}

	root, err := filepath.Abs(p.opts.FSRoot)
	if err != nil {
		return "", failed("fsRoot 配置非法: %v", err)
	}
	realRoot := root
	if r, rerr := filepath.EvalSymlinks(root); rerr == nil {
		realRoot = r
	}
	realTarget := abs
	if r, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		realTarget = r
	} else if r, rerr := filepath.EvalSymlinks(filepath.Dir(abs)); rerr == nil {
		// 目标还不存在（写入场景）：改为校验其父目录
		realTarget = filepath.Join(r, filepath.Base(abs))
	}
	rel, err := filepath.Rel(realRoot, realTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", denied(fmt.Errorf("路径越界：%s 不在允许的根目录 %s 内", realTarget, realRoot))
	}
	return abs, nil
}

// ---------------------------------------------------------------------------
// 网络
// ---------------------------------------------------------------------------

var allowedMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodHead: true,
}

func (p *Proxy) netRequest(pluginID string, params json.RawMessage) (any, *jsonrpc.Error) {
	var in struct {
		Method    string            `json:"method"`
		URL       string            `json:"url"`
		Headers   map[string]string `json:"headers,omitempty"`
		Body      string            `json:"body,omitempty"`
		TimeoutMs int               `json:"timeoutMs,omitempty"`
		// Encoding="base64" 时响应体按 base64 返回。
		// 下载图片/音视频这类二进制响应必须用它，否则 string(data) 会把
		// 无效 UTF-8 字节换成 U+FFFD，存下来就是坏文件。
		Encoding string `json:"encoding,omitempty"`
	}
	if err := unmarshal(params, &in); err != nil {
		return nil, badParams(err)
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = http.MethodGet
	}
	if !allowedMethods[method] {
		return nil, badParams(fmt.Errorf("不支持的方法 %q", in.Method))
	}
	if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		return nil, badParams(fmt.Errorf("url 必须是 http/https：%q", in.URL))
	}

	var body io.Reader
	if in.Body != "" {
		body = strings.NewReader(in.Body)
	}
	req, err := http.NewRequest(method, in.URL, body)
	if err != nil {
		return nil, badParams(err)
	}
	for k, v := range in.Headers {
		req.Header.Set(k, v)
	}

	timeout := p.opts.NetTimeout
	if in.TimeoutMs > 0 {
		timeout = time.Duration(in.TimeoutMs) * time.Millisecond
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, failed("请求 %s 失败: %v", in.URL, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxOutputBytes+1))
	if err != nil {
		return nil, failed("读取响应失败: %v", err)
	}
	truncated := len(data) > maxOutputBytes
	if truncated {
		data = data[:maxOutputBytes]
	}
	headers := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}
	p.audit(pluginID, "core.net.request", fmt.Sprintf("%s %s -> %d", method, in.URL, resp.StatusCode))

	if strings.EqualFold(strings.TrimSpace(in.Encoding), "base64") {
		return map[string]any{
			"status":    resp.StatusCode,
			"headers":   headers,
			"encoding":  "base64",
			"body":      base64.StdEncoding.EncodeToString(data),
			"truncated": truncated,
		}, nil
	}
	return map[string]any{
		"status":    resp.StatusCode,
		"headers":   headers,
		"body":      string(data),
		"truncated": truncated,
	}, nil
}

// ---------------------------------------------------------------------------
// 子进程
// ---------------------------------------------------------------------------

func (p *Proxy) spawn(pluginID string, params json.RawMessage) (any, *jsonrpc.Error) {
	var in struct {
		Command   string   `json:"command"`
		Args      []string `json:"args,omitempty"`
		Dir       string   `json:"dir,omitempty"`
		TimeoutMs int      `json:"timeoutMs,omitempty"`
	}
	if err := unmarshal(params, &in); err != nil {
		return nil, badParams(err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return nil, badParams(errors.New("缺少 command"))
	}
	if in.Dir != "" {
		if _, rpcErr := p.resolve(in.Dir); rpcErr != nil {
			return nil, rpcErr
		}
	}

	timeout := p.opts.SpawnTimeout
	if in.TimeoutMs > 0 {
		timeout = time.Duration(in.TimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, in.Command, in.Args...)
	cmd.Dir = in.Dir
	stdout := &capWriter{max: maxOutputBytes}
	stderr := &capWriter{max: maxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, failed("命令超时（%s）: %s", timeout, in.Command)
	}

	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			return nil, failed("执行 %s 失败: %v", in.Command, runErr)
		}
		exitCode = ee.ExitCode()
	}
	p.audit(pluginID, "core.process.spawn", fmt.Sprintf("%s -> exit %d", in.Command, exitCode))

	return map[string]any{
		"exitCode":  exitCode,
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"truncated": stdout.truncated || stderr.truncated,
	}, nil
}

// shellOpen 用系统默认程序打开一个文件（需 shell:open）。
//
// 与 process.spawn 的区别就是它**窄**：命令、参数全部由核心写死，
// 插件只能提供一个路径。所以界面这类插件想要"点一下打开图片"时，
// 不必为此拿到"能执行任意命令"的权限。
func (p *Proxy) shellOpen(pluginID string, params json.RawMessage) (any, *jsonrpc.Error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := unmarshal(params, &in); err != nil {
		return nil, badParams(err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return nil, badParams(errors.New("缺少 path"))
	}
	abs, rpcErr := p.resolve(in.Path)
	if rpcErr != nil {
		return nil, rpcErr
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, failed("打开 %s 失败: %v", abs, err)
	}
	if info.IsDir() {
		return nil, badParams(fmt.Errorf("%s 是目录；core.shell.open 只用于打开文件", abs))
	}

	cmd, err := openCommand(abs)
	if err != nil {
		return nil, failed("当前系统无法打开文件: %v", err)
	}
	// 不等它结束：看图/看视频的程序会一直开着，等下去等于把核心卡死。
	if err := cmd.Start(); err != nil {
		return nil, failed("启动打开程序失败: %v", err)
	}
	// 但必须收尸，否则进程表里会堆一堆僵尸
	go func() { _ = cmd.Wait() }()

	p.audit(pluginID, "core.shell.open", abs)
	return map[string]any{"path": abs, "opened": true}, nil
}

// openCommand 给出"用默认程序打开这个文件"的命令。
//
// 路径一律作为**独立 argv 元素**传进去，绝不拼成 shell 字符串 ——
// 文件名里带 & | ; " 时才有机会活下来，也免得它变成第二条命令。
func openCommand(path string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "windows":
		// 走 rundll32 的 FileProtocolHandler，而不是 cmd /c start：
		// 后者要多过一层 cmd.exe，引号与特殊字符的转义面大得多。
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path), nil
	case "darwin":
		return exec.Command("open", path), nil
	default:
		return exec.Command("xdg-open", path), nil
	}
}

// capWriter 是带容量上限的写入器，超出部分丢弃并标记。
type capWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	room := w.max - w.buf.Len()
	switch {
	case room <= 0:
		w.truncated = true
	case len(p) > room:
		w.buf.Write(p[:room])
		w.truncated = true
	default:
		w.buf.Write(p)
	}
	return len(p), nil
}

func (w *capWriter) String() string { return w.buf.String() }

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

func (p *Proxy) audit(pluginID, method, target string) {
	if p.logf != nil {
		p.logf("info", "能力代理：%s 调用 %s %s", pluginID, method, target)
	}
}

func unmarshal(params json.RawMessage, v any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	return json.Unmarshal(params, v)
}

func badParams(err error) *jsonrpc.Error {
	return jsonrpc.NewError(jsonrpc.CodeInvalidParams, "%v", err)
}

func denied(err error) *jsonrpc.Error {
	return jsonrpc.NewError(jsonrpc.CodePermissionDenied, "%v", err)
}

func failed(format string, args ...any) *jsonrpc.Error {
	return jsonrpc.NewError(jsonrpc.CodeInternalError, format, args...)
}
