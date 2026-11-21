// 数据桥扩展：各功能页的数据模型与业务方法。
//
// 新 GUI 直接调用各插件的业务服务（llm.v1 / roleplay.v1 / agent.v1 /
// imagegen.v1 / pluginManager.v1 / workspace.v1 / usage.v1），绕开旧 UI 的
// 终端面板。每个 loadXxx 异步拉取、更新快照后通知重绘；每个 saveXxx 写完
// 自动重新加载，保证界面与配置一致。
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---------- 模型配置 ----------
type presetView struct {
	Key, Label, Provider, BaseURL, KeyURL, Hint, Note, Group string
	Models                                                   []string
	NeedKey                                                  bool
}

type providerChoice struct {
	Name, BaseURL, Model, Note string
	Provider, Key              string
}

// modelProviderView 是「模型提供商」的一条配置（DSH ModelsSection 的卡片）。
type modelProviderView struct {
	ID, Preset, Label, Provider string
	BaseURL, APIKey, Model      string
	VisionModel, SystemPrompt   string
	HasKey, Active              bool
}

type modelConfigView struct {
	Current, BaseURL, APIKey, Model, SystemPrompt            string
	Temperature, MaxTokens, TimeoutMs                        int
	VisionProvider, VisionBaseURL, VisionAPIKey, VisionModel string
	Presets                                                  []presetView
	Providers                                                []modelProviderView
	Active                                                   string
	Available                                                []providerChoice
	Discovered                                               []providerChoice
	Note                                                     string
}

// ---------- 角色 ----------
type roleCard struct {
	ID, Name, Description, Personality, Scenario, Greeting, Memory string
	MemoryCount                                                    int
	Active                                                         bool
}

type rolesView struct {
	ActiveID                 string
	Roles                    []roleCard
	PersonaName, PersonaDesc string
	Prompt                   string
	Note                     string
}

// ---------- 预算 ----------
type budgetView struct {
	MaxSteps, TimeoutMs, ToolLimit int
	System, ToolsNote              string
	Tools                          []string
	Note                           string
	Loaded                         bool // 首次 info 返回后置 true，编辑器等它再初始化
}

// ---------- 文生图 ----------
type imagegenView struct {
	Current, BaseURL, Model, APIKey, Dir, Size string
	Available                                  []providerChoice
	LastPaths                                  []string
	Message                                    string
	Prompt                                     string
	Busy                                       bool
	Note                                       string
}

// ---------- 插件管理 ----------
type pluginMgrItem struct {
	ID, Name, Version, Description  string
	Runtime, Entry, Compat          string
	Provides, Consumes, Permissions []string
	Dir, DirPath                    string
	Disabled                        bool
	Protected                       bool
}

type pluginMgrView struct {
	Items []pluginMgrItem
	Note  string
}

// ---------- 工作区 ----------
type wsEntry struct {
	Path, Name string
	Dir        bool
	Size       int64
}

type workspaceView struct {
	Root, Path, Parent string
	Set, IsRoot        bool
	Entries            []wsEntry
	Note               string
}

// ---------- 用量 ----------
type usageView struct {
	BalanceOK, Available                                      bool
	Currency, Total, Granted, ToppedUp, CheckedAt, HitRateStr string
	Requests, Failed, PromptTokens, CompletionTokens          int
	TotalTokens, CacheHitTokens, CacheMissTokens              int
	Note                                                      string
}

// invoke 调任意服务，先 Consume 探测。
//
// 插件是逐个激活的：GUI 的 Activate 排在某些插件前面时，Consume 会短暂失败
// （"未安装"）。这里对"服务还没起来"做短重试（约 1.5 秒），等后面的插件注册完。
// 只对 Consume 失败重试；真正的业务调用不重试，避免重复触发写操作。
func (b *bridge) call(service, method string, params map[string]any) (json.RawMessage, error) {
	var err error
	for attempt := 0; attempt < 15; attempt++ {
		if _, err = b.ctx.Consume(service); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("未安装 %s", service)
	}
	if params == nil {
		params = map[string]any{}
	}
	return b.ctx.Invoke(service, method, params)
}

// 异步执行：后台调用 fn，完成后由 fn 自行更新快照；出错写到对应 note。
func (b *bridge) async(fn func()) { go fn() }

