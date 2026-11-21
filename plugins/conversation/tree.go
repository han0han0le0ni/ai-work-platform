// 链式对话的纯数据层：消息树、版本切换、路径解析。
//
// 这里不碰任何 IO，也不认识插件上下文 —— 全是可以直接单测的函数。
// 界面看到的「一条时间线」就是 Path()，树里其余的节点是隐没但仍在的旧时间线。
package main

import (
	"fmt"
	"time"

	"aiwork/sdk/go/plugin"
)

// 角色取值。
const (
	roleUser      = "user"
	roleAssistant = "assistant"
)

// 体积护栏：整个会话树存在插件配置的一个键里，没有分片能力，必须自己收着点。
const (
	maxTextRunes       = 8000 // 单条消息字数上限，超出截断并标记
	maxNodesPerSession = 400  // 单会话节点上限，超出拒绝新增（不删旧的）
	maxSessions        = 32   // 会话数上限
	maxTurnsForModel   = 16   // 只裁剪喂给模型的历史，不影响存储
)

// Node 是树上的一个节点，也就是一条消息的一个版本。
//
// 版本切换只改父节点的 Active 下标，从不删除任何节点 ——
// 旧版本连同它的整棵子树都留在 Nodes 里，翻回去就重新出现在 Path() 上。
type Node struct {
	ID       string   `json:"id"`
	Parent   string   `json:"parent,omitempty"` // "" 表示根
	Role     string   `json:"role"`
	Text     string   `json:"text"`
	At       string   `json:"at"`
	Via      string   `json:"via,omitempty"`   // 这条回复走的链路：agent.v1 / llm.v1
	Err      string   `json:"err,omitempty"`   // 生成失败的原因；仍是一个可以重生成的节点
	Trunc    bool     `json:"trunc,omitempty"` // Text 被上限截断
	Children []string `json:"children,omitempty"`
	Active   int      `json:"active"` // 当前选中第几个 child
}

// Session 是一条对话：一棵以 Roots 为根的树。
type Session struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	System     string           `json:"system,omitempty"`
	Updated    string           `json:"updated,omitempty"`
	Seq        int              `json:"seq"`        // 节点 ID 计数器，重启后接着发
	Roots      []string         `json:"roots"`      // 首条消息的多个版本
	RootActive int              `json:"rootActive"` // 当前选中第几个根
	Nodes      map[string]*Node `json:"nodes"`
}

// Store 是全部会话加上当前选中的那一个。
type Store struct {
	Version int                 `json:"version"`
	Current string              `json:"current"`
	Session map[string]*Session `json:"sessions"`
}

func newStore() *Store {
	return &Store{Version: 1, Session: map[string]*Session{}}
}

func nowISO() string { return time.Now().Format(time.RFC3339) }

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// group 返回某个父节点下的兄弟组（"" 表示根组）。
//
// 返回指针是因为 Nodes 里存的是 *Node，改 &n.Children 就等于改在树上；
// 根组则直接指向 s.Roots。父节点不存在时返回 nil，由调用方兜底。
func (s *Session) group(parent string) *[]string {
	if parent == "" {
		return &s.Roots
	}
	if n, ok := s.Nodes[parent]; ok {
		return &n.Children
	}
	return nil
}

// selectChild 把某个父节点的当前选中版本切到 idx。
func (s *Session) selectChild(parent string, idx int) {
	if parent == "" {
		s.RootActive = idx
		return
	}
	if n, ok := s.Nodes[parent]; ok {
		n.Active = idx
	}
}

func indexOf(list []string, id string) int {
	for i, v := range list {
		if v == id {
			return i
		}
	}
	return -1
}

// Path 解析出当前可见的那一条时间线。
//
// 从当前根出发，每步走父节点 Active 指向的那个孩子，直到叶子。
// 不在结果里的节点就是「翻页隐没」的旧时间线 —— 它们一个都没被删。
func (s *Session) Path() []*Node {
	out := make([]*Node, 0, 8)
	if len(s.Roots) == 0 {
		return out
	}
	seen := map[string]bool{}
	cur := s.Roots[clampInt(s.RootActive, 0, len(s.Roots)-1)]
	for cur != "" && !seen[cur] {
		seen[cur] = true
		n, ok := s.Nodes[cur]
		if !ok {
			break
		}
		out = append(out, n)
		if len(n.Children) == 0 {
			break
		}
		cur = n.Children[clampInt(n.Active, 0, len(n.Children)-1)]
	}
	return out
}

