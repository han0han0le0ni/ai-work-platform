package plugin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

// HandlerFunc 是事件处理函数。
type HandlerFunc func(payload json.RawMessage)

// Plugin 是插件作者实现的最小接口。
type Plugin interface {
	// Activate 在收到 core.init 后调用：在这里注册服务、订阅事件。
	Activate(ctx *Context) error
	// Handle 处理一次 core.invoke。args 可能是 {} 或 null。
	Handle(service, method string, args json.RawMessage) (any, error)
	// Deactivate 在收到 core.shutdown 时调用，可做清理。
	Deactivate() error
}

type server struct {
	p      Plugin
	in     *bufio.Reader
	out    io.Writer
	ctx    *Context
	mu     sync.Mutex
	nextID int64
	pend   map[string]chan *message
	evs    map[string][]HandlerFunc
}

// Serve 阻塞运行插件主循环，直到 stdin 关闭或核心要求退出。
func Serve(p Plugin) error {
	s := &server{
		p:    p,
		in:   bufio.NewReaderSize(os.Stdin, 64*1024),
		out:  os.Stdout,
		pend: make(map[string]chan *message),
		evs:  make(map[string][]HandlerFunc),
	}
	s.ctx = &Context{s: s, Permissions: []string{}}
	return s.loop()
}

func (s *server) loop() error {
	for {
		line, err := s.in.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var m message
			if uerr := json.Unmarshal(trimmed, &m); uerr != nil {
				Logf("error", "无法解析来自核心的消息: %v", uerr)
			} else {
				s.dispatch(&m)
			}
		}
		if err != nil {
			if err != io.EOF {
				Logf("error", "读取 stdin 失败: %v", err)
			}
			return nil
		}
	}
}

func (s *server) dispatch(m *message) {
	switch {
	case m.Method == "":
		s.deliver(m)
	case m.Method == "core.shutdown":
		s.handleShutdown()
	case m.isRequest():
		go s.handleRequest(m)
	default:
		go s.handleNotification(m)
	}
}

func (s *server) deliver(m *message) {
	s.mu.Lock()
	ch := s.pend[string(m.ID)]
	delete(s.pend, string(m.ID))
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- m:
		default:
		}
	}
}

func (s *server) handleRequest(m *message) {
	defer func() {
		if r := recover(); r != nil {
			Logf("error", "处理 %s 时 panic: %v\n%s", m.Method, r, debug.Stack())
			s.respondError(m.ID, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("插件 panic: %v", r)})
		}
	}()

	switch m.Method {
	case "core.init":
		var params struct {
			PluginID        string   `json:"pluginId"`
			APIVersion      string   `json:"apiVersion"`
			CoreVersion     string   `json:"coreVersion"`
			ConfigNamespace string   `json:"configNamespace"`
			Permissions     []string `json:"permissions"`
		}
		_ = json.Unmarshal(m.Params, &params)

		s.ctx.PluginID = params.PluginID
		s.ctx.APIVersion = params.APIVersion
		s.ctx.CoreVersion = params.CoreVersion
		s.ctx.Namespace = params.ConfigNamespace
		if s.ctx.Namespace == "" {
			s.ctx.Namespace = params.PluginID
		}
		if params.Permissions != nil {
			s.ctx.Permissions = params.Permissions
		}

		if err := s.p.Activate(s.ctx); err != nil {
			s.respondError(m.ID, asRPCError(err))
			return
		}
		s.respond(m.ID, map[string]any{"ready": true})

	case "core.invoke":
		var params struct {
			Service   string          `json:"service"`
			Method    string          `json:"method"`
			Args      json.RawMessage `json:"args"`
			Caller    string          `json:"caller"`
			Forwarded bool            `json:"forwarded"`
		}
		_ = json.Unmarshal(m.Params, &params)
		args := params.Args
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage("{}")
		}
		out, err := s.p.Handle(params.Service, params.Method, args)
		if err != nil {
			s.respondError(m.ID, asRPCError(err))
			return
		}
		s.respond(m.ID, out)

	default:
		s.respondError(m.ID, &RPCError{Code: CodeMethodNotFound, Message: "插件不支持方法 " + m.Method})
	}
}

