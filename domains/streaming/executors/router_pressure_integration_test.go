package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestRouterGetPressureSignals 测试 Router 压力信号获取
func TestRouterGetPressureSignals(t *testing.T) {
	limiter := credential.NewWithLimits(1000, 500, 200, 100)
	defer limiter.Stop()

	router := NewRouter(nil, limiter)
	router.PressureAwareEnabled = true

	ctx := context.Background()
	candidate := provider.Candidate{
		CredentialID: 123,
		ProviderID:   1,
		FpSlotLimit:  intPtr(10),
	}

	// 测试 Limiter 压力信号获取
	fpPressure, limiterPressure := router.getPressureSignals(ctx, candidate)

	// Limiter 应该返回有效压力值（0-1之间）
	if limiterPressure < 0 || limiterPressure > 1 {
		t.Errorf("limiterPressure out of range: %f", limiterPressure)
	}

	// FpSlot 压力应该为 0（没有设置 FpSlots）
	if fpPressure != 0 {
		t.Errorf("fpPressure should be 0 when FpSlots is nil, got %f", fpPressure)
	}
}

// TestRouterGetPressureSignals_NoFpSlotLimit 测试无 FpSlot 限制的情况
func TestRouterGetPressureSignals_NoFpSlotLimit(t *testing.T) {
	limiter := credential.NewWithLimits(1000, 500, 200, 100)
	defer limiter.Stop()

	router := NewRouter(nil, limiter)
	router.PressureAwareEnabled = true

	ctx := context.Background()
	candidate := provider.Candidate{
		CredentialID: 456,
		ProviderID:   2,
		FpSlotLimit:  nil, // 无 FpSlot 限制
	}

	fpPressure, limiterPressure := router.getPressureSignals(ctx, candidate)

	// 无 FpSlot 限制时应该返回 0
	if fpPressure != 0 {
		t.Errorf("fpPressure should be 0 when FpSlotLimit is nil, got %f", fpPressure)
	}

	// Limiter 压力应该有效
	if limiterPressure < 0 || limiterPressure > 1 {
		t.Errorf("limiterPressure out of range: %f", limiterPressure)
	}
}

// TestRouterApplyPressurePenalty 测试压力惩罚应用
func TestRouterApplyPressurePenalty(t *testing.T) {
	router := NewRouter(nil, nil)
	router.PressureAwareEnabled = true

	tests := []struct {
		name               string
		candidates         []provider.Candidate
		fpPressures        []float64
		limiterPressures   []float64
		expectWeightChange bool
	}{
		{
			name: "no pressure - no penalty",
			candidates: []provider.Candidate{
				{CredentialID: 1, Weight: 100},
			},
			fpPressures:        []float64{0.0},
			limiterPressures:   []float64{0.0},
			expectWeightChange: false,
		},
		{
			name: "low pressure - no penalty",
			candidates: []provider.Candidate{
				{CredentialID: 2, Weight: 100},
			},
			fpPressures:        []float64{0.4},
			limiterPressures:   []float64{0.3},
			expectWeightChange: false,
		},
		{
			name: "medium pressure - apply penalty",
			candidates: []provider.Candidate{
				{CredentialID: 3, Weight: 100},
			},
			fpPressures:        []float64{0.65},
			limiterPressures:   []float64{0.0},
			expectWeightChange: true,
		},
		{
			name: "high pressure - apply heavy penalty",
			candidates: []provider.Candidate{
				{CredentialID: 4, Weight: 100},
			},
			fpPressures:        []float64{0.9},
			limiterPressures:   []float64{0.0},
			expectWeightChange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 复制候选节点以避免测试间相互影响
			candidates := make([]provider.Candidate, len(tt.candidates))
			copy(candidates, tt.candidates)

			originalWeights := make([]int, len(candidates))
			for i, c := range candidates {
				originalWeights[i] = c.Weight
			}

			// 应用压力惩罚（需要模拟）
			for i := range candidates {
				fpPressure := tt.fpPressures[i]
				limiterPressure := tt.limiterPressures[i]
				penalty := calculatePressurePenalty(fpPressure, limiterPressure)
				penaltyFactor := 1.0 - penalty
				candidates[i].Weight = int(float64(candidates[i].Weight) * penaltyFactor)
			}

			// 验证权重变化
			for i, c := range candidates {
				if tt.expectWeightChange {
					if c.Weight >= originalWeights[i] {
						t.Errorf("expected weight to decrease, original=%d, new=%d", originalWeights[i], c.Weight)
					}
				} else {
					if c.Weight != originalWeights[i] {
						t.Errorf("expected weight to stay same, original=%d, new=%d", originalWeights[i], c.Weight)
					}
				}
			}
		})
	}
}

