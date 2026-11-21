package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

// pngBytes 造一张真 PNG 的字节，供假 backend 返回。
func pngBytes(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// layoutColor 用彩色渲染一帧。
// 缩略图只在有颜色的终端上画（无颜色时彩色块就是乱码），所以测试必须按彩色渲染。
func layoutColor(a *app, w, h int) {
	a.st.w, a.st.h = w, h
	a.canv = renderFrame(a.st, w, h, true)
}

// drainedThumb 等一条缩略图结果并应用。
func drainedThumb(t *testing.T, a *app) outcome {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case out := <-a.results:
			a.applyOutcome(out)
			if out.kind == "thumb" {
				return out
			}
		case <-deadline:
			t.Fatal("等缩略图超时")
			return outcome{}
		}
	}
}

// 消息里提到一个存在的图片路径，就要在它下面画出来。
// 这是"AI 发图"的落点：模型写出保存路径，界面就能显示图。
func TestMessageImagePathTriggersThumbnail(t *testing.T) {
	fb := &fakeBackend{fakeImg: pngBytes(t, 20, 20, color.RGBA{200, 40, 40, 255})}
	a := newTestApp(fb)
	a.st.msgs = []msgView{{
		role: "assistant",
		text: `图已经生成好了，保存在 E:\AI资料\cat.png，你看看。`,
	}}

	layoutColor(a, 110, 30)

	// 渲染时应当登记了这个路径
	if len(a.st.thumbWanted) != 1 || a.st.thumbWanted[0] != `E:\AI资料\cat.png` {
		t.Fatalf("渲染时应登记要读的图片，实际 %v", a.st.thumbWanted)
	}

	a.pumpThumbs()
	drainedThumb(t, a)

	e := a.st.thumbs[`E:\AI资料\cat.png`]
	if e == nil || e.failed || len(e.lines) != thumbRows {
		t.Fatalf("应缓存好缩略图，实际 %#v", e)
	}

	// 再渲染一帧：画面上要真的出现半块字符
	layoutColor(a, 110, 30)
	frame := a.canv.frame()
	if !strings.Contains(frame, "▀") {
		t.Fatal("画面上应出现缩略图（半块字符）")
	}
	if !strings.Contains(frame, "cat.png") {
		t.Fatal("应显示文件名")
	}
	if !strings.Contains(frame, `E:\AI资料\cat.png`) {
		t.Fatal("应显示完整路径")
	}
}

// 读不出来（文件没了 / 不是图片）要说清楚，而不是画一片空白
func TestBrokenImageShowsReason(t *testing.T) {
	fb := &fakeBackend{fakeImgErr: errTest}
	a := newTestApp(fb)
	a.st.msgs = []msgView{{role: "assistant", text: `见 E:\gone.png`}}

	layoutColor(a, 110, 30)
	a.pumpThumbs()
	drainedThumb(t, a)
	layoutColor(a, 110, 30)

	if !strings.Contains(a.canv.frame(), "读不出来") {
		t.Fatal("读图失败时应有明确说明")
	}
}

// 点「打开」= 让系统默认程序打开，不是让我们自己去执行命令
func TestClickOpensImageViaShell(t *testing.T) {
	fb := &fakeBackend{fakeImg: pngBytes(t, 8, 8, color.RGBA{10, 10, 200, 255})}
	a := newTestApp(fb)
	a.st.msgs = []msgView{{role: "assistant", text: `见 E:\pics\a.png`}}

	layoutColor(a, 110, 30)
	a.pumpThumbs()
	drainedThumb(t, a)
	layoutColor(a, 110, 30)

	x, y := findBox(t, a, `openimg:E:\pics\a.png`)
	a.click(x, y)

	waitFor(t, "交给系统打开", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.opened) == 1
	})
	fb.mu.Lock()
	got := fb.opened[0]
	fb.mu.Unlock()
	if got != `E:\pics\a.png` {
		t.Fatalf("打开路径不对：%q", got)
	}
}

// .svg 是矢量图：卡片要显示，但**不该去读它的内容** ——
// 标准库没有 SVG 解码器，读了也只是白跑一次 RPC。
func TestVectorImageShowsCardWithoutLoading(t *testing.T) {
	fb := &fakeBackend{}
	a := newTestApp(fb)
	a.st.msgs = []msgView{{role: "assistant", text: `画好了，见 E:\pics\flag.svg`}}

	layoutColor(a, 110, 30)

	if len(a.st.thumbWanted) != 0 {
		t.Fatalf("矢量图不该触发读取，实际 %v", a.st.thumbWanted)
	}
	fb.mu.Lock()
	n := len(fb.imgReads)
	fb.mu.Unlock()
	if n != 0 {
		t.Fatalf("不该发起图片读取，实际 %d 次", n)
	}
	frame := a.canv.frame()
	if !strings.Contains(frame, "flag.svg") {
		t.Fatal("应显示文件名")
	}
	if !strings.Contains(frame, "矢量图") {
		t.Fatal("应说明这是矢量图（而不是卡在「正在读图」）")
	}
}

// 没有图片路径的消息不该去读任何东西：否则每帧都会发起一堆无用 RPC
func TestPlainMessageDoesNotLoadImages(t *testing.T) {
	fb := &fakeBackend{fakeImg: pngBytes(t, 8, 8, color.RGBA{1, 1, 1, 255})}
	a := newTestApp(fb)
	a.st.msgs = []msgView{{role: "assistant", text: "这是一段普通回答，没有任何路径。"}}

	layoutColor(a, 110, 30)

	if len(a.st.thumbWanted) != 0 {
		t.Fatalf("普通消息不该登记图片，实际 %v", a.st.thumbWanted)
	}
	fb.mu.Lock()
	n := len(fb.imgReads)
	fb.mu.Unlock()
	if n != 0 {
		t.Fatalf("不该发起任何图片读取，实际 %d 次", n)
	}
}

// 同一张图渲染多帧只读一次（缓存要真的起作用，否则每帧都发 RPC）
func TestThumbnailCachedAcrossFrames(t *testing.T) {
	fb := &fakeBackend{fakeImg: pngBytes(t, 8, 8, color.RGBA{3, 3, 3, 255})}
	a := newTestApp(fb)
	a.st.msgs = []msgView{{role: "assistant", text: `见 C:\x\a.png`}}

	layoutColor(a, 110, 30)
	a.pumpThumbs()
	drainedThumb(t, a)

	for i := 0; i < 3; i++ {
		layoutColor(a, 110, 30)
		a.pumpThumbs()
	}

	fb.mu.Lock()
	n := len(fb.imgReads)
	fb.mu.Unlock()
	if n != 1 {
		t.Fatalf("缓存后不该重复读取，实际读了 %d 次", n)
	}
}
