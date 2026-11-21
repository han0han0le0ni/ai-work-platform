package capability

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"aiwork/core/jsonrpc"
)

const plugin = "com.test.tool"

func newProxy(root string) *Proxy {
	return New(Options{FSRoot: root}, nil)
}

func params(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// 规范 3.5 + 3.4：默认拒绝 —— 未声明权限的能力调用一律 -32002
func TestDeniedWithoutPermission(t *testing.T) {
	p := newProxy("")
	p.Grant(plugin, nil)

	cases := []string{"core.fs.read", "core.fs.list", "core.fs.write", "core.net.request", "core.process.spawn"}
	for _, m := range cases {
		_, rpcErr := p.Dispatch(plugin, m, params(map[string]any{"path": "x", "url": "http://x", "command": "x"}))
		if rpcErr == nil {
			t.Errorf("%s 在无权限时应当被拒绝", m)
			continue
		}
		if rpcErr.Code != jsonrpc.CodePermissionDenied {
			t.Errorf("%s 应返回 -32002，实际 %d", m, rpcErr.Code)
		}
	}
}

// 授权后能力可用；撤销后立即失效
func TestGrantAndRevoke(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(target, []byte("你好，能力代理"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := newProxy("")
	p.Grant(plugin, []string{PermFSRead})

	res, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": target}))
	if rpcErr != nil {
		t.Fatalf("已授权却失败: %v", rpcErr)
	}
	if got := res.(map[string]any)["content"]; got != "你好，能力代理" {
		t.Fatalf("内容不正确: %v", got)
	}

	p.Revoke(plugin)
	if _, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": target})); rpcErr == nil ||
		rpcErr.Code != jsonrpc.CodePermissionDenied {
		t.Fatalf("撤销后应被拒绝，实际 %v", rpcErr)
	}
}

// 能力面之外的 core.* 方法不代理
func TestUnknownCapabilityMethod(t *testing.T) {
	p := newProxy("")
	p.Grant(plugin, []string{PermFSRead})
	if Known("core.fs.read") != true || Known("core.fs.remove") != false {
		t.Fatal("Known 判定不正确")
	}
	_, rpcErr := p.Dispatch(plugin, "core.fs.remove", params(map[string]any{"path": "/"}))
	if rpcErr == nil || rpcErr.Code != jsonrpc.CodeMethodNotFound {
		t.Fatalf("未定义的能力方法应返回 -32601，实际 %v", rpcErr)
	}
}

// 规范 3.5：fsRoot 限制生效，越界访问被拒绝
func TestFSRootConfinement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("外部文件"), 0o644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("内部文件"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := newProxy(root)
	p.Grant(plugin, []string{PermFSRead, PermFSWrite})

	if res, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": inside})); rpcErr != nil {
		t.Fatalf("根目录内应可读: %v", rpcErr)
	} else if res.(map[string]any)["content"] != "内部文件" {
		t.Fatalf("内容不正确: %v", res)
	}

	if _, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": secret})); rpcErr == nil ||
		rpcErr.Code != jsonrpc.CodePermissionDenied {
		t.Fatalf("越界读取应被拒绝，实际 %v", rpcErr)
	}
	if _, rpcErr := p.Dispatch(plugin, "core.fs.write", params(map[string]any{
		"path": filepath.Join(outside, "x.txt"), "content": "x",
	})); rpcErr == nil || rpcErr.Code != jsonrpc.CodePermissionDenied {
		t.Fatalf("越界写入应被拒绝，实际 %v", rpcErr)
	}
	if _, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{
		"path": filepath.Join(root, "..", filepath.Base(outside), "secret.txt"),
	})); rpcErr == nil || rpcErr.Code != jsonrpc.CodePermissionDenied {
		t.Fatalf("用 .. 绕过应被拒绝，实际 %v", rpcErr)
	}
}

