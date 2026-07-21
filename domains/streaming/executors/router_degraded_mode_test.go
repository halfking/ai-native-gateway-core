package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/errorsx"
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
			result := r.tryDegradedMode(context.Background(), candidates)

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

		result := r.PlanCandidates(candidates, PlanContext{}, nil, nil, nil)

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

		result := r.PlanCandidates(candidates, PlanContext{}, nil, nil, nil)

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

		result := r.PlanCandidates(candidates, PlanContext{}, nil, nil, nil)

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

		result := r.PlanCandidates(candidates, PlanContext{}, nil, nil, nil)

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

		// StateManager 内存态原因（瞬态，应降级）
		// 对应 credentialstate/manager.go isTransient 的四个 errKind
		// 2026-07-18: 新增 state:empty_response，参考 errorsx.KindEmptyResponse
		// 设计意图（classify.go line 60-78："must not hard-exclude the
		// credential"）以及 NIM 13% empty-stream 偶发（executor_chat.go:826）。
		{"state:" + string(errorsx.KindTimeout), true},
		{"state:" + string(errorsx.KindStreamTimeout), true},
		{"state:" + string(errorsx.KindRateLimit), true},
		{"state:" + string(errorsx.KindUpstreamDown), true},
		{"state:" + string(errorsx.KindEmptyResponse), true},
		{"state:probe_direct_timeout", true},

		// StateManager 内存态原因（永久，不应降级）
		{"state:" + string(errorsx.KindAuth), false},
		{"state:" + string(errorsx.KindAuthRevoked), false},
		{"state:" + string(errorsx.KindModelNotFound), false},
		{"state:" + string(errorsx.KindQuotaPermanent), false},

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

// stateProviderStub 实现 credentialstate.StateProvider，用于在路由测试中
// 模拟内存态 (credID, model) → (available, reason)。reason 取 errorsx
// ErrorKind 字符串值，与 credentialstate/manager.go:218 LastError 赋值一致。
// 风格仿照 router_route_node_test.go 的 routerFpSlotsStub（map 驱动的小结构体）。
type stateProviderStub struct {
	// unavailable[credID][model] = reason（非空即不可用）
	unavailable map[int]map[string]string
	enabled     bool
}

func newstateProviderStub() *stateProviderStub {
	return &stateProviderStub{
		unavailable: make(map[int]map[string]string),
		enabled:     true,
	}
}

// set 标记 (credID, model) 不可用，返回给定 errKind 作为 reason。
func (s *stateProviderStub) set(credID int, model string, kind errorsx.ErrorKind) *stateProviderStub {
	if s.unavailable[credID] == nil {
		s.unavailable[credID] = make(map[string]string)
	}
	s.unavailable[credID][model] = string(kind)
	return s
}

func (s *stateProviderStub) GetState(_ context.Context, credID int, model string) (*credentialstate.State, error) {
	reason, ok := s.unavailable[credID][model]
	if !ok || reason == "" {
		return &credentialstate.State{CredentialID: credID, Model: model, Available: true}, nil
	}
	return &credentialstate.State{CredentialID: credID, Model: model, Available: false, LastError: reason}, nil
}

func (s *stateProviderStub) IsAvailable(_ context.Context, credID int, model string) (bool, string) {
	reason, ok := s.unavailable[credID][model]
	if !ok || reason == "" {
		return true, ""
	}
	return false, reason
}

func (s *stateProviderStub) Enabled() bool { return s.enabled }

// TestTryDegradedMode_StateManagerTransient 验证当候选 DB 字段全可用
// (UnavailableReason()=="") 但被 StateManager 内存态判为瞬态不可用时，
// 降级模式仍会采纳该候选。
//
// 回归 2026-07-14 生产事故 ba9fc64f：gpt-5.6-luna 单候选 cred=2 被
// state:timeout 过滤后 tryDegradedMode 返回 0 → 503 no_candidate。
func TestTryDegradedMode_StateManagerTransient(t *testing.T) {
	// 候选 DB 层完全可用——这是关键，确保唯一拒绝来源是 StateManager。
	availableCandidate := func(credID int) provider.Candidate {
		return provider.Candidate{
			CredentialID: credID,
			ProviderID:   314,
			RawModel:     "gpt-5.6-luna",
			Routable:     true,
			// AvailabilityState/QuotaState/LifecycleStatus 均默认零值 → 可用
		}
	}

	tests := []struct {
		name      string
		smReason  errorsx.ErrorKind // StateManager 返回的 reason（空串=可用）
		wantCount int
	}{
		// 瞬态：应降级
		{"state:timeout 应降级", errorsx.KindTimeout, 1},
		{"state:stream_timeout 应降级", errorsx.KindStreamTimeout, 1},
		{"state:rate_limit 应降级", errorsx.KindRateLimit, 1},
		{"state:upstream_down 应降级", errorsx.KindUpstreamDown, 1},
		// 永久：不应降级
		{"state:auth 不降级", errorsx.KindAuth, 0},
		{"state:auth_revoked 不降级", errorsx.KindAuthRevoked, 0},
		{"state:model_not_found 不降级", errorsx.KindModelNotFound, 0},
		{"state:quota_permanent 不降级", errorsx.KindQuotaPermanent, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := newstateProviderStub().
				set(2, "gpt-5.6-luna", tt.smReason)
			r := &Router{StateManager: sm}

			candidates := []provider.Candidate{availableCandidate(2)}
			result := r.tryDegradedMode(context.Background(), candidates)

			if len(result) != tt.wantCount {
				t.Errorf("tryDegradedMode() with state:%s returned %d candidates, want %d",
					tt.smReason, len(result), tt.wantCount)
			}
		})
	}
}

