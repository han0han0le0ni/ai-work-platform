// 启动器 —— 双击就能开。
//
// 核心二进制在 runtime/core.exe，藏在子目录里，桌面上、资源管理器里都不好找。
// 所以外面再包一层很薄的东西，只做四件事：
//
//  1. 摆在项目根目录，文件名就是产品名，带图标（构建脚本给这个 exe 打了资源）
//  2. 把控制台窗口标题改成产品名。核心只往 stderr 打一行启动信息、不管窗口，
//     而窗口是启动器先开的，所以标题由启动器来改。
//  3. 已经跑着一个实例时直接说清楚，不再开第二个去抢终端 / 抢设备。
//  4. 转发退出码：核心怎么退，启动器就怎么退。
//
// 它不认识插件、不认识 JSON-RPC、不认识 UI。核心挪走了它就没用了。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// appName 是窗口标题和提示语里的产品名。可执行文件本身的文件名由
// launcher/name.txt 提供（构建脚本读它），两者保持同一个名字。
const appName = "AI 工作平台"

const defaultControlPort = 9123

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		waitForEnter()
		os.Exit(1)
	}
}

func run() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	root := filepath.Dir(exe)

	runtimeDir, coreBin, err := locateRuntime(root)
	if err != nil {
		return err
	}

	// 控制台窗口是启动器开的，标题归启动器管。
	setConsoleTitle(appName)

	if addr, running := alreadyRunning(runtimeDir); running {
		fmt.Printf("%s 已经在运行了（控制台 %s）。\n", appName, addr)
		fmt.Println("先找找已经开着的那个窗口；确实没有的话，说明上次没退干净，")
		fmt.Println("把残留的 core 进程结束掉再启动。")
		waitForEnter()
		return nil
	}

	// 子进程直接共用当前这个控制台：一个窗口、一份 stdin/stdout。
	// 关窗口时控制台会一起带走所有挂着它的进程，不会留下孤儿。
	cmd := exec.Command(coreBin, "--runtime", runtimeDir)
	cmd.Dir = root
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			fmt.Println()
			fmt.Printf("%s 退出了（退出码 %d）。日志在 %s 里。\n",
				appName, exitErr.ExitCode(), filepath.Join(runtimeDir, "logs"))
			waitForEnter()
			return nil // 已经解释过了，不用再刷一遍 "启动失败"
		}
		return err
	}
	return nil
}

// locateRuntime 找到运行时目录和核心二进制。默认布局是
// <启动器所在目录>/runtime/，也允许启动器被单独拷到别处、从当前目录找。
func locateRuntime(root string) (dir string, bin string, err error) {
	candidates := []string{
		filepath.Join(root, "runtime"),
		filepath.Join(mustGetwd(), "runtime"),
	}
	for _, d := range candidates {
		for _, name := range []string{"core.exe", "core"} {
			p := filepath.Join(d, name)
			if st, statErr := os.Stat(p); statErr == nil && !st.IsDir() {
				abs, absErr := filepath.Abs(d)
				if absErr != nil {
					return d, p, nil
				}
				return abs, p, nil
			}
		}
	}
	return "", "", fmt.Errorf("在 %s 下没找到 runtime/core[.exe]，先跑一次构建", candidates[0])
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// alreadyRunning 借核心自己的控制端口判断有没有活着的实例。
// 端口从 runtime/core.json 读，读不到就用默认值。
func alreadyRunning(runtimeDir string) (string, bool) {
	host, port := "127.0.0.1", defaultControlPort
	if raw, err := os.ReadFile(filepath.Join(runtimeDir, "core.json")); err == nil {
		var cfg struct {
			ControlHost string `json:"controlHost"`
			ControlPort int    `json:"controlPort"`
		}
		if json.Unmarshal(raw, &cfg) == nil {
			if cfg.ControlHost != "" {
				host = cfg.ControlHost
			}
			if cfg.ControlPort > 0 {
				port = cfg.ControlPort
			}
		}
	}
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	conn, err := net.DialTimeout("tcp", addr, 400*time.Millisecond)
	if err != nil {
		return addr, false
	}
	conn.Close()
	return addr, true
}

// waitForEnter 让窗口停一下，否则双击启动时窗口会一闪而过，什么也看不到。
func waitForEnter() {
	fmt.Print("\n按回车关闭本窗口。")
	var s string
	fmt.Scanln(&s)
}
