package main

import (
	"encoding/json"
	"strings"

	"aiwork/sdk/go/plugin"
)

// ---------------------------------------------------------------------------
// 工作区插件自带的那一页界面（ui.page.workspace.v1）
//
// 这一页只做一件事：用**系统自带的文件夹选择框**挑工作目录。以前的终端里
// 自绘目录树（路径输入框 + 目录列表 + 刷新/向上一级）已经删掉 —— 系统对话框
// 有地址栏、搜索、侧边栏，比在终端里点着挑方便得多，再留着那份是重复且更差。
// ---------------------------------------------------------------------------

// panelService 是"插件自带页面"这条通道的服务名。
//
// 约定是 ui.page.<名字>.v1，名字里带上插件自己的名字 —— 服务名在注册表里
// 是一对一的，统一叫 ui.page.v1 的话，第二个想做界面的插件就会被拒之门外。
const panelService = "ui.page.workspace.v1"

// panelID 是这一页的稳定标识：切页与缓存都用它。
const panelID = "workspace"

// panelChangedEvent 是"我这一页变了，重新画一遍"的通知事件。
const panelChangedEvent = "ui.panel.changed"

// panelCtx 是这一页需要的那几样能力。
//
// 抽成接口只为一件事：单测里能换成假的 —— 界面逻辑（点了什么、画成什么样）
// 不该为了测一下就去连真磁盘。生产上注入的就是 plugin.Context。
type panelCtx interface {
	FSList(path string) (json.RawMessage, error)
	PickFolder(initial, title string) (json.RawMessage, error)
	SetConfig(key string, value any) error
	Emit(event string, payload any) error
	Log(level, msg string, meta map[string]any)
}

// panelRequest 就是线协议里的请求参数（定义在 SDK 里，两边共用一份）。
type panelRequest = plugin.PanelRequest

// wsPanel 是这一页的全部界面状态。
//
// 它和工作区本身的状态（root）分开：这里只留"一句反馈"，
// 真正的选择交给系统对话框，不在这里维护任何草稿。
type wsPanel struct {
	note string // 底部的一句反馈
	ok   bool   // 上面那句是好消息还是坏消息
}

