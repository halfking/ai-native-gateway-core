package routingopt

import (
	"context"
	"testing"
)

func fptr(v float64) *float64 { return &v }

// TestDecideAdaptation_HoldsWithoutEvidence proves the conservative baseline:
// without enough sliding-window samples the policy never churns parameters.
func TestDecideAdaptation_HoldsWithoutEvidence(t *testing.T) {
	prev := fptr(0.7)
	for _, samples := range []int{0, 1, confidenceMinSamples - 1} {
		d, reason := decideAdaptation(prev, 0.99, samples)
		if d != adaptHold || reason != "insufficient samples" {
			t.Fatalf("samples=%d should hold, got %v (%s)", samples, d, reason)
		}
	}
}

// TestDecideAdaptation_FirstCheckpoint proves the first data lands as a
// version checkpoint even without a persisted prior accuracy.
func TestDecideAdaptation_FirstCheckpoint(t *testing.T) {
	d, reason := decideAdaptation(nil, 0.8, 100)
	if d != adaptUpdate || reason != "first accuracy checkpoint" {
		t.Fatalf("nil prev with samples should checkpoint, got %v (%s)", d, reason)
	}
}

// TestDecideAdaptation_ImprovementThreshold proves the +2pp gate: small
// fluctuations must not create parameter-version churn.
func TestDecideAdaptation_ImprovementThreshold(t *testing.T) {
	prev := fptr(0.70)

	if d, _ := decideAdaptation(prev, 0.715, 1000); d != adaptHold {
		t.Fatalf("+1.5pp must hold, got %v", d)
	}
	if d, reason := decideAdaptation(prev, 0.725, 1000); d != adaptUpdate || reason != "accuracy improved" {
		t.Fatalf("+2.5pp must update, got %v (%s)", d, reason)
	}
	// Exactly at the threshold counts as improvement (>=).
	if d, _ := decideAdaptation(prev, 0.72, 1000); d != adaptUpdate {
		t.Fatalf("+2pp exactly must update, got %v", d)
	}
}

// TestDecideAdaptation_DropThreshold proves accuracy drops ≥5pp trigger the
// anomaly path (hold parameters + alert) instead of chasing the drop.
func TestDecideAdaptation_DropThreshold(t *testing.T) {
	prev := fptr(0.80)

	if d, reason := decideAdaptation(prev, 0.76, 1000); d != adaptHold {
		t.Fatalf("-4pp must hold, got %v (%s)", d, reason)
	}
	if d, reason := decideAdaptation(prev, 0.75, 1000); d != adaptAnomaly || reason != "accuracy drop" {
		t.Fatalf("-5pp must be an anomaly, got %v (%s)", d, reason)
	}
	if d, _ := decideAdaptation(prev, 0.60, 1000); d != adaptAnomaly {
		t.Fatalf("-20pp must be an anomaly, got %v", d)
	}
}

// TestAdaptExplorationDecayMath pins the exploration taper used on update:
// 5% → 4% → 3.2% … floor 1%, so a well-performing policy explores less but
// never stops discovering.
func TestAdaptExplorationDecayMath(t *testing.T) {
	cur := 0.05
	for i := 0; i < 10; i++ {
		cur = maxFloat(adaptExplorationFloor, cur*adaptExplorationDecay)
	}
	if cur != adaptExplorationFloor {
		t.Fatalf("10 decay steps should reach the floor %v, got %v", adaptExplorationFloor, cur)
	}
	if maxFloat(0.01, 0.0) != 0.01 {
		t.Fatal("floor guard failed")
	}
}

// TestDetectAnomalies_NilPoolIsQuiet proves the nil-pool (unwired) DAO path
// degrades to "no anomalies" instead of panicking — background workers may
// start before the pool is ready.
func TestDetectAnomalies_NilPoolIsQuiet(t *testing.T) {
	l := NewAdaptiveLearner(nil, nil)
	anomalies, err := l.DetectAnomalies(context.Background())
	if err != nil {
		t.Fatalf("nil-pool DetectAnomalies must not error: %v", err)
	}
	if len(anomalies) != 0 {
		t.Fatalf("nil-pool DetectAnomalies must return no anomalies, got %+v", anomalies)
	}
}
