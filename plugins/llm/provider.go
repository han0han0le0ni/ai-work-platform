package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// provider 抽象
// ---------------------------------------------------------------------------

// chatReq 是一次模型请求（内部统一格式）。
type chatReq struct {
	Messages    []Message
	Tools       []ToolSpec
	Model       string
	Temperature *float64
	MaxTokens   int
	System      string
}

// chatResp 是一次模型响应（内部统一格式）。
type chatResp struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        usage

	// Reasoning 是思考模型（DeepSeek 默认开 thinking）的思考过程。
	//
	// 它一直都在响应体里（reasoning_content），但原先只在"回答为空"时被拿来
	// 解释原因，调用方拿不到 —— 于是界面上的 AI 只会转圈，看不出它在想什么。
	// 这里原样带出去，谁想知道就自己取。
	Reasoning string
}

type usage struct {
	PromptTokens     int `json:"promptTokens,omitempty"`
	CompletionTokens int `json:"completionTokens,omitempty"`
	TotalTokens      int `json:"totalTokens,omitempty"`
}

// providerFn 是某个后端的一次调用。新增后端只要往 providers 里加一项。
type providerFn func(l *LLM, req *chatReq, cfg *settings) (*chatResp, error)

var providers = map[string]providerFn{
	"mock":   mockChat,
	"openai": openaiChat,
	"ollama": ollamaChat,
}

// ---------------------------------------------------------------------------
// 出网：一律走核心的能力代理
// ---------------------------------------------------------------------------

// doRequest 经核心发起一次 HTTP 请求。payload 为 nil 时不带请求体（GET）。
//
// 特意使用 Capability 而不是 ctx.NetRequest：前者能把 timeoutMs 传下去，
// 模型推理动辄几十秒，核心默认的 30 秒网络超时是不够的。
func (l *LLM) doRequest(method, url string, payload any, headers map[string]string, timeoutMs int) (int, string, error) {
	body := ""
	if payload != nil {
		blob, err := json.Marshal(payload)
		if err != nil {
			return 0, "", plugin.Errorf(plugin.CodeInternalError, "序列化请求失败: %v", err)
		}
		body = string(blob)
	}
	if headers == nil {
		headers = map[string]string{}
	}
	if payload != nil {
		headers["Content-Type"] = "application/json; charset=utf-8"
	}

	raw, err := l.ctx.Capability("core.net.request", map[string]any{
		"method":    method,
		"url":       url,
		"headers":   headers,
		"body":      body,
		"timeoutMs": timeoutMs,
	})
	if err != nil {
		if plugin.IsCode(err, plugin.CodePermissionDenied) {
			return 0, "", plugin.Errorf(plugin.CodePermissionDenied,
				"LLM 插件出网被核心拒绝：manifest 需要声明 network 权限（当前拒绝原因见核心日志）")
		}
		return 0, "", err
	}

	var out struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, "", plugin.Errorf(plugin.CodeInternalError, "解析能力代理响应失败: %v", err)
	}
	return out.Status, out.Body, nil
}

func (l *LLM) postJSON(url string, payload any, headers map[string]string, timeoutMs int) (int, string, error) {
	return l.doRequest("POST", url, payload, headers, timeoutMs)
}

// ---------------------------------------------------------------------------
// mock：离线后端
// ---------------------------------------------------------------------------

// mockChat 是不需要网络和密钥的后端，用来把整条链路（UI → session → agent → 工具）
// 跑完整、跑可复现。它按四条规则产生回复：
//
//  1. 前面已有工具结果 → 收尾汇总（这一步让 agent 的循环能正常结束）
//  2. 用户消息以 "tool:" 开头 → 精确产生指定工具调用，用于测试
//  3. 用户消息里有“读一下 / 查看 / read + 路径” → 产生 read_file 调用
//  4. 其余情况 → 回显，并提示怎么配置真实模型
func mockChat(l *LLM, req *chatReq, cfg *settings) (*chatResp, error) {
	// provider / model 由主流程统一写进响应，这里只需要内容本身
	resp := &chatResp{FinishReason: "stop"}

	if results := toolResultContents(req.Messages); len(results) > 0 {
		resp.Content = mockSummarize(results)
		return resp, nil
	}

	text := lastUserText(req.Messages)

	if name, args, ok := mockDirective(text); ok && hasTool(req.Tools, name) {
		resp.Content = "好的，我来调用 " + name + "。"
		resp.ToolCalls = []ToolCall{{ID: "mock-call-1", Name: name, Args: args}}
		resp.FinishReason = "tool_calls"
		return resp, nil
	}

	if hasTool(req.Tools, "read_file") {
		if path, ok := mockReadIntent(text); ok {
			resp.Content = "我来读一下这个文件。"
			resp.ToolCalls = []ToolCall{{
				ID:   "mock-call-1",
				Name: "read_file",
				Args: mustJSON(map[string]any{"path": path}),
			}}
			resp.FinishReason = "tool_calls"
			return resp, nil
		}
	}

	resp.Content = mockEcho(text, req.Tools)
	return resp, nil
}

