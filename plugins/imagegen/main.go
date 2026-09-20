// Command imagegen 是官方文生图插件（provides: imagegen.v1）。
//
// 它是**独立插件**，刻意不塞进 llm：llm 管"文字进、文字出"，这里管"文字进、图片出"，
// 两者的服务商、模型、密钥、计费方式都不一样。混在一起只会让两边都变糊。
//
// 对接的是 OpenAI 兼容的 POST /images/generations，所以这些都能用：
//
//	OpenAI      (dall-e-3)
//	阿里通义万相 (wanx-v1)
//	SiliconFlow (Kwai-Kolors/Kolors)
//	智谱        (cogview-3)
//	本地 SD WebUI / ComfyUI 的兼容层
//
// 生成的图片经 core.fs.write(base64) 落盘，然后返回**绝对路径** ——
// 界面认路径就会把它画成缩略图，所以"AI 生图"到"你看得见图"是自动接上的。
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"aiwork/sdk/go/plugin"
)

const (
	serviceName = "imagegen.v1"
	wsService   = "workspace.v1"
	outDirName  = "generated-images"

	// 主流图像接口的服务商预设：选中即填好地址与默认模型
	defaultOpenAIBase = "https://api.openai.com/v1"
)

type settings struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey"`
	Dir      string `json:"dir,omitempty"` // 输出目录，空 = 工作区/generated-images
	Size     string `json:"size,omitempty"`
}

// Provider 实现 plugin.Plugin。
type Provider struct{ ctx *plugin.Context }

func (p *Provider) Activate(ctx *plugin.Context) error {
	p.ctx = ctx
	if err := ctx.Provide(serviceName, map[string]any{
		"description": "文生图：prompt 进，图片路径出",
		"methods":     []string{"generate", "configure", "providers", "registerTool"},
	}); err != nil {
		return err
	}
	ctx.Log("info", "生图插件已就绪", map[string]any{
		"provider": p.settings().Provider, "model": p.settings().Model,
		"hasKey": p.settings().APIKey != "",
	})
	return nil
}

func (p *Provider) Deactivate() error { return nil }

func (p *Provider) Handle(service, method string, args json.RawMessage) (any, error) {
	switch method {
	case "generate":
		return p.generate(args)
	case "configure":
		return p.configure(args)
	case "providers":
		return p.providers(), nil
	case "registerTool":
		return p.registerTool()
	default:
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "%s 不支持方法 %s", serviceName, method)
	}
}

// ---------------------------------------------------------------------------
// 配置
// ---------------------------------------------------------------------------

func (p *Provider) settings() settings {
	s := settings{
		Provider: strings.ToLower(strings.TrimSpace(p.pick("provider", "AIWORK_IMAGE_PROVIDER"))),
		BaseURL:  strings.TrimSpace(p.pick("baseUrl", "AIWORK_IMAGE_BASE_URL")),
		Model:    strings.TrimSpace(p.pick("model", "AIWORK_IMAGE_MODEL")),
		APIKey:   strings.TrimSpace(p.pick("apiKey", "AIWORK_IMAGE_API_KEY")),
		Dir:      strings.TrimSpace(p.ctx.GetConfigString("dir", "")),
		Size:     strings.TrimSpace(p.ctx.GetConfigString("size", "")),
	}
	if s.Provider == "" && s.APIKey != "" {
		s.Provider = "openai"
	}
	if s.BaseURL == "" {
		s.BaseURL = defaultBaseURL(s.Provider)
	}
	if s.Model == "" {
		s.Model = defaultModel(s.Provider)
	}
	if s.Size == "" {
		s.Size = "1024x1024"
	}
	return s
}

// pick 按顺序找第一个非空来源：插件配置 -> 环境变量。
func (p *Provider) pick(key string, envs ...string) string {
	if v, err := p.ctx.GetConfig(key); err == nil && len(v) > 0 {
		var s string
		if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
			return s
		}
	}
	for _, e := range envs {
		if v := os.Getenv(e); v != "" {
			return v
		}
	}
	return ""
}

