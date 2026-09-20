package loader

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// ExtractPluginZip 把插件包解压到 destDir。
//
// 行为：
//   - 自动剥掉包内的单层根目录（例如 minimal-ui/manifest.json -> manifest.json）
//   - 防 zip slip（拒绝 .. 与绝对路径）
//   - 非 Windows 平台补齐入口文件的可执行位（Compress-Archive 不保留权限）
func ExtractPluginZip(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开压缩包失败: %w", err)
	}
	defer zr.Close()

	// 1. 定位 manifest.json，得到需要剥掉的前缀
	prefix := ""
	found := false
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasSuffix(name, "manifest.json") {
			p := strings.TrimSuffix(name, "manifest.json")
			if !found || len(p) < len(prefix) {
				prefix = p
				found = true
			}
		}
	}
	if !found {
		return errors.New("插件包中未找到 manifest.json")
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	cleanDest := filepath.Clean(destDir)

	var manifestBytes []byte

	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" {
			continue
		}
		rel = path.Clean(rel)
		if rel == "." || strings.HasPrefix(rel, "..") || path.IsAbs(rel) {
			continue
		}
		target := filepath.Join(destDir, filepath.FromSlash(rel))
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("压缩包中存在非法路径: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := copyZipEntry(f, target); err != nil {
			return err
		}
		if rel == "manifest.json" {
			manifestBytes, _ = os.ReadFile(target)
		}
	}

	// 2. 非 Windows 平台补齐可执行位
	if runtime.GOOS != "windows" && len(manifestBytes) > 0 {
		var m Manifest
		if json.Unmarshal(manifestBytes, &m) == nil && m.Entry != "" {
			for _, cand := range []string{m.Entry, m.Entry + ".sh"} {
				p := filepath.Join(destDir, cand)
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					_ = os.Chmod(p, st.Mode()|0o755)
				}
			}
		}
	}
	return nil
}

func copyZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", target, err)
	}
	return nil
}

// CopyDir 递归复制目录，用于“从本地文件夹安装插件”。
func CopyDir(src, dst string) error {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	if src == dst {
		return nil
	}
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s 不是目录", src)
	}
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm()|0o600)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}
