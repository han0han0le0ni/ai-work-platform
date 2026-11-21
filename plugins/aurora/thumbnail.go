package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"  // 让 image.Decode 认识 gif
	_ "image/jpeg" // 以及 jpeg —— 这两个只注册解码器，不用显式引用
	_ "image/png"
	"path/filepath"
	"regexp"
	"strings"
)

// 缩略图渲染：把一张图变成终端能直接画出来的彩色字符。
//
// 用 "▀"（上半块）拼：一个字符格的**前景色画上半像素、背景色画下半像素**，
// 于是 rows 行字符正好装下 2×rows 行像素。好处是完全不需要终端支持
// Kitty / iTerm2 / Sixel 那类图形协议 —— 普通终端就能显示。
//
// 只用 24-bit 真彩色（\033[38;2;R;G;Bm）。256 色的近似会让照片糊成一片，
// 而现代终端（Windows Terminal / iTerm2 / WezTerm…）都支持真彩。

// thumbCols / thumbRows 是聊天记录里缩略图的默认尺寸（字符格）。
const (
	thumbCols = 40
	thumbRows = 10
)

// isImagePath 按扩展名判断能不能当图片显示。
func isImagePath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp", ".svg":
		return true
	}
	return false
}

// isVectorPath 判断是不是矢量图。
//
// SVG 是**文本**格式，标准库里没有解码器（也不该为它引一整个渲染器）。
// 所以它只走"卡片 + 打开"：文件名、路径、点开用浏览器看 —— 不画缩略图，
// 也不去读它的内容（读了也解不出来，白跑一趟 RPC）。
func isVectorPath(p string) bool {
	return strings.EqualFold(filepath.Ext(p), ".svg")
}

// decodeImage 解出一张图。解码失败就返回错误 —— 调用方据此显示"读不了"。
func decodeImage(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("无法解码图片: %w", err)
	}
	return img, nil
}

// thumbnailLines 把图渲染成 rows 行、每行 cols 个显示列的字符串。
//
// color=false 时返回 nil：没颜色的终端上画彩色块只会得到一堆乱码，
// 调用方应当改显示文字占位。
func thumbnailLines(img image.Image, cols, rows int, color bool) []string {
	if !color || cols <= 0 || rows <= 0 || img == nil {
		return nil
	}
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return nil
	}
	// 纵向像素数 = 字符行数 × 2（每格上下各一个像素）
	ph := rows * 2

	out := make([]string, 0, rows)
	for cy := 0; cy < rows; cy++ {
		var line strings.Builder
		for cx := 0; cx < cols; cx++ {
			top := samplePx(img, b, cx, cy*2, cols, ph)
			bot := samplePx(img, b, cx, cy*2+1, cols, ph)
			// 前景 = 上半像素，背景 = 下半像素，再画一个半块字符
			fmt.Fprintf(&line, "\033[38;2;%d;%d;%dm\033[48;2;%d;%d;%dm▀",
				top.R, top.G, top.B, bot.R, bot.G, bot.B)
		}
		line.WriteString(cReset)
		out = append(out, line.String())
	}
	return out
}

// samplePx 把目标网格坐标映射回源图并取色（最近邻）。
//
// 网格是 cols×(ph/2) 的字符格、实际采样 ph 行像素；
// 取图**中心区域**做等比裁剪，避免把一张宽图压成扁条。
func samplePx(img image.Image, b image.Rectangle, gx, gy, cols, ph int) color.RGBA {
	sw, sh := b.Dx(), b.Dy()

	// 目标像素坐标 → 归一化 [0,1)
	u := (float64(gx) + 0.5) / float64(cols)
	v := (float64(gy) + 0.5) / float64(ph)

	// 按"字符格高约等于 2 倍宽"把归一化坐标换算成源图坐标，
	// 再取中心裁剪：宽高比不匹配时只裁掉多余部分，不拉伸。
	srcAspect := float64(sw) / float64(sh)
	dstAspect := float64(cols) / float64(ph/2) // 字符格宽高比约 1:2
	var fx, fy float64
	if srcAspect > dstAspect {
		// 源图更宽：横向裁掉两侧
		keep := dstAspect / srcAspect
		fx = (1-keep)/2 + u*keep
		fy = v
	} else {
		// 源图更高：纵向裁掉上下
		keep := srcAspect / dstAspect
		fx = u
		fy = (1-keep)/2 + v*keep
	}

	sx := b.Min.X + int(fx*float64(sw))
	sy := b.Min.Y + int(fy*float64(sh))
	if sx >= b.Max.X {
		sx = b.Max.X - 1
	}
	if sy >= b.Max.Y {
		sy = b.Max.Y - 1
	}
	if sx < b.Min.X {
		sx = b.Min.X
	}
	if sy < b.Min.Y {
		sy = b.Min.Y
	}

	r, g, bl, a := img.At(sx, sy).RGBA()
	if a == 0 {
		// 全透明像素没有颜色可言，画成深灰，免得出现一整块纯黑
		return color.RGBA{40, 40, 40, 255}
	}
	// RGBA() 返回的是 alpha 预乘的 16 位值，右移 8 位回到 8 位
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 255}
}

// ---------------------------------------------------------------------------
// 从文本里认出图片路径
// ---------------------------------------------------------------------------

// imagePathRe 匹配三种绝对路径写法：盘符、UNC、POSIX。
//
// 字符类里刻意排除了中文句读与引号 —— 模型写路径时几乎总带着它们
// （"保存在 E:\AI资料\cat.png。"），不排除的话标点会被吃进路径里。
const pathStop = `\s"'<>|*?，。；：！？（）【】「」『』“”《》、`

var imagePathRe = regexp.MustCompile(
	`[A-Za-z]:[\\/][^` + pathStop + `]*` + // C:\x\y.png
		`|\\\\[^` + pathStop + `]*` + // \\server\share\x.png
		`|/[^` + pathStop + `]*`) // /home/me/x.png

// findImagePaths 从一段文本里挑出"看起来是图片文件"的绝对路径。
//
// 这是"AI 发图"的落地点：模型只要在回答里写出保存路径（例如
// "已保存到 E:\AI资料\cat.png"），界面就能把那张图直接画在下面。
// 不要求模型做任何特殊格式 —— 它本来就会说文件在哪。
//
// 只认绝对路径：相对路径没法确定指的是哪儿，认了反而会去读错文件。
func findImagePaths(text string, limit int) []string {
	if !strings.Contains(text, ".") {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, loc := range imagePathRe.FindAllStringIndex(text, -1) {
		p := text[loc[0]:loc[1]]
		// POSIX 路径得看前面一个字符：`sub/dir/a.jpg` 里的 `/dir/a.jpg`
		// 也匹配正则，但它只是个相对路径的尾巴 —— 认了就会去读错文件。
		if strings.HasPrefix(p, "/") && loc[0] > 0 && isPathChar(rune(text[loc[0]-1])) {
			continue
		}
		// 正则已在中文标点处停下，这里只再剥掉尾部可能粘着的英文句读
		p = strings.TrimRight(p, `.,;:!?)]}`)
		if p == "" || !isImagePath(p) || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// isPathChar 判断这个字符是不是"路径的一部分"。
// 用它来确认一个 "/" 到底是路径开头，还是夹在相对路径中间。
func isPathChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_' || r == '-' || r == '.' || r == '/' || r == '\\':
		return true
	}
	return false
}