func (s *server) handleNotification(m *message) {
	switch m.Method {
	case "core.event":
		var params struct {
			Event   string          `json:"event"`
			Payload json.RawMessage `json:"payload"`
			Origin  string          `json:"origin"`
		}
		_ = json.Unmarshal(m.Params, &params)
		for _, h := range s.handlersFor(params.Event) {
			s.safeCallHandler(params.Event, h, params.Payload)
		}
	default:
		Logf("warn", "收到未知通知 %s", m.Method)
	}
}

func (s *server) safeCallHandler(event string, h HandlerFunc, payload json.RawMessage) {
	defer func() {
		if r := recover(); r != nil {
			Logf("error", "处理事件 %s 时 panic: %v", event, r)
		}
	}()
	h(payload)
}

func (s *server) handleShutdown() {
	if s.p != nil {
		if err := s.p.Deactivate(); err != nil {
			Logf("error", "Deactivate 失败: %v", err)
		}
	}
	os.Exit(0)
}

func (s *server) addHandler(event string, h HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evs[event] = append(s.evs[event], h)
}

func (s *server) handlersFor(event string) []HandlerFunc {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]HandlerFunc{}, s.evs[event]...)
	out = append(out, s.evs["*"]...)
	return out
}

func (s *server) send(m *message) error {
	if m.JSONRPC == "" {
		m.JSONRPC = rpcVersion
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.out.Write(data)
	return err
}

func (s *server) respond(id json.RawMessage, result any) {
	var raw json.RawMessage
	if result == nil {
		raw = json.RawMessage("null")
	} else if r, ok := result.(json.RawMessage); ok {
		if len(r) == 0 {
			raw = json.RawMessage("null")
		} else {
			raw = r
		}
	} else {
		b, err := json.Marshal(result)
		if err != nil {
			s.respondError(id, &RPCError{Code: CodeInternalError, Message: "序列化返回值失败: " + err.Error()})
			return
		}
		raw = b
	}
	if err := s.send(&message{ID: id, Result: raw}); err != nil {
		Logf("error", "回写响应失败: %v", err)
	}
}

func (s *server) respondError(id json.RawMessage, e *RPCError) {
	if e == nil {
		e = &RPCError{Code: CodeInternalError, Message: "未知错误"}
	}
	if err := s.send(&message{ID: id, Error: e}); err != nil {
		Logf("error", "回写错误响应失败: %v", err)
	}
}

func (s *server) notify(method string, params any) error {
	praw, err := rawParams(params)
	if err != nil {
		return err
	}
	return s.send(&message{Method: method, Params: praw})
}

func (s *server) nextRequestID() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	return json.RawMessage(strconv.FormatInt(s.nextID, 10))
}

// call 向核心发起一次请求并等待响应。timeout <= 0 表示使用 24 小时的兜底上限。
func (s *server) call(method string, params any, timeout time.Duration) (*message, error) {
	praw, err := rawParams(params)
	if err != nil {
		return nil, err
	}
	id := s.nextRequestID()
	key := string(id)
	ch := make(chan *message, 1)

	s.mu.Lock()
	s.pend[key] = ch
	s.mu.Unlock()

	cleanup := func() {
		s.mu.Lock()
		delete(s.pend, key)
		s.mu.Unlock()
	}

	if err := s.send(&message{ID: id, Method: method, Params: praw}); err != nil {
		cleanup()
		return nil, err
	}

	if timeout <= 0 {
		timeout = 24 * time.Hour
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, m.Error
		}
		return m, nil
	case <-timer.C:
		cleanup()
		return nil, fmt.Errorf("调用 %s 超时（%s）", method, timeout)
	}
}
