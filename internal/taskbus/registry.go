package taskbus

import (
	"fmt"
	"sync"
)

// Registry 按 TaskType 登记 Handler；实现 pipeline.TaskHandlerLookup。
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]Handler)}
}

// Register 登记 Handler；同名重复 ⇒ 错误（响亮失败，不许静默覆盖）。
func (r *Registry) Register(taskType string, h Handler) error {
	if r == nil {
		return fmt.Errorf("taskbus.Register: nil registry")
	}
	if taskType == "" {
		return fmt.Errorf("taskbus.Register: empty taskType")
	}
	if h == nil {
		return fmt.Errorf("taskbus.Register: nil handler for %q", taskType)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[taskType]; exists {
		return fmt.Errorf("taskbus.Register: taskType %q already registered", taskType)
	}
	r.handlers[taskType] = h
	return nil
}

// MustRegister 同 Register，失败 panic（启动期用）。
func (r *Registry) MustRegister(taskType string, h Handler) {
	if err := r.Register(taskType, h); err != nil {
		panic(err)
	}
}

// Handler 返回已登记的 Handler。
func (r *Registry) Handler(taskType string) (Handler, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[taskType]
	return h, ok
}

// HasHandler 实现 pipeline.TaskHandlerLookup（V2 对拍）。
func (r *Registry) HasHandler(taskType string) bool {
	_, ok := r.Handler(taskType)
	return ok
}

// TaskTypes 返回已登记类型（无序快照）。
func (r *Registry) TaskTypes() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.handlers))
	for k := range r.handlers {
		out = append(out, k)
	}
	return out
}
