// Command llm 是官方 LLM 插件（provides: llm.v1）。
//
// 它只做三件事：
//  1. 把 agent 发来的「消息 + 工具定义」翻译成某家模型 API 的请求
//  2. 把模型响应翻译回插件内部的统一格式（content + toolCalls）
//  3. 让 provider / baseUrl / model / 密钥都能在运行时改（core.setConfig，或调用 configure 方法）
//
// 支持三家后端，用一个配置键切换：
//
//	mock    离线（默认）：不需要网络和密钥，可把整条链路跑完整
//	openai  任何 OpenAI 兼容接口（OpenAI / DeepSeek / Moonshot / vLLM / LM Studio…）
//	ollama  本地 Ollama 原生接口
//
// 网络访问一律经过核心的能力代理（core.net.request）：插件自己不碰 socket。
// manifest 里没声明 network 权限的话，核心会直接返回 -32002，插件无能为力。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"aiwork/sdk/go/plugin"
)

const serviceName = "llm.v1"

// ---------------------------------------------------------------------------
// 插件内部统一的消息 / 工具格式
//
// 这是 llm.v1 的公开契约（只增不减）：agent 按这个格式发消息，llm 按这个格式回工具调用。
// 各家 provider 的原生差异（OpenAI 的参数是字符串、Ollama 是对象）在 provider.go 里翻译，
// 调用方不需要关心底下用的是哪家模型。
//
// 字段一律 camelCase，避免与「原生 API 长什么样」耦合。
// ---------------------------------------------------------------------------

// Message 是一条对话消息。
type Message struct {
	Role    string `json:"role"` // system / user / assistant / tool
	Content string `json:"content,omitempty"`
	// Parts 是**多模态**内容：一段文字 + 若干张图片。
	//
	// 与 Content 并存：Content 仍是普通文本的主通道，Parts 只在需要让模型
	// "看见"图片时才用。老调用方完全不受影响 —— 没有 Parts 就照旧发字符串。
	Parts      []Part     `json:"parts,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`  // role=assistant 时，模型要求调用的工具
	ToolCallID string     `json:"toolCallId,omitempty"` // role=tool 时，对应的工具调用 id
	Name       string     `json:"name,omitempty"`
}

// Part 是消息里的一段内容。
type Part struct {
	Type string `json:"type"` // text / image
	// Text 是 type=text 时的文字。
	Text string `json:"text,omitempty"`
	// ImageURL 是 type=image 时的图片地址：
	// data:image/png;base64,xxx（本地文件转成的内联图片），或 http(s):// 远程地址。
	ImageURL string `json:"imageUrl,omitempty"`
}

// ToolCall 是模型要求的一次工具调用。
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// ToolSpec 是提供给模型的工具定义（JSON Schema 子集）。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

// LLM 实现 plugin.Plugin。
type LLM struct {
	ctx *plugin.Context

	// panel 是「模型接入」那一页的界面状态（见 panel.go）。
	//
	// 这一页住在插件自己身上：连哪个平台、密钥去哪申请、本机应用默认端口是多少，
	// 全是 LLM 领域的知识 —— 界面不该存第二份。
	panel llmPanel
}

// Activate 注册 llm.v1。
func (l *LLM) Activate(ctx *plugin.Context) error {
	l.ctx = ctx
	l.panel = newLLMPanel()
	if err := ctx.Provide(serviceName, map[string]any{
		"description": "把消息与工具定义翻译成模型调用，返回文本与工具调用",
		"methods":     []string{"chat", "models", "providers", "presets", "configure", "ping", "discover"},
		"providers":   []string{"mock", "openai", "ollama"},
	}); err != nil {
		return err
	}

	// 自带界面：把「模型接入」挂在 UI 插件上。
	if err := ctx.Provide(panelService, map[string]any{
		"description": "模型接入页：平台预设 / 本机扫描 / 密钥 / 协议 / 识图（llm 自己渲染）",
		"methods":     []string{"list", "render", "action", "key"},
	}); err != nil {
		ctx.Log("warn", "自带界面没能挂上（"+err.Error()+"），功能不受影响", nil)
	}

	cfg := l.settings()
	ctx.Log("info", "LLM 已就绪", map[string]any{
		"provider":   cfg.Provider,
		"baseUrl":    cfg.BaseURL,
		"model":      cfg.Model,
		"hasKey":     cfg.APIKey != "",
		"permission": ctx.Permissions,
	})
	return nil
}

