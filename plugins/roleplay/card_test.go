package main

import (
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

// 提示词要把五样东西都带上：设定 / 性格 / 场景 / 我的身份 / 记忆。
func TestComposePromptCarriesEveryField(t *testing.T) {
	c := &Character{
		Name:        "林晚",
		Description: "城郊书店的店主，三十五岁，话不多。",
		Personality: "冷淡但心细，经常用反问句。",
		Scenario:    "雨夜，书店快打烊了。",
		Memory:      "- 你爱喝不加糖的美式",
	}
	p := persona{Name: "阿远", Desc: "常来买旧书的大学生"}

	got := composePrompt(c, p)
	for _, want := range []string{
		"林晚", "城郊书店的店主", "冷淡但心细", "雨夜", "阿远", "常来买旧书的大学生",
		"不加糖的美式", "不要替我做决定", "remember",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("系统提示词里缺少 %q：\n%s", want, got)
		}
	}
}

// 空字段不参与拼装：不能在提示词里留下"【性格】（未填写）"这种噪音 ——
// 那既占预算，又会诱导模型自己去编一个性格。
func TestComposePromptSkipsEmptyFields(t *testing.T) {
	got := composePrompt(&Character{Name: "小明"}, persona{})

	for _, unwanted := range []string{"【性格】", "【当前场景】", "【我的身份】", "【你记得的事】", "【人物设定】"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("空字段不该出现 %q：\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "小明") {
		t.Fatalf("至少要有名字：\n%s", got)
	}
}

// 没填名字也要能拼：用的是"这个角色"，而不是拼出「你要扮演「」」。
func TestComposePromptWithoutName(t *testing.T) {
	got := composePrompt(&Character{Personality: "倔"}, persona{})
	if !strings.Contains(got, "这个角色") {
		t.Fatalf("没填名字时应退回「这个角色」：\n%s", got)
	}
	if strings.Contains(got, "「」") {
		t.Fatalf("不该出现空的名字括号：\n%s", got)
	}
}

// 我的身份三种写法都要说人话：都有 / 只有名字 / 只有设定。
func TestComposePromptPersonaVariants(t *testing.T) {
	c := &Character{Name: "甲"}

	if got := composePrompt(c, persona{Name: "阿远"}); !strings.Contains(got, "我叫阿远") {
		t.Fatalf("只有名字时应写成「我叫阿远」：\n%s", got)
	}
	if got := composePrompt(c, persona{Desc: "一个送信的"}); !strings.Contains(got, "一个送信的") {
		t.Fatalf("只有设定时也要带上：\n%s", got)
	}
	if got := composePrompt(c, persona{}); strings.Contains(got, "【我的身份】") {
		t.Fatalf("身份完全没填时不该出现这一节：\n%s", got)
	}
}

// 记忆是一行一条的列表，累加不改写已有内容。
func TestAppendMemory(t *testing.T) {
	if got := appendMemory("", "爱喝美式"); got != "- 爱喝美式" {
		t.Fatalf("第一条要补上列表前缀，实际 %q", got)
	}
	if got := appendMemory("- 爱喝美式", "住在城南"); got != "- 爱喝美式\n- 住在城南" {
		t.Fatalf("第二条要接在下一行，实际 %q", got)
	}
	// 模型可能把整句"记住 - xxx"塞进来，别把减号记成内容的一部分
	if got := appendMemory("", "- 爱喝美式"); got != "- 爱喝美式" {
		t.Fatalf("开头多一个减号不该记成两条，实际 %q", got)
	}
	// 空内容不该往记忆里塞一条空的
	if got := appendMemory("- 已有", "   "); got != "- 已有" {
		t.Fatalf("空白内容应原样返回，实际 %q", got)
	}
	// 用户手写的自由文本后面追加，不能和旧内容粘成一行
	if got := appendMemory("喜欢下雨", "养了只猫"); got != "喜欢下雨\n- 养了只猫" {
		t.Fatalf("追加应换行，实际 %q", got)
	}
}

func TestMemoryCount(t *testing.T) {
	cases := map[string]int{
		"":                   0,
		"\n\n":               0,
		"- a":                1,
		"- a\n- b":           2,
		"- a\n\n- b\n":       2,
		"自由文本":               1,
		"- a\n- b\n- c\n- d": 4,
	}
	for in, want := range cases {
		if got := memoryCount(in); got != want {
			t.Fatalf("memoryCount(%q) = %d，期望 %d", in, got, want)
		}
	}
}

// 新建：自动发 id、名字兜底，其它字段留空。
func TestApplySaveCreates(t *testing.T) {
	st := &store{}
	c, created := st.applySave(saveParams{Name: sp("林晚")})

	if !created || c == nil {
		t.Fatal("没有 id 时应新建")
	}
	if c.ID != "c1" {
		t.Fatalf("第一张卡应是 c1，实际 %q", c.ID)
	}
	if c.Name != "林晚" || c.Updated == "" {
		t.Fatalf("名字与时间戳应写进去，实际 %+v", c)
	}
	if len(st.Characters) != 1 {
		t.Fatalf("状态里应有一张卡，实际 %d", len(st.Characters))
	}

	// 第二张接在后面
	c2, _ := st.applySave(saveParams{})
	if c2.ID != "c2" {
		t.Fatalf("第二张卡应是 c2，实际 %q", c2.ID)
	}
	if st.Seq != 2 {
		t.Fatalf("id 计数器应推进，实际 %d", st.Seq)
	}
}

// 名字空着会变成一个"无名卡"：列表里认不出来，所以必须兜底。
func TestApplySaveNameFallback(t *testing.T) {
	st := &store{}
	c, _ := st.applySave(saveParams{})
	if c.Name != "未命名角色" {
		t.Fatalf("名字应兜底，实际 %q", c.Name)
	}
}

// 指针的三态：没传=不动，传空=清空，传值=改。
func TestApplySaveFieldSemantics(t *testing.T) {
	st := &store{}
	c, _ := st.applySave(saveParams{
		Name: sp("甲"), Description: sp("原本的设定"), Personality: sp("原本的性格"),
	})

	// 只改名字：其它字段必须原样
	if _, created := st.applySave(saveParams{ID: c.ID, Name: sp("乙")}); created {
		t.Fatal("带 id 时不该再新建")
	}
	if c.Name != "乙" {
		t.Fatalf("名字应更新，实际 %q", c.Name)
	}
	if c.Description != "原本的设定" || c.Personality != "原本的性格" {
		t.Fatalf("没传的字段不该被动，实际 %+v", c)
	}

	// 传空串 = 清空
	st.applySave(saveParams{ID: c.ID, Description: sp("")})
	if c.Description != "" {
		t.Fatalf("传空串应清空，实际 %q", c.Description)
	}
	if c.Personality != "原本的性格" {
		t.Fatal("清空一个字段不该影响别的字段")
	}
}

// 记忆的减号清空约定：与设置页保持一致，省得长记忆按退格按到手酸。
func TestApplySaveMemoryClear(t *testing.T) {
	st := &store{}
	c, _ := st.applySave(saveParams{Name: sp("甲"), Memory: sp("- 一条\n- 两条")})
	if memoryCount(c.Memory) != 2 {
		t.Fatalf("前置条件：应有 2 条记忆，实际 %q", c.Memory)
	}

	st.applySave(saveParams{ID: c.ID, Memory: sp("-")})
	if c.Memory != "" {
		t.Fatalf("减号应清空记忆，实际 %q", c.Memory)
	}

	// 普通内容照常写入（不是减号就不特殊处理）
	st.applySave(saveParams{ID: c.ID, Memory: sp("一条新的")})
	if c.Memory != "一条新的" {
		t.Fatalf("记忆应被写入，实际 %q", c.Memory)
	}
}

func TestRemove(t *testing.T) {
	st := &store{}
	a, _ := st.applySave(saveParams{Name: sp("甲")})
	b, _ := st.applySave(saveParams{Name: sp("乙")})

	gone := st.remove(a.ID)
	if gone == nil || gone.Name != "甲" {
		t.Fatalf("应删掉甲，实际 %+v", gone)
	}
	if len(st.Characters) != 1 || st.Characters[0].ID != b.ID {
		t.Fatalf("只该剩下乙，实际 %+v", st.Characters)
	}
	if st.remove("c99") != nil {
		t.Fatal("删不存在的 id 应返回 nil")
	}
	// 删掉之后 id 不复用：旧引用（比如界面上的选中项）不会突然指向新卡
	if c, _ := st.applySave(saveParams{}); c.ID == a.ID {
		t.Fatalf("id 不该复用：%q", c.ID)
	}
}

func TestStoreActive(t *testing.T) {
	st := &store{}
	if st.active() != nil {
		t.Fatal("没有启用中的角色时应返回 nil")
	}
	c, _ := st.applySave(saveParams{Name: sp("甲")})
	st.ActiveID = c.ID
	if got := st.active(); got == nil || got.Name != "甲" {
		t.Fatalf("active 应返回启用的那张，实际 %+v", got)
	}
	// 指向一张已经被删掉的卡：返回 nil，而不是崩
	st.ActiveID = "c99"
	if st.active() != nil {
		t.Fatal("ActiveID 指向不存在的卡时应返回 nil")
	}
}
