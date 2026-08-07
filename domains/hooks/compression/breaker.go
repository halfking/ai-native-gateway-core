package compression

import (
	"sync"
	"time"
)

// summaryBreaker is a lightweight circuit breaker for the LLM-summary path
// (tryLLMSummary / tryLLMContextCompaction).
//
// docs/omni-ref3 C1: when the summary model is unavailable or erroring, every
// flagged request still calls it (summarymodel.Summarize), burning the summary
// model's quota and adding latency before falling back to mechanical trim. This
// breaker trips after `threshold` consecutive failures, short-circuits summary
// attempts for `cooldown`, and half-open-probes (one attempt) on cooldown
// expiry. While open, Prepare skips the LLM call and goes straight to
// mechanical compression.
//
// This is process-local state (like the per-engine breakers in the compression
// cache). A summary-model outage degrades per-process, not globally; that
// matches the existing fail-open model where a V2 read error falls back to V1.
//
// Tunables are read from env (LoadBreakerConfig) so operators can disable it
// (threshold=0) without a redeploy.
type summaryBreaker struct {
	mu            sync.Mutex
	failures      int       // consecutive failures since last success
	openedAt      time.Time // when the breaker opened (zero = closed)
	halfOpenProbe bool      // a single probe attempt is in flight
	cfg           breakerConfig
}

type breakerConfig struct {
	threshold int           // consecutive failures to trip; 0 disables the breaker
	cooldown  time.Duration // how long to stay open before a half-open probe
}

// defaultBreakerConfig is conservative: trip after 3 failures, cool down 30s.
// Tuned to absorb transient blips without letting a hard outage hammer the
// summary model. Disable via LLM_GATEWAY_SUMMARY_BREAKER_THRESHOLD=0.
func defaultBreakerConfig() breakerConfig {
	return breakerConfig{
		threshold: 3,
		cooldown:  30 * time.Second,
	}
}

func newSummaryBreaker() *summaryBreaker {
	return &summaryBreaker{cfg: defaultBreakerConfig()}
}

// allowDecide reports whether a summary attempt is allowed right now.
// Returns (allowed, halfOpen): when halfOpen is true the caller MUST report the
// outcome via RecordResult so the probe either re-closes or re-opens the breaker.
func (b *summaryBreaker) allowDecide(now time.Time) (allowed, halfOpen bool) {
	if b == nil || b.cfg.threshold <= 0 {
		return true, false // breaker disabled → always allow, no probe bookkeeping
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	// Closed (or not yet tripped): allow.
	if b.failures < b.cfg.threshold {
		return true, false
	}
	// Tripped. Are we past the cooldown? If so, allow exactly one probe.
	if now.Sub(b.openedAt) >= b.cfg.cooldown {
		if !b.halfOpenProbe {
			b.halfOpenProbe = true
			return true, true
		}
		return false, false // a probe is already in flight
	}
	return false, false // still cooling down
}

// RecordResult records the outcome of a summary attempt.
//   - success resets the failure count and closes the breaker.
//   - failure increments the count; at threshold the breaker opens (openedAt=now).
//   - a half-open probe that fails re-opens the breaker for another cooldown.
func (b *summaryBreaker) RecordResult(success bool, now time.Time) {
	if b == nil || b.cfg.threshold <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.halfOpenProbe = false // probe consumed regardless of outcome

	if success {
		b.failures = 0
		b.openedAt = time.Time{}
		return
	}
	b.failures++
	if b.failures >= b.cfg.threshold {
		b.openedAt = now
	}
}

// IsOpen is a read-only status for observability/tests (not on the hot path).
func (b *summaryBreaker) IsOpen(now time.Time) bool {
	if b == nil || b.cfg.threshold <= 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures >= b.cfg.threshold && now.Sub(b.openedAt) < b.cfg.cooldown
}
