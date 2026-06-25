package credential

import (
	"errors"
	"sync"
)

// Limiter 凭据并发限制器
type Limiter struct {
	mu       sync.Mutex
	inFlight map[string]int
	store    *InMemoryStore
}

// NewLimiter 创建并发限制器
func NewLimiter(store *InMemoryStore) *Limiter {
	return &Limiter{
		inFlight: make(map[string]int),
		store:    store,
	}
}

// Acquire 获取一个并发槽位（返回 false 表示已达上限）
func (l *Limiter) Acquire(credID string) (bool, error) {
	if credID == "" {
		return false, errors.New("credential: ID required")
	}
	cred, ok, err := l.store.Get(credID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errors.New("credential: not found")
	}
	if cred.MaxConcurrent <= 0 {
		// 无限制
		return true, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight[credID] >= cred.MaxConcurrent {
		return false, nil
	}
	l.inFlight[credID]++
	return true, nil
}

// Release 释放槽位
func (l *Limiter) Release(credID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight[credID] > 0 {
		l.inFlight[credID]--
	}
}

// InFlight 当前在飞数量
func (l *Limiter) InFlight(credID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inFlight[credID]
}
