// Package process 管理插件子进程与 JSON-RPC over stdio 的收发。
//
// 硬通道约定：
//   - 核心 → 插件：写入子进程 stdin
//   - 插件 → 核心：读取子进程 stdout
//   - 每条消息一行 JSON（JSON Lines）
//   - stderr 只用于插件日志，核心只转发不解析
//   - 进程退出即插件卸载
package process

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"aiwork/core/jsonrpc"
	"aiwork/core/logs"
)

// State 是插件状态机的取值。
type State string

const (
	StatePending  State = "pending"
	StateStarting State = "starting"
	StateActive   State = "active"
	StateDegraded State = "degraded"
	StateDead     State = "dead"
	StateDisabled State = "disabled"
)

const maxLineBytes = 16 * 1024 * 1024

// Handler 由宿主内核实现，负责处理插件发来的所有消息。
type Handler interface {
	HandleRequest(p *Plugin, msg *jsonrpc.Message)
	HandleNotification(p *Plugin, msg *jsonrpc.Message)
}

// Spec 描述如何启动一个插件子进程。
type Spec struct {
	ID   string
	Dir  string
	Bin  string
	Args []string
	Env  []string
}

// Plugin 代表一个运行中的插件子进程。
type Plugin struct {
	Spec   Spec
	Logger *logs.Logger

	handler Handler
	onState func(*Plugin, State)

	mu      sync.Mutex
	state   State
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	pending map[string]chan *jsonrpc.Message
	nextID  int64
	stopped bool

	dead     chan struct{}
	deadOnce sync.Once
}

// New 创建一个尚未启动的插件。
func New(spec Spec, lg *logs.Logger, h Handler, onState func(*Plugin, State)) *Plugin {
	return &Plugin{
		Spec:    spec,
		Logger:  lg,
		handler: h,
		onState: onState,
		state:   StatePending,
		pending: make(map[string]chan *jsonrpc.Message),
		dead:    make(chan struct{}),
	}
}

// State 返回当前状态。
func (p *Plugin) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// SetState 更新状态并回调。
func (p *Plugin) SetState(s State) {
	p.mu.Lock()
	if p.state == s {
		p.mu.Unlock()
		return
	}
	p.state = s
	p.mu.Unlock()
	if p.onState != nil {
		p.onState(p, s)
	}
}

// Alive 判断子进程是否仍然存活。
func (p *Plugin) Alive() bool {
	select {
	case <-p.dead:
		return false
	default:
		return true
	}
}

func (p *Plugin) closeDead() {
	p.deadOnce.Do(func() { close(p.dead) })
}

// Start 启动子进程并开始读取 stdout / stderr。
func (p *Plugin) Start() error {
	cmd := exec.Command(p.Spec.Bin, p.Spec.Args...)
	cmd.Dir = p.Spec.Dir
	if len(p.Spec.Env) > 0 {
		cmd.Env = append(os.Environ(), p.Spec.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		p.closeDead()
		return fmt.Errorf("创建 stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.closeDead()
		return fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		p.closeDead()
		return fmt.Errorf("创建 stderr 管道失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		p.closeDead()
		return fmt.Errorf("启动子进程失败: %w", err)
	}

	p.mu.Lock()
	p.cmd = cmd
	p.stdin = stdin
	p.mu.Unlock()

	p.SetState(StateStarting)
	go p.readStdout(stdout)
	go p.readStderr(stderr)
	go p.wait(cmd)
	return nil
}

func (p *Plugin) readStdout(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		msg, err := jsonrpc.Decode(line)
		if err != nil {
			p.Logger.Plugin(p.Spec.ID, "error", "收到无法解析的消息: "+err.Error(),
				map[string]any{"raw": truncate(string(line), 512)})
			continue
		}
		p.dispatch(msg)
	}
}

func (p *Plugin) readStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line != "" {
			p.Logger.PluginStderr(p.Spec.ID, line)
		}
	}
}

func (p *Plugin) wait(cmd *exec.Cmd) {
	err := cmd.Wait()

	p.mu.Lock()
	p.stopped = true
	pending := p.pending
	p.pending = make(map[string]chan *jsonrpc.Message)
	p.mu.Unlock()

	p.closeDead()

	deadMsg := jsonrpc.NewErrorResponse(nil, jsonrpc.NewError(jsonrpc.CodeProviderDead, "插件 %s 进程已退出", p.Spec.ID))
	for _, ch := range pending {
		select {
		case ch <- deadMsg:
		default:
		}
	}

	reason := "正常退出"
	if err != nil {
		reason = err.Error()
	}
	p.Logger.Plugin(p.Spec.ID, "warn", "插件进程退出: "+reason, nil)
	p.SetState(StateDead)
}

func (p *Plugin) dispatch(msg *jsonrpc.Message) {
	switch {
	case msg.IsResponse():
		p.mu.Lock()
		ch := p.pending[msg.IDKey()]
		delete(p.pending, msg.IDKey())
		p.mu.Unlock()
		if ch != nil {
			select {
			case ch <- msg:
			default:
			}
		}
	case msg.IsRequest():
		go p.safeRequest(msg)
	default:
		go p.safeNotification(msg)
	}
}

