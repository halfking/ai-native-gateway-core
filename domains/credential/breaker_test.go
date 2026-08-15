package credential

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
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

func TestConfirmedAuthFailureOpens(t *testing.T) {
	// 2026-07-22 fix (BUG #1): KindAuth now uses exponential cooling
	// (15min → 30min → ... → 24h cap) instead of RecoveryPermanent
	// quarantine. Two consecutive auth failures transition to OPEN
	// with a 15-minute cooling window; the credential_recovery 60s
	// ticker + active_probe mechanism can then flip it back to ready
	// once a successful probe confirms the apikey is fixed.
	b := New(1, 1)
	b.RecordFailure(KindAuth)
	b.RecordFailure(KindAuth)
	if b.State() != StateOpen {
		t.Fatalf("expected open (exponential cooling), got %s", b.State())
	}
	if b.Allow() {
		t.Fatal("open breaker should not allow requests")
	}
	// Confirm the cooling window matches the configured 5-minute
	// InitialCooling — guards against accidental future changes to
	// the KindAuth policy breaking the recovery contract.
	b.mu.Lock()
	cooling := b.coolingExpires.Sub(b.lastFailureAt)
	b.mu.Unlock()
	if cooling < 4*time.Minute || cooling > 6*time.Minute {
		t.Fatalf("expected ~5min cooling, got %s", cooling)
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
	firstCooling := time.Until(b.coolingExpires)
	b.mu.Unlock()
	if firstCooling < 118*time.Second || firstCooling > 122*time.Second {
		t.Fatalf("expected ~120s cooling, got %v", firstCooling)
	}

	// Second rate limit → still 120s (at max)
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second) // expire current cooling
	b.mu.Unlock()
	b.Allow()                      // transition to half-open
	b.RecordFailure(KindRateLimit) // half-open probe failure

	b.mu.Lock()
	secondCooling := time.Until(b.coolingExpires)
	b.mu.Unlock()
	if secondCooling < 118*time.Second || secondCooling > 122*time.Second {
		t.Fatalf("expected ~120s cooling, got %v", secondCooling)
	}
}

func TestTransientEscalation(t *testing.T) {
	b := New(1, 1)

	// 3 consecutive transient failures → escalate to exponential cooling
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)

	b.mu.Lock()
	cooling := time.Until(b.coolingExpires)
	b.mu.Unlock()
	if cooling < 28*time.Second || cooling > 32*time.Second {
		t.Fatalf("expected ~30s cooling after escalation, got %v", cooling)
	}
}

