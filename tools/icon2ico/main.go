// icon2ico —— 把一张方形图标原图转成 Windows 多尺寸 .ico。
//
// 用法：
//
//	go run ./tools/icon2ico -in launcher/assets/appicon.jpg -out runtime/app.ico
//
// 它做四件事，都是为了“原图不等于图标”这件常见的事：
//  1. 先裁掉四周留白，再按 -pad 补回内边距。原图常常把图案顶到画布边缘，
//     直接缩到 16x16 时齿轮的齿会被切掉一圈。
//  2. 把和画布边缘相连的纯白背景抠成透明。示意图案内部的白色块不动
//     （只从四条边往里漫水，不是全局按颜色抠）。
//  3. 逐档用预乘 alpha 的盒式滤波缩放。不预乘的话，蓝色边缘和透明背景
//     混合后会糊出一圈白边。
//  4. 每档存成 32bpp BMP。BMP 载荷兼容性最好：资源编译器、老版资源管理器、
//     UpdateResource 都认；PNG 载荷只有 Vista 之后的 Explorer 认。
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// isWhite 判定“接近纯白”。阈值取 240：足够宽松，能吃掉 JPEG 压出来的
// 252/253/254，又不会啃掉图案本身的浅色高光（图案最亮处也到不了三通道
// 同时 240）。
func isWhite(c color.NRGBA) bool {
	return c.A > 0 && c.R >= 240 && c.G >= 240 && c.B >= 240
}

func main() {
	in := flag.String("in", "", "输入图片（png / jpg）")
	out := flag.String("out", "", "输出 .ico 路径")
	pad := flag.Float64("pad", 0.09, "裁掉留白后补回的内边距比例（相对图案边长）")
	sizes := flag.String("sizes", "16,24,32,48,64,128,256", "输出的各档边长，逗号分隔")
	flag.Parse()

	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "用法: icon2ico -in <图片> -out <图标.ico>")
		os.Exit(2)
	}

	src, err := decodeImage(*in)
	if err != nil {
		fail(err)
	}
	dims, err := parseSizes(*sizes)
	if err != nil {
		fail(err)
	}

	canvas := prepare(src, *pad)

	frames := make([]*image.NRGBA, 0, len(dims))
	for _, d := range dims {
		frames = append(frames, resize(canvas, d))
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, encodeICO(frames), 0o644); err != nil {
		fail(err)
	}

	names := make([]string, len(dims))
	for i, d := range dims {
		names[i] = strconv.Itoa(d)
	}
	fmt.Printf("icon  : %s (%dx%d -> %s)\n", *out, canvas.Bounds().Dx(), canvas.Bounds().Dy(), strings.Join(names, "/"))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "icon2ico:", err)
	os.Exit(1)
}

func decodeImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return png.Decode(f)
	case ".jpg", ".jpeg":
		return jpeg.Decode(f)
	default:
		return nil, fmt.Errorf("不认识的图片格式: %s（只支持 png / jpg）", filepath.Ext(path))
	}
}

// prepare 把原图整理成一张带着内边距、背景透明的方形画布。
func prepare(src image.Image, pad float64) *image.NRGBA {
	base := toNRGBA(src)
	b := base.Bounds()

	// 图案外接框：只看“不接近白色”的像素。
	x0, y0, x1, y1 := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	found := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := base.NRGBAAt(x, y)
			if c.A > 8 && !isWhite(c) {
				if x < x0 {
					x0 = x
				}
				if x+1 > x1 {
					x1 = x + 1
				}
				if y < y0 {
					y0 = y
				}
				if y+1 > y1 {
					y1 = y + 1
				}
				found = true
			}
		}
	}
	if !found {
		fail(fmt.Errorf("整张图都是白的，没找到图案"))
	}

	// 垫成正方形：canvas 先铺满不透明白，再把图案贴上来，
	// 最后从四条边漫水把这片白色抠掉。
	artW, artH := x1-x0, y1-y0
	side := max(artW, artH)
	padPx := int(math.Round(float64(side) * pad))
	if padPx < 1 {
		padPx = 1
	}

	canvas := image.NewNRGBA(image.Rect(0, 0, side+2*padPx, side+2*padPx))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.NRGBA{255, 255, 255, 255}), image.Point{}, draw.Src)
	dst := image.Rect(
		padPx+(side-artW)/2,
		padPx+(side-artH)/2,
		padPx+(side-artW)/2+artW,
		padPx+(side-artH)/2+artH,
	)
	draw.Draw(canvas, dst, base, image.Point{x0, y0}, draw.Src)

	keyOutBorderWhite(canvas)
	return canvas
}