func mockEcho(text string, tools []ToolSpec) string {
	var b strings.Builder
	if text == "" {
		b.WriteString("（mock provider 收到了空消息，或者只收到了 system 提示）")
	} else {
		b.WriteString("【离线 mock 回复】我收到了：" + text + "\n")
	}
	b.WriteString("\n当前跑的是离线 mock provider —— 整条链路（UI → session → agent → toolExecutor → 工具）都是真实的，只有最外面这一次「模型推理」是本地模拟的。\n")

	if names := toolNames(tools); len(names) > 0 {
		b.WriteString("可用工具：" + strings.Join(names, "、") + "\n")
		b.WriteString("想看工具循环，可以对我说「读一下 <某个文件路径>」。\n")
	}
	b.WriteString("\n要接真实模型，任选一种：\n")
	b.WriteString("  /tool llm.v1 configure {\"provider\":\"ollama\",\"model\":\"llama3.1\"}          （本地，零密钥）\n")
	b.WriteString("  /tool llm.v1 configure {\"provider\":\"openai\",\"apiKey\":\"sk-…\"}             （OpenAI 兼容接口）\n")
	b.WriteString("也可以直接编辑 runtime/plugin-configs/com.official.llm.json，下一次调用就生效。")
	return b.String()
}

// mockSummarize 把工具结果整理成人话。这是 mock 后端的「最后一轮推理」。
func mockSummarize(items []string) string {
	var b strings.Builder
	b.WriteString("工具执行完成，结果如下：\n\n")
	for i, item := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		var obj map[string]any
		if json.Unmarshal([]byte(item), &obj) == nil {
			content, hasContent := obj["content"].(string)
			if hasContent {
				if p, ok := obj["path"].(string); ok {
					fmt.Fprintf(&b, "文件 %s：\n", p)
				}
				b.WriteString(content)
				if !strings.HasSuffix(content, "\n") {
					b.WriteString("\n")
				}
				continue
			}
		}
		b.WriteString(item)
		b.WriteString("\n")
	}
	b.WriteString("\n（以上由离线 mock provider 汇总；换成真实模型后会由模型组织语言作答。）")
	return b.String()
}

// mockReadIntentRe 从自然语言里抠出「要读的文件路径」。
//
// 路径本身可能含中文（Windows 上很常见），所以字符类不能用 [A-Za-z0-9]，
// 而是「一段不含空白、不含中文标点的连续字符」，并且要求以 .扩展名 结尾。
var mockReadIntentRe = regexp.MustCompile(
	`(?i)(?:读一下|读取|读|查看|看看|打开|read|open|cat|type)\s*[:：]?\s*([^\s，。！？；：、"'\x60]*\.[A-Za-z0-9]{1,8})`)

func mockReadIntent(text string) (string, bool) {
	m := mockReadIntentRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return "", false
	}
	return m[1], true
}

// mockDirective 解析 "tool:<名称> <json参数>" 这种精确指令。
func mockDirective(text string) (string, json.RawMessage, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), "tool:")
	if !ok {
		return "", nil, false
	}
	fields := strings.SplitN(strings.TrimSpace(rest), " ", 2)
	name := strings.TrimSpace(fields[0])
	if name == "" {
		return "", nil, false
	}
	args := json.RawMessage(`{}`)
	if len(fields) == 2 {
		candidate := strings.TrimSpace(fields[1])
		if json.Valid([]byte(candidate)) {
			args = json.RawMessage(candidate)
		}
	}
	return name, args, true
}

// ---------------------------------------------------------------------------
// openai：任何 OpenAI 兼容接口
// ---------------------------------------------------------------------------

