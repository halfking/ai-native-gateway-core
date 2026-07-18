package quality

import (
	"testing"
)

// TestScoreResult_CalculateQualityScore 测试综合质量分计算
func TestScoreResult_CalculateQualityScore(t *testing.T) {
	tests := []struct {
		name      string
		scores    ScoreResult
		wantScore float64
		wantGrade string
	}{
		{
			name: "完美供应商",
			scores: ScoreResult{
				AvailabilityScore:   100,
				PerformanceScore:    100,
				ReliabilityScore:    100,
				StabilityScore:      100,
				CostEfficiencyScore: 100,
			},
			wantScore: 100,
			wantGrade: "S",
		},
		{
			name: "高可用低性能",
			scores: ScoreResult{
				AvailabilityScore:   95,
				PerformanceScore:    60,
				ReliabilityScore:    85,
				StabilityScore:      70,
				CostEfficiencyScore: 80,
			},
			wantScore: 79.75, // 95*0.35 + 60*0.25 + 85*0.20 + 70*0.15 + 80*0.05
			wantGrade: "B",
		},
		{
			name: "不稳定",
			scores: ScoreResult{
				AvailabilityScore:   80,
				PerformanceScore:    80,
				ReliabilityScore:    60,
				StabilityScore:      40,
				CostEfficiencyScore: 80,
			},
			wantScore: 70, // 近似
			wantGrade: "B",
		},
		{
			name: "高错误率",
			scores: ScoreResult{
				AvailabilityScore:   50,
				PerformanceScore:    70,
				ReliabilityScore:    50,
				StabilityScore:      60,
				CostEfficiencyScore: 80,
			},
			wantScore: 58, // 近似
			wantGrade: "D",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.scores.CalculateQualityScore()

			// 允许 ±1 的误差
			if diff := tt.scores.QualityScore - tt.wantScore; diff < -1 || diff > 1 {
				t.Errorf("QualityScore = %.2f, want %.2f (±1)", tt.scores.QualityScore, tt.wantScore)
			}

			if tt.scores.Grade != tt.wantGrade {
				t.Errorf("Grade = %s, want %s", tt.scores.Grade, tt.wantGrade)
			}
		})
	}
}

// TestClamp 测试 clamp 函数
func TestClamp(t *testing.T) {
	tests := []struct {
		value, min, max float64
		want            float64
	}{
		{50, 0, 100, 50},
		{-10, 0, 100, 0},
		{150, 0, 100, 100},
		{99.5, 0, 100, 99.5},
	}

	for _, tt := range tests {
		got := clamp(tt.value, tt.min, tt.max)
		if got != tt.want {
			t.Errorf("clamp(%.1f, %.1f, %.1f) = %.1f, want %.1f",
				tt.value, tt.min, tt.max, got, tt.want)
		}
	}
}

// TestAvailabilityScorer_calculateSuccessScore 测试成功率得分
func TestAvailabilityScorer_calculateSuccessScore(t *testing.T) {
	s := &AvailabilityScorer{}

	tests := []struct {
		successRate float64
		want        float64
	}{
		{100, 100},
		{95, 95},
		{80, 80},
		{0, 0},
	}

	for _, tt := range tests {
		got := s.calculateSuccessScore(tt.successRate)
		if got != tt.want {
			t.Errorf("calculateSuccessScore(%.1f) = %.1f, want %.1f",
				tt.successRate, got, tt.want)
		}
	}
}

// TestAvailabilityScorer_calculate5xxScore 测试 5xx 错误率得分
func TestAvailabilityScorer_calculate5xxScore(t *testing.T) {
	s := &AvailabilityScorer{}

	tests := []struct {
		error5xxRate float64
		want         float64
	}{
		{0, 100},
		{1, 95},  // 100 - 1*5
		{5, 75},  // 100 - 5*5
		{10, 50}, // 100 - 10*5
		{20, 0},  // 封底
		{50, 0},  // 封底
	}

	for _, tt := range tests {
		got := s.calculate5xxScore(tt.error5xxRate)
		if got != tt.want {
			t.Errorf("calculate5xxScore(%.1f) = %.1f, want %.1f",
				tt.error5xxRate, got, tt.want)
		}
	}
}

// TestPerformanceScorer_calculateLatencyScore 测试延迟得分
func TestPerformanceScorer_calculateLatencyScore(t *testing.T) {
	s := &PerformanceScorer{}

	tests := []struct {
		latency float64
		factor  float64
		want    float64
	}{
		{100, 1.0, 100}, // < 500
		{500, 1.0, 100}, // = 500
		{750, 1.0, 50},  // 500-1000 区间
		{1000, 1.0, 0},  // = 1000（边界，第二段末尾）
		{2000, 1.0, 65}, // 1000-3000 区间
		{5000, 1.5, 35}, // P99 阈值放宽，超过 4500 阈值
	}

	for _, tt := range tests {
		got := s.calculateLatencyScore(tt.latency, tt.factor)
		// 允许 ±5 的误差（因为公式复杂）
		if diff := got - tt.want; diff < -5 || diff > 5 {
			t.Errorf("calculateLatencyScore(%.0f, %.1f) = %.1f, want %.1f (±5)",
				tt.latency, tt.factor, got, tt.want)
		}
	}
}

// TestCostEfficiencyScorer_calculateAbsoluteCostScore 测试绝对成本得分
func TestCostEfficiencyScorer_calculateAbsoluteCostScore(t *testing.T) {
	s := &CostEfficiencyScorer{}

	tests := []struct {
		cost float64
		want float64
	}{
		{0.005, 100}, // < 0.01
		{0.01, 100},  // = 0.01
		{0.03, 80},   // 0.01-0.05 区间
		{0.05, 60},   // = 0.05（边界，进入第三段）
		{0.08, 66},   // 0.05-0.1 区间
		{0.15, 25},   // > 0.1
	}

	for _, tt := range tests {
		got := s.calculateAbsoluteCostScore(tt.cost)
		// 允许 ±5 的误差
		if diff := got - tt.want; diff < -5 || diff > 5 {
			t.Errorf("calculateAbsoluteCostScore(%.3f) = %.1f, want %.1f (±5)",
				tt.cost, got, tt.want)
		}
	}
}