// ============================================================
// 模型配置 llm.v1
// ============================================================
func (b *bridge) loadModelConfig() {
	b.async(func() {
		raw, err := b.call("llm.v1", "providers", nil)
		if err != nil {
			b.patch(func(s *snapshot) { s.ModelCfg.Note = err.Error() })
			return
		}
		var out struct {
			Current   string `json:"current"`
			Active    string `json:"active"`
			Available []struct {
				Name    string `json:"name"`
				BaseURL string `json:"baseUrl"`
				Model   string `json:"model"`
				Note    string `json:"note"`
			} `json:"available"`
			Providers []struct {
				ID           string `json:"id"`
				Preset       string `json:"preset"`
				Label        string `json:"label"`
				Provider     string `json:"provider"`
				BaseURL      string `json:"baseUrl"`
				APIKey       string `json:"apiKey"`
				HasKey       bool   `json:"hasKey"`
				Model        string `json:"model"`
				VisionModel  string `json:"visionModel"`
				SystemPrompt string `json:"systemPrompt"`
				Active       bool   `json:"active"`
			} `json:"providers"`
			Config struct {
				Provider     string `json:"provider"`
				BaseURL      string `json:"baseUrl"`
				APIKey       string `json:"apiKey"`
				Model        string `json:"model"`
				Temperature  int    `json:"temperature"`
				MaxTokens    int    `json:"maxTokens"`
				TimeoutMs    int    `json:"timeoutMs"`
				SystemPrompt string `json:"systemPrompt"`
				VisionModel  string `json:"visionModel"`
			} `json:"config"`
		}
		_ = json.Unmarshal(raw, &out)
		v := modelConfigView{
			Current: out.Current, BaseURL: out.Config.BaseURL, APIKey: out.Config.APIKey,
			Model: out.Config.Model, SystemPrompt: out.Config.SystemPrompt,
			Temperature: out.Config.Temperature, MaxTokens: out.Config.MaxTokens,
			TimeoutMs: out.Config.TimeoutMs, VisionModel: out.Config.VisionModel,
			Active: out.Active,
		}
		for _, p := range out.Providers {
			v.Providers = append(v.Providers, modelProviderView{
				ID: p.ID, Preset: p.Preset, Label: p.Label, Provider: p.Provider,
				BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model,
				VisionModel: p.VisionModel, SystemPrompt: p.SystemPrompt,
				HasKey: p.HasKey, Active: p.Active,
			})
		}
		for _, p := range out.Available {
			v.Available = append(v.Available, providerChoice{Name: p.Name, BaseURL: p.BaseURL, Model: p.Model, Note: p.Note})
		}
		// 平台预设
		if praw, e := b.call("llm.v1", "presets", nil); e == nil {
			var ps []struct {
				Key, Label, Provider, BaseURL, KeyURL, Hint, Note, Group string
				Models                                                   []string
				NeedKey                                                  bool
			}
			if json.Unmarshal(praw, &ps) == nil {
				for _, p := range ps {
					v.Presets = append(v.Presets, presetView{
						Key: p.Key, Label: p.Label, Provider: p.Provider, BaseURL: p.BaseURL,
						KeyURL: p.KeyURL, Hint: p.Hint, Note: p.Note, Group: p.Group,
						Models: p.Models, NeedKey: p.NeedKey,
					})
				}
			}
		}
		// 保留上一次的状态提示（保存成功的绿色提示要在重载后仍然看得见）。
		b.patch(func(s *snapshot) { v.Note = s.ModelCfg.Note; s.ModelCfg = v })
	})
}

// saveProvider 把编辑卡里的草稿写回某条服务商并保存。
// 密钥留空表示"沿用已保存的密钥"（界面拿到的只是掩码）。
func (b *bridge) saveProvider(id string, e modelProviderView) {
	b.patch(func(s *snapshot) {
		for i := range s.ModelCfg.Providers {
			p := &s.ModelCfg.Providers[i]
			if p.ID != id {
				continue
			}
			p.Label = strings.TrimSpace(e.Label)
			p.Provider = strings.TrimSpace(e.Provider)
			p.BaseURL = strings.TrimRight(strings.TrimSpace(e.BaseURL), "/")
			p.Model = strings.TrimSpace(e.Model)
			p.VisionModel = strings.TrimSpace(e.VisionModel)
			p.SystemPrompt = e.SystemPrompt
			if k := strings.TrimSpace(e.APIKey); k != "" {
				p.APIKey = k
				p.HasKey = true
			}
			break
		}
	})
	b.saveModelConfig()
}

