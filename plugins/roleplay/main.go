// Command roleplay 是角色扮演插件（provides: roleplay.v1）。
//
// 它管三样东西，都是"人设"的一部分：
//
//	角色卡    名字 / 人物设定 / 性格 / 场景 / 开场白
//	我的身份  用户自己是谁（名字 + 设定）—— 同一个角色，对方是我的同事还是我的
//	          徒弟，说话方式完全不同，所以它和角色卡一样重要
//	记忆      这个角色"记得"的事，一条一行，可持续追加
//
// 拼成一段系统提示词后推给 agent.v1 —— 于是现有那条对话链路
// （界面 → conversation.v1 → agent.v1 → llm.v1）**一行都不用改**。
// 链路不认识"角色"这个概念，它只认系统提示词；角色扮演要做的就是翻译。
//
// 三个刻意的取舍：
//
//  1. **启用时推送、停用时还原**：不霸占 agent 的系统提示词配置，而是先把原来那份
//     存下来，停用时原样还回去 —— 否则用户本来写在 agent 上的提示词会被角色卡
//     永久顶掉，而且看不出丢在哪。
//  2. **记忆由对话产生**：注册一个 remember 工具，模型在扮演过程中需要记下什么
//     就自己调它。记忆如果只能靠用户手抄，角色扮演会退化成"每次开局重新交代一遍"。
//  3. **软依赖**：agent.v1 / toolExecutor.v1 没装也能编辑角色卡，只是推不出去、
//     模型也写不了记忆 —— 功能降级，但不报错、不崩。
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
	serviceName  = "roleplay.v1"
	agentService = "agent.v1"
	toolService  = "toolExecutor.v1"

	// configKey 是整份状态在本插件配置命名空间里的键名。
	configKey = "rp"
)

// pluginAPI 是本插件用到的那几样核心能力（生产上注入的就是 *plugin.Context）。
//
// 抽成接口只为一件事：单测里能换成假的 —— 界面逻辑（点了什么、画成什么样）
// 不该为了测一下就去连真核心。
type pluginAPI interface {
	Consume(name string) (string, error)
	Invoke(service, method string, args any) (json.RawMessage, error)
	GetConfig(key string) (json.RawMessage, error)
	SetConfig(key string, value any) error
	Emit(event string, payload any) error
	Log(level, msg string, meta map[string]any)
}

// Roleplay 实现 plugin.Plugin。
type Roleplay struct {
	ctx *plugin.Context
	api pluginAPI

	// panel 是角色页那一页的界面状态（见 panel.go）。
	//
	// 界面状态住在插件自己这里，不放在 UI 插件里 —— 这是"插件自带界面"的全部
	// 意义：UI 插件只借一块地方给它画，其余一概不管。
	panel rpPanel

	// remember 工具的注册状态。tool-executor 往往比本插件晚就绪，
	// 所以要重试；用锁护住"注册一次"这件事，避免并发重复注册。
	toolMu   sync.Mutex
	toolDone bool
}

// Activate 注册服务，并（如果上次退出时还有启用中的角色）把提示词重新推一次。
func (r *Roleplay) Activate(ctx *plugin.Context) error {
	r.ctx, r.api = ctx, ctx
	if err := ctx.Provide(serviceName, map[string]any{
		"description": "角色扮演：角色卡 / 我的身份 / 长期记忆，拼成系统提示词交给 agent",
		"methods": []string{"state", "get", "save", "delete", "persona",
			"remember", "activate", "deactivate", "prompt"},
	}); err != nil {
		return err
	}

	// 自带界面：把角色页挂在 UI 插件上。
	//
	// 提供 ui.page.* 就代表"我有一页自己的界面" —— UI 插件会来问
	// list / render / action / key，页面上有什么它一概不知道。
	if err := ctx.Provide(panelService, map[string]any{
		"description": "角色页：列表 / 角色卡 / 我的身份 / 提示词预览（插件自己渲染）",
		"methods":     []string{"list", "render", "action", "key"},
	}); err != nil {
		ctx.Log("warn", "自带界面没能挂上（"+err.Error()+"），功能不受影响", nil)
	}
	r.registerRememberTool()

	st := r.load()
	if c := st.active(); c != nil {
		// 重启后 agent 会带着自己的默认提示词回来，角色的那份得重新推一次。
		// agent.plugin 按目录序排在 roleplay 之前，所以此刻 agent.v1 已经就绪。
		note, err := r.pushPrompt(st, c)
		if err != nil {
			ctx.Log("warn", "启用中的角色没能重新推给 agent："+err.Error(), nil)
		} else {
			ctx.Log("info", "已重新启用角色「"+c.Name+"」", map[string]any{"note": note})
		}
	}
	return nil
}

