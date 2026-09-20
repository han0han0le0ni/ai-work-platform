package main

import (
	"strings"
	"testing"
)

// 正常有正文：什么都不该拦
func TestEmptyReplyPassesWithContent(t *testing.T) {
	if err := explainEmptyReply("这是回答", false, "stop", "", 200); err != nil {
		t.Fatalf("有正文时不该报错: %v", err)
	}
}

// 模型要调工具时 content 本来就是空的 —— 这是正常情况，不能当空回复拦掉
func TestEmptyReplyAllowsToolCalls(t *testing.T) {
	if err := explainEmptyReply("", true, "tool_calls", "", 200); err != nil {
		t.Fatalf("模型要调工具时不该报错: %v", err)
	}
}

// 思考吃光 token 预算：要说清楚是截断，并给出可操作的办法
func TestEmptyReplyExplainsTruncation(t *testing.T) {
	err := explainEmptyReply("", false, "length", "让我想想…", 200)
	if err == nil {
		t.Fatal("思考占满预算导致空回复时应报错")
	}
	if !strings.Contains(err.Error(), "maxTokens") {
		t.Fatalf("提示里要给出可操作的办法，实际: %v", err)
	}
}

// 只有思考没有正文：把思考摘录出来，别让用户面对空白
func TestEmptyReplySurfacesReasoning(t *testing.T) {
	err := explainEmptyReply("", false, "stop", "我在分析这张图的颜色分布…", 200)
	if err == nil {
		t.Fatal("只有思考内容时应报错")
	}
	if !strings.Contains(err.Error(), "颜色分布") {
		t.Fatalf("应把思考内容带出来给用户看，实际: %v", err)
	}
}

// 什么都没有：也要说清楚，不能返回空
func TestEmptyReplyExplainsBlank(t *testing.T) {
	err := explainEmptyReply("   ", false, "stop", "", 200)
	if err == nil {
		t.Fatal("彻底空回复时应报错")
	}
	if !strings.Contains(err.Error(), "空回复") {
		t.Fatalf("提示不对: %v", err)
	}
}