func openaiChat(l *LLM, req *chatReq, cfg *settings) (*chatResp, error) {
	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	body := map[string]any{
		"model":    cfg.Model,
		"messages": toOpenAIMessages(req.Messages, req.System),
		"stream":   false,
	}
	if len(req.Tools) > 0 {
		body["tools"] = toFunctionTools(req.Tools)
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}

	headers := map[string]string{}
	if cfg.APIKey != "" {
		headers["Authorization"] = "Bearer " + cfg.APIKey
	}

	status, raw, err := l.postJSON(url, body, headers, cfg.TimeoutMs)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				// ReasoningContent 是思考模型（DeepSeek V4.1 默认开 thinking）的思考过程。
				// 它和 content 分开返回；不读它的后果是：思考把 token 预算吃光时，
				// content 为空，界面上就是一条空回复。
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"HTTP %d，响应不是合法 JSON：%s", status, clipRunes(raw, 300))
	}
	if payload.Error != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"模型返回错误（HTTP %d）：%s", status, payload.Error.Message)
	}
	if status < 200 || status >= 300 {
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"模型接口返回 HTTP %d：%s", status, clipRunes(raw, 300))
	}
	if len(payload.Choices) == 0 {
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"模型没有返回任何 choice（HTTP %d）：%s", status, clipRunes(raw, 300))
	}

	choice := payload.Choices[0]
	resp := &chatResp{
		Content:      choice.Message.Content,
		Reasoning:    choice.Message.ReasoningContent,
		FinishReason: choice.FinishReason,
		Usage: usage{
			PromptTokens:     payload.Usage.PromptTokens,
			CompletionTokens: payload.Usage.CompletionTokens,
			TotalTokens:      payload.Usage.TotalTokens,
		},
	}

	if err := explainEmptyReply(resp.Content, len(choice.Message.ToolCalls) > 0,
		choice.FinishReason, choice.Message.ReasoningContent, status); err != nil {
		return nil, err
	}
	for i, tc := range choice.Message.ToolCalls {
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i+1)
		}
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:   id,
			Name: tc.Function.Name,
			Args: argsToRaw(tc.Function.Arguments),
		})
	}
	if resp.FinishReason == "" {
		if len(resp.ToolCalls) > 0 {
			resp.FinishReason = "tool_calls"
		} else {
			resp.FinishReason = "stop"
		}
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// ollama：本地原生接口
// ---------------------------------------------------------------------------

func ollamaChat(l *LLM, req *chatReq, cfg *settings) (*chatResp, error) {
	url := strings.TrimRight(cfg.BaseURL, "/") + "/api/chat"
	body := map[string]any{
		"model":    cfg.Model,
		"messages": toOllamaMessages(req.Messages, req.System),
		"stream":   false,
	}
	if len(req.Tools) > 0 {
		body["tools"] = toFunctionTools(req.Tools)
	}
	options := map[string]any{}
	if req.Temperature != nil {
		options["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		options["num_predict"] = req.MaxTokens
	}
	if len(options) > 0 {
		body["options"] = options
	}

	status, raw, err := l.postJSON(url, body, nil, cfg.TimeoutMs)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"Ollama 返回 HTTP %d：%s\n提示：确认 Ollama 已在 %s 运行，且模型 %s 已 pull。",
			status, clipRunes(raw, 300), cfg.BaseURL, cfg.Model)
	}

	var payload struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		DoneReason      string `json:"done_reason"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
		Error           string `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"Ollama 响应不是合法 JSON：%s", clipRunes(raw, 300))
	}
	if payload.Error != "" {
		return nil, plugin.Errorf(plugin.CodeInternalError, "Ollama 返回错误：%s", payload.Error)
	}

	resp := &chatResp{
		Content: payload.Message.Content,
		Usage: usage{
			PromptTokens:     payload.PromptEvalCount,
			CompletionTokens: payload.EvalCount,
			TotalTokens:      payload.PromptEvalCount + payload.EvalCount,
		},
	}
	for i, tc := range payload.Message.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:   fmt.Sprintf("ollama-call-%d", i+1),
			Name: tc.Function.Name,
			Args: argsToRaw(string(tc.Function.Arguments)),
		})
	}
	if payload.DoneReason != "" {
		resp.FinishReason = payload.DoneReason
	} else if len(resp.ToolCalls) > 0 {
		resp.FinishReason = "tool_calls"
	} else {
		resp.FinishReason = "stop"
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// 格式翻译
// ---------------------------------------------------------------------------

// toFunctionTools 转成 OpenAI / Ollama 共用的 function 工具形状。
func toFunctionTools(tools []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Schema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  params,
			},
		})
	}
	return out
}

func toOpenAIMessages(msgs []Message, system string) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	if strings.TrimSpace(system) != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolCallID,
				"content":      m.Content,
			})
		case "assistant":
			item := map[string]any{"role": "assistant", "content": m.Content}
			if len(m.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					calls = append(calls, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							"name":      tc.Name,
							"arguments": argString(tc.Args),
						},
					})
				}
				item["tool_calls"] = calls
			}
			out = append(out, item)
		default:
			item := map[string]any{"role": normalizeRole(m.Role)}
			item["content"] = openAIContent(m)
			out = append(out, item)
		}
	}
	return out
}

// openAIContent 决定一条消息发给 OpenAI 时的 content 形状。
//
// 没有图片就仍是字符串（兼容老调用方与不支持多模态的模型）；
// 有图片时改成数组：[{type:text,…},{type:image_url,…}]，这是 OpenAI 的多模态写法。
func openAIContent(m Message) any {
	text := strings.TrimSpace(m.Content)
	imgs := imageParts(m)
	if len(imgs) == 0 {
		if text == "" {
			return ""
		}
		return text
	}
	parts := make([]map[string]any, 0, len(imgs)+1)
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, u := range imgs {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": u},
		})
	}
	return parts
}

