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
						ErrorCount: 50,                                   // 5% error rate
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

// 2026-08-07: extended-dimension tests for the four new quality signals
// (rate-limit / concurrency / downtime-window / score-stability).

func TestDefaultScorer_CalculateRateLimitScore(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()

	tests := []struct {
		name        string
		hits, total int
		want        float64
	}{
		{"zero requests -> 100 (no traffic, can't be rate-limited)", 0, 0, 100},
		{"0% hits", 0, 1000, 100},
		{"0.25% hits", 25, 10000, 85},    // 100 - (0.0025/0.005)*30 = 85
		{"0.5% hits -> 70", 5, 1000, 70}, // 阈值点
		{"1% hits -> 40", 10, 1000, 40},  // 阈值点
		{"3% hits -> 20", 30, 1000, 20},  // 40 - (0.03-0.01)/0.04*40 = 20
		{">5% hits -> 0", 100, 1000, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snaps := []*providerprofile.MetricSnapshot{{
				RateLimitMetrics: &providerprofile.RateLimitMetrics{
					RateLimitHits: tt.hits,
					TotalRequests: tt.total,
				},
			}}
			scores := scorer.CalculateDimensionScores(snaps, providerprofile.DefaultWeights())
			assert.InDelta(t, tt.want, scores.RateLimitScore, 2.0)
		})
	}

	// 缺失维度（nil RateLimitMetrics）应返回 0 而不是 100
	t.Run("missing dimension -> 0", func(t *testing.T) {
		snaps := []*providerprofile.MetricSnapshot{{}}
		scores := scorer.CalculateDimensionScores(snaps, providerprofile.DefaultWeights())
		assert.Equal(t, 0.0, scores.RateLimitScore)
	})
}

func TestDefaultScorer_CalculateConcurrencyScore(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()

	tests := []struct {
		name                       string
		hardLimit, autoLimit, want float64
		isCapped                   bool
	}{
		{"unconfigured (EffLimit=0) -> 60 neutral", 0, 0, 60, false},
		{"EffLimit=1 -> 10", 1, 1, 10, false},
		{"EffLimit=10 -> 50", 10, 10, 50, false},
		{"EffLimit=50 -> 100", 50, 50, 100, false},
		{"EffLimit=100 -> 100 (cap)", 100, 100, 100, false},
		{"EffLimit=10 + IsCapped -> 35 (50-15)", 20, 10, 35, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hl, al := int(tt.hardLimit), int(tt.autoLimit)
			snaps := []*providerprofile.MetricSnapshot{{
				ConcurrencyCapacity: &providerprofile.ConcurrencyCapacity{
					ConcurrencyLimit:     hl,
					ConcurrencyLimitAuto: al,
					EffLimit:             al,
					IsCapped:             tt.isCapped,
				},
			}}
			scores := scorer.CalculateDimensionScores(snaps, providerprofile.DefaultWeights())
			assert.InDelta(t, tt.want, scores.ConcurrencyScore, 1.0)
		})
	}

	t.Run("missing dimension -> 0", func(t *testing.T) {
		snaps := []*providerprofile.MetricSnapshot{{}}
		scores := scorer.CalculateDimensionScores(snaps, providerprofile.DefaultWeights())
		assert.Equal(t, 0.0, scores.ConcurrencyScore)
	})
}

