// Package registry 实现核心的两张内存表：服务注册表与事件监听表。
//
// 核心只维护映射关系，不关心服务内容，也不保证事件顺序跨插件。
package registry

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Service 是一条服务注册记录。
type Service struct {
	Name     string
	PluginID string
	Meta     json.RawMessage
}

// Registry 是服务注册表。
type Registry struct {
	mu       sync.RWMutex
	services map[string]Service
}

// New 创建空注册表。
func New() *Registry {
	return &Registry{services: make(map[string]Service)}
}

// Provide 注册服务。同名服务重复注册时，只有同一个插件才允许覆盖。
// 服务名必须带版本，否则拒绝注册（规范 3.3 版本化规则）。
func (r *Registry) Provide(name, pluginID string, meta json.RawMessage) error {
	if name == "" {
		return fmt.Errorf("服务名不能为空")
	}
	if !ValidServiceName(name) {
		return fmt.Errorf("服务名必须形如 <名称>.v<版本>，例如 llm.v1，当前为 %q", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.services[name]; ok && old.PluginID != pluginID {
		return fmt.Errorf("服务 %s 已由 %s 提供", name, old.PluginID)
	}
	r.services[name] = Service{Name: name, PluginID: pluginID, Meta: meta}
	return nil
}

// Unprovide 由提供者主动注销服务。
func (r *Registry) Unprovide(name, pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.services[name]; ok && old.PluginID == pluginID {
		delete(r.services, name)
	}
}

// Lookup 发现服务。
func (r *Registry) Lookup(name string) (Service, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.services[name]
	return s, ok
}

// RemovePlugin 在插件卸载时清理其全部服务，返回被清理的服务名。
func (r *Registry) RemovePlugin(pluginID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var removed []string
	for name, s := range r.services {
		if s.PluginID == pluginID {
			delete(r.services, name)
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	return removed
}

// Snapshot 返回注册表快照。
func (r *Registry) Snapshot() map[string]Service {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Service, len(r.services))
	for k, v := range r.services {
		out[k] = v
	}
	return out
}

// Names 返回已注册的服务名（有序）。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.services))
	for k := range r.services {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Len 返回服务数量。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.services)
}
