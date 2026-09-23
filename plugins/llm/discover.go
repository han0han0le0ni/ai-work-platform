package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// discover：扫描本机，看看这台机器上跑着什么 AI 服务
//
// 「本机有没有 AI」是接本地模型时要回答的第一个问题，而答案只在端口上 ——
// 所以这里把常见本地推理服务的出厂默认端点挨个探一遍，探到什么就回报什么。
//
// 三条自我约束：
//  1. 探测一律走核心的能力代理（core.net.request），插件自己不碰 socket；
//  2. 每个候选只问一个"列模型"的只读端点，不做推理、不改配置，扫描无副作用；
//  3. 只在回环地址上探，绝不主动去扫内网或公网 —— 那是另一件事，也不该由扫描代劳。
//
// 换了端口、或者跑的是表里没有的东西，交给界面上的「自定义」——
// 扫描只是"帮你少打一遍字"，不是唯一入口。
// ---------------------------------------------------------------------------

// localCandidate 是一个"本机可能跑着 AI 服务"的候选端点。
type localCandidate struct {
	Key      string // 稳定标识：界面把它回传，用来"连接"这个服务
	Label    string // 给用户看的名字
	Provider string // 该用哪套协议跟它说话：openai / ollama
	BaseURL  string // 基础地址（不含探测路径）
	Probe    string // 探测路径：/v1/models 或 /api/tags
	Note     string // 一句话说明它是什么
}

// randomProbe 与 defaultProbeTimeoutMs 是探测参数。
//
// 超时给得很短：本机回环上"没在监听"是立刻返回连接被拒的，
// 只有防火墙丢包之类才会走到超时。宁可把某个慢服务漏掉，也别让扫描把界面卡住。
const (
	defaultProbeTimeoutMs = 900
)

// localCandidates 是常见本地 AI 服务的出厂默认端点表。
func localCandidates() []localCandidate {
	return []localCandidate{
		{Key: "ollama", Label: "Ollama", Provider: "ollama",
			BaseURL: "http://127.0.0.1:11434", Probe: "/api/tags",
			Note: "本地模型运行时，走原生 /api 接口"},
		{Key: "lmstudio", Label: "LM Studio", Provider: "openai",
			BaseURL: "http://127.0.0.1:1234/v1", Probe: "/models",
			Note: "桌面模型管理器，走 OpenAI 兼容接口"},
		{Key: "vllm", Label: "vLLM", Provider: "openai",
			BaseURL: "http://127.0.0.1:8000/v1", Probe: "/models",
			Note: "高吞吐推理服务"},
		{Key: "llamacpp", Label: "llama.cpp", Provider: "openai",
			BaseURL: "http://127.0.0.1:8080/v1", Probe: "/models",
			Note: "llama.cpp / llamafile / LocalAI 默认都是 8080"},
		{Key: "textgen", Label: "text-generation-webui", Provider: "openai",
			BaseURL: "http://127.0.0.1:5000/v1", Probe: "/models"},
		{Key: "koboldcpp", Label: "KoboldCpp", Provider: "openai",
			BaseURL: "http://127.0.0.1:5001/v1", Probe: "/models"},
		{Key: "jan", Label: "Jan", Provider: "openai",
			BaseURL: "http://127.0.0.1:1337/v1", Probe: "/models"},
		{Key: "gpt4all", Label: "GPT4All", Provider: "openai",
			BaseURL: "http://127.0.0.1:4891/v1", Probe: "/models"},
		{Key: "sglang", Label: "SGLang", Provider: "openai",
			BaseURL: "http://127.0.0.1:30000/v1", Probe: "/models"},
		{Key: "xinference", Label: "Xinference", Provider: "openai",
			BaseURL: "http://127.0.0.1:9997/v1", Probe: "/models"},
		{Key: "aphrodite", Label: "其它 OpenAI 兼容服务", Provider: "openai",
			BaseURL: "http://127.0.0.1:2242/v1", Probe: "/models",
			Note: "Aphrodite 等第三方兼容服务的常用端口"},
	}
}

// localHit 是扫描结果里的一条。
//
// 字段全大写是因为它要走 JSON 回给界面（见 workbench 的 localHit）。
type localHit struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Provider string   `json:"provider"`
	BaseURL  string   `json:"baseUrl"`
	Alive    bool     `json:"alive"`
	Models   []string `json:"models,omitempty"`
	Note     string   `json:"note,omitempty"`
	MS       int64    `json:"ms,omitempty"`
}

