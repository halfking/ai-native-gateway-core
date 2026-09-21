package autoroute

import (
	"slices"
	"testing"
	"time"
)

func TestEvaluateTreatmentGateRejectsMissingLatencyEvidence(t *testing.T) {
	base := TreatmentOutcome{Key: TreatmentOutcomeKey{Experiment: "exp", Version: "v1"}, Samples: 10, Successes: 10}
	cfg := TreatmentGateConfig{MinSamples: 10, MaxCostRegression: -1, MaxLatencyRegression: 0, MaxFailureRateRegression: -1}

	for _, tc := range []struct {
		name      string
		control   []float64
		treatment []float64
	}{
		{"both missing", nil, nil},
		{"control missing", nil, []float64{10}},
		{"treatment missing", []float64{10}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			control := base
			control.Key.Treatment = TreatmentControl
			control.LatencySamples = tc.control
			treatment := base
			treatment.Key.Treatment = TreatmentVariant
			treatment.LatencySamples = tc.treatment

			got := EvaluateTreatmentGate(control, treatment, cfg, time.Now())
			if got.Allowed || !got.RollbackSuggested || !slices.Contains(got.Reasons, "latency_unavailable") {
				t.Fatalf("gate = %+v, want latency evidence rejection", got)
			}
		})
	}
}
