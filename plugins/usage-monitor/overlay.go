package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// 浮层的几何：宽 36 列，贴在屏幕右上角，从第 2 行开始 ——
// 第 1 行是工作台的标题栏（写核心版本与插件 id），不盖它。
const (
	overlayW      = 36
	overlayTopRow = 2
)

const cReset = "\033[0m"

// overlay 是「悬浮窗」本体：直接往控制终端写的一小块矩形。
type overlay struct {
	u *Usage
	f *os.File

	mu sync.Mutex
}

// openOverlay 打开控制终端。拿不到终端（被重定向、无控制台）就返回 nil，
// 插件其余功能照常工作。
func openOverlay(u *Usage) *overlay {
	f, err := openConsoleOut()
	if err != nil {
		u.log("warn", "没有可用的控制终端，用量悬浮窗关闭："+err.Error())
		return nil
	}
	return &overlay{u: u, f: f}
}

func (o *overlay) close() {
	if o == nil || o.f == nil {
		return
	}
	_ = o.f.Close()
}

// render 按当前数据重画浮层。所有写终端的地方都在这里，且只用一次 Write。
func (o *overlay) render() {
	if o == nil || o.f == nil {
		return
	}

	u := o.u
	u.mu.Lock()
	cfg := u.cfg
	totals := u.totals
	balance := u.balance
	u.mu.Unlock()

	if cfg.OverlayOff {
		return
	}

	w, _ := consoleSize(o.f)
	if w < overlayW+4 {
		return // 窗口太窄，画上去只会糊成一团
	}

	lines := overlayLines(overlayW, totals, balance)
	col := w - overlayW + 1

	var sb strings.Builder
	sb.WriteString("\0337") // 保存光标：不干扰工作台自己维护的光标位置
	for i, line := range lines {
		sb.WriteString(fmt.Sprintf("\033[%d;%dH", overlayTopRow+i, col))
		sb.WriteString(cReset)
		sb.WriteString(line)
		sb.WriteString("\033[K")
	}
	sb.WriteString("\0338") // 恢复光标

	o.mu.Lock()
	defer o.mu.Unlock()
	_, _ = o.f.WriteString(sb.String())
}

// overlayLines 生成浮层的每一行（含边框），每行宽度都精确等于 w 个显示列。
func overlayLines(w int, t Totals, b Balance) []string {
	out := make([]string, 0, 8)
	out = append(out, ovTop(w, "DeepSeek 用量"))
	out = append(out, ovRow(w, "余额", balanceShort(b)))
	out = append(out, ovRow(w, "输入", humanInt(t.PromptTokens)+" tok"))
	out = append(out, ovRow(w, "输出", humanInt(t.CompletionTokens)+" tok"))
	out = append(out, ovRow(w, "缓存", hitRateShort(t)))
	out = append(out, ovRow(w, "请求", fmt.Sprintf("%d 次 · %s", t.Requests, shortClock(t.LastAt))))
	out = append(out, ovBottom(w))
	return out
}

func ovTop(w int, title string) string {
	head := "┌─ " + title + " "
	fill := w - dispWidth(head) - 1
	if fill < 0 {
		fill = 0
	}
	return head + strings.Repeat("─", fill) + "┐"
}

func ovBottom(w int) string {
	fill := w - 2
	if fill < 0 {
		fill = 0
	}
	return "└" + strings.Repeat("─", fill) + "┘"
}

// ovRow 画一行「│ 键        值 │」，值靠右。
func ovRow(w int, key, val string) string {
	inner := w - 2
	if inner < 4 {
		inner = 4
	}
	left := " " + key
	right := val + " "
	pad := inner - dispWidth(left) - dispWidth(right)
	if pad < 0 {
		// 值太长：先截值，再让左边顶满
		avail := inner - dispWidth(left) - 1
		if avail < 1 {
			avail = 1
		}
		right = cutDisplay(val, avail) + " "
		pad = inner - dispWidth(left) - dispWidth(right)
		if pad < 0 {
			pad = 0
		}
	}
	return "│" + left + strings.Repeat(" ", pad) + right + "│"
}

// balanceShort 把余额压成一行。
func balanceShort(b Balance) string {
	switch {
	case b.Err != "":
		return "不可用"
	case !b.OK:
		return "查询中…"
	}
	cur := "¥"
	switch b.Currency {
	case "USD":
		cur = "$"
	case "CNY", "":
		cur = "¥"
	default:
		cur = b.Currency + " "
	}
	return cur + b.Total
}

// hitRateShort 画成「62.3% (1.2M)」——百分比在前，命中量在后。
func hitRateShort(t Totals) string {
	den := t.CacheHitTokens + t.CacheMissTokens
	if den <= 0 {
		return "—"
	}
	pct := float64(t.CacheHitTokens) * 100 / float64(den)
	return fmt.Sprintf("%.1f%% (%s)", pct, humanInt(t.CacheHitTokens))
}

func shortClock(at string) string {
	if at == "" {
		return "—"
	}
	return at
}
