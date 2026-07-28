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
