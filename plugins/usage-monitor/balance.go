package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"aiwork/sdk/go/plugin"
)

// defaultRoot 是 DeepSeek 的接口根地址（余额接口挂在根上，不带 /v1）。
const defaultRoot = "https://api.deepseek.com"

// Balance 是一次余额查询的结果。
type Balance struct {
	OK        bool          `json:"ok"`
	Available bool          `json:"available"`
	Currency  string        `json:"currency,omitempty"`
	Total     string        `json:"total,omitempty"`
	Granted   string        `json:"granted,omitempty"`
	ToppedUp  string        `json:"toppedUp,omitempty"`
	Infos     []BalanceInfo `json:"infos,omitempty"`
	CheckedAt string        `json:"checkedAt,omitempty"`
	Err       string        `json:"err,omitempty"`
}

// BalanceInfo 是 balance_infos 里的一项（一般只有一种币种）。
type BalanceInfo struct {
	Currency string `json:"currency"`
	Total    string `json:"total"`
	Granted  string `json:"granted"`
	ToppedUp string `json:"toppedUp"`
}

// refreshBalance 查一次余额并写回状态。
//
// 查询失败不算异常状态：原因记在 Balance.Err 里（浮层显示"不可用"），
// 同时把错误原样抛给调用方。
func (u *Usage) refreshBalance() (any, error) {
	u.balanceMu.Lock()
	defer u.balanceMu.Unlock()

	key, root := u.credentials()
	if key == "" {
		b := Balance{
			CheckedAt: nowClock(),
			Err:       "没有可用的 API Key：先在设置页配好 DeepSeek，或用 usage.v1 configure 传 apiKey",
		}
		u.mu.Lock()
		u.balance = b
		u.mu.Unlock()
		return u.snapshot(), plugin.Errorf(plugin.CodeInvalidParams, "%s", b.Err)
	}

	b, err := u.fetchBalance(key, root)
	u.mu.Lock()
	prev := u.balance
	u.balance = b
	u.mu.Unlock()

	// 只在余额真的变了（或第一次查到）时记一行：轮询很勤，逐次记会刷屏。
	if err == nil && b.OK && (b.Total != prev.Total || b.Currency != prev.Currency || !prev.OK) {
		u.log("info", fmt.Sprintf("DeepSeek 余额 %s（可用=%v，赠送 %s，充值 %s）",
			balanceShort(b), b.Available, b.Granted, b.ToppedUp))
	}
	if err != nil {
		return u.snapshot(), err
	}
	return u.snapshot(), nil
}

// fetchBalance 走核心的能力代理发 GET {root}/user/balance。
func (u *Usage) fetchBalance(key, root string) (Balance, error) {
	b := Balance{CheckedAt: nowClock()}
	url := strings.TrimRight(root, "/") + "/user/balance"

	raw, err := u.ctx.Capability("core.net.request", map[string]any{
		"method": "GET",
		"url":    url,
		"headers": map[string]string{
			"Authorization": "Bearer " + key,
			"Accept":        "application/json",
		},
		"timeoutMs": 15000,
	})
	if err != nil {
		if plugin.IsCode(err, plugin.CodePermissionDenied) {
			b.Err = "出网被核心拒绝：manifest 需要声明 network 权限"
			return b, plugin.Errorf(plugin.CodePermissionDenied, "%s", b.Err)
		}
		b.Err = err.Error()
		return b, err
	}

	var out struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		b.Err = "解析能力代理响应失败: " + err.Error()
		return b, plugin.Errorf(plugin.CodeInternalError, "%s", b.Err)
	}

	if out.Status < 200 || out.Status >= 300 {
		if out.Status == 401 || out.Status == 403 {
			b.Err = fmt.Sprintf("接口拒绝了这个密钥（HTTP %d）", out.Status)
		} else {
			b.Err = fmt.Sprintf("查询余额失败（HTTP %d）：%s", out.Status, clip(out.Body, 160))
		}
		return b, plugin.Errorf(plugin.CodeInternalError, "%s", b.Err)
	}

	var payload struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency        string `json:"currency"`
			TotalBalance    string `json:"total_balance"`
			GrantedBalance  string `json:"granted_balance"`
			ToppedUpBalance string `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal([]byte(out.Body), &payload); err != nil {
		b.Err = "响应不是预期格式：" + clip(out.Body, 160)
		return b, plugin.Errorf(plugin.CodeInternalError, "%s", b.Err)
	}

	b.Available = payload.IsAvailable
	for _, info := range payload.BalanceInfos {
		b.Infos = append(b.Infos, BalanceInfo{
			Currency: info.Currency,
			Total:    info.TotalBalance,
			Granted:  info.GrantedBalance,
			ToppedUp: info.ToppedUpBalance,
		})
	}
	if len(b.Infos) == 0 {
		b.Err = "响应里没有余额信息"
		return b, plugin.Errorf(plugin.CodeInternalError, "%s", b.Err)
	}
	first := b.Infos[0]
	b.OK = true
	b.Currency = first.Currency
	b.Total = first.Total
	b.Granted = first.Granted
	b.ToppedUp = first.ToppedUp
	return b, nil
}

// credentials 决定用哪个密钥、打哪个根地址。
//
// 顺序：本插件自己的配置 > llm 插件的配置（用户已经在那儿填好了 DeepSeek 密钥）。
// 后者靠能力代理读文件实现，需要 filesystem:read 权限。
func (u *Usage) credentials() (key, root string) {
	u.mu.Lock()
	cfg := u.cfg
	u.mu.Unlock()

	key = strings.TrimSpace(cfg.APIKey)
	root = apiRoot(cfg.BaseURL)
	if key != "" {
		u.setKeySource("self")
		if root == "" {
			root = defaultRoot
		}
		return key, root
	}

	llm := u.llmConfig()
	if k := strings.TrimSpace(llm.APIKey); k != "" {
		src := "llm"
		if llm.Provider != "" {
			src = "llm:" + llm.Provider
		}
		u.setKeySource(src)
		if r := apiRoot(llm.BaseURL); r != "" {
			root = r
		}
		if root == "" {
			root = defaultRoot
		}
		return k, root
	}

	u.setKeySource("none")
	if root == "" {
		root = defaultRoot
	}
	return "", root
}

// llmConfig 是 llm 插件配置文件的样子。
type llmConfig struct {
	APIKey   string `json:"apiKey"`
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
}

// llmConfig 经能力代理读 llm 插件的配置（读不到就返回零值，比如没装它）。
func (u *Usage) llmConfig() llmConfig {
	var cfg llmConfig
	raw, err := u.ctx.FSRead("plugin-configs/com.official.llm.json")
	if err != nil {
		return cfg
	}
	var out struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return cfg
	}
	_ = json.Unmarshal([]byte(out.Content), &cfg)
	return cfg
}

// apiRoot 把 OpenAI 兼容的 Base URL 还原成接口根地址：
//
//	https://api.deepseek.com/v1  ->  https://api.deepseek.com
//	https://api.deepseek.com     ->  https://api.deepseek.com
func apiRoot(base string) string {
	b := strings.TrimSpace(base)
	if b == "" {
		return ""
	}
	b = strings.TrimRight(b, "/")
	b = strings.TrimSuffix(b, "/chat/completions")
	b = strings.TrimSuffix(b, "/v1")
	b = strings.TrimRight(b, "/")
	return b
}

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
