package loader

import (
	"fmt"

	"aiwork/core/jsonrpc"
)

// Skip 记录一个被跳过的插件及原因。
//
// Provides 是该插件原本声明的服务，Code 是对应的标准错误码：
// 消费者调用这些服务时，核心会返回精确原因（-32004 依赖缺失 / -32005 版本不兼容），
// 而不是笼统的 -32000 服务未找到。
type Skip struct {
	Dir      string
	ID       string
	Reason   string
	Provides []string
	Code     int
}

// Plan 是插件启动计划。
type Plan struct {
	Order   []Discovery // 拓扑序：被依赖者在前
	Skipped []Skip
}

type planNode struct {
	d    Discovery
	dead bool
	why  string
	code int
}

// BuildPlan 校验 apiVersion 与核心版本兼容性、检查依赖缺失与版本范围、检测循环依赖，
// 并给出拓扑排序后的启动顺序。缺失依赖的插件会被跳过并给出警告。
func BuildPlan(discs []Discovery, coreVersion, apiVersion string) *Plan {
	plan := &Plan{}
	byID := map[string]*planNode{}
	var nodes []*planNode

	kill := func(n *planNode, code int, format string, args ...any) {
		n.dead = true
		n.code = code
		n.why = fmt.Sprintf(format, args...)
	}

	for _, d := range discs {
		if d.Err != nil || d.Manifest == nil {
			reason := "manifest 无效"
			if d.Err != nil {
				reason = "manifest 无效: " + d.Err.Error()
			}
			plan.Skipped = append(plan.Skipped, Skip{Dir: d.Dir, Reason: reason, Code: jsonrpc.CodeProviderDead})
			continue
		}
		m := d.Manifest
		n := &planNode{d: d}
		nodes = append(nodes, n)

		if m.APIVersion != apiVersion {
			kill(n, jsonrpc.CodeAPIVersionIncompatible,
				"apiVersion 不兼容: 核心为 %s，插件为 %s", apiVersion, m.APIVersion)
			continue
		}
		ok, err := MatchRange(coreVersion, m.Compat.Core)
		if err != nil {
			kill(n, jsonrpc.CodeAPIVersionIncompatible, "compat.core %q 无法解析: %v", m.Compat.Core, err)
			continue
		}
		if !ok {
			kill(n, jsonrpc.CodeAPIVersionIncompatible,
				"compat.core %q 与核心版本 %s 不兼容", m.Compat.Core, coreVersion)
			continue
		}
		if prev, dup := byID[m.ID]; dup {
			kill(n, jsonrpc.CodeProviderDead, "插件 ID 与 %s 重复", prev.d.Dir)
			continue
		}
		byID[m.ID] = n
	}

	// 依赖缺失 / 版本不满足
	for _, n := range nodes {
		if n.dead {
			continue
		}
		for depID, rng := range n.d.Manifest.Dependencies {
			dep, ok := byID[depID]
			if !ok {
				kill(n, jsonrpc.CodeDependencyMissing, "依赖缺失: %s", depID)
				break
			}
			fit, err := MatchRange(dep.d.Manifest.Version, rng)
			if err != nil || !fit {
				kill(n, jsonrpc.CodeDependencyMissing,
					"依赖版本不满足: %s 需要 %q，实际 %s", depID, rng, dep.d.Manifest.Version)
				break
			}
		}
	}

	// 级联禁用：依赖被禁用的插件同样不可用
	for changed := true; changed; {
		changed = false
		for _, n := range nodes {
			if n.dead {
				continue
			}
			for depID := range n.d.Manifest.Dependencies {
				dep, ok := byID[depID]
				if !ok || dep.dead {
					kill(n, jsonrpc.CodeDependencyMissing, "依赖不可用: %s", depID)
					changed = true
					break
				}
			}
		}
	}

	// 拓扑排序（Kahn），剩余未处理者即为循环依赖
	indeg := map[*planNode]int{}
	for _, n := range nodes {
		if !n.dead {
			indeg[n] = 0
		}
	}
	for _, n := range nodes {
		if n.dead {
			continue
		}
		for depID := range n.d.Manifest.Dependencies {
			if dep, ok := byID[depID]; ok && !dep.dead {
				indeg[n]++
			}
		}
	}

	var queue []*planNode
	for _, n := range nodes {
		if !n.dead && indeg[n] == 0 {
			queue = append(queue, n)
		}
	}
	processed := map[*planNode]bool{}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		processed[n] = true
		plan.Order = append(plan.Order, n.d)
		for _, m := range nodes {
			if m.dead || processed[m] {
				continue
			}
			if _, dep := m.d.Manifest.Dependencies[n.d.Manifest.ID]; dep {
				indeg[m]--
				if indeg[m] == 0 {
					queue = append(queue, m)
				}
			}
		}
	}
	for _, n := range nodes {
		if !n.dead && !processed[n] {
			kill(n, jsonrpc.CodeDependencyMissing, "检测到循环依赖")
		}
	}

	for _, n := range nodes {
		if !n.dead {
			continue
		}
		plan.Skipped = append(plan.Skipped, Skip{
			Dir:      n.d.Dir,
			ID:       n.d.Manifest.ID,
			Reason:   n.why,
			Provides: n.d.Manifest.Provides,
			Code:     n.code,
		})
	}
	return plan
}
