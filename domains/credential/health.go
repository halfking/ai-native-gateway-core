package credential

import (
	"errors"
	"sync"
	"time"
)

// HealthChecker 健康检查器
type HealthChecker struct {
	store *InMemoryStore
	mu    sync.Mutex
	// checkInterval 上次检查后至少 N 毫秒才能再次检查
	checkInterval time.Duration
	// failThreshold 连续失败 N 次标记 unhealthy
	failThreshold int
	// successThreshold 连续成功 N 次恢复 healthy
	successThreshold int
}

// NewHealthChecker 创建健康检查器
func NewHealthChecker(store *InMemoryStore) *HealthChecker {
	return &HealthChecker{
		store:            store,
		checkInterval:    30 * time.Second,
		failThreshold:    3,
		successThreshold: 2,
	}
}

// Ping 模拟健康探测（实际可调用 provider 端点）
type PingFunc func(cred *Credential) error

// MarkSuccess 标记探测成功
func (h *HealthChecker) MarkSuccess(credID string) error {
	if credID == "" {
		return errors.New("credential: ID required")
	}
	cred, ok, err := h.store.Get(credID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("credential: not found")
	}
	cred.ConsecutiveFails = 0
	if cred.Status == StatusDegraded || cred.Status == StatusUnhealthy {
		cred.Status = StatusActive
	}
	cred.LastHealthCheck = time.Now()
	return h.store.Save(cred)
}

// MarkFailure 标记探测失败
func (h *HealthChecker) MarkFailure(credID string) error {
	if credID == "" {
		return errors.New("credential: ID required")
	}
	cred, ok, err := h.store.Get(credID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("credential: not found")
	}
	cred.ConsecutiveFails++
	cred.LastHealthCheck = time.Now()
	if cred.ConsecutiveFails >= h.failThreshold {
		cred.Status = StatusUnhealthy
	} else if cred.ConsecutiveFails > 0 {
		cred.Status = StatusDegraded
	}
	return h.store.Save(cred)
}

// IsHealthy 判断凭据是否健康
func (h *HealthChecker) IsHealthy(cred *Credential) bool {
	if cred == nil {
		return false
	}
	return cred.Status == StatusActive
}

// FilterHealthy 过滤出健康凭据
func (h *HealthChecker) FilterHealthy(creds []*Credential) []*Credential {
	out := make([]*Credential, 0, len(creds))
	for _, c := range creds {
		if h.IsHealthy(c) {
			out = append(out, c)
		}
	}
	return out
}
