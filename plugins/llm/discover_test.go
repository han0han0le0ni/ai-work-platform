package main

import (
	"strings"
	"testing"
)

// 一个正经的模型服务：2xx + 合法 JSON + 有模型名。
func TestProbeVerdictAcceptsOpenAIStyleList(t *testing.T) {
	body := `{"object":"list","data":[{"id":"qwen3.5-9b"},{"id":"llama-3.1-8b"}]}`
	alive, models, note := probeVerdict(200, body)
	if !alive {
		t.Fatal("合法 JSON 的模型清单应判为服务在跑")
	}
	if len(models) != 2 || models[0] != "qwen3.5-9b" {
		t.Fatalf("模型名没解析出来：%v", models)
	}
	if note != "" {
		t.Fatalf("一切正常时不该有额外说明，实际 %q", note)
	}
}

// Ollama 的原生形状也要认：{"models":[{"name":"llama3.1"}]}
func TestProbeVerdictAcceptsOllamaTags(t *testing.T) {
	alive, models, _ := probeVerdict(200, `{"models":[{"name":"llama3.1","size":4700000000}]}`)
	if !alive || len(models) != 1 || models[0] != "llama3.1" {
		t.Fatalf("应认出 Ollama /api/tags 的形状，实际 alive=%v models=%v", alive, models)
	}
}

// 本机 8000 / 8080 上随便一个网页服务也会回 200，但它不是 AI。
// 放进扫描结果里比漏报更糟 —— 用户会照着去连一个根本不能说话的东西。
func TestProbeVerdictRejectsNonJSON200(t *testing.T) {
	for _, body := range []string{
		"<!DOCTYPE html><html><body>hello</body></html>",
		"hello world",
		"",
	} {
		if alive, _, _ := probeVerdict(200, body); alive {
			t.Fatalf("200 但不是 JSON 的不该判为 AI 服务：%q", body)
		}
	}
}

// 要求密钥是一项有用的信息：服务确实在，只是要先配令牌（LM Studio 的开关、
// vLLM 的 --api-key 都会这样）。这条要说清楚，别让用户以为"没扫到"。
func TestProbeVerdictReportsAuthRequired(t *testing.T) {
	for _, status := range []int{401, 403} {
		alive, models, note := probeVerdict(status, `{"error":"unauthorized"}`)
		if !alive {
			t.Fatalf("HTTP %d 说明服务在，应判为发现", status)
		}
		if len(models) != 0 {
			t.Fatalf("没拿到清单时不该编造模型：%v", models)
		}
		if !strings.Contains(note, "密钥") {
			t.Fatalf("应说明它要求密钥，实际 %q", note)
		}
	}
}

// 其它状态码（404 端点不对 / 500 崩了 / 别的协议）一律当成"不是它"。
func TestProbeVerdictIgnoresOtherStatuses(t *testing.T) {
	for _, status := range []int{204, 301, 400, 404, 500, 502} {
		if alive, _, _ := probeVerdict(status, "404 page not found"); alive {
			t.Fatalf("HTTP %d 不该判为发现", status)
		}
	}
}

// 200 且是合法 JSON、但没有模型名：服务在，值得报出来（用户可以手填模型），
// 但要把原因写在说明里。
func TestProbeVerdictAliveWithoutModels(t *testing.T) {
	alive, models, note := probeVerdict(200, `{"object":"list","data":[]}`)
	if !alive {
		t.Fatal("是合法 JSON 就说明接口对得上，应判为发现")
	}
	if len(models) != 0 {
		t.Fatalf("空清单不该编出模型：%v", models)
	}
	if note == "" {
		t.Fatal("没报出模型名时要说明这件事")
	}
}

// 扫描必须只碰回环地址：主动去探内网/公网既慢又越界。
func TestLocalCandidatesAreLoopbackOnly(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range localCandidates() {
		if !strings.HasPrefix(c.BaseURL, "http://127.0.0.1:") {
			t.Fatalf("%s 的地址不在回环上：%s", c.Label, c.BaseURL)
		}
		if c.Provider != "openai" && c.Provider != "ollama" {
			t.Fatalf("%s 的协议不认识：%s", c.Label, c.Provider)
		}
		if !strings.HasPrefix(c.Probe, "/") {
			t.Fatalf("%s 的探测路径应以 / 开头：%s", c.Label, c.Probe)
		}
		if c.Key == "" || c.Label == "" {
			t.Fatalf("候选缺 key/label：%+v", c)
		}
		if seen[c.Key] {
			t.Fatalf("候选 key 重复：%s（界面靠它回传要连接哪一个）", c.Key)
		}
		seen[c.Key] = true
	}
}

// Ollama 走的是原生 /api/tags，不能跟 OpenAI 兼容的那批混成 /v1/models。
func TestOllamaUsesNativeProbe(t *testing.T) {
	var got localCandidate
	for _, c := range localCandidates() {
		if c.Key == "ollama" {
			got = c
		}
	}
	if got.Provider != "ollama" {
		t.Fatalf("Ollama 的协议应为 ollama，实际 %q", got.Provider)
	}
	if got.Probe != "/api/tags" {
		t.Fatalf("Ollama 应探 /api/tags，实际 %q", got.Probe)
	}
	if strings.HasSuffix(got.BaseURL, "/v1") {
		t.Fatalf("Ollama 的地址不该带 /v1：%q", got.BaseURL)
	}
}

func TestJoinedURL(t *testing.T) {
	cases := []struct{ base, path, want string }{
		{"http://127.0.0.1:1234/v1", "/models", "http://127.0.0.1:1234/v1/models"},
		{"http://127.0.0.1:1234/v1/", "/models", "http://127.0.0.1:1234/v1/models"},
		{"http://127.0.0.1:11434", "api/tags", "http://127.0.0.1:11434/api/tags"},
	}
	for _, c := range cases {
		if got := joinedURL(c.base, c.path); got != c.want {
			t.Fatalf("joinedURL(%q,%q) = %q，期望 %q", c.base, c.path, got, c.want)
		}
	}
}

// 一条都没扫到时的说明必须给出下一步 —— "什么都没发现"是用户最容易卡住的地方。
func TestDiscoverNoteGuidesWhenNothingFound(t *testing.T) {
	note := discoverNote(11, nil)
	if !strings.Contains(note, "11") || !strings.Contains(note, "自定义") {
		t.Fatalf("没扫到时要说清扫了几个、并指向手动填写，实际 %q", note)
	}
}

func TestDiscoverNoteListsFound(t *testing.T) {
	note := discoverNote(11, []localHit{{Label: "Ollama"}, {Label: "LM Studio"}})
	for _, want := range []string{"11", "2", "Ollama", "LM Studio"} {
		if !strings.Contains(note, want) {
			t.Fatalf("说明里缺少 %q：%q", want, note)
		}
	}
}
