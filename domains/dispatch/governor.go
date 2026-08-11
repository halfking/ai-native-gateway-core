package dispatch

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// A Governor paces how fast a credential's forwarder releases requests to the
// upstream. One Governor instance per credential. The forwarder calls Acquire
// before forwarding and Release after completion.
//
// Implementations:
//   - concurrencyGovernor: weighted semaphore (in-flight hard cap).
//   - rpmGovernor: requests-per-minute token bucket.
//   - tpmGovernor: tokens-per-minute token bucket (Acquire takes the
//     request's estimated prompt tokens).
//
// disabled / unlimited credentials use a no-op governor.
type Governor interface {
	Mode() string
	// Acquire blocks until the request may proceed, or ctx/giveUp fires.
	Acquire(ctx context.Context, qr *QueuedRequest, giveUp time.Time) error
	// Release frees an in-flight slot (no-op for rate-based governors).
	Release(qr *QueuedRequest)
}

// newGovernor builds the governor for a credential based on its mode/limits.
func newGovernor(ref CredentialRef) Governor {
	switch ref.ConcurrencyMode {
	case ModeRPM:
		if ref.RPMLimit <= 0 {
			return newNoopGovernor()
		}
		return newRPMGovernor(ref.RPMLimit)
	case ModeTPM:
		if ref.TPMLimit <= 0 {
			return newNoopGovernor()
		}
		return newTPMGovernor(ref.TPMLimit)
	case ModeDisabled:
		return newNoopGovernor()
	default: // ModeConcurrency or unset
		if ref.ConcurrencyLimit <= 0 {
			return newNoopGovernor()
		}
		return newConcurrencyGovernor(ref.ConcurrencyLimit)
	}
}

// ── concurrency: weighted semaphore ────────────────────────────────────────

type concurrencyGovernor struct {
	cap   int64
	used  atomic.Int64
	mode  string
}

func newConcurrencyGovernor(capacity int) *concurrencyGovernor {
	g := &concurrencyGovernor{cap: int64(capacity), mode: ModeConcurrency}
	return g
}
func (g *concurrencyGovernor) Mode() string { return g.mode }
func (g *concurrencyGovernor) Acquire(ctx context.Context, _ *QueuedRequest, giveUp time.Time) error {
	for {
		used := g.used.Load()
		if used < g.cap && g.used.CompareAndSwap(used, used+1) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(giveUp) {
			return errPaceTimeout
		}
		// Brief sleep; the ctx/giveUp checks bound the wait.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}
func (g *concurrencyGovernor) Release(_ *QueuedRequest) {
	for {
		v := g.used.Load()
		if v <= 0 {
			return
		}
		if g.used.CompareAndSwap(v, v-1) {
			return
		}
	}
}

// ── rpm: requests-per-minute token bucket ──────────────────────────────────

// rpmGovernor replenishes 1 token every (60s / rpm). Burst = rpm (i.e. the
// full per-minute allowance is available instantly, then paced). This matches
// the vendor semantics ("N requests per minute") and smooths bursts across
// the minute rather than firing them all at once after a refill.
type rpmGovernor struct {
	mu       sync.Mutex
	tokens   float64       // available tokens (fractional for smooth refill)
	rpm      int
	last     time.Time
	interval time.Duration // 60s / rpm
}

func newRPMGovernor(rpm int) *rpmGovernor {
	return &rpmGovernor{
		tokens:   float64(rpm),
		rpm:      rpm,
		last:     time.Now(),
		interval: time.Duration(float64(time.Second) * 60.0 / float64(rpm)),
	}
}
func (g *rpmGovernor) Mode() string { return ModeRPM }
func (g *rpmGovernor) Acquire(ctx context.Context, _ *QueuedRequest, giveUp time.Time) error {
	for {
		g.mu.Lock()
		now := time.Now()
		// Refill: one token per interval elapsed.
		elapsed := now.Sub(g.last)
		refill := float64(elapsed) / float64(g.interval)
		if refill > 0 {
			g.tokens += refill
			if g.tokens > float64(g.rpm) {
				g.tokens = float64(g.rpm) // cap at burst = rpm
			}
			g.last = now
		}
		if g.tokens >= 1 {
			g.tokens -= 1
			g.mu.Unlock()
			return nil
		}
		// Need to wait for the next whole token.
		need := 1 - g.tokens
		wait := time.Duration(need * float64(g.interval))
		g.mu.Unlock()

		if now.After(giveUp) {
			return errPaceTimeout
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
			// loop and re-check (token may now be available)
		}
	}
}
func (g *rpmGovernor) Release(_ *QueuedRequest) {} // rate-based: nothing to release

// ── tpm: tokens-per-minute token bucket ────────────────────────────────────

// tpmGovernor refills tpm/60 tokens per second (cap = tpm). Acquire charges
// the request's EstimatedTokens; when unknown (0), a conservative fixed cost
// (defaultTokenEstimate) is charged so unknown-size requests are still paced.
type tpmGovernor struct {
	mu       sync.Mutex
	tokens   float64
	tpm      int
	last     time.Time
	perSec   float64
}

const defaultTokenEstimate = 800 // conservative cost when EstimatedTokens unknown

func newTPMGovernor(tpm int) *tpmGovernor {
	return &tpmGovernor{
		tokens: float64(tpm),
		tpm:    tpm,
		last:   time.Now(),
		perSec: float64(tpm) / 60.0,
	}
}
func (g *tpmGovernor) Mode() string { return ModeTPM }
func (g *tpmGovernor) Acquire(ctx context.Context, qr *QueuedRequest, giveUp time.Time) error {
	cost := qr.EstimatedTokens
	if cost <= 0 {
		cost = defaultTokenEstimate
	}
	for {
		g.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(g.last).Seconds()
		if elapsed > 0 {
			g.tokens += elapsed * g.perSec
			if g.tokens > float64(g.tpm) {
				g.tokens = float64(g.tpm)
			}
			g.last = now
		}
		if g.tokens >= float64(cost) {
			g.tokens -= float64(cost)
			g.mu.Unlock()
			return nil
		}
		need := float64(cost) - g.tokens
		wait := time.Duration((need / g.perSec) * float64(time.Second))
		g.mu.Unlock()

		if now.After(giveUp) {
			return errPaceTimeout
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
func (g *tpmGovernor) Release(_ *QueuedRequest) {}

// ── disabled / unlimited ───────────────────────────────────────────────────

type noopGovernor struct{}

func newNoopGovernor() *noopGovernor      { return &noopGovernor{} }
func (g *noopGovernor) Mode() string      { return ModeDisabled }
func (g *noopGovernor) Acquire(context.Context, *QueuedRequest, time.Time) error { return nil }
func (g *noopGovernor) Release(*QueuedRequest)                                  {}
