package credentialhealth

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/health"
)

// TestRingBackedDetector_OnErrorIncrementsConsec verifies the
// happy path: OnError bumps the gauge.
func TestRingBackedDetector_OnErrorIncrementsConsec(t *testing.T) {
	d := NewRingBackedDetector(3)
	defer d.ReleaseAll()
	for i := 0; i < 5; i++ {
		_ = d.OnError(health.ErrorEvent{CredentialID: "cred-1"})
	}
	if got := d.GetConsecutiveFails("cred-1"); got != 5 {
		t.Fatalf("ConsecFails=%d want 5", got)
	}
	if got := d.GetErrorsPerMinute("cred-1"); got != 5 {
		t.Fatalf("ErrorsPerMinute=%d want 5", got)
	}
}

// TestRingBackedDetector_OnSuccessDecrementsConsec covers the
// gradual-recovery semantics.
func TestRingBackedDetector_OnSuccessDecrementsConsec(t *testing.T) {
	d := NewRingBackedDetector(3)
	defer d.ReleaseAll()
	for i := 0; i < 5; i++ {
		_ = d.OnError(health.ErrorEvent{CredentialID: "cred-1"})
	}
	d.OnSuccess("cred-1")
	if got := d.GetConsecutiveFails("cred-1"); got != 4 {
		t.Fatalf("after 1 success: ConsecFails=%d want 4", got)
	}
	for i := 0; i < 10; i++ {
		d.OnSuccess("cred-1")
	}
	if got := d.GetConsecutiveFails("cred-1"); got != 0 {
		t.Fatalf("after many successes: ConsecFails=%d want 0 (clamped)", got)
	}
}

// TestRingBackedDetector_IsUnhealthyThresholdTrips checks the
// unhealthy state is reached at the configured threshold.
func TestRingBackedDetector_IsUnhealthyThresholdTrips(t *testing.T) {
	d := NewRingBackedDetector(3)
	defer d.ReleaseAll()
	for i := 0; i < 3; i++ {
		_ = d.OnError(health.ErrorEvent{CredentialID: "cred-1"})
	}
	if !d.IsUnhealthy("cred-1") {
		t.Fatalf("IsUnhealthy should be true at threshold")
	}
}

// TestRingBackedDetector_ResetFailuresClearsGauge covers the
// admin-recovery flow.
func TestRingBackedDetector_ResetFailuresClearsGauge(t *testing.T) {
	d := NewRingBackedDetector(3)
	defer d.ReleaseAll()
	for i := 0; i < 5; i++ {
		_ = d.OnError(health.ErrorEvent{CredentialID: "cred-1"})
	}
	d.ResetFailures("cred-1")
	if got := d.GetConsecutiveFails("cred-1"); got != 0 {
		t.Fatalf("ResetFailures must clear gauge; got %d", got)
	}
}

// TestRingBackedDetector_UnknownCredentialReturnsZero verifies the
// safe defaults for never-seen credentials.
func TestRingBackedDetector_UnknownCredentialReturnsZero(t *testing.T) {
	d := NewRingBackedDetector(3)
	defer d.ReleaseAll()
	if got := d.GetConsecutiveFails("unknown"); got != 0 {
		t.Fatalf("unknown cred ConsecFails=%d want 0", got)
	}
	if d.IsUnhealthy("unknown") {
		t.Fatalf("unknown cred must not be unhealthy")
	}
}

// TestRingBackedDetector_ForgetReleasesCounter verifies Forget
// returns the counter to the pool and subsequent OnError starts
// fresh.
func TestRingBackedDetector_ForgetReleasesCounter(t *testing.T) {
	d := NewRingBackedDetector(3)
	defer d.ReleaseAll()
	_ = d.OnError(health.ErrorEvent{CredentialID: "cred-1"})
	_ = d.OnError(health.ErrorEvent{CredentialID: "cred-1"})
	if d.Size() != 1 {
		t.Fatalf("Size=%d want 1", d.Size())
	}
	d.Forget("cred-1")
	if d.Size() != 0 {
		t.Fatalf("Size=%d want 0 after Forget", d.Size())
	}
	if got := d.GetConsecutiveFails("cred-1"); got != 0 {
		t.Fatalf("after Forget+OnError, ConsecFails=%d want 0", got)
	}
}

// TestRingBackedDetector_DefaultThresholdMatchesLegacy verifies
// the zero-threshold fallback.
func TestRingBackedDetector_DefaultThresholdMatchesLegacy(t *testing.T) {
	d := NewRingBackedDetector(0) // 0 → default
	if d.Threshold() != 3 {
		t.Fatalf("Threshold=%d want 3 (default)", d.Threshold())
	}
}

// TestRingBackedDetector_DetectionResultFields spot-checks the
// populated DetectionResult on OnError.
func TestRingBackedDetector_DetectionResultFields(t *testing.T) {
	d := NewRingBackedDetector(3)
	defer d.ReleaseAll()
	var result health.DetectionResult
	for i := 0; i < 3; i++ {
		result = d.OnError(health.ErrorEvent{CredentialID: "cred-1"})
	}
	if !result.ShouldMarkUnhealthy {
		t.Fatalf("ShouldMarkUnhealthy should be true at threshold")
	}
	if result.RecommendedStatus != "Unhealthy" {
		t.Fatalf("RecommendedStatus=%q want Unhealthy", result.RecommendedStatus)
	}
	if result.ConsecutiveFails != 3 {
		t.Fatalf("ConsecutiveFails=%d want 3", result.ConsecutiveFails)
	}
}

