package main

import (
	"strings"
	"testing"
)

// 排版不变量：与 workbench 一样，浮层的每一行显示宽度必须精确等于 overlayW。
// 差一列，右上角那一块就会把工作台的边框顶歪一格。
func TestOverlayLinesWidth(t *testing.T) {
	cases := []struct {
		name string
		tot  Totals
		bal  Balance
	}{
		{"空数据", Totals{}, Balance{}},
		{"有数有余额", Totals{
			Requests: 128, PromptTokens: 1234567, CompletionTokens: 89012,
			CacheHitTokens: 900000, CacheMissTokens: 334567,
			LastAt: "18:04:05", LastModel: "deepseek-chat",
		}, Balance{OK: true, Available: true, Currency: "CNY", Total: "12.34"}},
		{"余额失败", Totals{Requests: 3}, Balance{Err: "接口拒绝了这个密钥（HTTP 401）"}},
		{"超长模型名撑爆值", Totals{
			Requests: 999999, PromptTokens: 999999999,
			CacheHitTokens: 1, CacheMissTokens: 1,
			LastAt: "23:59:59", LastModel: "a-very-long-model-name-that-should-be-cut",
		}, Balance{OK: true, Currency: "USD", Total: "1234567890.0000000001"}},
		{"未知的币种原样显示", Totals{}, Balance{OK: true, Currency: "EUR", Total: "9.99"}},
	}

	for _, tc := range cases {
		lines := overlayLines(overlayW, tc.tot, tc.bal)
		if len(lines) != 7 {
			t.Fatalf("%s: 期望 7 行，实际 %d 行", tc.name, len(lines))
		}
		for i, line := range lines {
			if w := dispWidth(line); w != overlayW {
				t.Errorf("%s: 第 %d 行宽度 %d，期望 %d（%q）", tc.name, i, w, overlayW, line)
			}
			if strings.Contains(line, "\033") {
				t.Errorf("%s: 第 %d 行含转义序列（应由 overlay.render 统一加）: %q", tc.name, i, line)
			}
		}
		if !strings.HasPrefix(lines[0], "┌") || !strings.HasPrefix(lines[len(lines)-1], "└") {
			t.Errorf("%s: 边框不完整：%q … %q", tc.name, lines[0], lines[len(lines)-1])
		}
	}
}

// 值再长也不能把行撑宽：ovRow 必须截断，而不是让右边框溢出屏幕。
func TestOvRowTruncates(t *testing.T) {
	for _, w := range []int{36, 40, 24} {
		line := ovRow(w, "余额", strings.Repeat("9", 200))
		if got := dispWidth(line); got != w {
			t.Errorf("宽度 %d：超长值把行撑成 %d 列（%q）", w, got, line)
		}
	}
}