// Handle 处理 core.invoke。
func (r *Roleplay) Handle(service, method string, args json.RawMessage) (any, error) {
	if service == panelService {
		return r.handlePanel(method, args)
	}
	if service != serviceName {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "角色扮演插件不提供服务 %s", service)
	}
	switch method {
	case "state":
		return r.state(), nil
	case "get":
		return r.get(args)
	case "save":
		return r.save(args)
	case "delete":
		return r.delete(args)
	case "persona":
		return r.persona(args)
	case "remember":
		return r.remember(args)
	case "activate":
		return r.activate(args)
	case "deactivate":
		return r.deactivate()
	case "prompt":
		return r.prompt(args)
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "roleplay.v1 不支持方法 %s", method)
	}
}

// Deactivate 清理。
func (r *Roleplay) Deactivate() error {
	if r.api != nil {
		r.api.Log("info", "角色扮演插件退出", nil)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 存取
// ---------------------------------------------------------------------------

func (r *Roleplay) load() *store {
	st := &store{}
	raw, err := r.api.GetConfig(configKey)
	if err != nil {
		r.api.Log("warn", "读取角色配置失败："+err.Error(), nil)
		return st
	}
	if len(raw) == 0 {
		return st
	}
	if err := json.Unmarshal(raw, st); err != nil {
		// 整份状态是一棵树（角色卡的记忆互相独立），拆开存一定会对不上，
		// 所以坏了只能按空处理 —— 但要在日志里说清楚，别让用户以为是插件丢了数据。
		r.api.Log("warn", "角色配置解析失败，按空处理："+err.Error(), nil)
		return &store{}
	}
	return st
}

func (r *Roleplay) saveStore(st *store) error {
	return r.api.SetConfig(configKey, st)
}

// ---------------------------------------------------------------------------
// 与 agent 的对接：推送 / 还原系统提示词
// ---------------------------------------------------------------------------

// pushPrompt 把角色卡拼成系统提示词推给 agent。
//
// 首次启用（st.ActiveID 还是空）时先把 agent 原本的提示词存起来 ——
// 停用时才有得还。已经在启用中就不动那份存档：否则会把角色的提示词
// 当成"原始提示词"存进 SavedSystem，还原时原样还回来等于没还原。
func (r *Roleplay) pushPrompt(st *store, c *Character) (string, error) {
	if _, err := r.api.Consume(agentService); err != nil {
		return "", fmt.Errorf("agent.v1 不可用（%v）—— 请安装 com.official.agent 插件", err)
	}
	if st.ActiveID == "" && st.SavedSystem == "" {
		if raw, err := r.api.Invoke(agentService, "info", map[string]any{}); err == nil {
			var info struct {
				System string `json:"systemPrompt"`
			}
			if json.Unmarshal(raw, &info) == nil {
				st.SavedSystem = info.System
				if err := r.saveStore(st); err != nil {
					return "", err
				}
			}
		}
	}

	prompt := composePrompt(c, st.Persona)
	if _, err := r.api.Invoke(agentService, "configure", map[string]any{"systemPrompt": prompt}); err != nil {
		return "", fmt.Errorf("写入 agent 系统提示词失败：%v", err)
	}
	return fmt.Sprintf("已把「%s」的设定推给 agent（提示词 %d 字）", firstNonEmpty(c.Name, "未命名角色"), len([]rune(prompt))), nil
}

// restoreSystem 把 agent 的系统提示词还回去。
//
// 传空串是合法的：那表示"原本没有自定义提示词"，还回去之后 agent 会用它的默认值。
func (r *Roleplay) restoreSystem(prev string) bool {
	if _, err := r.api.Consume(agentService); err != nil {
		return false
	}
	_, err := r.api.Invoke(agentService, "configure", map[string]any{"systemPrompt": prev})
	return err == nil
}

// ---------------------------------------------------------------------------
// 方法
// ---------------------------------------------------------------------------

// state 返回界面需要的全部状态：角色列表 + 我的身份 + 谁在启用中。
func (r *Roleplay) state() map[string]any {
	st := r.load()
	list := make([]map[string]any, 0, len(st.Characters))
	for _, c := range st.Characters {
		list = append(list, map[string]any{
			"id":          c.ID,
			"name":        c.Name,
			"greeting":    c.Greeting,
			"memoryCount": memoryCount(c.Memory),
			"updated":     c.Updated,
			"active":      c.ID == st.ActiveID,
		})
	}
	return map[string]any{
		"activeId":   st.ActiveID,
		"characters": list,
		"count":      len(list),
		"persona":    map[string]any{"name": st.Persona.Name, "desc": st.Persona.Desc},
		"agentReady": consumeOK(r.ctx, agentService),
		"toolReady":  consumeOK(r.ctx, toolService),
	}
}

func (r *Roleplay) get(args json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id,omitempty"`
	}
	_ = json.Unmarshal(args, &in)
	st := r.load()

	// 没给 id 就返回"当前启用中的那个"，方便界面一进来就有东西可看
	id := firstNonEmpty(in.ID, st.ActiveID)
	if id == "" {
		return map[string]any{"character": nil, "note": "还没有角色，点「新建」起一个"}, nil
	}
	c := st.find(id)
	if c == nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "找不到角色 %s", id)
	}
	return map[string]any{"character": c, "active": c.ID == st.ActiveID}, nil
}

