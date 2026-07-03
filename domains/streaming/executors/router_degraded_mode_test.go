package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestTryDegradedMode_TransientReasons 测试降级模式对瞬态原因的处理
// 2026-07-04: 单候选者降级逻辑测试
func TestTryDegradedMode_TransientReasons(t *testing.T) {
	r := &Router{}

	tests := []struct {
		name      string
		candidate provider.Candidate
		wantCount int // 期望返回的候选者数量
	}{
		{
			name: "cooling候选者应该被降级使用",
			candidate: provider.Candidate{
				CredentialID:      1,
				ProviderID:        10,
				RawModel:          "minimax-m3",
				Routable:          true, // 必须为true，否则会返回routing_blocked
				AvailabilityState: "cooling",
			},
			wantCount: 1,
		},
		{
			name: "rate_limited候选者应该被降级使用",
			candidate: provider.Candidate{
				CredentialID:      2,
				ProviderID:        10,
				RawModel:          "minimax-m3",
				Routable:          true,
				AvailabilityState: "rate_limited",
			},
			wantCount: 1,
		},
		{
			name: "suspended候选者应该被降级使用",
			candidate: provider.Candidate{
				CredentialID:      3,
				ProviderID:        10,
				RawModel:          "minimax-m3",
				Routable:          true,
				AvailabilityState: "suspended",
			},
			wantCount: 1,
		},
		{
			name: "auth_failed候选者不应该被降级使用",
			candidate: provider.Candidate{
				CredentialID:      4,
				ProviderID:        10,
				RawModel:          "minimax-m3",
				Routable:          true,
				AvailabilityState: "auth_failed",
			},
			wantCount: 0,
		},
		{
			name: "balance为0的候选者不应该被降级使用",
			candidate: provider.Candidate{
				CredentialID: 5,
				ProviderID:   10,
				RawModel:     "minimax-m3",
				Routable:     true,
				BalanceUSD:   func() *float64 { v := 0.0; return &v }(),
			},
			wantCount: 0,
		},
		{
			name: "lifecycle disabled候选者不应该被降级使用",
			candidate: provider.Candidate{
				CredentialID:    6,
				ProviderID:      10,
				RawModel:        "minimax-m3",
				Routable:        true,
				LifecycleStatus: "disabled",
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := []provider.Candidate{tt.candidate}
			result := r.tryDegradedMode(candidates)

			if len(result) != tt.wantCount {
				t.Errorf("tryDegradedMode() returned %d candidates, want %d. Candidate: %+v",
					len(result), tt.wantCount, tt.candidate)
			}
		})
	}
}

// TestPlanCandidates_DegradedMode 测试单候选者降级模式的完整流程
// 2026-07-04: 单候选者降级逻辑集成测试
func TestPlanCandidates_DegradedMode(t *testing.T) {
	r := &Router{}

	t.Run("单个cooling候选者应该触发降级模式", func(t *testing.T) {
		candidates := []provider.Candidate{
			{
				CredentialID:      1,
				ProviderID:        10,
				RawModel:          "minimax-m3",
				Routable:          true,
				AvailabilityState: "cooling",
			},
		}

		result := r.PlanCandidates(candidates, nil, nil, nil)

		if len(result) != 1 {
			t.Errorf("PlanCandidates() with single cooling candidate returned %d candidates, want 1",
				len(result))
		}
		if len(result) > 0 && result[0].CredentialID != 1 {
			t.Errorf("PlanCandidates() returned wrong candidate, got credential_id=%d, want 1",
				result[0].CredentialID)
		}
	})

	t.Run("单个auth_failed候选者不应该触发降级模式", func(t *testing.T) {
		candidates := []provider.Candidate{
			{
				CredentialID:      2,
				ProviderID:        10,
				RawModel:          "minimax-m3",
				Routable:          true,
				AvailabilityState: "auth_failed",
			},
		}

		result := r.PlanCandidates(candidates, nil, nil, nil)

		if len(result) != 0 {
			t.Errorf("PlanCandidates() with single auth_failed candidate returned %d candidates, want 0",
				len(result))
		}
	})

	t.Run("两个瞬态不可用候选者应该都被降级使用", func(t *testing.T) {
		candidates := []provider.Candidate{
			{
				CredentialID:      1,
				ProviderID:        10,
				RawModel:          "minimax-m3",
				Routable:          true,
				AvailabilityState: "cooling",
			},
			{
				CredentialID:      2,
				ProviderID:        10,
				RawModel:          "minimax-m3",
				Routable:          true,
				AvailabilityState: "rate_limited",
			},
		}

		result := r.PlanCandidates(candidates, nil, nil, nil)

		if len(result) != 2 {
			t.Errorf("PlanCandidates() with two transient unavailable candidates returned %d candidates, want 2",
				len(result))
		}
	})

	t.Run("3个候选者不应该触发降级模式", func(t *testing.T) {
		candidates := []provider.Candidate{
			{
				CredentialID:      1,
				ProviderID:        10,
				RawModel:          "minimax-m3",
				Routable:          true,
				AvailabilityState: "cooling",
			},
			{
				CredentialID:      2,
				ProviderID:        10,
				RawModel:          "minimax-m3",
				Routable:          true,
				AvailabilityState: "rate_limited",
			},
			{
				CredentialID:      3,
				ProviderID:        10,
				RawModel:          "minimax-m3",
				Routable:          true,
				AvailabilityState: "auth_failed",
			},
		}

		result := r.PlanCandidates(candidates, nil, nil, nil)

		if len(result) != 0 {
			t.Errorf("PlanCandidates() with 3 candidates should not trigger degraded mode, got %d candidates, want 0",
				len(result))
		}
	})
}

// TestIsTransientUnavailableReason 测试瞬态原因判断函数
// 2026-07-04: 单候选者降级逻辑测试
func TestIsTransientUnavailableReason(t *testing.T) {
	tests := []struct {
		reason string
		want   bool
	}{
		// 瞬态原因
		{"availability:cooling", true},
		{"availability:rate_limited", true},
		{"availability:suspended", true},

		// 永久原因
		{"availability:auth_failed", false},
		{"availability:unreachable", false},
		{"quota:balance_exhausted", false},
		{"quota:permanently_exhausted", false},
		{"quota:periodic_exhausted", false},
		{"lifecycle:disabled", false},
		{"balance:zero", false},
		{"routing_blocked", false},
		{"unknown", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			got := isTransientUnavailableReason(tt.reason)
			if got != tt.want {
				t.Errorf("isTransientUnavailableReason(%q) = %v, want %v",
					tt.reason, got, tt.want)
			}
		})
	}
}