func TestReset(t *testing.T) {
	// 2026-07-22 (BUG #1): Use KindQuota (not KindAuth) to land in
	// StateQuarantined — KindAuth now uses exponential cooling and
	// stops at StateOpen.
	b := New(1, 1)
	b.RecordFailure(KindQuota)
	b.RecordFailure(KindQuota)
	if b.State() != StateQuarantined {
		t.Fatalf("expected quarantined, got %s", b.State())
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
	m.RecordFailure(1, 1, KindAuth) // open with 15min cooling (BUG #1 fix)

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
	// 2026-07-22: KindAuth is now StateOpen (exponential), not
	// StateQuarantined. Quarantined is reserved for KindQuota (which
	// remains RecoveryPermanent).
	if states["open"] != 1 {
		t.Fatalf("expected 1 open (KindAuth exponential), got %d", states["open"])
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
	c1 := time.Until(b.coolingExpires)
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
	c2 := time.Until(b.coolingExpires)
	b.mu.Unlock()
	if c2 < 58*time.Second || c2 > 62*time.Second {
		t.Fatalf("expected ~60s cooling for second upstream_down, got %v", c2)
	}
}

func TestConcurrentOverloadTwoMinuteCooling(t *testing.T) {
	// KindConcurrent represents "service overloaded / too many concurrent
	// requests". The credential is taken out of rotation for 2 minutes to
	// let the upstream's concurrency window clear.
	//
	// 2026-08-08: renamed from TestConcurrentOverloadFiveMinuteCooling and
	// the comment corrected. The name, the "5 minutes" prose, and the
	// "threshold=3" claim were all stale: the policy at breaker.go has been
	// 2 min since 2026-07-24, the assertion below has always checked
	// 118-122s, and autoRecoveryFailureThreshold is 2. Only the extra
	// RecordFailure calls (harmless, threshold is a floor) kept it passing.
	b := New(1, 1)
	b.RecordFailure(KindConcurrent)
	b.RecordFailure(KindConcurrent)
	b.RecordFailure(KindConcurrent)
	if b.State() != StateOpen {
		t.Fatalf("expected open after 3 confirmed concurrent failures, got %s", b.State())
	}

	b.mu.Lock()
	cooling := time.Until(b.coolingExpires)
	b.mu.Unlock()
	if cooling < 118*time.Second || cooling > 122*time.Second {
		t.Fatalf("expected ~2min cooling for concurrent overload, got %v", cooling)
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

	// Probe success closes the credential.
	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Fatalf("expected closed after probe success, got %s", b.State())
	}
}

// TestProbeLeakRecoveryViaReleaseProbe reproduces the 2026-07-03 incident:
// the half-open probe slot is consumed by Allow(), the request exits without
// calling RecordSuccess/RecordFailure (e.g. blocked by the concurrency/RPM
// limiter), and the breaker would be wedged in HALF_OPEN until process restart.
// ReleaseProbe() must free the slot so the next request can probe again.
func TestProbeLeakRecoveryViaReleaseProbe(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()

	// First Allow() consumes the probe slot (OPEN → HALF_OPEN).
	if !b.Allow() {
		t.Fatal("should allow probe after cooling expiry")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("expected half_open, got %s", b.State())
	}

	// Without a release, a second Allow() must be rejected (probe still held).
	if b.Allow() {
		t.Fatal("second Allow() should be rejected while first probe is in flight")
	}

	// Simulate the request exiting without recording (limiter reject path).
	b.ReleaseProbe()
	if b.State() != StateHalfOpen {
		t.Fatalf("ReleaseProbe should not change state, got %s", b.State())
	}

	// Next request can probe again.
	if !b.Allow() {
		t.Fatal("Allow() should succeed after ReleaseProbe")
	}

	// Closing the probe with a success must clear the circuit.
	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Fatalf("expected closed after probe success, got %s", b.State())
	}
}

// TestProbeLeakRecoveryViaTimeout verifies the belt-and-suspenders safety net:
// even if a leaked probe is never explicitly released, claimProbe force-reclaims
// the slot once halfOpenProbeTimeout elapses, so the breaker cannot stay wedged
// in HALF_OPEN forever.
func TestProbeLeakRecoveryViaTimeout(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()

	if !b.Allow() {
		t.Fatal("should allow probe after cooling expiry")
	}

	// Simulate the probe holder leaking past the timeout.
	b.mu.Lock()
	b.nextProbeAt = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()

	if !b.Allow() {
		t.Fatal("Allow() should reclaim the leaked probe slot after timeout")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("expected half_open, got %s", b.State())
	}
}

// TestPeekAllowedDoesNotConsumeProbe ensures the sync-retry allCircuitOpen
// pre-check can inspect a half-open breaker without stealing its probe slot.
func TestPeekAllowedDoesNotConsumeProbe(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()

	if !b.PeekAllowed() {
		t.Fatal("PeekAllowed should report allowed after cooling expiry")
	}
	// PeekAllowed is read-only: it must NOT transition OPEN → HALF_OPEN.
	if b.State() != StateOpen {
		t.Fatalf("PeekAllowed must not transition state, got %s", b.State())
	}

	// PeekAllowed must NOT have consumed the probe: the real Allow() still
	// gets through as the probe.
	if !b.Allow() {
		t.Fatal("Allow() should still succeed after PeekAllowed (probe not consumed)")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("Allow() should transition to half_open, got %s", b.State())
	}
}

func TestManagerReleaseProbe(t *testing.T) {
	m := NewManager()
	m.RecordFailure(1, 1, KindTransient)
	m.RecordFailure(1, 1, KindTransient)
	m.RecordFailure(1, 1, KindTransient)
	b := m.Get(1, 1)
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()

	if !m.Allow(1, 1) {
		t.Fatal("should allow probe after cooling expiry")
	}
	if m.Allow(1, 1) {
		t.Fatal("second Allow() should be rejected while probe in flight")
	}

	m.ReleaseProbe(1, 1)
	if !m.Allow(1, 1) {
		t.Fatal("Allow() should succeed after manager-level ReleaseProbe")
	}
}

func TestManagerPeekAllowedDoesNotConsumeProbe(t *testing.T) {
	m := NewManager()
	m.RecordFailure(1, 1, KindTransient)
	m.RecordFailure(1, 1, KindTransient)
	m.RecordFailure(1, 1, KindTransient)
	b := m.Get(1, 1)
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()

	if !m.PeekAllowed(1, 1) {
		t.Fatal("manager PeekAllowed should report allowed after cooling expiry")
	}
	if b.State() != StateOpen {
		t.Fatalf("manager PeekAllowed must not transition state, got %s", b.State())
	}
	if !m.Allow(1, 1) {
		t.Fatal("Allow() should still succeed after manager PeekAllowed (probe not consumed)")
	}
}

// TestUpstreamOverloadedCoolingCapsAtFiveMinutes pins the breaker policy for
// KindUpstreamOverloaded (added 2026-08-08).
//
// The policy deliberately shares KindUpstreamDown's 30s start and exponential
// shape but caps at 5 min instead of 30 min: a relay answering "our servers
// are currently overloaded, please try again later" recovers on a seconds-to-
// minutes scale, so quarantining it for half an hour shrinks the candidate
// pool far longer than the fault lasts. This test drives enough rounds to
// reach the ceiling, which is the only place the two policies diverge.
func TestUpstreamOverloadedCoolingCapsAtFiveMinutes(t *testing.T) {
	b := New(1, 1)

	// exponentialRecoveryFailureThreshold is 2, so two consecutive failures
	// confirm the first opening at InitialCooling (30s).
	b.RecordFailure(errorsx.KindUpstreamOverloaded)
	b.RecordFailure(errorsx.KindUpstreamOverloaded)
	if b.State() != StateOpen {
		t.Fatalf("expected open after 2 confirmed overload failures, got %s", b.State())
	}
	b.mu.Lock()
	first := time.Until(b.coolingExpires)
	b.mu.Unlock()
	if first < 28*time.Second || first > 32*time.Second {
		t.Fatalf("first overload cooling = %v, want ~30s", first)
	}

	// Drive further rounds: 60s, 120s, 240s, then the 5-min ceiling.
	for _, want := range []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second} {
		b.mu.Lock()
		b.coolingExpires = time.Now().Add(-1 * time.Second)
		b.mu.Unlock()
		b.Allow()
		b.RecordFailure(errorsx.KindUpstreamOverloaded)

		b.mu.Lock()
		got := time.Until(b.coolingExpires)
		b.mu.Unlock()
		if got < want-2*time.Second || got > want+2*time.Second {
			t.Fatalf("overload cooling = %v, want ~%v", got, want)
		}
	}

	// Two more rounds would be 480s then 960s under KindUpstreamDown's
	// 30-min ceiling; KindUpstreamOverloaded must clamp both to 5 min.
	for round := 0; round < 2; round++ {
		b.mu.Lock()
		b.coolingExpires = time.Now().Add(-1 * time.Second)
		b.mu.Unlock()
		b.Allow()
		b.RecordFailure(errorsx.KindUpstreamOverloaded)

		b.mu.Lock()
		got := time.Until(b.coolingExpires)
		b.mu.Unlock()
		if got < 298*time.Second || got > 302*time.Second {
			t.Fatalf("round %d overload cooling = %v, want ~300s (5-min cap)", round, got)
		}
	}
}

