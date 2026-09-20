package registry

import "testing"

// 规范 3.7：服务名 <名称>.<版本>
func TestValidServiceName(t *testing.T) {
	good := []string{"echo.v1", "llm.v1", "llm.v2", "toolExecutor.v1", "sandbox.v1", "com.example.echo.v10"}
	for _, s := range good {
		if !ValidServiceName(s) {
			t.Errorf("应接受服务名 %q", s)
		}
	}
	bad := []string{"", "echo", "echo.1", "echo.v", "echo.vX", ".v1", "echo..v1", "1echo.v1"}
	for _, s := range bad {
		if ValidServiceName(s) {
			t.Errorf("应拒绝服务名 %q", s)
		}
	}
}

// 规范 3.7：事件名 <命名空间>.<动作>
func TestValidEventName(t *testing.T) {
	good := []string{"chat.message", "tool.before", "core.log", "plugin.state", "a.b.c2", Wildcard}
	for _, s := range good {
		if !ValidEventName(s) {
			t.Errorf("应接受事件名 %q", s)
		}
	}
	bad := []string{"", "chat", "chat.", ".message", "chat..message", "a.1"}
	for _, s := range bad {
		if ValidEventName(s) {
			t.Errorf("应拒绝事件名 %q", s)
		}
	}
}

// 服务名不带版本必须被拒绝注册
func TestProvideRejectsUnversioned(t *testing.T) {
	r := New()
	if err := r.Provide("echo", "com.a.echo", nil); err == nil {
		t.Fatal("不带版本的服务名应被拒绝")
	}
	if r.Len() != 0 {
		t.Fatal("被拒绝的服务不应进入注册表")
	}
	if err := r.Provide("echo.v1", "com.a.echo", nil); err != nil {
		t.Fatalf("合法服务名应被接受: %v", err)
	}
}

// 同名服务被其他插件抢占必须失败，同插件重复注册幂等
func TestProvideConflict(t *testing.T) {
	r := New()
	if err := r.Provide("echo.v1", "com.a.echo", nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Provide("echo.v1", "com.a.echo", nil); err != nil {
		t.Fatalf("同一插件重复注册应幂等: %v", err)
	}
	if err := r.Provide("echo.v1", "com.b.other", nil); err == nil {
		t.Fatal("不同插件抢注同名服务应失败")
	}
}

// 事件名不带命名空间必须被拒绝订阅
func TestSubscribeRejectsBadEvent(t *testing.T) {
	b := NewBus()
	if err := b.Subscribe("badevent", "com.a.echo"); err == nil {
		t.Fatal("不带命名空间的事件名应被拒绝")
	}
	if err := b.Subscribe("chat.message", "com.a.echo"); err != nil {
		t.Fatalf("合法事件名应被接受: %v", err)
	}
	if got := b.Subscribers("chat.message"); len(got) != 1 || got[0] != "com.a.echo" {
		t.Fatalf("订阅者列表不正确: %v", got)
	}
}

// 规范 1.5③：插件卸载时核心自动取消其所有监听
func TestUnsubscribeAll(t *testing.T) {
	b := NewBus()
	_ = b.Subscribe("chat.message", "com.a.echo")
	_ = b.Subscribe(Wildcard, "com.a.echo")
	b.UnsubscribeAll("com.a.echo")
	if n := b.Count(); n != 0 {
		t.Fatalf("卸载后监听数应为 0，实际 %d", n)
	}
}

// 规范 1.5①：提供者卸载后服务即刻失效
func TestRemovePlugin(t *testing.T) {
	r := New()
	_ = r.Provide("echo.v1", "com.a.echo", nil)
	_ = r.Provide("paste.v1", "com.a.echo", nil)
	_ = r.Provide("llm.v1", "com.b.llm", nil)

	removed := r.RemovePlugin("com.a.echo")
	if len(removed) != 2 {
		t.Fatalf("应清理 2 个服务，实际 %v", removed)
	}
	if _, ok := r.Lookup("echo.v1"); ok {
		t.Fatal("echo.v1 应已失效")
	}
	if _, ok := r.Lookup("llm.v1"); !ok {
		t.Fatal("其他插件的服务不应受影响")
	}
}
