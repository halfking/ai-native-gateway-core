package security

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

func TestSecurityHook_EnabledRequiresEnv(t *testing.T) {
	h := NewSecurityHook(nil, Scope{})
	if h.Enabled(context.Background(), nil) {
		t.Fatal("nil envelope should not enable")
	}
	if !h.Enabled(context.Background(), &domain.PipelineRequest{}) {
		t.Fatal("non-nil envelope should enable")
	}
}

func TestSecurityHook_NilRegistryBecomesEmpty(t *testing.T) {
	h := NewSecurityHook(nil, Scope{})
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

	h := NewSecurityHook(reg, Scope{})
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

	h := NewSecurityHook(reg, Scope{})
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
	h := NewSecurityHook(nil, Scope{})
	if err := h.OnError(context.Background(), nil, context.Canceled); err != nil {
		t.Fatalf("OnError should swallow, got %v", err)
	}
}
