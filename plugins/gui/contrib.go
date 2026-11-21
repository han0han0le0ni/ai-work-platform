package main

// 插件 UI 贡献（模仿 DSH 的 slot 机制）。
//
// DSH 的界面不是"插件各画一整页"，而是壳子先声明若干插槽
// （sidebar.panellist / sidebar.settings / sidebar.footer.action /
// settings.section ...），插件包把带 id/order/label 的条目注册进插槽，
// 壳子负责几何与渲染，插件只提供内容。见
// dsh-client-ui-sidebar/src/client/contract/slots.ts。
//
// 这里做同样的事：插件在 manifest 里声明 ui.contrib.v1 并实现 list，
// 返回带 slot 的条目；GUI 统一收集后画到对应的位置。老的 ui.page.*
// 不必改，加载器会把没人认领的面板折算成 sidebar.nav 条目（等价于
// DSH 的 panel 元数据 -> 侧栏行）。页面素材仍然按需拉取。

import (
	"encoding/json"
	"sort"
	"strings"

	"gioui.org/layout"
)

// 支持的插槽名。
const (
	slotSidebarNav      = "sidebar.nav"           // 侧栏导航行（打开插件页 / 原生页）
	slotFooterAction    = "sidebar.footer.action" // 设置上方的脚部动作
	slotHeaderAction    = "header.action"         // 顶栏右侧动作
	slotSettingsSection = "settings.section"      // 设置弹层左侧分区
)

type contribPage struct {
	Service string `json:"service"`
	Panel   string `json:"panel"`
	Native  string `json:"native"` // 直接指向内置页（chat/model/...）
}

