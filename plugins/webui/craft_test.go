package main

import (
	"strings"
	"testing"
)

// 分隔块格式：代码内含引号 / 换行 / 类 JSON 文本也必须原样取出。
func TestParseCraftBlock(t *testing.T) {
	in := "PLUGIN-ID: com.ai.dice\n" +
		"PLUGIN-NAME: 掷骰子\n" +
		"RUNTIME: python\n" +
		"PROVIDES: dice.v1\n" +
		"CONSUMES:\n" +
		"PERMISSIONS:\n" +
		"DESCRIPTION: 掷骰子\n" +
		"CODE-BEGIN\n" +
		"print(\"hello \\\"x\\\"\")\n" +
		"# 一行注释里有 KEY: VALUE 不应干扰头部\n" +
		"CODE-END\n"
	p, err := parseCraftPlan(in)
	if err != nil {
		t.Fatalf("parseCraftPlan: %v", err)
	}
	if p.ID != "com.ai.dice" || p.Name != "掷骰子" || p.Runtime != "python" {
		t.Fatalf("头部字段错误: %+v", p)
	}
	if len(p.Provides) != 1 || p.Provides[0] != "dice.v1" {
		t.Fatalf("provides 错误: %v", p.Provides)
	}
	if len(p.Consumes) != 0 || len(p.Permissions) != 0 {
		t.Fatalf("空清单应为空切片: %+v / %+v", p.Consumes, p.Permissions)
	}
	if p.Code != "print(\"hello \\\"x\\\"\")\n# 一行注释里有 KEY: VALUE 不应干扰头部" {
		t.Fatalf("代码原文提取错误: %q", p.Code)
	}
}

// 模型输出前后带解说、字段用了奇怪写法时仍应解析成功。
func TestParseCraftBlockLoose(t *testing.T) {
	in := "好的，这就给你造：\n\nID: Weather\nNAME: 天气\nRUNTIME: Python\n" +
		"PROVIDES: weather.v1, other.v2\nPERMISSIONS: network\nCODE-BEGIN\nprint(1)\nCODE-END\n希望好用"
	p, err := parseCraftPlan(in)
	if err != nil {
		t.Fatalf("parseCraftPlan loose: %v", err)
	}
	if p.ID != "com.ai.weather" { // 单词 id 兜底为 com.ai.weather
		t.Fatalf("id 规范化错误: %q", p.ID)
	}
	if len(p.Provides) != 2 || p.Permissions[0] != "network" || p.Runtime != "python" {
		t.Fatalf("清单/运行时错误: %+v", p)
	}
}

// JSON 兼容路径：模型不听指令仍输出 JSON 时也能接受。
func TestParseCraftJSONFallback(t *testing.T) {
	in := "前缀废话\n```json\n" + `{
  "id": "com.ai.echo",
  "name": "回显",
  "runtime": "go",
  "provides": ["echo.v1"],
  "consumes": [],
  "permissions": [],
  "code": "package main\nfunc main() {}"
}` + "\n```\n后缀"
	p, err := parseCraftPlan(in)
	if err != nil {
		t.Fatalf("json fallback: %v", err)
	}
	if p.ID != "com.ai.echo" || p.Runtime != "go" || p.Code != "package main\nfunc main() {}" {
		t.Fatalf("JSON 回退解析错误: %+v", p)
	}
}

// 两种格式都没有时给出明确错误。
func TestParseCraftGarbage(t *testing.T) {
	if _, err := parseCraftPlan("我不会写插件，对不起。"); err == nil {
		t.Fatal("垃圾输入应返回错误")
	}
}

// 回炉代码必须是单个文件：多段拼接要被拦住，干净代码放行。
func TestSanitizeGoSource(t *testing.T) {
	good := "// hi\npackage main\n\nimport \"aiwork/sdk/go/plugin\"\n\n" +
		"func main() { _ = plugin.Serve(nil) }\n"
	if _, err := sanitizeGoSource("```go\n" + good + "```"); err != nil {
		t.Fatalf("干净代码被误拒: %v", err)
	}
	bad := good + "\npackage main\n\nfunc main() {}\n"
	if _, err := sanitizeGoSource(bad); err == nil {
		t.Fatal("双段拼接应被拒绝")
	}
	if _, err := sanitizeGoSource("python code here"); err == nil {
		t.Fatal("非 Go 输出应被拒绝")
	}
}