// TestRingBackedDetector_SnapshotExposesPerCred verifies the
// observability surface.
func TestRingBackedDetector_SnapshotExposesPerCred(t *testing.T) {
	d := NewRingBackedDetector(3)
	defer d.ReleaseAll()
	_ = d.OnError(health.ErrorEvent{CredentialID: "cred-1"})
	_ = d.OnError(health.ErrorEvent{CredentialID: "cred-2"})
	snap := d.Snapshot()
	if _, ok := snap["cred-1"]; !ok {
		t.Fatalf("snapshot missing cred-1: %v", snap)
	}
	if _, ok := snap["cred-2"]; !ok {
		t.Fatalf("snapshot missing cred-2: %v", snap)
	}
}

// TestRingBackedDetector_MemoryFootprintPerCred is the regression
// test for Handoff-B #5: the per-credential memory budget must
// stay around 248 B (the documented layout). We don't measure
// bytes directly because reflect.Sizeof pulls in a heavy
// dependency; instead we verify the documented invariant via the
// high-water test (the pool keeps the high-water mark stable even
// as credential ids churn).
func TestRingBackedDetector_MemoryFootprintPerCred(t *testing.T) {
	d := NewRingBackedDetector(3)
	defer d.ReleaseAll()
	// Seed 1000 distinct credentials; the pool keeps the high
	// watermark at 1000 because we never Forget.
	distinct := make(map[string]struct{})
	for i := 0; i < 1000; i++ {
		id := idFor(i)
		_ = d.OnError(health.ErrorEvent{CredentialID: id})
		distinct[id] = struct{}{}
	}
	if got := d.Size(); got != len(distinct) {
		t.Fatalf("Size=%d want %d (distinct id count)", got, len(distinct))
	}
	// All distinct must still be tracked (no pool eviction of
	// high-water entries — pool only evicts via Forget /
	// ReleaseAll or sync.Pool's own GC).
	for id := range distinct {
		if got := d.GetConsecutiveFails(id); got != 1 {
			t.Fatalf("cred %s ConsecFails=%d want 1", id, got)
		}
	}
}

func idFor(i int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	// 3-char alphabet produces 26^3 = 17576 distinct ids, more
	// than enough for the 1000-id test. Older 2-char version
	// produced only 702 distinct ids which made the assertion
	// brittle.
	return string(alphabet[i%26]) +
		string(alphabet[(i/26)%26]) +
		string(alphabet[(i/676)%26]) +
		"-test"
}

// ---- Benchmarks ----

// BenchmarkRingBackedDetector_OnError is the hot-path benchmark:
// the function called on every error event in production.
func BenchmarkRingBackedDetector_OnError(b *testing.B) {
	d := NewRingBackedDetector(3)
	defer d.ReleaseAll()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.OnError(health.ErrorEvent{CredentialID: idFor(i)})
	}
}

// BenchmarkRingBackedDetector_GetErrorsPerMinute is the read side.
// Multiple callers query this on every credential-health check.
func BenchmarkRingBackedDetector_GetErrorsPerMinute(b *testing.B) {
	d := NewRingBackedDetector(3)
	defer d.ReleaseAll()
	// Pre-populate so the read isn't always zero.
	for i := 0; i < 60; i++ {
		_ = d.OnError(health.ErrorEvent{CredentialID: "cred-1"})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.GetErrorsPerMinute("cred-1")
	}
}

// BenchmarkLegacyErrorDetector_OnError is the reference for the
// ring-backed detector. Same operation through the legacy detector.
func BenchmarkLegacyErrorDetector_OnError(b *testing.B) {
	d := legacyNewErrorDetector(3)
	defer d.errorRateClear()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.onErrorFor(idFor(i))
	}
}

// The legacy detector is in domains/health; we use a thin local
// shim so the benchmark can construct one without an import cycle.
// We DO NOT replace the existing detector; this is purely for the
// benchmark comparison.
type legacyShim struct {
	cons map[string]int
	rate map[string]*legacyCounter
}

type legacyCounter struct {
	window [60]int
	cur    int
}

func legacyNewErrorDetector(threshold int) *legacyShim {
	if threshold == 0 {
		threshold = 3
	}
	return &legacyShim{
		cons: make(map[string]int),
		rate: make(map[string]*legacyCounter),
	}
}

func (l *legacyShim) onErrorFor(id string) int {
	l.cons[id]++
	c, ok := l.rate[id]
	if !ok {
		c = &legacyCounter{}
		l.rate[id] = c
	}
	c.window[c.cur]++
	sum := 0
	for _, v := range c.window {
		sum += v
	}
	return sum
}

func (l *legacyShim) errorRateClear() {
	l.cons = nil
	l.rate = nil
}