// save 新建或更新一张角色卡（id 为空即新建）。
func (r *Roleplay) save(args json.RawMessage) (any, error) {
	var in saveParams
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	st := r.load()
	c, created := st.applySave(in)
	if err := r.saveStore(st); err != nil {
		return nil, err
	}

	// 改的是启用中的角色 → 立刻把新提示词推过去，不然要等下次启用才生效
	pushed := false
	if st.ActiveID == c.ID {
		if _, err := r.pushPrompt(st, c); err == nil {
			pushed = true
		}
	}
	r.api.Emit("roleplay.changed", map[string]any{"id": c.ID, "created": created})
	// 自带的那一页得跟着刷新：存储变了，页面上还是旧内容就是"点了没反应"
	r.panel.invalidate(r)

	note := "已保存"
	if created {
		note = "已新建角色卡"
	}
	if pushed {
		note += "，并已推给 agent（下一条消息就按新人设说）"
	}
	return map[string]any{"id": c.ID, "created": created, "pushed": pushed, "note": note}, nil
}

func (r *Roleplay) delete(args json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	id := strings.TrimSpace(in.ID)
	st := r.load()
	gone := st.remove(id)
	if gone == nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "找不到角色 %s", id)
	}
	name := gone.Name

	// 删掉正在启用的角色：顺手停用，别让 agent 顶着一个已经不存在的角色说话
	wasActive := st.ActiveID == id
	restored := false
	if wasActive {
		prev := st.SavedSystem
		st.ActiveID, st.SavedSystem = "", ""
		restored = r.restoreSystem(prev)
	}
	if err := r.saveStore(st); err != nil {
		return nil, err
	}
	r.api.Emit("roleplay.changed", map[string]any{"id": id, "deleted": true})
	r.panel.invalidate(r)

	note := "已删除「" + name + "」"
	if wasActive {
		note += "，它正在启用中，已顺手停用"
		if restored {
			note += "并把 agent 的系统提示词还原"
		}
	}
	return map[string]any{"deleted": id, "wasActive": wasActive, "restored": restored, "note": note}, nil
}

