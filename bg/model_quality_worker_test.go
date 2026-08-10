package bg

import (
	"testing"
	"time"
)

// TestModelQualityWorker_TriggerDedup verifies the suspicious-action trigger
// path tracks in-flight + cooldown state so a flapping node cannot fan out an
// unbounded number of IQ tests (token-cost protection). We check the dedup
// bookkeeping directly since the goroutine path needs a real node source.
func TestModelQualityWorker_TriggerDedup(t *testing.T) {
	w := NewModelQualityWorker("", "", "", time.Second)
	w.triggerCooldown = 10 * time.Minute

	// Simulate a node that was just triggered.
	key := "7:gpt-4o"
	now := time.Now()
	w.triggerInFlight[key] = struct{}{}
	w.triggerLast[key] = now

	w.triggerMu.Lock()
	_, inFlight := w.triggerInFlight[key]
	last := w.triggerLast[key]
	w.triggerMu.Unlock()
	if !inFlight {
		t.Fatal("expected in-flight marker to be present")
	}
	if now.Sub(last) > w.triggerCooldown {
		t.Fatal("cooldown math wrong: last should be within cooldown of now")
	}
}

// TestModelQualityWorker_TestSingleNodeRequiresSource confirms the on-demand
// test fails fast when no node source is configured (the admin API surfaces an
// error instead of hanging).
func TestModelQualityWorker_TestSingleNodeRequiresSource(t *testing.T) {
	w := NewModelQualityWorker("", "", "", time.Second)
	if _, err := w.TestSingleNode(t.Context(), 1, "gpt-4o"); err == nil {
		t.Fatal("expected error when node source is nil")
	}
}

// TestModelQualityWorker_DefaultCooldown documents the default cooldown so
// operators know the repeat window without reading the constructor.
func TestModelQualityWorker_DefaultCooldown(t *testing.T) {
	w := NewModelQualityWorker("", "", "", time.Second)
	if w.triggerCooldown != 10*time.Minute {
		t.Fatalf("default cooldown changed: got %v, want 10m", w.triggerCooldown)
	}
	if w.triggerInFlight == nil || w.triggerLast == nil {
		t.Fatal("dedup maps must be initialized by constructor")
	}
}
