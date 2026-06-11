package circuit

import (
	"testing"
	"time"
)

func TestNewBreakerIsClosed(t *testing.T) {
	b := New(1, 2)
	if b.State() != StateClosed {
		t.Fatalf("expected closed, got %s", b.State())
	}
	if !b.Allow() {
		t.Fatal("new breaker should allow requests")
	}
}

func TestSingleTransientFailureDoesNotOpenCircuit(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindTransient)
	if b.State() != StateClosed {
		t.Fatalf("expected closed while failure is unconfirmed, got %s", b.State())
	}
	if !b.Allow() {
		t.Fatal("single transient failure should not block requests")
	}
}

func TestConfirmedTransientFailureOpensCircuit(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	if b.State() != StateOpen {
		t.Fatalf("expected open after confirmed failures, got %s", b.State())
	}
}

func TestSingleAuthFailureDoesNotQuarantine(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindAuth)
	if b.State() != StateClosed {
		t.Fatalf("expected closed while auth failure is unconfirmed, got %s", b.State())
	}
	if !b.Allow() {
		t.Fatal("single auth failure should not block requests")
	}
}

func TestConfirmedAuthFailureQuarantines(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindAuth)
	b.RecordFailure(KindAuth)
	if b.State() != StateQuarantined {
		t.Fatalf("expected quarantined, got %s", b.State())
	}
	if b.Allow() {
		t.Fatal("quarantined breaker should not allow requests")
	}
}

func TestQuotaFailureQuarantines(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindQuota)
	b.RecordFailure(KindQuota)
	if b.State() != StateQuarantined {
		t.Fatalf("expected quarantined, got %s", b.State())
	}
	if b.Allow() {
		t.Fatal("quarantined breaker should not allow requests")
	}
}

func TestSuccessClosesCircuit(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	if b.State() != StateOpen {
		t.Fatalf("expected open after failure")
	}
	if b.Allow() {
		t.Fatal("open circuit should not allow requests")
	}

	// Manually set to half-open (simulating cooling expiry)
	b.state.Store(int32(StateHalfOpen))
	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Fatalf("expected closed after success, got %s", b.State())
	}
	if !b.Allow() {
		t.Fatal("closed circuit should allow requests")
	}
}

func TestHalfOpenProbeRecovery(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)

	// Simulate cooling expiry
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()

	// Allow should transition to half-open
	if !b.Allow() {
		t.Fatal("should allow after cooling expiry")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("expected half_open, got %s", b.State())
	}

	// Success closes the circuit
	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Fatalf("expected closed, got %s", b.State())
	}
}

func TestHalfOpenProbeFailure(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)

	// Simulate cooling expiry
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()

	if !b.Allow() {
		t.Fatal("should allow after cooling expiry")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("expected half_open, got %s", b.State())
	}

	// Failure re-opens circuit
	b.RecordFailure(KindTransient)
	if b.State() != StateOpen {
		t.Fatalf("expected open after probe failure, got %s", b.State())
	}
	if b.Allow() {
		t.Fatal("open circuit should not allow requests")
	}
}

func TestRateLimitExponentialBackoff(t *testing.T) {
	b := New(1, 1)

	// Confirmed rate limit → 900s (15 min) cooling
	b.RecordFailure(KindRateLimit)
	b.RecordFailure(KindRateLimit)
	if b.State() != StateOpen {
		t.Fatalf("expected open, got %s", b.State())
	}

	b.mu.Lock()
	firstCooling := b.coolingExpires.Sub(time.Now())
	b.mu.Unlock()
	if firstCooling < 898*time.Second || firstCooling > 902*time.Second {
		t.Fatalf("expected ~900s cooling, got %v", firstCooling)
	}

	// Second rate limit → still 900s (at max)
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second) // expire current cooling
	b.mu.Unlock()
	b.Allow()                      // transition to half-open
	b.RecordFailure(KindRateLimit) // half-open probe failure

	b.mu.Lock()
	secondCooling := b.coolingExpires.Sub(time.Now())
	b.mu.Unlock()
	if secondCooling < 898*time.Second || secondCooling > 902*time.Second {
		t.Fatalf("expected ~900s cooling, got %v", secondCooling)
	}
}

func TestTransientEscalation(t *testing.T) {
	b := New(1, 1)

	// 3 consecutive transient failures → escalate to exponential cooling
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)

	b.mu.Lock()
	cooling := b.coolingExpires.Sub(time.Now())
	b.mu.Unlock()
	if cooling < 28*time.Second || cooling > 32*time.Second {
		t.Fatalf("expected ~30s cooling after escalation, got %v", cooling)
	}
}

func TestReset(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindAuth)
	b.RecordFailure(KindAuth)
	if b.State() != StateQuarantined {
		t.Fatalf("expected quarantined")
	}
	if b.Allow() {
		t.Fatal("quarantined should not allow")
	}

	b.Reset()
	if b.State() != StateClosed {
		t.Fatalf("expected closed after reset, got %s", b.State())
	}
	if !b.Allow() {
		t.Fatal("should allow after reset")
	}
}

func TestConsecutiveFailures(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)

	if c := b.ConsecutiveFailures(); c != 2 {
		t.Fatalf("expected 2 consecutive failures, got %d", c)
	}

	b.RecordSuccess()
	if c := b.ConsecutiveFailures(); c != 0 {
		t.Fatalf("expected 0 after success, got %d", c)
	}
}

