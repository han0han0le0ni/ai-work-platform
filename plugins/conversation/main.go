// Command conversation 是官方链式对话插件（provides: conversation.v1）。
//
// 它把「对话」从一条直线变成一棵树：
//
//	发消息 → 在叶子下挂一条 user，再挂一条 assistant
//	改消息 → 在同一个父节点下挂一条**新版本**（兄弟），当前路径切过去
//	重新生成 → 在锚点的父节点下再挂一条 assistant 兄弟
//	翻版本 → 只改父节点指向第几个孩子，路径当场重算
//
// 三个刻意的设计取舍：
//
//  1. **只增不删**：版本切换从不 delete 任何节点，只改 Active 下标。旧版本连同
//     它的整棵子树都留在树里 —— 界面上「翻页隐没」，翻回去就原样重现。
//  2. **失败也留痕**：模型调用失败时保留节点，assistant 侧记一条带 Err 的节点。
//     这是点击驱动的界面，用户需要一个落点去点「重新生成」，
//     而不是让刚打的一大段话凭空消失（与 session 插件「失败不留痕」相反）。
//  3. **不持锁调模型**：一次编排可能跑几分钟，期间用户还应该能翻页、切会话。
//     所以流程拆成「锁内改树 → 无锁调模型 → 锁内落回复」三段。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"aiwork/sdk/go/plugin"
)

const (
	serviceName  = "conversation.v1"
	agentService = "agent.v1"
	llmService   = "llm.v1"
	workspaceSvc = "workspace.v1"
)

// Conversation 实现 plugin.Plugin。
//
// 一把大锁护住整棵树：树的改动都是「追加 + 改下标」，粒度小，
// 真正耗时的是调模型，而那一段刻意在锁外跑。
type Conversation struct {
	ctx *plugin.Context

	mu    sync.Mutex
	store *Store
}

// nodeView 是发给界面的一条消息。
type nodeView struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Text    string `json:"text"`
	At      string `json:"at"`
	Version int    `json:"version"` // 它是这组版本里的第几个（从 1 开始）
	Total   int    `json:"total"`   // 这组一共几个
	Via     string `json:"via,omitempty"`
	Err     string `json:"err,omitempty"`
	Trunc   bool   `json:"trunc,omitempty"`
}

// modelMessage 是发给 agent.v1 / llm.v1 的消息（与它们约定的内部格式一致）。
type modelMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Activate 注册 conversation.v1 并载入会话树。
func (c *Conversation) Activate(ctx *plugin.Context) error {
	c.ctx = ctx
	c.store = newStore()

	if err := ctx.Provide(serviceName, map[string]any{
		"description": "链式对话：消息树、版本翻页、多会话管理",
		"methods": []string{
			"list", "newSession", "deleteSession", "switch", "history",
			"send", "edit", "regenerate", "switchVersion", "share", "export", "info",
		},
	}); err != nil {
		return err
	}

	if err := c.store.load(ctx); err != nil {
		ctx.Log("warn", "读取会话树失败（将从空树开始）："+err.Error(), nil)
	}

	c.mu.Lock()
	count := len(c.store.Session)
	c.mu.Unlock()

	ctx.Log("info", "链式对话插件已就绪", map[string]any{
		"conversations": count,
		"hasAgent":      c.available(agentService),
		"hasLLM":        c.available(llmService),
		"hasWorkspace":  c.available(workspaceSvc),
	})
	return nil
}

// Handle 处理 core.invoke。
func (c *Conversation) Handle(service, method string, args json.RawMessage) (any, error) {
	if service != serviceName {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "对话插件不提供服务 %s", service)
	}
	switch method {
	case "list":
		return c.list()
	case "newSession":
		return c.newSession(args)
	case "deleteSession":
		return c.deleteSession(args)
	case "switch":
		return c.switchTo(args)
	case "history":
		return c.history(args)
	case "send":
		return c.send(args)
	case "edit":
		return c.edit(args)
	case "regenerate":
		return c.regenerate(args)
	case "switchVersion":
		return c.switchVersion(args)
	case "share":
		return c.share(args)
	case "export":
		return c.export(args)
	case "info":
		return c.info(), nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "conversation.v1 不支持方法 %s", method)
	}
}

