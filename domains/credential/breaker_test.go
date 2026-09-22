package credential

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestCircuitTransitionLogsExposeStableStateFields(t *testing.T) {
	source, err := os.ReadFile("breaker.go")
	if err != nil {
		t.Fatalf("read breaker.go: %v", err)
	}
	text := string(source)
	for _, logMessage := range []string{
		"circuit half-open",
		"circuit quarantined",
		"circuit opened",
		"circuit closed",
		"circuit reset",
	} {
		idx := strings.Index(text, `slog.`)
		for idx >= 0 {
			if end := strings.Index(text[idx:], logMessage); end >= 0 {
				idx += end
				break
			}
			next := strings.Index(text[idx+len("slog."):], "slog.")
			if next < 0 {
				idx = -1
				break
			}
			idx += len("slog.") + next
		}
		if idx < 0 {
			t.Fatalf("transition log %q not found", logMessage)
		}
		end := idx + 350
		if end > len(text) {
			end = len(text)
		}
		entry := text[idx:end]
		for _, field := range []string{"previous_state", "new_state", "error_kind", "cooling_duration_ms"} {
			if !strings.Contains(entry, field) {
				t.Fatalf("transition log %q missing stable field %q", logMessage, field)
			}
		}
	}
}

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

// Wave 1 A4 (2026-09-22): 429/限流不再计入断路器失败——熔断只由真实故障驱动。
// 短期限流排序降权由 writer.go 绑定级冷却（Retry-After 优先，默认 3min）承担。
func TestRateLimitExcludedFromBreaker(t *testing.T) {
	b := New(1, 1)

	// 429 风暴不开断路器、不累计失败
	for i := 0; i < 10; i++ {
		b.RecordFailure(KindRateLimit)
	}
	if b.State() != StateClosed {
		t.Fatalf("expected closed under 429 storm, got %s", b.State())
	}
	if b.ConsecutiveFailures() != 0 {
		t.Fatalf("consecutive failures = %d, want 0", b.ConsecutiveFailures())
	}
	if !b.Allow() {
		t.Fatal("closed breaker under 429 storm should allow requests")
	}

	// HALF_OPEN 探针撞 429：归还探针槽、保持 HALF_OPEN，不确认失败
	b2 := New(1, 2)
	b2.mu.Lock()
	b2.state.Store(int32(StateHalfOpen))
	b2.nextProbeAt = time.Now()
	b2.mu.Unlock()
	if !b2.Allow() {
		t.Fatal("half-open should admit the probe")
	}
	b2.RecordFailure(KindRateLimit)
	if b2.State() != StateHalfOpen {
		t.Fatalf("429 probe must not change state, got %s", b2.State())
	}
	if !b2.Allow() {
		t.Fatal("probe slot must be released after 429 so the next request can probe")
	}

	// 对照：真实故障种类仍然开断路器（限流剔除不弱化故障路径，阈值为 2）
	b3 := New(1, 3)
	b3.RecordFailure(KindUpstreamDown)
	b3.RecordFailure(KindUpstreamDown)
	if b3.State() != StateOpen {
		t.Fatalf("real failures must still open the breaker, got %s", b3.State())
	}
}

