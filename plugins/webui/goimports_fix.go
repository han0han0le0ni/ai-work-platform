package main

import (
	"regexp"
	"sort"
	"strings"
)

// 造 Go 插件时小模型最常见的两类编译错：
//
//	imported and not used: "fmt"   —— 多引了
//	undefined: os                  —— 忘了引
//
// 这类错不需要动脑子，goimports 能秒修，但目标机器上没有 goimports。
// 这里按 go build 的报错做等价的小修补（只认标准库白名单），修完再编一次；
// 真正的语义错误才退回给模型回炉。零网络、零外部依赖。

// stdImportByPkg 是允许自动补的标准库：代码里用到的包名 -> import 路径。
var stdImportByPkg = map[string]string{
	"os": "os", "fmt": "fmt", "time": "time", "strings": "strings",
	"strconv": "strconv", "sort": "sort", "math": "math", "rand": "math/rand",
	"regexp": "regexp", "errors": "errors", "sync": "sync", "bytes": "bytes",
	"io": "io", "net": "net", "http": "net/http", "url": "net/url",
	"filepath": "path/filepath", "json": "encoding/json", "csv": "encoding/csv",
	"hex": "encoding/hex", "base64": "encoding/base64", "sha256": "crypto/sha256",
	"md5": "crypto/md5", "hmac": "crypto/hmac",
	"context": "context", "bufio": "bufio", "unicode": "unicode",
}

var (
	reUnusedImport = regexp.MustCompile(`imported and not used: "([^"]+)"`)
	reUndefined    = regexp.MustCompile(`undefined: ([A-Za-z_][A-Za-z0-9_]*)`)
	reImportBlock  = regexp.MustCompile(`(?s)import \((.*?)\)`)
	reSingleImport = regexp.MustCompile(`(?m)^import "([^"]+)"\s*\n?`)
)

// autoFixGoImports 按 stderr 修 import，返回新源码与"是否改过"。
// 只动标准库；遇到拿不准的（第三方包、别名）原样返回。
func autoFixGoImports(src, stderr string) (string, bool) {
	changed := false

	// 1) 删除"引了没用"的包。
	unused := map[string]bool{}
	for _, m := range reUnusedImport.FindAllStringSubmatch(stderr, -1) {
		unused[m[1]] = true
	}
	if len(unused) > 0 {
		// 块式 import：删掉对应行
		src = reImportBlock.ReplaceAllStringFunc(src, func(block string) string {
			inner := reImportBlock.FindStringSubmatch(block)[1]
			lines := strings.Split(inner, "\n")
			kept := make([]string, 0, len(lines))
			for _, ln := range lines {
				trim := strings.TrimSpace(ln)
				if trim == "" {
					kept = append(kept, ln)
					continue
				}
				// 必须先去空白再剥引号，否则 "\tfmt" 剥完还带 tab，匹配不上报错路径。
				path := strings.Trim(trim, `"`)
				if unused[path] {
					changed = true
					continue
				}
				kept = append(kept, ln)
			}
			return "import (" + strings.Join(kept, "\n") + ")"
		})
		// 单行 import：整行删掉
		src = reSingleImport.ReplaceAllStringFunc(src, func(line string) string {
			path := reSingleImport.FindStringSubmatch(line)[1]
			if unused[path] {
				changed = true
				return ""
			}
			return line
		})
	}

	// 2) 补"undefined: X"对应的标准库（白名单 + 去重 + 确实在代码里被引用）。
	existing := existingImports(src)
	toAdd := map[string]bool{}
	for _, m := range reUndefined.FindAllStringSubmatch(stderr, -1) {
		pkg := m[1]
		path, ok := stdImportByPkg[pkg]
		if !ok || path == "" || existing[path] || toAdd[path] {
			continue
		}
		// 双保险：源码里真的出现过 pkg. 才补，避免编译器其它语境下的 undefined 误补。
		if strings.Contains(src, pkg+".") {
			toAdd[path] = true
			changed = true
		}
	}
	if len(toAdd) > 0 {
		paths := make([]string, 0, len(toAdd))
		for p := range toAdd {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		src = insertImports(src, paths)
	}
	return src, changed
}

// existingImports 摘出当前文件已经引入的路径（块式 + 单行）。
func existingImports(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range reImportBlock.FindAllStringSubmatch(src, -1) {
		for _, ln := range strings.Split(m[1], "\n") {
			t := strings.Trim(strings.TrimSpace(ln), `"`)
			if t != "" {
				out[t] = true
			}
		}
	}
	for _, m := range reSingleImport.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	return out
}

// insertImports 把路径并入 import 块；没有块就在 package 声明后新建一个。
func insertImports(src string, paths []string) string {
	if loc := reImportBlock.FindStringIndex(src); loc != nil {
		block := src[loc[0]:loc[1]]
		m := reImportBlock.FindStringSubmatch(block)
		inner := strings.TrimRight(m[1], "\n")
		add := ""
		for _, p := range paths {
			add += "\t\"" + p + "\"\n"
		}
		newBlock := "import (" + inner + add + ")"
		return src[:loc[0]] + newBlock + src[loc[1]:]
	}
	lines := strings.SplitN(src, "\n", 2)
	rest := ""
	if len(lines) == 2 {
		rest = lines[1]
	}
	block := "\nimport (\n"
	for _, p := range paths {
		block += "\t\"" + p + "\"\n"
	}
	block += ")\n"
	return lines[0] + block + rest
}
