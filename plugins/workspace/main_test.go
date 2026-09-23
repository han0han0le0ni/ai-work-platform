package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 相对路径以工作目录为基准拼出来
func TestResolveUsesRootForRelativePaths(t *testing.T) {
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

// 围栏已取消：落在工作目录之外的绝对路径也照单全收
func TestResolveAllowsAbsolutePathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	w := &Workspace{root: root}

	outside := []string{
		filepath.Join(filepath.Dir(root), "outside"),
		filepath.Join(filepath.Dir(root), "outside", "a.txt"),
		filepath.Join(root, "a", "..", "..", "b.txt"),
	}
	for _, in := range outside {
		got, err := w.resolve(in)
		if err != nil {
			t.Fatalf("resolve(%q) 不应再被围栏拒绝: %v", in, err)
		}
		if got != filepath.Clean(in) {
			t.Fatalf("resolve(%q) = %q，期望 %q", in, got, filepath.Clean(in))
		}
	}
}

// 没指定工作目录时，绝对路径照样可用（全盘可访问）
func TestResolveAbsolutePathWithoutRoot(t *testing.T) {
	w := &Workspace{}
	abs := filepath.Join(t.TempDir(), "a.txt")
	got, err := w.resolve(abs)
	if err != nil {
		t.Fatalf("未设置工作目录时绝对路径应可用: %v", err)
	}
	if got != abs {
		t.Fatalf("resolve(%q) = %q，期望 %q", abs, got, abs)
	}
}

// 没指定工作目录时，相对路径没有落脚点，先提示去设置
func TestResolveRequiresRootForRelativePath(t *testing.T) {
	w := &Workspace{}
	if _, err := w.resolve("a.txt"); err == nil || !strings.Contains(err.Error(), "setRoot") {
		t.Fatalf("未设置工作目录时相对路径应提示先 setRoot，实际 %v", err)
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