// 非法服务名（小模型爱夹中文/下划线）要派生成合法名，并同步替换源码字面量。
func TestNormalizeProvides(t *testing.T) {
	code := "const serviceName = \"weather提醒.v1\"\n"
	out, fixed := normalizeProvides([]string{"weather提醒.v1"}, "com.ai.weather", code)
	if len(out) != 1 || out[0] != "weather.v1" {
		t.Fatalf("服务名派生错误: %v", out)
	}
	if !strings.Contains(fixed, "\"weather.v1\"") || strings.Contains(fixed, "提醒") {
		t.Fatalf("源码中的服务名未同步替换:\n%s", fixed)
	}
	// 合法名原样保留；dice.roll.v1 这种多段也合法
	out2, _ := normalizeProvides([]string{"dice.roll.v1", "dice.roll.v1"}, "com.ai.dice", "")
	if len(out2) != 1 || out2[0] != "dice.roll.v1" {
		t.Fatalf("合法服务名处理错误: %v", out2)
	}
	// 声明了 v3 就保留版本号
	out3, _ := normalizeProvides([]string{"bad_name.v3"}, "com.ai.timer", "")
	if len(out3) != 1 || out3[0] != "timer.v3" {
		t.Fatalf("版本号保留错误: %v", out3)
	}
}

// 从源码方法分发分支提取真实对外方法名。
func TestExtractMethods(t *testing.T) {
	py := "def handle(method, args):\n" +
		"    if method == 'gcd':\n        return 1\n" +
		"    elif method == \"ping\":\n        return 2\n" +
		"    if method == 'gcd':\n        return 1\n" // 重复分支去重
	got := extractMethods(py, "python")
	if len(got) != 2 || got[0] != "gcd" || got[1] != "ping" {
		t.Fatalf("python 方法提取错误: %v", got)
	}
	go1 := "switch method {\ncase \"ping\":\ncase \"run\":\ndefault:\n}"
	got2 := extractMethods(go1, "go")
	if len(got2) != 2 || got2[0] != "ping" || got2[1] != "run" {
		t.Fatalf("go 方法提取错误: %v", got2)
	}
}

// 撞上已装插件的服务名：自动改成 <id 末段>.vN 并替换源码。
func TestResolveServiceCollisions(t *testing.T) {
	occupied := map[string]bool{"toolExecutor.v1": true, "gcd.v1": true}
	code := "_SERVICE = \"toolExecutor.v1\"\n"
	out, fixed, renamed := resolveServiceCollisions(
		[]string{"toolExecutor.v1"}, "com.ai.gcd", code, occupied)
	if len(out) != 1 || out[0] != "gcd2.v1" {
		t.Fatalf("撞名避让错误: %v", out)
	}
	if !strings.Contains(fixed, "\"gcd2.v1\"") || strings.Contains(fixed, "toolExecutor") {
		t.Fatalf("源码服务名未替换: %s", fixed)
	}
	if len(renamed) != 1 {
		t.Fatalf("应报告 1 条改名: %v", renamed)
	}
	// 没撞的名字原样返回
	out2, _, _ := resolveServiceCollisions([]string{"fresh.v1"}, "com.ai.fresh", "", occupied)
	if out2[0] != "fresh.v1" {
		t.Fatalf("无撞名不应改: %v", out2)
	}
}

// 代码里注册名漏了版本后缀 / 与 manifest 不一致：以 provides 为准强制对齐。
func TestEnforceRegisteredServiceName(t *testing.T) {
	py := "_SERVICE = \"math.gcd\"\n" +
		"call(\"core.provide\", {\"name\": \"math.gcd\", \"meta\": {}})\n"
	out := enforceRegisteredServiceName(py, "math.gcd.v1", "python")
	if strings.Count(out, "\"math.gcd.v1\"") != 2 || strings.Contains(out, "\"math.gcd\"\n") {
		t.Fatalf("python 注册名未对齐:\n%s", out)
	}
	go1 := "const serviceName = \"x.v1\"\nfunc f(){ ctx.Provide(\"y.v1\", nil) }"
	out2 := enforceRegisteredServiceName(go1, "gcd.v1", "go")
	if !strings.Contains(out2, "const serviceName = \"gcd.v1\"") ||
		!strings.Contains(out2, "ctx.Provide(\"gcd.v1\", nil)") {
		t.Fatalf("go 注册名未对齐:\n%s", out2)
	}
	// 名字本来就对：保持可用
	ok := "_SERVICE = \"a.v1\"\n" +
		"call(\"core.provide\", {\"name\": \"a.v1\"})\n"
	out3 := enforceRegisteredServiceName(ok, "a.v1", "python")
	if strings.Count(out3, "\"a.v1\"") != 2 {
		t.Fatalf("幂等对齐被破坏:\n%s", out3)
	}
}
