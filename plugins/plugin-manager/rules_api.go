package main

// rulesAPI 是「插件规则」的机器可读版本。
//
// rules.go 里那份规则只画在 TUI 上给人看；AI 造插件时需要同一份规则，
// 还需要两种语言的**完整骨架** —— 模型拿着能直接编译 / 运行的范例，
// 生成的代码一次通过率远高于只给几条文字约束。本文件把两样东西打包成
// pluginManager.v1 rules 方法的返回值，专门喂给「一键造插件」链路。

// rulesSectionDTO 是发给调用方的一段规则。
type rulesSectionDTO struct {
	Title string   `json:"title"`
	Lines []string `json:"lines"`
}

// rulesPayload 是 rules 方法的完整返回。
type rulesPayload struct {
	// Sections 是核心在注册 / 加载点强制执行的命名与 manifest 规则。
	Sections []rulesSectionDTO `json:"sections"`
	// Runtimes 是 create 目前支持的运行时。
	Runtimes []string `json:"runtimes"`
	// Skeletons 是各语言可直接运行的完整骨架源码（含全部协议样板）。
	Skeletons map[string]string `json:"skeletons"`
	// SkeletonEntry 告知每种骨架的入口文件名与产物形态。
	SkeletonEntry map[string]string `json:"skeletonEntry"`
	// Flow 是造插件的标准步骤，调用方可照着推进度。
	Flow []string `json:"flow"`
}

// rules 返回机器可读的插件开发规则与骨架。
func (m *Manager) rules() (any, error) {
	secs := make([]rulesSectionDTO, 0, len(rulesSections))
	for _, s := range rulesSections {
		secs = append(secs, rulesSectionDTO{Title: s.head, Lines: s.body})
	}
	return rulesPayload{
		Sections: secs,
		Runtimes: []string{"python", "go"},
		Skeletons: map[string]string{
			// 固定的 demo id / 服务名只是范例，模型应按实际需求替换。
			"python": pythonSkeleton("com.example.demo", "示例", []string{"demo.v1"}),
			"go":     goSkeleton("com.example.demo", []string{"demo.v1"}),
		},
		SkeletonEntry: map[string]string{
			"python": "main.py（无需编译，重启核心即加载）",
			"go":     "main.go（须先调 builder.v1 build 编译出 main 可执行文件，再重启）",
		},
		Flow: []string{
			"1. 按规则确定 id（反向域名）、服务名（带 .vN 后缀）、权限名（小写冒号）",
			"2. 参照对应语言骨架生成完整入口源码，只改业务逻辑，协议样板原样保留",
			"3. 调 pluginManager.v1 create 写入插件目录与 manifest",
			"4. Go 插件调 builder.v1 build 编译；编译失败按 stderr 修源码后重试",
			"5. 重启核心，插件自动加载并注册服务",
		},
	}, nil
}
