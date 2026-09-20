package main

import (
	"strings"
	"testing"
)

// 正常情况：模型老实输出 SVG
func TestExtractSVGPlain(t *testing.T) {
	got, err := extractSVG(`<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`)
	if err != nil {
		t.Fatalf("应能提取: %v", err)
	}
	if !strings.HasPrefix(got, "<svg") || !strings.HasSuffix(got, "</svg>") {
		t.Fatalf("提取结果不完整: %q", got)
	}
}

// 模型很爱包一层代码块围栏 —— 必须剥掉，否则文件里混进 ``` 就不是合法 SVG
func TestExtractSVGStripsFence(t *testing.T) {
	in := "```xml\n<svg viewBox=\"0 0 10 10\"><circle r=\"4\"/></svg>\n```"
	got, err := extractSVG(in)
	if err != nil {
		t.Fatalf("带围栏时应能提取: %v", err)
	}
	if strings.Contains(got, "```") {
		t.Fatalf("围栏没剥干净: %q", got)
	}
	if !strings.HasPrefix(got, "<svg") {
		t.Fatalf("应从 <svg 开始: %q", got)
	}
}

// 模型在前面写"好的，这是你要的图："—— 那段话绝不能进文件
func TestExtractSVGDropsProse(t *testing.T) {
	in := "好的，这是你要的一面红旗：\n\n<svg viewBox=\"0 0 10 10\"><rect/></svg>\n希望你喜欢！"
	got, err := extractSVG(in)
	if err != nil {
		t.Fatalf("应能提取: %v", err)
	}
	if strings.Contains(got, "好的") || strings.Contains(got, "希望你喜欢") {
		t.Fatalf("解释文字没剥掉: %q", got)
	}
}

// 没有 SVG 时要如实报错，不能存一个空文件出去
func TestExtractSVGRejectsNoContent(t *testing.T) {
	if _, err := extractSVG("我画不出来。"); err == nil {
		t.Fatal("没有 SVG 时应报错")
	}
	if _, err := extractSVG(""); err == nil {
		t.Fatal("空内容应报错")
	}
	// 只有开标签、没有闭标签：半截内容同样不能要
	if _, err := extractSVG(`<svg viewBox="0 0 10 10"><rect`); err == nil {
		t.Fatal("不完整的 SVG 应报错")
	}
}

// 取最后一个 </svg>：模型偶尔会画两遍，我们要完整的那一段
func TestExtractSVGTakesFullSpan(t *testing.T) {
	in := `<svg><rect/></svg> 顺手再画一个 <svg><circle/></svg>`
	got, err := extractSVG(in)
	if err != nil {
		t.Fatalf("应能提取: %v", err)
	}
	if !strings.Contains(got, "circle") {
		t.Fatalf("应覆盖到闭合位置: %q", got)
	}
}

// 文件名：能认出来是什么、带时间戳不覆盖
func TestSVGName(t *testing.T) {
	got := svgName("", "a red flag")
	if !strings.HasPrefix(got, "a-red-flag-") || !strings.HasSuffix(got, ".svg") {
		t.Fatalf("名字不对: %q", got)
	}
	// 纯中文压不出 ASCII，要有兜底名
	if got := svgName("", "一面红旗"); !strings.HasPrefix(got, "drawing-") {
		t.Fatalf("应回落到默认名: %q", got)
	}
	// 指定了名字就用它
	if got := svgName("logo", "随便"); !strings.HasPrefix(got, "logo-") {
		t.Fatalf("应用给定名字: %q", got)
	}
}

// 摘要只留安全字符且不超长
func TestSlugSanitizes(t *testing.T) {
	got := slug(`C:\a/b & "c"`)
	if strings.ContainsAny(got, `\/:&"`) {
		t.Fatalf("文件名里不该出现这些字符: %q", got)
	}
	if len(got) > 24 {
		t.Fatalf("摘要不该超长: %q", got)
	}
}
