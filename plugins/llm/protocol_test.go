package main

import (
	"strings"
	"testing"
)

// 用户报过的真实场景：LM Studio 只认 OpenAI 协议，被按 Ollama 协议问 /api/chat 时
// 它回的是 **HTTP 200** 加一句 {"error":"Unexpected endpoint or method."} ——
// 状态码检查拦不住，用户看到的只是"连不上"。
// 提示必须直接说出该改哪个字段，否则用户会一直在地址和密钥上打转。
func TestProtocolHintWhenOllamaProtocolMeetsOpenAIServer(t *testing.T) {
	body := `{"error":"Unexpected endpoint or method. (POST /api/chat)"}`
	got := protocolMismatchHint(body, true)
	if !strings.Contains(got, "openai") {
		t.Fatalf("应提示把 provider 改成 openai，实际 %q", got)
	}
}

// 反向：Ollama 被按 OpenAI 协议问，回的是 404 加一行纯文本（不是合法 JSON）。
func TestProtocolHintWhenOpenAIProtocolMeetsOllama(t *testing.T) {
	for _, body := range []string{"404 page not found", "404 Page Not Found"} {
		got := protocolMismatchHint(body, false)
		if !strings.Contains(got, "ollama") {
			t.Fatalf("应提示把 provider 改成 ollama（%q），实际 %q", body, got)
		}
	}
}

// 用户报过的真实场景：LM Studio 的「要求 API 令牌」被打开之后，
// 所有请求都回 401，而密钥框里留着的往往是云端的密钥。
//
// 光说"接口拒绝了这个密钥"没用 —— 用户会一直在密钥和地址之间打转，
// 想不到问题出在服务端自己开的鉴权上。本机地址就必须把这一步说透。
func TestAuthHintOnLocalEndpoint(t *testing.T) {
	for _, url := range []string{
		"http://127.0.0.1:1234/v1",
		"http://127.0.0.1:1234",
		"http://localhost:1234/v1",
		"http://192.168.1.9:8000/v1",
		"http://10.0.0.7:8080/v1",
	} {
		got := authHint(401, url)
		for _, want := range []string{"本地服务", "Local Server", "Manage Tokens"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s 的 401 提示里缺少 %q：%q", url, want, got)
			}
		}
		// 密钥框是全平台共用的：不说这句，用户换回云端服务时会以为是自己填错了
		if !strings.Contains(got, "共用") {
			t.Fatalf("%s 的提示应说明密钥框共用：%q", url, got)
		}
	}
}

// 反过来同样重要：**云端地址的 401 绝不能带这段本机说教**。
// 那种情况下问题确实是密钥本身（没开通 / 填错 / 过期），
// 塞一段"本地服务要不要令牌"只会把人带偏。
func TestAuthHintSilentForCloud(t *testing.T) {
	for _, url := range []string{
		"https://api.deepseek.com/v1",
		"https://api.openai.com/v1",
		"https://api.siliconflow.cn/v1",
		"http://8.8.8.8:1234/v1", // 公网 IP，不是内网
	} {
		if got := authHint(401, url); got != "" {
			t.Fatalf("%s 是云端/公网地址，不该出现本机提示：%q", url, got)
		}
	}
}

// 只有鉴权失败才提示；别的状态码（404 / 500 / 200）都不该扯到密钥上。
func TestAuthHintOnlyOnAuthStatus(t *testing.T) {
	for _, status := range []int{200, 400, 404, 429, 500} {
		if got := authHint(status, "http://127.0.0.1:1234/v1"); got != "" {
			t.Fatalf("HTTP %d 不该给鉴权提示：%q", status, got)
		}
	}
	if authHint(403, "http://127.0.0.1:1234/v1") == "" {
		t.Fatal("403 也是鉴权失败，应给提示")
	}
}

// 这些提示都是给终端看的**纯文本**：markdown 的粗体标记不会渲染，
// 只会原样显示成两个星号，看起来像是插件把源码漏出来了。
// （这个错犯过两次，所以单独盯一行。）
func TestHintsArePlainText(t *testing.T) {
	for _, got := range []string{
		authHint(401, "http://127.0.0.1:1234/v1"),
		protocolMismatchHint(`{"error":"Unexpected endpoint or method. (POST /api/chat)"}`, true),
		protocolMismatchHint("404 page not found", false),
	} {
		if strings.Contains(got, "**") {
			t.Fatalf("提示里不该出现 markdown 标记：%q", got)
		}
	}
}

func TestIsLocalHost(t *testing.T) {
	local := []string{"http://127.0.0.1:1234/v1", "http://localhost:1234", "http://[::1]:1234/v1",
		"http://192.168.0.2/v1", "http://172.16.3.4/v1", "http://10.1.2.3/v1"}
	for _, u := range local {
		if !isLocalHost(u) {
			t.Fatalf("%s 应判为本机/内网", u)
		}
	}
	remote := []string{"https://api.deepseek.com/v1", "http://8.8.8.8/v1", "http://172.32.0.1/v1", ""}
	for _, u := range remote {
		if isLocalHost(u) {
			t.Fatalf("%s 不该判为本机/内网", u)
		}
	}
}

// 提示不能滥发。模型名写错、密钥不对这类失败里没有协议痕迹，
// 这时候再冒出「改 provider」就是把人往反方向带 —— 那才是真的难查。
func TestProtocolHintStaysQuietOnUnrelatedErrors(t *testing.T) {
	bodies := []string{
		`{"error":{"message":"Invalid model identifier \"qwen3.5-9b\"","code":"model_not_found"}}`,
		`{"error":{"message":"Incorrect API key provided"}}`,
		`{"error":"context length exceeded"}`,
		`{"error":"model 'llama3.1' not found, try pulling it first"}`,
		"",
	}
	for _, body := range bodies {
		if got := protocolMismatchHint(body, true); got != "" {
			t.Fatalf("不该对 %q 给出协议提示，实际 %q", body, got)
		}
		if got := protocolMismatchHint(body, false); got != "" {
			t.Fatalf("不该对 %q 给出协议提示，实际 %q", body, got)
		}
	}
}
