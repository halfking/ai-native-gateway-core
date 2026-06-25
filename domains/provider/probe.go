package provider

import (
	"errors"
	"sync"
	"time"
)

// Prober 健康探测器
type Prober struct {
	store *InMemoryStore
	mu    sync.Mutex
	// failThreshold 连续失败 N 次标记 unhealthy
	failThreshold int
}

// NewProber 创建探测器
func NewProber(store *InMemoryStore) *Prober {
	return &Prober{
		store:         store,
		failThreshold: 3,
	}
}

// ProbeFunc 探测回调（实际可调用 provider health endpoint）
type ProbeFunc func(p *Provider) error

// Probe 探测指定 provider
func (pr *Prober) Probe(providerID string, probe ProbeFunc) error {
	if providerID == "" {
		return errors.New("provider: ID required")
	}
	p, ok, err := pr.store.Get(providerID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("provider: not found")
	}
	if p.Disabled {
		return errors.New("provider: disabled")
	}
	if probe != nil {
		if err := probe(p); err != nil {
			_ = pr.MarkFailure(providerID)
			return err
		}
	}
	return pr.MarkSuccess(providerID)
}

// MarkSuccess 标记成功
func (pr *Prober) MarkSuccess(providerID string) error {
	if providerID == "" {
		return errors.New("provider: ID required")
	}
	p, ok, err := pr.store.Get(providerID)
	if err != nil || !ok {
		return err
	}
	p.ConsecutiveFails = 0
	if p.Status() == StatusDegraded || p.Status() == StatusUnhealthy {
		// 状态改变需要 Save
	}
	p.LastHealthCheck = time.Now()
	return pr.store.Save(p)
}

// MarkFailure 标记失败
func (pr *Prober) MarkFailure(providerID string) error {
	p, ok, err := pr.store.Get(providerID)
	if err != nil || !ok {
		return err
	}
	p.ConsecutiveFails++
	p.LastHealthCheck = time.Now()
	return pr.store.Save(p)
}

// Status 获取 provider 状态
func (p *Provider) Status() Status {
	if p.Disabled {
		return StatusDisabled
	}
	if p.ConsecutiveFails >= 3 {
		return StatusUnhealthy
	}
	if p.ConsecutiveFails > 0 {
		return StatusDegraded
	}
	return StatusActive
}

// IsHealthy 判断是否健康
func (pr *Prober) IsHealthy(p *Provider) bool {
	return p.Status() == StatusActive
}

// FilterHealthy 过滤健康 provider
func (pr *Prober) FilterHealthy(providers []*Provider) []*Provider {
	out := make([]*Provider, 0, len(providers))
	for _, p := range providers {
		if pr.IsHealthy(p) {
			out = append(out, p)
		}
	}
	return out
}

// SupportsModel 判断 provider 是否支持指定模型
func (p *Provider) SupportsModel(model string) bool {
	for _, m := range p.Models {
		if m.Name == model {
			return true
		}
	}
	return false
}