// 写 -> 读 -> 列 往返；limit 生效
func TestFSWriteReadList(t *testing.T) {
	root := t.TempDir()
	p := newProxy(root)
	p.Grant(plugin, []string{PermFSRead, PermFSWrite})

	nested := filepath.Join(root, "a", "b", "c.txt")
	if _, rpcErr := p.Dispatch(plugin, "core.fs.write", params(map[string]any{
		"path": nested, "content": "0123456789", "mkdir": true,
	})); rpcErr != nil {
		t.Fatalf("写入失败: %v", rpcErr)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("mkdir 未生效: %v", err)
	}

	res, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": nested, "limit": 4}))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	got := res.(map[string]any)
	if got["content"] != "0123" || got["truncated"] != true {
		t.Fatalf("limit 未生效: %v", got)
	}

	if _, rpcErr := p.Dispatch(plugin, "core.fs.write", params(map[string]any{
		"path": nested, "content": "+more", "append": true,
	})); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	data, _ := os.ReadFile(nested)
	if string(data) != "0123456789+more" {
		t.Fatalf("append 未生效: %q", data)
	}

	lst, rpcErr := p.Dispatch(plugin, "core.fs.list", params(map[string]any{"path": filepath.Join(root, "a", "b")}))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	entries := lst.(map[string]any)["entries"].([]map[string]any)
	if len(entries) != 1 || entries[0]["name"] != "c.txt" {
		t.Fatalf("列目录不正确: %v", entries)
	}
}

// 读目录要用 fs.list
func TestFSReadDirectoryRejected(t *testing.T) {
	root := t.TempDir()
	p := newProxy("")
	p.Grant(plugin, []string{PermFSRead})
	_, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": root}))
	if rpcErr == nil || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("读目录应提示改用 fs.list，实际 %v", rpcErr)
	}
}

// 网络：只允许 http/https，非白名单方法直接拒绝（不发真实请求）
func TestNetRequestValidation(t *testing.T) {
	p := newProxy("")
	p.Grant(plugin, []string{PermNetwork})

	if _, rpcErr := p.Dispatch(plugin, "core.net.request", params(map[string]any{
		"url": "file:///etc/passwd",
	})); rpcErr == nil || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("非 http(s) 协议应被拒绝，实际 %v", rpcErr)
	}
	if _, rpcErr := p.Dispatch(plugin, "core.net.request", params(map[string]any{
		"method": "TRACE", "url": "http://example.com",
	})); rpcErr == nil || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("非白名单方法应被拒绝，实际 %v", rpcErr)
	}
}

// 子进程：经核心执行并回收 stdout / 退出码
func TestSpawn(t *testing.T) {
	p := newProxy("")
	p.Grant(plugin, []string{PermProcessSpawn})

	command, args := "/bin/echo", []string{"hello-spawn"}
	if runtime.GOOS == "windows" {
		command, args = "cmd", []string{"/c", "echo", "hello-spawn"}
	}
	res, rpcErr := p.Dispatch(plugin, "core.process.spawn", params(map[string]any{
		"command": command, "args": args,
	}))
	if rpcErr != nil {
		t.Fatalf("spawn 失败: %v", rpcErr)
	}
	out := res.(map[string]any)
	if out["exitCode"] != 0 {
		t.Fatalf("退出码应为 0，实际 %v", out["exitCode"])
	}
	if !strings.Contains(out["stdout"].(string), "hello-spawn") {
		t.Fatalf("stdout 不正确: %v", out["stdout"])
	}
}

// 相对路径固定解析到基准目录，绝不跟随核心进程的当前目录
func TestRelativePathBaseDir(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "a.txt"), []byte("relative"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New(Options{BaseDir: base}, nil)
	p.Grant(plugin, []string{PermFSRead})

	res, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": "a.txt"}))
	if rpcErr != nil {
		t.Fatalf("相对路径应解析到基准目录: %v", rpcErr)
	}
	if res.(map[string]any)["content"] != "relative" {
		t.Fatalf("内容不正确: %v", res)
	}

	// 没有基准目录时明确拒绝，而不是偷偷用进程当前目录
	bare := New(Options{}, nil)
	bare.Grant(plugin, []string{PermFSRead})
	if _, rpcErr := bare.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": "a.txt"})); rpcErr == nil ||
		rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("未配置基准目录时应拒绝相对路径，实际 %v", rpcErr)
	}
}

