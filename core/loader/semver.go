package loader

import (
	"fmt"
	"strconv"
	"strings"
)

// Version 是一个极简语义化版本（只比较 major.minor.patch）。
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion 解析 "1.2.3" / "v1.2" / "1.2.3-beta.1"。
func ParseVersion(s string) (Version, error) {
	raw := strings.TrimSpace(s)
	raw = strings.TrimPrefix(raw, "v")
	if raw == "" {
		return Version{}, fmt.Errorf("版本号为空")
	}
	// 去掉预发布与构建元数据
	if i := strings.IndexAny(raw, "-+"); i >= 0 {
		raw = raw[:i]
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 3 {
		return Version{}, fmt.Errorf("版本号格式非法: %s", s)
	}
	var v Version
	dst := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return Version{}, fmt.Errorf("版本号格式非法: %s", s)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("版本号格式非法: %s", s)
		}
		*dst[i] = n
	}
	return v, nil
}

// Compare 返回 -1 / 0 / 1。
func (v Version) Compare(o Version) int {
	if v.Major != o.Major {
		return sign(v.Major - o.Major)
	}
	if v.Minor != o.Minor {
		return sign(v.Minor - o.Minor)
	}
	return sign(v.Patch - o.Patch)
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		return 0
	}
}

// String 输出规范形式。
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// MatchRange 判断版本是否落在范围表达式内。
//
// 支持：">=1.0 <2.0"、"^1.0"、"~1.2"、"1.0.0"、"*"（多个比较符按 AND 组合）。
func MatchRange(version, rng string) (bool, error) {
	rng = strings.TrimSpace(rng)
	if rng == "" || rng == "*" {
		return true, nil
	}
	v, err := ParseVersion(version)
	if err != nil {
		return false, err
	}
	tokens := strings.FieldsFunc(rng, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t'
	})
	if len(tokens) == 0 {
		return true, nil
	}
	for _, tok := range tokens {
		ok, err := matchToken(v, tok)
		if err != nil {
			return false, fmt.Errorf("无法解析版本范围 %q: %w", rng, err)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func matchToken(v Version, tok string) (bool, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" || tok == "*" {
		return true, nil
	}
	op := "="
	for _, p := range []string{">=", "<=", "==", ">", "<", "=", "^", "~"} {
		if strings.HasPrefix(tok, p) {
			op = p
			tok = strings.TrimSpace(tok[len(p):])
			break
		}
	}
	target, err := ParseVersion(tok)
	if err != nil {
		return false, err
	}
	switch op {
	case ">=":
		return v.Compare(target) >= 0, nil
	case "<=":
		return v.Compare(target) <= 0, nil
	case ">":
		return v.Compare(target) > 0, nil
	case "<":
		return v.Compare(target) < 0, nil
	case "^":
		return v.Compare(target) >= 0 && v.Major == target.Major, nil
	case "~":
		return v.Compare(target) >= 0 && v.Major == target.Major && v.Minor == target.Minor, nil
	default: // "=" / "=="
		return v.Compare(target) == 0, nil
	}
}
