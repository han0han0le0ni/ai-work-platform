package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 造一个装好事件的过程：两轮思考、两次工具调用（一次成功一次失败）。
func sampleTrace() *traceView {
	t := &traceView{started: time.Now().Add(-3 * time.Second), model: "deepseek-chat", round: 2}
	t.think = "用户想看 fs-tool 的权限声明，先读它的 manifest.json 再说"
	t.phase = "正在调用 read_file"
	t.steps = []traceStep{
		{tool: "list_dir", args: "path=plugins", ms: 120, ok: true, done: true},
		{tool: "read_file", args: "path=plugins/fs-tool/manifest.json", done: false},
	}
	return t
}

func TestApplyTraceFromEvents(t *testing.T) {
	a := &app{st: &state{busy: true}}
	w := &Workbench{}

	// 第 1 轮：模型开始想，想完说要调两个工具
	w.applyTrace(a, "agent.think", json.RawMessage(`{"round":0,"phase":"start"}`))
	w.applyTrace(a, "agent.think", json.RawMessage(
		`{"round":0,"phase":"done","model":"deepseek-chat","thinking":"先看看目录","text":"我来查一下","toolCalls":2}`))

	tr := a.st.trace
	if tr == nil {
		t.Fatal("收到 agent.think 就该有过程")
	}
	if tr.round != 1 {
		t.Errorf("轮次应从 1 开始，实际 %d", tr.round)
	}
	if tr.think != "先看看目录" {
		t.Errorf("思考内容没记下来：%q", tr.think)
	}
	if !strings.Contains(tr.phase, "2 个工具") {
		t.Errorf("阶段没说清要调几个工具：%q", tr.phase)
	}
	if tr.model != "deepseek-chat" {
		t.Errorf("模型名没记下来：%q", tr.model)
	}

	// 两次工具调用：一次成功、一次失败
	w.applyTrace(a, "chat.tool.before", json.RawMessage(`{"tool":"list_dir","args":{"path":"plugins"}}`))
	w.applyTrace(a, "chat.tool.after", json.RawMessage(`{"tool":"list_dir","ok":true,"ms":120,"chars":430}`))
	w.applyTrace(a, "chat.tool.before", json.RawMessage(`{"tool":"read_file","args":{"path":"a.txt"}}`))
	w.applyTrace(a, "chat.tool.after", json.RawMessage(`{"tool":"read_file","ok":false,"error":"文件不存在"}`))

	if len(tr.steps) != 2 {
		t.Fatalf("应有 2 个步骤，实际 %d", len(tr.steps))
	}
	if !tr.steps[0].ok || tr.steps[0].ms != 120 {
		t.Errorf("第一个工具应成功并记下耗时：%+v", tr.steps[0])
	}
	if tr.steps[1].ok || tr.steps[1].err != "文件不存在" {
		t.Errorf("第二个工具应记下失败原因：%+v", tr.steps[1])
	}

	// 收尾
	w.applyTrace(a, "agent.done", json.RawMessage(`{"stopped":"done","steps":2,"ms":3100}`))
	if !tr.done {
		t.Error("agent.done 之后过程应标记结束")
	}
	if !strings.Contains(tr.headline(), "2 次工具调用") {
		t.Errorf("结束那行应说出调用次数：%q", tr.headline())
	}
	if !strings.Contains(a.st.status, "AI：") {
		t.Errorf("状态栏也该说一句它在干什么：%q", a.st.status)
	}
}

// 同名工具连续调两次：结束事件只该收掉还没结束的那一次。
func TestApplyTraceSameToolTwice(t *testing.T) {
	a := &app{st: &state{busy: true}}
	w := &Workbench{}

	w.applyTrace(a, "chat.tool.before", json.RawMessage(`{"tool":"read_file"}`))
	w.applyTrace(a, "chat.tool.before", json.RawMessage(`{"tool":"read_file"}`))
	w.applyTrace(a, "chat.tool.after", json.RawMessage(`{"tool":"read_file","ok":true,"ms":10}`))

	steps := a.st.trace.steps
	if len(steps) != 2 || steps[0].done || !steps[1].done {
		t.Fatalf("只应收掉最后那一次：%+v", steps)
	}
	w.applyTrace(a, "chat.tool.after", json.RawMessage(`{"tool":"read_file","ok":false,"error":"boom"}`))
	if !a.st.trace.steps[0].done || a.st.trace.steps[0].ok {
		t.Errorf("第二次结束事件应收掉第一个：%+v", a.st.trace.steps)
	}
}