// Deactivate 退出前再写一次，避免异常退出丢掉最后一轮。
func (c *Conversation) Deactivate() error {
	if c.ctx == nil || c.store == nil {
		return nil
	}
	c.mu.Lock()
	err := c.store.save(c.ctx)
	c.mu.Unlock()
	if err != nil {
		c.ctx.Log("warn", "退出前保存会话树失败："+err.Error(), nil)
	}
	c.ctx.Log("info", "链式对话插件退出", nil)
	return nil
}

func (c *Conversation) available(service string) bool {
	_, err := c.ctx.Consume(service)
	return err == nil
}

// ---------------------------------------------------------------------------
// 三段式：锁内改树 → 无锁调模型 → 锁内落回复
// ---------------------------------------------------------------------------

// turn 描述一次「树已经改好了，等一条回复」的中间状态。
//
// messages 在锁内就复制成普通字符串，出了锁不再引用任何树上的对象 ——
// 这样后面那次模型调用与树的并发改动彻底无关。
type turn struct {
	sessionID string
	parentID  string
	messages  []modelMessage
	system    string
	reply     bool
	payload   map[string]any
	// attachments 随本次请求一起发给模型，但不落进树。
	attachments []attachment
}

// begin 在锁内跑一次树改动，并把「该喂模型什么」一并算好带出来。
func (c *Conversation) begin(requested string, fn func(s *Session) (parentID string, reply bool, err error)) (turn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	s, err := c.store.Resolve(requested)
	if err != nil {
		return turn{}, err
	}
	parentID, reply, err := fn(s)
	if err != nil {
		return turn{}, err
	}
	t := turn{
		sessionID: s.ID,
		parentID:  parentID,
		reply:     reply,
		system:    s.System,
		payload:   c.payload(s),
	}
	if reply {
		t.messages = modelMessages(s.PathTo(parentID))
	}
	c.save()
	return t, nil
}

// finish 调模型，并把回复落成一条 assistant 节点。
func (c *Conversation) finish(t turn) (map[string]any, error) {
	content, via, gerr := c.dispatch(t.messages, t.system, t.attachments)
	errMsg := ""
	if gerr != nil {
		errMsg = gerr.Error()
		c.ctx.Log("warn", "生成回复失败："+errMsg, nil)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.store.Resolve(t.sessionID)
	if err != nil {
		return nil, err
	}
	s.AddNode(t.parentID, roleAssistant, content, via, errMsg)
	c.save()
	return c.payload(s), nil
}

// apply 跑一次不需要调模型的改动。
func (c *Conversation) apply(requested string, fn func(s *Session) error) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	s, err := c.store.Resolve(requested)
	if err != nil {
		return nil, err
	}
	if err := fn(s); err != nil {
		return nil, err
	}
	c.save()
	return c.payload(s), nil
}

// save 落盘。调用方必须持有 c.mu。
//
// 写失败只记日志不中断：树在内存里是对的，界面照常工作，
// 下次改动会再写一遍 —— 比让用户的操作直接失败友好。
func (c *Conversation) save() {
	if err := c.store.save(c.ctx); err != nil {
		c.ctx.Log("warn", "保存会话树失败："+err.Error(), nil)
	}
}

// payload 把一条会话的当前时间线打包成界面要的形状。调用方必须持有 c.mu。
func (c *Conversation) payload(s *Session) map[string]any {
	path := s.Path()
	views := make([]nodeView, 0, len(path))
	for _, n := range path {
		version, total := s.VersionOf(n.ID)
		views = append(views, nodeView{
			ID:      n.ID,
			Role:    n.Role,
			Text:    n.Text,
			At:      n.At,
			Version: version,
			Total:   total,
			Via:     n.Via,
			Err:     n.Err,
			Trunc:   n.Trunc,
		})
	}

	out := map[string]any{
		"sessionId": s.ID,
		"name":      s.Name,
		"system":    s.System,
		"current":   c.store.Current,
		"sessions":  c.store.List(),
		"path":      views,
	}
	// 末尾是一条失败的回复时，顺带说一句人话，免得界面只有个空框
	if n := len(path); n > 0 && path[n-1].Err != "" {
		out["error"] = path[n-1].Err
		out["note"] = "生成失败：" + shorten(path[n-1].Err, 80) + " · 可点这条消息的「重新生成」重试"
	}
	return out
}

