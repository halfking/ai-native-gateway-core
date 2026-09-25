package bg

import (
	"context"
	"testing"
	"time"
)

// nil receiver and nil db are both no-ops. The contract: a worker
// built with a nil pool never panics, never returns an error, and
// leaves the started flag false so subsequent Start() calls are
// idempotent on a real pool.
func TestIntegrityFingerprintDrift_NilSafety(t *testing.T) {
	var w *IntegrityFingerprintDrift
	w.Start(context.Background())
	w.Stop() // must not panic
	if w != nil {
		t.Fatal("nil receiver check failed")
	}
}

func TestIntegrityFingerprintDrift_DefaultsMatchPlan(t *testing.T) {
	// We don't have a real DB in unit tests, so the worker can't
	// run; just verify the constructor populates fields. The
	// production defaults are critical for the plan acceptance
	// criteria (1h / 7d / 20 / 0.8), so any silent change here
	// would be a regression we want to catch.
	w := NewIntegrityFingerprintDrift(nil)
	if w.interval != time.Hour {
		t.Fatalf("interval = %v, want 1h", w.interval)
	}
	if w.days != 7 {
		t.Fatalf("days = %d, want 7", w.days)
	}
	if w.minSamples != 20 {
		t.Fatalf("minSamples = %d, want 20", w.minSamples)
	}
	if w.dominantRatio != 0.8 {
		t.Fatalf("dominantRatio = %v, want 0.8", w.dominantRatio)
	}
}

// Stop on a never-Started worker must be a no-op (no panic, no hang).
// This guards the production startup path where Stop may be called
// defensively before Start.
func TestIntegrityFingerprintDrift_StopWithoutStart(t *testing.T) {
	w := NewIntegrityFingerprintDrift(nil)
	w.Stop() // must return immediately
}

// TestFingerprintScanDecision pins the D11 (2026-09-25 audit round 8)
// short-circuit state machine: the drift worker must not repeatedly pay the
// full 7-day canonical-view scan on a database with zero fingerprint
// traffic (252: every system_fingerprint column empty; the scan was a
// full-scan no-op the 30s rolconfig kept killing).
func TestFingerprintScanDecision(t *testing.T) {
	cases := []struct {
		name                              string
		inProcSeen, probeDone, probeEmpty bool
		want                              fingerprintScanAction
	}{
		{"first tick probes", false, false, false, fingerprintScanProbe},
		{"probe found traffic → always scan", false, true, false, fingerprintScanRun},
		{"probe found nothing → skip forever", false, true, true, fingerprintScanSkip},
		{"in-process traffic overrides empty probe", true, true, true, fingerprintScanRun},
		{"in-process traffic on first tick", true, false, false, fingerprintScanRun},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fingerprintScanDecision(tc.inProcSeen, tc.probeDone, tc.probeEmpty)
			if got != tc.want {
				t.Fatalf("fingerprintScanDecision(%v,%v,%v) = %v, want %v",
					tc.inProcSeen, tc.probeDone, tc.probeEmpty, got, tc.want)
			}
		})
	}
}
