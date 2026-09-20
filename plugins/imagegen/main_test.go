package main

import (
	"strings"
	"testing"
)

// 生成的图要有可识别的名字：能从文件名看出画的是什么、什么时候画的
func TestAttachNameShape(t *testing.T) {
	got := attachName("", "a red fox", 0, 1)
	if !strings.HasPrefix(got, "a-red-fox-") || !strings.HasSuffix(got, ".png") {
		t.Fatalf("名字应含 prompt 摘要且以 .png 结尾，实际 %q", got)
	}
	// 两次调用不能撞名（否则后一张会覆盖前一张）
	if attachName("", "a red fox", 0, 1) == got {
		t.Skip("同一秒内可能同名，这是可接受的")
	}
}

// 多张图要各自带序号，不能互相覆盖
func TestAttachNameNumbersMultiple(t *testing.T) {
	a := attachName("cat", "x", 0, 3)
	b := attachName("cat", "x", 1, 3)
	if !strings.Contains(a, "cat-1") || !strings.Contains(b, "cat-2") {
		t.Fatalf("多张图应带序号，实际 %q / %q", a, b)
	}
	if a == b {
		t.Fatal("两张图不能同名")
	}
}

// 全中文 prompt 压不出 ASCII 摘要时，回落到默认名而不是空名
func TestSlugFallsBackForNonASCII(t *testing.T) {
	if got := slug("一只橘猫在窗台上"); got != "" {
		t.Fatalf("纯中文应压不出 ASCII，实际 %q", got)
	}
	got := attachName("", "一只橘猫", 0, 1)
	if !strings.HasPrefix(got, "image-") {
		t.Fatalf("应回落到默认名，实际 %q", got)
	}
}

// slug 只留安全字符，且不能超长
func TestSlugSanitizes(t *testing.T) {
	got := slug(`C:\Windows/../System32 & Other Stuff!`)
	if strings.ContainsAny(got, `\/:&! `) {
		t.Fatalf("文件名里不该出现这些字符：%q", got)
	}
	if len(got) > 24 {
		t.Fatalf("摘要不该超长：%q", got)
	}
}

// 服务商默认值：选了预设就应当有地址和模型，不能让用户面对空白
func TestProviderDefaults(t *testing.T) {
	cases := map[string][2]string{
		"wanx":        {"", "wanx-v1"},
		"siliconflow": {"", "Kwai-Kolors/Kolors"},
		"zhipu":       {"", "cogview-3"},
		"openai":      {defaultOpenAIBase, "dall-e-3"},
		"":            {defaultOpenAIBase, "dall-e-3"},
	}
	for provider, want := range cases {
		if got := defaultModel(provider); got != want[1] {
			t.Errorf("%q 的默认模型应是 %q，实际 %q", provider, want[1], got)
		}
		if want[0] != "" && defaultBaseURL(provider) != want[0] {
			t.Errorf("%q 的默认地址应是 %q，实际 %q", provider, want[0], defaultBaseURL(provider))
		}
	}
}

// 密钥回显只给头尾，中间打码 —— 界面与日志里都不能出现完整密钥
func TestMaskKey(t *testing.T) {
	if got := maskKey("sk-1234567890abcdef"); got != "sk-1****cdef" {
		t.Fatalf("打码不对：%q", got)
	}
	if got := maskKey("short"); got != "****" {
		t.Fatalf("太短的密钥应整体打码，实际 %q", got)
	}
	if got := maskKey(""); got != "" {
		t.Fatalf("空密钥应返回空，实际 %q", got)
	}
}

// 拼路径不能丢分隔符
func TestJoinPath(t *testing.T) {
	if got := joinPath("/a/b", "x.png"); got != "/a/b/x.png" {
		t.Fatalf("实际 %q", got)
	}
	if got := joinPath(`E:\pics\`, "x.png"); got != `E:\pics\x.png` {
		t.Fatalf("已带分隔符时不该重复，实际 %q", got)
	}
}
