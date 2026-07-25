package executors

import (
	"context"
	"math"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestCalculatePressurePenalty 测试压力惩罚计算
func TestCalculatePressurePenalty(t *testing.T) {
	tests := []struct {
		name             string
		fpPressure       float64
		limiterPressure  float64
		expectedPenalty  float64
		tolerance        float64
	}{
		{"no pressure", 0, 0, 0, 0.01},
		{"low pressure fp", 0.3, 0, 0, 0.01},
		{"low pressure limiter", 0, 0.3, 0, 0.01},
		{"both low pressure", 0.3, 0.2, 0, 0.01},
		{"threshold 0.5", 0.5, 0, 0, 0.01},
		{"just above threshold", 0.51, 0, 0.01, 0.02},
		{"medium pressure 0.65", 0.65, 0.6, 0.15, 0.02},
		{"medium pressure 0.7", 0.7, 0.6, 0.20, 0.02},
		{"high threshold 0.8", 0.8, 0, 0.30, 0.02},
		{"high pressure 0.85", 0.85, 0.8, 0.40, 0.02},
		{"high pressure 0.9", 0.9, 0.85, 0.50, 0.02},
		{"high pressure 0.95", 0.95, 0.9, 0.60, 0.02},
		{"max pressure 1.0", 1.0, 0.95, 0.70, 0.02},
		{"use max fp", 0.9, 0.3, 0.50, 0.02},
		{"use max limiter", 0.3, 0.9, 0.50, 0.02},
		{"both high", 0.95, 0.9, 0.60, 0.02},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			penalty := calculatePressurePenalty(tt.fpPressure, tt.limiterPressure)
			if math.Abs(penalty-tt.expectedPenalty) > tt.tolerance {
				t.Errorf("expected penalty ~%f, got %f (fp=%f, limiter=%f)",
					tt.expectedPenalty, penalty, tt.fpPressure, tt.limiterPressure)
			}
		})
	}
}

// TestCalculatePressurePenalty_Bounds 测试边界条件
func TestCalculatePressurePenalty_Bounds(t *testing.T) {
	tests := []struct {
		name     string
		pressure float64
		minValue float64
		maxValue float64
	}{
		{"zero pressure", 0, 0, 0},
		{"low pressure range", 0.3, 0, 0},
		{"threshold", 0.5, 0, 0},
		{"medium range start", 0.5001, 0, 0.3},
		{"medium range end", 0.7999, 0, 0.3},
		{"high threshold", 0.8, 0.3, 0.3},
		{"high range start", 0.8001, 0.3, 0.7},
		{"high range end", 0.9999, 0.3, 0.7},
		{"max pressure", 1.0, 0.7, 0.7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			penalty := calculatePressurePenalty(tt.pressure, 0)
			if penalty < tt.minValue || penalty > tt.maxValue {
				t.Errorf("penalty %f out of range [%f, %f] for pressure %f",
					penalty, tt.minValue, tt.maxValue, tt.pressure)
			}
		})
	}
}

// TestCalculatePressurePenalty_Monotonic 测试单调性（压力越高，惩罚越重）
func TestCalculatePressurePenalty_Monotonic(t *testing.T) {
	pressures := []float64{0, 0.3, 0.5, 0.6, 0.7, 0.8, 0.85, 0.9, 0.95, 1.0}
	
	var prevPenalty float64
	for i, pressure := range pressures {
		penalty := calculatePressurePenalty(pressure, 0)
		
		if i > 0 && penalty < prevPenalty {
			t.Errorf("penalty should be monotonic increasing, but penalty(%f)=%f < penalty(%f)=%f",
				pressure, penalty, pressures[i-1], prevPenalty)
		}
		
		prevPenalty = penalty
	}
}

// TestCalculatePressurePenalty_MaxPenalty 测试惩罚上限
func TestCalculatePressurePenalty_MaxPenalty(t *testing.T) {
	// 即使压力超过 1.0，惩罚也不应该超过 0.7
	pressures := []float64{1.0, 1.1, 1.5, 2.0}
	
	for _, pressure := range pressures {
		penalty := calculatePressurePenalty(pressure, 0)
		if penalty > 0.7 {
			t.Errorf("penalty should not exceed 0.7, got %f for pressure %f",
				penalty, pressure)
		}
	}
}

