package plugin

import (
	"testing"
)

func TestResolveString(t *testing.T) {
	cfg := map[string]string{}
	env := map[string]string{}
	getConfig := func(k string) string { return cfg[k] }
	getEnv := func(k string) string { return env[k] }

	t.Run("配置优先于环境变量", func(t *testing.T) {
		cfg["baseUrl"] = "http://config"
		env["ENV_URL"] = "http://env"
		got := ResolveString("baseUrl", getConfig, getEnv, "ENV_URL")
		if got != "http://config" {
			t.Fatalf("期望配置值 http://config，得到 %q", got)
		}
	})

	t.Run("配置为空时依次回退环境变量", func(t *testing.T) {
		cfg["baseUrl"] = ""
		env["FIRST"] = ""
		env["SECOND"] = "http://second"
		env["THIRD"] = "http://third"
		got := ResolveString("baseUrl", getConfig, getEnv, "FIRST", "SECOND", "THIRD")
		if got != "http://second" {
			t.Fatalf("期望第二个非空环境变量 http://second，得到 %q", got)
		}
	})

	t.Run("全空返回空串", func(t *testing.T) {
		cfg["baseUrl"] = ""
		env["FIRST"] = ""
		got := ResolveString("baseUrl", getConfig, getEnv, "FIRST")
		if got != "" {
			t.Fatalf("期望空串，得到 %q", got)
		}
	})

	t.Run("空白来源视为缺失", func(t *testing.T) {
		cfg["baseUrl"] = "   "
		env["FIRST"] = "\t\n"
		env["SECOND"] = "  http://trimmed  "
		got := ResolveString("baseUrl", getConfig, getEnv, "FIRST", "SECOND")
		if got != "http://trimmed" {
			t.Fatalf("期望 trim 后的 http://trimmed，得到 %q", got)
		}
	})

	t.Run("数字类型配置值可读出", func(t *testing.T) {
		// 这是旧 imagegen.pick 读不到的场景：GetConfigString 会把 JSON 数字转成字符串。
		cfg["port"] = "8090"
		got := ResolveString("port", getConfig, getEnv)
		if got != "8090" {
			t.Fatalf("期望数字配置值 8090，得到 %q", got)
		}
	})

	t.Run("nil getter 安全", func(t *testing.T) {
		if got := ResolveString("k", nil, nil, "E"); got != "" {
			t.Fatalf("期望空串，得到 %q", got)
		}
		if got := ResolveString("k", getConfig, nil, "E"); got != "" {
			t.Fatalf("期望空串，得到 %q", got)
		}
	})
}