// fsRoot 同时作为相对路径基准，围栏依然生效
func TestRelativePathInsideFSRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "in.txt"), []byte("in"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermFSRead})

	if _, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": "in.txt"})); rpcErr != nil {
		t.Fatalf("fsRoot 内的相对路径应可读: %v", rpcErr)
	}
	if _, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": "../escape.txt"})); rpcErr == nil ||
		rpcErr.Code != jsonrpc.CodePermissionDenied {
		t.Fatalf("相对路径越界应被拒绝，实际 %v", rpcErr)
	}
}

// 权限清单可被读出，便于审计
func TestPermissionsSnapshot(t *testing.T) {
	p := newProxy("")
	p.Grant(plugin, []string{PermNetwork, PermFSRead})
	got := p.Permissions(plugin)
	if len(got) != 2 || got[0] != PermFSRead || got[1] != PermNetwork {
		t.Fatalf("权限清单不正确: %v", got)
	}
	p.Reset()
	if len(p.Permissions(plugin)) != 0 {
		t.Fatal("Reset 应清空全部授权")
	}
}

// 删除：递归删目录、删不存在的东西不报错、目录必须显式 recursive
func TestFSDelete(t *testing.T) {
	root := t.TempDir()
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermFSDelete})

	// 目录未经 recursive 时拒绝，避免一次调用删掉一整棵树
	nested := filepath.Join(root, "echo.plugin", "main")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("exe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := p.Dispatch(plugin, "core.fs.delete", params(map[string]any{
		"path": filepath.Join(root, "echo.plugin"),
	})); rpcErr == nil || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("删除目录未带 recursive 应被拒绝，实际 %v", rpcErr)
	}

	res, rpcErr := p.Dispatch(plugin, "core.fs.delete", params(map[string]any{
		"path": filepath.Join(root, "echo.plugin"), "recursive": true,
	}))
	if rpcErr != nil {
		t.Fatalf("递归删除失败: %v", rpcErr)
	}
	got := res.(map[string]any)
	if got["deleted"] != true || got["existed"] != true {
		t.Fatalf("删除结果不正确: %v", got)
	}
	// 未配置待删除区时直接删，不走中转
	if got["moved"] != false {
		t.Fatalf("未配置 pending-delete 时不应走中转: %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "echo.plugin")); !os.IsNotExist(err) {
		t.Fatalf("目录仍然存在: %v", err)
	}

	// 删不存在的路径不是错误，只是 existed=false
	res, rpcErr = p.Dispatch(plugin, "core.fs.delete", params(map[string]any{
		"path": filepath.Join(root, "gone.plugin"), "recursive": true,
	}))
	if rpcErr != nil {
		t.Fatalf("删除不存在的路径不应报错: %v", rpcErr)
	}
	if res.(map[string]any)["existed"] != false {
		t.Fatalf("existed 应为 false: %v", res)
	}
}

