package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func testSession(t *testing.T) *Session {
	t.Helper()
	st := newStore()
	s, err := st.New("测试")
	if err != nil {
		t.Fatalf("新建会话失败: %v", err)
	}
	return s
}

// buildTimeline 造一条「两问两答」的时间线，返回四个节点 ID。
func buildTimeline(t *testing.T, s *Session) (u1, a1, u2, a2 *Node) {
	t.Helper()
	u1 = s.AddNode("", roleUser, "第一问", "", "")
	a1 = s.AddNode(u1.ID, roleAssistant, "第一答", agentService, "")
	u2 = s.AddNode(a1.ID, roleUser, "第二问", "", "")
	a2 = s.AddNode(u2.ID, roleAssistant, "第二答", agentService, "")
	return
}

func ids(path []*Node) []string {
	out := make([]string, 0, len(path))
	for _, n := range path {
		out = append(out, n.ID)
	}
	return out
}

// 改一条消息 = 追加一个新版本（兄弟），当前路径切过去，旧时间线一条不删。
func TestEditCreatesSiblingAndKeepsOldTimeline(t *testing.T) {
	s := testSession(t)
	u1, a1, u2, a2 := buildTimeline(t, s)
	_ = u1
	_ = a1

	before := len(s.Nodes)
	nu2, err := s.EditNode(u2.ID, "第二问（改过）")
	if err != nil {
		t.Fatalf("编辑失败: %v", err)
	}
	if nu2.ID == u2.ID {
		t.Fatal("编辑应当产生一个新版本，而不是原地改")
	}

	// 前缀（第一问/第一答）不变，从被改的那条起走新枝
	path := s.Path()
	if got := ids(path); !reflect.DeepEqual(got, []string{u1.ID, a1.ID, nu2.ID}) {
		t.Fatalf("改完之后路径 = %v，想要 [u1 a1 nu2]", got)
	}
	if path[2].Text != "第二问（改过）" {
		t.Fatalf("路径上应当是改后的文本，实际 %q", path[2].Text)
	}

	// 旧版本和它的整棵子树一条都没少，字段也没被动过
	if len(s.Nodes) != before+1 {
		t.Fatalf("节点数 = %d，想要 %d（只该多一条新版本）", len(s.Nodes), before+1)
	}
	old := s.Nodes[u2.ID]
	if old.Text != "第二问" {
		t.Fatalf("旧版本被改写了: %q", old.Text)
	}
	if len(old.Children) != 1 || old.Children[0] != a2.ID {
		t.Fatalf("旧版本的孩子被动过: %v", old.Children)
	}
	if _, ok := s.Nodes[a2.ID]; !ok {
		t.Fatal("旧时间线的回复被删了")
	}

	// 改完补一条新回复：旧的那串消息就此「翻页」隐没
	na2 := s.AddNode(nu2.ID, roleAssistant, "重答", agentService, "")
	if got := ids(s.Path()); !reflect.DeepEqual(got, []string{u1.ID, a1.ID, nu2.ID, na2.ID}) {
		t.Fatalf("补完回复后路径 = %v", got)
	}
	if a2.ID == na2.ID {
		t.Fatal("新回复不该复用旧节点")
	}
}

// 翻回旧版本，旧版本下游的消息应当原样重现。
func TestSwitchVersionRestoresOldTimeline(t *testing.T) {
	s := testSession(t)
	u1, a1, u2, a2 := buildTimeline(t, s)
	nu2, err := s.EditNode(u2.ID, "第二问（改过）")
	if err != nil {
		t.Fatalf("编辑失败: %v", err)
	}
	na2 := s.AddNode(nu2.ID, roleAssistant, "重答", agentService, "")

	// 此刻可见的是新时间线
	if got := ids(s.Path()); !reflect.DeepEqual(got, []string{u1.ID, a1.ID, nu2.ID, na2.ID}) {
		t.Fatalf("新时间线路径 = %v", got)
	}

	// 在新版本上点「<」：应该翻回旧版本，它后面的回复也跟着回来
	if err := s.SwitchVersion(nu2.ID, -1); err != nil {
		t.Fatalf("翻页失败: %v", err)
	}
	want := []string{u1.ID, a1.ID, u2.ID, a2.ID}
	if got := ids(s.Path()); !reflect.DeepEqual(got, want) {
		t.Fatalf("翻回旧版本后路径 = %v，想要 %v", got, want)
	}

	// 再点「>」又回到新时间线，两条时间线都还在
	if err := s.SwitchVersion(u2.ID, 1); err != nil {
		t.Fatalf("翻页失败: %v", err)
	}
	if got := ids(s.Path()); !reflect.DeepEqual(got, []string{u1.ID, a1.ID, nu2.ID, na2.ID}) {
		t.Fatalf("翻回新版本后路径 = %v", got)
	}
}

