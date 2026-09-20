package plugin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// noopPlugin 只是把 server 装起来，测的是 SDK 自己的收发链路。
type noopPlugin struct{}

func (noopPlugin) Activate(*Context) error                             { return nil }
func (noopPlugin) Handle(string, string, json.RawMessage) (any, error) { return nil, nil }
func (noopPlugin) Deactivate() error                                   { return nil }

// fakeCore 扮演核心：把插件写出的每一行读出来，是请求就回一个 ok。
//
// 返回的通道里是插件发出的所有请求 —— 测试用它断言"插件到底跟核心说了什么"。
func fakeCore(fromPlugin io.Reader, toPlugin io.Writer) <-chan message {
	reqs := make(chan message, 16)
	go func() {
		r := bufio.NewReader(fromPlugin)
		for {
			line, err := r.ReadBytes('\n')
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				var m message
				if json.Unmarshal(trimmed, &m) == nil {
					select {
					case reqs <- m:
					default:
					}
					if m.isRequest() {
						fmt.Fprintf(toPlugin, `{"jsonrpc":"2.0","id":%s,"result":{"ok":true}}`+"\n", m.ID)
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return reqs
}

func newTestServer() (*server, <-chan message, io.Writer) {
	coreToPluginR, coreToPluginW := io.Pipe()
	pluginToCoreR, pluginToCoreW := io.Pipe()

	s := &server{
		p:    noopPlugin{},
		in:   bufio.NewReader(coreToPluginR),
		out:  pluginToCoreW,
		pend: map[string]chan *message{},
		evs:  map[string][]HandlerFunc{},
	}
	s.ctx = &Context{s: s}
	return s, fakeCore(pluginToCoreR, coreToPluginW), coreToPluginW
}

// ctx.On 只把处理函数记在本地是不够的。
//
// 核心的 Emit 只投给 bus 里登记过的订阅者，所以 On 必须同时向核心发一次 core.on。
// 这一步曾经漏掉，后果是所有插件的 ctx.On 都收不到任何事件：界面的
// 「插件状态变化后自动补扫」完全失效，只能靠用户手点刷新。
func TestOnSubscribesAtCoreAndReceives(t *testing.T) {
	s, reqs, toPlugin := newTestServer()
	go func() { _ = s.loop() }()

	got := make(chan string, 1)
	s.ctx.On("plugin.state", func(payload json.RawMessage) { got <- string(payload) })

	select {
	case m := <-reqs:
		if m.Method != "core.on" {
			t.Fatalf("On 应该向核心发 core.on，实际发了 %s", m.Method)
		}
		if !bytes.Contains(m.Params, []byte("plugin.state")) {
			t.Fatalf("订阅的事件名不对：%s", m.Params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("On 没有向核心订阅：bus 里没有这个订阅者，事件永远送不到这个进程")
	}

	// 核心按订阅投回一条事件，处理函数必须被调用
	fmt.Fprintf(toPlugin, `{"jsonrpc":"2.0","method":"core.event","params":{"event":"plugin.state","payload":{"pluginId":"com.official.workspace","state":"active"}}}`+"\n")

	select {
	case p := <-got:
		if !strings.Contains(p, "workspace") {
			t.Fatalf("处理函数拿到的载荷不对：%s", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("core.event 没有派发到处理函数")
	}
}

// "*" 订阅全部：任何事件名都要能派发。
func TestWildcardHandlerReceivesAnyEvent(t *testing.T) {
	s, reqs, toPlugin := newTestServer()
	go func() { _ = s.loop() }()

	got := make(chan string, 1)
	s.ctx.On("*", func(payload json.RawMessage) { got <- string(payload) })

	select {
	case m := <-reqs:
		if m.Method != "core.on" || !bytes.Contains(m.Params, []byte(`"*"`)) {
			t.Fatalf("通配订阅没有正确发出：method=%s params=%s", m.Method, m.Params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("通配订阅没有向核心发出 core.on")
	}

	fmt.Fprintf(toPlugin, `{"jsonrpc":"2.0","method":"core.event","params":{"event":"core.log","payload":{"level":"info"}}}`+"\n")

	select {
	case p := <-got:
		if !strings.Contains(p, "info") {
			t.Fatalf("通配处理函数拿到的载荷不对：%s", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("通配订阅没有收到事件")
	}
}