// 按 base64 读二进制：这是图片/音视频能被正确上传的前提。
// 直接按 UTF-8 读会把无效字节换成 U+FFFD，图片会变成打不开的乱码。
func TestFSReadBase64(t *testing.T) {
	root := t.TempDir()
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermFSRead})

	// 一段故意包含非法 UTF-8 字节的“图片”
	raw := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0xFF, 0xFE, 0x00, 0x80}
	path := filepath.Join(root, "pic.png")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// 默认按 UTF-8 读：内容会被破坏（这正是要避免的）
	res, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": path}))
	if rpcErr != nil {
		t.Fatalf("读取失败: %v", rpcErr)
	}
	if got := res.(map[string]any)["content"].(string); got == base64.StdEncoding.EncodeToString(raw) {
		t.Fatal("默认不该返回 base64")
	}

	// 显式要 base64：必须能原样还原
	res, rpcErr = p.Dispatch(plugin, "core.fs.read", params(map[string]any{
		"path": path, "encoding": "base64",
	}))
	if rpcErr != nil {
		t.Fatalf("base64 读取失败: %v", rpcErr)
	}
	got := res.(map[string]any)
	if got["encoding"] != "base64" {
		t.Fatalf("应标注 encoding: %v", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(got["content"].(string))
	if err != nil {
		t.Fatalf("base64 解码失败: %v", err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Fatalf("二进制应原样还原，实际 %x", decoded)
	}
	if got["truncated"] != false {
		t.Fatal("小文件不应被截断")
	}
}

// 截断的一刀不能落在汉字身上：limit 按字节算，一个汉字却是 3 个字节，
// 直接切片会在结尾留下半个 UTF-8 序列 —— 往后传一路都带着一个 U+FFFD，
// 看着就像文件坏了，其实是这一刀切歪了。
func TestFSReadTruncatesOnRuneBoundary(t *testing.T) {
	root := t.TempDir()
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermFSRead})

	content := "万物皆插件已经上线了"
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	for n := 1; n <= len(content); n++ {
		res, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": path, "limit": n}))
		if rpcErr != nil {
			t.Fatalf("limit=%d 读取失败: %v", n, rpcErr)
		}
		got := res.(map[string]any)["content"].(string)
		if !utf8.ValidString(got) {
			t.Fatalf("limit=%d 切出了半个汉字: %q", n, got)
		}
		if strings.ContainsRune(got, '\uFFFD') {
			t.Fatalf("limit=%d 出现了替换字符: %q", n, got)
		}
	}

	// 两个字正好 6 字节：限额够就不能少给一个字
	res, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": path, "limit": 6}))
	if rpcErr != nil {
		t.Fatalf("读取失败: %v", rpcErr)
	}
	if got := res.(map[string]any)["content"].(string); got != "万物" {
		t.Fatalf("limit=6 应读到整整两个字，实际 %q", got)
	}
}

// 字节序标记不属于文件内容，Windows 那边留它的习惯很普遍
func TestFSReadStripsUTF8BOM(t *testing.T) {
	root := t.TempDir()
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermFSRead})

	path := filepath.Join(root, "bom.txt")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbf第一行\n第二行"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": path}))
	if rpcErr != nil {
		t.Fatalf("读取失败: %v", rpcErr)
	}
	if got := res.(map[string]any)["content"].(string); got != "第一行\n第二行" {
		t.Fatalf("应去掉 BOM，实际 %q", got)
	}
}

// GB18030 是绝大多数中文 .txt / .csv / .log 的实际编码。
// 不注明的话按 UTF-8 硬读，一整篇都是 U+FFFD —— 用户看到的就是"一个字都不认识"。
func TestFSReadGB18030(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("GB18030 的码表由 Windows 提供，其余平台拿不出可靠替代")
	}
	root := t.TempDir()
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermFSRead})

	// "万物皆插件" 的 GB18030 字节
	data := []byte{0xCD, 0xF2, 0xCE, 0xEF, 0xBD, 0xD4, 0xB2, 0xE5, 0xBC, 0xFE}
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	const want = "万物皆插件"
	for _, enc := range []string{"gb18030", "gbk", "gb2312"} {
		res, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": path, "encoding": enc}))
		if rpcErr != nil {
			t.Fatalf("encoding=%s 读取失败: %v", enc, rpcErr)
		}
		got := res.(map[string]any)
		if got["encoding"] != "gb18030" {
			t.Fatalf("应标注实际的解码方式: %v", got)
		}
		if got["content"].(string) != want {
			t.Fatalf("encoding=%s 解码错误，实际 %q，期望 %q", enc, got["content"], want)
		}
	}
}

