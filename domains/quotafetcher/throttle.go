package quotafetcher

import (
	"context"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"
)

// MinIntervalThrottle serializes genuine upstream quota network calls so N
// accounts on one IP don't all fire in the same second (which got OmniRoute
// OAuth tokens revoked — see open-sse/services/quotaFetchThrottle.ts header
// referencing router-for-me/CLIProxyAPI#2385).
//
// It is NOT a coalescing layer — concurrent callers each get their own slot
// and each pay a fetch. It only paces start times. Cache hits (inside each
// fetcher) never reach this gate.
//
// Fail-open by construction: Acquire only awaits timers, so it cannot knock
// a fetcher off its fail-open path.
type MinIntervalThrottle struct {
	minInterval time.Duration
	jitter      time.Duration

	mu        sync.Mutex
	lastStart time.Time
	chain     chan struct{} // serializes callers
}

const (
	defaultMinInterval = 250 * time.Millisecond
	defaultJitter      = 120 * time.Millisecond
	maxMinInterval     = 5 * time.Second
)

// NewMinIntervalThrottle reads env LLM_GATEWAY_QUOTA_FETCH_MIN_INTERVAL_MS
// (0 = disabled). A fresh instance per Manager.
func NewMinIntervalThrottle() *MinIntervalThrottle {
	interval := defaultMinInterval
	if v := os.Getenv("LLM_GATEWAY_QUOTA_FETCH_MIN_INTERVAL_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			interval = time.Duration(ms) * time.Millisecond
			if interval > maxMinInterval {
				interval = maxMinInterval
			}
		}
	}
	return &MinIntervalThrottle{
		minInterval: interval,
		jitter:      defaultJitter,
		chain:       make(chan struct{}, 1),
	}
}

// Acquire blocks until the caller's slot is ready. The first caller is never
// delayed; subsequent callers wait at least minInterval (+jitter) after the
// previous caller's start. No-op when minInterval <= 0.
func (t *MinIntervalThrottle) Acquire(ctx context.Context) {
	if t.minInterval <= 0 {
		return
	}
	// Serialize: take the token, release after pacing.
	select {
	case t.chain <- struct{}{}:
		defer func() { <-t.chain }()
	case <-ctx.Done():
		return
	}

	t.mu.Lock()
	wait := time.Duration(0)
	if !t.lastStart.IsZero() {
		elapsed := time.Since(t.lastStart)
		target := t.minInterval
		if t.jitter > 0 {
			target += time.Duration(rand.Int63n(int64(t.jitter)))
		}
		if elapsed < target {
			wait = target - elapsed
		}
	}
	t.lastStart = time.Now()
	t.mu.Unlock()

	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
	}
}
