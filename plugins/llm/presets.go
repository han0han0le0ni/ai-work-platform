package main

import "strings"

// ---------------------------------------------------------------------------
// 这张表本来长在界面插件里（workbench/presets.go）
//
// 它却全是 LLM 领域的知识：哪个平台说的是哪套协议、默认端点在哪、密钥去哪申请、
// 本机应用默认端口是多少。放在界面里意味着同一件事得说两遍 —— llm 插件自己有
// providers / discover / configure，界面又存了一份"能连什么"。两份迟早飘开。
//
// 搬过来的边界也很清楚：这张表**只描述服务**，不含任何界面状态。
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 服务目录：把"能连什么"摊成一张表
//
// 设置页回答的是一个很具体的问题：**我该连哪儿？** 答案分两大类，界面也按这两类
// 排（见 settingsLeftRows）：
//
//	cloud  云端 · 知名平台   —— 点一下，地址与模型自动填好，只需再粘密钥
//	niche  云端 · 自定义/冷门平台 —— 自己搭的、代理站、小众平台：地址与模型自己填
//	local  本地 · 已知应用   —— 本机跑的 Ollama / LM Studio / vLLM…：不要密钥
//
// 表里的地址都必须是 OpenAI 兼容端点或 Ollama 原生端点 —— 它们直接对应
// llm.v1 的 openai / ollama 两个 provider，中间没有任何转换层。
// ---------------------------------------------------------------------------

type providerPreset struct {
	Key      string
	Label    string
	Provider string // llm.v1 的 provider：mock / openai / ollama
	BaseURL  string
	Models   []string
	KeyURL   string // 去哪申请密钥
	NeedKey  bool
	// Hint 覆盖默认提示。默认那两句都是围绕「粘密钥」写的，
	// 而自定义端点要填的是地址和模型名，密钥反而是可以留空的那一项。
	Hint string
	// Note 是本地应用的一句话说明（它是什么、默认端口是多少）。
	Note string
	// Group 决定它出现在设置页的哪一块：cloud / niche / local。
	Group string
}

// 三个分组。界面按组取用，顺序也由这里定。
const (
	groupCloud = "cloud"
	groupNiche = "niche"
	groupLocal = "local"
)

