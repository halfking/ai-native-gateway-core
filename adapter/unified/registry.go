package unified

import (
	"errors"
	"fmt"
	"sync"
)

var (
	// globalRegistry 是全局 Adapter 注册表
	globalRegistry = NewRegistry()
)

// Registry 是 Adapter 注册表
type Registry struct {
	adapters map[string]Adapter
	mu       sync.RWMutex
}

// NewRegistry 创建一个新的注册表
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]Adapter),
	}
}

// Register 注册一个 Adapter
func (r *Registry) Register(adapter Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[adapter.Name()] = adapter
}

// Get 获取一个 Adapter
func (r *Registry) Get(provider string) (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	adapter, ok := r.adapters[provider]
	if !ok {
		return nil, fmt.Errorf("adapter not found: %s", provider)
	}
	return adapter, nil
}

// List 列出所有已注册的 Adapter 名称
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	return names
}

// Has 检查是否存在指定的 Adapter
func (r *Registry) Has(provider string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.adapters[provider]
	return ok
}

// Unregister 注销一个 Adapter
func (r *Registry) Unregister(provider string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.adapters[provider]; !ok {
		return fmt.Errorf("adapter not found: %s", provider)
	}
	delete(r.adapters, provider)
	return nil
}

// Global registry functions

// Register 注册一个 Adapter 到全局注册表
func Register(adapter Adapter) {
	globalRegistry.Register(adapter)
}

// GetAdapter 从全局注册表获取 Adapter
func GetAdapter(provider string) (Adapter, error) {
	return globalRegistry.Get(provider)
}

// ListAdapters 列出全局注册表中的所有 Adapter
func ListAdapters() []string {
	return globalRegistry.List()
}

// HasAdapter 检查全局注册表中是否存在指定的 Adapter
func HasAdapter(provider string) bool {
	return globalRegistry.Has(provider)
}

// UnregisterAdapter 从全局注册表注销 Adapter
func UnregisterAdapter(provider string) error {
	return globalRegistry.Unregister(provider)
}

// MustGetAdapter 从全局注册表获取 Adapter，不存在则 panic
func MustGetAdapter(provider string) Adapter {
	adapter, err := GetAdapter(provider)
	if err != nil {
		panic(err)
	}
	return adapter
}

// ValidateAndGetAdapter 验证请求并获取对应的 Adapter
func ValidateAndGetAdapter(provider string, req *UnifiedRequest) (Adapter, error) {
	adapter, err := GetAdapter(provider)
	if err != nil {
		return nil, err
	}

	if err := adapter.ValidateRequest(req); err != nil {
		return nil, fmt.Errorf("request validation failed: %w", err)
	}

	return adapter, nil
}

// GetStreamAdapter 获取支持流式的 Adapter
func GetStreamAdapter(provider string) (StreamAdapter, error) {
	adapter, err := GetAdapter(provider)
	if err != nil {
		return nil, err
	}

	streamAdapter, ok := adapter.(StreamAdapter)
	if !ok {
		return nil, errors.New("adapter does not support streaming")
	}

	return streamAdapter, nil
}

// init 自动注册内置 Adapter
func init() {
	Register(NewOpenAIAdapter())
	Register(NewAnthropicAdapter())
}