// 中文路径 / 中文文件名 / 中文内容在能力层要全通。
// 工作区插件的"设一个中文文件夹当工作区"最终都落到这些能力上，
// 任何一环把非 ASCII 处理坏了，用户看到的就是"汉字文件夹不能用"。
func TestFSChinesePathsEndToEnd(t *testing.T) {
	root := t.TempDir()
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermFSRead, PermFSWrite, PermFSDelete})

	dir := filepath.Join(root, "中文目录")
	if _, rpcErr := p.Dispatch(plugin, "core.fs.mkdir", params(map[string]any{"path": dir, "recursive": true})); rpcErr != nil {
		t.Fatalf("建中文目录失败: %v", rpcErr)
	}

	file := filepath.Join(dir, "笔记.txt")
	res, rpcErr := p.Dispatch(plugin, "core.fs.write", params(map[string]any{"path": file, "content": "第一行\n第二行"}))
	if rpcErr != nil {
		t.Fatalf("写中文路径失败: %v", rpcErr)
	}
	if res.(map[string]any)["path"] != file {
		t.Fatalf("写回路径不一致: %v", res)
	}

	// 列中文目录：文件名必须原样是中文
	res, rpcErr = p.Dispatch(plugin, "core.fs.list", params(map[string]any{"path": dir}))
	if rpcErr != nil {
		t.Fatalf("列中文目录失败: %v", rpcErr)
	}
	entries := res.(map[string]any)["entries"].([]map[string]any)
	if len(entries) != 1 || entries[0]["name"] != "笔记.txt" {
		t.Fatalf("中文文件名未原样返回: %v", entries)
	}

	// 读中文内容
	res, rpcErr = p.Dispatch(plugin, "core.fs.read", params(map[string]any{"path": file}))
	if rpcErr != nil {
		t.Fatalf("读中文路径失败: %v", rpcErr)
	}
	if got := res.(map[string]any)["content"].(string); got != "第一行\n第二行" {
		t.Fatalf("中文内容不一致: %q", got)
	}

	// 移动（改名）成另一个中文名
	dst := filepath.Join(dir, "改名.txt")
	if _, rpcErr = p.Dispatch(plugin, "core.fs.move", params(map[string]any{"from": file, "to": dst})); rpcErr != nil {
		t.Fatalf("中文改名失败: %v", rpcErr)
	}

	// 删除
	if _, rpcErr = p.Dispatch(plugin, "core.fs.delete", params(map[string]any{"path": dst})); rpcErr != nil {
		t.Fatalf("删除中文文件失败: %v", rpcErr)
	}
}

// 超过上限时明确标记，免得把半个图片当成完整文件发给模型
func TestFSReadBase64Truncated(t *testing.T) {
	root := t.TempDir()
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermFSRead})

	path := filepath.Join(root, "big.png")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0xFF}, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	res, rpcErr := p.Dispatch(plugin, "core.fs.read", params(map[string]any{
		"path": path, "encoding": "base64", "limit": 100,
	}))
	if rpcErr != nil {
		t.Fatalf("读取失败: %v", rpcErr)
	}
	if res.(map[string]any)["truncated"] != true {
		t.Fatalf("超出 limit 应标记 truncated: %v", res)
	}
}

// 建目录：递归建、已存在不报错、建在文件上要拒绝
func TestFSMkdir(t *testing.T) {
	root := t.TempDir()
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermFSWrite})

	deep := filepath.Join(root, "a", "b", "c")
	res, rpcErr := p.Dispatch(plugin, "core.fs.mkdir", params(map[string]any{"path": deep, "recursive": true}))
	if rpcErr != nil {
		t.Fatalf("递归建目录失败: %v", rpcErr)
	}
	if res.(map[string]any)["created"] != true {
		t.Fatalf("首次创建应返回 created=true: %v", res)
	}
	if info, err := os.Stat(deep); err != nil || !info.IsDir() {
		t.Fatalf("目录应已建立: %v", err)
	}

	// 再建一次：不报错，只说已存在
	res, rpcErr = p.Dispatch(plugin, "core.fs.mkdir", params(map[string]any{"path": deep, "recursive": true}))
	if rpcErr != nil {
		t.Fatalf("重复建目录不应报错: %v", rpcErr)
	}
	if res.(map[string]any)["created"] != false || res.(map[string]any)["existed"] != true {
		t.Fatalf("重复建目录应标记为已存在: %v", res)
	}

	// 不递归时，父目录不在就该失败
	if _, rpcErr := p.Dispatch(plugin, "core.fs.mkdir", params(map[string]any{
		"path": filepath.Join(root, "x", "y"),
	})); rpcErr == nil {
		t.Fatal("未递归且父目录缺失时应失败")
	}

	// 已有同名文件时不能建目录
	file := filepath.Join(root, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := p.Dispatch(plugin, "core.fs.mkdir", params(map[string]any{"path": file})); rpcErr == nil ||
		rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("同名文件存在时应拒绝建目录，实际 %v", rpcErr)
	}

	// 越权
	bare := New(Options{FSRoot: root}, nil)
	bare.Grant(plugin, []string{PermFSRead})
	if _, rpcErr := bare.Dispatch(plugin, "core.fs.mkdir", params(map[string]any{"path": deep})); rpcErr == nil ||
		rpcErr.Code != jsonrpc.CodePermissionDenied {
		t.Fatalf("未声明 filesystem:write 应被拒绝，实际 %v", rpcErr)
	}
}

