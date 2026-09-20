package main

import "strings"

// providerPreset 是一个"点一下就能用"的服务预设。
//
// 用户要做的只有两件事：点服务 → 粘 API Key。
// baseUrl 和默认模型都由预设填好，不需要用户去翻文档抄地址、敲模型名。
// 预设里的模型只是开箱默认值，点「获取可用模型」会向服务端要**真实**清单。
//
// 表里的地址都是 OpenAI 兼容端点，直接对应 llm.v1 的 openai provider。
type providerPreset struct {
	Key      string
	Label    string
	Provider string // llm.v1 的 provider：mock / openai / ollama
	BaseURL  string
	Models   []string
	KeyURL   string // 去哪申请密钥
	NeedKey  bool
}

var providerPresets = []providerPreset{
	{
		Key: "deepseek", Label: "DeepSeek", Provider: "openai",
		BaseURL: "https://api.deepseek.com/v1",
		Models:  []string{"deepseek-chat", "deepseek-reasoner"},
		KeyURL:  "platform.deepseek.com/api_keys", NeedKey: true,
	},
	{
		Key: "moonshot", Label: "Kimi", Provider: "openai",
		BaseURL: "https://api.moonshot.cn/v1",
		Models:  []string{"moonshot-v1-8k", "moonshot-v1-32k"},
		KeyURL:  "platform.moonshot.cn/console/api-keys", NeedKey: true,
	},
	{
		Key: "zhipu", Label: "智谱 GLM", Provider: "openai",
		BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		Models:  []string{"glm-4-plus", "glm-4-flash"},
		KeyURL:  "open.bigmodel.cn/usercenter/apikeys", NeedKey: true,
	},
	{
		Key: "qwen", Label: "通义千问", Provider: "openai",
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Models:  []string{"qwen-plus", "qwen-max"},
		KeyURL:  "bailian.console.aliyun.com", NeedKey: true,
	},
	{
		Key: "siliconflow", Label: "硅基流动", Provider: "openai",
		BaseURL: "https://api.siliconflow.cn/v1",
		Models:  []string{"deepseek-ai/DeepSeek-V3"},
		KeyURL:  "cloud.siliconflow.cn/account/ak", NeedKey: true,
	},
	{
		Key: "openai", Label: "OpenAI", Provider: "openai",
		BaseURL: "https://api.openai.com/v1",
		Models:  []string{"gpt-4o-mini", "gpt-4o"},
		KeyURL:  "platform.openai.com/api-keys", NeedKey: true,
	},
	{
		Key: "ollama", Label: "本地 Ollama", Provider: "ollama",
		BaseURL: "http://127.0.0.1:11434",
		Models:  []string{"llama3.1", "qwen2.5"},
		NeedKey: false,
	},
	{
		Key: "mock", Label: "离线 mock", Provider: "mock",
		Models:  []string{"mock-1"},
		NeedKey: false,
	},
	{
		Key: "custom", Label: "自定义", Provider: "openai",
		NeedKey: true,
	},
}

func presetByKey(key string) (providerPreset, bool) {
	for _, p := range providerPresets {
		if p.Key == key {
			return p, true
		}
	}
	return providerPreset{}, false
}

// presetByBaseURL 反查：配置里存的是地址，用它把界面上的选中状态还原出来。
func presetByBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	for _, p := range providerPresets {
		if p.BaseURL != "" && strings.TrimRight(p.BaseURL, "/") == baseURL {
			return p.Key
		}
	}
	return ""
}

// applyPreset 应用一个服务预设：填好地址与默认模型，并把焦点直接送到 API Key。
func (a *app) applyPreset(key string) {
	p, ok := presetByKey(key)
	if !ok {
		return
	}
	a.st.presetKey = p.Key
	a.st.provider = p.Provider
	a.st.modelList = append([]string{}, p.Models...)
	a.st.modelsNote = ""

	if f := a.st.field("baseUrl"); f != nil {
		f.value = p.BaseURL
	}
	if f := a.st.field("model"); f != nil {
		f.value = firstOf(p.Models)
	}

	// 焦点一定落在 API Key 上：接下来要做的就是粘贴
	a.st.focus = "apiKey"
	a.st.statusOK = true
	switch {
	case !p.NeedKey:
		a.st.status = "已选 " + p.Label + "：它不需要密钥，点「保存并生效」即可"
	case p.KeyURL != "":
		a.st.status = "已选 " + p.Label + "：粘贴 API Key 就行（密钥在 " + p.KeyURL + " 申请）"
	default:
		a.st.status = "已选 " + p.Label + "：粘贴 API Key 就行"
	}
}

func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}
