// Command workspace 是官方工作区文件插件，提供 workspace.v1。
//
// 和 fs-tool 的区别：**fs-tool 是按绝对路径读单个文件的示例，这里是"指定一个落脚文件夹，
// 然后整机哪里都能折腾"**。所有操作都经核心的能力代理执行，插件自己不碰文件系统。
//
// 工作目录（root）只是**相对路径的落脚点**：相对路径拼到它下面，绝对路径原样使用 ——
// 也就是说 C:\ D:\ E:\ 任意位置都能读写，不再有盘符 / 越界围栏。
// 唯一还兜底挡住的是核心运行时目录本身（由核心的能力代理护栏负责，删它或它的一级子目录会被拒）。
//
// 用法（终端界面的 /tool，或别的插件经 core.invoke 调用）：
//
//	/tool workspace.v1 setRoot {"path":"E:\\my-project"}   # 可选：只决定相对路径落在哪
//	/tool workspace.v1 tree
//	/tool workspace.v1 write  {"path":"notes/a.txt","content":"hello"}
//	/tool workspace.v1 read   {"path":"C:\\Users\\me\\Desktop\\a.txt"}
//	/tool workspace.v1 delete {"path":"notes","recursive":true}
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aiwork/sdk/go/plugin"
)

const serviceName = "workspace.v1"

// 递归操作的上限：防止在一个巨大的目录树上把内存和 RPC 通道撑爆。
const (
	maxWalkEntries = 2000
	maxTreeDepth   = 8
	maxTreeEntries = 400
	// maxCopyBytes 与核心单次能力调用的硬上限一致
	maxCopyBytes = 4 << 20
)

// Workspace 实现 plugin.Plugin。
type Workspace struct {
	ctx *plugin.Context

	// api 是界面这一页要用到的那几样能力（生产上注入的就是 ctx）。
	//
	// 把它单独拎出来只为一件事：单测里能换成假的，不必为了验证"点了目录会
	// 发生什么"去连真磁盘。
	api panelCtx

	// root 是当前指定的工作目录（绝对路径）。空表示还没指定。
	root string

	// panel 是这一页的界面状态（见 panel.go）。
	//
	// 界面状态数据放在插件自己这里，不放在 UI 插件里 —— 这是"插件自带界面"
	// 的全部意义：UI 插件只借一块地方给它画，其余一概不管。
	panel wsPanel
}

// ---------------------------------------------------------------------------
// 生命周期
// ---------------------------------------------------------------------------

func (w *Workspace) Activate(ctx *plugin.Context) error {
	w.ctx = ctx
	w.api = ctx

	// 上次指定的工作目录要接着用：它是用户的选择，不该每次重启都要重设一遍
	if raw, err := ctx.GetConfig("root"); err == nil && len(raw) > 0 {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			w.root = s
		}
	}

	if err := ctx.Provide(serviceName, map[string]any{
		"description": "工作区文件：在指定文件夹内创建 / 读取 / 修改 / 移动 / 复制 / 删除文件与目录",
		"methods": []string{
			"root", "setRoot", "browse", "list", "tree", "read", "write",
			"mkdir", "delete", "move", "copy", "stat",
		},
	}); err != nil {
		return err
	}

	// 自带界面：把"选工作目录"这一页挂在 UI 插件上。
	//
	// 插件提供 ui.panel.v1 就代表"我有一页自己的界面" —— UI 插件会来问
	// list / render / action，其余什么都不需要知道。
	if err := ctx.Provide(panelService, map[string]any{
		"description": "工作区自带的一页界面：浏览目录、挑工作目录（插件自己渲染）",
		"methods":     []string{"list", "render", "action", "key"},
	}); err != nil {
		ctx.Log("warn", "自带界面没能挂上（"+err.Error()+"），功能不受影响", nil)
	}

	ctx.Log("info", "工作区插件已就绪（workspace.v1）", map[string]any{
		"root":        firstNonEmpty(w.root, "（尚未指定）"),
		"permissions": ctx.Permissions,
	})
	return nil
}

