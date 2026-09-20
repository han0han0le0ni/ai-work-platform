package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// 思考内容要能从 llm 的响应一路带到编排层：链路上少一个字段，
// 界面上的「它在想什么」就永远是空的 —— 而且不会报错，只会静默变空。
func TestChatResultCarriesReasoning(t *testing.T) {
	raw := `{
	  "content": "我来查一下",
	  "toolCalls": [],
	  "finishReason": "tool_calls",
	  "provider": "openai",
	  "model": "deepseek-chat",
	  "reasoning": "用户想看 fs-tool 的权限声明，先读 manifest.json"
	}`
	var out llmChatResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if out.Reasoning == "" {
		t.Fatal("reasoning 字段没解析出来 —— 界面上就看不到思考内容了")
	}
	if !strings.Contains(out.Reasoning, "manifest.json") {
		t.Errorf("思考内容不对：%q", out.Reasoning)
	}
	if out.Content != "我来查一下" {
		t.Errorf("正文被 reasoning 顶掉了：%q", out.Content)
	}
}

// 事件是广播的：思考再长也得先裁一刀，不能整篇丢上总线。
func TestClipForView(t *testing.T) {
	if got := clipForView("  先看看目录  "); got != "先看看目录" {
		t.Errorf("应去掉首尾空白，实际 %q", got)
	}
	if got := clipForView(""); got != "" {
		t.Errorf("空思考应保持空，实际 %q", got)
	}
	short := strings.Repeat("想", viewClipRunes)
	if got := clipForView(short); got != short {
		t.Errorf("刚好到上限不该截断，实际长度 %d", len([]rune(got)))
	}
	long := clipForView(short + "多出来的一段")
	if !strings.HasSuffix(long, "…") {
		t.Errorf("超长应以省略号收尾：%q", long[len(long)-8:])
	}
	if got := len([]rune(long)); got != viewClipRunes+1 {
		t.Errorf("超长应截到 %d+1 个字符，实际 %d", viewClipRunes, got)
	}
}
