package main

import "encoding/json"

// ---------------------------------------------------------------------------
// 序列化的形状
//
// 界面里几个结构体的字段是小写（只在本包内用），直接 json.Marshal 会得到一个 {}：
// 数据静默丢失，而且不报任何错。
//
// 所以把 JSON 形状补在类型自己身上：一处定义，编码与解码共用，谁都不会漏字段。
// 这只加序列化的形状，不改字段本身，界面其余部分不受影响。
// ---------------------------------------------------------------------------

type wireService struct {
	Name     string `json:"name"`
	Alive    bool   `json:"alive"`
	Provider string `json:"provider"`
	Note     string `json:"note"`
}

func (s serviceStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireService{Name: s.name, Alive: s.alive, Provider: s.provider, Note: s.note})
}

func (s *serviceStatus) UnmarshalJSON(b []byte) error {
	var w wireService
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	s.name, s.alive, s.provider, s.note = w.Name, w.Alive, w.Provider, w.Note
	return nil
}

type wireSession struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Nodes   int    `json:"nodes"`
	Updated string `json:"updated"`
}

func (s sessionInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireSession{ID: s.id, Name: s.name, Nodes: s.nodes, Updated: s.updated})
}

func (s *sessionInfo) UnmarshalJSON(b []byte) error {
	var w wireSession
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	s.id, s.name, s.nodes, s.updated = w.ID, w.Name, w.Nodes, w.Updated
	return nil
}

type wireMsg struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	At      string `json:"at"`
	Text    string `json:"text"`
	Version int    `json:"version"`
	Total   int    `json:"total"`
	Via     string `json:"via"`
	Err     string `json:"err"`
	Trunc   bool   `json:"trunc"`
}

func (m msgView) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireMsg{
		ID: m.id, Role: m.role, At: m.at, Text: m.text,
		Version: m.version, Total: m.total, Via: m.via, Err: m.err, Trunc: m.trunc,
	})
}

func (m *msgView) UnmarshalJSON(b []byte) error {
	var w wireMsg
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	m.id, m.role, m.at, m.text = w.ID, w.Role, w.At, w.Text
	m.version, m.total, m.via, m.err, m.trunc = w.Version, w.Total, w.Via, w.Err, w.Trunc
	return nil
}

type wireConv struct {
	SessionID string        `json:"sessionId"`
	Name      string        `json:"name"`
	Note      string        `json:"note"`
	Sessions  []sessionInfo `json:"sessions"`
	Msgs      []msgView     `json:"msgs"`
}

func (s convSnapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireConv{
		SessionID: s.sessionID, Name: s.name, Note: s.note,
		Sessions: s.sessions, Msgs: s.msgs,
	})
}

func (s *convSnapshot) UnmarshalJSON(b []byte) error {
	var w wireConv
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	s.sessionID, s.name, s.note = w.SessionID, w.Name, w.Note
	s.sessions, s.msgs = w.Sessions, w.Msgs
	return nil
}