func defaultBaseURL(provider string) string {
	if provider == "openai" || provider == "" {
		return defaultOpenAIBase
	}
	return ""
}

func defaultModel(provider string) string {
	switch provider {
	case "wanx", "dashscope":
		return "wanx-v1"
	case "siliconflow":
		return "Kwai-Kolors/Kolors"
	case "zhipu", "bigmodel":
		return "cogview-3"
	default:
		return "dall-e-3"
	}
}

func (p *Provider) providers() map[string]any {
	s := p.settings()
	return map[string]any{
		"current": s.Provider,
		"available": []map[string]any{
			{"name": "openai", "baseUrl": defaultOpenAIBase, "model": "dall-e-3",
				"note": "OpenAI 官方"},
			{"name": "wanx", "baseUrl": "https://dashscope.aliyuncs.com/compatible-mode/v1",
				"model": "wanx-v1", "note": "阿里通义万相（国内直连）"},
			{"name": "siliconflow", "baseUrl": "https://api.siliconflow.cn/v1",
				"model": "Kwai-Kolors/Kolors", "note": "SiliconFlow 聚合（有免费额度）"},
			{"name": "zhipu", "baseUrl": "https://open.bigmodel.cn/api/paas/v4",
				"model": "cogview-3", "note": "智谱 CogView"},
			{"name": "local", "baseUrl": "http://127.0.0.1:7860/v1", "model": "sd",
				"note": "本地 SD WebUI / ComfyUI 兼容层"},
		},
		"config": map[string]any{
			"provider": s.Provider,
			"baseUrl":  s.BaseURL,
			"model":    s.Model,
			"apiKey":   maskKey(s.APIKey),
			"dir":      s.Dir,
			"size":     s.Size,
		},
	}
}

