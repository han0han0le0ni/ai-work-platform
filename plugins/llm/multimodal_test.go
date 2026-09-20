package main

import (
	"encoding/json"
	"testing"
)

// 没有图片时必须还是字符串：老调用方与不支持多模态的模型不能被牵连
func TestOpenAIContentStaysPlainWithoutImages(t *testing.T) {
	got := openAIContent(Message{Role: "user", Content: "你好"})
	if s, ok := got.(string); !ok || s != "你好" {
		t.Fatalf("无图片时 content 应是字符串，实际 %#v", got)
	}
}

// 有图片时改成多模态数组，文字在前、图片在后
func TestOpenAIContentPartsWithImage(t *testing.T) {
	got := openAIContent(Message{
		Role:    "user",
		Content: "这是什么？",
		Parts: []Part{
			{Type: "text", Text: "这是什么？"},
			{Type: "image", ImageURL: "data:image/png;base64,AAAA"},
		},
	})
	parts, ok := got.([]map[string]any)
	if !ok {
		t.Fatalf("有图片时 content 应是数组，实际 %#v", got)
	}
	if len(parts) != 2 {
		t.Fatalf("应包含 文本 + 图片 两段，实际 %d 段: %#v", len(parts), parts)
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "这是什么？" {
		t.Fatalf("第一段应是文本: %#v", parts[0])
	}
	if parts[1]["type"] != "image_url" {
		t.Fatalf("第二段应是 image_url: %#v", parts[1])
	}
	inner, ok := parts[1]["image_url"].(map[string]any)
	if !ok || inner["url"] != "data:image/png;base64,AAAA" {
		t.Fatalf("图片地址不正确: %#v", parts[1])
	}
}

// Ollama 要的是裸 base64，不吃 data: 前缀
func TestOllamaImagesStripDataPrefix(t *testing.T) {
	got := ollamaImages(Message{
		Parts: []Part{
			{Type: "image", ImageURL: "data:image/png;base64,QUJD"},
			{Type: "text", Text: "忽略我"},
		},
	})
	if len(got) != 1 || got[0] != "QUJD" {
		t.Fatalf("应剥掉 data: 前缀，实际 %#v", got)
	}
}

// Ollama 不支持远程图片，别把它塞进 images
func TestOllamaImagesSkipsRemote(t *testing.T) {
	got := ollamaImages(Message{
		Parts: []Part{{Type: "image", ImageURL: "https://example.com/a.png"}},
	})
	if len(got) != 0 {
		t.Fatalf("远程图片应被跳过，实际 %#v", got)
	}
}

// 整条翻译链路：user 消息带图片时，OpenAI 出数组、Ollama 出 images 字段
func TestToWireMessagesCarryImages(t *testing.T) {
	msgs := []Message{{
		Role:    "user",
		Content: "看图说话",
		Parts: []Part{
			{Type: "text", Text: "看图说话"},
			{Type: "image", ImageURL: "data:image/jpeg;base64,ZZZZ"},
		},
	}}

	oa := toOpenAIMessages(msgs, "")
	blob, _ := json.Marshal(oa)
	if !json.Valid(blob) {
		t.Fatal("翻译结果不是合法 JSON")
	}
	var decoded []map[string]any
	_ = json.Unmarshal(blob, &decoded)
	found := false
	for _, m := range decoded {
		if m["role"] == "user" {
			if _, ok := m["content"].([]any); ok {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("OpenAI 的 user 消息 content 应是数组: %s", blob)
	}

	ol := toOllamaMessages(msgs, "")
	for _, m := range ol {
		if m["role"] == "user" {
			imgs, ok := m["images"].([]string)
			if !ok || len(imgs) != 1 || imgs[0] != "ZZZZ" {
				t.Fatalf("Ollama 的 user 消息应带 images: %#v", m)
			}
			return
		}
	}
	t.Fatal("没找到 user 消息")
}

// 入参里的 parts 要能被解析出来（agent/UI 传 JSON 进来）
func TestDecodeMessagesKeepsParts(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","content":"看图","parts":[
		{"type":"text","text":"看图"},
		{"type":"image","imageUrl":"data:image/png;base64,QQ"}
	]}]`)
	msgs, err := decodeMessages(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(msgs) != 1 || len(msgs[0].Parts) != 2 {
		t.Fatalf("parts 应被保留: %#v", msgs)
	}
	if msgs[0].Parts[1].ImageURL != "data:image/png;base64,QQ" {
		t.Fatalf("图片地址丢失: %#v", msgs[0].Parts[1])
	}
}

// 坏 JSON 要报错，不能静默吞掉
func TestDecodeMessagesRejectsBadParts(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","parts":"not-an-array"}]`)
	if _, err := decodeMessages(raw); err == nil {
		t.Fatal("parts 非法时应报错")
	}
}
