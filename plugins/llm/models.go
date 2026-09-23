package main

import (
	"encoding/json"
	"sort"
	"strings"

	"aiwork/sdk/go/plugin"
)

type modelsParams struct {
	Provider string `json:"provider,omitempty"`
	BaseURL  string `json:"baseUrl,omitempty"`
	APIKey   string `json:"apiKey,omitempty"`
}

// modelListTimeoutMs 是「获取模型清单」的硬超时（毫秒）。
//
// 这是一次轻量 GET，服务正常时几十毫秒就回；20 秒还没回就是地址不对 /
// 协议不对 / 服务不可达。宁可早失败让界面恢复，也别让用户对着一句
// 「正在获取…」干等两分钟。
const modelListTimeoutMs = 20000

// models 向服务端要一份**真正可用**的模型清单。
//
// 这是"别让用户手打模型名"的关键：OpenAI 兼容接口都提供 GET /models，
// Ollama 提供 /api/tags。直接问服务端，比在客户端硬编码一份迟早会过期的清单靠谱得多。
//
// 三个参数都可选，用来支持"还没保存、先点一下试试"。
func (l *LLM) models(args json.RawMessage) (any, error) {
	var in modelsParams
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in)
	}

	cfg := l.settings()
	if s := strings.TrimSpace(in.Provider); s != "" {
		cfg.Provider = strings.ToLower(s)
	}
	if s := strings.TrimSpace(in.BaseURL); s != "" {
		cfg.BaseURL = s
	}
	if s := strings.TrimSpace(in.APIKey); s != "" {
		cfg.APIKey = s
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL(cfg.Provider)
	}

	if cfg.Provider == "mock" {
		return map[string]any{
			"provider": "mock",
			"models":   []string{"mock-1"},
			"count":    1,
			"note":     "离线 mock 没有远端模型列表",
		}, nil
	}

	url := strings.TrimRight(cfg.BaseURL, "/") + "/models"
	headers := map[string]string{}
	if cfg.Provider == "ollama" {
		url = strings.TrimRight(cfg.BaseURL, "/") + "/api/tags"
	} else if cfg.APIKey != "" {
		headers["Authorization"] = "Bearer " + cfg.APIKey
	}

	l.ctx.Log("info", "拉取模型列表", map[string]any{"provider": cfg.Provider, "url": url})
	// 模型清单是轻量请求，不值得按聊天请求的超时（默认 120s）去等：
	// 20 秒没响应基本就是地址不对、协议不对或服务不可达 —— 用户还等着界面恢复呢。
	t := cfg.TimeoutMs
	if t <= 0 || t > modelListTimeoutMs {
		t = modelListTimeoutMs
	}
	status, body, err := l.doRequest("GET", url, nil, headers, t)
	if err != nil {
		return nil, err
	}
	// 协议选错时（Ollama 去问 /models、OpenAI 服务被问 /api/tags）失败形态五花八门：
	// 404 的、回 200 却带 error 的、回 200 但结构对不上的都有。统一在这里认一次。
	hint := protocolMismatchHint(body, cfg.Provider == "ollama")
	switch {
	case status == 401 || status == 403:
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"接口拒绝了这个密钥（HTTP %d）：检查 API Key 是否填对、是否已开通%s",
			status, authHint(status, cfg.BaseURL))
	case status < 200 || status >= 300:
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"获取模型列表失败（HTTP %d）：%s%s", status, clipRunes(body, 200), hint)
	}

	list := parseModelList(body)
	if len(list) == 0 {
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"接口返回里没找到模型名称：%s%s", clipRunes(body, 200), hint)
	}
	sort.Strings(list)
	return map[string]any{
		"provider": cfg.Provider,
		"baseUrl":  cfg.BaseURL,
		"models":   list,
		"count":    len(list),
	}, nil
}

// parseModelList 兼容几种常见返回形状：
//
//	OpenAI 兼容: {"data":[{"id":"gpt-4o-mini"}]}
//	Ollama:      {"models":[{"name":"llama3.1"}]}
//	裸数组:      {"models":["a","b"]}
func parseModelList(body string) []string {
	var payload struct {
		Data   []json.RawMessage `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, 16)
	for _, group := range [][]json.RawMessage{payload.Data, payload.Models} {
		for _, item := range group {
			name := modelName(item)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func modelName(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		for _, v := range []string{obj.ID, obj.Name, obj.Model} {
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}