// Handle 处理 core.invoke。
func (l *LLM) Handle(service, method string, args json.RawMessage) (any, error) {
	if service == panelService {
		return l.handlePanel(method, args)
	}
	if service != serviceName {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "LLM 插件不提供服务 %s", service)
	}
	switch method {
	case "chat":
		return l.chat(args)
	case "models":
		return l.models(args)
	case "providers":
		return l.providersReport(), nil
	case "presets":
		// 服务目录：云端知名 / 自定义冷门 / 本地应用。界面（终端或 Web）据此画预设，
		// 不再在界面里存第二份 —— 连哪儿、密钥去哪申请、默认端口都是 LLM 领域的知识。
		return l.presetsReport(), nil
	case "discover":
		// 扫描本机常见 AI 端点：界面「本地」那一页靠它回答"这台机器上有没有 AI"。
		return l.discover(args)
	case "configure":
		return l.configure(args)
	case "ping":
		cfg := l.settings()
		return map[string]any{"ok": true, "provider": cfg.Provider, "model": cfg.Model}, nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "llm.v1 不支持方法 %s", method)
	}
}

// Deactivate 清理。
func (l *LLM) Deactivate() error {
	if l.ctx != nil {
		l.ctx.Log("info", "LLM 插件退出", nil)
	}
	return nil
}

// ---------------------------------------------------------------------------
// chat：唯一的主方法
// ---------------------------------------------------------------------------

type chatParams struct {
	Messages    json.RawMessage `json:"messages"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	System      string          `json:"system,omitempty"`
	Model       string          `json:"model,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   int             `json:"maxTokens,omitempty"`
}

func (l *LLM) chat(args json.RawMessage) (any, error) {
	var in chatParams
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}

	msgs, err := decodeMessages(in.Messages)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "messages 不能为空")
	}
	tools, err := decodeTools(in.Tools)
	if err != nil {
		return nil, err
	}

	cfg := l.settings()
	// 单次调用可以覆盖配置，方便 /tool 试不同模型
	if in.Model != "" {
		cfg.Model = in.Model
	}
	if in.Temperature != nil {
		cfg.Temperature = in.Temperature
	}
	if in.MaxTokens > 0 {
		cfg.MaxTokens = in.MaxTokens
	}
	system := strings.TrimSpace(in.System)
	if system == "" {
		system = cfg.System
	}

	// 带图片就换用视觉配置：文字和识图分开配，日常聊天用便宜的模型，
	// 只有真的发了图才切到会看图的那个。没配视觉模型时原样不动。
	if n := countImages(msgs); n > 0 {
		cfg = cfg.withVision()
		if in.Model != "" {
			cfg.Model = in.Model // 单次调用显式指定的模型最优先
		}
		l.ctx.Log("info", fmt.Sprintf("本次请求含 %d 张图片", n), map[string]any{
			"provider": cfg.Provider,
			"model":    cfg.Model,
			"note":     "已按多模态发出；模型若回复看不到图，说明这个模型不支持视觉（vision）",
		})
	}

	fn, ok := providers[cfg.Provider]
	if !ok {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"未知 provider %q（可选 mock / openai / ollama / openai-legacy）", cfg.Provider)
	}

	req := &chatReq{
		Messages:    msgs,
		Tools:       tools,
		Model:       cfg.Model,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
		System:      system,
	}

	start := time.Now()
	out, err := fn(l, req, &cfg)
	if err != nil {
		l.ctx.Log("warn", "模型调用失败", map[string]any{
			"provider": cfg.Provider, "model": cfg.Model, "err": err.Error(),
		})
		return nil, err
	}

	calls := out.ToolCalls
	if calls == nil {
		calls = []ToolCall{}
	}
	l.ctx.Log("info", "模型调用完成", map[string]any{
		"provider": cfg.Provider, "model": cfg.Model,
		"toolCalls": len(calls), "ms": time.Since(start).Milliseconds(),
	})

	return map[string]any{
		"content":      out.Content,
		"toolCalls":    calls,
		"finishReason": out.FinishReason,
		"provider":     cfg.Provider,
		"model":        cfg.Model,
		"usage":        out.Usage,
		"ms":           time.Since(start).Milliseconds(),
		// 思考过程（空字符串表示这个模型没有思考，或本次没开 thinking）。
		// 只增不减：老调用方照旧不读它。
		"reasoning": out.Reasoning,
	}, nil
}

