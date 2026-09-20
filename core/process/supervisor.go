package process

import (
	"sync"
	"time"
)

// Supervisor 维护所有插件子进程的生命周期。
//
// 核心只做三件事：启动、监控、退出时统一杀死。
type Supervisor struct {
	mu      sync.RWMutex
	plugins map[string]*Plugin
	order   []string
}

// NewSupervisor 创建空的插件管理器。
func NewSupervisor() *Supervisor {
	return &Supervisor{plugins: make(map[string]*Plugin)}
}

// Add 登记一个插件（保持启动顺序）。
func (s *Supervisor) Add(p *Plugin) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.plugins[p.Spec.ID]; !ok {
		s.order = append(s.order, p.Spec.ID)
	}
	s.plugins[p.Spec.ID] = p
}

// Get 按插件 ID 查找。
func (s *Supervisor) Get(id string) *Plugin {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.plugins[id]
}

// List 返回按启动顺序排列的插件列表。
func (s *Supervisor) List() []*Plugin {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Plugin, 0, len(s.order))
	for _, id := range s.order {
		if p, ok := s.plugins[id]; ok {
			out = append(out, p)
		}
	}
	return out
}

// Remove 摘除一个插件。
func (s *Supervisor) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.plugins, id)
	for i, v := range s.order {
		if v == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// Alive 返回存活插件数量。
func (s *Supervisor) Alive() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, p := range s.plugins {
		if p.Alive() {
			n++
		}
	}
	return n
}

// StopAll 逆启动序停止所有插件。
//
// 并着停，不是挨个停：Stop 对每个插件最多要等 grace + SIGTERM 3 秒 + SIGKILL 3 秒，
// 挨个来就是"每个插件的等待时间之和" —— 一个插件卡住能把重启拖成几十秒。
// 并起来总时长取决于最慢的那一个，而不是全部加起来。
// 发起顺序仍然逆着启动顺序（后启动的先收到通知），只是不等它走完就发下一个。
func (s *Supervisor) StopAll(grace time.Duration) {
	list := s.List()
	var wg sync.WaitGroup
	for i := len(list) - 1; i >= 0; i-- {
		wg.Add(1)
		go func(p *Plugin) {
			defer wg.Done()
			p.Stop(grace)
		}(list[i])
	}
	wg.Wait()
}
