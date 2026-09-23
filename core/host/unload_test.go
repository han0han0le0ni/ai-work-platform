package host

import (
	"testing"

	"aiwork/core/process"
	"aiwork/core/registry"
)

// 热卸载：停进程、摘除、注销服务、清订阅，四件事一起做完。
func TestUnloadPluginRemovesEverything(t *testing.T) {
	h := &Host{
		sup: process.NewSupervisor(),
		reg: registry.New(),
		bus: registry.NewBus(),
	}

	// 登记一个"运行中"的插件 + 一个服务 + 一个订阅
	p := process.New(process.Spec{ID: "com.official.demo"}, nil, nil, nil)
	h.sup.Add(p)
	if err := h.reg.Provide("demo.v1", "com.official.demo", nil); err != nil {
		t.Fatalf("注册服务失败：%v", err)
	}
	if err := h.bus.Subscribe("chat.message", "com.official.demo"); err != nil {
		t.Fatalf("订阅失败：%v", err)
	}

	removed := h.unloadPlugin("com.official.demo")

	if len(removed) != 1 || removed[0] != "demo.v1" {
		t.Fatalf("应注销 demo.v1，实际 %v", removed)
	}
	if h.sup.Get("com.official.demo") != nil {
		t.Fatal("应从 supervisor 摘除")
	}
	if p.Alive() {
		t.Fatal("进程应已停")
	}
	if _, ok := h.reg.Lookup("demo.v1"); ok {
		t.Fatal("服务应已注销")
	}
	if subs := h.bus.Subscribers("chat.message"); len(subs) != 0 {
		t.Fatalf("订阅应已清空，实际 %v", subs)
	}
}

// 卸载一个没在运行的插件：不 panic，返回空列表。
func TestUnloadPluginMissingIsNoop(t *testing.T) {
	h := &Host{
		sup: process.NewSupervisor(),
		reg: registry.New(),
		bus: registry.NewBus(),
	}

	removed := h.unloadPlugin("com.official.none")
	if len(removed) != 0 {
		t.Fatalf("没在运行的插件不该有服务可注销，实际 %v", removed)
	}
}
