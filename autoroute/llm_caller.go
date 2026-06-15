package autoroute

// llm_caller.go — LLM caller abstraction for the fallback classifier.
//
// This file provides:
//   - LLMCaller interface (decoupled from any specific LLM SDK)
//   - DisabledCaller (default; returns error so LLM fallback is no-op)
//   - NoopCaller (always returns "chat" — for safe smoke tests)
//   - WithCircuitBreaker wrapper to prevent hammering a flaky LLM
//
// Wiring rule: production should use DisabledCaller (no LLM call) for
// the auto-route classifier — the heuristic + pattern layer is
// sufficient and avoids an external dependency on the request path.
// The LLM fallback exists for cases where the heuristic returns very
// low confidence AND the request includes a long/ambiguous prompt
// that needs semantic understanding.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrLLMDisabled is returned by DisabledCaller and surfaced through
// the LLM call site to keep the fallback path non-blocking.
var ErrLLMDisabled = errors.New("autoroute: LLM caller disabled")

// ErrLLMCircuitOpen is returned when the circuit breaker is open
// (too many recent failures). Decider logs and falls back to the
// heuristic result.
var ErrLLMCircuitOpen = errors.New("autoroute: LLM circuit breaker open")

// RecordLLMMetricCall indirection. Wired by main.go (or init in tests)
// to forward LLM call metrics to the telemetry package. Defaults to
// a no-op so the autoroute package doesn't import telemetry (which
// would invert the existing dependency graph).
var RecordLLMMetricCall = func(outcome string, latency time.Duration) {}

// RecordLLMCircuitBreakerState indirection. See RecordLLMMetricCall.
var RecordLLMCircuitBreakerState = func(consecutive int, open bool) {}

// LLMCaller is the minimal interface the LLMFallbackClassifier
// depends on. Implementations should be safe for concurrent use.
type LLMCaller interface {
	// Call invokes the LLM with the given prompt and returns the raw
	// text response (typically a single task-type string). The
	// implementation is responsible for timeouts and retries.
	Call(ctx context.Context, prompt string) (string, error)
}

// ── DisabledCaller: default, no LLM call performed ──────────────

// DisabledCaller is the default LLMCaller. It returns ErrLLMDisabled
// so the LLM fallback path becomes a no-op (decider falls back to
// the heuristic result at low confidence).
type DisabledCaller struct{}

// Call implements LLMCaller.
func (DisabledCaller) Call(_ context.Context, _ string) (string, error) {
	return "", ErrLLMDisabled
}

// ── NoopCaller: always returns a fixed task type (for tests) ─────

// NoopCaller is a test helper that always returns a configured task
// type. Useful for unit-testing the LLM fallback path without an
// actual LLM endpoint.
type NoopCaller struct {
	FixedTask string
	Delay     time.Duration // optional artificial latency
}