// ---------------------------------------------------------------------------
// providers / configure：让「换成哪个模型」不需要改文件、不需要重启
// ---------------------------------------------------------------------------

func (l *LLM) providersReport() map[string]any {
	cfg := l.settings()
	list, activeID := l.allProviders(cfg)
	providersOut := make([]map[string]any, 0, len(list))
	for _, e := range list {
		providersOut = append(providersOut, map[string]any{
			"id":           e.ID,
			"preset":       e.Preset,
			"label":        e.displayLabel(),
			"provider":     e.Provider,
			"baseUrl":      e.BaseURL,
			"apiKey":       maskKey(e.APIKey),
			"hasKey":       e.APIKey != "",
			"model":        e.Model,
			"visionModel":  e.VisionModel,
			"systemPrompt": e.SystemPrompt,
			"active":       e.ID == activeID,
		})
	}
	return map[string]any{
		"current":   cfg.Provider,
		"active":    activeID,
		"providers": providersOut,
		"available": []map[string]any{
			{
				"name": "mock", "model": "mock-1",
				"note": "离线，无需网络与密钥；用于验证链路",
			},
			{
				"name": "openai", "baseUrl": defaultBaseURL("openai"), "model": defaultModel("openai"),
				"note": "任何 OpenAI 兼容接口（OpenAI / DeepSeek / Moonshot / vLLM / LM Studio…）",
			},
			{
				"name": "ollama", "baseUrl": defaultBaseURL("ollama"), "model": defaultModel("ollama"),
				"note": "本地 Ollama 原生接口",
			},
			{
				"name": "openai-legacy", "baseUrl": defaultBaseURL("openai"), "model": defaultModel("openai"),
				"note": "与 openai 同一套接口，但不发 llm.usage 事件（想关掉用量统计就换它）",
			},
		},
		"config": map[string]any{
			"provider":     cfg.Provider,
			"baseUrl":      cfg.BaseURL,
			"model":        cfg.Model,
			"apiKey":       maskKey(cfg.APIKey),
			"temperature":  cfg.Temperature,
			"maxTokens":    cfg.MaxTokens,
			"timeoutMs":    cfg.TimeoutMs,
			"systemPrompt": cfg.System,

			// 识图专用配置。留空的那几项表示"复用主配置"，
			// 界面据此显示成"跟随主配置"，而不是显示成空值。
			"visionProvider": cfg.VisionProvider,
			"visionBaseUrl":  cfg.VisionBaseURL,
			"visionApiKey":   maskKey(cfg.VisionAPIKey),
			"visionModel":    cfg.VisionModel,
			"visionReady":    cfg.VisionModel != "",
		},
	}
}

// presetsReport 把服务目录摊给界面：谁在哪、用什么协议、密钥去哪申请。
func (l *LLM) presetsReport() any {
	out := make([]map[string]any, 0, len(providerPresets))
	for _, p := range providerPresets {
		out = append(out, map[string]any{
			"key":      p.Key,
			"label":    p.Label,
			"provider": p.Provider,
			"baseUrl":  p.BaseURL,
			"models":   p.Models,
			"keyUrl":   p.KeyURL,
			"needKey":  p.NeedKey,
			"hint":     p.Hint,
			"note":     p.Note,
			"group":    p.Group,
		})
	}
	return out
}

