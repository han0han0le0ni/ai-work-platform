package loader

import (
	"testing"

	"aiwork/core/jsonrpc"
)

// 规范 3.3：manifest 声明兼容范围
func TestMatchRange(t *testing.T) {
	cases := []struct {
		version string
		rng     string
		want    bool
	}{
		{"1.0.0", ">=1.0 <2.0", true},
		{"1.9.9", ">=1.0 <2.0", true},
		{"2.0.0", ">=1.0 <2.0", false},
		{"0.9.0", ">=1.0 <2.0", false},
		{"1.2.3", "^1.0", true},
		{"2.0.0", "^1.0", false},
		{"1.2.9", "~1.2", true},
		{"1.3.0", "~1.2", false},
		{"1.0.0", "*", true},
		{"1.0.0", "", true},
		{"1.0.0", "=1.0.0", true},
	}
	for _, c := range cases {
		got, err := MatchRange(c.version, c.rng)
		if err != nil {
			t.Fatalf("MatchRange(%q,%q) 报错: %v", c.version, c.rng, err)
		}
		if got != c.want {
			t.Errorf("MatchRange(%q,%q) = %v，期望 %v", c.version, c.rng, got, c.want)
		}
	}
}

// 规范 2.2：manifest 必填字段 + 3.7 权限命名
func TestManifestValidate(t *testing.T) {
	base := func() *Manifest {
		return &Manifest{
			ID: "com.a.echo", Version: "1.0.0", APIVersion: "1",
			Entry: "main", Runtime: "binary", Compat: Compat{Core: ">=1.0 <2.0"},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("合法 manifest 不应报错: %v", err)
	}

	broken := map[string]func(*Manifest){
		"缺少 id":          func(m *Manifest) { m.ID = "" },
		"id 非反向域名":       func(m *Manifest) { m.ID = "echo" },
		"缺少 version":     func(m *Manifest) { m.Version = "" },
		"version 非法":     func(m *Manifest) { m.Version = "abc" },
		"缺少 apiVersion":  func(m *Manifest) { m.APIVersion = "" },
		"缺少 entry":       func(m *Manifest) { m.Entry = "" },
		"缺少 runtime":     func(m *Manifest) { m.Runtime = "" },
		"未知 runtime":     func(m *Manifest) { m.Runtime = "ruby" },
		"缺少 compat.core": func(m *Manifest) { m.Compat.Core = "" },
		"权限名格式非法":        func(m *Manifest) { m.Permissions = []string{"Filesystem:Read"} },
	}
	for name, mutate := range broken {
		m := base()
		mutate(m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s：应当校验失败", name)
		}
	}

	// 规范 3.4：未知权限只告警，不拒绝加载
	m := base()
	m.Permissions = []string{"network", "future:capability"}
	if err := m.Validate(); err != nil {
		t.Fatalf("格式合法的未知权限不应被拒绝: %v", err)
	}
	if unknown := m.UnknownPermissions(); len(unknown) != 1 || unknown[0] != "future:capability" {
		t.Fatalf("未知权限识别不正确: %v", unknown)
	}
}

func mkDiscovery(id, version, api, coreRange string, deps map[string]string) Discovery {
	return Discovery{
		Dir: "/plugins/" + id + ".plugin",
		Manifest: &Manifest{
			ID: id, Version: version, APIVersion: api,
			Entry: "main", Runtime: "binary",
			Compat:       Compat{Core: coreRange},
			Dependencies: deps,
			Provides:     []string{id + ".v1"},
		},
	}
}

// 规范 1.6 步骤 4 + 2.5：依赖缺失 -> 跳过，并以 -32004 精确报告
func TestBuildPlanSkipsMissingDependency(t *testing.T) {
	plan := BuildPlan([]Discovery{
		mkDiscovery("com.a.echo", "1.0.0", "1", ">=1.0 <2.0", map[string]string{"com.a.sandbox": "^1.0"}),
	}, "1.0.0", "1")

	if len(plan.Order) != 0 {
		t.Fatalf("依赖缺失的插件不应进入启动序列: %v", plan.Order)
	}
	if len(plan.Skipped) != 1 {
		t.Fatalf("应跳过 1 个插件，实际 %d", len(plan.Skipped))
	}
	s := plan.Skipped[0]
	if s.Code != jsonrpc.CodeDependencyMissing {
		t.Errorf("错误码应为 -32004，实际 %d", s.Code)
	}
	if len(s.Provides) != 1 || s.Provides[0] != "com.a.echo.v1" {
		t.Errorf("应记录该插件原本提供的服务，实际 %v", s.Provides)
	}
}

// 规范 2.5：apiVersion 不兼容 -> -32005
func TestBuildPlanSkipsIncompatibleAPIVersion(t *testing.T) {
	plan := BuildPlan([]Discovery{
		mkDiscovery("com.a.echo", "1.0.0", "2", ">=1.0 <2.0", nil),
	}, "1.0.0", "1")

	if len(plan.Skipped) != 1 || plan.Skipped[0].Code != jsonrpc.CodeAPIVersionIncompatible {
		t.Fatalf("apiVersion 不兼容应报 -32005，实际 %+v", plan.Skipped)
	}
}

// 规范 3.3：compat.core 不兼容 -> 拒绝加载
func TestBuildPlanSkipsIncompatibleCoreRange(t *testing.T) {
	plan := BuildPlan([]Discovery{
		mkDiscovery("com.a.echo", "1.0.0", "1", ">=2.0", nil),
	}, "1.0.0", "1")

	if len(plan.Skipped) != 1 || plan.Skipped[0].Code != jsonrpc.CodeAPIVersionIncompatible {
		t.Fatalf("compat.core 不兼容应被拒绝，实际 %+v", plan.Skipped)
	}
}

// 规范 1.6：被依赖者先启动
func TestBuildPlanTopologicalOrder(t *testing.T) {
	plan := BuildPlan([]Discovery{
		mkDiscovery("com.a.agent", "1.0.0", "1", ">=1.0 <2.0", map[string]string{"com.a.llm": "^1.0"}),
		mkDiscovery("com.a.llm", "1.0.0", "1", ">=1.0 <2.0", nil),
	}, "1.0.0", "1")

	if len(plan.Skipped) != 0 {
		t.Fatalf("不应跳过任何插件: %+v", plan.Skipped)
	}
	if len(plan.Order) != 2 {
		t.Fatalf("应加载 2 个插件，实际 %d", len(plan.Order))
	}
	if plan.Order[0].Manifest.ID != "com.a.llm" {
		t.Fatalf("被依赖者必须先启动，实际顺序 %s -> %s",
			plan.Order[0].Manifest.ID, plan.Order[1].Manifest.ID)
	}
}

// 依赖版本不满足同样跳过
func TestBuildPlanSkipsUnsatisfiedDependencyVersion(t *testing.T) {
	plan := BuildPlan([]Discovery{
		mkDiscovery("com.a.agent", "1.0.0", "1", ">=1.0 <2.0", map[string]string{"com.a.llm": "^2.0"}),
		mkDiscovery("com.a.llm", "1.0.0", "1", ">=1.0 <2.0", nil),
	}, "1.0.0", "1")

	if len(plan.Order) != 1 || plan.Order[0].Manifest.ID != "com.a.llm" {
		t.Fatalf("只有 llm 应被加载: %v", plan.Order)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].Code != jsonrpc.CodeDependencyMissing {
		t.Fatalf("依赖版本不满足应报 -32004: %+v", plan.Skipped)
	}
}

// 循环依赖必须被检出并跳过，而不是死循环
func TestBuildPlanDetectsCycle(t *testing.T) {
	plan := BuildPlan([]Discovery{
		mkDiscovery("com.a.x", "1.0.0", "1", ">=1.0 <2.0", map[string]string{"com.a.y": "^1.0"}),
		mkDiscovery("com.a.y", "1.0.0", "1", ">=1.0 <2.0", map[string]string{"com.a.x": "^1.0"}),
	}, "1.0.0", "1")

	if len(plan.Order) != 0 {
		t.Fatalf("循环依赖不应产生启动序列: %v", plan.Order)
	}
	if len(plan.Skipped) != 2 {
		t.Fatalf("循环依赖双方都应被跳过，实际 %+v", plan.Skipped)
	}
}

// 规范 2.6：安装只标记 pending-restart，重启后生效并清除标记
func TestPendingRestartRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := MarkPendingRestart(dir, Change{Action: "install", PluginID: "com.a.echo"}); err != nil {
		t.Fatal(err)
	}
	if err := MarkPendingRestart(dir, Change{Action: "install", PluginID: "com.a.ui"}); err != nil {
		t.Fatal(err)
	}

	got := TakePendingRestart(dir)
	if len(got) != 2 || got[0].PluginID != "com.a.echo" || got[1].PluginID != "com.a.ui" {
		t.Fatalf("标记内容不正确: %+v", got)
	}
	if again := TakePendingRestart(dir); len(again) != 0 {
		t.Fatalf("标记应已被清除，实际 %+v", again)
	}
}
