// Package logs 是核心唯一的日志落盘入口。
//
// 规则：核心只写入 logs/<pluginId>.log，不分析、不过滤、不告警。
// 需要日志分析的场景由“日志插件”订阅 core.log 事件后自行处理。
package logs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Logger 管理 logs/ 目录下的所有日志文件句柄。
type Logger struct {
	dir   string
	mu    sync.Mutex
	files map[string]*os.File
}

// New 创建日志器。dir 通常是 <runtime>/logs。
func New(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %w", err)
	}
	return &Logger{dir: dir, files: make(map[string]*os.File)}, nil
}

// Dir 返回日志目录。
func (l *Logger) Dir() string { return l.dir }

func sanitize(name string) string {
	if name == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := b.String()
	if s == "." || s == ".." || strings.Trim(s, ".") == "" {
		return "unknown"
	}
	return s
}

// file 惰性打开日志文件。
func (l *Logger) file(name string) (*os.File, error) {
	key := sanitize(name)
	if f, ok := l.files[key]; ok {
		return f, nil
	}
	f, err := os.OpenFile(filepath.Join(l.dir, key+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	l.files[key] = f
	return f, nil
}

func formatLine(level, msg string, meta map[string]any) string {
	var b strings.Builder
	b.WriteString(time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	b.WriteString(" [")
	b.WriteString(strings.ToUpper(level))
	b.WriteString("] ")
	b.WriteString(msg)
	if len(meta) > 0 {
		if data, err := json.Marshal(meta); err == nil {
			b.WriteByte(' ')
			b.Write(data)
		}
	}
	b.WriteByte('\n')
	return b.String()
}

// Write 是唯一的写入口。
func (l *Logger) Write(name, level, msg string, meta map[string]any) {
	line := formatLine(level, msg, meta)
	l.mu.Lock()
	defer l.mu.Unlock()
	if f, err := l.file(name); err == nil {
		_, _ = f.WriteString(line)
	}
}

// Core 写核心自身日志，落到 logs/core.log。
func (l *Logger) Core(level, format string, args ...any) {
	l.Write("core", level, fmt.Sprintf(format, args...), nil)
}

// Plugin 写插件日志，落到 logs/<pluginId>.log。
func (l *Logger) Plugin(pluginID, level, msg string, meta map[string]any) {
	l.Write(pluginID, level, msg, meta)
}

// PluginStderr 转发插件 stderr：核心只转发，不解析。
func (l *Logger) PluginStderr(pluginID, line string) {
	l.Write(pluginID, "stderr", line, nil)
}

// Close 关闭所有句柄。
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, f := range l.files {
		_ = f.Close()
		delete(l.files, k)
	}
}
