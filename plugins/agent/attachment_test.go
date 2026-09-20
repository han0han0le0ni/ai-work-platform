package main

import (
	"strings"
	"testing"
)

// 文本附件：内容要真的进正文，模型才读得到
func TestAttachTextFileIntoContent(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "看看这个文件"}}
	got := attachToLastUser(msgs, []Attachment{
		{Type: "text", Name: "notes.md", Text: "# 标题\n正文内容", Size: 20},
	})

	if len(got) != 1 {
		t.Fatalf("不该新增消息，实际 %d 条", len(got))
	}
	if !strings.Contains(got[0].Content, "看看这个文件") {
		t.Fatal("原问题要保留")
	}
	if !strings.Contains(got[0].Content, "notes.md") {
		t.Fatal("要带上文件名，否则模型不知道这段内容是哪来的")
	}
	if !strings.Contains(got[0].Content, "# 标题") {
		t.Fatal("文件内容要真的进正文")
	}
}

// 图片附件：要走 parts 让模型真的"看见"，而不是塞进正文当乱码
func TestAttachImageBecomesPart(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "这是什么？"}}
	got := attachToLastUser(msgs, []Attachment{
		{Type: "image", Name: "cat.png", DataURL: "data:image/png;base64,AAAA"},
	})

	if len(got[0].Parts) != 1 {
		t.Fatalf("图片应产生 1 个 part，实际 %d 个", len(got[0].Parts))
	}
	if got[0].Parts[0].Type != "image" || got[0].Parts[0].ImageURL != "data:image/png;base64,AAAA" {
		t.Fatalf("part 形状不对：%#v", got[0].Parts[0])
	}
	// base64 绝不能同时出现在正文里：那样既浪费 token，模型也读不懂
	if strings.Contains(got[0].Content, "AAAA") {
		t.Fatal("正文里不该出现图片的 base64")
	}
}

// 图片附件的正文里**不能出现文件名**。
//
// 踩过的坑：早先正文写了 "[图片附件: cat.png]"，模型于是把它当成工作区里的
// 一个文件，掉头去 list_dir 找它 —— 明明图就贴在这条消息里。
func TestAttachImageDoesNotMentionFilename(t *testing.T) {
	got := attachToLastUser([]Message{{Role: "user", Content: "这是什么"}}, []Attachment{
		{Type: "image", Name: "cat.png", DataURL: "data:image/png;base64,AAAA"},
	})

	if strings.Contains(got[0].Content, "cat.png") {
		t.Fatalf("图片附件的正文不该提文件名（会诱导模型去文件系统找）：%q", got[0].Content)
	}
	if len(got[0].Parts) != 1 {
		t.Fatalf("图片本身要照样挂上：%#v", got[0].Parts)
	}
}

// 视频这类解析不了的：如实说清楚，别让模型以为它能看懂
func TestAttachVideoSaysItCannotBeParsed(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "这段视频讲了什么"}}
	got := attachToLastUser(msgs, []Attachment{
		{Type: "file", Name: "clip.mp4", Size: 5 << 20},
	})

	if len(got[0].Parts) != 0 {
		t.Fatal("视频不该产生 image part")
	}
	if !strings.Contains(got[0].Content, "clip.mp4") {
		t.Fatal("要告诉模型有这么个附件")
	}
	if !strings.Contains(got[0].Content, "5.0 MB") {
		t.Fatalf("要报出可读的大小，实际正文：%s", got[0].Content)
	}
	if !strings.Contains(got[0].Content, "无法直接解析") {
		t.Fatal("要明确说明看不了内容，否则模型会编")
	}
}

// 附件并入的是最后一条 user 消息 —— 拆成两条模型往往只盯着后一条
func TestAttachJoinsLastUserMessage(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "第一问"},
		{Role: "assistant", Content: "第一答"},
		{Role: "user", Content: "第二问"},
	}
	got := attachToLastUser(msgs, []Attachment{{Type: "text", Name: "a.txt", Text: "X"}})

	if len(got) != 3 {
		t.Fatalf("不该新增消息，实际 %d 条", len(got))
	}
	if strings.Contains(got[0].Content, "a.txt") {
		t.Fatal("不该并进第一条")
	}
	if !strings.Contains(got[2].Content, "第二问") || !strings.Contains(got[2].Content, "a.txt") {
		t.Fatalf("应并进最后一条 user 消息，实际：%s", got[2].Content)
	}
}

// 一条 user 消息都没有时（罕见但可能），要补一条，而不是丢掉附件
func TestAttachAppendsWhenNoUserMessage(t *testing.T) {
	msgs := []Message{{Role: "assistant", Content: "我先说一句"}}
	got := attachToLastUser(msgs, []Attachment{{Type: "text", Name: "a.txt", Text: "X"}})

	if len(got) != 2 {
		t.Fatalf("应补一条 user 消息，实际 %d 条", len(got))
	}
	if got[1].Role != "user" || !strings.Contains(got[1].Content, "a.txt") {
		t.Fatalf("补出来的消息不对：%#v", got[1])
	}
}

// 没有附件时必须原样返回：这条路径每天都在跑，不能有任何副作用
func TestAttachNoopWithoutAttachments(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "你好"}}
	got := attachToLastUser(msgs, nil)
	if len(got) != 1 || got[0].Content != "你好" || len(got[0].Parts) != 0 {
		t.Fatalf("无附件时不该改动消息：%#v", got)
	}
}

// 附件没名字时也要有个说法，不能输出 "[附件: ]" 这种残句
func TestAttachUnnamedAttachment(t *testing.T) {
	got := attachToLastUser([]Message{{Role: "user"}}, []Attachment{{Type: "file", Size: 1}})
	if strings.Contains(got[0].Content, "[附件: ]") {
		t.Fatalf("无名字时应有兜底称呼：%s", got[0].Content)
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{5 << 20, "5.0 MB"},
	}
	for _, c := range cases {
		if got := humanSize(c.n); got != c.want {
			t.Errorf("%d 应显示为 %s，实际 %s", c.n, c.want, got)
		}
	}
}
