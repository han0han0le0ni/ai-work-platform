package plugin

import (
	"os"
	"strings"
)

// ---------------------------------------------------------------------------
// 配置解析（统一优先级链）
// ---------------------------------------------------------------------------
//
// 插件读一个参数时，来源往往不止一个：插件配置、AIWORK_* 环境变量、通用环境变量、
// 以及代码里的默认值。以前 llm 和 imagegen 各自写了一份 pick，行为已经长歪了（一个
// 能读数字配置、一个不能）。这里把"配置 -> 环境变量"这段收口到 SDK，默认值仍留给
// 各插件自己算 —— 因为 defaultBaseURL 这类默认值依赖 provider，不是常量。
//
// 这是 SDK 内部新增，不改核心、不改公开契约（符合 README「兼容性宪法」只增不减）。

// ResolveString 按优先级返回第一个非空字符串：先 getConfig(key)，再依次 getEnv(envs[i])。
// 每个来源都 TrimSpace，空白视为缺失。全都没命中返回空串。
//
// getConfig / getEnv 由调用方注入，便于单测不碰真实配置文件与环境变量。
func ResolveString(key string, getConfig func(string) string, getEnv func(string) string, envs ...string) string {
	if getConfig == nil || getEnv == nil {
		return ""
	}
	if v := strings.TrimSpace(getConfig(key)); v != "" {
		return v
	}
	for _, e := range envs {
		if v := strings.TrimSpace(getEnv(e)); v != "" {
			return v
		}
	}
	return ""
}

// Pick 解析字符串配置：插件配置优先，其次按顺序尝试给定的环境变量。
// 返回 TrimSpace 后的值，全都没命中返回空串；默认值由调用方自行补。
//
// 与旧的插件内 pick 相比，这一份统一走 GetConfigString，因此数字类型的配置值
// （例如把 port 写成 8090 而非 "8090"）也能正确读出，不再被 json.Unmarshal 跳过。
func (c *Context) Pick(key string, envs ...string) string {
	return ResolveString(key,
		func(k string) string { return c.GetConfigString(k, "") },
		os.Getenv,
		envs...)
}
