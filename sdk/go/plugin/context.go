package plugin

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Context 是插件与核心交互的唯一入口。
//
// 它只暴露核心的 6 个基础接口，不暴露任何核心内部实现。
type Context struct {
	PluginID    string
	APIVersion  string
	CoreVersion string
	Namespace   string
	Permissions []string

	s *server
}

// ---------------------------------------------------------------------------
// ① 服务注册与发现
// ---------------------------------------------------------------------------

// Provide 注册服务。服务名必须带版本，例如 echo.v1。
func (c *Context) Provide(name string, meta any) error {
	_, err := c.s.call("core.provide", map[string]any{"name": name, "meta": meta}, 10*time.Second)
	return err
}

// Consume 发现服务，返回提供者插件 ID。
func (c *Context) Consume(name string) (string, error) {
	resp, err := c.s.call("core.consume", map[string]any{"name": name}, 10*time.Second)
	if err != nil {
		return "", err
	}
	var out struct {
		PluginID string `json:"pluginId"`
		Alive    bool   `json:"alive"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return "", fmt.Errorf("解析 core.consume 响应失败: %w", err)
	}
	return out.PluginID, nil
}

// ---------------------------------------------------------------------------
// ② 服务调用
// ---------------------------------------------------------------------------

// Invoke 调用另一个插件提供的服务。
func (c *Context) Invoke(service, method string, args any) (json.RawMessage, error) {
	resp, err := c.s.call("core.invoke", map[string]any{
		"service": service,
		"method":  method,
		"args":    args,
	}, 0)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// ---------------------------------------------------------------------------
// ③ 事件总线
// ---------------------------------------------------------------------------

// Emit 发布事件。事件名必须带命名空间，例如 chat.message。
func (c *Context) Emit(event string, payload any) error {
	return c.s.notify("core.emit", map[string]any{"event": event, "payload": payload})
}

// On 订阅事件。"*" 表示订阅全部。
//
// 这里必须**同时**做两件事：把处理函数记在本地，并告诉核心"我要这个事件"。
// 只记本地是不行的 —— 核心的 Emit 只投给 bus 里登记过的订阅者，
// 没订阅过的进程收不到任何事件，处理函数永远等不到调用。
func (c *Context) On(event string, h HandlerFunc) {
	c.s.addHandler(event, h)
	if _, err := c.s.call("core.on", map[string]any{"event": event}, 10*time.Second); err != nil {
		Logf("warn", "订阅事件 %s 失败: %v", event, err)
	}
}

// ---------------------------------------------------------------------------
// ④ 配置管理
// ---------------------------------------------------------------------------

// GetConfig 读取配置。键不存在时返回 (nil, nil)。
func (c *Context) GetConfig(key string) (json.RawMessage, error) {
	resp, err := c.s.call("core.getConfig", map[string]any{"key": key}, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var out struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
		Found bool            `json:"found"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("解析 core.getConfig 响应失败: %w", err)
	}
	if !out.Found || len(out.Value) == 0 || string(out.Value) == "null" {
		return nil, nil
	}
	return out.Value, nil
}

// GetConfigString 读取字符串配置，缺失时返回 def。
func (c *Context) GetConfigString(key, def string) string {
	v, err := c.GetConfig(key)
	if err != nil || len(v) == 0 {
		return def
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(v, &n) == nil {
		return n.String()
	}
	return def
}

// GetConfigInt 读取整数配置，缺失时返回 def。
func (c *Context) GetConfigInt(key string, def int) int {
	v, err := c.GetConfig(key)
	if err != nil || len(v) == 0 {
		return def
	}
	var n int
	if json.Unmarshal(v, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		if i, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return i
		}
	}
	return def
}

// SetConfig 写入配置。配置键只增不减。
func (c *Context) SetConfig(key string, value any) error {
	_, err := c.s.call("core.setConfig", map[string]any{"key": key, "value": value}, 10*time.Second)
	return err
}

// ---------------------------------------------------------------------------
// ⑤ 日志
// ---------------------------------------------------------------------------

// Log 上报日志。核心只写入 logs/<pluginId>.log，并广播 core.log 事件。
func (c *Context) Log(level, msg string, meta map[string]any) {
	if err := c.s.notify("core.log", map[string]any{"level": level, "msg": msg, "meta": meta}); err != nil {
		Logf(level, "%s (上报失败: %v)", msg, err)
	}
}

// ---------------------------------------------------------------------------
// ⑥ 沙箱（接口预留）
// ---------------------------------------------------------------------------

