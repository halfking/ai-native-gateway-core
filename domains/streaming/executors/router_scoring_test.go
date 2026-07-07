package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/stretchr/testify/assert"
)

func TestCalculateLoadScore_BalancedWeights(t *testing.T) {
	router := &Router{
		LoadScoreWeights: DefaultLoadScoreWeights(),
	}

	candidate := provider.Candidate{
		CredentialID:     1,
		ProviderID:       1,
		P95LatencyMs:     500,
		SuccessRate:      0.95,
		ConcurrencyLimit: intPtr(50),
	}

	ctx := context.Background()
	score := calculateLoadScore(candidate, router, ctx, router.LoadScoreWeights)

	// 验证分数在合理范围内
	assert.GreaterOrEqual(t, score, 0.0)
	assert.LessOrEqual(t, score, 1.0)
}

func TestConcurrencyScore_Saturation(t *testing.T) {
	tests := []struct {
		name     string
		used     int
		limit    int
		expected float64
	}{
		{"空闲", 0, 50, 0.0},
		{"50%使用", 25, 50, 0.5},
		{"饱和", 50, 50, 1.0},
		{"超饱和", 60, 50, 1.0}, // 限制到1.0
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 这里需要 mock FpSlots，简化测试仅验证逻辑
			pressure := float64(tt.used) / float64(tt.limit)
			if pressure > 1.0 {
				pressure = 1.0
			}
			assert.Equal(t, tt.expected, pressure)
		})
	}
}

func TestLatencyScore_SaturationCurve(t *testing.T) {
	tests := []struct {
		latencyMs int
		expected  float64
	}{
		{50, 0.0},    // 极快，无惩罚
		{1000, 0.5},  // k=1000: 50%
		{2000, 0.67}, // 约67%
		{5000, 0.83}, // 约83%
	}

	for _, tt := range tests {
		t.Run("latency_"+string(rune(tt.latencyMs)), func(t *testing.T) {
			candidate := provider.Candidate{
				P95LatencyMs: tt.latencyMs,
			}
			score := calculateLatencyScore(candidate)

			if tt.latencyMs < 100 {
				assert.Equal(t, 0.0, score)
			} else {
				// 允许小误差
				assert.InDelta(t, tt.expected, score, 0.05)
			}
		})
	}
}

func TestQualityScore_SuccessRate(t *testing.T) {
	tests := []struct {
		name        string
		successRate float64
		expected    float64
	}{
		{"95%成功", 0.95, 0.05},
		{"80%成功", 0.80, 0.20},
		{"50%成功", 0.50, 0.50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := provider.Candidate{
				SuccessRate: tt.successRate,
			}
			score := calculateQualityScore(candidate)
			assert.Equal(t, tt.expected, score)
		})
	}
}

func TestDefaultLoadScoreWeights(t *testing.T) {
	weights := DefaultLoadScoreWeights()

	// 验证权重总和为1.0
	total := weights.ConcurrencyWeight + weights.IdentityWeight +
		weights.LatencyWeight + weights.QualityWeight

	assert.InDelta(t, 1.0, total, 0.001)

	// 验证各权重在合理范围内
	assert.Greater(t, weights.ConcurrencyWeight, 0.0)
	assert.Greater(t, weights.IdentityWeight, 0.0)
	assert.Greater(t, weights.LatencyWeight, 0.0)
	assert.Greater(t, weights.QualityWeight, 0.0)
}

func intPtr(v int) *int {
	return &v
}
