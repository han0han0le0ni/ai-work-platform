package main

import (
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------- 纯逻辑

// 附件类型按扩展名判断：图片要看图、文本要喂内容、其余只报名字
func TestAttachmentKind(t *testing.T) {
	cases := []struct{ name, want string }{
		{"cat.png", "image"},
		{"photo.JPG", "image"},
		{"anim.gif", "image"},
		{"shot.webp", "image"},
		{"readme.md", "text"},
		{"main.go", "text"},
		{"data.json", "text"},
		{"clip.mp4", "file"},
		{"app.exe", "file"},
		{"noext", "file"},
	}
	for _, c := range cases {
		if got := (attachment{Name: c.name}).kind(); got != c.want {
			t.Errorf("%s 应归为 %s，实际 %s", c.name, c.want, got)
		}
	}
}

// 拖拽：终端插入的是路径文字，带引号的要整段认出来
func TestSplitQuoted(t *testing.T) {
	got := splitQuoted(`"C:\my docs\a.png" D:\b.txt`)
	want := []string{`C:\my docs\a.png`, `D:\b.txt`}
	if len(got) != len(want) {
		t.Fatalf("应切出 %d 段，实际 %d 段：%v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 段应是 %q，实际 %q", i, want[i], got[i])
		}
	}
}

func TestDroppedPathsAcceptsRealPaths(t *testing.T) {
	cases := [][]string{
		{`C:\pics\cat.png`},
		{`\\server\share\x.png`},
		{`/home/me/x.txt`},
		// 含空格的路径只有带引号才认得出来 —— 这也是真实终端拖拽给出的形状
		{`"C:\my docs\a.png"`, `"D:\b.txt"`},
	}
	for _, c := range cases {
		line := ""
		for i, p := range c {
			if i > 0 {
				line += " "
			}
			line += p
		}
		got := droppedPaths(line)
		if len(got) != len(c) {
			t.Errorf("%q 应认出 %d 个路径，实际 %v", line, len(c), got)
		}
	}
}

// 不带引号的含空格路径无法与「路径 + 提问」区分，宁可不认 —— 认错会吞掉用户的话
func TestDroppedPathsRequiresQuotesForSpaces(t *testing.T) {
	if got := droppedPaths(`D:\a b\notes.txt`); got != nil {
		t.Fatalf("不带引号的含空格路径不该被认出，实际 %v", got)
	}
}

// 关键的反面：正常提问绝不能被当成附件吞掉 —— 那等于把用户的话吃掉
func TestDroppedPathsRejectsPlainQuestions(t *testing.T) {
	for _, s := range []string{
		"你好",
		`C:\ 是网盘吗`,
		`/usr/bin 里有什么`,
		`看看 D:\a.txt 这个文件`,
		"帮我写个函数",
		"",
		"multi\nline",
	} {
		if got := droppedPaths(s); got != nil {
			t.Errorf("%q 不该被当成拖进来的路径，实际 %v", s, got)
		}
	}
}

// ---------------------------------------------------------------- 界面：拖拽

// 回车时输入的是路径 → 转成附件，且这次不发送
func TestEnterOnDroppedPathAddsAttachment(t *testing.T) {
	fb := &fakeBackend{convOn: true}
	a := newTestApp(fb)
	a.st.convOK = true
	a.st.sessID = "s1"
	a.st.input = []rune(`C:\pics\cat.png`)
	a.st.cursor = len(a.st.input)

	a.enter()
	if n := fb.convSendCount(); n != 0 {
		t.Fatalf("认出路径时不该直接发送，实际发了 %d 次", n)
	}

	drainAttach(t, a)

	if len(a.st.attachments) != 1 {
		t.Fatalf("应加入 1 个附件，实际 %d 个", len(a.st.attachments))
	}
	if got := a.st.attachments[0].Path; got != `C:\pics\cat.png` {
		t.Fatalf("附件路径不对：%q", got)
	}
	if string(a.st.input) != "" {
		t.Fatalf("加为附件后输入框应清空，实际 %q", string(a.st.input))
	}
}

// 认错了也要留退路：读不出来就说清楚，内容还在输入框里，再回车当文本发
func TestDroppedPathFailureKeepsInput(t *testing.T) {
	fb := &fakeBackend{
		convOn:      true,
		fakeAttErrs: map[string]error{`C:\nope.png`: errors.New("找不到文件")},
	}
	a := newTestApp(fb)
	a.st.convOK = true
	a.st.sessID = "s1"
	a.st.input = []rune(`C:\nope.png`)
	a.st.cursor = len(a.st.input)

	a.enter()
	drainAttach(t, a)

	if len(a.st.attachments) != 0 {
		t.Fatal("读失败时不该留下附件")
	}
	if string(a.st.input) != `C:\nope.png` {
		t.Fatalf("读失败时内容要保留在输入框，实际 %q", string(a.st.input))
	}
	if a.st.noticeOK {
		t.Fatal("读失败应标记为不成功")
	}
}

// ---------------------------------------------------------------- 界面：面板

// 输入行旁边要真的有「附件」按钮，点了能打开面板
func TestAttachButtonOpensPanel(t *testing.T) {
	fb := &fakeBackend{fakeBrowse: wsListing{Path: "/snap", Parent: "/"}}
	a := newTestApp(fb)
	layout(a, 100, 30)

	x, y := findBox(t, a, "btn:attach")
	a.click(x, y)

	if !a.st.attachOpen {
		t.Fatal("点了附件按钮应打开面板")
	}
	drainAttach(t, a)

	if a.st.attachPath != "/snap" {
		t.Fatalf("面板应列出目录，实际 %q", a.st.attachPath)
	}
}