func (w *Workspace) Handle(service, method string, args json.RawMessage) (any, error) {
	if service == panelService {
		return w.handlePanel(method, args)
	}
	if service != serviceName {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound, "工作区插件不提供服务 %s", service)
	}
	switch method {
	case "root":
		return w.showRoot()
	case "setRoot":
		return w.setRoot(args)
	case "browse":
		return w.browse(args)
	case "list":
		return w.list(args)
	case "tree":
		return w.tree(args)
	case "read":
		return w.read(args)
	case "write":
		return w.write(args)
	case "mkdir":
		return w.makeDir(args)
	case "delete", "remove", "rm":
		return w.remove(args)
	case "move", "rename":
		return w.move(args)
	case "copy":
		return w.copy(args)
	case "stat":
		return w.stat(args)
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%s 不支持方法 %s", service, method)
	}
}

func (w *Workspace) Deactivate() error {
	if w.ctx != nil {
		w.ctx.Log("info", "工作区插件退出", nil)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 工作目录
// ---------------------------------------------------------------------------

func (w *Workspace) showRoot() (any, error) {
	out := map[string]any{"root": w.root, "set": w.root != ""}
	if w.root == "" {
		out["hint"] = hintWhenUnset()
	}
	return out, nil
}

func hintWhenUnset() string {
	return "相对路径需要先设置工作目录：setRoot {\"path\":\"<文件夹>\"}（绝对路径可直接使用）"
}

// setRoot 指定工作目录。
//
// 校验方式是让核心去列一次这个目录：列得出来就说明它存在、是目录、且有权限。
// 存下来的是核心返回的绝对路径，之后所有相对路径都以它为基准。
func (w *Workspace) setRoot(args json.RawMessage) (any, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	root, err := w.setRootPath(in.Path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "set": true}, nil
}

// setRootPath 是 setRoot 的实体：给路径、设工作目录、返回生效后的绝对路径。
//
// 单独抽出来是因为有两处调用：一次 RPC（别的插件 / 命令行），一次自带界面上的按钮。
// 界面那边不该为了用一下自己的方法而去拼一份 JSON。
func (w *Workspace) setRootPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", plugin.Errorf(plugin.CodeInvalidParams, "缺少 path")
	}
	raw, err := w.api.FSList(path)
	if err != nil {
		return "", plugin.Errorf(plugin.CodeInvalidParams,
			"不能把 %q 作为工作目录：%v", path, err)
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", plugin.Errorf(plugin.CodeInternalError, "解析目录列表失败: %v", err)
	}
	if out.Path == "" {
		return "", plugin.Errorf(plugin.CodeInternalError, "核心未返回目录路径")
	}

	w.root = out.Path
	if err := w.api.SetConfig("root", out.Path); err != nil {
		return "", plugin.Errorf(plugin.CodeInternalError, "保存工作目录失败: %v", err)
	}
	w.api.Log("info", "工作目录已设置", map[string]any{"root": out.Path})
	return w.root, nil
}

// browse 列出任意目录，用来**挑选**工作目录。
//
// 它只读目录名、不读文件内容，空路径时交给核心的默认目录（core.json 配了 fsRoot
// 就是 fsRoot，否则是运行时目录），并额外给出"上一级"的落点，方便界面顺着走。
func (w *Workspace) browse(args json.RawMessage) (any, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	// 空路径交给核心解析：通常是运行时目录（core.json 配了 fsRoot 就是 fsRoot）
	target := strings.TrimSpace(in.Path)
	if target == "" {
		target = "."
	}
	raw, err := w.ctx.FSList(target)
	if err != nil {
		return nil, err
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析目录列表失败: %w", err)
	}
	items, err := w.listOne(out.Path)
	if err != nil {
		return nil, err
	}

	// 给界面一个"上一级"的落点；已经在根目录下就没有上一级
	parent := ""
	if out.Path != "" {
		if up := filepath.Dir(out.Path); up != out.Path {
			parent = up
		}
	}
	return map[string]any{
		"path":      out.Path,
		"parent":    parent,
		"count":     len(items),
		"entries":   items,
		"isRoot":    w.root == out.Path,
		"root":      w.root,
		"rootIsSet": w.root != "",
	}, nil
}