// ---------------------------------------------------------------------------
// 只读
// ---------------------------------------------------------------------------

func (c *Conversation) list() (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.store.Resolve(""); err != nil {
		return nil, err
	}
	c.save()
	return map[string]any{
		"current":  c.store.Current,
		"sessions": c.store.List(),
		"limits":   c.limits(),
	}, nil
}

func (c *Conversation) history(args json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId,omitempty"`
	}
	_ = json.Unmarshal(args, &in)

	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.store.Resolve(in.SessionID)
	if err != nil {
		return nil, err
	}
	return c.payload(s), nil
}

func (c *Conversation) info() map[string]any {
	return map[string]any{
		"hasAgent":     c.available(agentService),
		"hasLLM":       c.available(llmService),
		"hasWorkspace": c.available(workspaceSvc),
		"limits":       c.limits(),
	}
}

func (c *Conversation) limits() map[string]any {
	return map[string]any{
		"maxTextRunes":       maxTextRunes,
		"maxNodesPerSession": maxNodesPerSession,
		"maxSessions":        maxSessions,
		"maxTurnsForModel":   maxTurnsForModel,
	}
}

// ---------------------------------------------------------------------------
// 会话管理
// ---------------------------------------------------------------------------

func (c *Conversation) newSession(args json.RawMessage) (any, error) {
	var in struct {
		Name string `json:"name,omitempty"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.store.New(in.Name)
	if err != nil {
		return nil, err
	}
	c.save()
	return c.payload(s), nil
}

func (c *Conversation) deleteSession(args json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId,omitempty"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	id := sanitizeID(in.SessionID)
	if id == "" {
		id = c.store.Current
	}
	removed, err := c.store.Delete(id)
	if err != nil {
		return nil, err
	}
	c.save()

	// 删空之后 Delete 会自动补一个空会话，保证界面永远有落点
	s, err := c.store.Resolve("")
	if err != nil {
		return nil, err
	}
	out := c.payload(s)
	out["deleted"] = true
	out["removedNodes"] = removed
	return out, nil
}

func (c *Conversation) switchTo(args json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	if sanitizeID(in.SessionID) == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 sessionId")
	}
	return c.history(args)
}

// ---------------------------------------------------------------------------
// 会改变树的操作
// ---------------------------------------------------------------------------

type sendParams struct {
	SessionID string `json:"sessionId,omitempty"`
	Text      string `json:"text"`
	System    string `json:"system,omitempty"`
	// Attachments 是随这条消息一起发出的文件 / 图片。
	//
	// 只进这一次请求，**不进会话树**：图片的 base64 动辄几 MB，
	// 存进会话文件会把树撑爆，翻页也变卡。节点里留一行文件名，
	// 人翻记录时看得出当时附了什么。
	Attachments []attachment `json:"attachments,omitempty"`
}

// attachment 是一个随消息发出的附件（形状与 agent.v1 / session.v1 一致）。
type attachment struct {
	Type    string `json:"type"`              // text / image / file
	Name    string `json:"name,omitempty"`    // 文件名
	Text    string `json:"text,omitempty"`    // type=text：文件内容
	DataURL string `json:"dataUrl,omitempty"` // type=image：data:image/png;base64,xxx
	Size    int64  `json:"size,omitempty"`
}

func (c *Conversation) send(args json.RawMessage) (any, error) {
	var in sendParams
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	text := strings.TrimSpace(in.Text)
	// 只发附件不写字也是合理用法（"看这张图"）：这时给个占位正文，
	// 否则模型只收到一堆 image part，没有任何指令。
	if text == "" && len(in.Attachments) == 0 {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 text")
	}
	if text == "" {
		text = "看看我发的附件。"
	}

	t, err := c.begin(in.SessionID, func(s *Session) (string, bool, error) {
		if len(s.Nodes) >= maxNodesPerSession {
			return "", false, plugin.Errorf(plugin.CodeInvalidParams,
				"本会话消息数已达上限 %d，请新建一个会话说（旧消息一条都不会删）", maxNodesPerSession)
		}
		if sys := strings.TrimSpace(in.System); sys != "" {
			s.System = sys
		}
		n := s.AddNode(s.Leaf(), roleUser, withAttachNote(text, in.Attachments), "", "")
		return n.ID, true, nil
	})
	if err != nil {
		return nil, err
	}
	t.attachments = in.Attachments
	return c.finish(t)
}

// withAttachNote 在正文后追加附件名，供历史记录显示。
func withAttachNote(text string, atts []attachment) string {
	if len(atts) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	for _, a := range atts {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = "附件"
		}
		b.WriteString("\n[附件: " + name + "]")
	}
	return b.String()
}

func (c *Conversation) edit(args json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId,omitempty"`
		NodeID    string `json:"nodeId"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 text")
	}

	t, err := c.begin(in.SessionID, func(s *Session) (string, bool, error) {
		if len(s.Nodes) >= maxNodesPerSession {
			return "", false, plugin.Errorf(plugin.CodeInvalidParams,
				"本会话消息数已达上限 %d，请新建一个会话说（旧消息一条都不会删）", maxNodesPerSession)
		}
		n, err := s.EditNode(in.NodeID, text)
		if err != nil {
			return "", false, err
		}
		// 改的是用户说的话，就等于重新问了一遍：顺手补一条 AI 回复。
		// 改的是 AI 自己的话，只是换措辞，不调模型。
		return n.ID, n.Role == roleUser, nil
	})
	if err != nil {
		return nil, err
	}
	if !t.reply {
		return t.payload, nil
	}
	return c.finish(t)
}

func (c *Conversation) regenerate(args json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId,omitempty"`
		NodeID    string `json:"nodeId"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}

	t, err := c.begin(in.SessionID, func(s *Session) (string, bool, error) {
		if len(s.Nodes) >= maxNodesPerSession {
			return "", false, plugin.Errorf(plugin.CodeInvalidParams,
				"本会话消息数已达上限 %d，请新建一个会话说（旧消息一条都不会删）", maxNodesPerSession)
		}
		n, ok := s.Nodes[in.NodeID]
		if !ok {
			return "", false, plugin.Errorf(plugin.CodeInvalidParams, "找不到消息 %s", in.NodeID)
		}
		// 点 AI 的消息：在它的父节点下再挂一个 assistant 兄弟。
		// 点用户的消息：在它自己下面再挂一个 assistant 孩子。
		// 两者都是「重新回答一次」，只是锚点不同。
		if n.Role == roleAssistant {
			return n.Parent, true, nil
		}
		return n.ID, true, nil
	})
	if err != nil {
		return nil, err
	}
	return c.finish(t)
}

func (c *Conversation) switchVersion(args json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId,omitempty"`
		NodeID    string `json:"nodeId"`
		Delta     int    `json:"delta"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	if in.Delta == 0 {
		in.Delta = 1
	}
	return c.apply(in.SessionID, func(s *Session) error {
		return s.SwitchVersion(in.NodeID, in.Delta)
	})
}

// ---------------------------------------------------------------------------
// 分享
// ---------------------------------------------------------------------------

func (c *Conversation) share(args json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId,omitempty"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.store.Resolve(in.SessionID)
	if err != nil {
		return nil, err
	}
	path := s.Path()
	md := Markdown(s, path)
	return map[string]any{
		"sessionId": s.ID,
		"name":      s.Name,
		"markdown":  md,
		"chars":     len([]rune(md)),
		"nodeCount": len(path),
	}, nil
}

// export 经 workspace.v1 把当前时间线写成 Markdown 文件。
//
// 自己不碰文件系统：动工作区的目录是 workspace.v1 的职责与权限，
// 对话插件只把「想写什么」交给它 —— 这是能力代理该有的样子。
func (c *Conversation) export(args json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId,omitempty"`
		Path      string `json:"path,omitempty"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in)
	}

	if _, err := c.ctx.Consume(workspaceSvc); err != nil {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound,
			"导出需要工作区插件（workspace.v1）：%s（请安装 com.official.workspace）", reasonOf(err))
	}

	c.mu.Lock()
	s, err := c.store.Resolve(in.SessionID)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	md := Markdown(s, s.Path())
	target := strings.TrimSpace(in.Path)
	if target == "" {
		// 文件名只用会话 ID + 时间戳：会话名是给人看的，可能是中文和空格
		target = fmt.Sprintf("conversations/%s-%s.md", s.ID, time.Now().Format("20060102-150405"))
	}
	c.mu.Unlock()

	raw, err := c.ctx.Invoke(workspaceSvc, "write", map[string]any{
		"path":    target,
		"content": md,
		"append":  false,
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		Path    string `json:"path"`
		AbsPath string `json:"absPath"`
		Size    int    `json:"size"`
		Note    string `json:"note"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "解析 workspace.v1 响应失败: %v", err)
	}
	return map[string]any{
		"path":    out.Path,
		"absPath": out.AbsPath,
		"size":    out.Size,
		"note":    firstNonEmpty(out.Note, "已导出"),
	}, nil
}

