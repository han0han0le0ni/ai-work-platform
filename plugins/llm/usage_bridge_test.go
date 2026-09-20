package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// 计量桥必须真的把 openai 换成"同一件事、顺手报数"的实现，
// 并且把原实现留成 openai-legacy 让人能退回去。
//
// 这两个断言盯的是一件会静默失效的事：init 里换掉了 providers["openai"]，
// 但换完之后的 provider 走得通、报得准，只有真跑一次才知道。
func TestMeteredProviderInstalled(t *testing.T) {
	metered, okMetered := providers["openai"]
	if !okMetered {
		t.Fatal("providers 里没有 openai")
	}
	legacy, okLegacy := providers["openai-legacy"]
	if !okLegacy {
		t.Fatal("计量桥没有把原实现留成 openai-legacy —— 装了桥就回不去了")
	}
	if reflect.ValueOf(metered).Pointer() == reflect.ValueOf(legacy).Pointer() {
		t.Fatal("openai 与 openai-legacy 指向同一个实现，计量桥没有接管")
	}
}

// 命中 / 未命中的 token 只在响应体里出现一次，解析漏了事后无法补算。
func TestMeteredResponseCarriesCacheTokens(t *testing.T) {
	raw := `{
	  "choices": [{"message": {"content": "你好"}, "finish_reason": "stop"}],
	  "usage": {
	    "prompt_tokens": 1000,
	    "completion_tokens": 80,
	    "total_tokens": 1080,
	    "prompt_cache_hit_tokens": 600,
	    "prompt_cache_miss_tokens": 400
	  }
	}`
	var out meteredResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if out.Usage.PromptTokens != 1000 || out.Usage.CompletionTokens != 80 || out.Usage.TotalTokens != 1080 {
		t.Fatalf("三个总量字段解析不对：%+v", out.Usage)
	}
	if out.Usage.PromptCacheHitTokens != 600 || out.Usage.PromptCacheMissTokens != 400 {
		t.Fatalf("缓存命中 / 未命中丢失：%+v", out.Usage)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "你好" {
		t.Fatalf("choices 解析不对：%+v", out.Choices)
	}
}

// 换实现不能换行为：多模态与工具调用都得照原样发出去。
func TestMeteredMessagesAndTools(t *testing.T) {
	msgs := meteredMessages([]Message{
		{Role: "user", Content: "这张图里有什么"},
		{Role: "user", Content: "看图", Parts: []Part{
			{Type: "text", Text: "描述它"},
			{Type: "image", ImageURL: "data:image/png;base64,AAAA"},
		}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Args: json.RawMessage(`{"path":"a.txt"}`)}}},
		{Role: "tool", ToolCallID: "c1", Name: "read_file", Content: "文件内容"},
	}, "你是助手")

	if len(msgs) != 5 {
		t.Fatalf("应含 system 共 5 条，实际 %d", len(msgs))
	}
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "你是助手" {
		t.Errorf("system 提示词没带上：%+v", msgs[0])
	}
	if msgs[1]["content"] != "这张图里有什么" {
		t.Errorf("纯文本消息应保持字符串：%+v", msgs[1])
	}
	parts, ok := msgs[2]["content"].([]map[string]any)
	if !ok || len(parts) != 3 {
		t.Fatalf("多模态应发 3 段内容（正文 + 文字 + 图片）：%+v", msgs[2])
	}
	if parts[2]["type"] != "image_url" {
		t.Errorf("图片应发成 image_url：%+v", parts[2])
	}
	calls, ok := msgs[3]["tool_calls"].([]map[string]any)
	if !ok || len(calls) != 1 || calls[0]["type"] != "function" {
		t.Fatalf("工具调用没发出去：%+v", msgs[3])
	}
	if msgs[4]["tool_call_id"] != "c1" {
		t.Errorf("工具结果没有关联 id：%+v", msgs[4])
	}

	tools := meteredTools([]ToolSpec{
		{Name: "read_file", Description: "读文件", Schema: json.RawMessage(`{"type":"object"}`)},
		{Name: "broken", Schema: json.RawMessage(`{`)},
	})
	if len(tools) != 2 {
		t.Fatalf("工具数不对：%d", len(tools))
	}
	params, _ := tools[1]["function"].(map[string]any)["parameters"].(json.RawMessage)
	if string(params) != `{"type":"object"}` {
		t.Errorf("非法 schema 应回落到空 object：%+v", tools[1])
	}
}

// 上报是尽力而为：没有核心连接时不能连累模型调用。
func TestMeterEmitIsBestEffort(t *testing.T) {
	meterEmit(nil, nil, nil, false, "no core")
	meterEmit(&LLM{}, &settings{Provider: "openai", Model: "deepseek-chat"}, nil, false, "boom")
	meterEmit(&LLM{}, &settings{Provider: "openai"}, &meteredUsage{PromptTokens: 1}, true, "")
}
