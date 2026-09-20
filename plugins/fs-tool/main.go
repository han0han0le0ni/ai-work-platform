// Command fs-tool 是官方文件工具插件，对应规范 4.2 链路里的 read_file。
//
// 它演示能力代理的正确用法：插件**不直接碰文件系统**，
// 而是把意图交给核心（core.fs.read / core.fs.write / core.fs.list），由核心按 manifest 声明的权限执行。
// 权限没声明，核心就返回 -32002，插件无能为力。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"aiwork/sdk/go/plugin"
)

// FsTool 实现 plugin.Plugin。
type FsTool struct {
	ctx *plugin.Context
}

// workspaceService 是用户在前端「文件」页指定的工作区。
const workspaceService = "workspace.v1"

// resolve 把相对路径优先解析到用户指定的工作区里。
//
// 不这么做的话，模型调 write_file {"path":"a.txt"} 会落到**运行时目录**（core.exe 旁边），
// 而不是用户在界面上挑好的那个文件夹 —— 用户明明指定了工作区，AI 却往系统目录里写。
//
// 绝对路径一律原样交出去：调用方既然写全了，就按它说的办。
// 工作区没装或没设置时保持原行为（由核心解析到运行时目录）。
func (f *FsTool) resolve(path string) string {
	return resolveIn(path, f.workspaceRoot())
}

// resolveIn 是解析的纯逻辑部分（抽出来是为了能直接测）。
func resolveIn(path, root string) string {
	p := strings.TrimSpace(path)
	if filepath.IsAbs(p) {
		return path
	}
	if root == "" {
		return path
	}
	if p == "" {
		// 空路径 = 工作区根目录。不这么处理的话 list_dir 省略 path 会列出
		// 运行时目录（core.exe 旁边），而不是用户选的那个文件夹。
		return root
	}
	return filepath.Join(root, p)
}

// workspaceRoot 问一下工作区插件当前的工作目录；不可用或未设置时返回空串。
func (f *FsTool) workspaceRoot() string {
	raw, err := f.ctx.Invoke(workspaceService, "root", map[string]any{})
	if err != nil {
		return ""
	}
	var out struct {
		Root string `json:"root"`
		Set  bool   `json:"set"`
	}
	if json.Unmarshal(raw, &out) != nil || !out.Set {
		return ""
	}
	return out.Root
}

// Activate 注册三个服务。注意：本插件全程没有 os.ReadFile 之类的直接调用。
func (f *FsTool) Activate(ctx *plugin.Context) error {
	f.ctx = ctx

	if err := ctx.Provide("readFile.v1", map[string]any{
		"description": "读取文件内容（经核心能力代理）",
		"methods":     []string{"run"},
	}); err != nil {
		return err
	}
	if err := ctx.Provide("writeFile.v1", map[string]any{
		"description": "写入文件内容（经核心能力代理）",
		"methods":     []string{"run"},
	}); err != nil {
		return err
	}
	if err := ctx.Provide("listDir.v1", map[string]any{
		"description": "列出目录条目（经核心能力代理）",
		"methods":     []string{"run"},
	}); err != nil {
		return err
	}

	ctx.Log("info", "文件工具已就绪（readFile.v1 / writeFile.v1 / listDir.v1）", map[string]any{
		"permissions": ctx.Permissions,
	})
	return nil
}

// Handle 处理 core.invoke。
func (f *FsTool) Handle(service, method string, args json.RawMessage) (any, error) {
	if method != "run" {
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%s 不支持方法 %s", service, method)
	}
	switch service {
	case "readFile.v1":
		return f.read(args)
	case "writeFile.v1":
		return f.write(args)
	case "listDir.v1":
		return f.list(args)
	default:
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "文件工具不提供服务 %s", service)
	}
}

// read 把读取意图交给核心，核心负责权限校验与真正的 IO。
func (f *FsTool) read(args json.RawMessage) (any, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	if in.Path == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 path")
	}

	resp, err := f.ctx.FSRead(f.resolve(in.Path))
	if err != nil {
		// 原样透传核心的错误码（-32002 权限不足 / -32000 等），不要改写成插件内部错误
		return nil, err
	}
	return json.RawMessage(resp), nil
}

// write 同上。
func (f *FsTool) write(args json.RawMessage) (any, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Append  bool   `json:"append"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	if in.Path == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 path")
	}

	resp, err := f.ctx.FSWrite(f.resolve(in.Path), in.Content, in.Append, true)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(resp), nil
}

// list 列出目录条目。path 为空时由核心决定基准目录（core.json 的 fsRoot，或运行时目录）。
func (f *FsTool) list(args json.RawMessage) (any, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}

	resp, err := f.ctx.FSList(f.resolve(in.Path))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(resp), nil
}

// Deactivate 清理。
func (f *FsTool) Deactivate() error {
	if f.ctx != nil {
		f.ctx.Log("info", "文件工具退出", nil)
	}
	return nil
}

func main() {
	if err := plugin.Serve(&FsTool{}); err != nil {
		plugin.Logf("error", "文件工具退出: %v", err)
		os.Exit(1)
	}
}