// SandboxRun 请求沙箱执行代码。未安装沙箱插件时返回 -32003。
func (c *Context) SandboxRun(code string, options any) (json.RawMessage, error) {
	resp, err := c.s.call("core.sandbox.run", map[string]any{"code": code, "options": options}, 0)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// ---------------------------------------------------------------------------
// 第二阶段：秘密存储（接口预留）
// ---------------------------------------------------------------------------

// GetSecret 读取密钥。未安装 secrets.v1 插件时返回 -32000。
func (c *Context) GetSecret(key string) (json.RawMessage, error) {
	resp, err := c.s.call("core.getSecret", map[string]any{"key": key}, 10*time.Second)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// SetSecret 写入密钥。
func (c *Context) SetSecret(key string, value string) error {
	_, err := c.s.call("core.setSecret", map[string]any{"key": key, "value": value}, 10*time.Second)
	return err
}

// ---------------------------------------------------------------------------
// 能力代理（规范 3.5）：文件、网络、系统调用由核心代为执行
//
// 权限必须在 manifest 的 permissions 中声明，否则核心返回 -32002。
// 核心是唯一的执行者，插件只描述“想做什么”，不自己动手。
// ---------------------------------------------------------------------------

// Capability 直接调用能力代理的任意方法。
func (c *Context) Capability(method string, params any) (json.RawMessage, error) {
	resp, err := c.s.call(method, params, 0)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// FSRead 经核心读文件（需 filesystem:read），返回 {path,size,content,truncated}。
func (c *Context) FSRead(path string) (json.RawMessage, error) {
	return c.Capability("core.fs.read", map[string]any{"path": path})
}

// FSReadBase64 按 base64 读文件（需 filesystem:read），返回 {path,size,encoding,content,truncated}。
//
// 读图片、音视频等二进制必须用它：默认的 FSRead 会转成字符串，
// 无效 UTF-8 字节被换成 U+FFFD，文件就毁了。
func (c *Context) FSReadBase64(path string) (json.RawMessage, error) {
	return c.Capability("core.fs.read", map[string]any{"path": path, "encoding": "base64"})
}

// FSWrite 经核心写文件（需 filesystem:write），返回 {path,size,append}。
func (c *Context) FSWrite(path, content string, appendMode, mkdir bool) (json.RawMessage, error) {
	return c.Capability("core.fs.write", map[string]any{
		"path":    path,
		"content": content,
		"append":  appendMode,
		"mkdir":   mkdir,
	})
}

// ShellOpen 请核心用系统默认程序打开一个文件（需 shell:open），
// 返回 {path,opened}。核心不等程序退出，所以调用会立刻返回。
//
// 它比 Spawn 窄得多：命令与参数由核心写死，插件只能给路径 ——
// 界面想"点一下打开图片"时用这个，别去要 process:spawn。
func (c *Context) ShellOpen(path string) (json.RawMessage, error) {
	return c.Capability("core.shell.open", map[string]any{"path": path})
}

// FSWriteBytes 按 base64 写二进制文件（需 filesystem:write），返回 {path,size}。
//
// 写图片、压缩包这类内容必须用它：FSWrite 会把内容当字符串写下去，
// 无效 UTF-8 字节被替换，存出来的文件是坏的。
func (c *Context) FSWriteBytes(path string, data []byte, mkdir bool) (json.RawMessage, error) {
	return c.Capability("core.fs.write", map[string]any{
		"path":     path,
		"content":  base64.StdEncoding.EncodeToString(data),
		"encoding": "base64",
		"mkdir":    mkdir,
	})
}

// FSList 经核心列目录（需 filesystem:read），返回 {path,entries:[{name,dir,size}]}。
func (c *Context) FSList(path string) (json.RawMessage, error) {
	return c.Capability("core.fs.list", map[string]any{"path": path})
}

// FSDelete 经核心删除文件或目录（需 filesystem:delete），
// 返回 {path,existed,deleted,moved,pending?,error?}。
//
// 目标被占用（例如 Windows 上正在运行的插件）时核心不会留下垃圾：
// 会把目标改名移进 pending-delete/，由核心在退出与下次启动时清理。
func (c *Context) FSDelete(path string, recursive bool) (json.RawMessage, error) {
	return c.Capability("core.fs.delete", map[string]any{"path": path, "recursive": recursive})
}

// FSMkdir 经核心创建目录（需 filesystem:write），返回 {path,created,existed}。
// 目录已存在时不报错，只把 created 置为 false。
func (c *Context) FSMkdir(path string, recursive bool) (json.RawMessage, error) {
	return c.Capability("core.fs.mkdir", map[string]any{"path": path, "recursive": recursive})
}

// FSMove 经核心重命名 / 移动文件或目录（需 filesystem:write），返回 {from,to,moved}。
//
// 目标已存在时核心会拒绝：覆盖式改名会把两个插件搅在一起。
func (c *Context) FSMove(from, to string) (json.RawMessage, error) {
	return c.Capability("core.fs.move", map[string]any{"from": from, "to": to})
}

// NetRequest 经核心发起 HTTP 请求（需 network），返回 {status,headers,body,truncated}。
func (c *Context) NetRequest(method, url string, headers map[string]string, body string) (json.RawMessage, error) {
	return c.Capability("core.net.request", map[string]any{
		"method":  method,
		"url":     url,
		"headers": headers,
		"body":    body,
	})
}

// Spawn 经核心执行子进程（需 process:spawn），返回 {exitCode,stdout,stderr,truncated}。
func (c *Context) Spawn(command string, args []string) (json.RawMessage, error) {
	return c.Capability("core.process.spawn", map[string]any{"command": command, "args": args})
}

// ---------------------------------------------------------------------------
// 本地日志：只写 stderr，绝不污染 stdout 上的 JSON-RPC 通道
// ---------------------------------------------------------------------------

// Logf 写插件自身的 stderr 日志，核心会把它转发到 logs/<pluginId>.log。
func Logf(level, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s [%s] %s\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), strings.ToUpper(level), fmt.Sprintf(format, args...))
}