// addProvider 追加一条服务商，并把它设为当前生效（与本应用"一套配置生效"的模型一致）。
func (b *bridge) addProvider(e modelProviderView) {
	b.patch(func(s *snapshot) {
		used := map[string]bool{}
		for _, p := range s.ModelCfg.Providers {
			used[p.ID] = true
		}
		id := e.ID
		if id == "" {
			id = "provider"
		}
		for used[id] {
			id += "-2"
		}
		e.ID = id
		e.Active = true
		for i := range s.ModelCfg.Providers {
			s.ModelCfg.Providers[i].Active = false
		}
		s.ModelCfg.Providers = append(s.ModelCfg.Providers, e)
		s.ModelCfg.Active = e.ID
	})
	b.saveModelConfig()
}

// removeProvider 删除一条服务商；删的是当前生效项时，切到剩下的第一条。
func (b *bridge) removeProvider(id string) {
	b.patch(func(s *snapshot) {
		kept := make([]modelProviderView, 0, len(s.ModelCfg.Providers))
		wasActive := s.ModelCfg.Active == id
		for _, p := range s.ModelCfg.Providers {
			if p.ID != id {
				kept = append(kept, p)
			}
		}
		s.ModelCfg.Providers = kept
		if wasActive {
			s.ModelCfg.Active = ""
			if len(kept) > 0 {
				s.ModelCfg.Active = kept[0].ID
			}
		}
		for i := range s.ModelCfg.Providers {
			s.ModelCfg.Providers[i].Active = s.ModelCfg.Providers[i].ID == s.ModelCfg.Active
		}
	})
	b.saveModelConfig()
}

// setActiveProvider 切换当前生效的服务商。
func (b *bridge) setActiveProvider(id string) {
	b.patch(func(s *snapshot) {
		s.ModelCfg.Active = id
		for i := range s.ModelCfg.Providers {
			s.ModelCfg.Providers[i].Active = s.ModelCfg.Providers[i].ID == id
		}
	})
	b.saveModelConfig()
}

// discoverModels 扫描本机服务（ollama 等）。
func (b *bridge) discoverModels() {
	b.patch(func(s *snapshot) { s.ModelCfg.Note = "正在扫描本机服务…" })
	b.async(func() {
		raw, err := b.call("llm.v1", "discover", map[string]any{"timeoutMs": 900})
		if err != nil {
			b.patch(func(s *snapshot) { s.ModelCfg.Note = err.Error() })
			return
		}
		var out struct {
			Scanned int `json:"scanned"`
			Found   []struct {
				Key, Label, Provider, BaseURL, Note string
				Models                              []string
				Alive                               bool
			} `json:"found"`
			Note string `json:"note"`
		}
		_ = json.Unmarshal(raw, &out)
		found := make([]providerChoice, 0, len(out.Found))
		for _, f := range out.Found {
			model := ""
			if len(f.Models) > 0 {
				model = f.Models[0]
			}
			found = append(found, providerChoice{
				Name: f.Label, BaseURL: f.BaseURL, Model: model, Note: f.Note,
				Provider: f.Provider, Key: f.Key,
			})
		}
		note := out.Note
		if note == "" {
			note = fmt.Sprintf("扫描 %d 个端口，发现 %d 个可用服务", out.Scanned, len(found))
		}
		b.patch(func(s *snapshot) { s.ModelCfg.Discovered = found; s.ModelCfg.Note = note })
	})
}

// saveModelConfig 保存模型配置（指针语义：这里把编辑态整份写回）。
func (b *bridge) saveModelConfig() {
	b.patch(func(s *snapshot) { s.ModelCfg.Note = "正在保存…" })
	b.async(func() {
		c := b.get().ModelCfg
		params := map[string]any{
			"temperature": c.Temperature, "maxTokens": c.MaxTokens, "timeoutMs": c.TimeoutMs,
		}
		if len(c.Providers) > 0 {
			// 列表模式：整份写回，active 指定当前生效项。
			list := make([]map[string]any, 0, len(c.Providers))
			for _, p := range c.Providers {
				list = append(list, map[string]any{
					"id": p.ID, "preset": p.Preset, "label": p.Label, "provider": p.Provider,
					"baseUrl": p.BaseURL, "apiKey": p.APIKey, "model": p.Model,
					"visionModel": p.VisionModel, "systemPrompt": p.SystemPrompt,
				})
			}
			params["providers"] = list
			params["active"] = c.Active
		} else {
			// 兼容路径：还没有列表（旧数据）时按扁平键写。
			params["provider"] = c.Current
			params["baseUrl"] = c.BaseURL
			params["apiKey"] = c.APIKey
			params["model"] = c.Model
			params["systemPrompt"] = c.SystemPrompt
			params["visionModel"] = c.VisionModel
		}
		raw, err := b.call("llm.v1", "configure", params)
		if err != nil {
			b.patch(func(s *snapshot) { s.ModelCfg.Note = "保存失败: " + err.Error() })
			return
		}
		var out struct {
			OK      bool `json:"ok"`
			Updated int  `json:"updated"`
			Current struct {
				APIKey string `json:"apiKey"`
			} `json:"current"`
		}
		_ = json.Unmarshal(raw, &out)
		note := fmt.Sprintf("已保存 %d 项配置", out.Updated)
		b.patch(func(s *snapshot) { s.ModelCfg.Note = note; s.ModelCfg.APIKey = out.Current.APIKey })
		b.refreshModelName()
		// 重新拉一次列表：把刚输入的明文密钥换成服务端返回的掩码值。
		b.loadModelConfig()
	})
}

