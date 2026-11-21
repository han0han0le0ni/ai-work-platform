package capability

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"aiwork/core/jsonrpc"
)

// shell.pickfolder 要自己的权限：光有 process:spawn 不算。
// 这正是它存在的意义 —— 弹系统选择框不该让插件拿到"能执行任意命令"的权限。
func TestPickFolderRequiresOwnPermission(t *testing.T) {
	p := New(Options{}, nil)
	p.Grant(plugin, []string{PermProcessSpawn})

	_, rpcErr := p.Dispatch(plugin, "core.shell.pickfolder", params(map[string]any{}))
	if rpcErr == nil || rpcErr.Code != jsonrpc.CodePermissionDenied {
		t.Fatalf("只有 process:spawn 时应被拒绝，实际 %v", rpcErr)
	}
}

// 脚本要带上标题和初始目录，用户看到的对话框才是有语境的。
func TestPickFolderScriptEmbedsTitleAndInitial(t *testing.T) {
	script := pickFolderScript("选择工作目录", "E:\\my project")
	if !strings.Contains(script, "选择工作目录") {
		t.Fatalf("脚本应含标题：%s", script)
	}
	if !strings.Contains(script, "E:\\my project") {
		t.Fatalf("脚本应含初始目录：%s", script)
	}
}

// 用户选中的路径经 base64 交回来，而不是明文写出：
// PowerShell 往管道写字用的是**系统当前的代码页**，中文机器上是 GBK ——
// 明文写路径的话，像"万物皆插件"这样的目录到这里就是一串 GBK 字节，
// 被当成 UTF-8 一拼，整条路径成了乱码。base64 只用 ASCII，跟语言设置无关。
func TestPickFolderPathSurvivesChinesePath(t *testing.T) {
	want := filepath.Join("E:\\", "万物皆插件")
	out := base64.StdEncoding.EncodeToString([]byte(want))
	if got := decodePickedPath([]byte(out + "\r\n")); got != want {
		t.Fatalf("中文路径应原样还原，实际 %q，期望 %q", got, want)
	}
}

// 一句都说不出来 = 用户点了取消
func TestDecodePickedPathEmptyMeansCancelled(t *testing.T) {
	if got := decodePickedPath([]byte("  \r\n  ")); got != "" {
		t.Fatalf("空输出应视为取消，实际 %q", got)
	}
	if got := decodePickedPath(nil); got != "" {
		t.Fatalf("无输出应视为取消，实际 %q", got)
	}
}

// 脚本这一侧必须真的编了 base64，否则上面的还原只是自说自话
func TestPickFolderScriptWritesBase64(t *testing.T) {
	script := pickFolderScript("选择工作目录", "E:\\万物皆插件")
	if !strings.Contains(script, "[System.Text.Encoding]::UTF8.GetBytes") ||
		!strings.Contains(script, "ToBase64String") {
		t.Fatalf("脚本应把路径编成 UTF-8 的 base64 再写出：%s", script)
	}
}

// PowerShell 单引号字符串里唯一的转义就是把 ' 写成 ''。
func TestPsQuoteEscapesSingleQuotes(t *testing.T) {
	if got := psQuote("a'b"); got != "a''b" {
		t.Fatalf("单引号应写成两个，实际 %q", got)
	}
}

// -EncodedCommand 要的是 UTF-16LE 的 base64；这里验证编码能正确还原。
func TestPsEncodedCommandRoundTrip(t *testing.T) {
	enc := psEncodedCommand("echo 你好")
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("应是合法 base64: %v", err)
	}
	if len(b)%2 != 0 {
		t.Fatalf("UTF-16LE 字节数应是偶数，实际 %d", len(b))
	}
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i < len(b); i += 2 {
		units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
	}
	if got := string(utf16.Decode(units)); got != "echo 你好" {
		t.Fatalf("还原后应一致，实际 %q", got)
	}
}
