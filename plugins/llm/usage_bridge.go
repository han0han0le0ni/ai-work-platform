package main

import (
	"encoding/json"
	"strings"
	"time"

	"aiwork/sdk/go/plugin"
)

// 本文件是 llm 插件的「计量桥」。
//
// 为什么需要它：缓存命中率 = 命中的输入 token / 全部输入 token，而这两个数
// （prompt_cache_hit_tokens / prompt_cache_miss_tokens）只出现在 DeepSeek 的
// **响应体**里。默认的 openai provider 只把三个总量字段解析进 chatResp.Usage，
// 命中信息在解析那一步就丢了；等 chat() 返回到 agent / 界面时，
// 已经没有任何地方能再把它捞回来。
//
// 所以这里把 openai 这个 provider 换成一个"同一件事、顺手报数"的实现：
// 发一样的请求，解析完整的 usage（含命中 / 未命中），再经事件总线
// emit 一条 llm.usage，由 com.official.usage 订阅并累计。
//
// 想回到原实现：configure {"provider":"openai-legacy"}。
func init() {
	if providers == nil {
		return
	}
	orig, ok := providers["openai"]
	if !ok {
		return
	}
	providers["openai-legacy"] = orig
	providers["openai"] = openaiChatMetered
}

// ---------------------------------------------------------------------------
// 请求体
// ---------------------------------------------------------------------------

func openaiChatMetered(l *LLM, req *chatReq, cfg *settings) (*chatResp, error) {
	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"

	body := map[string]any{
		"model":    cfg.Model,
		"messages": meteredMessages(req.Messages, req.System),
		"stream":   false,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		body["tools"] = meteredTools(req.Tools)
	}
	if req.ReasoningEffort != "" {
		body["reasoning_effort"] = req.ReasoningEffort
	}

	headers := map[string]string{}
	if cfg.APIKey != "" {
		headers["Authorization"] = "Bearer " + cfg.APIKey
	}

	status, raw, err := l.postJSON(url, body, headers, cfg.TimeoutMs)
	if err != nil {
		meterEmit(l, cfg, nil, false, err.Error())
		return nil, err
	}
	if status < 200 || status >= 300 {
		msg := meteredClip(raw, 300)
		meterEmit(l, cfg, nil, false, msg)
		if status == 401 || status == 403 {
			return nil, plugin.Errorf(plugin.CodeInternalError,
				"接口拒绝了这个密钥（HTTP %d）：检查 API Key 是否填对%s",
				status, authHint(status, cfg.BaseURL))
		}
		return nil, plugin.Errorf(plugin.CodeInternalError, "模型调用失败（HTTP %d）：%s", status, msg)
	}

	var out meteredResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		meterEmit(l, cfg, nil, false, err.Error())
		return nil, plugin.Errorf(plugin.CodeInternalError, "解析模型响应失败: %v", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		meterEmit(l, cfg, nil, false, out.Error.Message)
		return nil, plugin.Errorf(plugin.CodeInternalError, "模型返回错误：%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		meterEmit(l, cfg, nil, false, "响应里没有 choices")
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"模型响应里没有 choices：%s", meteredClip(raw, 200))
	}

	choice := out.Choices[0]
	resp := &chatResp{
		Content:      choice.Message.Content,
		Reasoning:    choice.Message.ReasoningContent,
		FinishReason: choice.FinishReason,
		Usage: usage{
			PromptTokens:     out.Usage.PromptTokens,
			CompletionTokens: out.Usage.CompletionTokens,
			TotalTokens:      out.Usage.TotalTokens,
		},
	}
	for _, tc := range choice.Message.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if len(args) == 0 || !json.Valid(args) {
			args = json.RawMessage("{}")
		}
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: args,
		})
	}

	meterEmit(l, cfg, &out.Usage, true, "")
	return resp, nil
}

// meteredMessages 把内部消息翻译成 OpenAI 的 messages。
//
// 与"只发字符串"的老路径兼容：没有 Parts 就是普通文本；
// 有 Parts（多模态）时发内容数组。
func meteredMessages(msgs []Message, system string) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	if strings.TrimSpace(system) != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	for _, m := range msgs {
		item := map[string]any{"role": m.Role}

		if len(m.Parts) == 0 {
			item["content"] = m.Content
		} else {
			parts := make([]map[string]any, 0, len(m.Parts)+1)
			if m.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": m.Content})
			}
			for _, p := range m.Parts {
				switch p.Type {
				case "text":
					parts = append(parts, map[string]any{"type": "text", "text": p.Text})
				case "image":
					parts = append(parts, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": p.ImageURL},
					})
				}
			}
			item["content"] = parts
		}

		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				args := strings.TrimSpace(string(tc.Args))
				if args == "" {
					args = "{}"
				}
				calls = append(calls, map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": args,
					},
				})
			}
			item["tool_calls"] = calls
		}
		if m.ToolCallID != "" {
			item["tool_call_id"] = m.ToolCallID
		}
		if m.Name != "" {
			item["name"] = m.Name
		}
		out = append(out, item)
	}
	return out
}

func meteredTools(tools []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		var params any = json.RawMessage(`{"type":"object"}`)
		if len(t.Schema) > 0 && json.Valid(t.Schema) {
			params = json.RawMessage(t.Schema)
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  params,
			},
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// 响应体
// ---------------------------------------------------------------------------

type meteredUsage struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
}

type meteredResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			// ReasoningContent 是思考模型的思考过程。计量桥替换的是默认 provider，
			// 这里不解析的话，界面就永远看不到 AI 在想什么。
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage meteredUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// ---------------------------------------------------------------------------
// 上报
// ---------------------------------------------------------------------------

// meterEmit 发一条 llm.usage。没有订阅者时事件被核心丢弃，无副作用。
func meterEmit(l *LLM, cfg *settings, u *meteredUsage, ok bool, errMsg string) {
	if l == nil || l.ctx == nil || cfg == nil {
		return
	}

	payload := map[string]any{
		"provider": cfg.Provider,
		"model":    cfg.Model,
		"ok":       ok,
		"at":       meteredNow(),
	}
	if u != nil {
		payload["promptTokens"] = u.PromptTokens
		payload["completionTokens"] = u.CompletionTokens
		payload["totalTokens"] = u.TotalTokens
		payload["cacheHitTokens"] = u.PromptCacheHitTokens
		payload["cacheMissTokens"] = u.PromptCacheMissTokens
	}
	if errMsg != "" {
		payload["err"] = meteredClip(errMsg, 200)
	}

	_ = l.ctx.Emit("llm.usage", payload)
}

func meteredNow() string { return time.Now().Format("15:04:05") }

func meteredClip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