func (b *bridge) refreshModelName() {
	b.patch(func(s *snapshot) { s.Model = b.model() })
}

// ============================================================
// 角色 roleplay.v1
// ============================================================
func (b *bridge) loadRoles() {
	b.async(func() {
		raw, err := b.call("roleplay.v1", "state", nil)
		if err != nil {
			b.patch(func(s *snapshot) { s.Roles.Note = err.Error() })
			return
		}
		var out struct {
			ActiveID   string `json:"activeId"`
			Characters []struct {
				ID, Name, Greeting string
				MemoryCount        int  `json:"memoryCount"`
				Active             bool `json:"active"`
			} `json:"characters"`
			Persona struct {
				Name string `json:"name"`
				Desc string `json:"desc"`
			} `json:"persona"`
			Count int `json:"count"`
		}
		_ = json.Unmarshal(raw, &out)
		v := rolesView{ActiveID: out.ActiveID, PersonaName: out.Persona.Name, PersonaDesc: out.Persona.Desc}
		for _, c := range out.Characters {
			v.Roles = append(v.Roles, roleCard{ID: c.ID, Name: c.Name, Greeting: c.Greeting, MemoryCount: c.MemoryCount, Active: c.Active})
		}
		// 当前角色的完整提示词
		id := out.ActiveID
		if id == "" && len(v.Roles) > 0 {
			id = v.Roles[0].ID
		}
		if id != "" {
			if praw, e := b.call("roleplay.v1", "prompt", map[string]any{"id": id}); e == nil {
				var po struct {
					Prompt string `json:"prompt"`
				}
				if json.Unmarshal(praw, &po) == nil {
					v.Prompt = po.Prompt
				}
			}
		}
		b.patch(func(s *snapshot) { s.Roles = v })
	})
}

func (b *bridge) activateRole(id string) {
	b.async(func() {
		_, err := b.call("roleplay.v1", "activate", map[string]any{"id": id})
		if err == nil {
			b.loadRoles()
		}
	})
}

func (b *bridge) deactivateRole() {
	b.async(func() {
		_, _ = b.call("roleplay.v1", "deactivate", nil)
		b.loadRoles()
	})
}

func (b *bridge) deleteRole(id string) {
	b.async(func() {
		_, _ = b.call("roleplay.v1", "delete", map[string]any{"id": id})
		b.loadRoles()
	})
}

// saveRole 新建或更新角色。
func (b *bridge) saveRole(c roleCard) {
	b.async(func() {
		params := map[string]any{
			"name": c.Name, "description": c.Description, "personality": c.Personality,
			"scenario": c.Scenario, "greeting": c.Greeting, "memory": c.Memory,
		}
		if c.ID != "" {
			params["id"] = c.ID
		}
		_, err := b.call("roleplay.v1", "save", params)
		if err == nil {
			b.loadRoles()
		} else {
			b.patch(func(s *snapshot) { s.Roles.Note = err.Error() })
		}
	})
}

func (b *bridge) savePersona(name, desc string) {
	b.async(func() {
		_, _ = b.call("roleplay.v1", "persona", map[string]any{"name": name, "desc": desc})
		b.loadRoles()
	})
}

