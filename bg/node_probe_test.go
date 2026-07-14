package bg

import (
	"testing"
	"time"
)

// TestNodeProbeBackoffLadder pins the 5s/30s/60s/5m/1h/2h/24h
// sequence mandated by the spec — any change here must be a
// deliberate spec update.
func TestNodeProbeBackoffLadder(t *testing.T) {
	want := []int{5, 30, 60, 300, 3600, 7200, 86400}
	if len(nodeProbeBackoff) != len(want) {
		t.Fatalf("len mismatch: got %d want %d", len(nodeProbeBackoff), len(want))
	}
	for i, v := range want {
		if nodeProbeBackoff[i] != v {
			t.Fatalf("backoff[%d] = %d, want %d", i, nodeProbeBackoff[i], v)
		}
	}
}

// TestNodeProbeMaxAttempts ensures attempt=7 marks paused (the
// "max one day" cap from the spec).
func TestNodeProbeMaxAttempts(t *testing.T) {
	if nodeProbeMaxAttempts != 7 {
		t.Fatalf("expected 7, got %d", nodeProbeMaxAttempts)
	}
}

// TestNodeProbeInFlightWindow sanity-checks the dedup window.
func TestNodeProbeInFlightWindow(t *testing.T) {
	if nodeProbeInFlightWindow < 1*time.Minute {
		t.Fatalf("in-flight window too short: %v", nodeProbeInFlightWindow)
	}
}

// TestNodeProbeDedupHashStable ensures the same (cred, model) pair
// produces the same hash across calls (used for testing the dedup
// map key).
func TestNodeProbeDedupHashStable(t *testing.T) {
	a := nodeProbeDedupHash(42, "gpt-5.4")
	b := nodeProbeDedupHash(42, "gpt-5.4")
	if a != b {
		t.Fatalf("hash not stable: %q vs %q", a, b)
	}
	if nodeProbeDedupHash(43, "gpt-5.4") == a {
		t.Fatalf("hash should differ across cred IDs")
	}
}