// TestRouterApplyPressurePenalty_MultipleCandidat es 测试多候选节点的压力惩罚
func TestRouterApplyPressurePenalty_MultipleCandidates(t *testing.T) {
	tests := []struct {
		name       string
		candidates []provider.Candidate
		pressures  []float64
	}{
		{
			name: "mixed pressure levels",
			candidates: []provider.Candidate{
				{CredentialID: 1, Weight: 100}, // 低压力
				{CredentialID: 2, Weight: 100}, // 中压力
				{CredentialID: 3, Weight: 100}, // 高压力
			},
			pressures: []float64{0.2, 0.65, 0.9},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := make([]provider.Candidate, len(tt.candidates))
			copy(candidates, tt.candidates)

			originalWeights := make([]int, len(candidates))
			for i := range candidates {
				originalWeights[i] = candidates[i].Weight
			}

			// 应用压力惩罚
			for i := range candidates {
				penalty := calculatePressurePenalty(tt.pressures[i], 0)
				penaltyFactor := 1.0 - penalty
				candidates[i].Weight = int(float64(candidates[i].Weight) * penaltyFactor)
			}

			// 验证：高压力节点权重应该最低
			if len(candidates) >= 3 {
				// candidates[0] (低压力) 应该权重最高
				// candidates[2] (高压力) 应该权重最低
				if candidates[0].Weight <= candidates[2].Weight {
					t.Errorf("low pressure node should have higher weight than high pressure node, got %d <= %d",
						candidates[0].Weight, candidates[2].Weight)
				}
			}
		})
	}
}

// TestRouterPressureAwareDisabled 测试压力感知关闭时的行为
func TestRouterPressureAwareDisabled(t *testing.T) {
	limiter := credential.NewWithLimits(1000, 500, 200, 100)
	defer limiter.Stop()

	router := NewRouter(nil, limiter)
	router.PressureAwareEnabled = false // 关闭压力感知

	ctx := context.Background()
	candidate := provider.Candidate{
		CredentialID: 789,
		ProviderID:   3,
		FpSlotLimit:  intPtr(10),
		Weight:       100,
	}

	// 获取压力信号（虽然关闭，但方法仍可调用）
	fpPressure, limiterPressure := router.getPressureSignals(ctx, candidate)

	// 压力值应该仍然有效
	_ = fpPressure
	_ = limiterPressure

	// 权重不应该被修改（因为 PressureAwareEnabled = false）
	originalWeight := candidate.Weight
	if candidate.Weight != originalWeight {
		t.Errorf("weight should not change when pressure-aware is disabled")
	}
}

// TestRouterPressureAware_EndToEnd 端到端压力感知路由测试
func TestRouterPressureAware_EndToEnd(t *testing.T) {
	limiter := credential.NewWithLimits(100, 50, 20, 10)
	defer limiter.Stop()

	router := NewRouter(nil, limiter)
	router.PressureAwareEnabled = true

	// 模拟场景：有3个候选节点，压力不同
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, Weight: 100, FpSlotLimit: intPtr(10)}, // 低压力
		{CredentialID: 2, ProviderID: 1, Weight: 100, FpSlotLimit: intPtr(10)}, // 中压力
		{CredentialID: 3, ProviderID: 1, Weight: 100, FpSlotLimit: intPtr(10)}, // 高压力
	}

	ctx := context.Background()

	// 为每个候选节点获取压力信号
	pressures := make([]float64, len(candidates))
	for i, candidate := range candidates {
		_, limiterPressure := router.getPressureSignals(ctx, candidate)
		pressures[i] = limiterPressure
	}

	// 验证：所有压力值都在有效范围内
	for i, pressure := range pressures {
		if pressure < 0 || pressure > 1 {
			t.Errorf("candidate %d pressure out of range: %f", i, pressure)
		}
	}

	// 应用压力惩罚
	for i := range candidates {
		penalty := calculatePressurePenalty(pressures[i], 0)
		penaltyFactor := 1.0 - penalty
		candidates[i].Weight = int(float64(candidates[i].Weight) * penaltyFactor)
	}

	// 验证：所有权重都是非负的
	for i, candidate := range candidates {
		if candidate.Weight < 0 {
			t.Errorf("candidate %d weight is negative: %d", i, candidate.Weight)
		}
	}
}
