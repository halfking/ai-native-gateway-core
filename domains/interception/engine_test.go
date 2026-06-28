package interception

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
)

func TestEngine_NilEnvelopeErrors(t *testing.T) {
	e := NewEngine(EngineConfig{})
	if _, err := e.Decide(context.Background(), nil); err == nil {
		t.Fatal("nil envelope should error")
	}
}

func TestEngine_NoVerdictsContinues(t *testing.T) {
	e := NewEngine(EngineConfig{})
	env := &domain.PipelineRequest{TenantID: "t1"}
	d, err := e.Decide(context.Background(), env)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Kind != governance.DecisionContinue {
		t.Fatalf("Kind = %s, want continue", d.Kind)
	}
	if env.Governance == nil || env.Governance.Decision != d {
		t.Fatal("decision should be recorded to env.Governance")
	}
}

func TestEngine_AllowingVerdictsContinue(t *testing.T) {
	e := NewEngine(EngineConfig{})
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "p1", Allow: true, Severity: 0})
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "p2", Allow: true, Severity: 1})

	d, _ := e.Decide(context.Background(), env)
	if d.Kind != governance.DecisionContinue {
		t.Fatalf("Kind = %s, want continue", d.Kind)
	}
}

func TestEngine_LowSeverityDenyContinues(t *testing.T) {
	e := NewEngine(EngineConfig{BlockThreshold: 2})
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "warn", Allow: false, Severity: 1, Reason: "soft warning"})

	d, _ := e.Decide(context.Background(), env)
	if d.Kind != governance.DecisionContinue {
		t.Fatalf("Kind = %s, want continue (severity below threshold)", d.Kind)
	}
}

func TestEngine_MediumSeverityBlocks(t *testing.T) {
	e := NewEngine(EngineConfig{BlockThreshold: 2})
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "p", Allow: false, Severity: 2, Reason: "policy violation"})

	d, _ := e.Decide(context.Background(), env)
	if d.Kind != governance.DecisionBlock {
		t.Fatalf("Kind = %s, want block", d.Kind)
	}
	if env.Governance.Decision != d {
		t.Fatal("decision should be recorded")
	}
}

func TestEngine_CriticalDefaultsToBlock(t *testing.T) {
	e := NewEngine(EngineConfig{BlockThreshold: 2})
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "p", Allow: false, Severity: 3, Reason: "jailbreak"})

	d, _ := e.Decide(context.Background(), env)
	if d.Kind != governance.DecisionBlock {
		t.Fatalf("Kind = %s, want block (default)", d.Kind)
	}
}

func TestEngine_CriticalWithSuspendFlagSuspends(t *testing.T) {
	e := NewEngine(EngineConfig{BlockThreshold: 2, SuspendOnCritical: true})
	env := &domain.PipelineRequest{
		TenantID:  "t1",
		SessionID: "sess-1",
		Envelope:  &domain.RequestEnvelope{RequestID: "req-1"},
	}
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "p", Allow: false, Severity: 3, Reason: "critical threat"})

	d, _ := e.Decide(context.Background(), env)
	if d.Kind != governance.DecisionSuspend {
		t.Fatalf("Kind = %s, want suspend", d.Kind)
	}
	if d.Suspension == nil || d.Suspension.WaitFor != governance.WaitForApproval {
		t.Fatalf("Suspension = %+v", d.Suspension)
	}
	if d.Suspension.Approval == nil {
		t.Fatal("Approval should be set")
	}
	if d.Suspension.Approval.SessionID != "sess-1" || d.Suspension.Approval.RequestID != "req-1" {
		t.Fatalf("approval context lost: %+v", d.Suspension.Approval)
	}
	if d.Suspension.Approval.RiskLevel != "high" {
		t.Fatalf("RiskLevel = %q, want high (default)", d.Suspension.Approval.RiskLevel)
	}
}

func TestEngine_HighestSeverityWins(t *testing.T) {
	e := NewEngine(EngineConfig{BlockThreshold: 2})
	env := &domain.PipelineRequest{TenantID: "t1"}
	// 1 deny low, 1 allow, 1 deny high → highest is high → block
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "low", Allow: false, Severity: 1})
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "ok", Allow: true, Severity: 0})
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "high", Allow: false, Severity: 3, Reason: "bad"})

	d, _ := e.Decide(context.Background(), env)
	if d.Kind != governance.DecisionBlock {
		t.Fatalf("Kind = %s, want block (highest severity wins)", d.Kind)
	}
}

func TestEngine_SeverityBoost(t *testing.T) {
	e := NewEngine(EngineConfig{BlockThreshold: 2, BlockSeverityBoost: 2})
	env := &domain.PipelineRequest{TenantID: "t1"}
	// verdict.Severity=1 + boost=2 → effective=2 → block
	env.EnsureGovernance().RecordVerdict(&governance.Verdict{PluginName: "low", Allow: false, Severity: 1, Reason: "soft"})

	d, _ := e.Decide(context.Background(), env)
	if d.Kind != governance.DecisionBlock {
		t.Fatalf("Kind = %s, want block (severity boosted)", d.Kind)
	}
}

func TestEngine_DefaultConfigApplied(t *testing.T) {
	e := NewEngine(EngineConfig{})
	if e.cfg.BlockThreshold != 2 {
		t.Errorf("default BlockThreshold = %d, want 2", e.cfg.BlockThreshold)
	}
	if e.cfg.CriticalRiskLevel != "high" {
		t.Errorf("default CriticalRiskLevel = %q, want high", e.cfg.CriticalRiskLevel)
	}
}

func TestEngine_DecisionRecordedWithTrace(t *testing.T) {
	e := NewEngine(EngineConfig{})
	env := &domain.PipelineRequest{TenantID: "t1"}
	if _, err := e.Decide(context.Background(), env); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := len(env.Governance.DecisionTrace); got != 1 {
		t.Fatalf("DecisionTrace len = %d, want 1", got)
	}
	if env.Governance.DecisionTrace[0].Stage != "governance" {
		t.Fatalf("trace stage = %q, want governance", env.Governance.DecisionTrace[0].Stage)
	}
}
