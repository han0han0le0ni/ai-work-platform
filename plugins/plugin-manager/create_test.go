package main

import (
	"strings"
	"testing"
)

// create 生成的 manifest 必须能通过核心的校验，否则插件会被静默跳过加载。
// 这里逐条对着核心 loader 的规则测。
func TestPluginDirName(t *testing.T) {
	cases := map[string]string{
		"com.ai.calc":         "calc.plugin",
		"com.official.echo":   "echo.plugin",
		"com.example.my-tool": "my-tool.plugin",
		"a.b":                 "b.plugin",
		"com.ai":              "ai.plugin",
	}
	for id, want := range cases {
		if got := pluginDirName(id); got != want {
			t.Fatalf("pluginDirName(%q) = %q，期望 %q", id, got, want)
		}
	}
}

// 规则与核心 loader.ParseVersion 一致：宽松接受，输出规范形式
func TestParseVersion(t *testing.T) {
	cases := map[string]string{
		"1.0.0":        "1.0.0",
		"0.1.2":        "0.1.2",
		"10.20.30":     "10.20.30",
		"v1.2.3":       "1.2.3", // v 前缀
		"1.0":          "1.0.0", // 两段
		"1":            "1.0.0", // 一段
		"1.0.0-beta.1": "1.0.0", // 预发布后缀
		" 1.2.3 ":      "1.2.3", // 首尾空格
	}
	for in, want := range cases {
		got, err := ParseVersion(in)
		if err != nil {
			t.Fatalf("%q 应当合法: %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseVersion(%q) = %q，期望规范化为 %q", in, got, want)
		}
	}

	bad := []string{"", "1.0.0.0", "1.0.x", "1..0", "-1.0.0", "v", "  ", "1.0.-1"}
	for _, v := range bad {
		if _, err := ParseVersion(v); err == nil {
			t.Fatalf("%q 应当被拒绝", v)
		}
	}
}

// id / 权限名的规则和核心保持一致
func TestCreatePatterns(t *testing.T) {
	goodIDs := []string{"com.ai.calc", "com.official.tool", "a.b", "com.example.my-plug"}
	for _, id := range goodIDs {
		if !idPattern.MatchString(id) {
			t.Fatalf("id %q 应当合法", id)
		}
	}
	badIDs := []string{"", "calc", "com.", ".com", "com ai calc", "com.ai.", "有一个中文"}
	for _, id := range badIDs {
		if idPattern.MatchString(id) {
			t.Fatalf("id %q 应当被拒绝", id)
		}
	}

	if !permissionPattern.MatchString("filesystem:read") ||
		!permissionPattern.MatchString("process:spawn") ||
		!permissionPattern.MatchString("network") {
		t.Fatal("合法权限名应被接受")
	}
	if permissionPattern.MatchString("Filesystem:Read") || permissionPattern.MatchString("filesystem read") {
		t.Fatal("非法权限名应被拒绝")
	}
}

// 生成的 Python 骨架必须自包含且能跑：不 import 任何 aiwork SDK，
// 因为 create 时未必读得到源码树里的 SDK。
func TestPythonSkeletonIsSelfContained(t *testing.T) {
	code := pythonSkeleton("com.ai.calc", "Calc", []string{"calc.v1"})

	if strings.Contains(code, "import aiwork_plugin") ||
		strings.Contains(code, "aiwork/sdk") {
		t.Fatal("Python 骨架不应依赖任何 SDK（插件目录要自包含）")
	}
	// 协议的最小集
	for _, want := range []string{
		"core.init", "core.invoke", "core.shutdown",
		"core.provide", "jsonrpc", "def handle(",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("骨架缺少 %q", want)
		}
	}
	if !strings.Contains(code, `SERVICE = "calc.v1"`) {
		t.Fatal("骨架里的服务名应来自 provides[0]")
	}
	// 没有 provides 时也要能生成
	if !strings.Contains(pythonSkeleton("com.ai.x", "X", nil), "unnamed.v1") {
		t.Fatal("没有 provides 时应退回默认服务名")
	}
}

func TestGoSkeletonUsesSDK(t *testing.T) {
	code := goSkeleton("com.ai.calc", []string{"calc.v1"})
	if !strings.Contains(code, "aiwork/sdk/go/plugin") {
		t.Fatal("Go 骨架应使用官方 SDK")
	}
	for _, want := range []string{"func main()", "plugin.Serve", "ctx.Provide"} {
		if !strings.Contains(code, want) {
			t.Fatalf("Go 骨架缺少 %q", want)
		}
	}
}
