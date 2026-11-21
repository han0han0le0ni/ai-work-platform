package main

// 多服务商配置（对齐 DSH 的「模型提供商」概念）。
//
// 老的配置是一组扁平键：provider / baseUrl / apiKey / model / visionModel /
// systemPrompt。一组键只能表达"一个当前在用的服务商"，界面因此只能有一张卡。
// 这里加一层列表配置：
//
//	providers       [{id,preset,label,provider,baseUrl,apiKey,model,visionModel,systemPrompt}]
//	activeProvider  当前生效的那条 id
//
// 扁平键继续保留并永远同步成"当前生效服务商"的值 —— 老的调用方（/tool、
// 面板、其他插件）读扁平键时看到的仍是一个完整、可用的配置，两边不会飘开。

import (
	"encoding/json"
	"fmt"
	"strings"

	"aiwork/sdk/go/plugin"
)

// providerEntry 是「模型提供商」的一条配置。
//
//   - id     稳定标识，界面按它对上号；预设条目直接用预设 key
//   - preset 目录里的 key（deepseek / lmstudio…），只用于还原名称与分组
//   - key    为空表示"沿用已保存的密钥"：界面拿到的永远是掩码值，
//     把掩码值原样传回来时必须识别出来，不能拿它覆盖真密钥
type providerEntry struct {
	ID           string `json:"id"`
	Preset       string `json:"preset,omitempty"`
	Label        string `json:"label,omitempty"`
	Provider     string `json:"provider"`
	BaseURL      string `json:"baseUrl,omitempty"`
	APIKey       string `json:"apiKey,omitempty"`
	Model        string `json:"model,omitempty"`
	VisionModel  string `json:"visionModel,omitempty"`
	SystemPrompt string `json:"systemPrompt,omitempty"`
}

func (e providerEntry) displayLabel() string {
	if strings.TrimSpace(e.Label) != "" {
		return strings.TrimSpace(e.Label)
	}
	if e.Preset != "" {
		if p, ok := presetByKey(e.Preset); ok {
			return p.Label
		}
	}
	if e.Provider != "" {
		return e.Provider
	}
	return "未命名服务商"
}