// countImages 数一遍这次请求总共带了几张图。
func countImages(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(imageParts(m))
	}
	return n
}

// imageParts 取出消息里的图片地址，按出现顺序。
func imageParts(m Message) []string {
	var out []string
	for _, p := range m.Parts {
		if !strings.EqualFold(p.Type, "image") {
			continue
		}
		if u := strings.TrimSpace(p.ImageURL); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// ollamaImages 取出图片里的裸 base64 —— Ollama 的 images 字段要的是它，
// 而不是 data:image/png;base64, 这一整串前缀。
func ollamaImages(m Message) []string {
	var out []string
	for _, u := range imageParts(m) {
		if i := strings.Index(u, ","); strings.HasPrefix(u, "data:") && i >= 0 {
			out = append(out, u[i+1:])
			continue
		}
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			continue // Ollama 只吃本地 base64
		}
		out = append(out, u)
	}
	return out
}

func toOllamaMessages(msgs []Message, system string) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	if strings.TrimSpace(system) != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			out = append(out, map[string]any{"role": "tool", "content": m.Content})
		case "assistant":
			item := map[string]any{"role": "assistant", "content": m.Content}
			if len(m.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					calls = append(calls, map[string]any{
						"function": map[string]any{
							"name":      tc.Name,
							"arguments": jsonObject(tc.Args),
						},
					})
				}
				item["tool_calls"] = calls
			}
			out = append(out, item)
		default:
			item := map[string]any{"role": normalizeRole(m.Role), "content": m.Content}
			if imgs := ollamaImages(m); len(imgs) > 0 {
				item["images"] = imgs
			}
			out = append(out, item)
		}
	}
	return out
}

// explainEmptyReply 检查"模型给了空 content"的几种情形，给出能照着做的解释。
//
// 为什么非要拦：思考模型（DeepSeek V4.1 默认开 thinking）把思考放在
// reasoning_content 里。思考用光 token 预算时 content 是空的，
// 如果原样返回，用户看到的就是一条"没反应"的空消息，且无从下手。
//
// 正常情况直接返回 nil：有正文，或者模型要去调工具（此时 content 本就为空）。
func explainEmptyReply(content string, hasToolCalls bool, finishReason, reasoning string, status int) error {
	if strings.TrimSpace(content) != "" || hasToolCalls {
		return nil
	}
	if finishReason == "length" {
		return plugin.Errorf(plugin.CodeInternalError,
			"模型输出被截断：思考过程（reasoning）占满了 token 预算，正式回答还没开始。"+
				"把 maxTokens 调大一些再试。")
	}
	if r := strings.TrimSpace(reasoning); r != "" {
		return plugin.Errorf(plugin.CodeInternalError,
			"模型只返回了思考过程，没有正式回答。思考摘录：%s", clipRunes(r, 300))
	}
	return plugin.Errorf(plugin.CodeInternalError,
		"模型返回了空回复（HTTP %d，finish_reason=%s）", status, finishReason)
}

// normalizeRole 兜底：只认识的角色原样传，其余当成 user，避免整条请求被接口拒绝。
func normalizeRole(role string) string {
	switch role {
	case "system", "user", "assistant", "tool":
		return role
	default:
		return "user"
	}
}

// argString 把工具参数转成 OpenAI 要求的「JSON 字符串」。
// 如果调用方已经传了字符串（模型自己拼的），原样保留，避免双重编码。
func argString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// argsToRaw 把模型给的参数统一成 JSON 对象。
// 有的接口给字符串（内含 JSON），有的直接给对象，这里都收。
func argsToRaw(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	// 不是合法 JSON：包一层，让工具执行器能明确报错并把错误喂回模型
	return mustJSON(map[string]any{"_raw": s})
}

// jsonObject 把参数转成对象（Ollama 要求对象而不是字符串）。
func jsonObject(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		return obj
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		var inner map[string]any
		if json.Unmarshal([]byte(s), &inner) == nil {
			return inner
		}
	}
	return map[string]any{}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func mustJSON(v any) json.RawMessage {
	blob, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return blob
}

func lastUserText(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return strings.TrimSpace(msgs[i].Content)
		}
	}
	return ""
}

func toolResultContents(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role == "tool" && strings.TrimSpace(m.Content) != "" {
			out = append(out, m.Content)
		}
	}
	return out
}

func hasTool(tools []ToolSpec, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func toolNames(tools []ToolSpec) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func clipRunes(s string, n int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= n {
		return string(rs)
	}
	return string(rs[:n]) + "…"
}
