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
package credential

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/ratelimit" // AUDIT-2: 限流总开关
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

	// OPT-2 (2026-07-12): bounded wait for the blocking semaphore layers.
	// The previous implementation blocked until ctx.Done(); with no
	// upstream-cancel path, an unbounded semaphore wait could pin the
	// request for the entire upstream timeout (120s) and starve every
	// other request on the same executor goroutine. 5s is well below the
	// shortest sync-retry interval (1s) so a saturated layer trips well
	// before the executor times out, allowing failover to the next
	// candidate within one sync-retry round.
	acquireWaitTimeout = 5 * time.Second

	defaultIdentityMaxEntries = 10000
	maxIdentityEntries        = 100000
	defaultIdentityIdleTTL    = 30 * time.Minute
	maxIdentityIdleTTL        = 24 * time.Hour
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
		if err := ctx.Err(); err != nil {
			return err
		}
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

	global *Semaphore
	pools  map[int]*Semaphore        // providerID → semaphore
	creds  map[string]*Semaphore     // "providerID/credentialID" → semaphore
	idents map[string]*identityEntry // "providerID/credentialID/identityHash" → entry
	keys   map[int]*Semaphore        // keyID → per-key semaphore (limit from DB)

	identityMaxEntries int
	identityIdleTTL    time.Duration
	identityEvictions  atomic.Uint64

	// RPM uses Redis across instances when configured and memory otherwise.
	rpmLimiter RPMLimiter

	mu       sync.RWMutex
	stopCh   chan struct{}
	stopOnce sync.Once
	workers  sync.WaitGroup
}

type identityEntry struct {
	sem      *Semaphore
	lastUsed time.Time
}

// rpmWindow is a 60-second sliding window of acquire timestamps. Stale
// entries (>60s) are pruned on every Check, keeping memory bounded.
type rpmWindow struct {
	timestamps []float64 // unix-seconds
}

// NewLimiter creates a new limiter with default limits.
//
// Renamed from `New` to `NewLimiter` during the 2026-06-26 migration
// to avoid a name collision with breaker.New (also in this package).
func NewLimiter() *Limiter {
	return NewWithLimits(DefaultGlobalLimit, DefaultPoolLimit, DefaultCredentialLimit, DefaultIdentityLimit)
}

// NewWithLimits creates a new limiter with custom limits.
func NewWithLimits(global, pool, credential, identity int) *Limiter {
	l := &Limiter{
		globalLimit:        global,
		poolLimit:          pool,
		credentialLimit:    credential,
		identityLimit:      identity,
		global:             NewSemaphore("global", global),
		pools:              make(map[int]*Semaphore),
		creds:              make(map[string]*Semaphore),
		idents:             make(map[string]*identityEntry),
		keys:               make(map[int]*Semaphore),
		identityMaxEntries: envPositiveInt("LLM_GATEWAY_IDENTITY_LIMITER_MAX_ENTRIES", defaultIdentityMaxEntries),
		identityIdleTTL:    envPositiveDuration("LLM_GATEWAY_IDENTITY_LIMITER_IDLE_TTL", defaultIdentityIdleTTL),
		rpmLimiter:         NewRPMLimiterFromEnv(),
		stopCh:             make(chan struct{}),
	}
	l.workers.Add(1)
	go l.recoveryLoop()
	return l
}

func envPositiveInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > maxIdentityEntries {
		return maxIdentityEntries
	}
	return value
}

func envPositiveDuration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(name))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > maxIdentityIdleTTL {
		return maxIdentityIdleTTL
	}
	return value
}

// CheckCredentialRPM records a credential acquire and returns true if
// the per-credential RPM cap (60-second sliding window) is not yet hit.
//
// 2026-07-15: limit==0 or nil = unlimited (default for paid credentials).
// Returns false when the credential has exceeded its rpm_limit in the
// last 60s, signalling the executor to failover to the next candidate.
// Memory bound: per-credential window holds at most `limit` floats (oldest
// entries are pruned on every Check).
func (l *Limiter) CheckCredentialRPM(providerID, credentialID int, limit *int) bool {
	if limit == nil || *limit <= 0 {
		return true
	}
	allowed, _, err := l.rpmLimiter.CheckAndReserve(context.Background(), providerID, credentialID, *limit)
	return err == nil && allowed
}