// persona 读或写"我的身份"。带参数即写，不带给就只读。
func (r *Roleplay) persona(args json.RawMessage) (any, error) {
	var in struct {
		Name *string `json:"name,omitempty"`
		Desc *string `json:"desc,omitempty"`
	}
	_ = json.Unmarshal(args, &in)

	st := r.load()
	if in.Name == nil && in.Desc == nil {
		return map[string]any{"name": st.Persona.Name, "desc": st.Persona.Desc}, nil
	}
	if in.Name != nil {
		st.Persona.Name = strings.TrimSpace(*in.Name)
	}
	if in.Desc != nil {
		st.Persona.Desc = strings.TrimSpace(*in.Desc)
	}
	if err := r.saveStore(st); err != nil {
		return nil, err
	}

	// 身份是提示词的一部分：正在启用中就得立刻重新推一次
	pushed := false
	if c := st.active(); c != nil {
		if _, err := r.pushPrompt(st, c); err == nil {
			pushed = true
		}
	}
	r.panel.invalidate(r)
	return map[string]any{
		"name": st.Persona.Name, "desc": st.Persona.Desc, "pushed": pushed,
		"note": "身份已保存" + pushNote(pushed),
	}, nil
}

func (r *Roleplay) activate(args json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	st := r.load()
	c := st.find(strings.TrimSpace(in.ID))
	if c == nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "找不到角色 %s", in.ID)
	}
	wasActive := st.ActiveID
	st.ActiveID = c.ID
	note, err := r.pushPrompt(st, c)
	if err != nil {
		// 推不出去就别假装启用成功：状态留在原地，让界面能如实显示
		st.ActiveID = wasActive
		_ = r.saveStore(st)
		return nil, plugin.Errorf(plugin.CodeInternalError, "启用失败：%v", err)
	}
	if err := r.saveStore(st); err != nil {
		return nil, err
	}
	r.api.Emit("roleplay.active", map[string]any{"id": c.ID, "name": c.Name})
	r.panel.invalidate(r)
	r.api.Log("info", "已启用角色「"+c.Name+"」", map[string]any{"id": c.ID})
	return map[string]any{
		"activeId": c.ID, "name": c.Name, "note": note,
		"greeting": c.Greeting,
	}, nil
}

func (r *Roleplay) deactivate() (any, error) {
	st := r.load()
	prev := st.SavedSystem
	had := st.ActiveID != ""
	st.ActiveID, st.SavedSystem = "", ""
	if err := r.saveStore(st); err != nil {
		return nil, err
	}
	restored := false
	note := "当前没有启用中的角色"
	if had {
		restored = r.restoreSystem(prev)
		note = "已停用角色"
		if restored {
			note += "，agent 的系统提示词已还原"
		} else {
			note += "，但 agent 的系统提示词没能还原（agent.v1 不可用？）"
		}
	}
	r.api.Emit("roleplay.active", map[string]any{"id": ""})
	r.panel.invalidate(r)
	return map[string]any{"activeId": "", "restored": restored, "note": note}, nil
}

// prompt 返回拼好的系统提示词，用来核对"角色卡到底会被怎么描述给模型"。
func (r *Roleplay) prompt(args json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id,omitempty"`
	}
	_ = json.Unmarshal(args, &in)
	st := r.load()
	id := firstNonEmpty(in.ID, st.ActiveID)
	c := st.find(id)
	if c == nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "找不到角色 %s", in.ID)
	}
	p := composePrompt(c, st.Persona)
	return map[string]any{
		"id": c.ID, "name": c.Name, "prompt": p,
		"chars": len([]rune(p)), "memoryCount": memoryCount(c.Memory),
	}, nil
}

// ---------------------------------------------------------------------------
// remember 工具
// ---------------------------------------------------------------------------

