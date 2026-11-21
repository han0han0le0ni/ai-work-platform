package main

import (
	"image"
	"image/color"
	"strings"
	"testing"
)

// 造一张纯色小图
func solidImage(w, h int, c color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// 行数、每行显示宽度都要精确 —— 差一列整个边框就歪了
func TestThumbnailShape(t *testing.T) {
	img := solidImage(100, 100, color.RGBA{200, 30, 30, 255})
	lines := thumbnailLines(img, 20, 5, true)

	if len(lines) != 5 {
		t.Fatalf("应输出 5 行，实际 %d 行", len(lines))
	}
	for i, l := range lines {
		// 每个半块字符正好占一列，数它就等于数显示宽度
		if got := strings.Count(l, "▀"); got != 20 {
			t.Fatalf("第 %d 行应有 20 列（半块字符），实际 %d", i, got)
		}
	}
}

// 颜色要真的写进去：纯红图渲染出来必须带 24-bit 红
func TestThumbnailCarriesTrueColor(t *testing.T) {
	img := solidImage(50, 50, color.RGBA{200, 30, 30, 255})
	lines := thumbnailLines(img, 8, 4, true)

	if len(lines) == 0 {
		t.Fatal("应产出内容")
	}
	if !strings.Contains(lines[0], "38;2;") || !strings.Contains(lines[0], "48;2;") {
		t.Fatalf("应使用 24-bit 真彩前景/背景，实际 %q", lines[0])
	}
	if !strings.Contains(lines[0], "▀") {
		t.Fatalf("应用半块字符拼图，实际 %q", lines[0])
	}
	// 红分量 200 应出现在转义序列里
	if !strings.Contains(lines[0], "200;") {
		t.Fatalf("应带上真实的红色分量，实际 %q", lines[0])
	}
}

// 无颜色终端：不要画彩色块（那只会得到乱码或一片黑），返回 nil 让调用方改文字
func TestThumbnailDisabledWithoutColor(t *testing.T) {
	img := solidImage(50, 50, color.RGBA{10, 20, 30, 255})
	if got := thumbnailLines(img, 20, 5, false); got != nil {
		t.Fatalf("无颜色时应返回 nil，实际 %q", got)
	}
}

// 退化输入不能崩：零尺寸、零行数都得安全返回
func TestThumbnailHandlesDegenerateInput(t *testing.T) {
	img := solidImage(4, 4, color.RGBA{1, 2, 3, 255})
	if got := thumbnailLines(img, 0, 5, true); got != nil {
		t.Fatal("cols=0 应返回 nil")
	}
	if got := thumbnailLines(img, 10, 0, true); got != nil {
		t.Fatal("rows=0 应返回 nil")
	}
	if got := thumbnailLines(nil, 10, 5, true); got != nil {
		t.Fatal("nil 图应返回 nil")
	}
}

// ---------------------------------------------------------------- 路径识别

// 模型说"已保存到 E:\AI资料\cat.png"——界面要能把那个路径认出来
func TestFindImagePathsInProse(t *testing.T) {
	text := "图已经生成好了，保存在 E:\\AI资料\\cat.png ，你可以看看。"
	got := findImagePaths(text, 3)

	if len(got) != 1 || got[0] != `E:\AI资料\cat.png` {
		t.Fatalf("应从正文里认出图片路径，实际 %v", got)
	}
}

// 中文标点、引号也要能剥掉
func TestFindImagePathsStripsPunctuation(t *testing.T) {
	got := findImagePaths("见「/tmp/a.png」，还有 \"C:\\pics\\b.jpg\"。", 5)
	if len(got) != 2 {
		t.Fatalf("应认出 2 个路径，实际 %v", got)
	}
	for _, p := range got {
		if strings.HasPrefix(p, "「") || strings.HasPrefix(p, `"`) || strings.HasSuffix(p, `"`) {
			t.Fatalf("标点没剥干净：%q", p)
		}
	}
}

// 只认绝对路径：相对路径没法确定指的是哪儿，认了反而会去读错文件
func TestFindImagePathsRequiresAbsolute(t *testing.T) {
	got := findImagePaths("看看 pic.png 和 sub/dir/a.jpg", 5)
	if len(got) != 0 {
		t.Fatalf("相对路径不该被认出来，实际 %v", got)
	}
}

// 非图片扩展名不认
func TestFindImagePathsSkipsNonImages(t *testing.T) {
	got := findImagePaths(`E:\a.txt E:\b.zip E:\c.md`, 5)
	if len(got) != 0 {
		t.Fatalf("非图片不该被认出来，实际 %v", got)
	}
}

// 同一个路径出现多次只算一个，且受 limit 限制
func TestFindImagePathsDedupsAndLimits(t *testing.T) {
	got := findImagePaths(`C:\a.png C:\a.png C:\b.png C:\c.png`, 2)
	if len(got) != 2 {
		t.Fatalf("应去重并受 limit 限制，实际 %v", got)
	}
}

// 大段无关文本不该被误判（也顺带确认没有明显性能问题）
func TestFindImagePathsOnPlainText(t *testing.T) {
	text := strings.Repeat("这是一段普通的中文回答，没有任何路径。", 50)
	if got := findImagePaths(text, 3); len(got) != 0 {
		t.Fatalf("普通文本不该认出路径，实际 %v", got)
	}
}
