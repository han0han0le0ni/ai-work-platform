package host

import (
	"os"
	"path/filepath"
	"testing"

	"aiwork/core/loader"
)

// 启动时清理：此刻插件还没运行、日志还没打开，所以能真正删掉
func TestApplyPendingDeletes(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plugins", "plugin-configs", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	gone := filepath.Join(dir, "plugins", "echo.plugin")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "plugin-configs", "com.official.echo.json")
	log := filepath.Join(dir, "logs", "com.official.echo.log")
	for _, p := range []string{cfg, log} {
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 同目录下的其他插件不能被牵连
	keep := filepath.Join(dir, "plugins", "keep.plugin")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	// 发起方必须声明 filesystem:delete
	if err := os.MkdirAll(filepath.Join(dir, "plugins", "manager.plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins", "manager.plugin", "manifest.json"),
		[]byte(`{"id":"com.official.plugin-manager","name":"m","version":"1.0.0","apiVersion":"1",
"entry":"main","runtime":"binary","permissions":["filesystem:read","filesystem:write","filesystem:delete"],
"compat":{"core":">=1.0 <2.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Host{dir: dir}
	h.applyPending([]loader.Change{
		{Action: "delete", PluginID: "com.official.echo", Dir: "echo.plugin", By: "com.official.plugin-manager"},
	})

	for _, p := range []string{gone, cfg, log} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s 应已被清除: %v", p, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("无关插件不应被牵连: %v", err)
	}

	// 幂等：重复执行不应报错
	h.applyPending([]loader.Change{
		{Action: "delete", PluginID: "com.official.echo", Dir: "echo.plugin", By: "com.official.plugin-manager"},
	})
}

// 启动时执行 enable / disable：此刻没有插件在运行，改名一定成功
func TestApplyPendingToggles(t *testing.T) {
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := filepath.Join(plugins, "manager.plugin")
	if err := os.MkdirAll(manager, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager, "manifest.json"),
		[]byte(`{"id":"com.official.plugin-manager","name":"m","version":"1.0.0","apiVersion":"1",
"entry":"main","runtime":"binary","permissions":["filesystem:read","filesystem:write","filesystem:delete"],
"compat":{"core":">=1.0 <2.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	enabled := filepath.Join(plugins, "echo.plugin")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	disabledDir := filepath.Join(plugins, "old.plugin.disabled")
	if err := os.MkdirAll(disabledDir, 0o755); err != nil {
		t.Fatal(err)
	}

	h := &Host{dir: dir}
	h.applyPending([]loader.Change{
		{Action: "disable", PluginID: "com.official.echo", Dir: "echo.plugin", By: "com.official.plugin-manager"},
		{Action: "enable", PluginID: "com.official.old", Dir: "old.plugin.disabled", By: "com.official.plugin-manager"},
	})

	if _, err := os.Stat(enabled + loader.DisabledSuffix); err != nil {
		t.Fatalf("禁用后应改名为 .disabled：%v", err)
	}
	if _, err := os.Stat(enabled); !os.IsNotExist(err) {
		t.Fatal("原名不应还在")
	}
	if _, err := os.Stat(filepath.Join(plugins, "old.plugin")); err != nil {
		t.Fatalf("启用后应去掉 .disabled 后缀：%v", err)
	}
}

// 启用 / 禁用只需要 filesystem:write，不要求 filesystem:delete
func TestToggleRequiresOnlyWrite(t *testing.T) {
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(filepath.Join(plugins, "echo.plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 只有读写权限，没有删除权限
	some := filepath.Join(plugins, "some.plugin")
	if err := os.MkdirAll(some, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(some, "manifest.json"),
		[]byte(`{"id":"com.other","name":"o","version":"1.0.0","apiVersion":"1",
"entry":"main","runtime":"binary","permissions":["filesystem:write"],
"compat":{"core":">=1.0 <2.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Host{dir: dir}
	h.applyPending([]loader.Change{
		{Action: "disable", PluginID: "com.official.echo", Dir: "echo.plugin", By: "com.other"},
	})
	if _, err := os.Stat(filepath.Join(plugins, "echo.plugin"+loader.DisabledSuffix)); err != nil {
		t.Fatalf("只声明 filesystem:write 也应能启停：%v", err)
	}

	// 但删除仍然要求 filesystem:delete
	h.applyPending([]loader.Change{
		{Action: "delete", PluginID: "com.official.echo", Dir: "echo.plugin" + loader.DisabledSuffix, By: "com.other"},
	})
	if _, err := os.Stat(filepath.Join(plugins, "echo.plugin"+loader.DisabledSuffix)); err != nil {
		t.Fatalf("没有 filesystem:delete 就不该替它删：%v", err)
	}
}

// 权限闸门：发起方没有 filesystem:delete 时，核心不替它删
func TestApplyPendingRequiresRequesterPermission(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "plugins", "echo.plugin")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// 发起方只有读写权限
	if err := os.MkdirAll(filepath.Join(dir, "plugins", "sneaky.plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins", "sneaky.plugin", "manifest.json"),
		[]byte(`{"id":"com.evil.sneaky","name":"s","version":"1.0.0","apiVersion":"1",
"entry":"main","runtime":"binary","permissions":["filesystem:read","filesystem:write"],
"compat":{"core":">=1.0 <2.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Host{dir: dir}
	h.applyPending([]loader.Change{
		{Action: "delete", PluginID: "com.official.echo", Dir: "echo.plugin", By: "com.evil.sneaky"},
	})
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("越权删除不应生效: %v", err)
	}

	// 没有发起方的记录同样不执行
	h.applyPending([]loader.Change{{Action: "delete", Dir: "echo.plugin"}})
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("缺少发起方时不应删除: %v", err)
	}
}

// 路径校验：目录名 / 插件 ID 里带路径分隔符或 .. 的记录一律忽略
func TestPendingPathsRejectsEscaping(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "outside-marker.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)

	var logged int
	logf := func(string, string, ...any) { logged++ }

	cases := []struct {
		name string
		in   loader.Change
	}{
		{"父目录穿越", loader.Change{Dir: "../.."}},
		{"绝对路径片段", loader.Change{Dir: `a\b.plugin`}},
		{"插件 ID 穿越", loader.Change{PluginID: "../escape"}},
	}
	for _, c := range cases {
		got := pendingPaths(dir, c.in, logf)
		if len(got) != 0 {
			t.Fatalf("%s 应被拒绝，实际产出 %v", c.name, got)
		}
	}
	if logged != len(cases) {
		t.Fatalf("非法记录应各记一条告警，实际 %d", logged)
	}

	// 正常名字照常产出三项
	got := pendingPaths(dir, loader.Change{Dir: "echo.plugin", PluginID: "com.official.echo"}, logf)
	if len(got) != 3 {
		t.Fatalf("正常记录应产出 3 条路径，实际 %v", got)
	}
	if got[0] != filepath.Join(dir, "plugins", "echo.plugin") {
		t.Fatalf("插件目录路径不正确: %s", got[0])
	}
	if got[1] != filepath.Join(dir, "plugin-configs", "com.official.echo.json") {
		t.Fatalf("配置路径不正确: %s", got[1])
	}
	if got[2] != filepath.Join(dir, "logs", "com.official.echo.log") {
		t.Fatalf("日志路径不正确: %s", got[2])
	}
}
