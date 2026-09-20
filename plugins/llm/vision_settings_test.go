package main

import "testing"

// 没配视觉模型：必须原样返回。这条路径是默认状态，任何改动都不该发生。
func TestWithVisionNoopWhenUnset(t *testing.T) {
	base := settings{Provider: "openai", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"}
	got := base.withVision()

	if got != base {
		t.Fatalf("没配视觉模型时不该改动配置：%#v", got)
	}
}

// 只配了视觉模型名：其余保持不动（同一个服务商的另一个模型）
func TestWithVisionSwapsModelOnly(t *testing.T) {
	base := settings{
		Provider: "openai", BaseURL: "https://api.deepseek.com/v1",
		APIKey: "sk-text", Model: "deepseek-chat",
		VisionModel: "deepseek-flash",
	}
	got := base.withVision()

	if got.Model != "deepseek-flash" {
		t.Fatalf("应切到视觉模型，实际 %q", got.Model)
	}
	if got.Provider != "openai" || got.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("同服务商时不该改地址：%#v", got)
	}
	if got.APIKey != "sk-text" {
		t.Fatal("没单独配视觉密钥时应复用主密钥")
	}
}

// 视觉用的是别家服务商：地址必须跟着换，否则会拿 A 家的模型名去敲 B 家的网关
func TestWithVisionSwitchesProviderAndBaseURL(t *testing.T) {
	base := settings{
		Provider: "openai", BaseURL: "https://api.deepseek.com/v1",
		APIKey: "sk-text", Model: "deepseek-chat",
		VisionProvider: "ollama", VisionModel: "qwen2-vl",
	}
	got := base.withVision()

	if got.Provider != "ollama" {
		t.Fatalf("应切 provider，实际 %q", got.Provider)
	}
	if got.BaseURL != defaultBaseURL("ollama") {
		t.Fatalf("换 provider 后应改用它的默认地址，实际 %q", got.BaseURL)
	}
	if got.Model != "qwen2-vl" {
		t.Fatalf("模型不对：%q", got.Model)
	}
}

// 显式给了视觉专用地址/密钥：以它为准
func TestWithVisionExplicitOverrides(t *testing.T) {
	base := settings{
		Provider: "openai", BaseURL: "https://a.example/v1",
		APIKey: "sk-text", Model: "cheap-text",
		VisionProvider: "openai", VisionBaseURL: "https://b.example/v1",
		VisionAPIKey: "sk-vision", VisionModel: "gpt-4o",
	}
	got := base.withVision()

	if got.BaseURL != "https://b.example/v1" {
		t.Fatalf("应用视觉专用地址，实际 %q", got.BaseURL)
	}
	if got.APIKey != "sk-vision" {
		t.Fatalf("应用视觉专用密钥，实际 %q", got.APIKey)
	}
	if got.Model != "gpt-4o" {
		t.Fatalf("模型不对：%q", got.Model)
	}
	// 主配置不能被就地改掉（settings 是值类型，改的是副本）
	if base.Model != "cheap-text" || base.APIKey != "sk-text" {
		t.Fatalf("不该污染主配置：%#v", base)
	}
}