// TestCalculatePressurePenalty_Symmetry 测试对称性（fp 和 limiter 对称）
func TestCalculatePressurePenalty_Symmetry(t *testing.T) {
	pressures := []float64{0, 0.3, 0.5, 0.7, 0.9, 1.0}
	
	for _, p := range pressures {
		// fp 压力 = p, limiter 压力 = 0
		penalty1 := calculatePressurePenalty(p, 0)
		
		// fp 压力 = 0, limiter 压力 = p
		penalty2 := calculatePressurePenalty(0, p)
		
		if math.Abs(penalty1-penalty2) > 0.001 {
			t.Errorf("penalty should be symmetric, but fp(%f)=%f != limiter(%f)=%f",
				p, penalty1, p, penalty2)
		}
	}
}

// TestCalculatePressurePenalty_PiecewiseContinuity 测试分段连续性
func TestCalculatePressurePenalty_PiecewiseContinuity(t *testing.T) {
	// 在分段点附近，惩罚应该连续
	thresholds := []float64{0.5, 0.8}
	epsilon := 0.001
	
	for _, threshold := range thresholds {
		penaltyBefore := calculatePressurePenalty(threshold-epsilon, 0)
		penaltyAt := calculatePressurePenalty(threshold, 0)
		penaltyAfter := calculatePressurePenalty(threshold+epsilon, 0)
		
		// 检查连续性（差值应该很小）
		if math.Abs(penaltyAt-penaltyBefore) > 0.01 {
			t.Errorf("discontinuity at threshold %f: before=%f, at=%f",
				threshold, penaltyBefore, penaltyAt)
		}
		if math.Abs(penaltyAfter-penaltyAt) > 0.01 {
			t.Errorf("discontinuity at threshold %f: at=%f, after=%f",
				threshold, penaltyAt, penaltyAfter)
		}
	}
}

// TestCalculatePressurePenalty_NegativeInput 测试负数输入
func TestCalculatePressurePenalty_NegativeInput(t *testing.T) {
	// 负数压力应该被当作 0 处理（math.Max 会选择 0）
	penalty := calculatePressurePenalty(-0.5, 0.3)
	expected := calculatePressurePenalty(0, 0.3)
	
	if math.Abs(penalty-expected) > 0.001 {
		t.Errorf("negative pressure should be treated as 0, got penalty=%f, expected=%f",
			penalty, expected)
	}
}

// Benchmark_CalculatePressurePenalty 性能基准测试
func Benchmark_CalculatePressurePenalty(b *testing.B) {
	for i := 0; i < b.N; i++ {
		calculatePressurePenalty(0.75, 0.65)
	}
}

// TestApplyPressurePenalty_WeightZero 测试 Weight=0 的边界条件
// 原始权重为 0 的候选不应被压力调整（保持不变）
func TestApplyPressurePenalty_WeightZero(t *testing.T) {
	r := &Router{
		PressureAwareEnabled: true,
	}

	// 只有 Weight=0 的候选，且没有 FpSlots/Limiter → 无压力 → 不应被调整
	// 但排序后它仍然是 weight=0
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "gpt-4", Weight: 0},
	}

	ctx := context.Background()
	r.applyPressurePenalty(ctx, candidates, "db_only")

	// Weight=0 的候选保持 0（不参与压力调整）
	if candidates[0].Weight != 0 {
		t.Errorf("expected weight 0 to remain 0, got %d", candidates[0].Weight)
	}
}

// TestApplyPressurePenalty_EmptyCandidates 测试空候选列表
func TestApplyPressurePenalty_EmptyCandidates(t *testing.T) {
	r := &Router{
		PressureAwareEnabled: true,
	}

	// 不应该 panic
	r.applyPressurePenalty(context.Background(), nil, "db_only")
	r.applyPressurePenalty(context.Background(), []provider.Candidate{}, "db_only")
}

// TestSortCandidatesByWeight 测试权重排序
func TestSortCandidatesByWeight(t *testing.T) {
	candidates := []provider.Candidate{
		{CredentialID: 1, Weight: 10},
		{CredentialID: 2, Weight: 100},
		{CredentialID: 3, Weight: 50},
	}

	sortCandidatesByWeight(candidates)

	// 预期顺序: 100, 50, 10
	if candidates[0].CredentialID != 2 || candidates[0].Weight != 100 {
		t.Errorf("expected first candidate to be cred=2 weight=100, got cred=%d weight=%d",
			candidates[0].CredentialID, candidates[0].Weight)
	}
	if candidates[1].CredentialID != 3 || candidates[1].Weight != 50 {
		t.Errorf("expected second candidate to be cred=3 weight=50, got cred=%d weight=%d",
			candidates[1].CredentialID, candidates[1].Weight)
	}
	if candidates[2].CredentialID != 1 || candidates[2].Weight != 10 {
		t.Errorf("expected third candidate to be cred=1 weight=10, got cred=%d weight=%d",
			candidates[2].CredentialID, candidates[2].Weight)
	}
}
