package main

import "time"

// Totals 是本机累计用量。它落盘在配置键 totals 里，重启不清零。
type Totals struct {
	Requests         int                     `json:"requests"`
	Failed           int                     `json:"failed,omitempty"`
	PromptTokens     int                     `json:"promptTokens"`
	CompletionTokens int                     `json:"completionTokens"`
	TotalTokens      int                     `json:"totalTokens"`
	CacheHitTokens   int                     `json:"cacheHitTokens"`
	CacheMissTokens  int                     `json:"cacheMissTokens"`
	LastAt           string                  `json:"lastAt,omitempty"`
	LastModel        string                  `json:"lastModel,omitempty"`
	LastErr          string                  `json:"lastErr,omitempty"`
	Since            string                  `json:"since,omitempty"`
	ByModel          map[string]*ModelTotals `json:"byModel,omitempty"`
}

// ModelTotals 是按模型拆开的计数。
type ModelTotals struct {
	Requests         int `json:"requests"`
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	CacheHitTokens   int `json:"cacheHitTokens"`
	CacheMissTokens  int `json:"cacheMissTokens"`
}

// hitRate 返回缓存命中率（0~1）。没有输入 token 时返回 0。
//
// DeepSeek 的 usage 里 prompt_cache_hit_tokens + prompt_cache_miss_tokens
// 正好等于 prompt_tokens，所以分母用这两者之和最准。
func hitRate(t Totals) float64 {
	den := t.CacheHitTokens + t.CacheMissTokens
	if den <= 0 {
		return 0
	}
	return float64(t.CacheHitTokens) / float64(den)
}

func nowClock() string { return time.Now().Format("15:04:05") }