// Stop stops the recovery loop.
func (l *Limiter) Stop() {
	l.stopOnce.Do(func() {
		close(l.stopCh)
		l.workers.Wait()
	})
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
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry, ok := l.idents[key]; ok {
		entry.lastUsed = now
		return entry.sem
	}
	if !l.makeIdentityRoomLocked(now) {
		return NewSemaphore(fmt.Sprintf("ident_ephemeral_%s", key), l.identityLimit)
	}
	entry := &identityEntry{
		sem:      NewSemaphore(fmt.Sprintf("ident_%s", key), l.identityLimit),
		lastUsed: now,
	}
	l.idents[key] = entry
	return entry.sem
}

func (l *Limiter) makeIdentityRoomLocked(now time.Time) bool {
	l.cleanupIdentitiesLocked(now)
	for len(l.idents) >= l.identityMaxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range l.idents {
			if entry.sem.Used() != 0 {
				continue
			}
			if oldestKey == "" || entry.lastUsed.Before(oldest) {
				oldestKey, oldest = key, entry.lastUsed
			}
		}
		if oldestKey == "" {
			return false
		}
		delete(l.idents, oldestKey)
		l.identityEvictions.Add(1)
	}
	return true
}

func (l *Limiter) cleanupIdentitiesLocked(now time.Time) {
	cutoff := now.Add(-l.identityIdleTTL)
	for key, entry := range l.idents {
		if len(l.idents) <= l.identityMaxEntries && entry.lastUsed.After(cutoff) {
			continue
		}
		if entry.sem.Used() != 0 {
			continue
		}
		delete(l.idents, key)
		l.identityEvictions.Add(1)
	}
}

