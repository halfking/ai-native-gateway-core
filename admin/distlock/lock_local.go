package distlock

import (
	"context"
	"sync"
)

// LocalManager is the single-process fallback used when Redis is unavailable.
// It intentionally has no TTL or crash takeover: callers must release the
// leader handle, and request contexts remain the only follower cancellation.
type LocalManager struct {
	mu      sync.Mutex
	flights map[string]chan struct{}
}

func NewLocalManager() *LocalManager {
	return &LocalManager{flights: make(map[string]chan struct{})}
}

func (m *LocalManager) Enabled() bool { return m != nil }

type localBackend struct {
	m   *LocalManager
	key string
	ch  chan struct{}
}

func (b *localBackend) leaderRelease(_ context.Context) error {
	if b == nil || b.m == nil || b.ch == nil {
		return nil
	}
	b.m.mu.Lock()
	defer b.m.mu.Unlock()
	if cur, ok := b.m.flights[b.key]; ok && cur == b.ch {
		delete(b.m.flights, b.key)
		close(b.ch)
	}
	return nil
}

func (b *localBackend) check(context.Context) error { return nil }

func (m *LocalManager) Acquire(_ context.Context, opts AcquireOpts) (*Handle, error) {
	if m == nil {
		return nil, ErrNotEnabled
	}
	if opts.Key == "" {
		distlockAcquireTotal.WithLabelValues("unknown", "invalid").Inc()
		return nil, ErrInvalidKey
	}
	if !validMode(opts.Mode) {
		distlockAcquireTotal.WithLabelValues(normalizeLockScope(opts.Scope), "invalid").Inc()
		return nil, ErrUnsupportedMode
	}
	scope := normalizeLockScope(opts.Scope)
	m.mu.Lock()
	if m.flights == nil {
		m.flights = make(map[string]chan struct{})
	}
	if ch, ok := m.flights[opts.Key]; ok {
		m.mu.Unlock()
		distlockAcquireTotal.WithLabelValues(scope, "follower").Inc()
		return newLocalFollowerHandle(opts.Key, scope, ch, &localBackend{m: m, key: opts.Key, ch: ch}), nil
	}
	ch := make(chan struct{})
	m.flights[opts.Key] = ch
	m.mu.Unlock()
	distlockAcquireTotal.WithLabelValues(scope, "leader").Inc()
	backend := &localBackend{m: m, key: opts.Key, ch: ch}
	return newLeaderHandle(opts.Key, 0, scope, backend), nil
}
