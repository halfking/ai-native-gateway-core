// bg/active_probe_backoff_test.go — unit tests for the backoff schedule.
//
// The backoff chain (5s / 30s / 2m / 5m / 15m) is the contract between
// the worker and the dashboard: tests assert the chain exactly so any
// accidental edit will fail the suite before reaching production.
package bg

import (
	"testing"
	"time"
)

func TestComputeBackoff_DefaultChain(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 5 * time.Second},
		{2, 30 * time.Second},
		{3, 2 * time.Minute},
		{4, 5 * time.Minute},
		{5, 15 * time.Minute},
		{6, 15 * time.Minute}, // overflow → capped at last
		{7, 15 * time.Minute},
		{99, 15 * time.Minute},
	}
	for _, c := range cases {
		got := computeBackoff(c.attempt)
		if got != c.want {
			t.Errorf("computeBackoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestComputeBackoffForChain_EdgeCases(t *testing.T) {
	chain := []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second}

	// nextAttempt < 1 returns first element
	if got := computeBackoffForChain(0, chain); got != 1*time.Second {
		t.Errorf("attempt=0 should return first element, got %v", got)
	}
	if got := computeBackoffForChain(-5, chain); got != 1*time.Second {
		t.Errorf("attempt=-5 should return first element, got %v", got)
	}

	// exact match
	if got := computeBackoffForChain(2, chain); got != 2*time.Second {
		t.Errorf("attempt=2 should return chain[1], got %v", got)
	}

	// overflow → last element
	if got := computeBackoffForChain(99, chain); got != 3*time.Second {
		t.Errorf("attempt=99 should return last element, got %v", got)
	}

	// empty chain → 0
	if got := computeBackoffForChain(1, nil); got != 0 {
		t.Errorf("nil chain should return 0, got %v", got)
	}
}

func TestDefaultErrorProbeBackoffChain_Length(t *testing.T) {
	// The chain must have exactly 5 entries (matches MaxAttempts default
	// and the documented "5 rounds" guarantee).
	if got := len(DefaultErrorProbeBackoffChain); got != 5 {
		t.Errorf("DefaultErrorProbeBackoffChain length = %d, want 5", got)
	}
}

func TestDefaultErrorProbeBackoffChain_MonotonicNonDecreasing(t *testing.T) {
	// Each step must be >= the previous one (or workers could thrash).
	for i := 1; i < len(DefaultErrorProbeBackoffChain); i++ {
		prev := DefaultErrorProbeBackoffChain[i-1]
		cur := DefaultErrorProbeBackoffChain[i]
		if cur < prev {
			t.Errorf("backoff[%d]=%v < backoff[%d]=%v (not monotonic)",
				i, cur, i-1, prev)
		}
	}
}

func TestDefaultErrorProbeBackoffChain_Within30Minutes(t *testing.T) {
	// Sum of all retries should fit in a 30-minute window so operators
	// don't have to wait an unbounded time for the "final attempt" row.
	var total time.Duration
	for _, d := range DefaultErrorProbeBackoffChain {
		total += d
	}
	const maxAllowed = 30 * time.Minute
	if total > maxAllowed {
		t.Errorf("total backoff chain = %v, exceeds 30m cap (%v)", total, maxAllowed)
	}
}