// 点目录是"进去"，点文件才是"加入附件"
func TestAttachPanelBrowseAndAdd(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.st.attachOpen = true
	a.st.attachPath = "/snap"
	a.st.attachEntries = []dirEntry{
		{Name: "docs", Dir: true},
		{Name: "cat.png"},
	}

	// 点目录：进去
	a.browseTo2("docs", true)
	drainAttach(t, a)
	if a.st.attachPath != "/snap/docs" {
		t.Fatalf("点目录应进去，实际 %q", a.st.attachPath)
	}
	if len(a.st.attachments) != 0 {
		t.Fatal("点目录不该加附件")
	}

	// 点文件：加入
	a.st.attachEntries = []dirEntry{{Name: "cat.png"}}
	a.browseTo2("cat.png", false)
	drainAttach(t, a)
	if len(a.st.attachments) != 1 {
		t.Fatalf("点文件应加入附件，实际 %d 个", len(a.st.attachments))
	}
}

// 同一个文件点两次只算一个 —— 否则会白白多发一份 base64 出去
func TestAttachPanelDeduplicates(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.st.attachOpen = true
	a.st.attachEntries = []dirEntry{{Name: "cat.png"}}

	a.browseTo2("cat.png", false)
	drainAttach(t, a)
	a.browseTo2("cat.png", false)
	drainAttach(t, a)

	if len(a.st.attachments) != 1 {
		t.Fatalf("重复添加应被忽略，实际 %d 个", len(a.st.attachments))
	}
}

// 「移除」要把那一个删掉，而不是清空全部
func TestRemoveOneAttachment(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.st.attachments = []attachment{
		{Name: "a.txt", Path: "/a.txt"},
		{Name: "b.txt", Path: "/b.txt"},
	}

	a.removeAttachment(0)

	if len(a.st.attachments) != 1 || a.st.attachments[0].Name != "b.txt" {
		t.Fatalf("应只移除第一个，实际 %v", a.st.attachments)
	}
}

// 越界的下标不能崩
func TestRemoveAttachmentOutOfRange(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.attachments = []attachment{{Name: "a.txt"}}
	a.removeAttachment(5)
	a.removeAttachment(-1)
	if len(a.st.attachments) != 1 {
		t.Fatal("越界下标不该改变列表")
	}
}

// Esc 关面板，但已经挑好的附件要留着 —— 那是用户的工作成果
func TestEscClosesPanelButKeepsAttachments(t *testing.T) {
	a := newTestApp(&fakeBackend{})
	a.st.attachOpen = true
	a.st.attachments = []attachment{{Name: "a.txt", Path: "/a.txt"}}

	a.handle(event{key: keyEsc})

	if a.st.attachOpen {
		t.Fatal("Esc 应关闭面板")
	}
	if len(a.st.attachments) != 1 {
		t.Fatal("关面板不该丢掉已选附件")
	}
}

// ---------------------------------------------------------------- 界面：发送

// 发送时附件要一路带上，发完清空（否则下一条会莫名又带上同一张图）
func TestSendCarriesAttachments(t *testing.T) {
	fb := &fakeBackend{convOn: true}
	a := newTestApp(fb)
	a.st.convOK = true
	a.st.sessID = "s1"
	a.st.attachments = []attachment{
		{Type: "text", Name: "a.txt", Path: "/a.txt", Text: "hi"},
		{Type: "image", Name: "c.png", Path: "/c.png", DataURL: "data:image/png;base64,AA"},
	}
	a.st.input = []rune("看看这两个")
	a.st.cursor = len(a.st.input)

	a.enter()
	waitFor(t, "附件随消息发出", func() bool { return fb.attachSendCount() == 1 })

	if got := fb.lastAttachCount(); got != 2 {
		t.Fatalf("应带 2 个附件，实际 %d 个", got)
	}
	if n := len(a.st.attachments); n != 0 {
		t.Fatalf("发送后应清空附件，实际还剩 %d 个", n)
	}
	if got := fb.lastSentText(); got != "看看这两个" {
		t.Fatalf("正文不对：%q", got)
	}
}

// 只发附件不写字也要能发出去（"看这张图"）
func TestSendAttachmentOnly(t *testing.T) {
	fb := &fakeBackend{convOn: true}
	a := newTestApp(fb)
	a.st.convOK = true
	a.st.sessID = "s1"
	a.st.attachments = []attachment{{Type: "image", Name: "c.png", Path: "/c.png", DataURL: "data:image/png;base64,AA"}}

	a.enter()
	waitFor(t, "只带附件的消息也能发出", func() bool { return fb.attachSendCount() == 1 })

	if got := fb.lastSentText(); got == "" {
		t.Fatal("只发附件时也该给一句占位正文，否则界面出现空消息")
	}
}

// ---------------------------------------------------------------- 辅助

// drainAttach 收一条附件相关的异步结果并应用（测试里就是"等后台读完"）。
func drainAttach(t *testing.T, a *app) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case out := <-a.results:
			a.applyOutcome(out)
			if out.kind == "attach" || out.kind == "attachone" || out.kind == "attachdrop" {
				return
			}
		case <-deadline:
			t.Fatal("等附件结果超时")
		}
	}
}

func (f *fakeBackend) convSendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.convSends)
}

func (f *fakeBackend) attachSendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.attSends)
}

func (f *fakeBackend) lastAttachCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.attSends) == 0 {
		return -1
	}
	return f.attSends[len(f.attSends)-1]
}

func (f *fakeBackend) lastSentText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.convSends) == 0 {
		return ""
	}
	return f.convSends[len(f.convSends)-1]
}
