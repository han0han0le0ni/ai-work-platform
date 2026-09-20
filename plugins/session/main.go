// Command session 是官方会话插件（provides: session.v1）。
//
// 它是界面聊天入口的第一站，负责「记住上一句」：
//
//	UI → session.v1.sendMessage → agent.v1.run（带完整历史）→ llm / 工具 → 回答 → 存历史
//
// 三个刻意的设计取舍：
//
//  1. **降级不报错**：没有 agent.v1 就直接走 llm.v1 纯聊天；两个都没有才报错，
//     并在错误里说清楚该装哪个插件。
//  2. **失败不留痕**：这一轮失败就把刚加进去的 user 消息撤掉，
//     免得下一轮上下文里挂着一句没人回的话。
//  3. **上下文有界**：历史按条数裁剪后存进插件配置，长会话不会无限膨胀。
package main

import (
	"encoding/json"
	"os"
	"strings"
	"sync"

	"aiwork/sdk/go/plugin"
)

const serviceName = "session.v1"

// Session 实现 plugin.Plugin。
//
// 每个会话独立加锁：一个长会话在推理时，不该挡住另一个会话读历史。
type Session struct {
	ctx *plugin.Context

	mu     sync.Mutex
	convos map[string]*Conversation
}

// Activate 注册 session.v1 并载入历史。
func (s *Session) Activate(ctx *plugin.Context) error {
	s.ctx = ctx
	s.convos = map[string]*Conversation{}

	if err := ctx.Provide(serviceName, map[string]any{
		"description": "会话：维护多轮对话历史，把消息交给 agent 处理",
		"methods":     []string{"sendMessage", "history", "clear", "list", "system", "info"},
	}); err != nil {
		return err
	}

	if err := s.load(); err != nil {
		ctx.Log("warn", "读取会话历史失败（将从空历史开始）："+err.Error(), nil)
	}

	s.mu.Lock()
	count := len(s.convos)
	s.mu.Unlock()

	ctx.Log("info", "会话插件已就绪", map[string]any{
		"sessions": count,
		"hasAgent": consumeOK(ctx, "agent.v1"),
		"hasLLM":   consumeOK(ctx, "llm.v1"),
		"maxTurns": ctx.GetConfigInt("maxTurns", 16),
	})
	return nil
}

// Handle 处理 core.invoke。
func (s *Session) Handle(service, method string, args json.RawMessage) (any, error) {
	if service != serviceName {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "会话插件不提供服务 %s", service)
	}
	switch method {
	case "sendMessage":
		return s.sendMessage(args)
	case "history":
		return s.history(args)
	case "clear":
		return s.clear(args)
	case "list":
		return map[string]any{"sessions": s.summaries()}, nil
	case "system":
		return s.setSystem(args)
	case "info":
		return s.info(), nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "session.v1 不支持方法 %s", method)
	}
}

// Deactivate 清理：把内存里的会话再写一次，避免异常退出丢掉最后一轮。
func (s *Session) Deactivate() error {
	if s.ctx == nil {
		return nil
	}
	if err := s.save(); err != nil {
		s.ctx.Log("warn", "退出前保存会话失败："+err.Error(), nil)
	}
	s.ctx.Log("info", "会话插件退出", nil)
	return nil
}

// ---------------------------------------------------------------------------
// sendMessage：唯一会改变状态的入口
// ---------------------------------------------------------------------------

type sendParams struct {
	Text      string `json:"text"`
	SessionID string `json:"sessionId,omitempty"`
	System    string `json:"system,omitempty"`
	// Attachments 是本次附上的文件 / 图片。
	//
	// 它们**只参与这一次请求，不进历史**：一张图的 base64 动辄几 MB，
	// 存进 plugin-configs 会把配置文件撑爆，而且下一轮再问时模型也未必还需要看它。
	// 历史里保留的是"用户问了什么"，附件则记一行文字说明，人能看懂就够了。
	Attachments []attachment `json:"attachments,omitempty"`
}

type attachment struct {
	Type    string `json:"type"`              // text / image / file
	Name    string `json:"name,omitempty"`    // 文件名
	Text    string `json:"text,omitempty"`    // type=text：文件内容
	DataURL string `json:"dataUrl,omitempty"` // type=image：data:image/png;base64,xxx
	Size    int64  `json:"size,omitempty"`
}

func (s *Session) sendMessage(args json.RawMessage) (any, error) {
	var in sendParams
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 text")
	}

	id := s.sessionID(in.SessionID)

	s.mu.Lock()
	convo := s.get(id)
	if sys := strings.TrimSpace(in.System); sys != "" {
		convo.System = sys
	}
	// 历史里存的是"问了什么 + 附了什么"，不是附件的二进制本身
	convo.Messages = append(convo.Messages, Turn{
		Role: "user", Content: displayText(text, in.Attachments), At: nowISO(),
	})
	history := append([]Turn{}, convo.Messages...)
	system := convo.System
	s.mu.Unlock()

	reply, via, tools, err := s.dispatch(history, system, in.Attachments)
	if err != nil {
		// 失败不留痕：撤掉刚加进去的 user 消息
		s.mu.Lock()
		if n := len(convo.Messages); n > 0 &&
			convo.Messages[n-1].Role == "user" && convo.Messages[n-1].Content == text {
			convo.Messages = convo.Messages[:n-1]
		}
		s.mu.Unlock()
		return nil, err
	}

	s.mu.Lock()
	convo.Messages = append(convo.Messages, Turn{Role: "assistant", Content: reply, At: nowISO()})
	convo.Updated = nowISO()
	s.trim(convo)
	turns := len(convo.Messages)
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.ctx.Log("warn", "保存会话历史失败："+err.Error(), nil)
	}
	_ = s.ctx.Emit("session.updated", map[string]any{"sessionId": id, "turns": turns})

	out := map[string]any{
		"content":   reply,
		"sessionId": id,
		"turns":     turns,
		"via":       via,
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	return out, nil
}

