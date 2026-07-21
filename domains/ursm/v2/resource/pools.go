package resource

import (
	"errors"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

type FP interface {
	Acquire(ctx Context, holder string) (Lease, error)
	Release(ctx Context, l Lease) error
	Stats(credentialID int) (used, free int)
}

type Concurrency interface {
	Acquire(credentialID string) (Token, error)
	Release(credentialID string, t Token) error
}

type RPM interface {
	Reserve(credentialID string) (ok bool, err error)
}

type Lease struct {
	SlotIndex int
	Holder    string
	Unlimited bool
}

type Token struct{}

type Context interface{ Done() <-chan struct{} }

var ErrSaturated = errors.New("resource saturated")

// NewLocalConcurrency: 第一阶段保留进程内 semaphore 实现；接口预留 Redis 替换。
func NewLocalConcurrency(cap int) *LocalConcurrency {
	if cap <= 0 {
		cap = 1
	}
	return &LocalConcurrency{cap: cap, used: map[string]int{}}
}

type LocalConcurrency struct {
	mu   sync.Mutex
	cap  int
	used map[string]int
}

func (l *LocalConcurrency) Acquire(credentialID string) (Token, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used[credentialID] >= l.cap {
		return Token{}, ErrSaturated
	}
	l.used[credentialID]++
	return Token{}, nil
}

func (l *LocalConcurrency) Release(credentialID string, _ Token) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used[credentialID] > 0 {
		l.used[credentialID]--
	}
	return nil
}

func MapToResourceView(used, limit int) api.NodeView {
	return api.NodeView{ConcUsed: used, ConcLimit: limit}
}
