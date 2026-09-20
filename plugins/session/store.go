package main

import (
	"encoding/json"
	"sort"
	"time"

	"aiwork/sdk/go/plugin"
)

// configKeySessions 是会话历史在插件配置里的键名。
//
// 用 core.setConfig 存，等于把「往哪存、怎么存」交给核心的配置层：
// 落在 runtime/plugin-configs/com.official.session.json，格式化时会随插件配置一起清空，
// 这正是我们想要的语义 —— 格式化就是回到出厂状态。
const configKeySessions = "sessions"

// Turn 是一轮对话。
//
// 只存 user 和 assistant 的最终文本：agent 内部的 assistant/tool 往返属于中间过程，
// 存下来只会让下一轮的上下文变长、变乱。
type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	At      string `json:"at"`
}

// Conversation 是一个会话。
type Conversation struct {
	System   string `json:"system,omitempty"`
	Messages []Turn `json:"messages"`
	Updated  string `json:"updated,omitempty"`
}

// message 是发给 agent.v1 / llm.v1 的消息（与它们约定的内部格式一致）。
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (s *Session) load() error {
	raw, err := s.ctx.GetConfig(configKeySessions)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var saved map[string]*Conversation
	if err := json.Unmarshal(raw, &saved); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, c := range saved {
		if c == nil {
			continue
		}
		s.convos[id] = c
	}
	return nil
}

// save 把全部会话写回插件配置。
//
// 会话键只增不减：clear 会把某个会话的 messages 清空，但保留这个会话本身，
// 免得界面上的「换个新会话」把配置键也一起抹掉。
func (s *Session) save() error {
	s.mu.Lock()
	snapshot := make(map[string]Conversation, len(s.convos))
	for id, c := range s.convos {
		snapshot[id] = *c
	}
	s.mu.Unlock()
	return s.ctx.SetConfig(configKeySessions, snapshot)
}

// get 取（或新建）一个会话。调用方必须持有 s.mu。
func (s *Session) get(id string) *Conversation {
	c, ok := s.convos[id]
	if !ok || c == nil {
		c = &Conversation{Messages: []Turn{}}
		s.convos[id] = c
	}
	if c.Messages == nil {
		c.Messages = []Turn{}
	}
	return c
}

// trim 按条数裁剪历史。调用方必须持有 s.mu。
//
// 裁剪后尽量让历史从 user 开始：否则模型会收到一串「凭空冒出的 assistant 回复」，
// 上下文会变得难以理解。
func (s *Session) trim(c *Conversation) {
	limit := s.ctx.GetConfigInt("maxTurns", 16)
	if limit <= 0 {
		limit = 16
	}
	if len(c.Messages) <= limit {
		return
	}
	c.Messages = append([]Turn{}, c.Messages[len(c.Messages)-limit:]...)
	if len(c.Messages) > 1 && c.Messages[0].Role != "user" {
		c.Messages = c.Messages[1:]
	}
}

// sessionID 解析会话 ID：请求里指定 > 配置的默认值 > "default"。
func (s *Session) sessionID(requested string) string {
	if id := sanitizeID(requested); id != "" {
		return id
	}
	if id := sanitizeID(s.ctx.GetConfigString("defaultSession", "")); id != "" {
		return id
	}
	return "default"
}

// sanitizeID 只保留安全的字符，避免会话 ID 变成奇怪的东西。
func sanitizeID(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			out = append(out, r)
		case r == ' ':
			out = append(out, '-')
		}
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

// summaries 返回全部会话的概要（按会话 ID 排序，输出稳定）。
func (s *Session) summaries() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.convos))
	for id, c := range s.convos {
		out = append(out, map[string]any{
			"sessionId": id,
			"turns":     len(c.Messages),
			"updated":   c.Updated,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["sessionId"].(string) < out[j]["sessionId"].(string)
	})
	return out
}

func nowISO() string { return time.Now().Format(time.RFC3339) }

// reasonOf 把「服务不可用」翻译成一句人能看懂的原因。
func reasonOf(err error) string {
	switch {
	case err == nil:
		return ""
	case plugin.IsCode(err, plugin.CodeServiceNotFound):
		return "插件未安装"
	case plugin.IsCode(err, plugin.CodeProviderDead):
		return "提供者已退出"
	case plugin.IsCode(err, plugin.CodeDependencyMissing):
		return "依赖缺失"
	case plugin.IsCode(err, plugin.CodeAPIVersionIncompatible):
		return "版本不兼容"
	default:
		return err.Error()
	}
}