// ---------------------------------------------------------------------------
// 路径：相对路径落工作目录，绝对路径直通全盘
// ---------------------------------------------------------------------------

// resolve 把用户给的路径解析成绝对路径。
//
// 相对路径以工作目录（root）为基准拼出来；绝对路径原样使用 ——
// 整机任意位置（含 C/D/E 等所有盘符）都能读写，这里不再做盘符 / 越界围栏。
// 工作目录只是相对路径的落脚点；没指定它时，只有绝对路径可用。
func (w *Workspace) resolve(p string) (string, error) {
	s := strings.TrimSpace(p)
	if s == "" {
		if w.root == "" {
			return "", plugin.Errorf(plugin.CodeInvalidParams, hintWhenUnset())
		}
		return filepath.Clean(w.root), nil
	}
	if filepath.IsAbs(s) {
		return filepath.Clean(s), nil
	}
	if w.root == "" {
		return "", plugin.Errorf(plugin.CodeInvalidParams, hintWhenUnset())
	}
	return filepath.Clean(filepath.Join(w.root, s)), nil
}

// rel 返回路径相对工作目录的显示形式（用于输出，读起来比绝对路径短）。
func (w *Workspace) rel(abs string) string {
	rel, err := filepath.Rel(w.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

// ---------------------------------------------------------------------------
// 列目录 / 结构
// ---------------------------------------------------------------------------

type item struct {
	Path string `json:"path"`            // 相对工作目录，目录以 / 结尾
	Name string `json:"name"`            // 名字
	Dir  bool   `json:"dir"`             // 是否是目录
	Size int64  `json:"size"`            // 文件字节数
	Err  string `json:"error,omitempty"` // 递归时某一层读失败
}

func (w *Workspace) list(args json.RawMessage) (any, error) {
	var in struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	dir, err := w.resolve(in.Path)
	if err != nil {
		return nil, err
	}
	if !in.Recursive {
		items, err := w.listOne(dir)
		if err != nil {
			return nil, err
		}
		return map[string]any{"root": w.root, "path": w.rel(dir), "count": len(items), "entries": items}, nil
	}

	items, truncated, err := w.walk(dir)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"root":      w.root,
		"path":      w.rel(dir),
		"count":     len(items),
		"truncated": truncated,
		"entries":   items,
	}, nil
}

// listOne 列一层目录。
func (w *Workspace) listOne(dir string) ([]item, error) {
	raw, err := w.api.FSList(dir)
	if err != nil {
		return nil, err
	}
	var out struct {
		Path    string `json:"path"`
		Entries []struct {
			Name string `json:"name"`
			Dir  bool   `json:"dir"`
			Size int64  `json:"size"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析目录列表失败: %w", err)
	}
	items := make([]item, 0, len(out.Entries))
	for _, e := range out.Entries {
		name := e.Name
		if e.Dir {
			name += "/"
		}
		items = append(items, item{Path: name, Name: e.Name, Dir: e.Dir, Size: e.Size})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Dir != items[j].Dir {
			return items[i].Dir // 目录排在前面
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

// walk 深度优先遍历，带条目上限。
func (w *Workspace) walk(dir string) ([]item, bool, error) {
	var out []item
	truncated := false

	var visit func(cur, prefix string) error
	visit = func(cur, prefix string) error {
		if len(out) >= maxWalkEntries {
			truncated = true
			return nil
		}
		items, err := w.listOne(cur)
		if err != nil {
			out = append(out, item{Path: prefix, Name: filepath.Base(cur), Dir: true, Err: err.Error()})
			return nil // 单个子目录读失败不该让整个遍历失败
		}
		for _, it := range items {
			if len(out) >= maxWalkEntries {
				truncated = true
				return nil
			}
			child := it.Name
			if prefix != "" {
				child = prefix + "/" + it.Name
			}
			full := filepath.Join(cur, it.Name)
			if it.Dir {
				out = append(out, item{Path: child + "/", Name: it.Name, Dir: true})
				if err := visit(full, child); err != nil {
					return err
				}
				continue
			}
			out = append(out, item{Path: child, Name: it.Name, Size: it.Size})
		}
		return nil
	}

	if err := visit(dir, ""); err != nil {
		return nil, truncated, err
	}
	return out, truncated, nil
}

// tree 生成一棵可读的目录树文本。
func (w *Workspace) tree(args json.RawMessage) (any, error) {
	var in struct {
		Path  string `json:"path"`
		Depth int    `json:"depth"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	depth := in.Depth
	if depth <= 0 {
		depth = 3
	}
	if depth > maxTreeDepth {
		depth = maxTreeDepth
	}
	dir, err := w.resolve(in.Path)
	if err != nil {
		return nil, err
	}

	var sb strings.Builder
	count := 0
	sb.WriteString(w.rel(dir) + "\n")

	var visit func(cur, prefix string, level int)
	visit = func(cur, prefix string, level int) {
		if level > depth || count >= maxTreeEntries {
			return
		}
		items, err := w.listOne(cur)
		if err != nil {
			sb.WriteString(prefix + "└── [读取失败: " + err.Error() + "]\n")
			return
		}
		for i, it := range items {
			if count >= maxTreeEntries {
				sb.WriteString(prefix + "└── …（内容过多，已省略）\n")
				return
			}
			count++
			last := i == len(items)-1
			branch := "├── "
			next := prefix + "│   "
			if last {
				branch = "└── "
				next = prefix + "    "
			}
			sb.WriteString(prefix + branch + it.Name + "\n")
			if it.Dir {
				visit(filepath.Join(cur, it.Name), next, level+1)
			}
		}
	}
	visit(dir, "", 1)

	return map[string]any{"root": w.root, "path": w.rel(dir), "depth": depth, "tree": sb.String()}, nil
}

// ---------------------------------------------------------------------------
// 读写
// ---------------------------------------------------------------------------

func (w *Workspace) read(args json.RawMessage) (any, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Path) == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 path")
	}
	abs, err := w.resolve(in.Path)
	if err != nil {
		return nil, err
	}
	raw, err := w.ctx.FSRead(abs)
	if err != nil {
		return nil, err
	}
	var out struct {
		Path      string `json:"path"`
		Size      int64  `json:"size"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析读取结果失败: %w", err)
	}
	return map[string]any{
		"path":      w.rel(abs),
		"absPath":   abs,
		"size":      out.Size,
		"content":   out.Content,
		"truncated": out.Truncated,
	}, nil
}

func (w *Workspace) write(args json.RawMessage) (any, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Append  bool   `json:"append"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Path) == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 path")
	}
	abs, err := w.resolve(in.Path)
	if err != nil {
		return nil, err
	}
	// mkdir=true：父目录不存在就顺手建出来，写文件不该因为少一层目录而失败
	raw, err := w.ctx.FSWrite(abs, in.Content, in.Append, true)
	if err != nil {
		return nil, err
	}
	var out struct {
		Path string `json:"path"`
		Size int    `json:"size"`
	}
	_ = json.Unmarshal(raw, &out)

	verb := "写入"
	if in.Append {
		verb = "追加"
	}
	return map[string]any{
		"path":    w.rel(abs),
		"absPath": abs,
		"size":    out.Size,
		"append":  in.Append,
		"note":    fmt.Sprintf("已%s %d 字节", verb, out.Size),
	}, nil
}