func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// keyOutBorderWhite 从四条边漫水，把连到边缘的白背景变成透明。
func keyOutBorderWhite(m *image.NRGBA) {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	seen := make([]bool, w*h)

	type pt struct{ x, y int }
	queue := make([]pt, 0, (w+h)*2)
	for x := 0; x < w; x++ {
		queue = append(queue, pt{x, 0}, pt{x, h - 1})
	}
	for y := 0; y < h; y++ {
		queue = append(queue, pt{0, y}, pt{w - 1, y})
	}

	for len(queue) > 0 {
		p := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if p.x < 0 || p.y < 0 || p.x >= w || p.y >= h {
			continue
		}
		i := p.y*w + p.x
		if seen[i] {
			continue
		}
		seen[i] = true

		c := m.NRGBAAt(p.x, p.y)
		if c.A == 0 || !isWhite(c) {
			continue
		}
		m.SetNRGBA(p.x, p.y, color.NRGBA{0, 0, 0, 0})
		queue = append(queue, pt{p.x - 1, p.y}, pt{p.x + 1, p.y}, pt{p.x, p.y - 1}, pt{p.x, p.y + 1})
	}
}

// resize 预乘 alpha 的盒式滤波。
func resize(src *image.NRGBA, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()

	for y := 0; y < size; y++ {
		y0, y1 := y*sh/size, (y+1)*sh/size
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < size; x++ {
			x0, x1 := x*sw/size, (x+1)*sw/size
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, a uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					c := src.NRGBAAt(xx, yy)
					r += uint64(c.R) * uint64(c.A)
					g += uint64(c.G) * uint64(c.A)
					b += uint64(c.B) * uint64(c.A)
					a += uint64(c.A)
				}
			}
			if a == 0 {
				continue // dst 已经是全 0
			}
			n := uint64(y1-y0) * uint64(x1-x0)
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r / a),
				G: uint8(g / a),
				B: uint8(b / a),
				A: uint8(a / n),
			})
		}
	}
	return dst
}

// bmpIconEntry 把一帧写成 ICO 里的 32bpp BMP 载荷：
// BITMAPINFOHEADER（高度写两倍，因为后面跟 AND 掩码）+ 自下而上的 BGRA + 全 0 掩码。
func bmpIconEntry(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	rowSize := w * 4
	maskRow := ((w + 31) / 32) * 4 // 1bpp，每行补齐到 4 字节

	buf := new(bytes.Buffer)
	hdr := make([]byte, 40)
	binary.LittleEndian.PutUint32(hdr[0:], 40)          // biSize
	binary.LittleEndian.PutUint32(hdr[4:], uint32(w))   // biWidth
	binary.LittleEndian.PutUint32(hdr[8:], uint32(h*2)) // biHeight = XOR + AND
	binary.LittleEndian.PutUint16(hdr[12:], 1)          // biPlanes
	binary.LittleEndian.PutUint16(hdr[14:], 32)         // biBitCount
	binary.LittleEndian.PutUint32(hdr[20:], uint32(rowSize*h+maskRow*h))
	buf.Write(hdr)

	row := make([]byte, rowSize)
	for y := h - 1; y >= 0; y-- { // BMP 行序是自下而上
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			row[x*4+0] = c.B
			row[x*4+1] = c.G
			row[x*4+2] = c.R
			row[x*4+3] = c.A
		}
		buf.Write(row)
	}
	buf.Write(make([]byte, maskRow*h))
	return buf.Bytes()
}

// encodeICO 拼出 .ico：ICONDIR + 目录项 + 每帧的 BMP 载荷。
func encodeICO(frames []*image.NRGBA) []byte {
	blobs := make([][]byte, len(frames))
	for i, f := range frames {
		blobs[i] = bmpIconEntry(f)
	}

	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, uint16(0)) // idReserved
	binary.Write(buf, binary.LittleEndian, uint16(1)) // idType = icon
	binary.Write(buf, binary.LittleEndian, uint16(len(frames)))

	offset := 6 + 16*len(frames)
	dir := make([]byte, 16*len(frames))
	for i, blob := range blobs {
		w := frames[i].Bounds().Dx()
		e := dir[i*16 : (i+1)*16]
		if w >= 256 {
			e[0], e[1] = 0, 0 // 256 在 ICO 里用 0 表示
		} else {
			e[0], e[1] = byte(w), byte(w)
		}
		e[2], e[3] = 0, 0 // 颜色数、保留位
		binary.LittleEndian.PutUint16(e[4:], 1)
		binary.LittleEndian.PutUint16(e[6:], 32)
		binary.LittleEndian.PutUint32(e[8:], uint32(len(blob)))
		binary.LittleEndian.PutUint32(e[12:], uint32(offset))
		offset += len(blob)
	}
	buf.Write(dir)
	for _, blob := range blobs {
		buf.Write(blob)
	}
	return buf.Bytes()
}

func parseSizes(s string) ([]int, error) {
	out := []int{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > 256 {
			return nil, fmt.Errorf("非法尺寸 %q（允许 1..256）", part)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-sizes 一个尺寸都没给")
	}
	return out, nil
}