// ============================================================
// 预算 agent.v1
// ============================================================
func (b *bridge) loadBudget() {
	// 先标记未加载，编辑器等新数据回来再初始化，避免沿用旧快照。
	b.patch(func(s *snapshot) { s.Budget.Loaded = false })
	b.async(func() {
		raw, err := b.call("agent.v1", "info", nil)
		if err != nil {
			b.patch(func(s *snapshot) { s.Budget.Note = err.Error() })
			return
		}
		var out struct {
			MaxSteps           int      `json:"maxSteps"`
			TimeoutMs          int      `json:"timeoutMs"`
			MaxToolResultChars int      `json:"maxToolResultChars"`
			SystemPrompt       string   `json:"systemPrompt"`
			Tools              []string `json:"tools"`
			ToolsNote          string   `json:"toolsNote"`
		}
		_ = json.Unmarshal(raw, &out)
		b.patch(func(s *snapshot) {
			s.Budget = budgetView{
				MaxSteps: out.MaxSteps, TimeoutMs: out.TimeoutMs, ToolLimit: out.MaxToolResultChars,
				System: out.SystemPrompt, Tools: out.Tools, ToolsNote: out.ToolsNote,
				Loaded: true,
			}
		})
	})
}

func (b *bridge) saveBudget(v budgetView) {
	b.async(func() {
		_, err := b.call("agent.v1", "configure", map[string]any{
			"maxSteps": v.MaxSteps, "timeoutMs": v.TimeoutMs,
			"maxToolResultChars": v.ToolLimit, "systemPrompt": v.System,
		})
		if err == nil {
			b.loadBudget()
		} else {
			b.patch(func(s *snapshot) { s.Budget.Note = err.Error() })
		}
	})
}

// ============================================================
// 文生图 imagegen.v1
// ============================================================
func (b *bridge) loadImageGen() {
	b.async(func() {
		raw, err := b.call("imagegen.v1", "providers", nil)
		if err != nil {
			b.patch(func(s *snapshot) { s.ImageGen.Note = err.Error() })
			return
		}
		var out struct {
			Current   string `json:"current"`
			Available []struct {
				Name, BaseURL, Model, Note string
			} `json:"available"`
			Config struct {
				Provider, BaseURL, Model, APIKey, Dir, Size string
			} `json:"config"`
		}
		_ = json.Unmarshal(raw, &out)
		v := imagegenView{
			Current: out.Current, BaseURL: out.Config.BaseURL, Model: out.Config.Model,
			APIKey: out.Config.APIKey, Dir: out.Config.Dir, Size: out.Config.Size,
		}
		for _, p := range out.Available {
			v.Available = append(v.Available, providerChoice{Name: p.Name, BaseURL: p.BaseURL, Model: p.Model, Note: p.Note})
		}
		b.patch(func(s *snapshot) { s.ImageGen = v })
	})
}

func (b *bridge) saveImageGen(v imagegenView) {
	b.async(func() {
		_, err := b.call("imagegen.v1", "configure", map[string]any{
			"provider": v.Current, "baseUrl": v.BaseURL, "model": v.Model,
			"apiKey": v.APIKey, "dir": v.Dir, "size": v.Size,
		})
		if err == nil {
			b.loadImageGen()
		} else {
			b.patch(func(s *snapshot) { s.ImageGen.Note = err.Error() })
		}
	})
}

func (b *bridge) generateImage(prompt string) {
	b.patch(func(s *snapshot) { s.ImageGen.Busy = true; s.ImageGen.Message = "正在生成…" })
	b.async(func() {
		raw, err := b.call("imagegen.v1", "generate", map[string]any{"prompt": prompt})
		var msg string
		var paths []string
		if err != nil {
			msg = err.Error()
		} else {
			var out struct {
				OK      bool     `json:"ok"`
				Paths   []string `json:"paths"`
				Message string   `json:"message"`
				Count   int      `json:"count"`
			}
			_ = json.Unmarshal(raw, &out)
			paths = out.Paths
			msg = out.Message
			if msg == "" {
				msg = fmt.Sprintf("已生成 %d 张", out.Count)
			}
		}
		b.patch(func(s *snapshot) {
			s.ImageGen.Busy = false
			s.ImageGen.Message = msg
			if len(paths) > 0 {
				s.ImageGen.LastPaths = paths
			}
		})
	})
}

