package main

import "strings"

// ---------------------------------------------------------------------------
// 文生图服务商目录：一张表，RPC（providers）与页面（panel.go）共用。
//
// 之前这 5 条写死在 providers() 里，页面要用就得再抄一份 —— 同一份知识存两处，
// 迟早飘开（改了一个服务商地址、忘了另一个）。抽成单一数据源，两处都从这儿读。
// ---------------------------------------------------------------------------

type imagePreset struct {
	Key     string
	Label   string
	BaseURL string
	Model   string
	Note    string
}

var imagePresets = []imagePreset{
	{Key: "openai", Label: "OpenAI", BaseURL: defaultOpenAIBase, Model: "dall-e-3",
		Note: "OpenAI 官方"},
	{Key: "wanx", Label: "通义万相", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Model: "wanx-v1", Note: "阿里通义万相（国内直连）"},
	{Key: "siliconflow", Label: "SiliconFlow", BaseURL: "https://api.siliconflow.cn/v1",
		Model: "Kwai-Kolors/Kolors", Note: "聚合平台（有免费额度）"},
	{Key: "zhipu", Label: "智谱", BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		Model: "cogview-3", Note: "智谱 CogView"},
	{Key: "local", Label: "本地 SD", BaseURL: "http://127.0.0.1:7860/v1",
		Model: "sd", Note: "本地 SD WebUI / ComfyUI 兼容层"},
}

// presetAliases 是 provider 取值的**历史别名**：老配置/环境变量里可能写的是
// dashscope、bigmodel 这类实际服务名，而不是目录里的短键名。
var presetAliases = map[string]string{
	"dashscope": "wanx",
	"bigmodel":  "zhipu",
}

func presetByKey(key string) (imagePreset, bool) {
	if k, ok := presetAliases[key]; ok {
		key = k
	}
	for _, it := range imagePresets {
		if it.Key == key {
			return it, true
		}
	}
	return imagePreset{}, false
}

func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
