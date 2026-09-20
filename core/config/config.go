// Package config 是插件配置的持久化层。
//
// 每个插件独立命名空间，落在 plugin-configs/<pluginId>.json。
// 核心只负责读写，不解释内容，配置键只增不减。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store 是插件配置存储。
type Store struct {
	dir string
	mu  sync.Mutex
}

// New 创建配置存储。dir 通常是 <runtime>/plugin-configs。
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建配置目录失败: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir 返回配置目录。
func (s *Store) Dir() string { return s.dir }

func (s *Store) filePath(pluginID string) string {
	var b strings.Builder
	for _, r := range pluginID {
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
	name := b.String()
	if name == "" || name == "." || name == ".." {
		name = "unknown"
	}
	return filepath.Join(s.dir, name+".json")
}

// Load 读取某插件的全部配置。
func (s *Store) Load(pluginID string) (map[string]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(pluginID)
}

func (s *Store) loadLocked(pluginID string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(s.filePath(pluginID))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, err
	}
	out := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("配置 %s 损坏: %w", pluginID, err)
	}
	return out, nil
}

func (s *Store) saveLocked(pluginID string, data map[string]json.RawMessage) error {
	blob, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath(pluginID), append(blob, '\n'), 0o644)
}

// Get 读取单个配置键。
func (s *Store) Get(pluginID, key string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked(pluginID)
	if err != nil {
		return nil, false, err
	}
	v, ok := data[key]
	return v, ok, nil
}

// Set 写入单个配置键。只增不减：核心不会主动删除任何键。
func (s *Store) Set(pluginID, key string, value json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.loadLocked(pluginID)
	if err != nil {
		return err
	}
	data[key] = value
	return s.saveLocked(pluginID, data)
}

// All 返回某插件的全部配置快照。
func (s *Store) All(pluginID string) (map[string]json.RawMessage, error) {
	return s.Load(pluginID)
}
