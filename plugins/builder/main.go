// Command builder 是官方插件构建器，提供 builder.v1。
//
// 它存在的唯一理由：让 AI 写的 **Go** 插件源码能被编译成可执行文件 ——
// 这是目前唯一需要 process:spawn 的场景（Python 插件不用编译，写完即可加载）。
//
// 安全边界是这里最要紧的设计：
//
//	**它不接受任何"命令"参数。** 外部只能说"构建某个插件目录"，
//	真正执行的命令、参数、工作目录全部由本插件写死。
//	拿到 process:spawn 却不转发命令行，就等于拿到了编译器而不是 shell。
//
// 具体限制：
//   - 目标目录必须形如 <名字>.plugin，且落在运行时 plugins/ 里（不许 ../）
//   - 只执行 go build，参数固定为 -o <产物> <单一源文件>
//   - 带超时，失败时如实返回 exitCode / stdout / stderr
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"aiwork/sdk/go/plugin"
)

const serviceName = "builder.v1"

// dirPattern 限制目标目录的形状，挡住 ../ 之类的路径穿越。
var dirPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.plugin$`)

// buildTimeoutMs 给编译留足时间（首次编译要拉标准库，可能较慢）。
const buildTimeoutMs = 180000

// Builder 实现 plugin.Plugin。
type Builder struct {
	ctx *plugin.Context
}

func (b *Builder) Activate(ctx *plugin.Context) error {
	b.ctx = ctx
	if err := ctx.Provide(serviceName, map[string]any{
		"description": "编译插件源码（目前支持 Go）。只做构建，不执行任意命令",
		"methods":     []string{"build", "toolchains"},
	}); err != nil {
		return err
	}
	ctx.Log("info", "构建器已就绪（builder.v1）", map[string]any{"permissions": ctx.Permissions})
	return nil
}

func (b *Builder) Handle(service, method string, args json.RawMessage) (any, error) {
	if service != serviceName {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "构建器不提供服务 %s", service)
	}
	switch method {
	case "build":
		return b.build(args)
	case "toolchains":
		return b.toolchains()
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%s 不支持方法 %s", service, method)
	}
}

func (b *Builder) Deactivate() error { return nil }

// ---------------------------------------------------------------------------
// 构建
// ---------------------------------------------------------------------------

// build 把某个插件目录里的 Go 源码编译成可执行文件。
func (b *Builder) build(args json.RawMessage) (any, error) {
	var in struct {
		Dir  string `json:"dir"`  // 例如 calc.plugin
		Lang string `json:"lang"` // 目前只支持 go
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	in.Dir = strings.TrimSpace(in.Dir)
	if in.Dir == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 dir（例如 calc.plugin）")
	}
	if !dirPattern.MatchString(in.Dir) {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"dir 必须形如 <名字>.plugin，且只能是单个目录名，当前 %q", in.Dir)
	}
	lang := strings.ToLower(strings.TrimSpace(in.Lang))
	if lang == "" {
		lang = "go"
	}
	if lang != "go" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"目前只支持编译 Go 源码（lang=go）；Python 插件不需要编译，重启即可加载")
	}

	modRoot, err := b.moduleRoot()
	if err != nil {
		return nil, err
	}

	pluginDir, err := b.pluginsDir()
	if err != nil {
		return nil, err
	}

	src := filepath.Join(pluginDir, in.Dir, "main.go")
	exeSuffix := ""
	if runtime.GOOS == "windows" {
		exeSuffix = ".exe"
	}
	out := filepath.Join(pluginDir, in.Dir, "main"+exeSuffix)

	// 先确认源码在，免得编译报一堆看不懂的错
	if _, err := b.ctx.FSRead(src); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"读不到源码 %s：%v（先用 pluginManager.v1 create 建插件）", src, err)
	}

	start := time.Now()
	raw, err := b.ctx.Capability("core.process.spawn", map[string]any{
		"command":   "go",
		"args":      []string{"build", "-o", out, src},
		"dir":       modRoot,
		"timeoutMs": buildTimeoutMs,
	})
	if err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "执行 go build 失败：%v（机器上装 Go 了吗）", err)
	}

	var res struct {
		ExitCode int64  `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	_ = json.Unmarshal(raw, &res)

	ok := res.ExitCode == 0
	result := map[string]any{
		"dir":      in.Dir,
		"lang":     lang,
		"output":   out,
		"exitCode": res.ExitCode,
		"stdout":   res.Stdout,
		"stderr":   res.Stderr,
		"ok":       ok,
		"ms":       time.Since(start).Milliseconds(),
	}
	if !ok {
		result["note"] = "编译失败，按 stderr 修源码后重试"
		return result, plugin.Errorf(plugin.CodeInternalError, "编译失败（exit %d）：%s",
			res.ExitCode, firstLine(res.Stderr))
	}
	result["note"] = "编译成功，重启核心后加载"
	return result, nil
}

// toolchains 报告机器上有哪些工具链可用，供 AI 决定用哪种语言写插件。
func (b *Builder) toolchains() (any, error) {
	probes := []struct {
		name string
		args []string
	}{
		{"go", []string{"version"}},
		{"python", []string{"--version"}},
		{"node", []string{"--version"}},
		{"javac", []string{"-version"}},
		{"gcc", []string{"--version"}},
	}
	out := map[string]any{}
	for _, p := range probes {
		info := map[string]any{"available": false}
		raw, err := b.ctx.Capability("core.process.spawn", map[string]any{
			"command":   p.name,
			"args":      p.args,
			"timeoutMs": 10000,
		})
		if err == nil {
			var res struct {
				ExitCode int64  `json:"exitCode"`
				Stdout   string `json:"stdout"`
				Stderr   string `json:"stderr"`
			}
			_ = json.Unmarshal(raw, &res)
			if res.ExitCode == 0 {
				info["available"] = true
				info["version"] = firstLine(res.Stdout + res.Stderr)
			}
		}
		out[p.name] = info
	}
	return map[string]any{"toolchains": out, "note": "AI 写插件优先用 python（免编译）；需要性能或要用 SDK 时用 go（写完要 build）"}, nil
}

// ---------------------------------------------------------------------------
// 路径
// ---------------------------------------------------------------------------

// pluginsDir 返回运行时 plugins/ 的绝对路径。
//
// 和 workspace 插件一样：问核心"."解析到哪里，那就是运行时基准目录。
func (b *Builder) pluginsDir() (string, error) {
	base, err := b.baseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "plugins"), nil
}

// moduleRoot 返回含 go.mod 的目录（通常是运行时目录的上一级）。
//
// Go 必须在模块内编译：插件源码 import 了 aiwork/sdk/go/plugin，
// 离开模块根就解析不到这个包。
func (b *Builder) moduleRoot() (string, error) {
	base, err := b.baseDir()
	if err != nil {
		return "", err
	}
	root := filepath.Dir(base)
	if _, err := b.ctx.FSRead(filepath.Join(root, "go.mod")); err != nil {
		return "", plugin.Errorf(plugin.CodeInternalError,
			"找不到模块根（%s 下没有可读的 go.mod）：Go 插件必须在模块内编译", root)
	}
	return root, nil
}

func (b *Builder) baseDir() (string, error) {
	raw, err := b.ctx.FSList(".")
	if err != nil {
		return "", err
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析目录列表失败: %w", err)
	}
	if out.Path == "" {
		return "", plugin.Errorf(plugin.CodeInternalError, "核心未返回基准目录")
	}
	return out.Path, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func main() {
	if err := plugin.Serve(&Builder{}); err != nil {
		plugin.Logf("error", "构建器退出: %v", err)
		os.Exit(1)
	}
}