func TestApiRoot(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com/v1":  "https://api.deepseek.com",
		"https://api.deepseek.com/v1/": "https://api.deepseek.com",
		"https://api.deepseek.com":     "https://api.deepseek.com",
		"https://api.deepseek.com/":    "https://api.deepseek.com",
		"":                             "",
		"  ":                           "",
	}
	for in, want := range cases {
		if got := apiRoot(in); got != want {
			t.Errorf("apiRoot(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestHitRate(t *testing.T) {
	if got := hitRate(Totals{}); got != 0 {
		t.Errorf("没有缓存数据时命中率应为 0，实际 %v", got)
	}
	got := hitRate(Totals{CacheHitTokens: 1, CacheMissTokens: 3})
	if got < 0.249 || got > 0.251 {
		t.Errorf("命中率应为 0.25，实际 %v", got)
	}
}

func TestHumanInt(t *testing.T) {
	real := map[int]string{
		0:          "0",
		7:          "7",
		999:        "999",
		1000:       "1,000",
		1234567:    "1,234,567",
		100:        "100",
		1000000:    "1,000,000",
		-1234567:   "-1,234,567",
		2147483647: "2,147,483,647",
	}
	for in, want := range real {
		if got := humanInt(in); got != want {
			t.Errorf("humanInt(%d) = %q，期望 %q", in, got, want)
		}
	}
}

// 余额那一行：没查过显示「查询中…」，查不到显示「不可用」，都不暴露密钥或长错误。
func TestBalanceShort(t *testing.T) {
	if got := balanceShort(Balance{}); got != "查询中…" {
		t.Errorf("未查询应显示查询中，实际 %q", got)
	}
	if got := balanceShort(Balance{Err: "boom"}); got != "不可用" {
		t.Errorf("查询失败应显示不可用，实际 %q", got)
	}
	if got := balanceShort(Balance{OK: true, Currency: "CNY", Total: "12.34"}); got != "¥12.34" {
		t.Errorf("人民币应显示 ¥12.34，实际 %q", got)
	}
	if got := balanceShort(Balance{OK: true, Currency: "USD", Total: "1.00"}); got != "$1.00" {
		t.Errorf("美元应显示 $1.00，实际 %q", got)
	}
	if got := balanceShort(Balance{OK: true, Currency: "EUR", Total: "9.99"}); got != "EUR 9.99" {
		t.Errorf("未知币种应原样显示，实际 %q", got)
	}
}

func TestHitRateShort(t *testing.T) {
	if got := hitRateShort(Totals{}); got != "—" {
		t.Errorf("无缓存数据应显示 —，实际 %q", got)
	}
	got := hitRateShort(Totals{CacheHitTokens: 500, CacheMissTokens: 1500})
	if !strings.Contains(got, "25.0%") {
		t.Errorf("命中率 25%% 未显示出来：%q", got)
	}
}

// 计量：一次事件累计进总数，也按模型拆开；失败计数单独走。
func TestApplyEvent(t *testing.T) {
	u := &Usage{totals: Totals{ByModel: map[string]*ModelTotals{}, Since: "00:00:00"}}

	u.applyEvent(&usageEvent{Model: "deepseek-chat", PromptTokens: 100, CompletionTokens: 20,
		CacheHitTokens: 80, CacheMissTokens: 20, OK: true})
	u.applyEvent(&usageEvent{Model: "deepseek-chat", PromptTokens: 50, CompletionTokens: 5,
		CacheHitTokens: 10, CacheMissTokens: 40, OK: true})
	u.applyEvent(&usageEvent{Provider: "openai", Model: "gpt-4o-mini", PromptTokens: 7, Err: "HTTP 429"})

	if u.totals.Requests != 3 {
		t.Errorf("请求数应为 3，实际 %d", u.totals.Requests)
	}
	if u.totals.PromptTokens != 157 {
		t.Errorf("输入 token 应为 157，实际 %d", u.totals.PromptTokens)
	}
	if u.totals.CompletionTokens != 25 {
		t.Errorf("输出 token 应为 25，实际 %d", u.totals.CompletionTokens)
	}
	if u.totals.Failed != 1 {
		t.Errorf("失败数应为 1，实际 %d", u.totals.Failed)
	}
	// totalTokens 没给时由输入 + 输出补上
	if u.totals.TotalTokens != 182 {
		t.Errorf("总量应为 182，实际 %d", u.totals.TotalTokens)
	}
	if u.totals.LastErr != "HTTP 429" {
		t.Errorf("最后一次错误应记下来，实际 %q", u.totals.LastErr)
	}
	chat := u.totals.ByModel["deepseek-chat"]
	if chat == nil || chat.Requests != 2 || chat.CacheHitTokens != 90 {
		t.Errorf("按模型累计不对：%+v", chat)
	}
	mini := u.totals.ByModel["gpt-4o-mini"]
	if mini == nil || mini.Requests != 1 {
		t.Errorf("第二个模型未被拆开：%+v", u.totals.ByModel)
	}
	if len(u.totals.ByModel) != 2 {
		t.Errorf("应只有两个模型，实际 %d", len(u.totals.ByModel))
	}
}

// 既没有 model 也没有 provider 的事件也要落到一个桶里，不能凭空消失。
func TestApplyEventUnknownModel(t *testing.T) {
	u := &Usage{totals: Totals{ByModel: map[string]*ModelTotals{}, Since: "00:00:00"}}
	u.applyEvent(&usageEvent{PromptTokens: 1})
	if u.totals.ByModel["unknown"] == nil {
		t.Fatalf("未知来源应落到 unknown 桶：%+v", u.totals.ByModel)
	}
}