func (p *Plugin) safeRequest(msg *jsonrpc.Message) {
	defer func() {
		if r := recover(); r != nil {
			p.Logger.Plugin(p.Spec.ID, "error", fmt.Sprintf("处理 %s 时 panic: %v", msg.Method, r), nil)
			p.RespondError(msg.ID, jsonrpc.NewError(jsonrpc.CodeInternalError, "核心处理 %s 时内部错误", msg.Method))
		}
	}()
	p.handler.HandleRequest(p, msg)
}

func (p *Plugin) safeNotification(msg *jsonrpc.Message) {
	defer func() {
		if r := recover(); r != nil {
			p.Logger.Plugin(p.Spec.ID, "error", fmt.Sprintf("处理通知 %s 时 panic: %v", msg.Method, r), nil)
		}
	}()
	p.handler.HandleNotification(p, msg)
}

// Send 把一条消息写入子进程 stdin。
func (p *Plugin) Send(m *jsonrpc.Message) error {
	data, err := jsonrpc.Encode(m)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdin == nil || p.stopped {
		return fmt.Errorf("插件 %s 未运行", p.Spec.ID)
	}
	if _, err := p.stdin.Write(data); err != nil {
		return fmt.Errorf("写入插件 %s 失败: %w", p.Spec.ID, err)
	}
	return nil
}

// Notify 发送通知（无 id）。
func (p *Plugin) Notify(method string, params any) error {
	return p.Send(jsonrpc.NewNotification(method, params))
}

// Respond 发送成功响应。
func (p *Plugin) Respond(id json.RawMessage, result any) {
	if err := p.Send(jsonrpc.NewResponse(id, result)); err != nil {
		p.Logger.Plugin(p.Spec.ID, "error", "回写响应失败: "+err.Error(), nil)
	}
}

// RespondError 发送错误响应。
func (p *Plugin) RespondError(id json.RawMessage, e *jsonrpc.Error) {
	if err := p.Send(jsonrpc.NewErrorResponse(id, e)); err != nil {
		p.Logger.Plugin(p.Spec.ID, "error", "回写错误响应失败: "+err.Error(), nil)
	}
}

func (p *Plugin) nextRequestID() json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	return json.RawMessage(strconv.FormatInt(p.nextID, 10))
}

// Call 向插件发起请求并等待响应。timeout <= 0 表示一直等待（直到插件死亡）。
func (p *Plugin) Call(method string, params any, timeout time.Duration) (*jsonrpc.Message, error) {
	return p.CallWithID(p.nextRequestID(), method, params, timeout)
}

// CallWithID 使用指定 id 发起请求。core.init 约定使用 id 0。
func (p *Plugin) CallWithID(id json.RawMessage, method string, params any, timeout time.Duration) (*jsonrpc.Message, error) {
	if !p.Alive() {
		return nil, fmt.Errorf("插件 %s 未运行", p.Spec.ID)
	}
	key := string(id)
	ch := make(chan *jsonrpc.Message, 1)

	p.mu.Lock()
	p.pending[key] = ch
	p.mu.Unlock()

	cleanup := func() {
		p.mu.Lock()
		delete(p.pending, key)
		p.mu.Unlock()
	}

	if err := p.Send(jsonrpc.NewRequest(id, method, params)); err != nil {
		cleanup()
		return nil, err
	}

	if timeout <= 0 {
		select {
		case m := <-ch:
			return m, nil
		case <-p.dead:
			cleanup()
			return nil, fmt.Errorf("插件 %s 进程已退出", p.Spec.ID)
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case m := <-ch:
		return m, nil
	case <-p.dead:
		cleanup()
		return nil, fmt.Errorf("插件 %s 进程已退出", p.Spec.ID)
	case <-timer.C:
		cleanup()
		return nil, fmt.Errorf("调用 %s 超时（%s）", method, timeout)
	}
}

// Stop 优雅停止插件：先发 core.shutdown，宽限期内未退出则强杀。
func (p *Plugin) Stop(grace time.Duration) {
	p.mu.Lock()
	cmd := p.cmd
	stopped := p.stopped
	p.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		p.closeDead()
		return
	}
	select {
	case <-p.dead:
		return
	default:
	}

	// 第一步：通知插件自己退出（规范 2.4 的 core.shutdown）
	if !stopped {
		_ = p.Notify("core.shutdown", map[string]any{"reason": "normal"})
	}
	if grace > 0 {
		select {
		case <-p.dead:
			return
		case <-time.After(grace):
		}
	}

	// 第二步：SIGTERM → 3 秒 → SIGKILL（规范 1.6 的退出流程）
	if cmd.Process != nil {
		_ = terminate(cmd.Process)
	}
	select {
	case <-p.dead:
		return
	case <-time.After(3 * time.Second):
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-p.dead:
	case <-time.After(3 * time.Second):
		p.Logger.Plugin(p.Spec.ID, "error", "插件进程未能在强杀后退出", nil)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(截断)"
}