func TestManagerCreateAndGet(t *testing.T) {
	m := NewManager()

	b1 := m.GetOrCreate(1, 1)
	b2 := m.GetOrCreate(1, 1)
	if b1 != b2 {
		t.Fatal("GetOrCreate should return same instance")
	}

	b3 := m.Get(1, 1)
	if b3 != b1 {
		t.Fatal("Get should return same instance")
	}

	b4 := m.Get(999, 999)
	if b4 != nil {
		t.Fatal("Get for non-existent should return nil")
	}
}

func TestManagerRecordAndAllow(t *testing.T) {
	m := NewManager()

	if !m.Allow(1, 1) {
		t.Fatal("should allow initially")
	}

	m.RecordFailure(1, 1, KindAuth)
	if !m.Allow(1, 1) {
		t.Fatal("should still allow after one unconfirmed auth failure")
	}
	m.RecordFailure(1, 1, KindAuth)
	if m.Allow(1, 1) {
		t.Fatal("should not allow after auth failure")
	}

	m.RecordSuccess(1, 1)
	if !m.Allow(1, 1) {
		t.Fatal("should allow after success")
	}
}

func TestManagerStats(t *testing.T) {
	m := NewManager()
	m.GetOrCreate(1, 1)
	m.GetOrCreate(2, 2)

	m.RecordFailure(1, 1, KindAuth)
	m.RecordFailure(1, 1, KindAuth) // quarantined

	stats := m.Stats()
	if len(stats) != 2 {
		t.Fatalf("expected 2 breakers, got %d", len(stats))
	}

	states := make(map[string]int)
	for _, s := range stats {
		states[s["state"].(string)]++
	}
	if states["closed"] != 1 {
		t.Fatalf("expected 1 closed, got %d", states["closed"])
	}
	if states["quarantined"] != 1 {
		t.Fatalf("expected 1 quarantined, got %d", states["quarantined"])
	}
}

func TestManagerProbeCheck(t *testing.T) {
	m := NewManager()
	m.RecordFailure(1, 1, KindTransient)
	m.RecordFailure(1, 1, KindTransient)
	m.RecordFailure(1, 1, KindTransient)

	// Should not probe while still cooling
	if m.ProbeCheck(1, 1) {
		t.Fatal("should not probe while open")
	}

	// Manually expire cooling
	b := m.Get(1, 1)
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()

	if !m.ProbeCheck(1, 1) {
		t.Fatal("should probe after cooling expiry")
	}

	// Close the probe with success
	m.CloseProbe(1, 1, true, "")
	if m.Allow(1, 1) != true {
		t.Fatal("should allow after probe success")
	}
}

func TestManagerResetAll(t *testing.T) {
	m := NewManager()
	m.RecordFailure(1, 1, KindAuth)
	m.RecordFailure(1, 1, KindAuth)
	m.RecordFailure(2, 2, KindQuota)
	m.RecordFailure(2, 2, KindQuota)

	if m.Allow(1, 1) || m.Allow(2, 2) {
		t.Fatal("both should be blocked")
	}

	m.ResetAll()

	if !m.Allow(1, 1) || !m.Allow(2, 2) {
		t.Fatal("both should be allowed after reset")
	}
}

func TestUpstreamDownExponentialBackoff(t *testing.T) {
	b := New(1, 1)

	// Confirmed upstream_down → 30s
	b.RecordFailure(KindUpstreamDown)
	b.RecordFailure(KindUpstreamDown)
	b.mu.Lock()
	c1 := b.coolingExpires.Sub(time.Now())
	b.mu.Unlock()

	if c1 < 28*time.Second || c1 > 32*time.Second {
		t.Fatalf("expected ~30s cooling for first upstream_down, got %v", c1)
	}

	// Expire and fail again → 60s
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()
	b.Allow()
	b.RecordFailure(KindUpstreamDown)

	b.mu.Lock()
	c2 := b.coolingExpires.Sub(time.Now())
	b.mu.Unlock()
	if c2 < 58*time.Second || c2 > 62*time.Second {
		t.Fatalf("expected ~60s cooling for second upstream_down, got %v", c2)
	}
}

func TestConcurrentOverloadFiveMinuteCooling(t *testing.T) {
	// KindConcurrent represents "service overloaded / too many concurrent
	// requests". The credential should be taken out of rotation for
	// 5 minutes to let the upstream's concurrency window clear.
	// RecoveryAuto uses autoRecoveryFailureThreshold=3, so we record
	// three failures to confirm the circuit opening.
	b := New(1, 1)
	b.RecordFailure(KindConcurrent)
	b.RecordFailure(KindConcurrent)
	b.RecordFailure(KindConcurrent)
	if b.State() != StateOpen {
		t.Fatalf("expected open after 3 confirmed concurrent failures, got %s", b.State())
	}

	b.mu.Lock()
	cooling := b.coolingExpires.Sub(time.Now())
	b.mu.Unlock()
	if cooling < 298*time.Second || cooling > 302*time.Second {
		t.Fatalf("expected ~5min cooling for concurrent overload, got %v", cooling)
	}

	// After cooling expires, a single probe should be allowed.
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()
	if !b.Allow() {
		t.Fatal("should allow probe after cooling expires")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("expected half_open for probe, got %s", b.State())
	}

	// Probe success closes the circuit.
	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Fatalf("expected closed after probe success, got %s", b.State())
	}
}