// 重新生成喂给模型的上下文 = 从根到锚点的那条祖先链。
func TestRegenerateContextIsPathToAnchor(t *testing.T) {
	s := testSession(t)
	u1, a1, u2, a2 := buildTimeline(t, s)

	// 点 AI 的消息：锚点是它的父节点（也就是那条用户消息）
	if got := ids(s.PathTo(a2.Parent)); !reflect.DeepEqual(got, []string{u1.ID, a1.ID, u2.ID}) {
		t.Fatalf("重生成上下文 = %v", got)
	}
	msgs := modelMessages(s.PathTo(a2.Parent))
	if len(msgs) != 3 || msgs[2].Content != "第二问" {
		t.Fatalf("喂给模型的消息不对: %+v", msgs)
	}
	if last := msgs[len(msgs)-1]; last.Role != roleUser {
		t.Fatalf("重生成时最后一条应当是用户消息，实际 %q", last.Role)
	}

	// 点用户的消息：锚点是它自己
	if got := ids(s.PathTo(u2.ID)); !reflect.DeepEqual(got, []string{u1.ID, a1.ID, u2.ID}) {
		t.Fatalf("用户消息重生成上下文 = %v", got)
	}
}

// 生成失败：节点照样保留，叶子仍是 assistant，失败的那条不进上下文。
func TestFailureKeepsNodesAndLeafIsAssistant(t *testing.T) {
	s := testSession(t)
	u := s.AddNode("", roleUser, "问一句", "", "")
	a := s.AddNode(u.ID, roleAssistant, "", "", "链路未装齐：至少需要 agent.v1 或 llm.v1")

	path := s.Path()
	if len(path) != 2 {
		t.Fatalf("路径长度 = %d，想要 2（失败也要留住节点）", len(path))
	}
	if a.Err == "" || path[1].Err != a.Err {
		t.Fatal("失败的回复应当带上原因，界面才有东西可显示")
	}
	if leaf := s.Path()[len(s.Path())-1]; leaf.Role != roleAssistant {
		t.Fatalf("叶子应当恒为 assistant，实际 %q", leaf.Role)
	}
	if msgs := modelMessages(path); len(msgs) != 1 || msgs[0].Role != roleUser {
		t.Fatalf("失败节点不该进上下文: %+v", msgs)
	}
}

func TestVersionOf(t *testing.T) {
	s := testSession(t)
	u := s.AddNode("", roleUser, "问", "", "")
	if v, total := s.VersionOf(u.ID); v != 1 || total != 1 {
		t.Fatalf("单版本应当是 1/1，实际 %d/%d", v, total)
	}
	second, err := s.EditNode(u.ID, "问（第二版）")
	if err != nil {
		t.Fatalf("编辑失败: %v", err)
	}
	if v, total := s.VersionOf(second.ID); v != 2 || total != 2 {
		t.Fatalf("新版本应当是 2/2，实际 %d/%d", v, total)
	}
	if v, total := s.VersionOf(u.ID); v != 1 || total != 2 {
		t.Fatalf("旧版本应当是 1/2，实际 %d/%d", v, total)
	}
}

// 翻到头就停住，不绕圈。
func TestSwitchVersionClampsAtEnds(t *testing.T) {
	s := testSession(t)
	u := s.AddNode("", roleUser, "问", "", "")
	second, err := s.EditNode(u.ID, "问二")
	if err != nil {
		t.Fatalf("编辑失败: %v", err)
	}
	if err := s.SwitchVersion(second.ID, 5); err != nil {
		t.Fatalf("翻页失败: %v", err)
	}
	if v, _ := s.VersionOf(s.Path()[0].ID); v != 2 {
		t.Fatalf("越界翻页应当停在最后一版，实际第 %d 版", v)
	}
	if err := s.SwitchVersion(second.ID, -5); err != nil {
		t.Fatalf("翻页失败: %v", err)
	}
	if v, _ := s.VersionOf(s.Path()[0].ID); v != 1 {
		t.Fatalf("越界翻页应当停在第一版，实际第 %d 版", v)
	}
}

