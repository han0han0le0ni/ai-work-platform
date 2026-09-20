// Command agent 是官方 Agent 插件（provides: agent.v1）。
//
// 它就是规范 4.2 那张链路图里「Agent 插件」那一格，负责把「会聊天」变成「会干活」：
//
//	取工具清单 → 调 llm.v1.chat（带 tools）
//	  → 模型要求调工具？ → toolExecutor.v1.execute → 结果作为 tool 消息追加 → 回到上面
//	  → 没有工具调用了 → 返回最终回答
//
// 三个刻意的设计取舍：
//
//  1. **缺插件不崩**：没有 toolExecutor.v1 时自动退化成纯聊天；只有连 llm.v1 都没有才报错。
//  2. **有预算**：单次对话有步数上限和时间预算。模型陷入「调工具 → 再调同一个工具」的
//     死循环是常态，没有预算会一直烧 token。核心默认 5 分钟就掐断转发，这里必须更早收手。
//  3. **错误喂回模型**：工具失败不直接抛给用户，而是把错误信息作为 tool 结果交回去，
//     让模型自己改参数重试 —— 这才是工具调用真正有用的地方。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"aiwork/sdk/go/plugin"
)

const (
	serviceName = "agent.v1"
	llmService  = "llm.v1"
	toolService = "toolExecutor.v1"

	defaultMaxSteps  = 6
	defaultTimeoutMs = 240000 // 核心转发 5 分钟掐断，这里留出余量提前收手
	defaultToolLimit = 8000   // 单条工具结果喂回模型的字符上限
)

// Unlimited 是不限制预算的标记值。
//
// 步数与时间都可以设成它：模型一直调工具是常态，但"最多几轮"该由用户决定，
// 不该由插件写死。设成不限制就等于把收手的判断完全交给模型自己。
const Unlimited = -1

const defaultSystemPrompt = `你是运行在「AI 工作平台」里的助手。你可以调用工具来完成任务。

规则：
1. 需要读取文件、写入文件、查看目录、获取当前时间时，先调用工具，不要凭记忆编造内容。
2. 工具返回错误时，看懂原因后调整参数重试一次；仍然失败就如实告诉用户。
3. 用简洁的中文回答，不要复述工具的原始 JSON，除非用户明确想看。`

// ---------------------------------------------------------------------------
// 内部消息契约（与 llm.v1 一致；插件是独立进程，类型各自定义）
// ---------------------------------------------------------------------------

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Parts      []Part     `json:"parts,omitempty"` // 多模态：与 llm.v1 的形状一致
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// Part 是消息里的一段内容：文字，或一张图片。形状与 llm.v1 保持一致，
// agent 只是透传 —— 它自己不理解图片，交给模型看。
type Part struct {
	Type     string `json:"type"` // text / image
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"imageUrl,omitempty"` // data:image/png;base64,xxx 或 http(s) 地址
}

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

type usage struct {
	PromptTokens     int `json:"promptTokens,omitempty"`
	CompletionTokens int `json:"completionTokens,omitempty"`
	TotalTokens      int `json:"totalTokens,omitempty"`
}

func (u *usage) add(o usage) {
	u.PromptTokens += o.PromptTokens
	u.CompletionTokens += o.CompletionTokens
	u.TotalTokens += o.TotalTokens
}

// llmChatResult 是 llm.v1.chat 的响应。
type llmChatResult struct {
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"toolCalls"`
	FinishReason string     `json:"finishReason"`
	Provider     string     `json:"provider"`
	Model        string     `json:"model"`
	Usage        usage      `json:"usage"`
	// Reasoning 是思考模型的思考过程（reasoning_content）。
	// 它不参与编排，只用来让界面显示"它现在在想什么"。
	Reasoning string `json:"reasoning"`
}

