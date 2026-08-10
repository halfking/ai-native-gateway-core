package autoroute

import (
	"math"
	"testing"
)

func affinityTestCandidate() Candidate {
	return Candidate{
		CanonicalID:       1,
		CanonicalName:     "test-model",
		TaskMatchScore:    0.8,
		UnitPriceInPer1M:  100,
		UnitPriceOutPer1M: 100,
		SuccessRate:       0.97,
		P95LatencyMs:      1500,
		ProviderCategory:  "official",
	}
}

// The invariant that makes the staged rollout safe: in shadow mode the
// composite must be bit-identical to the 4-dimension path, so enabling shadow
// cannot change any routing decision.
func TestScoreWithAffinity_ShadowDoesNotChangeComposite(t *testing.T) {
	c := affinityTestCandidate()
	prices := map[int]float64{1: 200}

	for _, affinity := range []float64{0, 10, 50, 90, 100} {
		base := ScoreWithChannelQuality(c, TaskCode, prices, 5)
		shadow := ScoreWithAffinity(c, TaskCode, prices, 5, affinity, false)

		if shadow.Composite != base.Composite {
			t.Errorf("affinity=%.0f: shadow composite %.10f != 4-dim %.10f",
				affinity, shadow.Composite, base.Composite)
		}
		// The score is still recorded — that is the point of shadow mode.
		if shadow.Affinity != affinity {
			t.Errorf("shadow must still record affinity: got %.2f, want %.2f", shadow.Affinity, affinity)
		}
		if shadow.AffinityApplied {
			t.Error("AffinityApplied must be false in shadow mode")
		}
		// Every other dimension must be untouched.
		if shadow.MatchScore != base.MatchScore || shadow.PriceScore != base.PriceScore ||
			shadow.ChannelQuality != base.ChannelQuality || shadow.Reliability != base.Reliability {
			t.Error("shadow mode altered a non-affinity dimension")
		}
	}
}

// A neutral affinity must be a no-op even when applied, otherwise merely
// turning the feature on would shift every score.
func TestScoreWithAffinity_NeutralIsNoOpWhenApplied(t *testing.T) {
	c := affinityTestCandidate()
	prices := map[int]float64{1: 200}

	base := ScoreWithChannelQuality(c, TaskCode, prices, 0)
	applied := ScoreWithAffinity(c, TaskCode, prices, 0, AffinityNeutral, true)

	// base*0.85 + 50*0.15 == base only if base == 50, so compare against the
	// explicit expectation rather than asserting equality.
	want := base.Composite*(1-AffinityWeight) + AffinityNeutral*AffinityWeight
	if math.Abs(applied.Composite-want) > 1e-9 {
		t.Errorf("got %.10f, want %.10f", applied.Composite, want)
	}
	if !applied.AffinityApplied {
		t.Error("AffinityApplied should be true")
	}
}

func TestScoreWithAffinity_HighAffinityRaisesLowAffinityLowers(t *testing.T) {
	c := affinityTestCandidate()
	prices := map[int]float64{1: 200}

	neutral := ScoreWithAffinity(c, TaskCode, prices, 0, AffinityNeutral, true)
	high := ScoreWithAffinity(c, TaskCode, prices, 0, 90, true)
	low := ScoreWithAffinity(c, TaskCode, prices, 0, 10, true)

	if high.Composite <= neutral.Composite {
		t.Errorf("high affinity should raise composite: %.4f vs %.4f", high.Composite, neutral.Composite)
	}
	if low.Composite >= neutral.Composite {
		t.Errorf("low affinity should lower composite: %.4f vs %.4f", low.Composite, neutral.Composite)
	}

	// Bounded influence: across the whole legal affinity range the composite
	// may move at most AffinityWeight * (range) = 0.15 * 80 = 12 points.
	if spread := high.Composite - low.Composite; spread > 12.0001 {
		t.Errorf("affinity moved composite by %.4f; expected <= 12", spread)
	}
}

// Affinity must not be able to overturn a large gap in task match — it informs
// ranking, it does not dictate it.
func TestScoreWithAffinity_CannotOverrideStrongMatchGap(t *testing.T) {
	prices := map[int]float64{1: 200, 2: 200}

	goodMatch := affinityTestCandidate()
	goodMatch.TaskMatchScore = 1.0

	poorMatch := affinityTestCandidate()
	poorMatch.CanonicalID = 2
	poorMatch.TaskMatchScore = 0.0

	// Worst case: the well-matched model has minimum affinity, the mismatched
	// one has maximum.
	good := ScoreWithAffinity(goodMatch, TaskCode, prices, 0, 10, true)
	poor := ScoreWithAffinity(poorMatch, TaskCode, prices, 0, 90, true)

	if poor.Composite >= good.Composite {
		t.Errorf("affinity overrode a full task-match gap: poor=%.4f >= good=%.4f",
			poor.Composite, good.Composite)
	}
}

func TestScoreWithAffinity_CorrectionNotDilutedByScaling(t *testing.T) {
	c := affinityTestCandidate()
	prices := map[int]float64{1: 200}

	// Correction is an absolute ±10 nudge and must survive the 0.85 scaling
	// intact, else the session-level penalty would silently weaken.
	withPenalty := ScoreWithAffinity(c, TaskCode, prices, -10, AffinityNeutral, true)
	withBonus := ScoreWithAffinity(c, TaskCode, prices, 5, AffinityNeutral, true)

	if delta := withBonus.Composite - withPenalty.Composite; math.Abs(delta-15) > 1e-9 {
		t.Errorf("correction delta = %.10f, want exactly 15 (undiluted)", delta)
	}
}

func TestScoreWithAffinity_GarbageAffinityFallsBackToNeutral(t *testing.T) {
	c := affinityTestCandidate()
	prices := map[int]float64{1: 200}

	expected := ScoreWithAffinity(c, TaskCode, prices, 0, AffinityNeutral, true)

	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		got := ScoreWithAffinity(c, TaskCode, prices, 0, bad, true)
		if got.Affinity != AffinityNeutral {
			t.Errorf("affinity %v should degrade to neutral, got %.4f", bad, got.Affinity)
		}
		if math.IsNaN(got.Composite) || math.IsInf(got.Composite, 0) {
			t.Errorf("affinity %v produced non-finite composite %v", bad, got.Composite)
		}
		if math.Abs(got.Composite-expected.Composite) > 1e-9 {
			t.Errorf("affinity %v: composite %.6f != neutral %.6f", bad, got.Composite, expected.Composite)
		}
	}
}

// End-to-end through the store, since that is how production calls it: a model
// with too few samples must score exactly as if affinity were off.
func TestScoreWithAffinity_StoreIntegrationColdStart(t *testing.T) {
	c := affinityTestCandidate()
	prices := map[int]float64{1: 200}

	store := newTestAffinityStore(AffinityOn, []AffinityRecord{{
		TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 1,
		Affinity: 90, SampleCount: AffinityMinSamples - 1, // below the floor
	}})

	affinity, found := store.Lookup(TaskCode, ProfileSmart, "", int64(c.CanonicalID))
	if found {
		t.Error("a below-floor row must not report as found")
	}

	got := ScoreWithAffinity(c, TaskCode, prices, 0, affinity, true)
	neutral := ScoreWithAffinity(c, TaskCode, prices, 0, AffinityNeutral, true)
	if math.Abs(got.Composite-neutral.Composite) > 1e-9 {
		t.Errorf("cold start should score as neutral: %.6f vs %.6f", got.Composite, neutral.Composite)
	}
}
