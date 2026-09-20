// 会话树的持久化、会话生命周期与 Markdown 序列化。
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"aiwork/sdk/go/plugin"
)

// configKeyConversations 是整棵树在插件配置里的键名。
//
// 一次 SetConfig 全量写：核心的配置层没有分片能力，树又必须保持整体一致
// （父节点的 Active 指向兄弟组里的下标，拆开存一定会对不上）。
// 体积由 tree.go 里的几个上限兜住。
const configKeyConversations = "conversations"

// sessionInfo 是会话语列表里的一项。
type sessionInfo struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Nodes     int    `json:"nodes"`
	Updated   string `json:"updated,omitempty"`
}

func (st *Store) load(ctx *plugin.Context) error {
	raw, err := ctx.GetConfig(configKeyConversations)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var saved Store
	if err := json.Unmarshal(raw, &saved); err != nil {
		return err
	}
	if saved.Session == nil {
		saved.Session = map[string]*Session{}
	}
	for _, s := range saved.Session {
		if s == nil {
			continue
		}
		if s.Nodes == nil {
			s.Nodes = map[string]*Node{}
		}
	}
	st.Version = saved.Version
	st.Current = saved.Current
	st.Session = saved.Session
	return nil
}

func (st *Store) save(ctx *plugin.Context) error {
	return ctx.SetConfig(configKeyConversations, st)
}

func (st *Store) sortedIDs() []string {
	out := make([]string, 0, len(st.Session))
	for id := range st.Session {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// List 返回全部会话的概要（按会话 ID 排序，输出稳定）。
func (st *Store) List() []sessionInfo {
	out := make([]sessionInfo, 0, len(st.Session))
	for _, id := range st.sortedIDs() {
		s := st.Session[id]
		if s == nil {
			continue
		}
		out = append(out, sessionInfo{
			SessionID: s.ID,
			Name:      s.Name,
			Nodes:     len(s.Nodes),
			Updated:   s.Updated,
		})
	}
	return out
}

// Resolve 解析请求里的会话 ID：指定 > 当前 > 稳定地挑一个 > 新建。
//
// 指定了一个不存在的 ID 是调用方的错，直接报错 —— 悄悄换成别的会话
// 会让界面显示和用户预期对不上。
func (st *Store) Resolve(requested string) (*Session, error) {
	if id := sanitizeID(requested); id != "" {
		s, ok := st.Session[id]
		if !ok || s == nil {
			return nil, plugin.Errorf(plugin.CodeInvalidParams, "找不到会话 %s", id)
		}
		st.Current = id
		return s, nil
	}
	if s, ok := st.Session[st.Current]; ok && s != nil {
		return s, nil
	}
	if len(st.Session) > 0 {
		for _, id := range st.sortedIDs() {
			if s := st.Session[id]; s != nil {
				st.Current = id
				return s, nil
			}
		}
	}
	return st.New("")
}

// New 新建一个会话并切过去。
func (st *Store) New(name string) (*Session, error) {
	if st.Session == nil {
		st.Session = map[string]*Session{}
	}
	if len(st.Session) >= maxSessions {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "会话数已达上限 %d，请先删掉不用的会话", maxSessions)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("会话 %d", len(st.Session)+1)
	}
	s := &Session{ID: st.freeID(), Name: name, Nodes: map[string]*Node{}}
	st.Session[s.ID] = s
	st.Current = s.ID
	return s, nil
}

// Delete 删除一个会话，返回它带走多少个节点。
//
// 删的是 Session 这个 map 条目，配置键本身仍在 —— 符合核心「配置键只增不减」。
// 删空之后立刻补一个空会话，界面永远有一个可用的落点。
func (st *Store) Delete(id string) (int, error) {
	s, ok := st.Session[id]
	if !ok || s == nil {
		return 0, plugin.Errorf(plugin.CodeInvalidParams, "找不到会话 %s", id)
	}
	n := len(s.Nodes)
	delete(st.Session, id)
	if st.Current == id {
		st.Current = ""
	}
	if len(st.Session) == 0 {
		_, err := st.New("")
		return n, err
	}
	if st.Current == "" {
		for _, key := range st.sortedIDs() {
			st.Current = key
			break
		}
	}
	return n, nil
}

// freeID 造一个没人用过的会话 ID：时间戳打底，撞了就加序号。
func (st *Store) freeID() string {
	base := "s" + time.Now().Format("20060102-150405")
	id := base
	for i := 2; ; i++ {
		if _, taken := st.Session[id]; !taken {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

// sanitizeID 只保留安全的字符，避免会话 ID 变成奇怪的东西。
func sanitizeID(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			out = append(out, r)
		case r == ' ':
			out = append(out, '-')
		}
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

// roleLabel 把角色翻成界面上用的两个字。
func roleLabel(role string) string {
	if role == roleUser {
		return "你"
	}
	return "AI"
}

// Markdown 把一条时间线渲染成可分享的 Markdown。
func Markdown(s *Session, path []*Node) string {
	var b strings.Builder
	b.WriteString("# " + s.Name + "\n")
	for _, n := range path {
		version, total := s.VersionOf(n.ID)
		b.WriteString(fmt.Sprintf("\n### %s (%d/%d) · %s\n\n", roleLabel(n.Role), version, total, n.At))
		if n.Err != "" {
			b.WriteString("> 生成失败：" + n.Err + "\n")
			continue
		}
		b.WriteString(n.Text + "\n")
	}
	return b.String()
}
