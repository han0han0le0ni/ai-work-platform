package main

import (
	"strings"
	"testing"
)

// 预设条目只给 preset 时，名称 / 地址 / 模型都应由目录补齐 ——
// 界面只需要选"DeepSeek"，不该再抄一遍地址。
func TestNormalizeProviderEntriesFillsPresetDefaults(t *testing.T) {
	list, err := normalizeProviderEntries([]providerEntry{
		{Preset: "deepseek", Provider: "openai"},
		{Preset: "lmstudio", Provider: "openai"},
	})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if list[0].Label != "DeepSeek" || list[0].BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("预设默认值没补上: %+v", list[0])
	}
	if list[0].Model != "deepseek-chat" {
		t.Fatalf("模型应取预设第一条，实际 %q", list[0].Model)
	}
	if list[1].Label != "LM Studio" || list[1].BaseURL != "http://127.0.0.1:1234/v1" {
		t.Fatalf("本地预设没补上: %+v", list[1])
	}
}

// id 必须唯一：两条都叫 current 时，界面按 id 找卡片会串。
func TestNormalizeProviderEntriesDedupesID(t *testing.T) {
	list, err := normalizeProviderEntries([]providerEntry{
		{ID: "current", Provider: "openai", BaseURL: "https://a.example/v1"},
		{ID: "current", Provider: "openai", BaseURL: "https://b.example/v1"},
	})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if list[0].ID == list[1].ID {
		t.Fatalf("id 应去重，实际都是 %q", list[0].ID)
	}
}

func TestNormalizeProviderEntriesRejectsUnknownProvider(t *testing.T) {
	_, err := normalizeProviderEntries([]providerEntry{{Provider: "anthropic"}})
	if err == nil || !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("未知 provider 应报错并点名，实际 %v", err)
	}
}

// 旧配置升上来时，界面要能凭 baseUrl 还原成"LM Studio"这张卡。
func TestLegacyEntryMatchesPresetByBaseURL(t *testing.T) {
	e := (&LLM{}).legacyEntry(settings{
		Provider: "openai", BaseURL: "http://127.0.0.1:1234/v1",
		Model: "ai@q4_k_m", APIKey: "sk-test-123456",
	})
	if e.Preset != "lmstudio" || e.Label != "LM Studio" {
		t.Fatalf("应还原成 LM Studio 预设: %+v", e)
	}
	if e.Model != "ai@q4_k_m" || e.APIKey != "sk-test-123456" {
		t.Fatalf("旧配置的模型与密钥应原样带过来: %+v", e)
	}
}
