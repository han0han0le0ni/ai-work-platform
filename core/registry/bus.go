package registry

import (
	"fmt"
	"sort"
	"sync"
)

// Wildcard 表示订阅所有事件。
const Wildcard = "*"

// Bus 是事件监听表。
//
// 事件不持久化，重启即消失；插件卸载时核心自动取消其所有监听。
type Bus struct {
	mu   sync.RWMutex
	subs map[string]map[string]struct{}
}

// NewBus 创建空事件总线。
func NewBus() *Bus {
	return &Bus{subs: make(map[string]map[string]struct{})}
}

// Subscribe 订阅事件。event 为 "*" 时订阅全部事件。
// 事件名必须带命名空间，否则拒绝订阅（规范 3.7 命名规范）。
func (b *Bus) Subscribe(event, pluginID string) error {
	if event == "" || pluginID == "" {
		return fmt.Errorf("事件名与插件 ID 不能为空")
	}
	if !ValidEventName(event) {
		return fmt.Errorf("事件名必须形如 <命名空间>.<动作>，例如 chat.message，当前为 %q", event)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	set, ok := b.subs[event]
	if !ok {
		set = make(map[string]struct{})
		b.subs[event] = set
	}
	set[pluginID] = struct{}{}
	return nil
}

// UnsubscribeAll 取消某插件的全部监听。
func (b *Bus) UnsubscribeAll(pluginID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for event, set := range b.subs {
		delete(set, pluginID)
		if len(set) == 0 {
			delete(b.subs, event)
		}
	}
}

// Subscribers 返回某事件的所有订阅者（含通配订阅者）。
func (b *Bus) Subscribers(event string) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	seen := make(map[string]struct{})
	var out []string
	add := func(set map[string]struct{}) {
		for id := range set {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	add(b.subs[event])
	add(b.subs[Wildcard])
	sort.Strings(out)
	return out
}

// Count 返回监听关系总数。
func (b *Bus) Count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, set := range b.subs {
		n += len(set)
	}
	return n
}