type contribAction struct {
	Service string         `json:"service"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type contribution struct {
	Slot   string         `json:"slot"`
	ID     string         `json:"id"`
	Label  string         `json:"label"`
	Icon   string         `json:"icon"`
	Order  int            `json:"order"`
	Page   *contribPage   `json:"page"`
	Action *contribAction `json:"action"`
	Source string         `json:"-"` // 提供者插件 ID（排错用）
	Auto   bool           `json:"-"` // 由 ui.page.* 折算出来的兜底条目
}

// loadContributions 汇总所有 ui.contrib.v1 提供者的条目，并把没有显式
// 认领的 ui.page.* 面板折算成侧栏导航条目。结果写进快照，版本号 +1。
func (b *bridge) loadContributions() {
	b.async(func() {
		out := make([]contribution, 0)
		explicit := make(map[string]bool) // service/panel 已被显式 nav 认领

		raw, err := b.call("pluginManager.v1", "list", nil)
		if err == nil {
			for _, p := range parsePluginList(raw) {
				if p.Disabled {
					continue
				}
				hasContrib := false
				for _, svc := range p.Provides {
					if svc == "ui.contrib.v1" {
						hasContrib = true
						break
					}
				}
				if !hasContrib {
					continue
				}
				craw, e := b.call("ui.contrib.v1", "list", nil)
				if e != nil {
					continue
				}
				var listing struct {
					Contributions []contribution `json:"contributions"`
				}
				var wrapped struct {
					Items []contribution `json:"items"`
				}
				if json.Unmarshal(craw, &listing) == nil && len(listing.Contributions) > 0 {
					for i := range listing.Contributions {
						listing.Contributions[i].Source = p.ID
						out = append(out, listing.Contributions[i])
					}
				} else if json.Unmarshal(craw, &wrapped) == nil {
					for i := range wrapped.Items {
						wrapped.Items[i].Source = p.ID
						out = append(out, wrapped.Items[i])
					}
				}
			}
		}

		for _, c := range out {
			if c.Slot == slotSidebarNav && c.Page != nil {
				explicit[c.Page.Service+"/"+c.Page.Panel] = true
			}
		}

		// 兜底：老的 ui.page.* 面板 -> 侧栏导航条目（DSH 也是从 panel
		// 列表元数据解析出侧栏行，插件不必自己写一行导航）。
		for _, pp := range b.discoverPanelPages() {
			if explicit[pp.Service+"/"+pp.ID] {
				continue
			}
			icon := ""
			if r := []rune(pp.Title); len(r) > 0 {
				icon = string(r[0])
			}
			out = append(out, contribution{
				Slot:  slotSidebarNav,
				ID:    pp.ID,
				Label: pp.Title,
				Icon:  icon,
				Order: pp.Order,
				Page:  &contribPage{Service: pp.Service, Panel: pp.ID},
				Auto:  true,
			})
		}

		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Slot != out[j].Slot {
				return out[i].Slot < out[j].Slot
			}
			return out[i].Order < out[j].Order
		})

		b.patch(func(s *snapshot) {
			s.Contribs = out
			s.ContribsLoaded = true
			s.ContribsRev++
		})
	})
}

// pluginBrief 是 pluginManager.v1/list 里用得到的字段。
type pluginBrief struct {
	ID       string   `json:"id"`
	Provides []string `json:"provides"`
	Disabled bool     `json:"disabled"`
}

// parsePluginList 兼容裸数组与 {plugins:[...]} 两种返回。
func parsePluginList(raw json.RawMessage) []pluginBrief {
	var arr []pluginBrief
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		return arr
	}
	var wrap struct {
		Plugins []pluginBrief `json:"plugins"`
	}
	if json.Unmarshal(raw, &wrap) == nil {
		return wrap.Plugins
	}
	return arr
}

// contribsOf 取快照里某个插槽的条目（保持快照下标，便于对齐点击控件）。
func (u *UI) contribsOf(slot string) []int {
	snap := u.bridge.get()
	out := make([]int, 0, 4)
	for i := range snap.Contribs {
		if strings.EqualFold(snap.Contribs[i].Slot, slot) {
			out = append(out, i)
		}
	}
	return out
}

// isActionSlot 判断插槽是不是"点一下就执行"的动作位（脚部 / 顶栏）。
func isActionSlot(slot string) bool {
	return strings.EqualFold(slot, slotFooterAction) || strings.EqualFold(slot, slotHeaderAction)
}

// pageKeyFor 返回贡献条目指向的页面 key。
// 原生页直接用固定 key；插件页沿用合成页的 "panel:service/panel"。
func pageKeyFor(c contribution) string {
	if c.Page == nil {
		return ""
	}
	if c.Page.Native != "" {
		return c.Page.Native
	}
	if c.Page.Service == "" || c.Page.Panel == "" {
		return ""
	}
	return "panel:" + c.Page.Service + "/" + c.Page.Panel
}

// runContribution 执行一个贡献条目：有页面目标就切页，否则异步调动作。
func (u *UI) runContribution(c contribution) {
	if key := pageKeyFor(c); key != "" {
		for i := range u.pages {
			if u.pages[i].key == key {
				u.activeKey = key
				u.onPageEnter(u.pages[i])
				u.window.Invalidate()
				return
			}
		}
		return
	}
	if c.Action != nil {
		a := c.Action
		go func() { _, _ = u.bridge.call(a.Service, a.Method, a.Params) }()
	}
}

// handleContributionClicks 处理脚部与顶栏动作的点击。
// 与 draw 用同一组 clickable（按快照下标对齐），避免"画了却点不到"。
func (u *UI) handleContributionClicks(gtx layout.Context) {
	snap := u.bridge.get()
	for i := range snap.Contribs {
		if !isActionSlot(snap.Contribs[i].Slot) {
			continue
		}
		if clickAt(&u.contribBtns, i).Clicked(gtx) {
			u.runContribution(snap.Contribs[i])
			return
		}
	}
}

// headerActions 返回顶栏动作的条目下标；最多 3 个，再多顶栏放不下。
func (u *UI) headerActions() []int {
	all := u.contribsOf(slotHeaderAction)
	if len(all) > 3 {
		all = all[:3]
	}
	return all
}
