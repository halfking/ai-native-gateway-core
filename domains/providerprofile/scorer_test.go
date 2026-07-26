package providerprofile_test

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
)

func TestDefaultScorer_CalculateNetworkScore(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()

	tests := []struct {
		name      string
		snapshots []*providerprofile.MetricSnapshot
		expected  float64
	}{
		{
			name: "excellent network - P95 50ms",
			snapshots: []*providerprofile.MetricSnapshot{
				{
					NetworkMetrics: &providerprofile.NetworkMetrics{P95: 50},
				},
			},
			expected: 100,
		},
		{
			name: "good network - P95 200ms",
			snapshots: []*providerprofile.MetricSnapshot{
				{
					NetworkMetrics: &providerprofile.NetworkMetrics{P95: 200},
				},
			},
			expected: 90, // 100 - (200-100)/200*20 = 90
		},
		{
			name: "poor network - P95 2000ms",
			snapshots: []*providerprofile.MetricSnapshot{
				{
					NetworkMetrics: &providerprofile.NetworkMetrics{P95: 2000},
				},
			},
			expected: 25, // 50 - (2000-1000)/2000*50 = 25
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scores := scorer.CalculateDimensionScores(tt.snapshots, providerprofile.DefaultWeights())
			assert.InDelta(t, tt.expected, scores.NetworkScore, 1.0)
		})
	}
}

func TestDefaultScorer_CalculateAvailabilityScore(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()

	snapshots := []*providerprofile.MetricSnapshot{
		{
			AvailabilityMetrics: &providerprofile.AvailabilityMetrics{
				TotalRequests:   100,
				SuccessRequests: 95,
				AvgTTFTMs:       300,
				AvgDurationMs:   3000,
			},
		},
	}

	scores := scorer.CalculateDimensionScores(snapshots, providerprofile.DefaultWeights())

	// 成功率95% * 0.7 + TTFT(300ms)100分 * 0.15 + Duration(3s)100分 * 0.15
	// = 66.5 + 15 + 15 = 96.5
	assert.InDelta(t, 96.5, scores.AvailabilityScore, 1.0)
}

func TestDefaultScorer_CalculateStabilityScore(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()

	tests := []struct {
		name      string
		snapshots []*providerprofile.MetricSnapshot
		minScore  float64
		maxScore  float64
	}{
		{
			name: "excellent stability - 0.5% error rate",
			snapshots: []*providerprofile.MetricSnapshot{
				{
					AvailabilityMetrics: &providerprofile.AvailabilityMetrics{
						TotalRequests: 1000,
					},
					StabilityMetrics: &providerprofile.StabilityMetrics{
						ErrorCount: 5,
						ErrorTypes: map[string]int{"400": 5},
					},
				},
			},
			minScore: 99,
			maxScore: 100,
		},
		{
			name: "poor stability with 5xx errors",
			snapshots: []*providerprofile.MetricSnapshot{
				{
					AvailabilityMetrics: &providerprofile.AvailabilityMetrics{
						TotalRequests: 1000,
					},
					StabilityMetrics: &providerprofile.StabilityMetrics{
						ErrorCount: 50, // 5% error rate
						ErrorTypes: map[string]int{"500": 30, "400": 20}, // 3% 5xx errors
					},
				},
			},
			minScore: 50, // Base 80 - penalty 30 = 50
			maxScore: 52,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scores := scorer.CalculateDimensionScores(tt.snapshots, providerprofile.DefaultWeights())
			assert.GreaterOrEqual(t, scores.StabilityScore, tt.minScore)
			assert.LessOrEqual(t, scores.StabilityScore, tt.maxScore)
		})
	}
}

func TestDefaultScorer_CalculateScaleScore(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()

	snapshots := []*providerprofile.MetricSnapshot{
		{
			ScaleMetrics: &providerprofile.ScaleMetrics{
				TotalModels:     50,
				AvailableModels: 45,
			},
		},
	}

	scores := scorer.CalculateDimensionScores(snapshots, providerprofile.DefaultWeights())

	// 可用率 90% * 0.6 + 模型数量(50个)100分 * 0.4 = 54 + 40 = 94
	assert.InDelta(t, 94, scores.ScaleScore, 1.0)
}

func TestDefaultScorer_CalculateTotalScore(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()

	snapshots := []*providerprofile.MetricSnapshot{
		{
			MetricTime: time.Now(),
			NetworkMetrics: &providerprofile.NetworkMetrics{
				P50: 80,
				P95: 150,
				P99: 250,
			},
			AvailabilityMetrics: &providerprofile.AvailabilityMetrics{
				TotalRequests:   100,
				SuccessRequests: 98,
				AvgTTFTMs:       400,
				AvgDurationMs:   4000,
			},
			StabilityMetrics: &providerprofile.StabilityMetrics{
				ErrorCount: 2,
				ErrorTypes: map[string]int{"400": 2},
			},
			ScaleMetrics: &providerprofile.ScaleMetrics{
				TotalModels:     30,
				AvailableModels: 28,
			},
		},
	}

	weights := providerprofile.DefaultWeights()
	scores := scorer.CalculateDimensionScores(snapshots, weights)
	totalScore := scorer.CalculateTotalScore(scores, weights)

	// 应该得到一个合理的总分（考虑到只有4个维度有分数）
	assert.Greater(t, totalScore, 70.0)
	assert.LessOrEqual(t, totalScore, 100.0)
}

func TestDefaultScorer_EmptySnapshots(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()

	scores := scorer.CalculateDimensionScores([]*providerprofile.MetricSnapshot{}, providerprofile.DefaultWeights())

	assert.Equal(t, 0.0, scores.NetworkScore)
	assert.Equal(t, 0.0, scores.AvailabilityScore)
	assert.Equal(t, 0.0, scores.StabilityScore)
	assert.Equal(t, 0.0, scores.ScaleScore)
}