// PathTo 返回从根一路到 id（含 id）的祖先链，按时间先后排列。
//
// 它就是「这条消息当时看到的上下文」：重新生成和改完之后的自动回复都按它喂模型。
// id 为空时返回空链（表示空上下文）。
func (s *Session) PathTo(id string) []*Node {
	var chain []*Node
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur]; {
		seen[cur] = true
		n, ok := s.Nodes[cur]
		if !ok {
			break
		}
		chain = append(chain, n)
		cur = n.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// Leaf 返回当前路径的末端节点 ID，空会话返回 ""。
func (s *Session) Leaf() string {
	p := s.Path()
	if len(p) == 0 {
		return ""
	}
	return p[len(p)-1].ID
}

// AddNode 在 parent 之下追加一个节点，并把父节点的当前版本切到它。
//
// parent 为 "" 表示挂在根组下（首条消息，或首条消息的新版本）。
// 父节点不存在时退化成挂在根下 —— 数据损坏也不该把用户刚说的话丢掉。
func (s *Session) AddNode(parent, role, text, via, errMsg string) *Node {
	if s.Nodes == nil {
		s.Nodes = map[string]*Node{}
	}
	s.Seq++

	n := &Node{
		ID:     fmt.Sprintf("n%d", s.Seq),
		Parent: parent,
		Role:   role,
		At:     nowISO(),
		Via:    via,
		Err:    errMsg,
	}
	if r := []rune(text); len(r) > maxTextRunes {
		n.Text = string(r[:maxTextRunes])
		n.Trunc = true
	} else {
		n.Text = text
	}

	g := s.group(parent)
	if g == nil {
		n.Parent = ""
		g = &s.Roots
	}
	s.Nodes[n.ID] = n
	*g = append(*g, n.ID)
	s.selectChild(n.Parent, len(*g)-1)
	s.Updated = n.At
	return n
}

// EditNode 把一条消息改成一个新版本。
//
// 关键：新版本是旧版本的**兄弟**，追加在同一个父节点下，然后把当前版本切过去。
// 旧版本原地不动，它后面那一串消息因此从 Path() 上隐没（界面上就是「翻页」了），
// 但它们在 Nodes 里一条都没少，翻回旧版本就原样重现。
func (s *Session) EditNode(id, text string) (*Node, error) {
	old, ok := s.Nodes[id]
	if !ok {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "找不到消息 %s", id)
	}
	return s.AddNode(old.Parent, old.Role, text, old.Via, ""), nil
}

// SwitchVersion 在一条消息的多个版本之间前后翻页（delta 为 ±1）。
//
// 翻的是「它父节点下选中了哪一个孩子」，因此路径的前缀完全不变，
// 只有这条消息和它下游会变 —— 界面上滚动位置因此能停在原地。
func (s *Session) SwitchVersion(id string, delta int) error {
	n, ok := s.Nodes[id]
	if !ok {
		return plugin.Errorf(plugin.CodeInvalidParams, "找不到消息 %s", id)
	}
	g := s.group(n.Parent)
	if g == nil || len(*g) == 0 {
		return nil
	}
	cur := indexOf(*g, id)
	if cur < 0 {
		return nil
	}
	s.selectChild(n.Parent, clampInt(cur+delta, 0, len(*g)-1))
	return nil
}

// VersionOf 返回一条消息是它那组版本里的第几个、共几个。
func (s *Session) VersionOf(id string) (version, total int) {
	n, ok := s.Nodes[id]
	if !ok {
		return 0, 0
	}
	g := s.group(n.Parent)
	if g == nil {
		return 1, 1
	}
	return indexOf(*g, id) + 1, len(*g)
}

// DeleteNode 删除一条消息**及其整棵下游子树**，并把它从兄弟组里摘除。
//
// 这是用户显式的删除，和「改消息只增不删」的版本哲学不同：节点连同从它
// 长出来的所有版本与回复一起消失。删掉的若正是父节点当前选中的版本，
// 选中位回退到前一个兄弟；整组删空时父节点自然变成叶子，根组删空即空会话。
// 返回实际删除的节点数。
func (s *Session) DeleteNode(id string) (int, error) {
	n, ok := s.Nodes[id]
	if !ok {
		return 0, plugin.Errorf(plugin.CodeInvalidParams, "找不到消息 %s", id)
	}

	removed := 0
	var walk func(string)
	walk = func(x string) {
		xn, ok := s.Nodes[x]
		if !ok {
			return
		}
		for _, ch := range xn.Children {
			walk(ch)
		}
		delete(s.Nodes, x)
		removed++
	}
	walk(id)

	parent := n.Parent
	g := s.group(parent)
	idx := -1
	if g != nil {
		idx = indexOf(*g, id)
		if idx >= 0 {
			*g = append((*g)[:idx], (*g)[idx+1:]...)
		}
	}

	// 修正父节点的选中位：删的在选中位之前（或就是选中位），选中位前移一格。
	if parent == "" {
		switch {
		case len(s.Roots) == 0:
			s.RootActive = 0
		case idx >= 0 && idx <= s.RootActive:
			s.RootActive = clampInt(s.RootActive-1, 0, len(s.Roots)-1)
		}
	} else if p, ok := s.Nodes[parent]; ok {
		switch {
		case len(p.Children) == 0:
			p.Active = 0
		case idx >= 0 && idx <= p.Active:
			p.Active = clampInt(p.Active-1, 0, len(p.Children)-1)
		}
	}
	s.Updated = nowISO()
	return removed, nil
}
