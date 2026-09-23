package main

import (
	"fmt"
	"strings"
	"time"
)

// Character 是一张角色卡。
//
// 字段与"酒馆"那类角色卡的常用项对齐：设定 / 性格 / 场景 / 开场白 / 记忆。
// 全部可编辑，没有一个是必填 —— 空字段不参与拼装，不会在提示词里留下空标题。
type Character struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"` // 人物设定：它是谁、什么来历
	Personality string `json:"personality,omitempty"` // 性格与说话方式
	Scenario    string `json:"scenario,omitempty"`    // 当前场景：在哪儿、正在发生什么
	Greeting    string `json:"greeting,omitempty"`    // 开场白：刚启用时界面用它起个头
	Memory      string `json:"memory,omitempty"`      // 长期记忆：一条一行
	Updated     string `json:"updated,omitempty"`
}

// persona 是"我"这一侧的身份 —— 角色扮演里它和角色卡一样重要：
// 同一个角色，对方是我的同事还是我的徒弟，说话方式完全不同。
type persona struct {
	Name string `json:"name,omitempty"`
	Desc string `json:"desc,omitempty"`
}

// store 是插件的全部状态，整体存在本插件的配置命名空间里。
type store struct {
	Characters []Character `json:"characters"`
	Persona    persona     `json:"persona"`
	ActiveID   string      `json:"activeId,omitempty"`
	Seq        int         `json:"seq"`

	// SavedSystem 是"启用角色之前，agent 自己的系统提示词"。
	//
	// 存它只为一件事：停用角色时把 agent 原样还回去。不存的话，用户原本
	// 在 agent 上写的系统提示词会被角色卡永久顶掉，而且看不出来去哪了。
	SavedSystem string `json:"savedSystem,omitempty"`
}

func (s *store) find(id string) *Character {
	for i := range s.Characters {
		if s.Characters[i].ID == id {
			return &s.Characters[i]
		}
	}
	return nil
}

func (s *store) active() *Character {
	if s.ActiveID == "" {
		return nil
	}
	return s.find(s.ActiveID)
}

// nextID 发一个短 id。角色卡是给人看的，用 c1 / c2 比一串 uuid 好认。
func (s *store) nextID() string {
	s.Seq++
	return fmt.Sprintf("c%d", s.Seq)
}

// saveParams 是一次 save 的入参。
//
// 字段一律用指针：**没传 = 保持原样，传空串 = 清空**。界面每次会带上全部字段，
// 但命令行/其它插件可以只改一项，所以这个区分必须留着。
type saveParams struct {
	ID          string  `json:"id,omitempty"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Personality *string `json:"personality,omitempty"`
	Scenario    *string `json:"scenario,omitempty"`
	Greeting    *string `json:"greeting,omitempty"`
	Memory      *string `json:"memory,omitempty"`
}

// applySave 把一次 save 落到状态上：有 id 就更新，没有就新建。
//
// 抽成纯函数是为了能直接测 —— 新建的默认值、指针"没传/传空"的区别、
// 记忆的减号清空约定，都是容易写错、而且用户一定会撞上的地方。
func (s *store) applySave(in saveParams) (*Character, bool) {
	c := s.find(strings.TrimSpace(in.ID))
	created := false
	if c == nil {
		s.Characters = append(s.Characters, Character{ID: s.nextID()})
		c = &s.Characters[len(s.Characters)-1]
		created = true
	}

	apply := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	apply(&c.Name, in.Name)
	apply(&c.Description, in.Description)
	apply(&c.Personality, in.Personality)
	apply(&c.Scenario, in.Scenario)
	apply(&c.Greeting, in.Greeting)
	if in.Memory != nil {
		// 与设置页同一个约定：一个减号 = 清空。
		// 记忆可能很长，靠退格清干净要按到手酸。
		v := strings.TrimSpace(*in.Memory)
		if v == "-" {
			v = ""
		}
		c.Memory = v
	}
	if c.Name == "" {
		c.Name = "未命名角色" // 名字是列表里唯一的标识，不能空着
	}
	c.Updated = nowStamp()
	return c, created
}

// remove 按 id 删掉一张角色卡，返回被删掉的那张（没找到返回 nil）。
func (s *store) remove(id string) *Character {
	for i := range s.Characters {
		if s.Characters[i].ID == id {
			gone := s.Characters[i]
			s.Characters = append(s.Characters[:i], s.Characters[i+1:]...)
			return &gone
		}
	}
	return nil
}

// composePrompt 把角色卡 + 我的身份 + 记忆拼成一段系统提示词。
//
// 这是整个插件的核心：现有的对话链路（界面 → conversation.v1 → agent.v1 → llm.v1）
// 不认识"角色"这个概念，它只认系统提示词。所以角色扮演在这里落地的办法就是
// 把角色卡**翻译**成一段系统提示词，然后交给 agent —— 链路一行都不用改。
//
// 空字段直接跳过，不写"【性格】（未填写）"这种噪音。
func composePrompt(c *Character, p persona) string {
	var b strings.Builder

	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = "这个角色"
	}
	fmt.Fprintf(&b, "接下来你要扮演「%s」和我对话。这是角色扮演，不是助手答疑。\n\n", name)

	field := func(label, v string) {
		if v = strings.TrimSpace(v); v != "" {
			fmt.Fprintf(&b, "【%s】\n%s\n\n", label, v)
		}
	}
	field("人物设定", c.Description)
	field("性格", c.Personality)
	field("当前场景", c.Scenario)

	// 我的身份：名字和设定各自可有可无，所以不写成固定模板
	me := strings.TrimSpace(p.Name)
	desc := strings.TrimSpace(p.Desc)
	switch {
	case me != "" && desc != "":
		fmt.Fprintf(&b, "【我的身份】\n我叫%s。%s\n\n", me, desc)
	case me != "":
		fmt.Fprintf(&b, "【我的身份】\n我叫%s。\n\n", me)
	case desc != "":
		fmt.Fprintf(&b, "【我的身份】\n%s\n\n", desc)
	}

	field("你记得的事", c.Memory)

	b.WriteString("【怎么说话】\n" +
		"1. 始终以「" + name + "」的身份用第一人称说话，保持上面的人设与语气。\n" +
		"2. 不要替我做决定、不要替我说话、不要描写我的心理活动 —— 只写你自己。\n" +
		"3. 口语化、有来有回，默认两三句即可；除非场景需要，不要长篇大论。\n" +
		"4. 我告诉你值得长期记住的事时，调用 remember 工具记下来；\n" +
		"   只在嘴上说\"我记住了\"没有用，下一次对话你就忘了。\n" +
		"5. 除非我明确要求，否则不要跳出角色，也不要说自己是 AI。\n")

	return strings.TrimRight(b.String(), "\n")
}

// appendMemory 往记忆里追加一条，存成 "- " 开头的列表。
//
// 用列表而不是自由文本，是为了让"记住了几件事"一眼可见，
// 模型读起来也清楚哪一条是哪一条。
func appendMemory(cur, text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimSpace(strings.TrimLeft(text, "-"))
	if text == "" {
		return cur
	}
	line := "- " + text
	if strings.TrimSpace(cur) == "" {
		return line
	}
	return strings.TrimRight(cur, "\n") + "\n" + line
}

// memoryCount 数一下记住了几条，界面上显示成"记忆 3 条"。
func memoryCount(memory string) int {
	n := 0
	for _, line := range strings.Split(memory, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func nowStamp() string { return time.Now().Format("2006-01-02 15:04") }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