type discoverParams struct {
	// TimeoutMs 是单个端点的探测超时，不传就用默认值。
	TimeoutMs int `json:"timeoutMs,omitempty"`
}

// discover 并行探测本机常见 AI 端点，只返回"确实在跑"的那些。
//
// 之所以并行：串行探十来个端点，每个都要等一次连接被拒或超时，
// 最坏情况能拖到十几秒 —— 而用户点的是「扫描本机」，不该盯着转圈。
func (l *LLM) discover(args json.RawMessage) (any, error) {
	var in discoverParams
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in) // 参数可选：解析失败就用默认值
	}
	timeout := in.TimeoutMs
	if timeout <= 0 {
		timeout = defaultProbeTimeoutMs
	}

	cands := localCandidates()
	hits := make([]localHit, len(cands))
	var wg sync.WaitGroup
	for i, cand := range cands {
		wg.Add(1)
		go func(i int, cand localCandidate) {
			defer wg.Done()
			hits[i] = l.probeLocal(cand, timeout)
		}(i, cand)
	}
	wg.Wait()

	found := make([]localHit, 0, len(hits))
	for _, h := range hits {
		if h.Alive {
			found = append(found, h)
		}
	}

	note := discoverNote(len(cands), found)
	l.ctx.Log("info", "扫描本机 AI 服务", map[string]any{
		"scanned": len(cands), "found": len(found),
	})
	return map[string]any{
		"scanned": len(cands),
		"found":   found,
		"note":    note,
	}, nil
}

func discoverNote(scanned int, found []localHit) string {
	if len(found) == 0 {
		return fmt.Sprintf("扫描了 %d 个常见本地端点，一个都没发现 —— "+
			"下面「自定义本地服务」里可以手动填地址与模型", scanned)
	}
	names := make([]string, 0, len(found))
	for _, h := range found {
		names = append(names, h.Label)
	}
	return fmt.Sprintf("扫描了 %d 个常见本地端点，发现 %d 个在运行：%s",
		scanned, len(found), strings.Join(names, "、"))
}

// probeLocal 探一个候选端点。连不上就是"不存在"，这不是错误 ——
// 扫描的意义就在于"大部分端口是空的"。
func (l *LLM) probeLocal(cand localCandidate, timeoutMs int) localHit {
	hit := localHit{
		Key:      cand.Key,
		Label:    cand.Label,
		Provider: cand.Provider,
		BaseURL:  cand.BaseURL,
	}
	start := time.Now()
	status, body, err := l.doRequest("GET", joinedURL(cand.BaseURL, cand.Probe), nil, nil, timeoutMs)
	hit.MS = time.Since(start).Milliseconds()
	if err != nil {
		return hit
	}
	hit.Alive, hit.Models, hit.Note = probeVerdict(status, body)
	if hit.Alive && cand.Note != "" {
		hit.Note = strings.TrimSpace(strings.Join([]string{hit.Note, cand.Note}, " · "))
		hit.Note = strings.TrimPrefix(hit.Note, " · ")
	}
	return hit
}

// probeVerdict 判断一次探测的回应意味着什么。
//
// 只认三种"确实是个 AI 服务"的信号：
//   - 2xx 且是合法 JSON：它按接口的规矩回了（有没有模型名是另一回事）
//   - 401 / 403：它认得这个接口，只是要密钥（LM Studio 的开关、vLLM 的 --api-key）
//
// 别的状态码一律当成"不是它"：本机上 8000 / 8080 这类端口太常见，
// 一个随便的网页服务回 200 + HTML 就会被误认成模型服务 —— 那比漏报更糟。
func probeVerdict(status int, body string) (alive bool, models []string, note string) {
	switch {
	case status == 401 || status == 403:
		return true, nil, fmt.Sprintf("服务在，但要求密钥（HTTP %d）", status)
	case status >= 200 && status < 300:
		if !json.Valid([]byte(strings.TrimSpace(body))) {
			return false, nil, ""
		}
		list := parseModelList(body)
		if len(list) == 0 {
			return true, nil, "服务在，但没报出模型名（可手填）"
		}
		return true, list, ""
	default:
		return false, nil, ""
	}
}

// joinedURL 拼出探测地址。BaseURL 末尾可能带斜杠，去掉再拼，避免 //models。
func joinedURL(base, path string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/") + "/" + strings.TrimLeft(path, "/")
}
