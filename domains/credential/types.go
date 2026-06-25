// Package credential 实现凭据管理领域。
// 阶段: PreRouting (健康检查 + 限流) / Routing (凭据选择)
//
// 职责：
//   - 凭据生命周期 (Register / Update / Delete / Rotate)
//   - 健康状态追踪 (Healthy / Degraded / Unhealthy)
//   - 并发控制 (MaxConcurrent / Semaphore)
//   - 熔断器 (Circuit breaker)
//   - 加密存储抽象 (密文 + 解密接口)
package credential

import (
	"errors"
	"sync"
	"time"
)

// Status 凭据状态
type Status string

const (
	StatusActive    Status = "active"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
	StatusDisabled  Status = "disabled"
)

// Credential 凭据（不存储明文 key，仅存密文 + 元数据）
type Credential struct {
	ID         string
	TenantID   string
	ProviderID string
	Model      string
	// EncryptedKey 加密后的 API key (解密由 Crypto 接口处理)
	EncryptedKey []byte
	// Priority 优先级 (0=最低, 100=最高)
	Priority int
	Status   Status
	// MaxConcurrent 最大并发请求数
	MaxConcurrent int
	// Metadata 自由扩展字段
	Metadata map[string]any
	// 健康指标
	LastHealthCheck  time.Time
	ConsecutiveFails int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Store 凭据存储接口
type Store interface {
	Save(cred *Credential) error
	Get(id string) (*Credential, bool, error)
	Delete(id string) error
	List(tenantID string) ([]*Credential, error)
}

// Crypto 加解密接口（密文 <-> 明文）
type Crypto interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// PlainCrypto 明文存储（仅用于测试/本地开发）
type PlainCrypto struct{}

// Encrypt 不加密
func (PlainCrypto) Encrypt(plaintext []byte) ([]byte, error) { return plaintext, nil }

// Decrypt 不解密
func (PlainCrypto) Decrypt(ciphertext []byte) ([]byte, error) { return ciphertext, nil }

// InMemoryStore 内存凭据存储
type InMemoryStore struct {
	mu    sync.RWMutex
	creds map[string]*Credential
}

// NewInMemoryStore 创建内存凭据存储
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{creds: make(map[string]*Credential)}
}

// Save 保存凭据
func (s *InMemoryStore) Save(cred *Credential) error {
	if cred == nil || cred.ID == "" {
		return errors.New("credential: ID required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cred.CreatedAt.IsZero() {
		cred.CreatedAt = time.Now()
	}
	cred.UpdatedAt = time.Now()
	s.creds[cred.ID] = cred
	return nil
}

// Get 获取凭据
func (s *InMemoryStore) Get(id string) (*Credential, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.creds[id]
	if !ok {
		return nil, false, nil
	}
	// 返回副本
	cp := *c
	if c.Metadata != nil {
		cp.Metadata = make(map[string]any, len(c.Metadata))
		for k, v := range c.Metadata {
			cp.Metadata[k] = v
		}
	}
	if c.EncryptedKey != nil {
		cp.EncryptedKey = make([]byte, len(c.EncryptedKey))
		copy(cp.EncryptedKey, c.EncryptedKey)
	}
	return &cp, true, nil
}

// Delete 删除凭据
func (s *InMemoryStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.creds, id)
	return nil
}

// List 列出指定租户的所有凭据
func (s *InMemoryStore) List(tenantID string) ([]*Credential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Credential, 0)
	for _, c := range s.creds {
		if tenantID == "" || c.TenantID == tenantID {
			cp := *c
			out = append(out, &cp)
		}
	}
	return out, nil
}

// Count 返回总数（测试用）
func (s *InMemoryStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.creds)
}
