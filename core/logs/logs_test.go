package logs

import (
	"os"
	"path/filepath"
	"testing"
)

// 热卸载插件后要能**当场删掉它的日志**：句柄是惰性打开、且默认一直持有的，
// 不关掉的话 Windows 上 `logs/<id>.log` 删不掉，删除就得退回"重启后生效"。
// ClosePlugin 正是为这个加的。
func TestClosePluginReleasesLogFile(t *testing.T) {
	dir := t.TempDir()
	lg, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	// 写一行，句柄被惰性打开并持有
	lg.Plugin("com.official.demo", "info", "hello", nil)
	logPath := filepath.Join(dir, "com.official.demo.log")
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("日志应已写入：%v", err)
	}

	lg.ClosePlugin("com.official.demo")

	// 关掉之后就能删 —— Windows 上句柄还开着的话这一步会失败
	if err := os.Remove(logPath); err != nil {
		t.Fatalf("ClosePlugin 之后日志应能删掉，实际：%v", err)
	}
}

// ClosePlugin 只关指定的那一个，别的插件日志不受影响。
func TestClosePluginKeepsOthers(t *testing.T) {
	dir := t.TempDir()
	lg, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	lg.Plugin("com.official.a", "info", "a", nil)
	lg.Plugin("com.official.b", "info", "b", nil)

	lg.ClosePlugin("com.official.a")

	if _, err := os.Stat(filepath.Join(dir, "com.official.b.log")); err != nil {
		t.Fatalf("别的插件日志不该被牵连：%v", err)
	}
}

// nil 日志器上调 ClosePlugin 不应 panic（unloadPlugin 的单测里没有日志器）。
func TestClosePluginNilSafe(t *testing.T) {
	var lg *Logger
	lg.ClosePlugin("com.official.demo")
}
