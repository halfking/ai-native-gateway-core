package interception

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
)

func TestInterceptionHook_NilEngineBecomesDefault(t *testing.T) {
	h := NewInterceptionHook(nil)
	if h.Engine() == nil {
		t.Fatal("nil engine should auto-create default")
	}
}

func TestInterceptionHook_EnabledRequiresEnv(t *testing.T) {
	h := NewInterceptionHook(nil)
	if h.Enabled(context.Background(), nil) {
		t.Fatal("nil envelope should not enable")
	}
	if !h.Enabled(context.Background(), &domain.PipelineRequest{}) {
		t.Fatal("non-nil envelope should enable")
	}
}

func TestInterceptionHook_ExecuteWritesDecision(t *testing.T) {
	e := NewEngine(EngineConfig{BlockThreshold: 2})
	h := NewInterceptionHook(e)
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "p", Allow: false, Severity: 2, Reason: "block"})

	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := h.LastDecision(env); got == nil || got.Kind != governance.DecisionBlock {
		t.Fatalf("LastDecision = %+v, want Block", got)
	}
}

func TestInterceptionHook_OnErrorSwallows(t *testing.T) {
	h := NewInterceptionHook(nil)
	if err := h.OnError(context.Background(), nil, context.Canceled); err != nil {
		t.Fatalf("OnError should swallow, got %v", err)
	}
}

func TestInterceptionHook_DoesNotReturnBlockAsError(t *testing.T) {
	// PR-V4-04 阶段：hook 不阻断 pipeline，HTTP 拦截由后续 PR 引入。
	e := NewEngine(EngineConfig{BlockThreshold: 2})
	h := NewInterceptionHook(e)
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "p", Allow: false, Severity: 3, Reason: "block"})

	err := h.Execute(context.Background(), env)
	if err != nil {
		t.Fatalf("Execute returned error (would break pipeline): %v", err)
	}
	if env.Governance.Decision.Kind != governance.DecisionBlock {
		t.Fatalf("decision kind = %s, want block", env.Governance.Decision.Kind)
	}
}
