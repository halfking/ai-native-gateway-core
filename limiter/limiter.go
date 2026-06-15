// Package limiter implements a four-layer concurrency controller for LLM
// gateway data-plane requests.
//
// Layers (from outermost to innermost):
//
//	Global     — overall max concurrency across all providers (default 1000)
//	Pool       — per-provider max concurrency (default 100)
//	Credential — per-credential max concurrency (default 50)
//	Identity   — per-identity per-credential max concurrency (default 10)
//
// Each layer is a weighted semaphore. Acquire/Release are O(1) atomic
// operations. Shrink reduces capacity on rate-limit signals. Recover
// gradually restores capacity over time.
package limiter

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Default limits
// ---------------------------------------------------------------------------

const (
	DefaultGlobalLimit     = 1000
	DefaultPoolLimit       = 100
	DefaultCredentialLimit = 50
	DefaultIdentityLimit   = 10
)

const (
	shrinkRecoveryInterval = 5 * time.Minute
	shrinkRecoveryFactor   = 0.5 // recover 50% of shrink every interval
	fullRecoveryCycles     = 3   // 3 intervals = 15 min for full recovery
)

// ---------------------------------------------------------------------------
// Semaphore — weighted counting semaphore
// ---------------------------------------------------------------------------

// Semaphore provides acquire/release with dynamic capacity.
type Semaphore struct {
	name     string
	capacity atomic.Int64
	used     atomic.Int64
}

// NewSemaphore creates a new semaphore with the given capacity.
func NewSemaphore(name string, capacity int) *Semaphore {
	s := &Semaphore{name: name}
	s.capacity.Store(int64(capacity))
	return s
}

// Capacity returns the current capacity.
func (s *Semaphore) Capacity() int { return int(s.capacity.Load()) }

// Used returns the currently used count.
func (s *Semaphore) Used() int { return int(s.used.Load()) }

// Available returns the remaining capacity.
func (s *Semaphore) Available() int { return s.Capacity() - s.Used() }

// TryAcquire attempts to acquire a token without blocking.
func (s *Semaphore) TryAcquire() bool {
	for {
		used := s.used.Load()
		cap := s.capacity.Load()
		if used >= cap {
			return false
		}
		if s.used.CompareAndSwap(used, used+1) {
			return true
		}
	}
}

