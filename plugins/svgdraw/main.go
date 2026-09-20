// Command svgdraw 是官方"纯文本画图"插件（provides: svgdraw.v1）。
//
// 它解决的是一个很实际的问题：DeepSeek 这类模型**只输出文本**，接不了
// /images/generations 那种文生图接口。但"文本"不等于"不能画图" ——
// SVG 本身就是**用文本描述图形**的格式：矩形、圆、路径、颜色全是文字。
//
// 所以这里的做法是：
//
//	prompt → llm.v1（让模型写 SVG）→ 提取 <svg>…</svg> → 存成 .svg 文件
//
// 得到的是一张**真图**，浏览器/看图程序都能打开，而且不需要任何外部图像服务、
// 不需要额外的 key —— 你已经在用的那个文本模型就够了。
//
// 与 imagegen 的分工（两个插件刻意分开）：
//
//	imagegen  光栅图（照片级写实），要另接图像服务
//	svgdraw   矢量图（图标 / 图示 / 招牌 / 简单插画 / 图表），纯文本就能画
//
// 矢量图放大不糊，适合画示意图和图标；照片那种它就无能为力 —— 那是 imagegen 的活。
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aiwork/sdk/go/plugin"
)

const (
	serviceName = "svgdraw.v1"
	llmService  = "llm.v1"
	wsService   = "workspace.v1"
	outDirName  = "generated-images"
)

// systemPrompt 是画图的关键：把模型当成一台"矢量绘图仪"，
// 明确禁止解释、禁止 markdown 包裹 —— 否则它会先写一段"好的，我来画…"，
// 那段文字混进文件里，SVG 就废了。
const systemPrompt = `你是一台矢量绘图引擎。用户会描述他们想要的图形，你直接输出可用的 SVG 代码。

硬性要求：
1. 只输出 SVG 代码本身，从 <svg 开始到 </svg> 结束
2. 不要任何解释、不要 markdown 代码块围栏、不要在前后加任何文字
3. 根元素必须带 xmlns="http://www.w3.org/2000/svg" 和 viewBox
4. 图形要画完整：背景、主体、必要的细节都用 <rect> / <circle> / <path> / <text> 等画出来
5. 字体用通用族（如 sans-serif），不要依赖系统里可能没有的字体
6. 中文文字可以直接写进 <text> 里

用户描述得含糊时，补上合理的细节让画面完整，但不要改变主题。`

type Drawer struct{ ctx *plugin.Context }

func (d *Drawer) Activate(ctx *plugin.Context) error {
	d.ctx = ctx
	if err := ctx.Provide(serviceName, map[string]any{
		"description": "用纯文本画矢量图：prompt 进，.svg 路径出（无需外部图像服务）",
		"methods":     []string{"draw", "registerTool"},
	}); err != nil {
		return err
	}
	ctx.Log("info", "文本画图插件已就绪", map[string]any{"backend": llmService})
	return nil
}

func (d *Drawer) Deactivate() error { return nil }

func (d *Drawer) Handle(service, method string, args json.RawMessage) (any, error) {
	switch method {
	case "draw":
		return d.draw(args)
	case "registerTool":
		return d.registerTool()
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%s 不支持方法 %s", serviceName, method)
	}
}

// ---------------------------------------------------------------------------
// 画图
// ---------------------------------------------------------------------------

type drawParams struct {
	Prompt string `json:"prompt"`
	Name   string `json:"name,omitempty"`
	Dir    string `json:"dir,omitempty"`
}