// dispatch 按「agent → llm」的顺序探测可用链路。
//
// 核心不关心缺哪个插件，会话也不关心；它只负责把能用的用起来。
// 注意：agent 报错就原样返回，不再往 llm 降级 —— 那是一次真实失败，
// displayText 生成存进历史的那一行：正文 + 附件名字。
//
// 只记名字不记内容，所以历史文件始终很小，人翻记录时也能看出"当时附了什么"。
func displayText(text string, atts []attachment) string {
	if len(atts) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	for _, a := range atts {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = "附件"
		}
		b.WriteString("\n[附件: " + name + "]")
	}
	return b.String()
}

// 悄悄换成另一次调用只会让问题更难查。
func (s *Session) dispatch(history []Turn, system string, atts []attachment) (string, string, []string, error) {
	msgs := make([]message, 0, len(history))
	for _, t := range history {
		msgs = append(msgs, message{Role: t.Role, Content: t.Content})
	}
	params := map[string]any{"messages": msgs}
	if strings.TrimSpace(system) != "" {
		params["system"] = system
	}
	if len(atts) > 0 {
		params["attachments"] = atts
	}

	if _, err := s.ctx.Consume("agent.v1"); err == nil {
		raw, err := s.ctx.Invoke("agent.v1", "run", params)
		if err != nil {
			return "", "agent.v1", nil, err
		}
		var out struct {
			Content string   `json:"content"`
			Tools   []string `json:"tools"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", "agent.v1", nil, plugin.Errorf(plugin.CodeInternalError, "解析 agent.v1 响应失败: %v", err)
		}
		return out.Content, "agent.v1", out.Tools, nil
	}

	if _, err := s.ctx.Consume("llm.v1"); err == nil {
		raw, err := s.ctx.Invoke("llm.v1", "chat", params)
		if err != nil {
			return "", "llm.v1", nil, err
		}
		var out struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", "llm.v1", nil, plugin.Errorf(plugin.CodeInternalError, "解析 llm.v1 响应失败: %v", err)
		}
		return out.Content, "llm.v1", nil, nil
	}

	return "", "", nil, plugin.Errorf(plugin.CodeServiceNotFound,
		"会话需要 agent.v1 或 llm.v1，但两个都不可用。"+
			"最省事的装法：com.official.llm（提供模型）+ com.official.agent（提供编排）")
}

// ---------------------------------------------------------------------------
// 历史管理
// ---------------------------------------------------------------------------

func (s *Session) history(args json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId,omitempty"`
		Limit     int    `json:"limit,omitempty"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in)
	}
	id := s.sessionID(in.SessionID)

	s.mu.Lock()
	convo := s.get(id)
	turns := append([]Turn{}, convo.Messages...)
	system := convo.System
	updated := convo.Updated
	s.mu.Unlock()

	if in.Limit > 0 && len(turns) > in.Limit {
		turns = turns[len(turns)-in.Limit:]
	}
	return map[string]any{
		"sessionId": id,
		"system":    system,
		"turns":     len(turns),
		"messages":  turns,
		"updated":   updated,
	}, nil
}

func (s *Session) clear(args json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId,omitempty"`
		All       bool   `json:"all,omitempty"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in)
	}

	if in.All {
		s.mu.Lock()
		n := 0
		for id := range s.convos {
			s.convos[id] = &Conversation{Messages: []Turn{}}
			n++
		}
		s.mu.Unlock()
		if err := s.save(); err != nil {
			s.ctx.Log("warn", "保存失败："+err.Error(), nil)
		}
		return map[string]any{"ok": true, "cleared": n, "scope": "all"}, nil
	}

	id := s.sessionID(in.SessionID)
	s.mu.Lock()
	convo := s.get(id)
	cleared := len(convo.Messages)
	convo.Messages = []Turn{}
	convo.Updated = nowISO()
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.ctx.Log("warn", "保存失败："+err.Error(), nil)
	}
	_ = s.ctx.Emit("session.updated", map[string]any{"sessionId": id, "turns": 0})
	return map[string]any{"ok": true, "sessionId": id, "cleared": cleared}, nil
}

func (s *Session) setSystem(args json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId,omitempty"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	id := s.sessionID(in.SessionID)

	s.mu.Lock()
	convo := s.get(id)
	convo.System = in.Text
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.ctx.Log("warn", "保存失败："+err.Error(), nil)
	}
	return map[string]any{"ok": true, "sessionId": id, "system": in.Text}, nil
}

func (s *Session) info() map[string]any {
	// summaries 自带加锁：这里不能再持锁，否则 sync.Mutex 不可重入会自锁死
	return map[string]any{
		"defaultSession": s.sessionID(""),
		"maxTurns":       s.ctx.GetConfigInt("maxTurns", 16),
		"sessions":       s.summaries(),
		"hasAgent":       consumeOK(s.ctx, "agent.v1"),
		"hasLLM":         consumeOK(s.ctx, "llm.v1"),
	}
}

func consumeOK(ctx *plugin.Context, service string) bool {
	_, err := ctx.Consume(service)
	return err == nil
}

func main() {
	if err := plugin.Serve(&Session{}); err != nil {
		plugin.Logf("error", "会话插件退出: %v", err)
		os.Exit(1)
	}
}
