package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Discovery 是一条扫描结果。Err 非空表示该插件不可用。
type Discovery struct {
	Dir      string
	Manifest *Manifest
	Err      error
}

// ID 返回插件 ID，可能为空。
func (d Discovery) ID() string {
	if d.Manifest == nil {
		return ""
	}
	return d.Manifest.ID
}

// Scan 扫描 plugins/ 目录。
//
// 步骤：
//  1. 把 *.zip 解压成 <名称>.plugin/ 并删除原 zip（已解压过的跳过）
//  2. 解析所有 *.plugin/ 目录下的 manifest.json
func Scan(pluginsDir string) ([]Discovery, error) {
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []Discovery

	// 1. 解压插件包
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".zip") {
			continue
		}
		zipPath := filepath.Join(pluginsDir, e.Name())
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		target := filepath.Join(pluginsDir, base+".plugin")
		if _, statErr := os.Stat(target); statErr == nil {
			// 已经解压过，忽略该 zip
			continue
		}
		if err := ExtractPluginZip(zipPath, target); err != nil {
			out = append(out, Discovery{Dir: target, Err: fmt.Errorf("解压 %s 失败: %w", e.Name(), err)})
			continue
		}
		_ = os.Remove(zipPath)
	}

	entries, err = os.ReadDir(pluginsDir)
	if err != nil {
		return out, err
	}

	// 2. 解析 manifest
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".plugin") {
			continue
		}
		dir := filepath.Join(pluginsDir, e.Name())
		m, mErr := LoadManifest(dir)
		out = append(out, Discovery{Dir: dir, Manifest: m, Err: mErr})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}
