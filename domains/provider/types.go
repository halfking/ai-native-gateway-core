// Package provider 实现供应商管理领域。
// 阶段: PreRouting (健康探测 + 模型列表) / Routing (协议选择)
//
// 职责：
//   - Provider 注册 / 配置 (BaseURL / AuthType / Models)
//   - 健康探测 (ping + probe 模型列表)
//   - 协议适配 (OpenAI / Anthropic / Azure OpenAI)
//   - 模型路由 (model -> provider 映射)
package provider

import (
	"errors"
	"sync"
	"time"
)

// Protocol 协议类型
type Protocol string

const (
	ProtocolOpenAI    Protocol = "openai"
	ProtocolAnthropic Protocol = "anthropic"
	ProtocolAzure     Protocol = "azure"
	ProtocolCustom    Protocol = "custom"
)

// Provider 供应商
type Provider struct {
	ID       string
	Name     string
	BaseURL  string
	Protocol Protocol
	// AuthType 认证方式: "bearer" / "api_key" / "azure_ad"
	AuthType string
	// Models 支持的模型列表
	Models []ModelSpec
	// Headers 自定义请求头
	Headers map[string]string
	// TimeoutSec 请求超时（秒）
	TimeoutSec int
	// Disabled 是否禁用
	Disabled bool
	// Metadata 自由扩展
	Metadata map[string]any
	// 健康状态
	LastHealthCheck  time.Time
	ConsecutiveFails int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ModelSpec 模型规格
type ModelSpec struct {
	Name             string
	MaxContextTokens int
	SupportsStream   bool
	SupportsTools    bool
	InputCostPer1K   float64
	OutputCostPer1K  float64
}

// Status provider 状态
type Status string

const (
	StatusActive    Status = "active"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
	StatusDisabled  Status = "disabled"
)

// Store provider 存储接口
type Store interface {
	Save(p *Provider) error
	Get(id string) (*Provider, bool, error)
	Delete(id string) error
	List() ([]*Provider, error)
	// FindByModel 按模型名查找
	FindByModel(model string) ([]*Provider, error)
}

// InMemoryStore 内存存储
type InMemoryStore struct {
	mu        sync.RWMutex
	providers map[string]*Provider
}

// NewInMemoryStore 创建内存存储
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{providers: make(map[string]*Provider)}
}

// Save 保存 provider
func (s *InMemoryStore) Save(p *Provider) error {
	if p == nil || p.ID == "" {
		return errors.New("provider: ID required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	p.UpdatedAt = time.Now()
	s.providers[p.ID] = p
	return nil
}

// Get 获取 provider
func (s *InMemoryStore) Get(id string) (*Provider, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.providers[id]
	if !ok {
		return nil, false, nil
	}
	cp := *p
	if p.Models != nil {
		cp.Models = make([]ModelSpec, len(p.Models))
		copy(cp.Models, p.Models)
	}
	return &cp, true, nil
}

// Delete 删除 provider
func (s *InMemoryStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.providers, id)
	return nil
}

// List 列出所有 provider
func (s *InMemoryStore) List() ([]*Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Provider, 0, len(s.providers))
	for _, p := range s.providers {
		cp := *p
		out = append(out, &cp)
	}
	return out, nil
}

// FindByModel 查找支持指定模型的 provider
func (s *InMemoryStore) FindByModel(model string) ([]*Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Provider, 0)
	for _, p := range s.providers {
		for _, m := range p.Models {
			if m.Name == model {
				cp := *p
				out = append(out, &cp)
				break
			}
		}
	}
	return out, nil
}

// Count 返回总数
func (s *InMemoryStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.providers)
}