func TestUnknownNodeIsRejected(t *testing.T) {
	s := testSession(t)
	if _, err := s.EditNode("nope", "x"); err == nil {
		t.Fatal("改一条不存在的消息应当报错")
	}
	if err := s.SwitchVersion("nope", 1); err == nil {
		t.Fatal("翻一条不存在的消息应当报错")
	}
}

// 删会话只带走自己，别的会话逐字段不变。
func TestDeleteSessionLeavesOtherSessionsIntact(t *testing.T) {
	st := newStore()
	a, err := st.New("甲")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	buildTimeline(t, a)
	b, err := st.New("乙")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	b.AddNode("", roleUser, "乙的问题", "", "")

	snapshot := make(map[string]string)
	for id, s := range st.Session {
		blob, _ := json.Marshal(s)
		snapshot[id] = string(blob)
	}

	if _, err := st.Delete(a.ID); err != nil {
		t.Fatalf("删会话失败: %v", err)
	}
	if _, ok := st.Session[a.ID]; ok {
		t.Fatal("甲没被删掉")
	}
	blob, _ := json.Marshal(st.Session[b.ID])
	if string(blob) != snapshot[b.ID] {
		t.Fatalf("删甲影响了乙:\n%s\n%s", string(blob), snapshot[b.ID])
	}
}

// 删空之后自动补一个空会话，界面永远有落点。
func TestDeleteLastSessionLeavesOne(t *testing.T) {
	st := newStore()
	s, err := st.New("唯一")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	if _, err := st.Delete(s.ID); err != nil {
		t.Fatalf("删会话失败: %v", err)
	}
	if len(st.Session) != 1 {
		t.Fatalf("删空后应当留下一个空会话，实际 %d 个", len(st.Session))
	}
	if _, ok := st.Session[st.Current]; !ok {
		t.Fatalf("当前会话 %q 指向不存在的会话", st.Current)
	}
}

// 存盘再读回来，树（含每个节点的 Active 下标）必须一模一样。
func TestStoreRoundTrip(t *testing.T) {
	st := newStore()
	s, err := st.New("往返")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	u1, _, u2, _ := buildTimeline(t, s)
	if _, err := s.EditNode(u2.ID, "改过的第二问"); err != nil {
		t.Fatalf("编辑失败: %v", err)
	}
	if err := s.SwitchVersion(u2.ID, -1); err != nil {
		t.Fatalf("翻页失败: %v", err)
	}
	_ = u1

	blob, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var back Store
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if back.Current != st.Current {
		t.Fatalf("当前会话丢了: %q != %q", back.Current, st.Current)
	}
	if !reflect.DeepEqual(back.Session, st.Session) {
		t.Fatal("往返之后树不一致（Active 下标最容易在这里丢）")
	}
}

// 超长文本截断并标记，不炸配置文件。
func TestTextTruncation(t *testing.T) {
	s := testSession(t)
	long := strings.Repeat("甲", maxTextRunes+50)
	n := s.AddNode("", roleUser, long, "", "")
	if !n.Trunc {
		t.Fatal("超长文本应当被标记为截断")
	}
	if got := len([]rune(n.Text)); got != maxTextRunes {
		t.Fatalf("截断后长度 = %d，想要 %d", got, maxTextRunes)
	}
}

// 喂模型的历史要裁剪，并尽量从 user 开始。
func TestModelMessagesTrimsToUserStart(t *testing.T) {
	s := testSession(t)
	u := s.AddNode("", roleUser, "问 0", "", "")
	cur := u.ID
	for i := 1; i <= maxTurnsForModel+4; i++ {
		a := s.AddNode(cur, roleAssistant, "答", agentService, "")
		n := s.AddNode(a.ID, roleUser, "问", "", "")
		cur = n.ID
	}

	msgs := modelMessages(s.Path())
	if len(msgs) > maxTurnsForModel {
		t.Fatalf("喂模型的消息数 = %d，超过上限 %d", len(msgs), maxTurnsForModel)
	}
	if len(msgs) > 1 && msgs[0].Role != roleUser {
		t.Fatalf("裁剪后应当从 user 开始，实际第一条是 %q", msgs[0].Role)
	}
}

func TestSanitizeID(t *testing.T) {
	if got := sanitizeID("a b/c*d"); got != "a-bcd" {
		t.Fatalf("sanitizeID = %q", got)
	}
	if got := sanitizeID(strings.Repeat("x", 100)); len(got) != 64 {
		t.Fatalf("超长 ID 应当截到 64，实际 %d", len(got))
	}
}
