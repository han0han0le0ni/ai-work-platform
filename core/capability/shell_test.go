package capability

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"aiwork/core/jsonrpc"
)

// shell.open 要自己的权限：光有 process:spawn 不算。
//
// 这正是它存在的意义 —— UI 插件想要"点一下打开图片"时，
// 不该为此拿到能执行任意命令的权限。
func TestShellOpenRequiresOwnPermission(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.png")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermProcessSpawn})

	_, rpcErr := p.Dispatch(plugin, "core.shell.open", params(map[string]any{"path": file}))
	if rpcErr == nil || rpcErr.Code != jsonrpc.CodePermissionDenied {
		t.Fatalf("只有 process:spawn 时应被拒绝，实际 %v", rpcErr)
	}
}

// 目录不给开：这个能力是"打开文件"，别变成"打开资源管理器"
func TestShellOpenRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pics")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermShellOpen})

	_, rpcErr := p.Dispatch(plugin, "core.shell.open", params(map[string]any{"path": sub}))
	if rpcErr == nil || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("目录应被拒绝，实际 %v", rpcErr)
	}
}

// 不存在的路径要如实报错，不能假装打开了
func TestShellOpenRejectsMissingPath(t *testing.T) {
	root := t.TempDir()
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermShellOpen})

	_, rpcErr := p.Dispatch(plugin, "core.shell.open", params(map[string]any{
		"path": filepath.Join(root, "nope.png"),
	}))
	if rpcErr == nil {
		t.Fatal("不存在的文件应报错")
	}
}

// 路径围栏照样生效：fsRoot 之外的东西不给开
func TestShellOpenRespectsFSRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	file := filepath.Join(outside, "secret.png")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermShellOpen})

	if _, rpcErr := p.Dispatch(plugin, "core.shell.open", params(map[string]any{"path": file})); rpcErr == nil {
		t.Fatal("fsRoot 之外的路径应被拒绝")
	}
}

// 缺 path 要报错，而不是去开个什么都不开
func TestShellOpenRequiresPath(t *testing.T) {
	p := New(Options{}, nil)
	p.Grant(plugin, []string{PermShellOpen})
	if _, rpcErr := p.Dispatch(plugin, "core.shell.open", params(map[string]any{})); rpcErr == nil {
		t.Fatal("缺 path 应报错")
	}
}

// 命令形状：路径必须是**独立的一个 argv 元素**，绝不拼进 shell 字符串。
// 文件名里带 & | ; " 时，拼字符串就会被解释成第二条命令。
func TestOpenCommandKeepsPathAsSingleArgv(t *testing.T) {
	nasty := `a b&;|"x.png`
	cmd, err := openCommand(nasty)
	if err != nil {
		t.Fatalf("应能构造命令: %v", err)
	}
	found := false
	for _, a := range cmd.Args {
		if a == nasty {
			found = true
		}
	}
	if !found {
		t.Fatalf("路径应原样作为一个参数传入，实际 argv: %q", cmd.Args)
	}

	// Windows 上不该经过 cmd.exe（多一层解析就多一层注入面）
	if runtime.GOOS == "windows" {
		if strings.Contains(strings.ToLower(cmd.Args[0]), "cmd") {
			t.Fatalf("不该走 cmd.exe：%q", cmd.Args)
		}
		if !strings.Contains(strings.ToLower(cmd.Args[0]), "rundll32") {
			t.Fatalf("Windows 上应用 rundll32，实际 %q", cmd.Args[0])
		}
	}
}