func TestDefaultScorer_CalculateAvailabilityWindowScore(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()

	tests := []struct {
		name            string
		downtime, total int
		longest         int
		want            float64
	}{
		{"no buckets -> 100", 0, 0, 0, 100},
		{"1% downtime -> 100", 1, 100, 1, 100},
		{"5% downtime -> ~78", 5, 100, 5, 78},                    // 100 - (0.05-0.01)/0.19*100 ≈ 78.9
		{"10% downtime + LongestRun=2 -> ~52", 10, 100, 2, 52},   // 100 - (0.10-0.01)/0.19*100 ≈ 52.6, LongestRun=2 不触发惩罚
		{"10% downtime + LongestRun=10 -> ~44", 10, 100, 10, 44}, // 52.6 - (10-6)*2 = 44.6
		{"20% downtime -> 0", 20, 100, 20, 0},
		{"LongestRun=12 -> extra penalty", 5, 100, 12, 66},         // 78.9 - (12-6)*2 = 66.9
		{"LongestRun=24 -> penalty capped at -30", 5, 100, 24, 48}, // 78.9 - 30 = 48.9
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snaps := []*providerprofile.MetricSnapshot{{
				AvailabilityWindow: &providerprofile.AvailabilityWindow{
					DowntimeBuckets: tt.downtime,
					TotalBuckets:    tt.total,
					LongestRun:      tt.longest,
				},
			}}
			scores := scorer.CalculateDimensionScores(snaps, providerprofile.DefaultWeights())
			assert.InDelta(t, tt.want, scores.AvailabilityWindowScore, 3.0)
		})
	}

	t.Run("missing dimension -> 0", func(t *testing.T) {
		snaps := []*providerprofile.MetricSnapshot{{}}
		scores := scorer.CalculateDimensionScores(snaps, providerprofile.DefaultWeights())
		assert.Equal(t, 0.0, scores.AvailabilityWindowScore)
	})
}

func TestDefaultScorer_CalculateQualityStabilityScore(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()

	tests := []struct {
		name    string
		cv      float64
		sampleN int
		want    float64
	}{
		{"CV=0.02 stable -> 100", 0.02, 12, 100},
		{"CV=0.05 borderline -> 100", 0.05, 12, 100},
		{"CV=0.10 -> ~67", 0.10, 12, 66.6},
		{"CV=0.20 -> 0", 0.20, 12, 0},
		{"CV=0.30 very volatile -> 0", 0.30, 12, 0},
		{"single sample -> 100 (no data)", 0.50, 1, 100}, // <2 样本不扣分
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snaps := []*providerprofile.MetricSnapshot{{
				QualityStabilitySignal: &providerprofile.QualityStabilitySignal{
					CV:      tt.cv,
					SampleN: tt.sampleN,
				},
			}}
			scores := scorer.CalculateDimensionScores(snaps, providerprofile.DefaultWeights())
			assert.InDelta(t, tt.want, scores.QualityStabilityScore, 2.0)
		})
	}

	t.Run("missing dimension -> 0", func(t *testing.T) {
		snaps := []*providerprofile.MetricSnapshot{{}}
		scores := scorer.CalculateDimensionScores(snaps, providerprofile.DefaultWeights())
		assert.Equal(t, 0.0, scores.QualityStabilityScore)
	})
}

func TestDefaultScorer_CalculateTotalScore_ExtendedWeights(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()
	weights := providerprofile.DefaultWeights()

	// 所有 8 个维度都填满（4 老 + 4 新）
	snaps := []*providerprofile.MetricSnapshot{{
		NetworkMetrics:         &providerprofile.NetworkMetrics{P95: 150},
		AvailabilityMetrics:    &providerprofile.AvailabilityMetrics{TotalRequests: 100, SuccessRequests: 98, AvgTTFTMs: 400, AvgDurationMs: 4000},
		StabilityMetrics:       &providerprofile.StabilityMetrics{ErrorCount: 2, ErrorTypes: map[string]int{"400": 2}},
		ScaleMetrics:           &providerprofile.ScaleMetrics{TotalModels: 30, AvailableModels: 28},
		RateLimitMetrics:       &providerprofile.RateLimitMetrics{RateLimitHits: 0, TotalRequests: 100},
		ConcurrencyCapacity:    &providerprofile.ConcurrencyCapacity{EffLimit: 20},
		AvailabilityWindow:     &providerprofile.AvailabilityWindow{DowntimeBuckets: 1, TotalBuckets: 24, LongestRun: 1},
		QualityStabilitySignal: &providerprofile.QualityStabilitySignal{CV: 0.04, SampleN: 12},
	}}

	scores := scorer.CalculateDimensionScores(snaps, weights)
	total := scorer.CalculateTotalScore(scores, weights)

	// 所有维度都应有非零分，总分应在一个合理区间（70-100）
	assert.Greater(t, total, 70.0, "all dimensions filled, total should be high")
	assert.LessOrEqual(t, total, 100.0)

	// 所有新维度分数都应该 > 0
	assert.Greater(t, scores.RateLimitScore, 0.0)
	assert.Greater(t, scores.ConcurrencyScore, 0.0)
	assert.Greater(t, scores.AvailabilityWindowScore, 0.0)
	assert.Greater(t, scores.QualityStabilityScore, 0.0)
}

