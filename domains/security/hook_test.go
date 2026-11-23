package security

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance" // Verdict 类型
)

func TestSecurityHook_EnabledRequiresEnv(t *testing.T) {
	h := NewSecurityHook(nil, Scope{}, nil)
	if h.Enabled(context.Background(), nil) {
		t.Fatal("nil envelope should not enable")
	}
	if !h.Enabled(context.Background(), &domain.PipelineRequest{}) {
		t.Fatal("non-nil envelope should enable")
	}
}

func TestSecurityHook_NilRegistryBecomesEmpty(t *testing.T) {
	h := NewSecurityHook(nil, Scope{}, nil)
	if h.Registry() == nil {
		t.Fatal("nil registry should auto-create empty")
	}
	if got := h.Registry().Count(); got != 0 {
		t.Fatalf("Count = %d, want 0", got)
	}
}

func TestSecurityHook_ExecuteWritesVerdictsToGovernance(t *testing.T) {
	reg := NewRegistry()
	mustReg(t, reg, &stubPlugin{name: "p1", allow: true})
	mustReg(t, reg, &stubPlugin{name: "p2", allow: false})

	h := NewSecurityHook(reg, Scope{}, nil)
	env := &domain.PipelineRequest{TenantID: "t1"}

	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if env.Governance == nil {
		t.Fatal("Governance should be initialized by EnsureGovernance")
	}
	if got := len(env.Governance.Verdicts); got != 2 {
		t.Fatalf("Verdicts len = %d, want 2", got)
	}
	if !env.Governance.HasBlock() {
		t.Fatal("HasBlock = false, want true (p2 denies)")
	}
}

func TestSecurityHook_Execute_IdempotentGovernance(t *testing.T) {
	reg := NewRegistry()
	mustReg(t, reg, &stubPlugin{name: "p", allow: true})

	h := NewSecurityHook(reg, Scope{}, nil)
	env := &domain.PipelineRequest{TenantID: "t1"}

	// 第一次执行
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	first := env.Governance

	// 第二次执行应复用同一个 GovernanceState 实例（不丢失已有 verdicts）
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if env.Governance != first {
		t.Fatal("Governance pointer should be stable across Execute calls")
	}
	if got := len(env.Governance.Verdicts); got != 2 {
		t.Fatalf("Verdicts len = %d, want 2 (accumulated)", got)
	}
}

func TestSecurityHook_OnErrorSwallows(t *testing.T) {
	h := NewSecurityHook(nil, Scope{}, nil)
	if err := h.OnError(context.Background(), nil, context.Canceled); err != nil {
		t.Fatalf("OnError should swallow, got %v", err)
	}
}

func TestSecurityHook_ApplyObserveMode_EnforceMode(t *testing.T) {
	// enforce 模式：所有拦截保持不变
	h := NewSecurityHook(nil, Scope{}, nil)
	h.mode.Store("enforce")

	verdicts := []*governance.Verdict{
		{PluginName: "p1", Allow: false, Severity: 15, Reason: "high threat"},
		{PluginName: "p2", Allow: false, Severity: 5, Reason: "low threat"},
		{PluginName: "p3", Allow: true, Severity: 0, Reason: "safe"},
	}

	result := h.applyObserveMode(verdicts)

	// enforce 模式：所有判决保持原样
	if result[0].Allow {
		t.Error("high severity verdict should stay blocked in enforce mode")
	}
	if result[1].Allow {
		t.Error("low severity verdict should stay blocked in enforce mode")
	}
	if !result[2].Allow {
		t.Error("allowed verdict should stay allowed")
	}
	// 原因不应被修改
	if result[0].Reason != "high threat" {
		t.Errorf("reason should not be modified in enforce mode, got %q", result[0].Reason)
	}
}