func TestTransientEscalation(t *testing.T) {
	b := New(1, 1)

	// R31 (audit §四#10): the escalation now follows a real exponential curve.
	// 2 consecutive transient failures → escalate onto the UpstreamDown
	// profile at its initial 30s step; each further sustained failure doubles
	// the cooling (30s → 60s), capped at the profile's 30min ceiling.
	b.RecordFailure(KindTransient)
	b.RecordFailure(KindTransient)

	b.mu.Lock()
	cycle1, d1 := b.coolingCycle, b.coolingExpires.Sub(b.openSince)
	b.mu.Unlock()
	if cycle1 != 1 || d1 != 30*time.Second {
		t.Fatalf("expected escalation cycle 1 with ~30s cooling, got cycle %d / %v", cycle1, d1)
	}

	b.RecordFailure(KindTransient)
	b.mu.Lock()
	cycle2, d2 := b.coolingCycle, b.coolingExpires.Sub(b.openSince)
	b.mu.Unlock()
	if cycle2 != 2 || d2 != 60*time.Second {
		t.Fatalf("expected escalation cycle 2 with ~60s cooling, got cycle %d / %v", cycle2, d2)
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

// R31 (audit 2026-09-16 §四#5): the explicit probe API (ProbeCheck/CloseProbe)
// was removed as a dead seam; this keeps the underlying half-open recovery
// contract pinned through the live API — Allow consumes a half-open probe,
// and RecordSuccess closes the circuit so the next Allow passes.
func TestManagerHalfOpenRecoveryViaLiveAPI(t *testing.T) {
	m := NewManager()
	for i := 0; i < 3; i++ {
		m.RecordFailure(1, 1, KindTransient)
	}

	// Should not allow while still cooling
	if m.Allow(1, 1) {
		t.Fatal("should not allow while open")
	}

	// Manually expire cooling so Allow transitions OPEN → HALF_OPEN
	b := m.Get(1, 1)
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-1 * time.Second)
	b.mu.Unlock()

	if !m.Allow(1, 1) {
		t.Fatal("should allow the half-open probe after cooling expiry")
	}

	// A success on the half-open probe closes the breaker
	m.RecordSuccess(1, 1)
	if m.Allow(1, 1) != true {
		t.Fatal("should allow after probe success closes the circuit")
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

// TestFreeTierCoolingProfile (2026-09-15, 245 free-capacity plan): a
// billing_mode='free' credential cools on the shortened freeTierPolicies
// profile — "engine busy" on a 1-concurrency free provider is a normal,
// milliseconds-scale condition, and a 2-minute hard stop discarded usable
// free capacity (326 circuit-open cycles / 24h on NVIDIA NIM 18/8+18/19).
func TestFreeTierCoolingProfile(t *testing.T) {
	m := NewManager()

	// Paid breaker: KindConcurrent cools 2 minutes (default policy).
	m.RecordFailure(1, 100, errorsx.KindConcurrent)
	m.RecordFailure(1, 100, errorsx.KindConcurrent)
	paid := m.Get(1, 100)
	if paid.State() != StateOpen {
		t.Fatalf("paid breaker should be open after 2 concurrent failures, got %s", paid.State())
	}
	paidCooling := parseCoolingExpires(t, paid.Stats())
	if paidCooling < time.Minute {
		t.Fatalf("paid concurrent cooling should be ~2min, got %v", paidCooling)
	}

	// Free breaker: same two failures, but cooling is ~5 seconds.
	m.RecordFailureWithBillingMode(1, 200, errorsx.KindConcurrent, "free")
	m.RecordFailureWithBillingMode(1, 200, errorsx.KindConcurrent, "free")
	free := m.Get(1, 200)
	if !free.IsFreeTier() {
		t.Fatal("free breaker should be marked as free tier")
	}
	if free.State() != StateOpen {
		t.Fatalf("free breaker should be open after 2 concurrent failures, got %s", free.State())
	}
	freeCooling := parseCoolingExpires(t, free.Stats())
	if freeCooling > 10*time.Second {
		t.Fatalf("free concurrent cooling should be ~5s, got %v", freeCooling)
	}

	// The free profile only applies to breakers seen with billing_mode=free;
	// a paid credential must not inherit it.
	if m.Get(1, 100).IsFreeTier() {
		t.Fatal("paid breaker must not be marked free tier")
	}
}

// TestFreeTierTransientEscalationOwnsFreeProfile: sustained transient
// failures on a free credential still escalate, but onto the free UpstreamDown
// profile (15s first step) instead of the paid 30s — a dead free provider
// backs off without pinning its capacity for half-hour ceilings. R31 (audit
// §四#10) note: the curve is real now (15s → 30s → …); the free profile's
// 5-minute ceiling clamp is pinned in TestEscalatedCoolingBacksOffExponentially.
func TestFreeTierTransientEscalationOwnsFreeProfile(t *testing.T) {
	m := NewManager()
	m.RecordFailureWithBillingMode(2, 300, errorsx.KindTransient, "free")
	m.RecordFailureWithBillingMode(2, 300, errorsx.KindTransient, "free")
	free := m.Get(2, 300)
	if free.State() != StateOpen {
		t.Fatalf("free breaker should be open after repeated transient failures, got %s", free.State())
	}
	free.mu.Lock()
	cycle, d := free.coolingCycle, free.coolingExpires.Sub(free.openSince)
	free.mu.Unlock()
	if cycle != 1 || d != 15*time.Second {
		t.Fatalf("free escalation should start on the free profile's 15s step, got cycle %d / %v", cycle, d)
	}
}

// parseCoolingExpires decodes the RFC3339 cooling_expires diagnostic field
// from Breaker.Stats into a remaining duration.
func parseCoolingExpires(t *testing.T, stats map[string]any) time.Duration {
	t.Helper()
	raw, ok := stats["cooling_expires"]
	if !ok {
		t.Fatal("stats missing cooling_expires")
	}
	str, ok := raw.(string)
	if !ok {
		t.Fatalf("cooling_expires = %T, want string", raw)
	}
	ts, err := time.Parse(time.RFC3339, str)
	if err != nil {
		t.Fatalf("parse cooling_expires %q: %v", str, err)
	}
	return time.Until(ts)
}

// R31 (audit 2026-09-16 §四#10): the transient-family escalation previously
// wrote a flat InitialCooling and never advanced coolingCycle, so the
// "exponential" in its log was false, a sustained outage cooled the same step
// forever, and the cycle>=5 sustained-outage alert could never fire. This pins
// the repaired behaviour: the cycle advances and the duration follows the
// escalated policy's exponential curve, capped at that policy's MaxCooling.
func TestEscalatedCoolingBacksOffExponentially(t *testing.T) {
	m := NewManager()

	// Paid: escalates onto defaultPolicies[KindUpstreamDown] (30s initial,
	// 30min ceiling): 30s → 60s → 120s …
	b := m.GetOrCreate(9, 9)
	b.RecordFailure(KindNetwork) // pending confirmation (threshold 2)
	b.RecordFailure(KindNetwork) // opens, escalation cycle 1
	b.mu.Lock()
	cycle1, d1 := b.coolingCycle, b.coolingExpires.Sub(b.openSince)
	b.mu.Unlock()
	if cycle1 != 1 || d1 != 30*time.Second {
		t.Fatalf("first escalation: cycle=%d cooling=%v, want cycle 1 / 30s", cycle1, d1)
	}

	b.RecordFailure(KindNetwork) // sustained failure → cycle 2
	b.mu.Lock()
	cycle2, d2 := b.coolingCycle, b.coolingExpires.Sub(b.openSince)
	b.mu.Unlock()
	if cycle2 != 2 || d2 != 60*time.Second {
		t.Fatalf("second escalation: cycle=%d cooling=%v, want cycle 2 / 60s", cycle2, d2)
	}

	// Free: escalates onto freeTierPolicies[KindUpstreamDown] (15s initial,
	// 5min ceiling) — 15s → 30s → …, and the ceiling must actually clamp
	// instead of cooling 15s flat forever.
	bf := m.GetOrCreate(8, 8)
	bf.MarkFreeTier()
	bf.RecordFailure(KindTimeout)
	bf.RecordFailure(KindTimeout)
	bf.mu.Lock()
	cycleF1, dF1 := bf.coolingCycle, bf.coolingExpires.Sub(bf.openSince)
	bf.mu.Unlock()
	if cycleF1 != 1 || dF1 != 15*time.Second {
		t.Fatalf("free first escalation: cycle=%d cooling=%v, want cycle 1 / 15s", cycleF1, dF1)
	}

	// Cycles 2..6: 30s, 60s, 120s, 240s, then 480s clamps to the 300s ceiling.
	for i := 2; i <= 6; i++ {
		bf.RecordFailure(KindTimeout)
		bf.mu.Lock()
		cycle, d := bf.coolingCycle, bf.coolingExpires.Sub(bf.openSince)
		bf.mu.Unlock()
		want := time.Duration(15 * (1 << uint(i-1)) * int(time.Second))
		if want > 5*time.Minute {
			want = 5 * time.Minute
		}
		if cycle != i || d != want {
			t.Fatalf("free escalation cycle %d: cycle=%d cooling=%v, want %v", i, cycle, d, want)
		}
	}
}