// ============================================================
// 插件管理 pluginManager.v1
// ============================================================
func (b *bridge) loadPluginMgr() {
	b.async(func() {
		raw, err := b.call("pluginManager.v1", "list", nil)
		if err != nil {
			b.patch(func(s *snapshot) { s.PluginMgr.Note = err.Error() })
			return
		}
		var parse = func(data json.RawMessage) []pluginMgrItem {
			type mgrJSON struct {
				ID, Name, Version, Description  string
				Runtime, Entry, Compat          string
				Provides, Consumes, Permissions []string
				Dir, DirPath                    string
				Disabled                        bool
				Self                            bool
			}
			var arr []mgrJSON
			if json.Unmarshal(data, &arr) != nil {
				var wrap struct {
					Plugins []mgrJSON `json:"plugins"`
				}
				if json.Unmarshal(data, &wrap) != nil {
					return nil
				}
				arr = wrap.Plugins
			}
			out := make([]pluginMgrItem, 0, len(arr))
			for _, p := range arr {
				out = append(out, pluginMgrItem{
					ID: p.ID, Name: p.Name, Version: p.Version, Description: p.Description,
					Runtime: p.Runtime, Entry: p.Entry, Compat: p.Compat,
					Provides: p.Provides, Consumes: p.Consumes, Permissions: p.Permissions,
					Dir: p.Dir, DirPath: p.DirPath,
					Disabled: p.Disabled, Protected: p.Self,
				})
			}
			return out
		}
		items := parse(raw)
		b.patch(func(s *snapshot) { s.PluginMgr = pluginMgrView{Items: items} })
	})
}

func (b *bridge) togglePlugin(id string, enable bool) {
	b.async(func() {
		method := "disable"
		if enable {
			method = "enable"
		}
		raw, err := b.call("pluginManager.v1", method, map[string]any{"id": id})
		if err == nil {
			// setEnabled 现在会返回是否还需重启：热加载成功就是"无需重启"。
			var res struct {
				Note string `json:"note"`
			}
			_ = json.Unmarshal(raw, &res)
			if res.Note != "" {
				b.patch(func(s *snapshot) { s.PluginMgr.Note = res.Note })
			}
			b.loadPluginMgr()
			// 服务已热卸载/热加载：重建侧栏里的插件页面，卸载后对应页面要立刻消失。
			if b.onPluginState != nil {
				go b.onPluginState()
			}
		} else {
			b.patch(func(s *snapshot) { s.PluginMgr.Note = err.Error() })
		}
	})
}

// deletePlugin 删除插件（目录 + 配置 + 日志），随后重新拉取列表。
func (b *bridge) deletePlugin(id string) {
	b.async(func() {
		raw, err := b.call("pluginManager.v1", "delete", map[string]any{"id": id})
		if err == nil {
			var res struct {
				Note string `json:"note"`
			}
			_ = json.Unmarshal(raw, &res)
			b.patch(func(s *snapshot) {
				if res.Note != "" {
					s.PluginMgr.Note = res.Note
				} else {
					s.PluginMgr.Note = "已删除插件；重启后生效"
				}
			})
			b.loadPluginMgr()
			if b.onPluginState != nil {
				go b.onPluginState()
			}
		} else {
			b.patch(func(s *snapshot) { s.PluginMgr.Note = err.Error() })
		}
	})
}

// ============================================================
// 工作区 workspace.v1
// ============================================================
func (b *bridge) loadWorkspaceRoot() {
	b.async(func() {
		raw, err := b.call("workspace.v1", "root", nil)
		if err != nil {
			b.patch(func(s *snapshot) { s.Workspace.Note = err.Error() })
			return
		}
		var out struct {
			Root string `json:"root"`
			Set  bool   `json:"set"`
			Hint string `json:"hint"`
		}
		_ = json.Unmarshal(raw, &out)
		root := out.Root
		b.patch(func(s *snapshot) {
			s.Workspace.Root = root
			s.Workspace.Set = out.Set
			if !out.Set {
				s.Workspace.Note = out.Hint
			}
		})
		if out.Set {
			b.browseWorkspace(root)
		}
	})
}