// providerPresets 是全部可选服务，按"云端知名 → 云端自定义 → 本地"排列。
//
// 顺序是有意义的：它决定界面上的先后，也决定 presetByBaseURL 反查时
// 同地址多条记录谁先命中（本地那批的地址互不重复，所以不会歧义）。
var providerPresets = []providerPreset{
	// ---- 云端 · 知名平台：点一下就能用（地址与默认模型由预设填好）----
	{
		Key: "deepseek", Label: "DeepSeek", Provider: "openai", Group: groupCloud,
		BaseURL: "https://api.deepseek.com/v1",
		Models:  []string{"deepseek-chat", "deepseek-reasoner"},
		KeyURL:  "platform.deepseek.com/api_keys", NeedKey: true,
	},
	{
		Key: "moonshot", Label: "Kimi", Provider: "openai", Group: groupCloud,
		BaseURL: "https://api.moonshot.cn/v1",
		Models:  []string{"moonshot-v1-8k", "moonshot-v1-32k"},
		KeyURL:  "platform.moonshot.cn/console/api-keys", NeedKey: true,
	},
	{
		Key: "zhipu", Label: "智谱 GLM", Provider: "openai", Group: groupCloud,
		BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		Models:  []string{"glm-4-plus", "glm-4-flash"},
		KeyURL:  "open.bigmodel.cn/usercenter/apikeys", NeedKey: true,
	},
	{
		Key: "qwen", Label: "通义千问", Provider: "openai", Group: groupCloud,
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Models:  []string{"qwen-plus", "qwen-max"},
		KeyURL:  "bailian.console.aliyun.com", NeedKey: true,
	},
	{
		Key: "siliconflow", Label: "硅基流动", Provider: "openai", Group: groupCloud,
		BaseURL: "https://api.siliconflow.cn/v1",
		Models:  []string{"deepseek-ai/DeepSeek-V3"},
		KeyURL:  "cloud.siliconflow.cn/account/ak", NeedKey: true,
	},
	{
		Key: "openai", Label: "OpenAI", Provider: "openai", Group: groupCloud,
		BaseURL: "https://api.openai.com/v1",
		Models:  []string{"gpt-4o-mini", "gpt-4o"},
		KeyURL:  "platform.openai.com/api-keys", NeedKey: true,
	},
	{
		Key: "wenxin", Label: "百度文心", Provider: "openai", Group: groupCloud,
		BaseURL: "https://qianfan.baidubce.com/v2",
		Models:  []string{"ernie-4.5-turbo-128k"},
		KeyURL:  "console.bce.baidu.com/iam", NeedKey: true,
	},
	{
		Key: "hunyuan", Label: "腾讯混元", Provider: "openai", Group: groupCloud,
		BaseURL: "https://api.hunyuan.cloud.tencent.com/v1",
		Models:  []string{"hunyuan-turbos-latest"},
		KeyURL:  "console.cloud.tencent.com/hunyuan/api-key", NeedKey: true,
	},
	{
		Key: "doubao", Label: "字节豆包", Provider: "openai", Group: groupCloud,
		BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		Models:  []string{"doubao-pro-32k"},
		KeyURL:  "console.volcengine.com/ark", NeedKey: true,
		// 方舟的模型名要写「接入点 ID」（ep-…），不写清楚用户会一直填成模型名然后 404
		Hint: "模型名要填方舟控制台里的「接入点 ID」（ep-…），不是 doubao-pro-32k 这种名字",
	},
	{
		Key: "spark", Label: "讯飞星火", Provider: "openai", Group: groupCloud,
		BaseURL: "https://spark-api-open.xf-yun.com/v1",
		Models:  []string{"generalv3.5"},
		KeyURL:  "console.xfyun.cn", NeedKey: true,
	},
	{
		Key: "minimax", Label: "MiniMax", Provider: "openai", Group: groupCloud,
		BaseURL: "https://api.minimax.chat/v1",
		Models:  []string{"abab6.5s-chat"},
		KeyURL:  "platform.minimaxi.com", NeedKey: true,
	},
	{
		Key: "stepfun", Label: "阶跃星辰", Provider: "openai", Group: groupCloud,
		BaseURL: "https://api.stepfun.com/v1",
		Models:  []string{"step-2-16k"},
		KeyURL:  "platform.stepfun.com", NeedKey: true,
	},
	{
		Key: "yi", Label: "零一万物", Provider: "openai", Group: groupCloud,
		BaseURL: "https://api.lingyiwanwu.com/v1",
		Models:  []string{"yi-lightning"},
		KeyURL:  "platform.lingyiwanwu.com", NeedKey: true,
	},
	{
		Key: "gemini", Label: "Google Gemini", Provider: "openai", Group: groupCloud,
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		Models:  []string{"gemini-2.0-flash"},
		KeyURL:  "aistudio.google.com/apikey", NeedKey: true,
	},
	{
		Key: "groq", Label: "Groq", Provider: "openai", Group: groupCloud,
		BaseURL: "https://api.groq.com/openai/v1",
		Models:  []string{"llama-3.3-70b-versatile"},
		KeyURL:  "console.groq.com/keys", NeedKey: true,
	},
	{
		Key: "openrouter", Label: "OpenRouter", Provider: "openai", Group: groupCloud,
		BaseURL: "https://openrouter.ai/api/v1",
		Models:  []string{"openai/gpt-4o-mini"},
		KeyURL:  "openrouter.ai/keys", NeedKey: true,
	},

	// ---- 云端 · 自定义 / 冷门平台 ----
	//
	// **位置在"云端"这一组的末尾、本地应用之前。** 它接的是任何 OpenAI 兼容端点
	// （自建网关 / 代理站 / 小众云厂商都算），与上面的知名平台是同一类东西。
	// 排在本地那批后面时，列表一换行正好落进"本地那一组" —— 位置本身就是暗示。
	{
		Key: "custom", Label: "自定义 / 冷门平台", Provider: "openai", Group: groupNiche,
		// NeedKey 保持 true：自定义端点要不要密钥取决于**它自己**，界面不该替它
		// 下"不需要密钥"的结论 —— 那句话会挂在 API Key 下面，把通用端点说成本地服务。
		NeedKey: true,
		// 该说清的是"必须填什么"：地址与模型名，而不是密钥。
		Hint: "填服务地址与模型名（可点「获取可用模型」照抄），密钥按该服务的要求填",
	},

	// ---- 本地 · 已知应用（本机默认端口）----
	//
	// 这几条既是"点一下直接填好"，也是扫描结果的回填目标：
	// 扫描到的 key 与这里的 key 一致，界面据此高亮对应的那一项。
	{
		Key: "ollama", Label: "Ollama", Provider: "ollama", Group: groupLocal,
		BaseURL: "http://127.0.0.1:11434",
		Models:  []string{"llama3.1", "qwen2.5"},
		NeedKey: false, Note: "走原生 /api 接口；模型要先 ollama pull",
	},
	{
		Key: "lmstudio", Label: "LM Studio", Provider: "openai", Group: groupLocal,
		BaseURL: "http://127.0.0.1:1234/v1",
		NeedKey: false, Note: "在 Developer → Local Server 里启动服务",
	},
	{
		Key: "vllm", Label: "vLLM", Provider: "openai", Group: groupLocal,
		BaseURL: "http://127.0.0.1:8000/v1",
		NeedKey: false, Note: "OpenAI 兼容接口，端口看启动参数",
	},
	{
		Key: "llamacpp", Label: "llama.cpp", Provider: "openai", Group: groupLocal,
		BaseURL: "http://127.0.0.1:8080/v1",
		NeedKey: false, Note: "llama.cpp / llamafile / LocalAI 默认都是 8080",
	},
	{
		Key: "textgen", Label: "text-generation-webui", Provider: "openai", Group: groupLocal,
		BaseURL: "http://127.0.0.1:5000/v1",
		NeedKey: false,
	},
	{
		Key: "koboldcpp", Label: "KoboldCpp", Provider: "openai", Group: groupLocal,
		BaseURL: "http://127.0.0.1:5001/v1",
		NeedKey: false,
	},
	{
		Key: "jan", Label: "Jan", Provider: "openai", Group: groupLocal,
		BaseURL: "http://127.0.0.1:1337/v1",
		NeedKey: false,
	},
	{
		Key: "gpt4all", Label: "GPT4All", Provider: "openai", Group: groupLocal,
		BaseURL: "http://127.0.0.1:4891/v1",
		NeedKey: false,
	},
	{
		Key: "sglang", Label: "SGLang", Provider: "openai", Group: groupLocal,
		BaseURL: "http://127.0.0.1:30000/v1",
		NeedKey: false,
	},
	{
		Key: "xinference", Label: "Xinference", Provider: "openai", Group: groupLocal,
		BaseURL: "http://127.0.0.1:9997/v1",
		NeedKey: false,
	},
	{
		Key: "mock", Label: "离线 mock", Provider: "mock", Group: groupLocal,
		Models:  []string{"mock-1"},
		NeedKey: false, Note: "不连网、不要密钥，用来验证整条链路",
	},
}

