package routingopt

import (
	"context"
	"errors"
	"testing"
	"time"
)

// confidence_correction_test.go — taskprofile correction blending into the
// PostClassify confidence adjuster (2026-09-18).

func TestBlendCorrectionsIntoAccuracy_EmptyIsIdentity(t *testing.T) {
	stats := map[string]TaskAccuracyStat{"coding": {Accuracy: 0.9, Samples: 50}}
	got := BlendCorrectionsIntoAccuracy(stats, nil)
	if got["coding"].Accuracy != 0.9 || got["coding"].Samples != 50 {
		t.Fatalf("empty corrections must be identity, got %+v", got)
	}
	got = BlendCorrectionsIntoAccuracy(stats, map[string]TaskCorrectionStat{})
	if got["coding"].Samples != 50 {
		t.Fatalf("zero corrections must be identity, got %+v", got)
	}
}

func TestBlendCorrectionsIntoAccuracy_HumanWeightTwo(t *testing.T) {
	// 50 auto samples at 0.9 accuracy; 10 corrections all agreeing.
	stats := map[string]TaskAccuracyStat{"coding": {Accuracy: 0.9, Samples: 50}}
	cs := map[string]TaskCorrectionStat{"coding": {Total: 10, Agrees: 10}}
	got := BlendCorrectionsIntoAccuracy(stats, cs)["coding"]

	wantAcc := (0.9*50 + 1.0*2.0*10) / (50 + 2.0*10) // = 65/70 ≈ 0.9286
	if diff := got.Accuracy - wantAcc; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("accuracy = %.6f, want %.6f (human ×2 weight)", got.Accuracy, wantAcc)
	}
	if got.Samples != 70 {
		t.Fatalf("samples = %d, want 70 (50 + 2×10)", got.Samples)
	}
	// Confirmation raises accuracy → PostClassify delta moves positive.
	if got.Accuracy <= 0.9 {
		t.Fatalf("confirmed corrections must not lower accuracy: %.4f", got.Accuracy)
	}
}

func TestBlendCorrectionsIntoAccuracy_CorrectionsDamp(t *testing.T) {
	// 6 corrections, only 2 agree → humanRate 1/3 drags accuracy down.
	stats := map[string]TaskAccuracyStat{"documentation": {Accuracy: 0.9, Samples: 50}}
	cs := map[string]TaskCorrectionStat{"documentation": {Total: 6, Agrees: 2}}
	got := BlendCorrectionsIntoAccuracy(stats, cs)["documentation"]
	if got.Accuracy >= 0.9 {
		t.Fatalf("corrections must damp accuracy, got %.4f", got.Accuracy)
	}
}

func TestBlendCorrectionsIntoAccuracy_SeedsPureHumanStat(t *testing.T) {
	// Type with no auto feedback: corrections seed the stat at samples = 2×C.
	cs := map[string]TaskCorrectionStat{"mystery": {Total: 5, Agrees: 1}}
	got := BlendCorrectionsIntoAccuracy(map[string]TaskAccuracyStat{}, cs)["mystery"]
	if got.Samples != 10 {
		t.Fatalf("seeded samples = %d, want 10", got.Samples)
	}
	if want := 1.0 / 5.0; got.Accuracy != want { // pure-human stat == humanRate
		t.Fatalf("seeded accuracy = %.4f, want %.4f", got.Accuracy, want)
	}
	// 10 corrections is exactly the confidenceMinSamples gate — fewer must
	// not be enough on their own (gate uses stat.Samples ≥ 20 → 2×C ≥ 20).
	if got.Samples >= confidenceMinSamples && confidenceMinSamples > 10 {
		t.Logf("gate check: samples %d vs min %d (informational)", got.Samples, confidenceMinSamples)
	}
}

func TestBlendCorrectionsIntoAccuracy_DoesNotMutateInput(t *testing.T) {
	stats := map[string]TaskAccuracyStat{"coding": {Accuracy: 0.9, Samples: 50}}
	cs := map[string]TaskCorrectionStat{"coding": {Total: 10, Agrees: 0}}
	_ = BlendCorrectionsIntoAccuracy(stats, cs)
	if stats["coding"].Accuracy != 0.9 || stats["coding"].Samples != 50 {
		t.Fatal("input stats map mutated")
	}
}

// stubCorrectionSource drives the PostClassify path without a DB.
type stubCorrectionSource struct {
	stats map[string]TaskCorrectionStat
	err   error
}

func (s *stubCorrectionSource) CorrectionStats(ctx context.Context, since time.Time) (map[string]TaskCorrectionStat, error) {
	return s.stats, s.err
}

func newTestAdjusterWithSource(t *testing.T, src CorrectionSource) *ConfidenceAdjuster {
	t.Helper()
	adj := NewConfidenceAdjuster(nil) // nil pool → GetTaskTypeAccuracy returns empty, no I/O
	adj.SetCorrectionSource(src)
	t.Cleanup(func() {
		confidenceBaseline, confidenceStrength, confidenceMinSamples, confidenceMaxDelta, confidenceTTL =
			0.75, 0.2, 20, 0.1, 60*time.Second
	})
	return adj
}

func TestPostClassify_WithCorrectionSource_DampsWeakTaskType(t *testing.T) {
	adj := newTestAdjusterWithSource(t, &stubCorrectionSource{stats: map[string]TaskCorrectionStat{
		"documentation": {Total: 20, Agrees: 4}, // rate 0.8 → human-only acc 0.2 → clamped -0.1
	}})
	got, err := adj.PostClassify(context.Background(), "documentation", 0.8)
	if err != nil {
		t.Fatalf("PostClassify: %v", err)
	}
	if want := 0.8 - confidenceMaxDelta; got != want {
		t.Fatalf("PostClassify = %.4f, want %.4f (clamped negative delta)", got, want)
	}
	// A type without corrections stays untouched.
	other, err := adj.PostClassify(context.Background(), "coding", 0.8)
	if err != nil || other != 0.8 {
		t.Fatalf("uncorrected type = (%.2f, %v), want (0.80, nil)", other, err)
	}
}

func TestPostClassify_CorrectionSourceError_DegradesToBaseline(t *testing.T) {
	adj := newTestAdjusterWithSource(t, &stubCorrectionSource{err: errors.New("db down")})
	got, err := adj.PostClassify(context.Background(), "documentation", 0.8)
	if err != nil {
		t.Fatalf("source error must not escape: %v", err)
	}
	if got != 0.8 {
		t.Fatalf("source error must degrade to unadjusted confidence, got %.2f", got)
	}
}

func TestSetCorrectionSource_NilReceiverSafe(t *testing.T) {
	var adj *ConfidenceAdjuster
	adj.SetCorrectionSource(nil) // must not panic
	opt := (*RealOptimizer)(nil)
	opt.WithCorrectionSource(nil) // must not panic
}