// ---------------------------------------------------------------------------
// 模型链路
// ---------------------------------------------------------------------------

// dispatch 按「agent → llm」的顺序探测可用链路。
//
// agent 报错就原样返回，不再往 llm 降级 —— 那是一次真实失败，
// 悄悄换成另一次调用只会让问题更难查（与 session 插件同一策略）。
func (c *Conversation) dispatch(msgs []modelMessage, system string, atts []attachment) (string, string, error) {
	if len(msgs) == 0 {
		return "", "", plugin.Errorf(plugin.CodeInvalidParams, "没有可发送的消息")
	}
	params := map[string]any{"messages": msgs}
	if s := strings.TrimSpace(system); s != "" {
		params["system"] = s
	}
	if len(atts) > 0 {
		params["attachments"] = atts
	}

	if c.available(agentService) {
		raw, err := c.ctx.Invoke(agentService, "run", params)
		if err != nil {
			return "", agentService, err
		}
		var out struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", agentService, plugin.Errorf(plugin.CodeInternalError, "解析 agent.v1 响应失败: %v", err)
		}
		return out.Content, agentService, nil
	}

	if c.available(llmService) {
		raw, err := c.ctx.Invoke(llmService, "chat", params)
		if err != nil {
			return "", llmService, err
		}
		var out struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", llmService, plugin.Errorf(plugin.CodeInternalError, "解析 llm.v1 响应失败: %v", err)
		}
		return out.Content, llmService, nil
	}

	return "", "", plugin.Errorf(plugin.CodeServiceNotFound,
		"会话需要 agent.v1 或 llm.v1，但两个都不可用。"+
			"最省事的装法：com.official.llm（提供模型）+ com.official.agent（提供编排）")
}

// modelMessages 把一条时间线转成喂给模型的消息。
//
// 失败的节点和空节点都跳过：把「生成失败」当成上下文喂回去只会污染下一轮。
// 最后按条数裁剪，并尽量让历史从 user 开始 —— 否则模型会收到一串
// 「凭空冒出的 assistant 回复」，上下文会变得难以理解。
func modelMessages(nodes []*Node) []modelMessage {
	out := make([]modelMessage, 0, len(nodes))
	for _, n := range nodes {
		if n.Err != "" || strings.TrimSpace(n.Text) == "" {
			continue
		}
		out = append(out, modelMessage{Role: n.Role, Content: n.Text})
	}
	if len(out) > maxTurnsForModel {
		out = out[len(out)-maxTurnsForModel:]
	}
	if len(out) > 1 && out[0].Role != roleUser {
		out = out[1:]
	}
	return out
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// reasonOf 把「服务不可用」翻译成一句人能看懂的原因。
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// shorten 按字符（不是字节）截断，中文不会被切出乱码。
func shorten(s string, max int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}

func main() {
	if err := plugin.Serve(&Conversation{}); err != nil {
		plugin.Logf("error", "对话插件退出: %v", err)
		os.Exit(1)
	}
}
