package compression

import (
	"testing"
	"time"
)

// newBreakerWith overrides the config for deterministic tests.
func newBreakerWith(threshold int, cooldown time.Duration) *summaryBreaker {
	return &summaryBreaker{cfg: breakerConfig{threshold: threshold, cooldown: cooldown}}
}

// TestBreaker_ClosedAllows is the steady state: under threshold, every attempt
// is allowed and a success resets the count.
func TestBreaker_ClosedAllows(t *testing.T) {
	b := newBreakerWith(3, time.Hour)
	now := time.Now()
	for i := 0; i < 2; i++ { // below threshold
		if allow, _ := b.allowDecide(now); !allow {
			t.Fatalf("attempt %d blocked while closed", i)
		}
		b.RecordResult(false, now)
	}
	// A success resets so the next failure shouldn't immediately trip at 3.
	if allow, _ := b.allowDecide(now); !allow {
		t.Fatal("blocked while still closed")
	}
	b.RecordResult(true, now)
	if b.failures != 0 {
		t.Fatalf("success did not reset failures: %d", b.failures)
	}
}

// TestBreaker_TripsAfterThreshold confirms the breaker opens after exactly
// `threshold` consecutive failures and blocks subsequent attempts.
func TestBreaker_TripsAfterThreshold(t *testing.T) {
	b := newBreakerWith(3, time.Hour)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if allow, _ := b.allowDecide(now); !allow {
			t.Fatalf("attempt %d blocked before threshold reached", i)
		}
		b.RecordResult(false, now)
	}
	// 4th attempt: breaker should be open.
	if allow, _ := b.allowDecide(now); allow {
		t.Fatal("allowed an attempt while breaker open")
	}
	if !b.IsOpen(now) {
		t.Fatal("IsOpen=false after threshold failures")
	}
}

// TestBreaker_HalfOpenProbeAfterCooldown verifies the breaker allows exactly
// one probe attempt once cooldown elapses, and a probe failure re-opens it.
func TestBreaker_HalfOpenProbeAfterCooldown(t *testing.T) {
	b := newBreakerWith(2, 10*time.Millisecond)
	t0 := time.Now()
	// Trip it.
	b.RecordResult(false, t0)
	b.RecordResult(false, t0)

	// Within cooldown: blocked.
	if allow, _ := b.allowDecide(t0.Add(5 * time.Millisecond)); allow {
		t.Fatal("allowed during cooldown")
	}

	// After cooldown: one probe allowed.
	after := t0.Add(20 * time.Millisecond)
	allow1, half1 := b.allowDecide(after)
	if !allow1 || !half1 {
		t.Fatal("expected a half-open probe after cooldown")
	}
	// A second concurrent probe must be blocked (only one in flight).
	if allow2, _ := b.allowDecide(after); allow2 {
		t.Fatal("allowed a second probe while one is in flight")
	}

	// Probe fails → re-open for another cooldown.
	b.RecordResult(false, after)
	if allow, _ := b.allowDecide(after); allow {
		t.Fatal("allowed immediately after a failed probe (should be re-open)")
	}
	if !b.IsOpen(after) {
		t.Fatal("breaker not re-open after failed probe")
	}
}

// TestBreaker_HalfOpenProbeSuccessCloses confirms a successful probe fully
// closes the breaker (failures reset to 0).
func TestBreaker_HalfOpenProbeSuccessCloses(t *testing.T) {
	b := newBreakerWith(2, 10*time.Millisecond)
	t0 := time.Now()
	b.RecordResult(false, t0)
	b.RecordResult(false, t0)

	after := t0.Add(20 * time.Millisecond)
	if _, ok := b.allowDecide(after); !ok {
		t.Fatal("probe not allowed after cooldown")
	}
	b.RecordResult(true, after) // probe succeeds
	if b.failures != 0 || !b.openedAt.IsZero() {
		t.Fatalf("successful probe did not close breaker: failures=%d openedAt=%v", b.failures, b.openedAt)
	}
	// Now fully closed — several attempts allowed.
	for i := 0; i < 5; i++ {
		if allow, _ := b.allowDecide(after); !allow {
			t.Fatal("blocked after breaker closed")
		}
	}
}

// TestBreaker_Disabled confirms threshold=0 disables the breaker entirely
// (always allow, no bookkeeping) — the operator kill-switch.
func TestBreaker_Disabled(t *testing.T) {
	b := newBreakerWith(0, time.Hour)
	now := time.Now()
	for i := 0; i < 100; i++ {
		if allow, _ := b.allowDecide(now); !allow {
			t.Fatalf("disabled breaker blocked attempt %d", i)
		}
		b.RecordResult(false, now) // must be a no-op
	}
	if b.IsOpen(now) {
		t.Fatal("disabled breaker reports open")
	}
}
