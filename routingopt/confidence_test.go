package routingopt

import (
	"context"
	"testing"
	"time"
)

// TestAdjustClassConfidence_NotEnoughSamples proves the baseline guarantee:
// task types with little feedback history keep the classifier confidence.
func TestAdjustClassConfidence_NotEnoughSamples(t *testing.T) {
	stats := map[string]TaskAccuracyStat{
		"code": {Accuracy: 0.3, Samples: confidenceMinSamples - 1}, // just below threshold
	}
	if got := AdjustClassConfidence("code", 0.9, stats); got != 0.9 {
		t.Fatalf("insufficient samples must not adjust confidence: got %v want 0.9", got)
	}
	if got := AdjustClassConfidence("unknown-task", 0.9, stats); got != 0.9 {
		t.Fatalf("unknown task type must not adjust confidence: got %v", got)
	}
	if got := AdjustClassConfidence("code", 0.9, nil); got != 0.9 {
		t.Fatalf("nil stats must not adjust confidence: got %v", got)
	}
}

// TestAdjustClassConfidence_DampsLowAccuracy proves runtime learning:
// a task type routing poorly gets its confidence damped, which makes the
// Decider's LLM-fallback threshold trigger earlier exactly where the
// heuristic classifier is weakest.
func TestAdjustClassConfidence_DampsLowAccuracy(t *testing.T) {
	stats := map[string]TaskAccuracyStat{
		"code": {Accuracy: 0.5, Samples: 100}, // 25pp below baseline
	}
	// delta = (0.5-0.75)*0.2 = -0.05
	got := AdjustClassConfidence("code", 0.72, stats)
	if diff := got - 0.67; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("low accuracy should damp 0.72 → 0.67, got %v", got)
	}
}

// TestAdjustClassConfidence_BoostsHighAccuracy proves accurate task types
// stay on the fast heuristic path.
func TestAdjustClassConfidence_BoostsHighAccuracy(t *testing.T) {
	stats := map[string]TaskAccuracyStat{
		"chat": {Accuracy: 1.0, Samples: 200}, // 25pp above baseline
	}
	// delta = (1.0-0.75)*0.2 = +0.05
	got := AdjustClassConfidence("chat", 0.72, stats)
	if diff := got - 0.77; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("high accuracy should boost 0.72 → 0.77, got %v", got)
	}
}

// TestAdjustClassConfidence_Clamps proves the delta cap and [0,1] clamping.
func TestAdjustClassConfidence_Clamps(t *testing.T) {
	// Delta cap: (0.0-0.75)*0.2 = -0.15 would exceed the 0.1 cap.
	stats := map[string]TaskAccuracyStat{"code": {Accuracy: 0.0, Samples: 500}}
	if got := AdjustClassConfidence("code", 0.95, stats); got != 0.85 {
		t.Fatalf("delta must be capped at ±0.1: 0.95 → 0.85, got %v", got)
	}
	// Upper clamp: confidence already near 1 with a positive delta.
	statsHi := map[string]TaskAccuracyStat{"chat": {Accuracy: 1.0, Samples: 500}}
	if got := AdjustClassConfidence("chat", 0.98, statsHi); got != 1.0 {
		t.Fatalf("adjusted confidence must clamp at 1.0, got %v", got)
	}
	// Lower clamp.
	statsLo := map[string]TaskAccuracyStat{"code": {Accuracy: 0.0, Samples: 500}}
	if got := AdjustClassConfidence("code", 0.05, statsLo); got != 0.0 {
		t.Fatalf("adjusted confidence must clamp at 0.0, got %v", got)
	}
}

// TestConfidenceAdjuster_CachesSnapshot proves the 60s TTL cache keeps the
// Decide hot path off the DB (one DAO read per TTL, not per request).
func TestConfidenceAdjuster_CachesSnapshot(t *testing.T) {
	// pool=nil → DAO read would panic if hit; a primed cache must avoid it.
	c := NewConfidenceAdjuster(nil)
	c.mu.Lock()
	c.taskStats = map[string]TaskAccuracyStat{
		"code": {Accuracy: 0.5, Samples: 100},
	}
	c.refreshedAt = time.Now()
	c.mu.Unlock()

	got, err := c.PostClassify(context.Background(), "code", 0.72)
	if err != nil {
		t.Fatalf("PostClassify must not fail on cached snapshot: %v", err)
	}
	if diff := got - 0.67; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cached path should apply damping 0.72 → 0.67, got %v", got)
	}
}

// TestConfidenceAdjuster_StaleSnapshotDegradesToBaseline proves a failed DAO
// read never breaks routing: with no data the confidence passes through.
func TestConfidenceAdjuster_StaleSnapshotDegradesToBaseline(t *testing.T) {
	c := NewConfidenceAdjuster(nil) // nil pool → snapshot read fails/returns empty
	got, err := c.PostClassify(context.Background(), "code", 0.9)
	if err != nil {
		t.Fatalf("PostClassify must not error on empty snapshot: %v", err)
	}
	if got != 0.9 {
		t.Fatalf("empty snapshot must pass confidence through: got %v", got)
	}
}
