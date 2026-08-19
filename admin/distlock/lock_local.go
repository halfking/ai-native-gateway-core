package distlock

import (
	"context"
	"sync"
	"time"
)

// LocalManager is the in-process fallback used when Redis is unavailable
// (cfg.RedisAddr == "" or Redis ping failed at startup). It implements
// the same Manager interface so callers can switch implementations
// transparently.
//
// Limitations vs RedisManager:
//
//   - Single-process only. Two gateway replicas each running an auto
//     title generator will each spawn their own goroutine against the
//     same session. The session_titles ON CONFLICT DO NOTHING guard
//     still keeps the DB consistent, but upstream LLM chatter will
//     multiply across replicas. This is an accepted trade-off for not
//     requiring Redis to deploy the gateway.
//   - Lock is not bounded by TTL; the goroutine holding it must call
//     Release exactly once to unblock waiters. There is no leader-crash
//     recovery (a crashed leader wedges its followers until the process
//     restarts). This is acceptable because the calling goroutines have
//     their own context timeouts (45s for title generation) and the
//     deferred Release fires when they return.
//
// The struct is safe for concurrent use by multiple goroutines.
type LocalManager struct {
	mu      sync.Mutex
	flights map[string]chan struct{}
}

// NewLocalManager builds a new in-process manager.
func NewLocalManager() *LocalManager {
	return &LocalManager{flights: make(map[string]chan struct{})}
}

// Enabled always returns true: LocalManager has no transport that can
// fail.
func (m *LocalManager) Enabled() bool { return true }

// localBackend implements handleBackend for LocalManager. The fields are
// only used by the leader path; follower handles carry a zero-value
// localBackend because they only need the channel (which lives on the
// Handle itself).
type localBackend struct {
	m   *LocalManager
	key string
	ch  chan struct{}
}

func (b *localBackend) leaderRelease(_ context.Context) {
	if b == nil || b.m == nil || b.ch == nil {
		return
	}
	b.m.mu.Lock()
	if cur, ok := b.m.flights[b.key]; ok && cur == b.ch {
		delete(b.m.flights, b.key)
	}
	close(b.ch)
	b.m.mu.Unlock()
}

func (b *localBackend) followerClose() {
	// No-op: the follower's release channel is shared with the leader;
	// the leader's leaderRelease is what closes it.
}

// Acquire returns a leader handle on the first call for opts.Key, or a
// follower handle on subsequent calls before the leader Releases. This
// mirrors the RedisManager.Acquire contract so call sites are identical.
//
// Errors are not returned: LocalManager has no transport that can fail.
// Passing an empty opts.Key is rejected with ErrNotEnabled for parity
// with RedisManager.
func (m *LocalManager) Acquire(_ context.Context, opts AcquireOpts) (*Handle, error) {
	if opts.Key == "" {
		distlockAcquireTotal.WithLabelValues("error").Inc()
		return nil, ErrNotEnabled
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 60 * time.Minute // not enforced; local mode has no TTL
	}
	m.mu.Lock()
	if m.flights == nil {
		m.flights = make(map[string]chan struct{})
	}
	if ch, ok := m.flights[opts.Key]; ok {
		// Follower: reuse the leader's release channel.
		m.mu.Unlock()
		distlockAcquireTotal.WithLabelValues("follower").Inc()
		return &Handle{
			leader:    false,
			key:       opts.Key,
			ttl:       ttl,
			scope:     normalizeLockScope(opts.Scope),
			localWait: ch,
			backend:   &localBackend{m: m, key: opts.Key, ch: ch},
		}, nil
	}
	ch := make(chan struct{})
	m.flights[opts.Key] = ch
	m.mu.Unlock()
	distlockAcquireTotal.WithLabelValues("leader").Inc()
	return &Handle{
		leader:  true,
		key:     opts.Key,
		ttl:     ttl,
		scope:   normalizeLockScope(opts.Scope),
		backend: &localBackend{m: m, key: opts.Key, ch: ch},
	}, nil
}
