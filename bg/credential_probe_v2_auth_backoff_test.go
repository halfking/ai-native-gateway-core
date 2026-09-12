// Package bg — credential_probe_v2_auth_backoff_test.go
//
// Pins the probe-level auth backoff ladder (probe-recovery closeout P2,
// 2026-09-13): a 401/403 from a probe must carry an exponential
// availability_recover_at instead of NULL, so a permanently revoked key
// decays from one probe per 15min to one per 24h instead of oscillating
// through the 30s recovery tick every hour.
package bg

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAuthProbeBackoffRecoverAtLadder(t *testing.T) {
	base := time.Now()
	want := []time.Duration{
		15 * time.Minute, // n=1
		30 * time.Minute, // n=2
		1 * time.Hour,    // n=3
		2 * time.Hour,    // n=4
		4 * time.Hour,    // n=5
		8 * time.Hour,    // n=6
		16 * time.Hour,   // n=7
		24 * time.Hour,   // n=8 (cap)
		24 * time.Hour,   // n=9 stays capped
		24 * time.Hour,   // n=20 stays capped
	}
	for i, d := range want {
		n := i + 1
		got := authProbeBackoffRecoverAt(n)
		delay := got.Sub(base)
		// Allow scheduling slack: the helper stamps time.Now() internally.
		if delay < d-10*time.Second || delay > d+time.Minute {
			t.Fatalf("authProbeBackoffRecoverAt(%d) delay=%s want ~%s", n, delay, d)
		}
	}
}

func TestAuthProbeBackoffRecoverAtClampsNonPositiveN(t *testing.T) {
	base := time.Now()
	got := authProbeBackoffRecoverAt(0)
	if delay := got.Sub(base); delay < 15*time.Minute-10*time.Second || delay > 15*time.Minute+time.Minute {
		t.Fatalf("n=0 should use the base rung, got delay %s", delay)
	}
	got = authProbeBackoffRecoverAt(-5)
	if delay := got.Sub(base); delay < 15*time.Minute-10*time.Second || delay > 15*time.Minute+time.Minute {
		t.Fatalf("negative n should use the base rung, got delay %s", delay)
	}
}

func TestAuthProbeBackoffRecoverAtMonotonic(t *testing.T) {
	prev := time.Time{}
	for n := 1; n <= 10; n++ {
		got := authProbeBackoffRecoverAt(n)
		if prev.After(got) {
			t.Fatalf("ladder not monotonic at n=%d: %s after %s", n, prev, got)
		}
		prev = got
	}
}

// TestCycleAllRespectsAuthBackoffLadder pins the 2026-09-13 audit-round
// fixes F1/F2: the ladder in availability_recover_at must actually gate
// probe FREQUENCY, not only routing release.
//
//   - F1: cycleAll must skip rows whose availability_recover_at is still in
//     the future — otherwise a permanently-revoked key was re-probed every
//     hourly cycle (plus fast reprobe) regardless of the 15m→24h ladder.
//   - F2: a failed cycle probe must not schedule the 5-minute fast reprobe
//     for auth_failed — that single follow-up request defeated the decay.
//     unreachable (plausibly transient) keeps the fast reprobe.
func TestCycleAllRespectsAuthBackoffLadder(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential_probe_v2.go failed: %v", err)
	}
	body := string(src)

	if !strings.Contains(body, "AND (c.availability_recover_at IS NULL OR c.availability_recover_at <= now())") {
		t.Fatal("cycleAll must skip rows whose availability_recover_at is still in the future (F1)")
	}
	if strings.Contains(body, `pr.AvailabilityState == "auth_failed" || pr.AvailabilityState == "unreachable"`) {
		t.Fatal("auth_failed must not trigger the 5-minute fast reprobe (F2)")
	}
}