func TestDefaultScorer_ExtendedDimensions_PartialMissing(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()
	weights := providerprofile.DefaultWeights()

	// 仅 RateLimitMetrics 缺失，其它 7 维都填
	snaps := []*providerprofile.MetricSnapshot{{
		NetworkMetrics:      &providerprofile.NetworkMetrics{P95: 150},
		AvailabilityMetrics: &providerprofile.AvailabilityMetrics{TotalRequests: 100, SuccessRequests: 98, AvgTTFTMs: 400, AvgDurationMs: 4000},
		StabilityMetrics:    &providerprofile.StabilityMetrics{ErrorCount: 2, ErrorTypes: map[string]int{"400": 2}},
		ScaleMetrics:        &providerprofile.ScaleMetrics{TotalModels: 30, AvailableModels: 28},
		// RateLimitMetrics 缺失
		ConcurrencyCapacity:    &providerprofile.ConcurrencyCapacity{EffLimit: 20},
		AvailabilityWindow:     &providerprofile.AvailabilityWindow{DowntimeBuckets: 1, TotalBuckets: 24},
		QualityStabilitySignal: &providerprofile.QualityStabilitySignal{CV: 0.04, SampleN: 12},
	}}

	scores := scorer.CalculateDimensionScores(snaps, weights)

	assert.Equal(t, 0.0, scores.RateLimitScore, "missing RateLimitMetrics should be 0")

	// 因为 RateLimitScore=0 但其它 3 个新维度都 >0，CalculateTotalScore
	// 仍然使用 ExtendedWeights（hasNewDim=true）。总分应仍 > 0。
	total := scorer.CalculateTotalScore(scores, weights)
	assert.Greater(t, total, 0.0)
}

func TestDefaultScorer_CalculateTotalScore_BackwardsCompat(t *testing.T) {
	scorer := providerprofile.NewDefaultScorer()
	weights := providerprofile.DefaultWeights()

	// 仅老 4 维有值（所有新维度都 nil，模拟旧版快照）
	snaps := []*providerprofile.MetricSnapshot{{
		NetworkMetrics:      &providerprofile.NetworkMetrics{P95: 150},
		AvailabilityMetrics: &providerprofile.AvailabilityMetrics{TotalRequests: 100, SuccessRequests: 98, AvgTTFTMs: 400, AvgDurationMs: 4000},
		StabilityMetrics:    &providerprofile.StabilityMetrics{ErrorCount: 2, ErrorTypes: map[string]int{"400": 2}},
		ScaleMetrics:        &providerprofile.ScaleMetrics{TotalModels: 30, AvailableModels: 28},
	}}

	scores := scorer.CalculateDimensionScores(snaps, weights)

	// 新维度应全部为 0（缺失）
	assert.Equal(t, 0.0, scores.RateLimitScore)
	assert.Equal(t, 0.0, scores.ConcurrencyScore)
	assert.Equal(t, 0.0, scores.AvailabilityWindowScore)
	assert.Equal(t, 0.0, scores.QualityStabilityScore)

	total := scorer.CalculateTotalScore(scores, weights)
	// 老逻辑用 weights {Network:0.10, Availability:0.20, Stability:0.20, Scale:0.05} 总和 0.55，
	// BackwardCompatWeights 把它们归一化到总和 1.0，等价于原值。
	assert.Greater(t, total, 70.0)
	assert.LessOrEqual(t, total, 100.0)
}