func (d *Drawer) draw(args json.RawMessage) (any, error) {
	var in drawParams
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 prompt")
	}
	if _, err := d.ctx.Consume(llmService); err != nil {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound,
			"需要 %s 才能画图（它负责把描述写成 SVG）", llmService)
	}

	raw, err := d.ctx.Invoke(llmService, "chat", map[string]any{
		"system":   systemPrompt,
		"messages": []map[string]any{{"role": "user", "content": prompt}},
		// 思考模型会先花一大截 token 在 reasoning 上，预算小了正文就是空的
		"maxTokens": 8000,
	})
	if err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "让模型画图失败: %v", err)
	}
	var out struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "解析模型响应失败: %v", err)
	}

	svg, err := extractSVG(out.Content)
	if err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "%v；模型原文片段：%s",
			err, clip(strings.TrimSpace(out.Content), 200))
	}

	dir := strings.TrimSpace(in.Dir)
	if dir == "" {
		dir = d.outputDir()
	}
	path := joinPath(dir, svgName(in.Name, prompt))
	// 参数是 (path, content, appendMode, mkdir)：这里"不追加、要建目录"。
	// 传反了就会写成 append+不建目录，第一次保存必然找不到路径。
	if _, err := d.ctx.FSWrite(path, svg, false, true); err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "保存 SVG 失败: %v", err)
	}

	d.ctx.Log("info", "已画出矢量图", map[string]any{
		"path": path, "bytes": len(svg),
	})
	return map[string]any{
		"ok":      true,
		"path":    path,
		"format":  "svg",
		"bytes":   len(svg),
		"prompt":  prompt,
		"message": "已画出矢量图：" + path,
	}, nil
}

// extractSVG 从模型输出里抠出 SVG。
//
// 模型很爱自作主张地包一层 ```xml 围栏，或者在前面写"好的，这是你要的图："。
// 与其在 prompt 里反复叮嘱（它照样会犯），不如这里老老实实把两端裁掉。
func extractSVG(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("模型返回了空内容（可能是思考占满了 token 预算）")
	}
	start := strings.Index(s, "<svg")
	end := strings.LastIndex(s, "</svg>")
	if start < 0 || end < 0 || end < start {
		return "", fmt.Errorf("模型输出里没有找到完整的 <svg>…</svg>")
	}
	svg := s[start : end+len("</svg>")]
	// 自闭合的写法（<svg …/>）也认
	if !strings.HasPrefix(svg, "<svg") {
		return "", fmt.Errorf("SVG 片段不完整")
	}
	return svg, nil
}

// outputDir 优先存进工作区，这样用户在自己的目录里就能看到。
func (d *Drawer) outputDir() string {
	if root := d.workspaceRoot(); root != "" {
		return joinPath(root, outDirName)
	}
	return outDirName
}

func (d *Drawer) workspaceRoot() string {
	if _, err := d.ctx.Consume(wsService); err != nil {
		return ""
	}
	raw, err := d.ctx.Invoke(wsService, "browse", map[string]any{})
	if err != nil {
		return ""
	}
	var out struct {
		Root string `json:"root"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	return strings.TrimSpace(out.Root)
}

// svgName 起个能认出来的名字：prompt 摘要 + 时间戳，避免互相覆盖。
func svgName(given, prompt string) string {
	base := strings.TrimSpace(given)
	if base == "" {
		base = slug(prompt)
	}
	if base == "" {
		base = "drawing"
	}
	return fmt.Sprintf("%s-%s.svg", base, time.Now().Format("20060102-150405"))
}

// slug 把 prompt 压成一小段可做文件名的 ASCII；全是中文就返回空（用默认名）。
func slug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
		if b.Len() >= 24 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func joinPath(dir, name string) string {
	if strings.HasSuffix(dir, "/") || strings.HasSuffix(dir, `\`) {
		return dir + name
	}
	return dir + "/" + name
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// registerTool 把画图能力登记进工具目录，AI 就能自己决定"我画一个给你看"。
func (d *Drawer) registerTool() (any, error) {
	if _, err := d.ctx.Consume("toolExecutor.v1"); err != nil {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound,
			"需要 toolExecutor.v1 才能注册工具")
	}
	raw, err := d.ctx.Invoke("toolExecutor.v1", "register", map[string]any{
		"name": "draw_svg",
		"description": "用矢量方式画图（图标 / 图示 / 招牌 / 简单插画 / 图表），" +
			"返回保存好的 .svg 文件路径。画不了照片级的写实图。",
		"service": serviceName,
		"method":  "draw",
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{"type": "string",
					"description": "要画什么，越具体越好（主体、构图、配色、文字）"},
				"name": map[string]any{"type": "string", "description": "文件名前缀，可选"},
			},
			"required": []string{"prompt"},
		},
	})
	if err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "注册工具失败: %v", err)
	}
	return map[string]any{"ok": true, "tool": "draw_svg", "raw": json.RawMessage(raw)}, nil
}

func main() { plugin.Serve(&Drawer{}) }