// list 回答"我有哪几页"。
func (p *wsPanel) list() []plugin.PanelInfo {
	return []plugin.PanelInfo{{
		ID:    panelID,
		Title: "工作区",
		Order: 20,
	}}
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

// render 画这一页。w×h 是宿主给的可用空间，由它说了算 ——
// 插件不能假设自己有多大地方。这里不做任何慢操作，按帧回答。
func (p *wsPanel) render(w *Workspace, req panelRequest) plugin.PanelRender {
	width, height := req.W, req.H
	if width < 1 || height < 1 {
		return plugin.PanelRender{}
	}

	b := plugin.NewBlock(width, height)
	if width < 20 || height < 6 {
		b.At(0).Text(plugin.SWarn, " 窗口太小，放不下这一页")
		return b.Render(0, 0, "把窗口拉大些")
	}

	// ① 标题：一眼看清现在挂在哪个目录上
	const title = " 工作区文件"
	b.At(0).Text(plugin.SAccent+plugin.SBold, title)
	cur := "当前：" + firstNonEmpty(w.root, "（未指定）")
	b.At(0).Text(plugin.SDim, "  "+plugin.Truncate(plugin.OneLine(cur), maxInt(0, width-plugin.DisplayWidth(title)-3)))

	// ② 说明 + 系统选择按钮
	b.At(2).Text(plugin.SDim, " 用系统文件夹选择框挑工作目录，选完即设。")
	b.Button(3, 1, "系统选择…", "syspick", "", true)

	// ③ 当前工作区
	if w.root != "" {
		b.At(5).Text(plugin.SDim+plugin.SBold, " 当前工作区")
		b.At(6).Text("", " "+plugin.Truncate(plugin.OneLine(w.root), maxInt(0, width-2)))
	}

	// ④ 底部反馈
	if p.note != "" {
		st := plugin.Style(plugin.SDim)
		if !p.ok {
			st = plugin.Style(plugin.SBad)
		}
		b.At(height-1).Text(st, " "+plugin.Truncate(p.note, maxInt(0, width-2)))
	}

	return b.Render(0, 0, "点「系统选择…」弹系统对话框选工作目录")
}

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------

// action 处理一次点击，返回要显示给用户的一句话。
func (p *wsPanel) action(w *Workspace, req panelRequest) string {
	switch req.Action {
	case "syspick":
		return p.sysPick(w)
	}
	return ""
}

// key 处理一次按键。这一页没有输入框，不需要键盘 —— 只在协议上留个空实现。
func (p *wsPanel) key(w *Workspace, kind, text string) (string, bool) {
	return "", false
}

// sysPick 弹系统文件夹选择框，把选中的目录设为工作区。
func (p *wsPanel) sysPick(w *Workspace) string {
	raw, err := w.api.PickFolder(w.root, "选择工作目录")
	if err != nil {
		return p.say(w, "弹出系统选择框失败："+err.Error(), false)
	}
	var out struct {
		Path      string `json:"path"`
		Cancelled bool   `json:"cancelled"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return p.say(w, "解析选择结果失败: "+err.Error(), false)
	}
	if out.Cancelled || strings.TrimSpace(out.Path) == "" {
		return p.say(w, "已取消选择", true)
	}
	return p.setRoot(w, out.Path)
}

// setRoot 把选中的目录设为工作目录，并给一句确认。
func (p *wsPanel) setRoot(w *Workspace, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return p.say(w, "没有选中目录", false)
	}
	if _, err := w.setRootPath(path); err != nil {
		return p.say(w, err.Error(), false)
	}
	return p.say(w, "工作区已设为 "+w.root+"（重启后仍然记得）", true)
}

// ---------------------------------------------------------------------------
// 状态
// ---------------------------------------------------------------------------

// say 记一句反馈并通知宿主重画。
//
// 通知是必须的：宿主只在收到它（或换了尺寸、滚动位置）时才重新要一帧，
// 少了它，界面就会停在旧内容上 —— "点了没反应"就是这么来的。
func (p *wsPanel) say(w *Workspace, note string, ok bool) string {
	p.note, p.ok = note, ok
	p.changed(w)
	return note
}

// changed 通知宿主"这一页变了，重新画一遍"。
//
// 带上服务名：宿主据此精确定位到"哪一页"（只报页面 id 也行，但两个插件
// 页面重名时会多刷一页）。
func (p *wsPanel) changed(w *Workspace) {
	if w == nil || w.api == nil {
		return
	}
	_ = w.api.Emit(panelChangedEvent, map[string]any{
		"service": panelService,
		"panel":   panelID,
	})
}

// ---------------------------------------------------------------------------
// 与宿主对接
// ---------------------------------------------------------------------------

// handlePanel 是 ui.page.workspace.v1 的分发入口。
func (w *Workspace) handlePanel(method string, args json.RawMessage) (any, error) {
	switch method {
	case "list":
		return map[string]any{"panels": w.panel.list(), "note": "工作区"}, nil
	case "render":
		var in panelRequest
		_ = json.Unmarshal(args, &in)
		if in.Panel != "" && in.Panel != panelID {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "工作区没有页面 %s", in.Panel)
		}
		return w.panel.render(w, in), nil
	case "action":
		var in panelRequest
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
		}
		return map[string]any{"note": w.panel.action(w, in)}, nil
	case "key":
		var in panelRequest
		_ = json.Unmarshal(args, &in)
		copyText, exit := w.panel.key(w, in.Kind, in.Text)
		out := map[string]any{"ok": true}
		if copyText != "" {
			out["copy"] = copyText
		}
		if exit {
			out["exit"] = true
		}
		return out, nil
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%s 不支持方法 %s", panelService, method)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