// storedProviders 读已保存的服务商列表。
func (l *LLM) storedProviders() []providerEntry {
	raw, err := l.ctx.GetConfig("providers")
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var out []providerEntry
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// pickActive 返回当前生效的条目；activeProvider 失效时退回第一条。
func (l *LLM) pickActive(list []providerEntry) (providerEntry, bool) {
	if len(list) == 0 {
		return providerEntry{}, false
	}
	id := strings.TrimSpace(l.ctx.GetConfigString("activeProvider", ""))
	if id != "" {
		for _, e := range list {
			if e.ID == id {
				return e, true
			}
		}
	}
	return list[0], true
}

// legacyEntry 把旧的扁平配置折成一条服务商，供界面在"还没用新列表保存过"
// 时也能画出当前卡片。
func (l *LLM) legacyEntry(s settings) providerEntry {
	preset := presetByBaseURL(s.BaseURL)
	e := providerEntry{
		ID:       "current",
		Preset:   preset,
		Label:    s.Provider,
		Provider: s.Provider,
		BaseURL:  s.BaseURL,
		APIKey:   s.APIKey,
		Model:    s.Model,
	}
	if preset != "" {
		e.ID = preset
		if p, ok := presetByKey(preset); ok {
			e.Label = p.Label
		}
	}
	return e
}

// allProviders 返回界面要展示的提供商列表：优先用列表配置，没有就用扁平配置
// 折成一条 —— 这样从旧版本升上来时设置页不会突然空掉。
func (l *LLM) allProviders(s settings) ([]providerEntry, string) {
	list := l.storedProviders()
	if len(list) == 0 {
		e := l.legacyEntry(s)
		return []providerEntry{e}, e.ID
	}
	e, ok := l.pickActive(list)
	if !ok {
		return list, ""
	}
	return list, e.ID
}

// normalizeProviderEntries 校验并补齐列表：provider 必须是已知后端，
// id 必须唯一；地址 / 模型留空时按预设或 provider 默认值补上。
func normalizeProviderEntries(in []providerEntry) ([]providerEntry, error) {
	out := make([]providerEntry, 0, len(in))
	seen := map[string]bool{}
	for i, raw := range in {
		e := raw
		e.Provider = strings.ToLower(strings.TrimSpace(e.Provider))
		if e.Provider == "" {
			e.Provider = "openai"
		}
		if _, ok := providers[e.Provider]; !ok {
			return nil, plugin.Errorf(plugin.CodeInvalidParams,
				"providers[%d] 的 provider %q 未知（可选 mock / openai / ollama / openai-legacy）", i, e.Provider)
		}
		e.Preset = strings.TrimSpace(e.Preset)
		if p, ok := presetByKey(e.Preset); ok {
			if strings.TrimSpace(e.Label) == "" {
				e.Label = p.Label
			}
			if strings.TrimSpace(e.BaseURL) == "" {
				e.BaseURL = p.BaseURL
			}
			if strings.TrimSpace(e.Model) == "" && len(p.Models) > 0 {
				e.Model = p.Models[0]
			}
		}
		e.ID = strings.TrimSpace(e.ID)
		if e.ID == "" {
			if e.Preset != "" {
				e.ID = e.Preset
			} else {
				e.ID = fmt.Sprintf("provider-%d", i+1)
			}
		}
		for seen[e.ID] {
			e.ID += "-2"
		}
		seen[e.ID] = true
		e.Label = strings.TrimSpace(e.Label)
		if e.Label == "" {
			e.Label = e.Provider
		}
		e.BaseURL = strings.TrimRight(strings.TrimSpace(e.BaseURL), "/")
		if e.BaseURL == "" {
			e.BaseURL = defaultBaseURL(e.Provider)
		}
		e.Model = strings.TrimSpace(e.Model)
		if e.Model == "" {
			e.Model = defaultModel(e.Provider)
		}
		e.VisionModel = strings.TrimSpace(e.VisionModel)
		out = append(out, e)
	}
	return out, nil
}

// mergeProviderKeys 处理"密钥没改"的情况。
//
// 界面只拿到掩码值，保存时要么传空串，要么把掩码原样传回来；两种都表示
// "沿用旧密钥"。只有真正的新密钥才覆盖。
func (l *LLM) mergeProviderKeys(list []providerEntry) []providerEntry {
	old := map[string]string{}
	stored := l.storedProviders()
	for _, e := range stored {
		old[e.ID] = e.APIKey
	}
	// 第一次从扁平配置升级到列表时，providers 还不存在，真密钥在扁平键里；
	// 而界面拿到的只是它的掩码。掩码能对上就说明"用户没改密钥"，照抄原值。
	legacy := ""
	if len(stored) == 0 {
		legacy = strings.TrimSpace(l.ctx.Pick("apiKey", "AIWORK_LLM_API_KEY", "OPENAI_API_KEY", "DEEPSEEK_API_KEY"))
		if legacy == "" {
			legacy = l.secretKey()
		}
	}
	for i := range list {
		k := strings.TrimSpace(list[i].APIKey)
		if k != "" && !strings.Contains(k, "****") {
			continue // 用户真的输入了新密钥
		}
		if v, ok := old[list[i].ID]; ok && v != "" {
			list[i].APIKey = v
			continue
		}
		if legacy != "" && k != "" && k == maskKey(legacy) {
			list[i].APIKey = legacy
			continue
		}
		list[i].APIKey = ""
	}
	return list
}

// syncLegacy 把某条服务商同步到扁平键，保持旧调用方可用。
func (l *LLM) syncLegacy(e providerEntry) error {
	kv := []struct {
		key   string
		value any
	}{
		{"provider", e.Provider},
		{"baseUrl", e.BaseURL},
		{"apiKey", e.APIKey},
		{"model", e.Model},
		{"visionModel", e.VisionModel},
		{"systemPrompt", e.SystemPrompt},
	}
	for _, u := range kv {
		if err := l.ctx.SetConfig(u.key, u.value); err != nil {
			return err
		}
	}
	return nil
}

// saveProviders 写入整份列表 + 当前生效 id，并同步扁平键。
func (l *LLM) saveProviders(list []providerEntry, activeID string) error {
	if activeID == "" && len(list) > 0 {
		activeID = list[0].ID
	}
	if activeID != "" {
		found := false
		for _, e := range list {
			if e.ID == activeID {
				found = true
				break
			}
		}
		if !found {
			if len(list) > 0 {
				activeID = list[0].ID
			} else {
				activeID = ""
			}
		}
	}
	if err := l.ctx.SetConfig("providers", list); err != nil {
		return err
	}
	if err := l.ctx.SetConfig("activeProvider", activeID); err != nil {
		return err
	}
	for _, e := range list {
		if e.ID == activeID {
			return l.syncLegacy(e)
		}
	}
	return nil
}

// patchActiveEntry 在只改扁平键时，把改动同步进当前那条列表配置，
// 避免列表与扁平键各说各话。
func (l *LLM) patchActiveEntry(patch providerEntry, fields map[string]bool) {
	list := l.storedProviders()
	if len(list) == 0 {
		return
	}
	e, ok := l.pickActive(list)
	if !ok {
		return
	}
	for i := range list {
		if list[i].ID != e.ID {
			continue
		}
		if fields["provider"] {
			list[i].Provider = patch.Provider
		}
		if fields["baseUrl"] {
			list[i].BaseURL = patch.BaseURL
		}
		if fields["apiKey"] {
			list[i].APIKey = patch.APIKey
		}
		if fields["model"] {
			list[i].Model = patch.Model
		}
		if fields["visionModel"] {
			list[i].VisionModel = patch.VisionModel
		}
		if fields["systemPrompt"] {
			list[i].SystemPrompt = patch.SystemPrompt
		}
		break
	}
	_ = l.saveProviders(list, e.ID)
}