func (w *Workspace) makeDir(args json.RawMessage) (any, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Path) == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 path")
	}
	abs, err := w.resolve(in.Path)
	if err != nil {
		return nil, err
	}
	raw, err := w.ctx.FSMkdir(abs, true)
	if err != nil {
		return nil, err
	}
	var out struct {
		Path    string `json:"path"`
		Created bool   `json:"created"`
	}
	_ = json.Unmarshal(raw, &out)
	note := "目录已存在"
	if out.Created {
		note = "目录已创建"
	}
	return map[string]any{
		"path":    w.rel(abs),
		"absPath": abs,
		"created": out.Created,
		"note":    note,
	}, nil
}

// ---------------------------------------------------------------------------
// 删除 / 移动 / 复制
// ---------------------------------------------------------------------------

func (w *Workspace) remove(args json.RawMessage) (any, error) {
	var in struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Path) == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 path")
	}
	abs, err := w.resolve(in.Path)
	if err != nil {
		return nil, err
	}
	// 不许把工作目录自己删了
	if samePath(abs, w.root) {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "不能删除工作目录本身：%s", w.root)
	}
	raw, err := w.ctx.FSDelete(abs, in.Recursive)
	if err != nil {
		return nil, err
	}
	var out struct {
		Path    string `json:"path"`
		Existed bool   `json:"existed"`
		Deleted bool   `json:"deleted"`
		Moved   bool   `json:"moved"`
		Pending string `json:"pending"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)

	res := map[string]any{
		"path":    w.rel(abs),
		"absPath": abs,
		"existed": out.Existed,
		"deleted": out.Deleted,
		"moved":   out.Moved,
	}
	if out.Pending != "" {
		res["pending"] = out.Pending
		res["note"] = "目标被占用，已移入待删除区，稍后清理"
	}
	if out.Error != "" {
		res["error"] = out.Error
	}
	if !out.Existed {
		res["note"] = "目标不存在，无需删除"
	}
	return res, nil
}

func (w *Workspace) move(args json.RawMessage) (any, error) {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.From) == "" || strings.TrimSpace(in.To) == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 from / to")
	}
	// 起点与终点都必须落在工作目录内
	from, err := w.resolve(in.From)
	if err != nil {
		return nil, err
	}
	to, err := w.resolve(in.To)
	if err != nil {
		return nil, err
	}
	if _, err := w.ctx.FSMove(from, to); err != nil {
		return nil, err
	}
	return map[string]any{
		"from": w.rel(from), "to": w.rel(to), "moved": true,
		"note": fmt.Sprintf("已移动 %s → %s", w.rel(from), w.rel(to)),
	}, nil
}

// copy 按文本复制一个文件。
//
// 说明白限制：内容要经 JSON 传一趟，所以只适合 UTF-8 文本。
// 二进制文件请用 move（改名不经过内容传输）。
func (w *Workspace) copy(args json.RawMessage) (any, error) {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.From) == "" || strings.TrimSpace(in.To) == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 from / to")
	}
	from, err := w.resolve(in.From)
	if err != nil {
		return nil, err
	}
	to, err := w.resolve(in.To)
	if err != nil {
		return nil, err
	}

	raw, err := w.ctx.Capability("core.fs.read", map[string]any{"path": from, "limit": maxCopyBytes})
	if err != nil {
		// 目录读不出来（core.fs.read 明确拒绝目录），给一句有用的提示
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"读取 %s 失败（%v）。copy 只支持单个文件，目录请用 move 或逐个复制", w.rel(from), err)
	}
	var src struct {
		Size      int64  `json:"size"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &src); err != nil {
		return nil, fmt.Errorf("解析读取结果失败: %w", err)
	}
	if src.Truncated {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"%s 超过 %d 字节的复制上限，请改用 move", w.rel(from), maxCopyBytes)
	}
	if _, err := w.ctx.FSWrite(to, src.Content, false, true); err != nil {
		return nil, err
	}
	return map[string]any{
		"from": w.rel(from), "to": w.rel(to), "size": src.Size, "copied": true,
		"note": "按文本复制（二进制文件请用 move）",
	}, nil
}

// stat 查看单个条目的信息。
func (w *Workspace) stat(args json.RawMessage) (any, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	abs, err := w.resolve(in.Path)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(abs)
	items, err := w.listOne(parent)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(abs)
	for _, it := range items {
		if it.Name == name {
			return map[string]any{
				"path": w.rel(abs), "absPath": abs,
				"name": it.Name, "dir": it.Dir, "size": it.Size,
			}, nil
		}
	}
	return nil, plugin.Errorf(plugin.CodeInvalidParams, "工作区里没有 %s", w.rel(abs))
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func decode(args json.RawMessage, v any) error {
	if len(args) == 0 || string(args) == "null" {
		return nil
	}
	if err := json.Unmarshal(args, v); err != nil {
		return plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func main() {
	if err := plugin.Serve(&Workspace{}); err != nil {
		plugin.Logf("error", "工作区插件退出: %v", err)
		os.Exit(1)
	}
}