// Step 记录一次工具调用，最终随应答一起返回，界面可以直接展示「它到底干了什么」。
type Step struct {
	Index  int             `json:"index"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Ms     int64           `json:"ms"`
}

// Agent 实现 plugin.Plugin。
type Agent struct {
	ctx *plugin.Context
}

// Activate 注册 agent.v1。
func (a *Agent) Activate(ctx *plugin.Context) error {
	a.ctx = ctx
	if err := ctx.Provide(serviceName, map[string]any{
		"description": "编排 LLM 与工具：推理 → 调用工具 → 再推理，直到给出最终回答",
		"methods":     []string{"run", "tools", "info", "configure"},
	}); err != nil {
		return err
	}
	ctx.Log("info", "Agent 已就绪", map[string]any{
		"hasLLM":  consumeOK(ctx, llmService),
		"hasTool": consumeOK(ctx, toolService),
	})
	return nil
}

// Handle 处理 core.invoke。
func (a *Agent) Handle(service, method string, args json.RawMessage) (any, error) {
	if service != serviceName {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "Agent 插件不提供服务 %s", service)
	}
	switch method {
	case "run":
		return a.run(args)
	case "tools":
		_, names, note, err := a.loadTools("")
		if err != nil {
			return nil, err
		}
		return map[string]any{"tools": names, "note": note}, nil
	case "info":
		return a.info(), nil
	case "configure":
		return a.configure(args)
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "agent.v1 不支持方法 %s", method)
	}
}

// Deactivate 清理。
func (a *Agent) Deactivate() error {
	if a.ctx != nil {
		a.ctx.Log("info", "Agent 插件退出", nil)
	}
	return nil
}

func (a *Agent) info() map[string]any {
	_, names, note, _ := a.loadTools("")
	maxSteps := a.ctx.GetConfigInt("maxSteps", defaultMaxSteps)
	timeoutMs := a.ctx.GetConfigInt("timeoutMs", defaultTimeoutMs)
	return map[string]any{
		"hasLLM":      consumeOK(a.ctx, llmService),
		"hasToolExec": consumeOK(a.ctx, toolService),
		"tools":       names,
		"toolsNote":   note,
		"maxSteps":    budgetValue(maxSteps, defaultMaxSteps),
		"timeoutMs":   budgetValue(timeoutMs, defaultTimeoutMs),
		"maxToolResultChars": budgetValue(
			a.ctx.GetConfigInt("maxToolResultChars", defaultToolLimit), defaultToolLimit),
		"systemPrompt": a.ctx.GetConfigString("systemPrompt", defaultSystemPrompt),
		"unlimited":    Unlimited,
		"hint":         "maxSteps / timeoutMs / maxToolResultChars 设为 -1 表示不限制；用 configure 修改，立即生效且重启后仍记得",
	}
}

// budgetValue 把配置值翻成人能读的返回值：-1 就明说"不限制"。
func budgetValue(configured, def int) any {
	if configured == Unlimited {
		return "不限制"
	}
	if configured <= 0 {
		return def
	}
	return configured
}

// configure 调整编排预算。
//
//	{"maxSteps":12}        最多 12 轮工具调用
//	{"maxSteps":-1}        不限制轮数（由模型自己决定何时收手）
//	{"timeoutMs":-1}       不限制时长
//
// 只写传进来的字段，没传的保持原样 —— 和 llm.v1.configure 的手感一致。
func (a *Agent) configure(args json.RawMessage) (any, error) {
	var in struct {
		MaxSteps  *int    `json:"maxSteps"`
		TimeoutMs *int    `json:"timeoutMs"`
		ToolLimit *int    `json:"maxToolResultChars"`
		System    *string `json:"systemPrompt"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}

	updated := 0
	set := func(key string, v any) error {
		if err := a.ctx.SetConfig(key, v); err != nil {
			return err
		}
		updated++
		return nil
	}
	if in.MaxSteps != nil {
		if err := set("maxSteps", *in.MaxSteps); err != nil {
			return nil, err
		}
	}
	if in.TimeoutMs != nil {
		if err := set("timeoutMs", *in.TimeoutMs); err != nil {
			return nil, err
		}
	}
	if in.ToolLimit != nil {
		if err := set("maxToolResultChars", *in.ToolLimit); err != nil {
			return nil, err
		}
	}
	if in.System != nil {
		if err := set("systemPrompt", *in.System); err != nil {
			return nil, err
		}
	}
	if updated == 0 {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"没有可更新的字段（可用：maxSteps / timeoutMs / maxToolResultChars / systemPrompt）")
	}

	info := a.info()
	return map[string]any{
		"updated":   updated,
		"maxSteps":  info["maxSteps"],
		"timeoutMs": info["timeoutMs"],
		"note":      "已保存并立即生效（不需要重启）",
	}, nil
}