func (p *Provider) configure(args json.RawMessage) (any, error) {
	var in struct {
		Provider *string `json:"provider,omitempty"`
		BaseURL  *string `json:"baseUrl,omitempty"`
		Model    *string `json:"model,omitempty"`
		APIKey   *string `json:"apiKey,omitempty"`
		Dir      *string `json:"dir,omitempty"`
		Size     *string `json:"size,omitempty"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	type kv struct {
		key   string
		value any
	}
	var updates []kv
	add := func(key string, v *string) {
		if v != nil {
			updates = append(updates, kv{key, strings.TrimSpace(*v)})
		}
	}
	add("provider", in.Provider)
	add("baseUrl", in.BaseURL)
	add("model", in.Model)
	add("apiKey", in.APIKey)
	add("dir", in.Dir)
	add("size", in.Size)
	if len(updates) == 0 {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"没有要修改的字段（可用：provider / baseUrl / model / apiKey / dir / size）")
	}
	for _, u := range updates {
		if err := p.ctx.SetConfig(u.key, u.value); err != nil {
			return nil, plugin.Errorf(plugin.CodeInternalError, "写入配置 %s 失败: %v", u.key, err)
		}
	}
	s := p.settings()
	p.ctx.Log("info", "生图配置已更新", map[string]any{
		"provider": s.Provider, "model": s.Model, "hasKey": s.APIKey != "",
	})
	return map[string]any{
		"ok": true,
		"current": map[string]any{
			"provider": s.Provider, "baseUrl": s.BaseURL, "model": s.Model,
			"apiKey": maskKey(s.APIKey), "size": s.Size,
		},
	}, nil
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 6 {
		return "****"
	}
	return k[:4] + "****" + k[len(k)-4:]
}

// ---------------------------------------------------------------------------
// 生成
// ---------------------------------------------------------------------------

func (p *Provider) generate(args json.RawMessage) (any, error) {
	var in struct {
		Prompt string `json:"prompt"`
		Size   string `json:"size,omitempty"`
		Model  string `json:"model,omitempty"`
		N      int    `json:"n,omitempty"`
		Dir    string `json:"dir,omitempty"`
		Name   string `json:"name,omitempty"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "参数非法: %v", err)
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams, "缺少 prompt")
	}
	cfg := p.settings()
	if cfg.APIKey == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"还没配密钥。用 imagegen.v1 configure 填 apiKey，"+
				"或设环境变量 AIWORK_IMAGE_API_KEY")
	}
	if cfg.BaseURL == "" {
		return nil, plugin.Errorf(plugin.CodeInvalidParams,
			"provider %q 没有默认地址，请在 configure 里给 baseUrl", cfg.Provider)
	}
	if in.Model != "" {
		cfg.Model = in.Model
	}
	if in.Size != "" {
		cfg.Size = in.Size
	}
	n := in.N
	if n <= 0 {
		n = 1
	}
	if n > 4 {
		n = 4 // 一次最多四张：再多既费钱又没人看
	}

	body := map[string]any{
		"model":  cfg.Model,
		"prompt": prompt,
		"n":      n,
		"size":   cfg.Size,
		// 优先要 base64：URL 形式还得再下载一次，多一跳就多一处失败
		"response_format": "b64_json",
	}
	blob, _ := json.Marshal(body)
	resp, err := p.post(cfg, "/images/generations", blob)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &payload); err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"解析生图响应失败: %v；原始响应：%s", err, clip(string(resp), 300))
	}
	if payload.Error != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "生图失败：%s", payload.Error.Message)
	}
	if len(payload.Data) == 0 {
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"接口没有返回图片：%s", clip(string(resp), 300))
	}

	dir := strings.TrimSpace(in.Dir)
	if dir == "" {
		dir = cfg.Dir
	}
	outDir := p.outputDir(dir)

	var saved []string
	for i, d := range payload.Data {
		data, err := p.fetchImage(cfg, d.B64JSON, d.URL)
		if err != nil {
			return nil, err
		}
		name := attachName(in.Name, prompt, i, n)
		path := joinPath(outDir, name)
		raw, err := p.ctx.FSWriteBytes(path, data, true)
		if err != nil {
			return nil, plugin.Errorf(plugin.CodeInternalError, "保存图片失败: %v", err)
		}
		var wr struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
		}
		_ = json.Unmarshal(raw, &wr)
		saved = append(saved, wr.Path)
	}

	p.ctx.Log("info", fmt.Sprintf("已生成 %d 张图", len(saved)), map[string]any{
		"paths": saved, "model": cfg.Model,
	})
	return map[string]any{
		"ok":      true,
		"model":   cfg.Model,
		"prompt":  prompt,
		"paths":   saved,
		"count":   len(saved),
		"message": "已保存到：" + strings.Join(saved, "、"),
	}, nil
}