type configureParams struct {
	Provider     *string  `json:"provider,omitempty"`
	BaseURL      *string  `json:"baseUrl,omitempty"`
	APIKey       *string  `json:"apiKey,omitempty"`
	Model        *string  `json:"model,omitempty"`
	SystemPrompt *string  `json:"systemPrompt,omitempty"`
	Temperature  *float64 `json:"temperature,omitempty"`
	MaxTokens    *int     `json:"maxTokens,omitempty"`
	TimeoutMs    *int     `json:"timeoutMs,omitempty"`

	// 识图专用（可选）。传空串 = 清掉该项，让它回落到主配置。
	VisionProvider *string `json:"visionProvider,omitempty"`
	VisionBaseURL  *string `json:"visionBaseUrl,omitempty"`
	VisionAPIKey   *string `json:"visionApiKey,omitempty"`
	VisionModel    *string `json:"visionModel,omitempty"`

	// 多服务商：整份列表写回 + 指定当前生效的那条。
	//
	// 传了 providers 就按列表模式处理（界面走这条路）；不传则维持老的
	// 扁平键写法，/tool 与旧界面照旧可用。
	Providers *[]providerEntry `json:"providers,omitempty"`
	Active    *string          `json:"active,omitempty"`
}

// configure 把配置写进本插件的命名空间（plugin-configs/com.official.llm.json）。
//
// 不写文件、不重启：核心的 getConfig 每次都从磁盘读，所以下一次调用就生效。
func (l *LLM) configure(args json.RawMessage) (any, error) {
	var in configureParams
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}

	// 列表模式：一次写回全部服务商与当前生效项。
	if in.Providers != nil {
		list, err := normalizeProviderEntries(*in.Providers)
		if err != nil {
			return nil, err
		}
		list = l.mergeProviderKeys(list)
		active := ""
		if in.Active != nil {
			active = strings.TrimSpace(*in.Active)
		}
		if err := l.saveProviders(list, active); err != nil {
			return nil, plugin.Errorf(plugin.CodeInternalError, "写入服务商列表失败: %v", err)
		}
		cfg := l.settings()
		activeID := l.ctx.GetConfigString("activeProvider", "")
		l.ctx.Log("info", "LLM 服务商列表已更新", map[string]any{
			"count": len(list), "active": activeID, "provider": cfg.Provider, "model": cfg.Model,
		})
		return map[string]any{
			"ok":      true,
			"updated": len(list) + 1,
			"active":  activeID,
			"current": map[string]any{
				"provider": cfg.Provider, "baseUrl": cfg.BaseURL,
				"model": cfg.Model, "apiKey": maskKey(cfg.APIKey),
			},
		}, nil
	}

	type kv struct {
		key   string
		value any
	}
	var updates []kv
	if in.Provider != nil {
		p := strings.ToLower(strings.TrimSpace(*in.Provider))
		if _, ok := providers[p]; !ok {
			return nil, plugin.Errorf(plugin.CodeInvalidParams,
				"未知 provider %q（可选 mock / openai / ollama / openai-legacy）", p)
		}
		updates = append(updates, kv{"provider", p})
	}
	if in.BaseURL != nil {
		updates = append(updates, kv{"baseUrl", strings.TrimSpace(*in.BaseURL)})
	}
	if in.APIKey != nil {
		updates = append(updates, kv{"apiKey", strings.TrimSpace(*in.APIKey)})
	}
	if in.Model != nil {
		updates = append(updates, kv{"model", strings.TrimSpace(*in.Model)})
	}
	if in.SystemPrompt != nil {
		updates = append(updates, kv{"systemPrompt", *in.SystemPrompt})
	}
	if in.Temperature != nil {
		updates = append(updates, kv{"temperature", *in.Temperature})
	}
	if in.MaxTokens != nil {
		updates = append(updates, kv{"maxTokens", *in.MaxTokens})
	}
	if in.TimeoutMs != nil {
		updates = append(updates, kv{"timeoutMs", *in.TimeoutMs})
	}
	// 识图配置：空串是"显式清空"，所以这里用 != nil 判断，不能只看内容非空
	if in.VisionProvider != nil {
		p := strings.ToLower(strings.TrimSpace(*in.VisionProvider))
		if p != "" {
			if _, ok := providers[p]; !ok {
				return nil, plugin.Errorf(plugin.CodeInvalidParams,
					"未知 provider %q（可选 mock / openai / ollama / openai-legacy）", p)
			}
		}
		updates = append(updates, kv{"visionProvider", p})
	}
	if in.VisionBaseURL != nil {
		updates = append(updates, kv{"visionBaseUrl", strings.TrimSpace(*in.VisionBaseURL)})
	}
	if in.VisionAPIKey != nil {
		updates = append(updates, kv{"visionApiKey", strings.TrimSpace(*in.VisionAPIKey)})
	}
	if in.VisionModel != nil {
		updates = append(updates, kv{"visionModel", strings.TrimSpace(*in.VisionModel)})
	}
	if len(updates) == 0 {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"没有要修改的字段（可用：provider / baseUrl / apiKey / model / systemPrompt / "+
				"temperature / maxTokens / timeoutMs / visionProvider / visionBaseUrl / visionApiKey / visionModel）")
	}

	for _, u := range updates {
		if err := l.ctx.SetConfig(u.key, u.value); err != nil {
			return nil, plugin.Errorf(plugin.CodeInternalError, "写入配置 %s 失败: %v", u.key, err)
		}
	}

	// 只改扁平键时，把改动同步进"当前服务商"那条列表配置，
	// 否则界面上的卡片会显示旧值，与真正生效的配置对不上。
	if len(l.storedProviders()) > 0 {
		fields := map[string]bool{}
		var patch providerEntry
		if in.Provider != nil {
			fields["provider"] = true
			patch.Provider = strings.ToLower(strings.TrimSpace(*in.Provider))
		}
		if in.BaseURL != nil {
			fields["baseUrl"] = true
			patch.BaseURL = strings.TrimRight(strings.TrimSpace(*in.BaseURL), "/")
		}
		if in.APIKey != nil && !strings.Contains(*in.APIKey, "****") {
			fields["apiKey"] = true
			patch.APIKey = strings.TrimSpace(*in.APIKey)
		}
		if in.Model != nil {
			fields["model"] = true
			patch.Model = strings.TrimSpace(*in.Model)
		}
		if in.SystemPrompt != nil {
			fields["systemPrompt"] = true
			patch.SystemPrompt = *in.SystemPrompt
		}
		if in.VisionModel != nil {
			fields["visionModel"] = true
			patch.VisionModel = strings.TrimSpace(*in.VisionModel)
		}
		if len(fields) > 0 {
			l.patchActiveEntry(patch, fields)
		}
	}

	cfg := l.settings()
	// 只回显掩码后的密钥，避免密钥在日志/界面里被打印出来
	l.ctx.Log("info", "LLM 配置已更新", map[string]any{
		"provider": cfg.Provider, "baseUrl": cfg.BaseURL, "model": cfg.Model, "hasKey": cfg.APIKey != "",
	})
	return map[string]any{
		"ok":      true,
		"updated": len(updates),
		"current": map[string]any{
			"provider": cfg.Provider, "baseUrl": cfg.BaseURL,
			"model": cfg.Model, "apiKey": maskKey(cfg.APIKey),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// 配置解析
//
// 优先级：插件配置 > 环境变量 > provider 默认值。
// 密钥也可以放在秘密存储插件里（规范 4.4 方案 B），装了 secrets.v1 就自动生效。
// ---------------------------------------------------------------------------

type settings struct {
	Provider    string
	BaseURL     string
	APIKey      string
	Model       string
	Temperature *float64
	MaxTokens   int
	TimeoutMs   int
	System      string

	// 识图专用配置（可选）。任何一项留空 = 复用上面对应的那一项。
	//
	// 为什么分开：便宜快的纯文本模型不会看图，视觉模型又通常更贵。
	// 分开之后，日常聊天用便宜的，只有真的发了图才切到视觉模型。
	VisionProvider string
	VisionBaseURL  string
	VisionAPIKey   string
	VisionModel    string
}

// withVision 返回"这次请求带了图片，该用哪套配置"。
//
// 没配视觉模型就原样返回 —— 行为与从前完全一致（不改变任何现有部署）。
func (s settings) withVision() settings {
	if strings.TrimSpace(s.VisionModel) == "" {
		return s
	}
	out := s
	out.Model = s.VisionModel
	switched := false
	if s.VisionProvider != "" && s.VisionProvider != s.Provider {
		out.Provider = s.VisionProvider
		switched = true
	}
	if s.VisionBaseURL != "" {
		out.BaseURL = s.VisionBaseURL
	} else if switched {
		// 换了 provider 却没给地址：用新 provider 的默认地址，
		// 否则会拿 OpenAI 的 model 名去请求 DeepSeek 的网关。
		out.BaseURL = defaultBaseURL(out.Provider)
	}
	if s.VisionAPIKey != "" {
		out.APIKey = s.VisionAPIKey
	}
	return out
}

func (l *LLM) settings() settings {
	s := l.flatSettings()
	// 有列表配置时，以"当前生效的服务商"为准；扁平键只提供全局参数
	// （temperature / maxTokens / timeoutMs）和缺省兜底。
	if list := l.storedProviders(); len(list) > 0 {
		if e, ok := l.pickActive(list); ok {
			s.Provider = strings.ToLower(strings.TrimSpace(e.Provider))
			s.BaseURL = strings.TrimSpace(e.BaseURL)
			s.APIKey = strings.TrimSpace(e.APIKey)
			s.Model = strings.TrimSpace(e.Model)
			if v := strings.TrimSpace(e.VisionModel); v != "" {
				s.VisionModel = v
			}
			if v := strings.TrimSpace(e.SystemPrompt); v != "" {
				s.System = e.SystemPrompt
			}
		}
	}
	if s.APIKey == "" {
		s.APIKey = l.secretKey()
	}
	if s.Provider == "" {
		// 有密钥就默认走 OpenAI 兼容接口，否则用离线 mock —— 两种情况都能开箱即用
		if s.APIKey != "" {
			s.Provider = "openai"
		} else {
			s.Provider = "mock"
		}
	}
	if s.TimeoutMs <= 0 {
		s.TimeoutMs = 120000
	}
	if s.BaseURL == "" {
		s.BaseURL = defaultBaseURL(s.Provider)
	}
	if s.Model == "" {
		s.Model = defaultModel(s.Provider)
	}
	return s
}

// flatSettings 只读老的扁平配置，不做任何默认值推断。
func (l *LLM) flatSettings() settings {
	s := settings{
		Provider:  strings.ToLower(strings.TrimSpace(l.ctx.Pick("provider", "AIWORK_LLM_PROVIDER"))),
		BaseURL:   strings.TrimSpace(l.ctx.Pick("baseUrl", "AIWORK_LLM_BASE_URL", "OPENAI_BASE_URL")),
		APIKey:    strings.TrimSpace(l.ctx.Pick("apiKey", "AIWORK_LLM_API_KEY", "OPENAI_API_KEY", "DEEPSEEK_API_KEY")),
		Model:     strings.TrimSpace(l.ctx.Pick("model", "AIWORK_LLM_MODEL")),
		MaxTokens: l.ctx.GetConfigInt("maxTokens", 0),
		TimeoutMs: l.ctx.GetConfigInt("timeoutMs", 120000),
		System:    l.ctx.GetConfigString("systemPrompt", ""),

		VisionProvider: strings.ToLower(strings.TrimSpace(
			l.ctx.Pick("visionProvider", "AIWORK_LLM_VISION_PROVIDER"))),
		VisionBaseURL: strings.TrimSpace(
			l.ctx.Pick("visionBaseUrl", "AIWORK_LLM_VISION_BASE_URL")),
		VisionAPIKey: strings.TrimSpace(
			l.ctx.Pick("visionApiKey", "AIWORK_LLM_VISION_API_KEY")),
		VisionModel: strings.TrimSpace(
			l.ctx.Pick("visionModel", "AIWORK_LLM_VISION_MODEL")),
	}
	if v, err := l.ctx.GetConfig("temperature"); err == nil && len(v) > 0 {
		var f float64
		if json.Unmarshal(v, &f) == nil {
			s.Temperature = &f
		}
	}
	return s
}

// secretKey 尝试从秘密存储插件取密钥。没装 secrets.v1 时核心返回 -32000，这里静默忽略。
func (l *LLM) secretKey() string {
	raw, err := l.ctx.GetSecret("llm.apiKey")
	if err != nil || len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	// 有的实现直接返回 {"value": "..."}
	var obj struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Value
	}
	return ""
}

func defaultBaseURL(provider string) string {
	switch provider {
	case "openai":
		return "https://api.openai.com/v1"
	case "ollama":
		return "http://127.0.0.1:11434"
	default:
		return ""
	}
}

func defaultModel(provider string) string {
	switch provider {
	case "openai":
		return "gpt-4o-mini"
	case "ollama":
		return "llama3.1"
	default:
		return "mock-1"
	}
}

// maskKey 只用于展示，绝不回显完整密钥。
func maskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// ---------------------------------------------------------------------------
// 输入解码：对调用方尽量宽容
// ---------------------------------------------------------------------------

// decodeMessages 解析 messages 数组。content 不是字符串时按 JSON 原样保留，
// 避免调用方传数字 / null 就直接报错。
func decodeMessages(raw json.RawMessage) ([]Message, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "messages 必须是数组: %v", err)
	}
	out := make([]Message, 0, len(items))
	for i, it := range items {
		var m Message
		if v, ok := it["role"]; ok {
			_ = json.Unmarshal(v, &m.Role)
		}
		m.Role = strings.ToLower(strings.TrimSpace(m.Role))
		if m.Role == "" {
			m.Role = "user"
		}
		m.Content = looseString(it["content"])
		if v, ok := it["parts"]; ok {
			if err := json.Unmarshal(v, &m.Parts); err != nil {
				return nil, plugin.Errorf(plugin.CodeInvalidParams, "messages[%d].parts 非法: %v", i, err)
			}
		}
		if v, ok := it["toolCallId"]; ok {
			_ = json.Unmarshal(v, &m.ToolCallID)
		}
		if v, ok := it["name"]; ok {
			_ = json.Unmarshal(v, &m.Name)
		}
		if v, ok := it["toolCalls"]; ok {
			if err := json.Unmarshal(v, &m.ToolCalls); err != nil {
				return nil, plugin.Errorf(plugin.CodeInvalidParams, "messages[%d].toolCalls 非法: %v", i, err)
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// decodeTools 兼容两种工具定义形状：
//
//	内部契约： {"name":"read_file","description":"…","schema":{…}}
//	原生形状： {"type":"function","function":{"name":"read_file","parameters":{…}}}
//
// 所以 agent 传内部形状、外部直接贴 OpenAI 形状都能用。
func decodeTools(raw json.RawMessage) ([]ToolSpec, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "tools 必须是数组: %v", err)
	}
	out := make([]ToolSpec, 0, len(items))
	for i, it := range items {
		if inner, ok := it["function"]; ok {
			var f map[string]json.RawMessage
			if json.Unmarshal(inner, &f) == nil {
				it = f
			}
		}
		var t ToolSpec
		if v, ok := it["name"]; ok {
			_ = json.Unmarshal(v, &t.Name)
		}
		if v, ok := it["description"]; ok {
			_ = json.Unmarshal(v, &t.Description)
		}
		for _, key := range []string{"schema", "parameters", "input_schema"} {
			if v, ok := it[key]; ok {
				t.Schema = v
				break
			}
		}
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "tools[%d] 缺少 name", i)
		}
		out = append(out, t)
	}
	return out, nil
}

// looseString 容忍 content 不是字符串。
func looseString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func main() {
	if err := plugin.Serve(&LLM{}); err != nil {
		plugin.Logf("error", "LLM 插件退出: %v", err)
		os.Exit(1)
	}
}
