package security

import (
	"context"
	"fmt"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
)

// Registry 安全插件注册中心。
//
// 线程安全；注册与查询可并发。
type Registry struct {
	mu      sync.RWMutex
	plugins []Plugin
}

// NewRegistry 构造空注册中心。
func NewRegistry() *Registry { return &Registry{} }

// Register 注册插件；同名插件重复注册返回 error。
func (r *Registry) Register(p Plugin) error {
	if r == nil {
		return fmt.Errorf("security: nil registry")
	}
	if p == nil {
		return fmt.Errorf("security: nil plugin")
	}
	name := p.Name()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.plugins {
		if existing.Name() == name {
			return fmt.Errorf("security: duplicate plugin name %q", name)
		}
	}
	r.plugins = append(r.plugins, p)
	return nil
}

// MustRegister Register 失败时 panic；用于启动期已知插件清单的场景。
func (r *Registry) MustRegister(p Plugin) {
	if err := r.Register(p); err != nil {
		panic(err)
	}
}

// List 返回当前已注册插件（只读快照）。
func (r *Registry) List() []Plugin {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Plugin, len(r.plugins))
	copy(out, r.plugins)
	return out
}

// Count 返回已注册插件数量。
func (r *Registry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.plugins)
}

// RunAll 在指定 scope 下运行所有插件，收集所有 verdict。
//
// 单个插件 Inspect 失败不会中断其他插件；失败被转写为一条 Allow=false verdict
// （Code="plugin_error"）。返回的 verdict 顺序与注册顺序一致。
func (r *Registry) RunAll(ctx context.Context, env *domain.PipelineRequest, scope Scope) ([]*governance.Verdict, error) {
	if r == nil {
		return nil, fmt.Errorf("security: nil registry")
	}
	if env == nil {
		return nil, fmt.Errorf("security: nil envelope")
	}

	plugins := r.List()
	tenantID := env.TenantID
	modelID := ""
	if env.SelectedProvider != nil {
		modelID = env.SelectedProvider.Name
	}

	out := make([]*governance.Verdict, 0, len(plugins))
	for _, p := range plugins {
		if !scopeAllows(scope, tenantID, modelID) {
			continue
		}
		v, err := p.Inspect(ctx, env)
		if err != nil {
			out = append(out, &governance.Verdict{
				PluginName: p.Name(),
				Allow:      false,
				Severity:   2,
				Code:       "plugin_error",
				Reason:     err.Error(),
			})
			continue
		}
		if v == nil {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// scopeAllows 判断当前 (tenant, model) 是否在 scope 范围内。
//
// 任一非空字段表示"必须命中才允许"；空字段表示"不限"。
func scopeAllows(s Scope, tenantID, modelID string) bool {
	if len(s.TenantIDs) > 0 && !containsString(s.TenantIDs, tenantID) {
		return false
	}
	if len(s.ModelIDs) > 0 && !containsString(s.ModelIDs, modelID) {
		return false
	}
	return true
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