// ---------------------------------------------------------------------------
// run：编排主循环
// ---------------------------------------------------------------------------

type runParams struct {
	Messages    json.RawMessage `json:"messages"`
	System      string          `json:"system,omitempty"`
	Model       string          `json:"model,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxSteps    int             `json:"maxSteps,omitempty"`
	TimeoutMs   int             `json:"timeoutMs,omitempty"`
	Mode        string          `json:"mode,omitempty"` // "chat" = 不使用工具，纯聊天
	// Attachments 是本次对话带上来的附件（文件 / 图片 / 视频）。
	// agent 不理解内容，只负责把它翻译成模型看得懂的形状。
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment 是一个随消息一起发出的附件。
type Attachment struct {
	Type    string `json:"type"`              // text / image / file
	Name    string `json:"name,omitempty"`    // 原始文件名
	Text    string `json:"text,omitempty"`    // type=text：文件内容（已读出来）
	DataURL string `json:"dataUrl,omitempty"` // type=image：data:image/png;base64,xxx
	Size    int64  `json:"size,omitempty"`    // 原始字节数，用于告知模型与用户
}

func (a *Agent) run(args json.RawMessage) (any, error) {
	var in runParams
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}

	messages, err := decodeMessages(in.Messages)
	if err != nil {
		return nil, err
	}
	// 附件并入最后一条用户消息：模型要"同时"看到问题本身和附上的内容，
	// 拆成两条消息它往往只盯着后一条看。
	messages = attachToLastUser(messages, in.Attachments)
	if len(in.Attachments) > 0 {
		a.ctx.Log("info", "本次携带附件", map[string]any{
			"count": len(in.Attachments), "types": attachTypes(in.Attachments),
		})
	}
	if len(messages) == 0 {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "messages 不能为空")
	}

	if _, err := a.ctx.Consume(llmService); err != nil {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound,
			"agent 需要 llm.v1 才能工作：%s（请安装 com.official.llm 插件）", reasonOf(err))
	}

	maxSteps, stepsUnlimited := a.limit(in.MaxSteps, "maxSteps", defaultMaxSteps)
	timeoutMs, timeUnlimited := a.limit(in.TimeoutMs, "timeoutMs", defaultTimeoutMs)
	var deadline time.Time
	if !timeUnlimited {
		deadline = time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	}

	system := strings.TrimSpace(in.System)
	if system == "" {
		system = a.ctx.GetConfigString("systemPrompt", defaultSystemPrompt)
	}

	tools, toolNames, toolNote, err := a.loadTools(in.Mode)
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		a.ctx.Log("info", fmt.Sprintf("本次编排携带 %d 个工具", len(tools)), map[string]any{
			"tools": strings.Join(toolNames, ","),
		})
	}

	start := time.Now()
	var (
		steps    []Step
		total    usage
		provider string
		model    string
		content  string
		stopped  = "done"
	)

	for round := 0; ; round++ {
		if !timeUnlimited && time.Now().After(deadline) {
			stopped = "timeout"
			break
		}

		// 调模型之前先说一声：这一步动辄几十秒，界面上必须有东西可看，
		// 否则用户只看到一个转圈，不知道是卡住了还是在思考。
		_ = a.ctx.Emit("agent.think", map[string]any{"round": round, "phase": "start"})

		chatStart := time.Now()
		out, err := a.chat(messages, tools, system, in)
		if err != nil {
			_ = a.ctx.Emit("agent.think", map[string]any{
				"round": round, "phase": "error", "error": err.Error(),
			})
			return nil, err
		}
		provider, model = out.Provider, out.Model
		total.add(out.Usage)

		_ = a.ctx.Emit("agent.think", map[string]any{
			"round":     round,
			"phase":     "done",
			"model":     out.Model,
			"ms":        time.Since(chatStart).Milliseconds(),
			"thinking":  clipForView(out.Reasoning),
			"text":      clipForView(out.Content),
			"toolCalls": len(out.ToolCalls),
		})

		_ = a.ctx.Emit("agent.step", map[string]any{
			"round":        round,
			"toolCalls":    len(out.ToolCalls),
			"finishReason": out.FinishReason,
		})

		if len(out.ToolCalls) == 0 {
			content, stopped = out.Content, "done"
			break
		}
		if len(tools) == 0 {
			// 模型想调工具，但没有工具可用：把它的文本兜住，别丢
			content, stopped = out.Content, "no_tools"
			break
		}
		if !stepsUnlimited && round >= maxSteps {
			content, stopped = out.Content, "max_steps"
			break
		}

		// 先把 assistant 的工具调用记进上下文，再逐个执行并追加 tool 结果
		messages = append(messages, Message{
			Role:      "assistant",
			Content:   out.Content,
			ToolCalls: out.ToolCalls,
		})
		for _, call := range out.ToolCalls {
			st, msg := a.execTool(round, call)
			steps = append(steps, st)
			messages = append(messages, msg)
		}
	}

	if steps == nil {
		steps = []Step{}
	}
	content = a.closeOut(content, stopped, maxSteps)

	a.ctx.Log("info", "编排结束", map[string]any{
		"stopped": stopped, "steps": len(steps), "ms": time.Since(start).Milliseconds(),
	})
	_ = a.ctx.Emit("agent.done", map[string]any{
		"stopped": stopped, "steps": len(steps), "ms": time.Since(start).Milliseconds(),
	})

	return map[string]any{
		"content":   content,
		"steps":     steps,
		"tools":     toolNames,
		"usage":     total,
		"provider":  provider,
		"model":     model,
		"stopped":   stopped,
		"ms":        time.Since(start).Milliseconds(),
		"toolsNote": toolNote,
	}, nil
}

// attachToLastUser 把附件并入最后一条 user 消息。
//
// 文本附件：把文件名与内容拼进正文（模型读正文就够了）。
// 图片附件：作为 image part 挂上去，让模型真的"看见"。
// 其余类型（视频等）：正文里留一行说明，至少让模型知道有这么个东西。
func attachToLastUser(msgs []Message, atts []Attachment) []Message {
	if len(atts) == 0 {
		return msgs
	}

	var text strings.Builder
	var parts []Part
	for _, at := range atts {
		name := strings.TrimSpace(at.Name)
		if name == "" {
			name = "附件"
		}
		switch strings.ToLower(strings.TrimSpace(at.Type)) {
		case "image":
			if at.DataURL != "" {
				// 图片**只放 part，绝不往正文写文件名**。
				//
				// 踩过的坑：早先这里写了 "[图片附件: cat.png]"，模型于是把 cat.png
				// 当成"工作区里的一个文件"，掉头去 list_dir 找它 —— 明明图就贴在
				// 这条消息里。文件名对模型没有意义（它要的是像素），
				// 而历史记录里的附件名由 conversation/session 那层负责标注。
				parts = append(parts, Part{Type: "image", ImageURL: at.DataURL})
				continue
			}
		case "text":
			if strings.TrimSpace(at.Text) != "" {
				text.WriteString("\n\n[文件 " + name + "]\n" + at.Text)
				continue
			}
		}
		// 视频 / 音频 / 二进制：模型看不了内容，只留元数据
		text.WriteString("\n[附件: " + name + "，类型 " + at.Type +
			"，大小 " + humanSize(at.Size) + "。当前模型无法直接解析其内容，可就文件名等信息作答]")
	}

	idx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return append(msgs, Message{
			Role:    "user",
			Content: strings.TrimSpace(text.String()),
			Parts:   parts,
		})
	}
	msgs[idx].Content += text.String()
	if len(parts) > 0 {
		msgs[idx].Parts = append(msgs[idx].Parts, parts...)
	}
	return msgs
}

func attachTypes(atts []Attachment) string {
	seen := make([]string, 0, len(atts))
	for _, a := range atts {
		seen = append(seen, a.Type)
	}
	return strings.Join(seen, ",")
}

// humanSize 把字节数说成一眼能读的大小。
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	sizes := "KMGTPE"
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < len(sizes)-1; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), sizes[exp])
}

// closeOut 在异常收场时补一句人能看懂的话，避免界面上一片空白。
func (a *Agent) closeOut(content, stopped string, maxSteps int) string {
	switch stopped {
	case "timeout":
		if content == "" {
			return "（本次对话超过了时间预算，已中止。可以再问一次，或把问题拆小。）"
		}
		return content + "\n\n（本次对话超过了时间预算，先停在这里。）"
	case "max_steps":
		return content + fmt.Sprintf("\n\n（已达到单次对话的工具调用上限 %d 步，先停在这里。再问一次可以继续。）", maxSteps)
	case "no_tools":
		if content == "" {
			return "（模型想调用工具，但当前没有可用的工具执行器。装上 com.official.tool-executor 即可。）"
		}
		return content
	default:
		if strings.TrimSpace(content) == "" {
			return "（模型没有返回文本内容。）"
		}
		return content
	}
}

// chat 调一次 llm.v1.chat。
func (a *Agent) chat(messages []Message, tools []ToolSpec, system string, in runParams) (*llmChatResult, error) {
	params := map[string]any{
		"messages": messages,
		"system":   system,
	}
	if len(tools) > 0 {
		params["tools"] = tools
	}
	if in.Model != "" {
		params["model"] = in.Model
	}
	if in.Temperature != nil {
		params["temperature"] = *in.Temperature
	}
	if n := a.ctx.GetConfigInt("maxTokens", 0); n > 0 {
		params["maxTokens"] = n
	}

	raw, err := a.ctx.Invoke(llmService, "chat", params)
	if err != nil {
		return nil, err
	}
	var out llmChatResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "解析 llm.v1 响应失败: %v", err)
	}
	return &out, nil
}

// execTool 执行一次工具调用，返回给界面看的 Step 和喂回模型的 tool 消息。
func (a *Agent) execTool(round int, call ToolCall) (Step, Message) {
	st := Step{Index: round + 1, Tool: call.Name, Args: call.Args}
	start := time.Now()

	_ = a.ctx.Emit("chat.tool.before", map[string]any{"tool": call.Name, "args": call.Args})

	raw, err := a.ctx.Invoke(toolService, "execute", map[string]any{
		"name": call.Name,
		"args": call.Args,
	})
	st.Ms = time.Since(start).Milliseconds()

	if err != nil {
		// 工具失败：把错误当成工具结果喂回去，让模型自己修正，而不是直接失败
		st.Error = err.Error()
		_ = a.ctx.Emit("chat.tool.after", map[string]any{
			"tool": call.Name, "ok": false, "error": st.Error, "ms": st.Ms,
		})
		return st, Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    string(mustJSON(map[string]any{"error": st.Error})),
		}
	}

	// toolExecutor 的返回是 {tool, result, via, ms}，真正要喂给模型的是 result
	payload := raw
	var wrapper struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &wrapper) == nil && len(wrapper.Result) > 0 {
		payload = wrapper.Result
	}
	if !json.Valid(payload) {
		payload = mustJSON(map[string]any{"result": string(payload)})
	}
	st.Result = payload

	text := a.clipToolResult(string(payload))
	_ = a.ctx.Emit("chat.tool.after", map[string]any{
		"tool": call.Name, "ok": true, "ms": st.Ms, "chars": len(text),
	})
	return st, Message{
		Role:       "tool",
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    text,
	}
}

// viewClipRunes 是事件里「思考 / 正文」的展示上限。
const viewClipRunes = 300

// clipForView 把思考或正文裁成界面放得下的长度。
//
// 事件是广播的，所有订阅者都会收到一份：几千字的思考原样丢上总线既浪费，
// 界面那几行也摆不下 —— 截到"看得出它在想什么"就够了。
func clipForView(s string) string {
	// 思考是整段整段写的，中间全是换行。事件是给人看的"现在在想什么"，
	// 换行留着只会让订阅方（界面）多画出几行空行 —— 先压成一行。
	s = strings.Join(strings.Fields(s), " ")
	rs := []rune(s)
	if len(rs) <= viewClipRunes {
		return s
	}
	return string(rs[:viewClipRunes]) + "…"
}

// clipToolResult 限制单条工具结果喂回模型的长度。
//
// 读一个几 MB 的文件会把上下文直接撑爆，而模型真正需要的往往只是开头。
func (a *Agent) clipToolResult(s string) string {
	limit := a.ctx.GetConfigInt("maxToolResultChars", defaultToolLimit)
	if limit == Unlimited {
		return s // 明确要求不限制：原样交回模型
	}
	if limit <= 0 {
		limit = defaultToolLimit
	}
	rs := []rune(s)
	if len(rs) <= limit {
		return s
	}
	return string(rs[:limit]) + fmt.Sprintf("\n…（内容过长，已截断，原始长度 %d 字符）", len(rs))
}

// loadTools 取可用工具清单。mode="chat" 或工具执行器缺失时返回空清单，不报错。
func (a *Agent) loadTools(mode string) ([]ToolSpec, []string, string, error) {
	if strings.EqualFold(strings.TrimSpace(mode), "chat") {
		return nil, []string{}, "本次为纯聊天模式，未加载工具", nil
	}
	if _, err := a.ctx.Consume(toolService); err != nil {
		return nil, []string{}, "工具执行器不可用（" + reasonOf(err) + "），本次退化为纯聊天", nil
	}

	raw, err := a.ctx.Invoke(toolService, "list", map[string]any{"availableOnly": true})
	if err != nil {
		return nil, []string{}, "取工具清单失败：" + err.Error(), nil
	}

	var out struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"schema"`
			Available   bool            `json:"available"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, []string{}, "解析工具清单失败：" + err.Error(), nil
	}

	specs := make([]ToolSpec, 0, len(out.Tools))
	names := make([]string, 0, len(out.Tools))
	for _, t := range out.Tools {
		if !t.Available || t.Name == "" {
			continue
		}
		specs = append(specs, ToolSpec{Name: t.Name, Description: t.Description, Schema: t.Schema})
		names = append(names, t.Name)
	}
	return specs, names, "", nil
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// decodeMessages 接受两种输入：消息数组，或一句纯文本。
func decodeMessages(raw json.RawMessage) ([]Message, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		if strings.TrimSpace(plain) == "" {
			return nil, nil
		}
		return []Message{{Role: "user", Content: plain}}, nil
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "messages 必须是数组或字符串: %v", err)
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
		if v, ok := it["content"]; ok {
			m.Content = looseString(v)
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

// limit 取「参数 > 配置 > 默认」。
//
// 约定：Unlimited(-1) 表示不限制，返回 (0, true)；其余返回具体数值。
// 参数传 0 表示"没指定"，才会去看配置 —— 所以 0 不能兼任"不限制"。
func (a *Agent) limit(param int, configKey string, def int) (value int, unlimited bool) {
	v := param
	if v == 0 {
		v = a.ctx.GetConfigInt(configKey, def)
	}
	if v == Unlimited {
		return 0, true
	}
	if v <= 0 {
		v = def
	}
	return v, false
}

func consumeOK(ctx *plugin.Context, service string) bool {
	_, err := ctx.Consume(service)
	return err == nil
}

func mustJSON(v any) json.RawMessage {
	blob, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return blob
}

func reasonOf(err error) string {
	switch {
	case err == nil:
		return ""
	case plugin.IsCode(err, plugin.CodeServiceNotFound):
		return "插件未安装"
	case plugin.IsCode(err, plugin.CodeProviderDead):
		return "提供者已退出"
	case plugin.IsCode(err, plugin.CodeDependencyMissing):
		return "依赖缺失"
	case plugin.IsCode(err, plugin.CodeAPIVersionIncompatible):
		return "版本不兼容"
	default:
		return err.Error()
	}
}

func main() {
	if err := plugin.Serve(&Agent{}); err != nil {
		plugin.Logf("error", "Agent 插件退出: %v", err)
		os.Exit(1)
	}
}