func presetsOfGroup(group string) []providerPreset {
	out := make([]providerPreset, 0, len(providerPresets))
	for _, p := range providerPresets {
		if p.Group == group {
			out = append(out, p)
		}
	}
	return out
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

func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// wireLabel 把 llm.v1 的 provider 翻成"端点说的是哪套协议"。
//
// 界面上一律说协议名、不说 provider 名：openai / ollama 是插件里的字段值，
// 而用户要判断的是"我那个服务说的是哪套话"。两者混着用，就是"连不上"
// 这类问题最难自查的根源。
func wireLabel(provider string) string {
	switch provider {
	case "ollama":
		return "Ollama 原生"
	case "mock":
		return "离线 mock"
	default:
		return "OpenAI 兼容"
	}
}

// portOf 从地址里抠出端口。
//
// 本机服务排查时，端口是最需要照抄的一串数字（"我明明装的是 LM Studio，
// 它却在 1234 而不是 8000"），而完整的 Base URL 往往在屏幕上被截掉尾巴。
func portOf(baseURL string) string {
	s := strings.TrimSpace(baseURL)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return ""
}

// presetOfCandidate 把"扫描到的本机服务"对应到目录里的那一条。
//
// 扫描结果与目录是两份数据（一份来自探测，一份来自预设），靠 key 对上号：
// 对上了才高亮得出来"你现在连的就是它"。
func presetOfCandidate(key string) (providerPreset, bool) { return presetByKey(key) }
