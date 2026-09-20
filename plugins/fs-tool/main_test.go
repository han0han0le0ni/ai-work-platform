package main

import (
	"path/filepath"
	"testing"
)

// 相对路径要落到用户指定的工作区，而不是运行时目录（core.exe 旁边）。
// 这是用户报过的问题：明明在界面上选好了文件夹，AI 却往系统目录里写。
func TestResolveIn(t *testing.T) {
	root := t.TempDir()

	cases := []struct {
		name string
		path string
		root string
		want string
	}{
		{"相对路径拼到工作区", "notes/a.txt", root, filepath.Join(root, "notes", "a.txt")},
		{"空路径就是工作区根", "", root, root},
		{"只有空格也算空", "   ", root, root},
		{"去掉首尾空白", "  a.txt  ", root, filepath.Join(root, "a.txt")},
		{"没工作区时保持原样", "a.txt", "", "a.txt"},
	}

	for _, c := range cases {
		if got := resolveIn(c.path, c.root); got != c.want {
			t.Fatalf("%s：resolveIn(%q, %q) = %q，期望 %q", c.name, c.path, c.root, got, c.want)
		}
	}

	// 绝对路径永远原样使用：调用方写全了就按它说的办
	abs := filepath.Join(root, "already", "absolute.txt")
	if got := resolveIn(abs, "/some/other/root"); got != abs {
		t.Fatalf("绝对路径不该被改写：%q -> %q", abs, got)
	}
}