// fetchImage 拿到图片字节：接口给 base64 就直接解码，只给 URL 就再下一次。
func (p *Provider) fetchImage(cfg settings, b64, url string) ([]byte, error) {
	if strings.TrimSpace(b64) != "" {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, plugin.Errorf(plugin.CodeInternalError, "图片 base64 解码失败: %v", err)
		}
		return data, nil
	}
	if strings.TrimSpace(url) == "" {
		return nil, plugin.Errorf(plugin.CodeInternalError, "接口既没给 b64_json 也没给 url")
	}
	// 走核心的网络代理，并要求 base64 —— 普通返回是字符串，会把 PNG 弄坏
	raw, err := p.ctx.Capability("core.net.request", map[string]any{
		"method": "GET", "url": url, "encoding": "base64",
	})
	if err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "下载图片失败: %v", err)
	}
	var out struct {
		Status    int    `json:"status"`
		Body      string `json:"body"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "解析下载结果失败: %v", err)
	}
	if out.Status < 200 || out.Status >= 300 {
		return nil, plugin.Errorf(plugin.CodeInternalError, "下载图片返回 HTTP %d", out.Status)
	}
	if out.Truncated {
		return nil, plugin.Errorf(plugin.CodeInternalError, "图片太大，下载不完整")
	}
	data, err := base64.StdEncoding.DecodeString(out.Body)
	if err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "下载内容不是合法 base64: %v", err)
	}
	return data, nil
}

// post 走核心的网络代理发一次 JSON 请求。
func (p *Provider) post(cfg settings, path string, body []byte) ([]byte, error) {
	raw, err := p.ctx.Capability("core.net.request", map[string]any{
		"method": "POST",
		"url":    strings.TrimRight(cfg.BaseURL, "/") + path,
		"headers": map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + cfg.APIKey,
		},
		"body": string(body),
	})
	if err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "请求生图接口失败: %v", err)
	}
	var out struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "解析响应失败: %v", err)
	}
	if out.Status < 200 || out.Status >= 300 {
		return nil, plugin.Errorf(plugin.CodeInternalError,
			"生图接口返回 HTTP %d：%s", out.Status, clip(out.Body, 300))
	}
	return []byte(out.Body), nil
}

// outputDir 决定图片存哪儿：优先工作区，这样用户在自己的目录里就能看到。
func (p *Provider) outputDir(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if root := p.workspaceRoot(); root != "" {
		return joinPath(root, outDirName)
	}
	return outDirName // 相对运行时目录
}

// workspaceRoot 问一下 workspace.v1 当前的工作区；没装或没设就返回空。
func (p *Provider) workspaceRoot() string {
	if _, err := p.ctx.Consume(wsService); err != nil {
		return ""
	}
	raw, err := p.ctx.Invoke(wsService, "browse", map[string]any{})
	if err != nil {
		return ""
	}
	var out struct {
		Root string `json:"root"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	return strings.TrimSpace(out.Root)
}

// attachName 给生成的图起个能认出来的名字：带时间戳，避免互相覆盖。
func attachName(given, prompt string, idx, total int) string {
	base := strings.TrimSpace(given)
	if base == "" {
		base = slug(prompt)
	}
	if base == "" {
		base = "image"
	}
	if idx > 0 || total > 1 {
		base = fmt.Sprintf("%s-%d", base, idx+1)
	}
	return fmt.Sprintf("%s-%s.png", base, time.Now().Format("20060102-150405"))
}

// slug 把 prompt 压成一小段可做文件名的 ASCII；全是中文就返回空（用默认名）。
func slug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
		if b.Len() >= 24 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func joinPath(dir, name string) string {
	if strings.HasSuffix(dir, "/") || strings.HasSuffix(dir, `\`) {
		return dir + name
	}
	return dir + "/" + name
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------------------------------------------------------------------------
// 让 AI 能自己调
// ---------------------------------------------------------------------------

// registerTool 把生图能力登记进工具目录，这样模型可以自己决定"我要画一张"。
func (p *Provider) registerTool() (any, error) {
	if _, err := p.ctx.Consume("toolExecutor.v1"); err != nil {
		return nil, plugin.Errorf(plugin.CodeServiceNotFound,
			"需要 toolExecutor.v1 才能注册工具")
	}
	raw, err := p.ctx.Invoke("toolExecutor.v1", "register", map[string]any{
		"name":        "generate_image",
		"description": "根据文字描述生成图片，返回保存好的文件路径。需要画图、配图、生成插图时用它。",
		"service":     serviceName,
		"method":      "generate",
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{"type": "string", "description": "图片内容的文字描述，越具体越好"},
				"size":   map[string]any{"type": "string", "description": "尺寸，如 1024x1024"},
				"n":      map[string]any{"type": "integer", "description": "生成几张，默认 1，最多 4"},
			},
			"required": []string{"prompt"},
		},
	})
	if err != nil {
		return nil, plugin.Errorf(plugin.CodeInternalError, "注册工具失败: %v", err)
	}
	return map[string]any{"ok": true, "tool": "generate_image", "raw": json.RawMessage(raw)}, nil
}

func main() { plugin.Serve(&Provider{}) }