// core.fs.move：改名换目录，这是插件启停的实现基础
func TestFSMove(t *testing.T) {
	base := t.TempDir()
	p := New(Options{BaseDir: base}, nil)
	p.Grant(plugin, []string{PermFSWrite})

	from := filepath.Join(base, "plugins", "echo.plugin")
	if err := os.MkdirAll(from, 0o755); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(base, "plugins", "echo.plugin"+".disabled")

	res, rpcErr := p.Dispatch(plugin, "core.fs.move", params(map[string]any{"from": from, "to": to}))
	if rpcErr != nil {
		t.Fatalf("移动失败: %v", rpcErr)
	}
	if res.(map[string]any)["moved"] != true {
		t.Fatalf("应返回 moved=true: %v", res)
	}
	if _, err := os.Stat(to); err != nil {
		t.Fatalf("目标应存在: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatal("源应已消失")
	}

	// 目标已存在时拒绝覆盖：否则两个插件会被搅在一起
	if err := os.MkdirAll(from, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := p.Dispatch(plugin, "core.fs.move", params(map[string]any{
		"from": from, "to": to,
	})); rpcErr == nil || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("目标已存在时应拒绝，实际 %v", rpcErr)
	}

	// 源不存在 / 缺少参数 / 越权
	if _, rpcErr := p.Dispatch(plugin, "core.fs.move", params(map[string]any{
		"from": filepath.Join(base, "plugins", "nope.plugin"), "to": to,
	})); rpcErr == nil || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("源不存在时应拒绝，实际 %v", rpcErr)
	}
	if _, rpcErr := p.Dispatch(plugin, "core.fs.move", params(map[string]any{"from": from})); rpcErr == nil ||
		rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("缺少 to 时应拒绝，实际 %v", rpcErr)
	}

	bare := New(Options{BaseDir: base}, nil)
	bare.Grant(plugin, []string{PermFSRead})
	if _, rpcErr := bare.Dispatch(plugin, "core.fs.move", params(map[string]any{
		"from": from, "to": filepath.Join(base, "plugins", "x.plugin"),
	})); rpcErr == nil || rpcErr.Code != jsonrpc.CodePermissionDenied {
		t.Fatalf("未声明 filesystem:write 应被拒绝，实际 %v", rpcErr)
	}
}

// 移动同样受运行时护栏保护：不能把 plugins/ 整个搬走
func TestFSMoveGuardRuntimeDir(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := New(Options{BaseDir: base}, nil)
	p.Grant(plugin, []string{PermFSWrite})

	if _, rpcErr := p.Dispatch(plugin, "core.fs.move", params(map[string]any{
		"from": filepath.Join(base, "plugins"), "to": filepath.Join(base, "plugins2"),
	})); rpcErr == nil || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("移动一级目录应被护栏拒绝，实际 %v", rpcErr)
	}
}

