package providerprofile

// scorer_signals_test.go — internal package tests for the helper functions
// in scorer_signals.go (calculateDailySnapshotScores, meanStddevCV,
// ExtendedWeights/BackwardCompatWeights totals).

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMeanStddevCV verifies the basic statistics helper used by the
// aggregator to compute QualityStabilitySignal.
func TestMeanStddevCV(t *testing.T) {
	cases := []struct {
		name       string
		values     []float64
		wantMean   float64
		wantStddev float64
		wantCV     float64
	}{
		{"empty", nil, 0, 0, 0},
		{"single value", []float64{85}, 85, 0, 0},
		{"two equal", []float64{80, 80}, 80, 0, 0},
		{"three values: 80, 90, 100", []float64{80, 90, 100}, 90, 8.1649658, 0.0907},
		{"uniform 0..4", []float64{0, 1, 2, 3, 4}, 2, 1.4142135, 0.7071},
		{"zero mean -> cv=0", []float64{-1, 0, 1}, 0, 0.8164, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mean, stddev, cv := meanStddevCV(tc.values)
			assert.InDelta(t, tc.wantMean, mean, 1e-3)
			assert.InDelta(t, tc.wantStddev, stddev, 1e-3)
			assert.InDelta(t, tc.wantCV, cv, 1e-3)
		})
	}
}

// TestExtendedWeightsSumToOne ensures the default extended weight table
// sums to exactly 1.0 — important because CalculateTotalScore relies on
// weightedSum/totalWeight without renormalizing the table itself.
func TestExtendedWeightsSumToOne(t *testing.T) {
	w := DefaultExtendedWeights()
	total := w.Network + w.Availability + w.Stability + w.Scale +
		w.RateLimit + w.Concurrency + w.AvailabilityWindow + w.QualityStability +
		w.Credibility + w.CostAccuracy + w.Price
	assert.InDelta(t, 1.0, total, 1e-9)
}

// TestBackwardCompatWeightsSumToOne ensures the fallback table used when
// only the legacy 4 dimensions have data also sums to 1.0.
func TestBackwardCompatWeightsSumToOne(t *testing.T) {
	w := BackwardCompatWeights()
	total := w.Network + w.Availability + w.Stability + w.Scale
	assert.InDelta(t, 1.0, total, 1e-9)
}

// TestCalculateDailySnapshotScores runs the helper that aggregator uses
// to derive one "daily score" per snapshot. With a single snapshot of
// a healthy credential, the score should be high.
func TestCalculateDailySnapshotScores(t *testing.T) {
	snaps := []*MetricSnapshot{{
		CredentialID: 1,
		NetworkMetrics: &NetworkMetrics{
			P50: 80, P95: 150, P99: 250,
		},
		AvailabilityMetrics: &AvailabilityMetrics{
			TotalRequests: 100, SuccessRequests: 99, AvgTTFTMs: 400, AvgDurationMs: 4000,
		},
		StabilityMetrics: &StabilityMetrics{
			ErrorCount: 1, ErrorTypes: map[string]int{"400": 1},
		},
		ScaleMetrics: &ScaleMetrics{TotalModels: 30, AvailableModels: 28},
	}}
	scores := calculateDailySnapshotScores(snaps)
	assert.Len(t, scores, 1)
	assert.Greater(t, scores[0], 70.0, "healthy single snapshot should score > 70")
}

// TestCalculateDailySnapshotScores_Empty handles empty input.
func TestCalculateDailySnapshotScores_Empty(t *testing.T) {
	scores := calculateDailySnapshotScores(nil)
	assert.Nil(t, scores)

	scores2 := calculateDailySnapshotScores([]*MetricSnapshot{})
	assert.Nil(t, scores2)
}

// TestCalculateDailySnapshotScores_Variation uses snapshots that vary
// in quality so the resulting score sequence has non-zero CV. This is
// the input the aggregator would feed to stampQualityStability.
func TestCalculateDailySnapshotScores_Variation(t *testing.T) {
	// Snapshot 1: great
	good := &MetricSnapshot{
		NetworkMetrics:      &NetworkMetrics{P95: 50},
		AvailabilityMetrics: &AvailabilityMetrics{TotalRequests: 100, SuccessRequests: 99, AvgTTFTMs: 300, AvgDurationMs: 3000},
		StabilityMetrics:    &StabilityMetrics{ErrorCount: 1, ErrorTypes: map[string]int{"400": 1}},
		ScaleMetrics:        &ScaleMetrics{TotalModels: 30, AvailableModels: 28},
	}
	// Snapshot 2: poor
	poor := &MetricSnapshot{
		NetworkMetrics:      &NetworkMetrics{P95: 3000},
		AvailabilityMetrics: &AvailabilityMetrics{TotalRequests: 100, SuccessRequests: 30, AvgTTFTMs: 6000, AvgDurationMs: 35000},
		StabilityMetrics:    &StabilityMetrics{ErrorCount: 70, ErrorTypes: map[string]int{"500": 70}},
		ScaleMetrics:        &ScaleMetrics{TotalModels: 30, AvailableModels: 28},
	}
	scores := calculateDailySnapshotScores([]*MetricSnapshot{good, poor})
	assert.Len(t, scores, 2)
	// Good should score much higher than poor
	assert.Greater(t, scores[0]-scores[1], 20.0)
}