// TestUpstreamOverloadedIsNotQuarantined guards the single most important
// property of the new kind: it must never reach StateQuarantined. Quarantine
// is manual-recovery-only, and an overloaded upstream recovers by itself —
// pinning it there would need an operator to clear a fault that already
// healed.
func TestUpstreamOverloadedIsNotQuarantined(t *testing.T) {
	b := New(1, 1)
	for i := 0; i < 10; i++ {
		b.RecordFailure(errorsx.KindUpstreamOverloaded)
	}
	if b.State() == StateQuarantined {
		t.Fatal("overload must not quarantine the credential; it recovers on its own")
	}
	if b.State() != StateOpen {
		t.Fatalf("expected open after repeated overload failures, got %s", b.State())
	}
}

// TestManagerResetSingleCredential locks the 2026-08-15 emergency-repair
// contract: Manager.Reset(providerID, credentialID) must close exactly the
// targeted breaker (admin force_enable / clear_circuit path) and leave other
// credentials' breakers untouched.
func TestManagerResetSingleCredential(t *testing.T) {
	m := NewManager()

	// Open two breakers.
	for i := 0; i < 3; i++ {
		m.RecordFailure(7, 100, errorsx.KindNetwork)
		m.RecordFailure(7, 200, errorsx.KindNetwork)
	}
	if m.Get(7, 100).State() != StateOpen || m.Get(7, 200).State() != StateOpen {
		t.Fatal("precondition: both breakers should be open")
	}

	m.Reset(7, 100)

	if got := m.Get(7, 100).State(); got != StateClosed {
		t.Fatalf("targeted breaker should be closed after Reset, got %s", got)
	}
	if got := m.Get(7, 200).State(); got != StateOpen {
		t.Fatalf("unrelated breaker must stay open, got %s", got)
	}
	if !m.Allow(7, 100) {
		t.Fatal("reset breaker should allow requests immediately")
	}

	// Resetting a never-created breaker is a no-op, not a panic.
	m.Reset(42, 999)
}