func TestSecurityHook_ApplyObserveMode_ObserveMode_TieredProtection(t *testing.T) {
	// observe 模式：分级防护
	h := NewSecurityHook(nil, Scope{}, nil)
	h.mode.Store("observe")

	tests := []struct {
		name             string
		severity         int
		originalAllow    bool
		originalReason   string
		expectAllow      bool
		expectReasonPfx  string
	}{
		{
			name:            "severity_15_jailbreak_still_blocked",
			severity:        15,
			originalAllow:   false,
			originalReason:  "jailbreak detected",
			expectAllow:     false,
			expectReasonPfx: "[observe:blocked]",
		},
		{
			name:            "severity_10_exactly_still_blocked",
			severity:        10,
			originalAllow:   false,
			originalReason:  "threshold threat",
			expectAllow:     false,
			expectReasonPfx: "[observe:blocked]",
		},
		{
			name:            "severity_9_becomes_allowed",
			severity:        9,
			originalAllow:   false,
			originalReason:  "low risk injection",
			expectAllow:     true,
			expectReasonPfx: "[observe:allowed]",
		},
		{
			name:            "severity_5_becomes_allowed",
			severity:        5,
			originalAllow:   false,
			originalReason:  "minor concern",
			expectAllow:     true,
			expectReasonPfx: "[observe:allowed]",
		},
		{
			name:            "severity_0_becomes_allowed",
			severity:        0,
			originalAllow:   false,
			originalReason:  "informational",
			expectAllow:     true,
			expectReasonPfx: "[observe:allowed]",
		},
		{
			name:            "already_allowed_stays_allowed",
			severity:        20,
			originalAllow:   true,
			originalReason:  "safe request",
			expectAllow:     true,
			expectReasonPfx: "", // 不修改原因
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdicts := []*governance.Verdict{
				{
					PluginName: "test",
					Allow:      tt.originalAllow,
					Severity:   tt.severity,
					Reason:     tt.originalReason,
				},
			}

			result := h.applyObserveMode(verdicts)

			if result[0].Allow != tt.expectAllow {
				t.Errorf("Allow = %v, want %v", result[0].Allow, tt.expectAllow)
			}

			if tt.expectReasonPfx != "" {
				if !contains(result[0].Reason, tt.expectReasonPfx) {
					t.Errorf("Reason = %q, want to contain %q", result[0].Reason, tt.expectReasonPfx)
				}
				if !contains(result[0].Reason, tt.originalReason) {
					t.Errorf("Reason should still contain original reason %q, got %q", tt.originalReason, result[0].Reason)
				}
			} else {
				if result[0].Reason != tt.originalReason {
					t.Errorf("Reason = %q, want %q (unchanged)", result[0].Reason, tt.originalReason)
				}
			}
		})
	}
}

func TestSecurityHook_ApplyObserveMode_UninitializedMode(t *testing.T) {
	// 未初始化的 mode（Load 返回 nil）：保持原判决
	// 需要绕过 NewSecurityHook 的自动初始化
	h := &SecurityHook{
		registry: NewRegistry(),
		scope:    Scope{},
	}
	// 不调用 loadConfig，所以 mode.Load() 返回 nil

	verdicts := []*governance.Verdict{
		{PluginName: "p1", Allow: false, Severity: 15},
	}

	result := h.applyObserveMode(verdicts)

	if result[0].Allow {
		t.Error("verdict should stay blocked when mode is uninitialized")
	}
}

func TestSecurityHook_ApplyObserveMode_UnknownMode(t *testing.T) {
	// 未知的 mode 字符串：保持原判决
	h := NewSecurityHook(nil, Scope{}, nil)
	h.mode.Store("unknown_mode")

	verdicts := []*governance.Verdict{
		{PluginName: "p1", Allow: false, Severity: 15},
	}

	result := h.applyObserveMode(verdicts)

	if result[0].Allow {
		t.Error("verdict should stay blocked when mode is unknown")
	}
}

func TestSecurityHook_ApplyObserveMode_MultipleVerdicts(t *testing.T) {
	// 多个判决混合场景
	h := NewSecurityHook(nil, Scope{}, nil)
	h.mode.Store("observe")

	verdicts := []*governance.Verdict{
		{PluginName: "jailbreak", Allow: false, Severity: 15, Reason: "jailbreak attempt"},
		{PluginName: "minor_injection", Allow: false, Severity: 5, Reason: "suspicious pattern"},
		{PluginName: "safe_check", Allow: true, Severity: 0, Reason: "passed"},
	}

	result := h.applyObserveMode(verdicts)

	// 第一个：高危，保持拦截
	if result[0].Allow {
		t.Error("high severity verdict should stay blocked")
	}
	if !contains(result[0].Reason, "[observe:blocked]") {
		t.Errorf("high severity reason should have [observe:blocked] prefix, got %q", result[0].Reason)
	}

	// 第二个：低危，转为观察
	if !result[1].Allow {
		t.Error("low severity verdict should be allowed in observe mode")
	}
	if !contains(result[1].Reason, "[observe:allowed]") {
		t.Errorf("low severity reason should have [observe:allowed] prefix, got %q", result[1].Reason)
	}

	// 第三个：本来就通过，不变
	if !result[2].Allow {
		t.Error("originally allowed verdict should stay allowed")
	}
	if contains(result[2].Reason, "[observe:") {
		t.Errorf("originally allowed verdict reason should not be modified, got %q", result[2].Reason)
	}
}

// contains 是字符串包含辅助函数
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
