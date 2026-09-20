package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
)

// osc52Max 是 base64 之后的长度上限。
//
// 走 OSC 52 就是把整段 base64 塞进一条转义序列发给终端，多数终端对单条序列
// 有长度限制（常见是 8KB 到 100KB）。超过就返回空串，让调用方改走"导出成文件"。
const osc52Max = 74000

// osc52 生成把文本写入系统剪贴板的转义序列；内容过长或为空时返回空串。
//
// 用 OSC 52 而不是找 pbcopy/xclip：那些工具在有还是没有、叫什么名字上
// 因平台而异，而 OSC 52 只要终端支持就行 —— 终端自己会把内容交给系统剪贴板，
// 插件既不需要权限也不需要子进程。
func osc52(s string) string {
	if s == "" {
		return ""
	}
	b := base64.StdEncoding.EncodeToString([]byte(s))
	if len(b) > osc52Max {
		return ""
	}
	// tmux 会把未知的 OSC 序列吞掉，必须用它的 passthrough 包一层
	if os.Getenv("TMUX") != "" {
		return "\033Ptmux;\033\033]52;c;" + b + "\a\033\\"
	}
	return "\033]52;c;" + b + "\a"
}

// jsonUnmarshal 是 encoding/json 的薄封装，供 humanize 使用。
func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

// maskForDisplay 本地打码，用于保存成功后的即时确认。
// 服务端（llm.v1 providers）返回的打码值随后会刷回来，两者格式一致。
func maskForDisplay(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// prettyJSONAny 把任意值格式化成缩进 JSON。
func prettyJSONAny(v any) string {
	blob, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(blob)
}