// 模型调用失败要说得出口，不能只留下一个转圈。
func TestApplyTraceModelError(t *testing.T) {
	a := &app{st: &state{busy: true}}
	w := &Workbench{}
	w.applyTrace(a, "agent.think", json.RawMessage(`{"round":1,"phase":"error","error":"接口拒绝了这个密钥"}`))
	if got := a.st.trace.headline(); !strings.Contains(got, "接口拒绝了这个密钥") {
		t.Errorf("失败原因应出现在过程里：%q", got)
	}
	if a.st.statusOK {
		t.Error("出错时状态栏不应显示成成功")
	}
}

// 排版不变量：过程框的每一行宽度都精确等于 bw（差一列就把消息区顶歪）。
func TestTraceRowsWidth(t *testing.T) {
	cases := []*state{
		{busy: true, spinner: 2, trace: sampleTrace()},
		{busy: true, trace: &traceView{started: time.Now()}},                   // 一个事件都没到
		{trace: &traceView{started: time.Now(), done: true, phase: "完成"}},      // 已结束，留着回看
		{busy: true, trace: &traceView{started: time.Now(), err: "模型调用失败：超时"}}, // 出错
		// 超长工具名 + 超长思考：不能把行撑宽
		{busy: true, trace: &traceView{started: time.Now(), round: 12,
			think: strings.Repeat("想", 500),
			steps: []traceStep{{tool: strings.Repeat("tool_name", 20), args: strings.Repeat("arg", 60), ms: 99999, ok: true, done: true}}}},
	}

	for _, bw := range []int{60, 40, 24} {
		for i, st := range cases {
			rows := traceRows(st, bw)
			if len(rows) == 0 {
				t.Fatalf("case %d 宽度 %d：不该画不出东西", i, bw)
			}
			for j, r := range rows {
				line := renderTraceRow(r, bw+2)
				if w := dispWidth(line); w != bw {
					t.Errorf("case %d 宽度 %d：第 %d 行宽 %d（%q）", i, bw, j, w, line)
				}
			}
		}
	}
}

// 内容必须真的看得见：工具名、耗时、思考那一段。
func TestTraceRowsContent(t *testing.T) {
	st := &state{busy: true, spinner: 1, trace: sampleTrace()}
	var sb strings.Builder
	for _, r := range traceRows(st, 60) {
		sb.WriteString(renderTraceRow(r, 62))
		sb.WriteString("\n")
	}
	frame := sb.String()

	for _, want := range []string{"AI 在做什么", "deepseek-chat", "list_dir", "read_file", "120ms", "思考：", "权限声明"} {
		if !strings.Contains(frame, want) {
			t.Errorf("过程框里看不到 %q：\n%s", want, frame)
		}
	}
}

// 没有过程时聊天页不该凭空多出一个空框。
func TestTraceRowsNil(t *testing.T) {
	if rows := traceRows(&state{}, 60); len(rows) != 0 {
		t.Errorf("没有过程时应返回 nil，实际 %d 行", len(rows))
	}
}

// 参数摘要：原始 JSON 是给模型看的，界面上只留"它在读哪个文件"。
func TestArgsSummary(t *testing.T) {
	cases := map[string]string{
		`{"path":"plugins/fs-tool/manifest.json"}`: "path=plugins/fs-tool/manifest.json",
		`{}`:                                   "",
		`null`:                                 "",
		``:                                     "",
		`{"path":"a.txt","encoding":"base64"}`: "encoding=base64 · path=a.txt",
		`{"a":1,"b":2,"c":3}`:                  "a=1 · b=2 …",
		`{"nested":{"x":1}}`:                   `nested={"x":1}`,
	}
	for in, want := range cases {
		if got := argsSummary(json.RawMessage(in), 40); got != want {
			t.Errorf("argsSummary(%s) = %q，期望 %q", in, got, want)
		}
	}
	// 超长值要截断，不能把整行占满
	long := argsSummary(json.RawMessage(`{"path":"`+strings.Repeat("x", 200)+`"}`), 30)
	if dispWidth(long) > 30 {
		t.Errorf("超长参数没截断：宽 %d", dispWidth(long))
	}
}

// 发新消息时换一个新过程；翻版本、切会话不该把上一次的记录冲掉。
func TestResetTrace(t *testing.T) {
	st := &state{trace: sampleTrace()}
	old := st.trace
	st.resetTrace()
	if st.trace == old {
		t.Fatal("resetTrace 没有换掉旧过程")
	}
	if len(st.trace.steps) != 0 || st.trace.done {
		t.Errorf("新过程应是空的：%+v", st.trace)
	}
}

// renderTraceRow 把一行渲染成纯文本（不带颜色）：宽度断言要在纯文本上做。
func renderTraceRow(r rowFn, w int) string {
	c := newCanvas(w, 1, false)
	l := newLine(false)
	r(c, l, 0, 0)
	return l.String()
}