// 删除需要独立权限：只有 filesystem:write 不算
func TestFSDeleteRequiresOwnPermission(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New(Options{FSRoot: root}, nil)
	p.Grant(plugin, []string{PermFSRead, PermFSWrite})

	if _, rpcErr := p.Dispatch(plugin, "core.fs.delete", params(map[string]any{"path": target})); rpcErr == nil ||
		rpcErr.Code != jsonrpc.CodePermissionDenied {
		t.Fatalf("未声明 filesystem:delete 应被拒绝，实际 %v", rpcErr)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("被拒绝时不应删除文件")
	}
}

// 护栏：不允许把运行时目录本身或它的一级子目录删空
func TestFSDeleteGuardRuntimeDir(t *testing.T) {
	base := t.TempDir()
	for _, sub := range []string{"plugins", "plugin-configs", "logs"} {
		if err := os.MkdirAll(filepath.Join(base, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := New(Options{BaseDir: base}, nil)
	p.Grant(plugin, []string{PermFSDelete})

	for _, path := range []string{base, filepath.Join(base, "plugins"), filepath.Join(base, "logs")} {
		if _, rpcErr := p.Dispatch(plugin, "core.fs.delete", params(map[string]any{
			"path": path, "recursive": true,
		})); rpcErr == nil || rpcErr.Code != jsonrpc.CodeInvalidParams {
			t.Fatalf("%s 应被护栏拒绝，实际 %v", path, rpcErr)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s 被删掉了", path)
		}
	}

	// 一级目录里的具体条目是可以删的
	one := filepath.Join(base, "plugins", "echo.plugin")
	if err := os.MkdirAll(one, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := p.Dispatch(plugin, "core.fs.delete", params(map[string]any{
		"path": one, "recursive": true,
	})); rpcErr != nil {
		t.Fatalf("具体插件目录应允许删除: %v", rpcErr)
	}
}

// 删不掉时把目标整体搬进 pending-delete/：原位置立刻清空，副本留在待删除区
func TestMoveToPending(t *testing.T) {
	base := t.TempDir()
	pending := filepath.Join(base, "pending-delete")
	p := New(Options{BaseDir: base, PendingDir: pending}, nil)

	target := filepath.Join(base, "plugins", "busy.plugin")
	if err := os.MkdirAll(filepath.Join(target, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, err := p.moveToPending(target)
	if err != nil {
		t.Fatalf("移入待删除区失败: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("原位置应已清空: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "manifest.json")); err != nil {
		t.Fatalf("副本应完整保留: %v", err)
	}
	if filepath.Dir(dst) != pending {
		t.Fatalf("副本应落在 pending-delete/ 下，实际 %s", dst)
	}
	// 两次搬运不能互相覆盖
	second := filepath.Join(base, "plugins", "busy.plugin")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	dst2, err := p.moveToPending(second)
	if err != nil || dst2 == dst {
		t.Fatalf("同名目标应生成不同的落点: %v %s", err, dst2)
	}
}

// 彻底删不动（连改名都被拒）时：老实报错，且绝不把目录留在半删状态
func TestFSDeleteHonestFailureWhenStuck(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("依赖 Windows 的文件锁语义")
	}
	base := t.TempDir()
	pending := filepath.Join(base, "pending-delete")
	p := New(Options{BaseDir: base, PendingDir: pending}, nil)
	p.Grant(plugin, []string{PermFSDelete})

	target := filepath.Join(base, "plugins", "busy.plugin")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	stuck := filepath.Join(target, "manifest.json")
	if err := os.WriteFile(stuck, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 不带 FILE_SHARE_DELETE 的句柄：删除与改名都会被拒
	hold, err := os.Open(stuck)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()

	_, rpcErr := p.Dispatch(plugin, "core.fs.delete", params(map[string]any{
		"path": target, "recursive": true,
	}))
	if rpcErr == nil {
		t.Skip("当前环境允许删除被占用的文件，无法复现卡死场景")
	}
	if _, err := os.Stat(stuck); err != nil {
		t.Fatalf("删不掉时必须保持现场完整，不能半删: %v", err)
	}
}