// Call implements LLMCaller.
func (n NoopCaller) Call(ctx context.Context, _ string) (string, error) {
	if n.Delay > 0 {
		select {
		case <-time.After(n.Delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return n.FixedTask, nil
}

// ── CircuitBreakerCaller: wraps a real caller with failure isolation

// CircuitBreakerCaller wraps a real LLMCaller with a simple failure
// counter. After maxFailures consecutive failures, the breaker
// "opens" and rejects calls for cooldown. The counter resets on
// any successful call.
//
// Rationale: a flaky LLM endpoint would otherwise cause every
// low-confidence request to do an expensive timeout. The breaker
// keeps the hot path's worst-case latency bounded.
type CircuitBreakerCaller struct {
	Inner       LLMCaller
	MaxFailures int           // default: 5
	Cooldown    time.Duration // default: 30s

	mu          sync.Mutex
	consecutive int
	openUntil   time.Time
}

// NewCircuitBreakerCaller wraps inner with a 5-failure / 30s breaker.
func NewCircuitBreakerCaller(inner LLMCaller) *CircuitBreakerCaller {
	return &CircuitBreakerCaller{
		Inner:       inner,
		MaxFailures: 5,
		Cooldown:    30 * time.Second,
	}
}

// Call implements LLMCaller.
func (c *CircuitBreakerCaller) Call(ctx context.Context, prompt string) (string, error) {
	now := time.Now()

	c.mu.Lock()
	if c.consecutive >= c.MaxFailures && now.Before(c.openUntil) {
		c.mu.Unlock()
		return "", ErrLLMCircuitOpen
	}
	c.mu.Unlock()

	resp, err := c.Inner.Call(ctx, prompt)

	c.mu.Lock()
	if err != nil {
		c.consecutive++
		if c.consecutive >= c.MaxFailures {
			c.openUntil = now.Add(c.Cooldown)
		}
	} else {
		c.consecutive = 0
		c.openUntil = time.Time{}
	}
	c.mu.Unlock()

	return resp, err
}

// State returns the current breaker state for observability.
// Returns consecutive-failures and time-until-cooldown-ends.
func (c *CircuitBreakerCaller) State() (consecutive int, openFor time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	consecutive = c.consecutive
	open := c.consecutive >= c.MaxFailures && time.Now().Before(c.openUntil)
	// Mirror to Prometheus (best-effort, never blocks)
	RecordLLMCircuitBreakerState(consecutive, open)
	if open {
		openFor := time.Until(c.openUntil)
		if openFor < 0 {
			openFor = 0
		}
		return consecutive, openFor
	}
	return consecutive, 0
}

// Stats is a snapshot of the breaker for inspection.
func (c *CircuitBreakerCaller) Stats() (consecutive int, maxFailures int, cooldown time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.consecutive, c.MaxFailures, c.Cooldown
}

// ── MetricsCollector: optional observability for any LLMCaller

// CallerMetrics aggregates per-LLM-call metrics in a lock-free way.
// Used to expose Prometheus counters without forcing every
// implementation to handle metric emission.
type CallerMetrics struct {
	Calls       atomic.Int64
	Successes   atomic.Int64
	Failures    atomic.Int64
	Timeouts    atomic.Int64
	LatencyNs   atomic.Int64 // sum; Prometheus consumer divides by Calls
	BreakerOpen atomic.Int64
}

// RecordSuccess increments success counters atomically.
func (m *CallerMetrics) RecordSuccess(latency time.Duration) {
	m.Calls.Add(1)
	m.Successes.Add(1)
	m.LatencyNs.Add(latency.Nanoseconds())
}

// RecordFailure increments failure counters atomically.
func (m *CallerMetrics) RecordFailure(latency time.Duration, isTimeout bool) {
	m.Calls.Add(1)
	m.Failures.Add(1)
	m.LatencyNs.Add(latency.Nanoseconds())
	if isTimeout {
		m.Timeouts.Add(1)
	}
}

// RecordBreakerOpen increments the breaker-open counter.
func (m *CallerMetrics) RecordBreakerOpen() {
	m.BreakerOpen.Add(1)
}

// InstrumentedCaller wraps an LLMCaller with metric collection.
type InstrumentedCaller struct {
	Inner  LLMCaller
	Metrics *CallerMetrics
}

// NewInstrumentedCaller wraps inner with a fresh metrics object.
func NewInstrumentedCaller(inner LLMCaller) *InstrumentedCaller {
	return &InstrumentedCaller{Inner: inner, Metrics: &CallerMetrics{}}
}

// Call implements LLMCaller with metric emission.
//
// Outcome label mapping (for the Prometheus counter):
//   - "success"        — Inner returned no error
//   - "failure"        — Inner returned a non-timeout error
//   - "timeout"        — ctx.DeadlineExceeded or context cancelled
//   - "breaker_open"   — inner caller is a CircuitBreakerCaller
//                         that rejected the call
func (i *InstrumentedCaller) Call(ctx context.Context, prompt string) (string, error) {
	start := time.Now()
	resp, err := i.Inner.Call(ctx, prompt)
	latency := time.Since(start)

	outcome := "success"
	if err != nil {
		switch {
		case errors.Is(err, ErrLLMCircuitOpen):
			outcome = "breaker_open"
		case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
			outcome = "timeout"
		default:
			outcome = "failure"
		}
		i.Metrics.RecordFailure(latency, outcome == "timeout")
	} else {
		i.Metrics.RecordSuccess(latency)
	}

	// Mirror to Prometheus
	RecordLLMMetricCall(outcome, latency)
	return resp, err
}

// ── NewLLMFallbackClassifierWithCaller — convenience wrapper ─────

// NewLLMFallbackClassifierWithCaller returns a new LLMFallbackClassifier
// using the provided caller. Equivalent to NewLLMFallbackClassifier
// but takes an LLMCaller interface (not a func) for testability.
func NewLLMFallbackClassifierWithCaller(caller LLMCaller) *LLMFallbackClassifier {
	if caller == nil {
		caller = DisabledCaller{}
	}
	return NewLLMFallbackClassifier(caller.Call)
}
