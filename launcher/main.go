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
	"strings"
	"time"
)

// appName 是窗口标题和提示语里的产品名。可执行文件本身的文件名由
// launcher/name.txt 提供（构建脚本读它），两者保持同一个名字。
const appName = "AI 工作平台"

const defaultControlPort = 9123

func main() {
	if err := run(); err != nil {
		messageBox(appName, "启动失败: "+err.Error())
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

	if addr, running := alreadyRunning(runtimeDir); running {
		messageBox(appName, appName+" 已经在运行了（控制端口 "+addr+"）。\n\n"+
			"先找找已经开着的那个窗口；确实没有的话，说明上次没退干净，\n"+
			"把残留的 core 进程结束掉再启动。")
		return nil
	}

	// 有插件 = 核心直接进原生 GUI，不给它建控制台窗口（这是去掉黑框的关键）。
	// 无插件 = 核心进 TUI 引导菜单，必须给它一个新控制台让用户交互。
	cmd := exec.Command(coreBin, "--runtime", runtimeDir)
	cmd.Dir = root
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if pluginsExist(runtimeDir) {
		configureChild(cmd)
	} else {
		configureChildConsole(cmd)
	}

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			messageBox(appName, fmt.Sprintf("%s 退出了（退出码 %d）。\n日志在 %s 里。",
				appName, exitErr.ExitCode(), filepath.Join(runtimeDir, "logs")))
			return nil // 已经解释过了，不用再刷一遍 "启动失败"
		}
		return err
	}

	// 退出码 0：可能是正常退出，也可能是「重启核心」。
	//
	// 重启时旧 core 先派生新 core 再退出，新 core 会重新监听控制端口。
	// 若启动器此刻就退出，控制台窗口随之关闭，Windows 会把还挂在控制台上的
	// 新 core 一起带走 —— 这正是「重启后核心没了」的根子。
	// 所以再等一小会儿：一旦端口重新出现，就继续等新 core 退出，
	// 把控制台让给新 core，直到它也退出才收尾。
	waitForSuccessor(runtimeDir)
	return nil
}

// waitForSuccessor 处理「重启核心」：旧 core 退出后，若控制端口在短暂间隔内
// 重新出现（新 core 已接管），就阻塞等待新 core 也退出；否则按正常退出收尾。
//
// 轮询窗口给得足够宽：新 core 的控制端口在加载插件之前就打开，通常几百毫秒
// 内就绪，几秒足够覆盖启动偏慢或端口 TIME_WAIT 重试的机器。
func waitForSuccessor(runtimeDir string) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, running := alreadyRunning(runtimeDir); running {
			// 新 core 起来了，等它退出再走
			for {
				time.Sleep(400 * time.Millisecond)
				if _, still := alreadyRunning(runtimeDir); !still {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
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

// pluginsExist 判断运行时是否已经装了插件（和核心 PluginsExist 同样的判据）。
// 有插件 = 核心直接进 GUI = 不给它建控制台；无插件 = 核心进 TUI 引导菜单 = 必须给控制台。
func pluginsExist(runtimeDir string) bool {
	entries, err := os.ReadDir(filepath.Join(runtimeDir, "plugins"))
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
