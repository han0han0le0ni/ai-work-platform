package main

import (
	"strings"
	"testing"
)

// 附件内容绝不能落进会话树。
//
// 一张图的 base64 动辄几 MB，存进会话文件会把树撑爆、翻页变卡，
// 而且下几轮再问时模型也未必还需要那张图。树里只留文件名。
func TestWithAttachNoteKeepsOnlyNames(t *testing.T) {
	huge := strings.Repeat("A", 10000)
	got := withAttachNote("看图", []attachment{
		{Type: "image", Name: "cat.png", DataURL: "data:image/png;base64," + huge},
		{Type: "text", Name: "notes.txt", Text: huge},
	})

	if strings.Contains(got, huge) {
		t.Fatal("节点文本里不该出现附件内容（会把会话文件撑爆）")
	}
	if !strings.HasPrefix(got, "看图") {
		t.Fatalf("原文要保留在开头，实际：%q", got[:min(20, len(got))])
	}
	for _, want := range []string{"cat.png", "notes.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("应记下附件名 %s，实际：%s", want, got)
		}
	}
}

// 没有附件时一个字都不能改：这条路径每天都在跑
func TestWithAttachNoteNoop(t *testing.T) {
	if got := withAttachNote("你好", nil); got != "你好" {
		t.Fatalf("无附件时应原样返回，实际 %q", got)
	}
}

// 附件没名字时不能输出 "[附件: ]" 这种残句
func TestWithAttachNoteUnnamed(t *testing.T) {
	got := withAttachNote("看", []attachment{{Type: "file"}})
	if strings.Contains(got, "[附件: ]") {
		t.Fatalf("应有兜底称呼，实际 %q", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