// TestTryDegradedMode_NoStateManager 向后兼容：StateManager 为 nil 时，
// 只看候选自身 UnavailableReason()，行为与 2026-07-04 版本一致。
func TestTryDegradedMode_NoStateManager(t *testing.T) {
	r := &Router{} // StateManager == nil

	// DB 层 cooling → 应降级（既有路径）
	cooling := provider.Candidate{
		CredentialID: 1, ProviderID: 10, RawModel: "m", Routable: true,
		AvailabilityState: "cooling",
	}
	if got := r.tryDegradedMode(context.Background(), []provider.Candidate{cooling}); len(got) != 1 {
		t.Errorf("no-SM cooling: got %d, want 1", len(got))
	}

	// DB 层全可用且无 SM → 无原因，不降级
	clean := provider.Candidate{
		CredentialID: 2, ProviderID: 10, RawModel: "m", Routable: true,
	}
	if got := r.tryDegradedMode(context.Background(), []provider.Candidate{clean}); len(got) != 0 {
		t.Errorf("no-SM clean: got %d, want 0", len(got))
	}
}

// TestPlanCandidates_DegradedMode_StateManagerTimeout 端到端回归：
// 单候选被 StateManager 判 timeout 时，PlanCandidates 应走降级返回 1，
// 而不是 0 节点 503。这条直接锁住生产 bug ba9fc64f。
func TestPlanCandidates_DegradedMode_StateManagerTimeout(t *testing.T) {
	sm := newstateProviderStub().
		set(2, "gpt-5.6-luna", errorsx.KindTimeout)
	r := &Router{StateManager: sm}

	candidates := []provider.Candidate{
		{
			CredentialID: 2,
			ProviderID:   314,
			RawModel:     "gpt-5.6-luna",
			Routable:     true,
			// DB 层全可用——拒绝只来自 StateManager 内存态
		},
	}

	result := r.PlanCandidates(candidates, PlanContext{}, nil, nil, nil)
	if len(result) != 1 {
		t.Fatalf("PlanCandidates() with single state:timeout candidate returned %d, want 1 (degraded mode). "+
			"This is the ba9fc64f regression: single-point candidate rejected by transient SM state must degrade, not 503.",
			len(result))
	}
	if result[0].CredentialID != 2 {
		t.Errorf("returned credential_id=%d, want 2", result[0].CredentialID)
	}
}

// TestPlanCandidates_DegradedMode_StateManagerPermanent 端到端：
// 单候选被 StateManager 判永久错误（auth）时，降级不触发，返回 0。
func TestPlanCandidates_DegradedMode_StateManagerPermanent(t *testing.T) {
	sm := newstateProviderStub().
		set(2, "gpt-5.6-luna", errorsx.KindAuth)
	r := &Router{StateManager: sm}

	candidates := []provider.Candidate{
		{
			CredentialID: 2,
			ProviderID:   314,
			RawModel:     "gpt-5.6-luna",
			Routable:     true,
		},
	}

	result := r.PlanCandidates(candidates, PlanContext{}, nil, nil, nil)
	if len(result) != 0 {
		t.Errorf("PlanCandidates() with single state:auth candidate returned %d, want 0 (permanent error must not degrade)",
			len(result))
	}
}

// TestFreeCredentialsTolerateTransient 验证免费凭据 transient 容忍判定
// （2026-07-14）。免费凭据对 transient 错误不硬剔（靠软降权），但对永久错误
// 仍硬剔。付费凭据（billingMode="" 或 "per_token"）永远 false（走原硬剔路径）。
func TestFreeCredentialsTolerateTransient(t *testing.T) {
	tests := []struct {
		name        string
		billingMode string
		kind        errorsx.ErrorKind
		want        bool
	}{
		// 免费 + transient → 容忍（不硬剔）
		{"free+timeout", "free", errorsx.KindTimeout, true},
		{"free+stream_timeout", "free", errorsx.KindStreamTimeout, true},
		{"free+network", "free", errorsx.KindNetwork, true},
		{"free+rate_limit", "free", errorsx.KindRateLimit, true},
		{"free+upstream_down", "free", errorsx.KindUpstreamDown, true},
		{"free+transient", "free", errorsx.KindTransient, true},
		// 免费 + 永久 → 不容忍（仍硬剔，坏 key 不拖垮路由）
		{"free+auth", "free", errorsx.KindAuth, false},
		{"free+model_not_found", "free", errorsx.KindModelNotFound, false},
		{"free+quota_permanent", "free", errorsx.KindQuotaPermanent, false},
		// 付费 → 永远 false（走原硬剔路径）
		{"paid+timeout", "", errorsx.KindTimeout, false},
		{"per_token+timeout", "per_token", errorsx.KindTimeout, false},
		{"paid+auth", "per_token", errorsx.KindAuth, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := freeCredentialsTolerateTransient(tt.billingMode, tt.kind)
			if got != tt.want {
				t.Errorf("freeCredentialsTolerateTransient(billingMode=%q, kind=%q) = %v, want %v",
					tt.billingMode, tt.kind, got, tt.want)
			}
		})
	}
}