// registerRememberTool 把"记住一件事"注册成工具，让模型在扮演过程中自己写记忆。
//
// 这是整套设计里最要紧的一步：记忆如果不能由对话本身产生，用户每换一次场景
// 都得重新交代一遍背景，角色扮演就退化成了"套模板的问答"。
//
// **必须重试**：工具的目录序在 roleplay 之后（tool-executor.plugin > roleplay.plugin），
// 本插件刚起来那一刻它还没注册好。一次不成就不再问的话，"模型自己写记忆"
// 会静默失效 —— 而且只在重启后才看得出来，最难查的那种。
func (r *Roleplay) registerRememberTool() {
	go func() {
		for i := 0; i < 20; i++ {
			if r.tryRegisterRememberTool() {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		r.api.Log("warn", "始终没能注册 remember 工具：toolExecutor.v1 一直不可用"+
			"（模型无法自己写记忆，界面上仍可编辑）", nil)
	}()
}

// tryRegisterRememberTool 试一次，成功返回 true。
func (r *Roleplay) tryRegisterRememberTool() bool {
	r.toolMu.Lock()
	defer r.toolMu.Unlock()
	if r.toolDone {
		return true
	}
	if _, err := r.api.Consume(toolService); err != nil {
		return false
	}
	_, err := r.api.Invoke(toolService, "register", map[string]any{
		"name": "remember",
		"description": "把用户告诉你的、值得长期记住的事写进当前角色的记忆，下次对话仍然记得。" +
			"当用户说「记住……」或交代了重要设定（称呼、约定、经历）时调用它。" +
			"只在嘴上说\"我记住了\"没有用 —— 那不会存进记忆。",
		"service": serviceName,
		"method":  "remember",
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string", "description": "要记住的内容，一句话说清"},
			},
			"required": []string{"text"},
		},
	})
	if err != nil {
		r.api.Log("warn", "注册 remember 工具失败："+err.Error(), nil)
		return false
	}
	r.toolDone = true
	r.api.Log("info", "已注册 remember 工具（模型可以自己写记忆）", nil)
	return true
}

func (r *Roleplay) remember(args json.RawMessage) (any, error) {
	var in struct {
		Text string `json:"text"`
		ID   string `json:"id,omitempty"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	if strings.TrimSpace(in.Text) == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "text 不能为空")
	}
	st := r.load()
	id := firstNonEmpty(in.ID, st.ActiveID)
	if id == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"没有启用中的角色（先 activate 一个角色，或显式传 id）")
	}
	c := st.find(id)
	if c == nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "找不到角色 %s", id)
	}

	c.Memory = appendMemory(c.Memory, in.Text)
	c.Updated = nowStamp()
	if err := r.saveStore(st); err != nil {
		return nil, err
	}

	// 记忆是提示词的一部分：这一轮记下的东西要立刻进下一轮，
	// 否则模型刚说"记住了"、下一句又忘了，反而更像穿帮。
	pushed := false
	if st.ActiveID == id {
		if _, err := r.pushPrompt(st, c); err == nil {
			pushed = true
		}
	}
	count := memoryCount(c.Memory)
	r.api.Emit("roleplay.memory", map[string]any{"id": id, "count": count, "text": strings.TrimSpace(in.Text)})
	// 模型刚记下一条：页面上那个记忆框得跟着长出来，否则用户看到的还是旧内容
	r.panel.invalidate(r)
	r.api.Log("info", "记住了一件事", map[string]any{"id": id, "count": count})

	return map[string]any{
		"id": id, "count": count, "pushed": pushed,
		"note": fmt.Sprintf("已记下（这是第 %d 条），下一轮对话就生效", count),
	}, nil
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func pushNote(pushed bool) string {
	if pushed {
		return "，并已推给 agent（下一条消息就生效）"
	}
	return ""
}

func consumeOK(api pluginAPI, svc string) bool {
	if api == nil {
		return false
	}
	_, err := api.Consume(svc)
	return err == nil
}

func main() {
	if err := plugin.Serve(&Roleplay{}); err != nil {
		plugin.Logf("error", "角色扮演插件退出: %v", err)
		os.Exit(1)
	}
}
