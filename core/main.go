// AI 工作平台核心 —— 一个约 1600 行的加载器。
//
// 核心只做三件事：加载插件、管理生命周期、格式化兜底。
// 它不认识 AI、不认识 LLM、不认识工具、不认识 UI。
// 它只认识两件事：JSON-RPC 2.0 和 plugins/ 里的 manifest.json。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"aiwork/core/bootstrap"
	"aiwork/core/host"
)

func main() {
	var (
		runtimeDir = flag.String("runtime", "", "运行时目录（默认：可执行文件所在目录）")
		firstRun   = flag.Bool("first-run", false, "格式化后首次启动，进入引导菜单")
		builtinPkg = flag.String("builtin", "", "内置官方插件包路径（覆盖自动探测）")
		showVer    = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVer {
		fmt.Printf("aiwork core %s (apiVersion %s)\n", host.CoreVersion, host.APIVersion)
		return
	}

	dir := *runtimeDir
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "无法定位可执行文件:", err)
			os.Exit(1)
		}
		dir = filepath.Dir(exe)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "运行时目录非法:", err)
		os.Exit(1)
	}
	dir = abs

	h, err := host.New(dir, host.Options{
		BaseFlags:   []string{"--runtime", dir},
		BuiltinPath: *builtinPkg,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "核心启动失败:", err)
		os.Exit(1)
	}

	// 格式化后进入引导菜单；plugins/ 为空时也进入。
	if *firstRun || !h.PluginsExist() {
		bootstrap.Run(&bootstrap.Env{
			RuntimeDir:    dir,
			BuiltinUIPath: h.BuiltinUIPath(),
			Fresh:         *firstRun,
			Logf:          h.Logf,
		})
	}

	// 核心自身不渲染 UI，只在 stderr 报一行启动信息，避免进程“静默失踪”。
	// 装了 UI 插件时这一行会被界面重绘覆盖掉。
	fmt.Fprintf(os.Stderr, "[core] v%s 启动中 · 运行时 %s · 日志 %s\n",
		host.CoreVersion, dir, filepath.Join(dir, "logs"))

	if err := h.Run(); err != nil {
		h.Logs().Core("error", "核心运行失败: %v", err)
		fmt.Fprintln(os.Stderr, "核心运行失败:", err)
		os.Exit(1)
	}
	// 显式退出：Host.Shutdown 已停掉全部插件、关完日志并关闭 stopped，
	// 这里直接终结进程，避免个别后台 goroutine / 子进程句柄把退出挂住。
	os.Exit(0)
}

func usage() {
	fmt.Fprintf(os.Stderr, `AI 工作平台核心 v%s（apiVersion %s）

用法: core [选项]

选项:
  --runtime <目录>   运行时目录（默认：可执行文件所在目录）
  --first-run        格式化后首次启动，进入引导菜单
  --builtin <路径>   内置官方插件包路径
  --version          打印版本

运行时目录结构:
  core[.exe]         核心二进制
  core.json          核心配置（格式化不删除）
  plugins/           插件目录（格式化清空）
  plugin-configs/    插件配置（格式化清空）
  logs/              日志（格式化保留）
  pending-delete/    待删除插件（重启后清理）

格式化（应急通道，公开契约）:
  Linux/macOS:  kill -USR2 <pid>
  全平台:       POST http://127.0.0.1:9123/format
                Header: X-Format-Token: <core.json 中的 controlToken>
`, host.CoreVersion, host.APIVersion)
}