// Key returns the per-key semaphore for the given API key ID.
// The limit is dynamic (from DB per-key setting) so capacity is passed in.
//
// Because the per-key rate_limit_concurrent can be changed at runtime via the
// admin API, an already-cached semaphore whose capacity diverges from the
// caller-supplied limit is resized in place. capacity is an atomic.Int64, so
// Store is safe vs concurrent Acquire/Release; Shrink/RecoverStep already
// mutate it the same way. A non-positive limit means "unlimited" — the per-key
// layer is skipped by AcquireAll in that case, so we leave the semaphore as-is.
func (l *Limiter) Key(keyID int, limit int) *Semaphore {
	l.mu.RLock()
	s, ok := l.keys[keyID]
	l.mu.RUnlock()
	if ok {
		if limit > 0 && s.Capacity() != limit {
			s.capacity.Store(int64(limit))
		}
		return s
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok = l.keys[keyID]; ok {
		if limit > 0 && s.Capacity() != limit {
			s.capacity.Store(int64(limit))
		}
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
//
// AUDIT-2 (2026-07-12): if rate_limit.enabled is OFF (per settings.Global
// + ratelimit/gate.go), AcquireAll returns a no-op ReleaseFunc and the
// request proceeds without any concurrency check. This matches the user
// semantic: "限流降级模块关闭时不限制并发". The release is still safe to
// call (returns immediately on no-op state).
//
// OPT-2 (2026-07-12): each blocking layer (global / pool / credential) is
// bounded by an independent wait timeout so that a single saturated layer
// does not pin the request thread for the entire lifetime of the upstream
// call. The previous implementation blocked until ctx.Done(), which meant
// CLI / internal callers with no deadline would wait indefinitely. The
// bounded timeout matches the executor's expectation: when a layer is
// saturated beyond the budget, return an error so the executor can move
// on to the next candidate.
func (l *Limiter) AcquireAll(ctx context.Context, providerID, credentialID int, identityHash string, keyID int, keyConcurrentLimit int, rpmLimit *int) (ReleaseFunc, error) {
	// AUDIT-2: 限流总开关关闭 → 直接放行，返回 no-op release。
	if !ratelimit.IsRateLimitEnabled() {
		return func() {}, nil
	}

	// OPT-2: cap the blocking wait per layer. ctx may have no deadline
	// (CLI, internal call), so derive a child context that fires after
	// acquireWaitTimeout. The original ctx still wins on early cancel.
	waitCtx, waitCancel := context.WithTimeout(ctx, acquireWaitTimeout)
	defer waitCancel()

	// Acquire global (blocking with context, OPT-2 bounded)
	if err := l.global.Acquire(waitCtx); err != nil {
		return nil, fmt.Errorf("global limit: %w", err)
	}

	// Acquire pool (blocking with context, OPT-2 bounded)
	pool := l.Pool(providerID)
	if err := pool.Acquire(waitCtx); err != nil {
		l.global.Release()
		return nil, fmt.Errorf("pool limit: %w", err)
	}

	// Acquire credential (blocking with context, OPT-2 bounded)
	cred := l.Credential(providerID, credentialID)
	if err := cred.Acquire(waitCtx); err != nil {
		pool.Release()
		l.global.Release()
		return nil, fmt.Errorf("credential limit: %w", err)
	}

	// Reserve RPM only after the request owns a credential slot. Recording
	// before semaphore acquisition would charge requests that timed out or
	// failed at an outer concurrency layer, causing false rate-limit failures.
	// The reservation is atomic in the selected RPM implementation; on
	// rejection, release all slots
	// acquired so far and let the executor fail over to another candidate.
	if !l.CheckCredentialRPM(providerID, credentialID, rpmLimit) {
		cred.Release()
		pool.Release()
		l.global.Release()
		return nil, fmt.Errorf("credential rpm limit (provider=%d credential=%d limit=%d)", providerID, credentialID, derefInt(rpmLimit))
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

	var releaseOnce sync.Once
	return func() {
		releaseOnce.Do(func() {
			if keyAcquired && keySem != nil {
				keySem.Release()
			}
			if identAcquired {
				ident.Release()
			}
			cred.Release()
			pool.Release()
			l.global.Release()
		})
	}, nil
}

// AcquireAllNoCredLayer is the dispatch-path variant of AcquireAll: it
// enforces only the OUTER layers (global → pool → identity[soft] → per-key
// [soft]) and intentionally skips the credential semaphore and the per-credential
// RPM check. The credential-level concurrency/rate gating is owned by the
// dispatch governor (domains/dispatch), which is the single authority for the
// credential's concurrency_mode. Calling both would double-count and could
// over-limit (the Limiter's credential sem defaults to 50 regardless of the
// per-credential concurrency_limit / rpm_limit / tpm_limit).
//
// Used by executor_dispatch.go when dispatch_v2 is enabled. Same kill-switch
// and bounded-wait semantics as AcquireAll.
func (l *Limiter) AcquireAllNoCredLayer(ctx context.Context, providerID, credentialID int, identityHash string, keyID int, keyConcurrentLimit int) (ReleaseFunc, error) {
	if !ratelimit.IsRateLimitEnabled() {
		return func() {}, nil
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, acquireWaitTimeout)
	defer waitCancel()

	if err := l.global.Acquire(waitCtx); err != nil {
		return nil, fmt.Errorf("global limit: %w", err)
	}
	pool := l.Pool(providerID)
	if err := pool.Acquire(waitCtx); err != nil {
		l.global.Release()
		return nil, fmt.Errorf("pool limit: %w", err)
	}

	// Identity (soft cap, non-blocking).
	ident := l.Identity(providerID, credentialID, identityHash)
	identAcquired := ident.TryAcquire()
	if !identAcquired {
		slog.Warn("identity limit reached, bypassing",
			"provider", providerID, "credential", credentialID, "identity", identityHash)
	}

	// Per-key (soft cap, non-blocking).
	var keyAcquired bool
	var keySem *Semaphore
	if keyID > 0 && keyConcurrentLimit > 0 {
		keySem = l.Key(keyID, keyConcurrentLimit)
		keyAcquired = keySem.TryAcquire()
		if !keyAcquired {
			slog.Warn("per-key concurrent limit reached, bypassing", "key_id", keyID)
		}
	}

	var releaseOnce sync.Once
	return func() {
		releaseOnce.Do(func() {
			if keyAcquired && keySem != nil {
				keySem.Release()
			}
			if identAcquired {
				ident.Release()
			}
			pool.Release()
			l.global.Release()
		})
	}, nil
}

// derefInt safely dereferences a *int (e.g. Candidate.RPMLimit which may
// be nil for paid credentials). Returns 0 when nil so the limiter treats
// 0 as "unlimited" everywhere.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
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

	// Per-API-key semaphores. Exposing used/capacity here is what makes a
	// leaked used-count observable: if a key shows used > 0 while no requests
	// are in flight, the ReleaseFunc was not invoked for some acquire. Without
	// this, per-key concurrency leaks are invisible (Stats previously only
	// returned identity_count).
	keyEntries := make([]map[string]any, 0, len(l.keys))
	for keyID, s := range l.keys {
		keyEntries = append(keyEntries, map[string]any{
			"key_id":    keyID,
			"capacity":  s.Capacity(),
			"used":      s.Used(),
			"available": s.Available(),
		})
	}

	// Per-identity (providerID/credentialID/identityHash) semaphores. Same
	// rationale as keys: surface used so soft-cap saturation is observable.
	identEntries := make([]map[string]any, 0, len(l.idents))
	for k, entry := range l.idents {
		s := entry.sem
		identEntries = append(identEntries, map[string]any{
			"identity":  k,
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
		"pools":              poolEntries,
		"credentials":        credEntries,
		"keys":               keyEntries,
		"identities":         identEntries,
		"identity_count":     len(l.idents),
		"identity_evictions": l.identityEvictions.Load(),
	}
}

func (l *Limiter) recoveryLoop() {
	defer l.workers.Done()
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
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupIdentitiesLocked(time.Now())

	for _, s := range l.pools {
		s.RecoverStep(l.poolLimit)
	}
	for _, s := range l.creds {
		s.RecoverStep(l.credentialLimit)
	}
}

// ---------------------------------------------------------------------------
// Pressure Signal - Phase 2.1
// ---------------------------------------------------------------------------

// GetPressure 返回指定 credential 的 Limiter 压力（0-1）。
// 压力 = max(各层 Used / Capacity)；返回 0 表示无压力或无限制。
//
// 该值由 Router.applyPressurePenalty 在每次路由选择时读取，用于给候选
// 节点施加权重惩罚（见 executors/router.go）。因此必须始终反映各层
// semaphore 的实时占用——一个刚刚 Release 了 token 的 credential 必须
// 立即降权，否则会被继续惩罚，违背 pressure-aware routing 的目的。
//
// 参数:
//   - credentialID: credential ID (Layer 2)
//   - poolID: provider pool ID (Layer 1)
//   - identityKey: 身份标识（Layer 3，可选）
//
// 返回最严重层的压力值（0-1）。
//
// 历史：c6842218 曾为此方法引入 5 秒 TTL 缓存以降低高频路由的开销，但
// Release()/TryAcquire() 不会失效缓存，导致压力值最长 5 秒不反映真实
// 占用。calculatePressure 全部是 atomic.Int64 读取 + 少量除法，实测远
// 低于路由其它开销（Redis FpSlot 查询、权重排序），缓存收益不抵正确性
// 损失，已于 2026-07-27 移除。不要再加回 TTL 缓存；如需降开销，应从
// semaphore 内部读取路径优化，而非在调用方缓存语义值。
func (l *Limiter) GetPressure(
	credentialID int,
	poolID int,
	identityKey string,
) float64 {
	return l.calculatePressure(credentialID, poolID, identityKey)
}

// calculatePressure reads the live atomic counters of every semaphore layer
// and returns the highest Used/Capacity ratio. No caching — callers (Router)
// depend on the value reflecting the current occupancy.
func (l *Limiter) calculatePressure(
	credentialID int,
	poolID int,
	identityKey string,
) float64 {
	var maxPressure float64

	// Layer 0: Global
	if l.global != nil {
		cap := l.global.Capacity()
		if cap > 0 {
			used := l.global.Used()
			pressure := float64(used) / float64(cap)
			if pressure > maxPressure {
				maxPressure = pressure
			}
		}
	}

	// Layer 1: Pool
	l.mu.RLock()
	pool, hasPool := l.pools[poolID]
	l.mu.RUnlock()
	if hasPool {
		cap := pool.Capacity()
		if cap > 0 {
			used := pool.Used()
			pressure := float64(used) / float64(cap)
			if pressure > maxPressure {
				maxPressure = pressure
			}
		}
	}

	// Layer 2: Credential (key format: "providerID/credentialID")
	credKey := fmt.Sprintf("%d/%d", poolID, credentialID)
	l.mu.RLock()
	cred, hasCred := l.creds[credKey]
	l.mu.RUnlock()
	if hasCred {
		cap := cred.Capacity()
		if cap > 0 {
			used := cred.Used()
			pressure := float64(used) / float64(cap)
			if pressure > maxPressure {
				maxPressure = pressure
			}
		}
	}

	// Layer 3: Identity（可选）
	if identityKey != "" {
		l.mu.RLock()
		identity, hasIdentity := l.idents[identityKey]
		l.mu.RUnlock()
		if hasIdentity {
			sem := identity.sem
			cap := sem.Capacity()
			if cap > 0 {
				used := sem.Used()
				pressure := float64(used) / float64(cap)
				if pressure > maxPressure {
					maxPressure = pressure
				}
			}
		}
	}

	// 压力不会超过 1.0
	if maxPressure > 1.0 {
		maxPressure = 1.0
	}

	return maxPressure
}
