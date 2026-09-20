// Package bootstrap 是核心内置的引导菜单（TUI）。
//
// 格式化后自动进入；当 plugins/ 为空时也会自动进入。
// 引导菜单属于核心，不可插件化。
package bootstrap

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aiwork/core/loader"
)

// Env 是引导菜单需要的运行时信息。
type Env struct {
	RuntimeDir    string
	BuiltinUIPath string
	Fresh         bool
	Logf          func(level, format string, args ...any)
}

// Run 进入引导菜单并阻塞，直到用户选择跳过/退出或 stdin 关闭。
func Run(env *Env) {
	if env.Fresh {
		fmt.Println("[系统] 格式化完成，系统已重置。")
	} else {
		fmt.Println("[系统] 当前没有任何插件。")
	}
	in := bufio.NewReader(os.Stdin)

	for {
		printMenu()
		line, err := prompt(in, "> ")
		if err != nil {
			fmt.Println()
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		switch strings.ToLower(line) {
		case "1":
			installBuiltin(env)
		case "2":
			url, err := prompt(in, "[系统] 请输入插件包网址（.zip）：")
			if err != nil {
				return
			}
			installFromURL(env, strings.TrimSpace(url))
		case "3":
			path, err := prompt(in, "[系统] 请输入插件文件夹路径：")
			if err != nil {
				return
			}
			installFromDir(env, strings.TrimSpace(path))
		case "4":
			fmt.Println("[系统] 已跳过。可随时用 `install <url|path>` 安装插件。")
			return
		case "exit", "quit", "q":
			fmt.Println("[系统] 已退出。")
			os.Exit(0)
		case "help", "?":
			printHelp()
		case "list", "ls":
			listPlugins(env)
		default:
			if strings.HasPrefix(line, "install ") {
				arg := strings.TrimSpace(strings.TrimPrefix(line, "install "))
				switch {
				case strings.HasPrefix(arg, "http://"), strings.HasPrefix(arg, "https://"):
					installFromURL(env, arg)
				case arg == "builtin", arg == "official":
					installBuiltin(env)
				default:
					installFromDir(env, arg)
				}
				continue
			}
			fmt.Printf("[系统] 无法识别的输入：%s（输入 help 查看帮助）\n", line)
		}
	}
}

func printMenu() {
	fmt.Println("[系统] 请输入数字选择操作：")
	fmt.Println("  1) 安装官方极简 UI（推荐）")
	fmt.Println("  2) 从网址安装 UI 插件")
	fmt.Println("  3) 从本地文件夹安装 UI 插件")
	fmt.Println("  4) 跳过，稍后手动安装")
}

func printHelp() {
	fmt.Println("[系统] 可用命令：")
	fmt.Println("  help                  显示帮助")
	fmt.Println("  install <url|path>    安装插件（网址或本地文件夹）")
	fmt.Println("  install builtin       安装官方极简 UI")
	fmt.Println("  list                  列出已安装插件")
	fmt.Println("  exit                  退出")
}

func prompt(in *bufio.Reader, prefix string) (string, error) {
	fmt.Print(prefix)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func pluginsDir(env *Env) string { return filepath.Join(env.RuntimeDir, "plugins") }

// installResult 统一汇报安装结果，并标记 pending-restart（规范 2.6）。
//
// 核心不热更新：安装只是把插件放进 plugins/ 并留下标记，重启后才生效。
func installResult(env *Env, id, dir string, err error) {
	if err != nil {
		fmt.Printf("[系统] 安装失败：%v\n", err)
		return
	}
	if markErr := loader.MarkPendingRestart(env.RuntimeDir, loader.Change{
		Action:   "install",
		PluginID: id,
		Dir:      dir,
	}); markErr != nil {
		fmt.Printf("[系统] 已安装 %s，但写入 pending-restart 标记失败：%v\n", id, markErr)
		return
	}
	fmt.Printf("[系统] 已安装插件 %s -> %s\n", id, filepath.Base(dir))
	fmt.Println("[系统] 已标记 pending-restart：插件变更重启生效。")
}

func installBuiltin(env *Env) {
	if env.BuiltinUIPath == "" {
		fmt.Println("[系统] 未找到内置官方极简 UI 包（runtime/builtin/minimal-ui.zip）。")
		fmt.Println("[系统] 请先执行构建脚本：build.ps1 / make build，或改用 2)/3) 手动安装。")
		return
	}
	id, dir, err := installZip(env, env.BuiltinUIPath)
	installResult(env, id, dir, err)
}

func installFromURL(env *Env, url string) {
	if url == "" {
		fmt.Println("[系统] 网址为空，已取消。")
		return
	}
	tmpDir, err := os.MkdirTemp("", "aiwork-dl-")
	if err != nil {
		fmt.Printf("[系统] 安装失败：%v\n", err)
		return
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, "plugin.zip")
	if err := download(url, zipPath); err != nil {
		fmt.Printf("[系统] 下载失败：%v\n", err)
		return
	}
	id, dir, err := installZip(env, zipPath)
	installResult(env, id, dir, err)
}

func installFromDir(env *Env, src string) {
	src = strings.Trim(src, "\"")
	if src == "" {
		fmt.Println("[系统] 路径为空，已取消。")
		return
	}
	m, err := loader.LoadManifest(src)
	if err != nil {
		installResult(env, "", "", err)
		return
	}
	target := filepath.Join(pluginsDir(env), m.DirName())
	if abs, absErr := filepath.Abs(src); absErr == nil && abs == target {
		fmt.Println("[系统] 该目录已在 plugins/ 中，无需重复安装。")
		return
	}
	if err := os.RemoveAll(target); err != nil {
		installResult(env, m.ID, target, err)
		return
	}
	if err := loader.CopyDir(src, target); err != nil {
		installResult(env, m.ID, target, err)
		return
	}
	installResult(env, m.ID, target, nil)
}

// installZip 解压插件包到临时目录，读出 manifest 后落到 plugins/<name>.plugin。
func installZip(env *Env, zipPath string) (string, string, error) {
	tmp, err := os.MkdirTemp("", "aiwork-install-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmp)

	if err := loader.ExtractPluginZip(zipPath, tmp); err != nil {
		return "", "", err
	}
	m, err := loader.LoadManifest(tmp)
	if err != nil {
		return "", "", err
	}
	target := filepath.Join(pluginsDir(env), m.DirName())
	if err := os.RemoveAll(target); err != nil {
		return "", "", err
	}
	if err := loader.CopyDir(tmp, target); err != nil {
		return "", "", err
	}
	return m.ID, target, nil
}

func download(url, dest string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}

func listPlugins(env *Env) {
	entries, err := os.ReadDir(pluginsDir(env))
	if err != nil {
		fmt.Println("[系统] 读取 plugins/ 失败：", err)
		return
	}
	names := make([]string, 0, len(entries))
	meta := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".plugin") {
			continue
		}
		dir := filepath.Join(pluginsDir(env), e.Name())
		if m, err := loader.LoadManifest(dir); err == nil {
			meta[e.Name()] = fmt.Sprintf("%s v%s", m.ID, m.Version)
		} else {
			meta[e.Name()] = "manifest 无效: " + err.Error()
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		fmt.Println("[系统] plugins/ 为空。")
		return
	}
	sort.Strings(names)
	fmt.Println("[系统] 已安装插件：")
	for _, n := range names {
		fmt.Printf("  - %-28s %s\n", n, meta[n])
	}
}
