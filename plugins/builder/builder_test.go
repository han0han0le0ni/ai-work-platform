package main

import "testing"

// dirPattern 挡住路径穿越：builder 只允许 xxx.plugin 这种单个目录名。
// 这是拿到 process:spawn 之后的第一道闸门 —— 目标一旦能写成 ../，
// 编译产物就可以落到 plugins/ 之外的任何地方。
func TestDirPattern(t *testing.T) {
	good := []string{"calc.plugin", "my-tool.plugin", "a.plugin", "plugin123.plugin"}
	for _, d := range good {
		if !dirPattern.MatchString(d) {
			t.Fatalf("%q 应当被接受", d)
		}
	}
	bad := []string{
		"", "../calc.plugin", "a/b.plugin", "calc.plugin/../x", ".plugin",
		"calc.plugin.exe", `..\evil.plugin`, "calc.plugin/", "/etc/calc.plugin",
	}
	for _, d := range bad {
		if dirPattern.MatchString(d) {
			t.Fatalf("%q 应当被拒绝（会变成路径穿越）", d)
		}
	}
}
