package main

import "encoding/json"

// defaultCatalog 是出厂工具目录：把已经存在的插件服务桥接成「模型看得懂的工具」。
//
// 注意这个目录本身不实现任何能力，它只是翻译层：
// 工具插件装没装，由 list 返回的 available 字段如实反映。
// 模型看到 available=false 的工具时，会（也应该）改用别的办法。
//
// 新增一个工具的最小代价是往这里加一项；不想改代码，就用 toolExecutor.v1 的 register 方法。
func defaultCatalog() []Tool {
	return []Tool{
		{
			Name:        "read_file",
			Description: "读取一个文本文件的内容。需要文件确实存在；目录请改用 list_dir。",
			Service:     "readFile.v1",
			Method:      "run",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "文件路径。相对路径按用户当前的工作区解析；绝对路径原样使用"}
  },
  "required": ["path"]
}`),
		},
		{
			Name:        "write_file",
			Description: "把文本写入文件。默认覆盖整个文件；append=true 时追加到末尾。父目录不存在会自动创建。",
			Service:     "writeFile.v1",
			Method:      "run",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "文件路径。相对路径会落到用户当前的工作区里，不要拼绝对路径"},
    "content": {"type": "string", "description": "要写入的文本"},
    "append": {"type": "boolean", "description": "是否追加而不是覆盖，默认 false"}
  },
  "required": ["path", "content"]
}`),
		},
		{
			Name:        "list_dir",
			Description: "列出目录下的条目（名称、是否子目录、大小）。",
			Service:     "listDir.v1",
			Method:      "run",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "目录路径，省略时为用户当前的工作区"}
  },
  "required": []
}`),
		},
		{
			Name:        "now",
			Description: "返回当前的日期与时间。回答涉及「今天」「现在」的问题时用它，不要凭记忆猜。",
			Builtin:     true,
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "required": []
}`),
		},
	}
}