func (b *bridge) browseWorkspace(path string) {
	b.async(func() {
		params := map[string]any{}
		if path != "" {
			params["path"] = path
		}
		raw, err := b.call("workspace.v1", "browse", params)
		if err != nil {
			b.patch(func(s *snapshot) { s.Workspace.Note = err.Error() })
			return
		}
		var full struct {
			Path      string `json:"path"`
			Parent    string `json:"parent"`
			Root      string `json:"root"`
			IsRoot    bool   `json:"isRoot"`
			RootIsSet bool   `json:"rootIsSet"`
			Count     int    `json:"count"`
			Entries   []struct {
				Path string `json:"path"`
				Name string `json:"name"`
				Dir  bool   `json:"dir"`
				Size int64  `json:"size"`
			} `json:"entries"`
		}
		_ = json.Unmarshal(raw, &full)
		entries := make([]wsEntry, 0, len(full.Entries))
		for _, e := range full.Entries {
			entries = append(entries, wsEntry{Path: e.Path, Name: e.Name, Dir: e.Dir, Size: e.Size})
		}
		b.patch(func(s *snapshot) {
			s.Workspace.Path = full.Path
			s.Workspace.Parent = full.Parent
			s.Workspace.IsRoot = full.IsRoot
			s.Workspace.Entries = entries
			s.Workspace.Note = ""
		})
	})
}

func (b *bridge) pickWorkspaceRoot() {
	b.async(func() {
		raw, err := b.ctx.Capability("core.shell.pickfolder", map[string]any{"title": "选择工作区目录"})
		if err != nil {
			b.patch(func(s *snapshot) { s.Workspace.Note = "选择目录失败: " + err.Error() })
			return
		}
		var out struct {
			Path      string `json:"path"`
			Cancelled bool   `json:"cancelled"`
		}
		_ = json.Unmarshal(raw, &out)
		if out.Cancelled || out.Path == "" {
			return
		}
		_, err = b.call("workspace.v1", "setRoot", map[string]any{"path": out.Path})
		if err == nil {
			b.loadWorkspaceRoot()
		} else {
			b.patch(func(s *snapshot) { s.Workspace.Note = err.Error() })
		}
	})
}

// ============================================================
// 用量 usage.v1
// ============================================================
func (b *bridge) loadUsage() {
	b.async(func() {
		raw, err := b.call("usage.v1", "snapshot", nil)
		if err != nil {
			b.patch(func(s *snapshot) { s.Usage.Note = err.Error() })
			return
		}
		var out struct {
			Balance struct {
				OK        bool   `json:"ok"`
				Available bool   `json:"available"`
				Currency  string `json:"currency"`
				Total     string `json:"total"`
				Granted   string `json:"granted"`
				ToppedUp  string `json:"toppedUp"`
				CheckedAt string `json:"checkedAt"`
				Err       string `json:"err"`
			} `json:"balance"`
			Totals struct {
				Requests         int `json:"requests"`
				Failed           int `json:"failed"`
				PromptTokens     int `json:"promptTokens"`
				CompletionTokens int `json:"completionTokens"`
				TotalTokens      int `json:"totalTokens"`
				CacheHitTokens   int `json:"cacheHitTokens"`
				CacheMissTokens  int `json:"cacheMissTokens"`
			} `json:"totals"`
			HitRate   float64 `json:"hitRate"`
			KeySource string  `json:"keySource"`
			Note      string  `json:"note"`
		}
		_ = json.Unmarshal(raw, &out)
		hit := fmt.Sprintf("%.0f%%", out.HitRate*100)
		v := usageView{
			BalanceOK: out.Balance.OK, Available: out.Balance.Available,
			Currency: out.Balance.Currency, Total: out.Balance.Total,
			Granted: out.Balance.Granted, ToppedUp: out.Balance.ToppedUp,
			CheckedAt: out.Balance.CheckedAt, HitRateStr: hit,
			Requests: out.Totals.Requests, Failed: out.Totals.Failed,
			PromptTokens: out.Totals.PromptTokens, CompletionTokens: out.Totals.CompletionTokens,
			TotalTokens: out.Totals.TotalTokens, CacheHitTokens: out.Totals.CacheHitTokens,
			CacheMissTokens: out.Totals.CacheMissTokens,
			Note:            out.Note,
		}
		if out.Balance.Err != "" {
			v.Note = out.Balance.Err
		}
		b.patch(func(s *snapshot) { s.Usage = v })
	})
}

func (b *bridge) refreshBalance() {
	b.async(func() {
		_, _ = b.call("usage.v1", "balance", nil)
		b.loadUsage()
	})
}

func (b *bridge) resetUsage() {
	b.async(func() {
		_, _ = b.call("usage.v1", "reset", nil)
		b.loadUsage()
	})
}

// ============================================================
// 快照补丁：统一在锁内修改，避免与对话事件写冲突。
// ============================================================
func (b *bridge) patch(fn func(*snapshot)) {
	b.mu.Lock()
	fn(&b.snap)
	b.mu.Unlock()
	b.changed()
}

