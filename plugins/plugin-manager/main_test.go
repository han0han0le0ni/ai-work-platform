package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func sampleSet() []installed {
	return []installed{
		{
			info: pluginInfo{ID: "com.official.fs-tool", Dir: "fs-tool.plugin", DirPath: "/rt/plugins/fs-tool.plugin"},
			man:  manifestFile{ID: "com.official.fs-tool", Provides: []string{"readFile.v1"}},
		},
		{
			info: pluginInfo{ID: "com.official.llm", Dir: "llm.plugin", DirPath: "/rt/plugins/llm.plugin"},
			man: manifestFile{
				ID:       "com.official.llm",
				Provides: []string{"llm.v1"},
				Consumes: []string{"readFile.v1"},
			},
		},
		{
			info: pluginInfo{ID: "com.official.agent", Dir: "agent.plugin", DirPath: "/rt/plugins/agent.plugin"},
			man: manifestFile{
				ID:           "com.official.agent",
				Provides:     []string{"agent.v1"},
				Dependencies: map[string]string{"com.official.fs-tool": "^1.0"},
			},
		},
		{
			info: pluginInfo{ID: "com.official.workbench", Dir: "workbench.plugin", DirPath: "/rt/plugins/workbench.plugin"},
			man:  manifestFile{ID: "com.official.workbench", Provides: []string{"ui.v1"}},
		},
	}
}

// 按 ID 定位；ID 匹配不上时回退到目录名
func TestFindPlugin(t *testing.T) {
	all := sampleSet()

	if got, ok := findPlugin(all, "com.official.fs-tool"); !ok || got.info.Dir != "fs-tool.plugin" {
		t.Fatalf("按 ID 查找失败: %+v", got)
	}
	if got, ok := findPlugin(all, "llm.plugin"); !ok || got.man.ID != "com.official.llm" {
		t.Fatalf("按目录名查找失败: %+v", got)
	}
	if got, ok := findPlugin(all, "llm"); !ok || got.man.ID != "com.official.llm" {
		t.Fatalf("按去后缀目录名查找失败: %+v", got)
	}
	if _, ok := findPlugin(all, "com.official.missing"); ok {
		t.Fatal("不存在的插件不应被找到")
	}
}

// 删除前把“谁会受影响”说清楚
func TestDependents(t *testing.T) {
	all := sampleSet()

	fs, _ := findPlugin(all, "com.official.fs-tool")
	warns := dependents(all, fs)
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "com.official.llm 正在消费服务 readFile.v1") {
		t.Fatalf("应提示 llm 正在消费 readFile.v1: %v", warns)
	}
	if !strings.Contains(joined, "com.official.agent") {
		t.Fatalf("应提示 agent 在 dependencies 中声明了 fs-tool: %v", warns)
	}

	// 删 UI 要提醒“重启后没有界面”
	ui, _ := findPlugin(all, "com.official.workbench")
	uiWarns := strings.Join(dependents(all, ui), "\n")
	if !strings.Contains(uiWarns, "ui.v1") {
		t.Fatalf("删除提供 ui.v1 的插件应给出提醒: %v", uiWarns)
	}

	// 无人依赖时不应产生噪音
	agent, _ := findPlugin(all, "com.official.agent")
	if got := dependents(all, agent); len(got) != 0 {
		t.Fatalf("无人依赖时应无警告: %v", got)
	}
}

// 删除清单：目录 + 配置 + 日志；keepLogs 时留日志
func TestPlan(t *testing.T) {
	m := &Manager{root: "/rt"}
	all := sampleSet()
	fs, _ := findPlugin(all, "com.official.fs-tool")

	plan := m.plan(fs, false)
	if len(plan) != 3 {
		t.Fatalf("应包含目录、配置、日志三项: %v", plan)
	}
	if plan[0] != "/rt/plugins/fs-tool.plugin" {
		t.Fatalf("第 0 项必须是插件目录: %v", plan[0])
	}
	if plan[1] != filepath.Join("/rt", "plugin-configs", "com.official.fs-tool.json") {
		t.Fatalf("配置路径不正确: %v", plan[1])
	}
	if plan[2] != filepath.Join("/rt", "logs", "com.official.fs-tool.log") {
		t.Fatalf("日志路径不正确: %v", plan[2])
	}

	if got := m.plan(fs, true); len(got) != 2 {
		t.Fatalf("keepLogs 时不应删除日志: %v", got)
	}
}

// 目录名换算：禁用加后缀，启用去掉后缀，且不能重复加
func TestToggleDisabledName(t *testing.T) {
	cases := []struct {
		dir    string
		enable bool
		want   string
	}{
		{"fs-tool.plugin", false, "fs-tool.plugin.disabled"},
		{"fs-tool.plugin.disabled", true, "fs-tool.plugin"},
		{"fs-tool.plugin", true, "fs-tool.plugin"},                    // 已是启用态，保持不变
		{"fs-tool.plugin.disabled", false, "fs-tool.plugin.disabled"}, // 已是禁用态，不重复加
	}
	for _, c := range cases {
		if got := toggleDisabledName(c.dir, c.enable); got != c.want {
			t.Fatalf("toggleDisabledName(%q, %v) = %q，期望 %q", c.dir, c.enable, got, c.want)
		}
	}
}

// 拼路径的插件 ID 必须挡住，否则能删到别处去
func TestValidID(t *testing.T) {
	good := []string{"com.official.fs-tool", "com.example.my-plugin", "a.b"}
	for _, id := range good {
		if !validID(id) {
			t.Fatalf("%q 应当合法", id)
		}
	}
	bad := []string{"", "../escape", `..\escape`, "a/b", "com/official", "C:evil"}
	for _, id := range bad {
		if validID(id) {
			t.Fatalf("%q 应当被拒绝", id)
		}
	}
}
