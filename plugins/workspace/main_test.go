package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 围栏是这个插件的核心价值：工作目录之外的路径一律进不来
func TestResolveConfinesToRoot(t *testing.T) {
	root := t.TempDir()
	w := &Workspace{root: root}

	cases := []struct {
		in   string
		want string
	}{
		{"", root},
		{".", root},
		{"a.txt", filepath.Join(root, "a.txt")},
		{"sub/a.txt", filepath.Join(root, "sub", "a.txt")},
		{"./sub/../a.txt", filepath.Join(root, "a.txt")},
		{filepath.Join(root, "sub", "a.txt"), filepath.Join(root, "sub", "a.txt")},
	}
	for _, c := range cases {
		got, err := w.resolve(c.in)
		if err != nil {
			t.Fatalf("resolve(%q) 不应报错: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("resolve(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 各种逃出去的写法都必须被挡下
func TestResolveRejectsEscaping(t *testing.T) {
	root := t.TempDir()
	w := &Workspace{root: root}

	bad := []string{
		"..",
		"../",
		"../..",
		".." + string(os.PathSeparator) + "etc",
		".." + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "etc" + string(os.PathSeparator) + "passwd",
		"a/../../b",
		"sub/../../../outside",
		filepath.Join(filepath.Dir(root), "outside"),
		filepath.Join(filepath.Dir(root), "outside", "a.txt"),
	}
	for _, in := range bad {
		got, err := w.resolve(in)
		if err == nil {
			t.Fatalf("resolve(%q) 应被拒绝，实际放行到 %q", in, got)
		}
		if !strings.Contains(err.Error(), "越界") {
			t.Fatalf("拒绝理由应说明越界，实际 %q", err.Error())
		}
	}
}

// 没指定工作目录时，任何路径操作都要先提示去设置
func TestResolveRequiresRoot(t *testing.T) {
	w := &Workspace{}
	if _, err := w.resolve("a.txt"); err == nil || !strings.Contains(err.Error(), "setRoot") {
		t.Fatalf("未设置工作目录时应提示先 setRoot，实际 %v", err)
	}
}

// rel 输出相对路径，读起来比绝对路径短
func TestRel(t *testing.T) {
	root := t.TempDir()
	w := &Workspace{root: root}
	if got := w.rel(filepath.Join(root, "sub", "a.txt")); got != "sub/a.txt" {
		t.Fatalf("rel = %q，期望 sub/a.txt", got)
	}
	if got := w.rel(root); got != "." {
		t.Fatalf("工作目录自身的 rel 应是 .，实际 %q", got)
	}
}

func TestSamePath(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "b")
	if !samePath(a, a) {
		t.Fatal("同路径应相等")
	}
	if samePath(a, filepath.Join(root, "c")) {
		t.Fatal("不同路径不应相等")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "x", "y"); got != "x" {
		t.Fatalf("应取第一个非空值，实际 %q", got)
	}
	if got := firstNonEmpty("", "  "); got != "" {
		t.Fatalf("全空应返回空串，实际 %q", got)
	}
}
