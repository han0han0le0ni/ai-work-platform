package main

import (
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// 显示宽度：中文、emoji 占两列。终端排版最容易错的地方就在这里。
// 与工作台（plugins/workbench/render.go）同一套区间表。
// ---------------------------------------------------------------------------

// wideRanges 必须保持有序。
var wideRanges = [][2]rune{
	{0x1100, 0x115F},
	{0x2E80, 0x303E},
	{0x3041, 0x33FF},
	{0x3400, 0x4DBF},
	{0x4E00, 0x9FFF},
	{0xA000, 0xA4CF},
	{0xAC00, 0xD7A3},
	{0xF900, 0xFAFF},
	{0xFE10, 0xFE19},
	{0xFE30, 0xFE6F},
	{0xFF00, 0xFF60},
	{0xFFE0, 0xFFE6},
	{0x1F000, 0x1F0FF},
	{0x1F200, 0x1F2FF},
	{0x1F300, 0x1F64F},
	{0x1F680, 0x1F6FF},
	{0x1F900, 0x1F9FF},
	{0x1FA00, 0x1FAFF},
	{0x20000, 0x3FFFD},
}

func isWide(r rune) bool {
	lo, hi := 0, len(wideRanges)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case r < wideRanges[mid][0]:
			hi = mid - 1
		case r > wideRanges[mid][1]:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}

func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case r >= 0x0300 && r <= 0x036f:
		return 0
	case r >= 0x200b && r <= 0x200f:
		return 0
	case r == 0xfeff:
		return 0
	case isWide(r):
		return 2
	}
	return 1
}

func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// cutDisplay 按显示宽度硬截断，不补省略号。
func cutDisplay(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if dispWidth(s) <= max {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > max {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

// humanInt 给整数加千分位：1234567 -> 1,234,567。
func humanInt(n int) string {
	if n < 0 {
		return "-" + humanInt(-n)
	}
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	pre := len(s) % 3
	if pre == 0 {
		pre = 3
	}
	var b strings.Builder
	b.WriteString(s[:pre])
	for i := pre; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