// 判断某服务是否可用。
func (b *bridge) hasService(name string) bool {
	_, err := b.ctx.Consume(name)
	return err == nil
}

// stripANSI 去掉 ANSI 样式码（合成器页的纯文本兜底渲染用）。
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// ============================================================
// ui.page.* 合成器兜底：动态发现 + 渲染 + 动作
// ============================================================

// panelPage 是一个合成页的清单项。
type panelPage struct {
	Service string `json:"service"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Order   int    `json:"order"`
}

// panelFrame 是一次画面渲染的结果（纯文本行 + 命中区）。
type panelFrame struct {
	Title     string
	Lines     []string
	Hits      []panelHit
	Scroll    int
	MaxScroll int
	Note      string
}

type panelHit struct {
	X, Y, W int
	Action  string
	Arg     string
}

// discoverPanelPages 扫描已安装插件的 manifest，找出所有 ui.page.* 服务及其实例页。
// 核心没有"列出服务"接口，只能读 manifest 的 provides（与 workbench 一致）。
func (b *bridge) discoverPanelPages() []panelPage {
	out := make([]panelPage, 0)
	raw, err := b.call("pluginManager.v1", "list", nil)
	if err != nil {
		return out
	}
	var arr []struct {
		ID       string   `json:"id"`
		Provides []string `json:"provides"`
		Disabled bool     `json:"disabled"`
	}
	if json.Unmarshal(raw, &arr) != nil {
		var wrap struct {
			Plugins []struct {
				ID       string   `json:"id"`
				Provides []string `json:"provides"`
				Disabled bool     `json:"disabled"`
			} `json:"plugins"`
		}
		if json.Unmarshal(raw, &wrap) != nil {
			return out
		}
		for _, p := range wrap.Plugins {
			arr = append(arr, p)
		}
	}
	for _, p := range arr {
		if p.Disabled {
			continue
		}
		for _, svc := range p.Provides {
			if !strings.HasPrefix(svc, "ui.page.") {
				continue
			}
			// 问插件它有哪些页。
			praw, e := b.call(svc, "list", nil)
			if e != nil {
				continue
			}
			var listing struct {
				Panels []struct {
					ID    string `json:"id"`
					Title string `json:"title"`
					Order int    `json:"order"`
				} `json:"panels"`
			}
			if json.Unmarshal(praw, &listing) != nil {
				continue
			}
			for _, panel := range listing.Panels {
				out = append(out, panelPage{Service: svc, ID: panel.ID, Title: panel.Title, Order: panel.Order})
			}
		}
	}
	// 按 order 排序（简单插入）。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Order < out[j-1].Order; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// renderPanelPage 渲染合成页的当前画面。
func (b *bridge) renderPanelPage(p panelPage, w, h, scroll int) panelFrame {
	var frame panelFrame
	raw, err := b.call(p.Service, "render", map[string]any{
		"panel": p.ID, "w": w, "h": h, "scroll": scroll,
	})
	if err != nil {
		frame.Lines = []string{"页面暂不可用", err.Error()}
		return frame
	}
	var out struct {
		Title string   `json:"title"`
		Lines []string `json:"lines"`
		Hits  []struct {
			Y      int    `json:"y"`
			X      int    `json:"x"`
			W      int    `json:"w"`
			Action string `json:"action"`
			Arg    string `json:"arg"`
		} `json:"hits"`
		Scroll    int    `json:"scroll"`
		MaxScroll int    `json:"maxScroll"`
		Note      string `json:"note"`
	}
	if json.Unmarshal(raw, &out) != nil {
		frame.Lines = []string{"画面解析失败"}
		return frame
	}
	frame.Title = out.Title
	frame.Lines = out.Lines
	frame.Scroll = out.Scroll
	frame.MaxScroll = out.MaxScroll
	frame.Note = out.Note
	for _, hit := range out.Hits {
		frame.Hits = append(frame.Hits, panelHit{X: hit.X, Y: hit.Y, W: hit.W, Action: hit.Action, Arg: hit.Arg})
	}
	return frame
}

// panelAction 转发点击动作给插件。
func (b *bridge) panelAction(p panelPage, panelID, action, arg string) string {
	raw, err := b.call(p.Service, "action", map[string]any{
		"panel": panelID, "action": action, "arg": arg,
	})
	if err != nil {
		return err.Error()
	}
	var out struct {
		Note string `json:"note"`
	}
	if json.Unmarshal(raw, &out) == nil && out.Note != "" {
		return out.Note
	}
	return ""
}
