// Package loader 负责插件扫描、manifest 解析、apiVersion 校验与依赖拓扑排序。
package loader

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// RequiredAPIVersion 是当前核心实现的插件协议版本。
const RequiredAPIVersion = "1"

// Compat 声明插件兼容的核心版本范围。
type Compat struct {
	Core string `json:"core"`
}

// Manifest 是 manifest.json 的完整模型。
type Manifest struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Version      string            `json:"version"`
	APIVersion   string            `json:"apiVersion"`
	Entry        string            `json:"entry"`
	Runtime      string            `json:"runtime"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
	Provides     []string          `json:"provides,omitempty"`
	Consumes     []string          `json:"consumes,omitempty"`
	Permissions  []string          `json:"permissions,omitempty"`
	Compat       Compat            `json:"compat"`
}

// idPattern 强制反向域名格式：com.example.echo
var idPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_-]*)(\.[A-Za-z0-9]([A-Za-z0-9_-]*))+$`)

// permissionPattern 强制权限名小写、冒号分隔：filesystem:read
var permissionPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(:[a-z][a-z0-9]*)*$`)

// LoadManifest 读取并校验插件目录下的 manifest.json。
func LoadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("读取 manifest.json 失败: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析 manifest.json 失败: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate 校验必填字段。
func (m *Manifest) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("manifest 缺少必填字段 id")
	}
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("id 必须为反向域名格式，当前为 %q", m.ID)
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("manifest 缺少必填字段 version")
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return fmt.Errorf("version 非法: %w", err)
	}
	if strings.TrimSpace(m.APIVersion) == "" {
		return errors.New("manifest 缺少必填字段 apiVersion")
	}
	if strings.TrimSpace(m.Entry) == "" {
		return errors.New("manifest 缺少必填字段 entry")
	}
	if strings.TrimSpace(m.Runtime) == "" {
		return errors.New("manifest 缺少必填字段 runtime")
	}
	if strings.TrimSpace(m.Compat.Core) == "" {
		return errors.New("manifest 缺少必填字段 compat.core")
	}
	switch m.Runtime {
	case "binary", "python", "node", "java", "wasm":
	default:
		return fmt.Errorf("未知 runtime %q（可选 binary/python/node/java/wasm）", m.Runtime)
	}
	// 规范 3.7：权限名小写、冒号分隔
	for _, perm := range m.Permissions {
		if !permissionPattern.MatchString(perm) {
			return fmt.Errorf("权限名必须为小写冒号分隔格式，例如 filesystem:read，当前为 %q", perm)
		}
	}
	return nil
}

// KnownPermissions 是核心已知的权限清单。属于公开契约，只增不减。
var KnownPermissions = []string{
	"network",
	"filesystem:read",
	"filesystem:write",
	"filesystem:delete",
	"process:spawn",
	"shell:open",
	"shell:pickfolder",
	"shell:pickfiles",
	"sandbox:use",
}

// UnknownPermissions 返回核心不认识的权限名。
//
// 规范 3.4：MVP 阶段核心只记录、不强制；未知权限只告警，不拒绝加载，
// 以便未来新增权限时不破坏旧核心。
func (m *Manifest) UnknownPermissions() []string {
	known := make(map[string]bool, len(KnownPermissions))
	for _, p := range KnownPermissions {
		known[p] = true
	}
	var out []string
	for _, p := range m.Permissions {
		if !known[p] {
			out = append(out, p)
		}
	}
	return out
}

// LocalID 返回 id 的最后一段，例如 com.official.minimal-ui -> minimal-ui。
func (m *Manifest) LocalID() string {
	parts := strings.Split(m.ID, ".")
	if len(parts) == 0 {
		return m.ID
	}
	return parts[len(parts)-1]
}

// DirName 返回插件在 plugins/ 下的标准目录名，例如 minimal-ui.plugin。
func (m *Manifest) DirName() string {
	return m.LocalID() + ".plugin"
}

// ResolveEntry 解析入口可执行文件与启动参数。
//
// 核心不关心插件用什么语言写的，只看 runtime 字段：
//   - binary: 直接执行 entry（Windows 下自动补 .exe/.cmd/.bat）
//   - python: 用 python 执行 entry[.py]
//   - node:   用 node 执行 entry[.js]
//   - java:   entry 是 jar 包则 java -jar，是类名则 java -cp <插件目录> <类名>
//   - wasm:   需要适配器插件，核心不直接加载
func ResolveEntry(dir string, m *Manifest) (bin string, args []string, err error) {
	entry := m.Entry
	var candidates []string
	switch m.Runtime {
	case "binary":
		candidates = []string{entry}
		if runtime.GOOS == "windows" {
			candidates = append(candidates, entry+".exe", entry+".cmd", entry+".bat")
		}
	case "python":
		candidates = []string{entry, entry + ".py"}
	case "node":
		candidates = []string{entry, entry + ".js"}
	case "java":
		return resolveJavaEntry(dir, entry)
	case "wasm":
		return "", nil, fmt.Errorf("runtime=wasm 需要适配器插件，核心不直接加载")
	default:
		return "", nil, fmt.Errorf("未知 runtime %q", m.Runtime)
	}

	for _, c := range candidates {
		p := filepath.Join(dir, c)
		st, statErr := os.Stat(p)
		if statErr != nil || st.IsDir() {
			continue
		}
		switch m.Runtime {
		case "python":
			return pythonBin(), []string{"-u", p}, nil
		case "node":
			return nodeBin(), []string{p}, nil
		default:
			return p, nil, nil
		}
	}
	return "", nil, fmt.Errorf("入口文件不存在: %s (runtime=%s)", entry, m.Runtime)
}

// resolveJavaEntry 支持两种入口写法：jar 包，或类名（配合插件目录下的 .class）。
func resolveJavaEntry(dir, entry string) (string, []string, error) {
	for _, cand := range []string{entry, entry + ".jar"} {
		p := filepath.Join(dir, cand)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return javaBin(), []string{"-jar", p}, nil
		}
	}
	// 类名写法：com.example.Main -> com/example/Main.class
	rel := strings.ReplaceAll(entry, ".", string(filepath.Separator))
	if _, err := os.Stat(filepath.Join(dir, rel+".class")); err == nil {
		return javaBin(), []string{"-cp", dir, entry}, nil
	}
	return "", nil, fmt.Errorf("找不到 Java 入口：%s（既不是 jar 包，也没有对应的 .class）", entry)
}

// 解释器路径可用环境变量覆盖，方便机器上只有 python3 / 非默认 JDK 的情况。
func pythonBin() string {
	if v := os.Getenv("AIWORK_PYTHON"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
}

func nodeBin() string {
	if v := os.Getenv("AIWORK_NODE"); v != "" {
		return v
	}
	return "node"
}

func javaBin() string {
	if v := os.Getenv("AIWORK_JAVA"); v != "" {
		return v
	}
	return "java"
}