// Acquire blocks until a token is available or the context is cancelled.
func (s *Semaphore) Acquire(ctx context.Context) error {
	for {
		if s.TryAcquire() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TryAcquireWithTimeout attempts to acquire within the given timeout.
func (s *Semaphore) TryAcquireWithTimeout(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.TryAcquire() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// Release releases a token.
func (s *Semaphore) Release() {
	for {
		v := s.used.Load()
		if v <= 0 {
			return
		}
		if s.used.CompareAndSwap(v, v-1) {
			return
		}
	}
}

// Shrink reduces capacity by the given factor (0 < factor < 1).
// The new capacity is: max(1, ceil(capacity * factor)).
func (s *Semaphore) Shrink(factor float64) {
	for {
		oldCap := s.capacity.Load()
		newCap := int64(math.Max(1, math.Ceil(float64(oldCap)*factor)))
		if s.capacity.CompareAndSwap(oldCap, newCap) {
			slog.Warn("semaphore shrunk",
				"name", s.name,
				"old_capacity", oldCap,
				"new_capacity", newCap,
				"factor", factor,
			)
			return
		}
	}
}

// RecoverStep increases capacity by one step toward the target.
func (s *Semaphore) RecoverStep(targetCapacity int) {
	for {
		oldCap := s.capacity.Load()
		target := int64(targetCapacity)
		if oldCap >= target {
			return
		}
		recovery := int64(math.Ceil(float64(target-oldCap) * shrinkRecoveryFactor))
		if recovery < 1 {
			recovery = 1
		}
		newCap := oldCap + recovery
		if newCap > target {
			newCap = target
		}
		if s.capacity.CompareAndSwap(oldCap, newCap) {
			slog.Info("semaphore recovery",
				"name", s.name,
				"old_capacity", oldCap,
				"new_capacity", newCap,
				"target", targetCapacity,
			)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Limiter — four-layer concurrency controller
// ---------------------------------------------------------------------------

// Limiter manages concurrency across five layers.
type Limiter struct {
	globalLimit     int
	poolLimit       int
	credentialLimit int
	identityLimit   int

	global   *Semaphore
	pools    map[int]*Semaphore // providerID → semaphore
	creds    map[string]*Semaphore // "providerID/credentialID" → semaphore
	idents   map[string]*Semaphore // "providerID/credentialID/identityHash" → semaphore
	keys     map[int]*Semaphore    // keyID → per-key semaphore (limit from DB)

	mu          sync.RWMutex
	stopCh      chan struct{}
}

// New creates a new limiter with default limits.
func New() *Limiter {
	return NewWithLimits(DefaultGlobalLimit, DefaultPoolLimit, DefaultCredentialLimit, DefaultIdentityLimit)
}

// NewWithLimits creates a new limiter with custom limits.
func NewWithLimits(global, pool, credential, identity int) *Limiter {
	l := &Limiter{
		globalLimit:     global,
		poolLimit:       pool,
		credentialLimit: credential,
		identityLimit:   identity,
		global:          NewSemaphore("global", global),
		pools:           make(map[int]*Semaphore),
		creds:           make(map[string]*Semaphore),
		idents:          make(map[string]*Semaphore),
		keys:            make(map[int]*Semaphore),
		stopCh:          make(chan struct{}),
	}
	go l.recoveryLoop()
	return l
}

// Stop stops the recovery loop.
func (l *Limiter) Stop() {
	close(l.stopCh)
}

// Global returns the global semaphore.
func (l *Limiter) Global() *Semaphore { return l.global }

// Pool returns the pool-level semaphore for the given provider.
func (l *Limiter) Pool(providerID int) *Semaphore {
	l.mu.RLock()
	s, ok := l.pools[providerID]
	l.mu.RUnlock()
	if ok {
		return s
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok = l.pools[providerID]; ok {
		return s
	}
	s = NewSemaphore(fmt.Sprintf("pool_%d", providerID), l.poolLimit)
	l.pools[providerID] = s
	return s
}

// Credential returns the credential-level semaphore.
func (l *Limiter) Credential(providerID, credentialID int) *Semaphore {
	key := fmt.Sprintf("%d/%d", providerID, credentialID)
	l.mu.RLock()
	s, ok := l.creds[key]
	l.mu.RUnlock()
	if ok {
		return s
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok = l.creds[key]; ok {
		return s
	}
	s = NewSemaphore(fmt.Sprintf("cred_%s", key), l.credentialLimit)
	l.creds[key] = s
	return s
}

// Identity returns the identity-level semaphore.
func (l *Limiter) Identity(providerID, credentialID int, identityHash string) *Semaphore {
	key := fmt.Sprintf("%d/%d/%s", providerID, credentialID, identityHash)
	l.mu.RLock()
	s, ok := l.idents[key]
	l.mu.RUnlock()
	if ok {
		return s
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok = l.idents[key]; ok {
		return s
	}
	s = NewSemaphore(fmt.Sprintf("ident_%s", key), l.identityLimit)
	l.idents[key] = s
	return s
}

// Key returns the per-key semaphore for the given API key ID.
// The limit is dynamic (from DB per-key setting) so capacity is passed in.
func (l *Limiter) Key(keyID int, limit int) *Semaphore {
	l.mu.RLock()
	s, ok := l.keys[keyID]
	l.mu.RUnlock()
	if ok {
		return s
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok = l.keys[keyID]; ok {
		return s
	}
	s = NewSemaphore(fmt.Sprintf("key_%d", keyID), limit)
	l.keys[keyID] = s
	return s
}

// AcquireAll attempts to acquire tokens across all five layers.
// Returns a ReleaseFunc that releases all acquired tokens.
// If any layer is saturated, previously acquired tokens are released.
//
// The 5th layer (per-key) is non-blocking: if the key's concurrent limit is
// reached, the request bypasses this check and continues. This matches the
// identity-layer behaviour (soft cap).
func (l *Limiter) AcquireAll(ctx context.Context, providerID, credentialID int, identityHash string, keyID int, keyConcurrentLimit int) (ReleaseFunc, error) {
	// Acquire global (blocking with context)
	if err := l.global.Acquire(ctx); err != nil {
		return nil, fmt.Errorf("global limit: %w", err)
	}

	// Acquire pool (blocking with context)
	pool := l.Pool(providerID)
	if err := pool.Acquire(ctx); err != nil {
		l.global.Release()
		return nil, fmt.Errorf("pool limit: %w", err)
	}

	// Acquire credential (blocking with context)
	cred := l.Credential(providerID, credentialID)
	if err := cred.Acquire(ctx); err != nil {
		pool.Release()
		l.global.Release()
		return nil, fmt.Errorf("credential limit: %w", err)
	}

	// Acquire identity (non-blocking — identity limit is a soft cap)
	ident := l.Identity(providerID, credentialID, identityHash)
	identAcquired := ident.TryAcquire()
	if !identAcquired {
		slog.Warn("identity limit reached, bypassing",
			"provider", providerID,
			"credential", credentialID,
			"identity", identityHash,
			"used", ident.Used(),
			"capacity", ident.Capacity(),
		)
	}

	// Acquire per-key concurrent slot (non-blocking — soft cap from DB).
	// keyConcurrentLimit == 0 means "unlimited" → skip per-key check entirely.
	var keyAcquired bool
	var keySem *Semaphore
	if keyID > 0 && keyConcurrentLimit > 0 {
		keySem = l.Key(keyID, keyConcurrentLimit)
		keyAcquired = keySem.TryAcquire()
		if !keyAcquired {
			slog.Warn("per-key concurrent limit reached, bypassing",
				"key_id", keyID,
				"used", keySem.Used(),
				"capacity", keySem.Capacity(),
			)
		}
	}

	return func() {
		if keyAcquired && keySem != nil {
			keySem.Release()
		}
		if identAcquired {
			ident.Release()
		}
		cred.Release()
		pool.Release()
		l.global.Release()
	}, nil
}

// Shrink reduces credential capacity on rate-limit events.
func (l *Limiter) Shrink(providerID, credentialID int) {
	l.Credential(providerID, credentialID).Shrink(0.7)
}

// ReleaseFunc releases all previously acquired concurrency tokens.
type ReleaseFunc func()

// Stats returns diagnostic information for all layers.
func (l *Limiter) Stats() map[string]any {
	l.mu.RLock()
	defer l.mu.RUnlock()

	poolEntries := make([]map[string]any, 0, len(l.pools))
	for id, s := range l.pools {
		poolEntries = append(poolEntries, map[string]any{
			"provider_id": id,
			"capacity":    s.Capacity(),
			"used":        s.Used(),
			"available":   s.Available(),
		})
	}

	credEntries := make([]map[string]any, 0, len(l.creds))
	for key, s := range l.creds {
		credEntries = append(credEntries, map[string]any{
			"key":       key,
			"capacity":  s.Capacity(),
			"used":      s.Used(),
			"available": s.Available(),
		})
	}

	return map[string]any{
		"global": map[string]int{
			"capacity":  l.global.Capacity(),
			"used":      l.global.Used(),
			"available": l.global.Available(),
		},
		"pools":       poolEntries,
		"credentials": credEntries,
		"identity_count": len(l.idents),
	}
}

func (l *Limiter) recoveryLoop() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("limiter recoveryLoop panic", "recover", r)
		}
	}()
	ticker := time.NewTicker(shrinkRecoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.recoveryStep()
		case <-l.stopCh:
			return
		}
	}
}

func (l *Limiter) recoveryStep() {
	l.mu.RLock()
	defer l.mu.RUnlock()

	for _, s := range l.pools {
		s.RecoverStep(l.poolLimit)
	}
	for _, s := range l.creds {
		s.RecoverStep(l.credentialLimit)
	}
}
