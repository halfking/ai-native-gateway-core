package interception

import (
	"net/http"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

func TestInspectDecision_NilEnv(t *testing.T) {
	if got := InspectDecision(nil); got != DispatchContinue {
		t.Fatalf("nil env = %v, want Continue", got)
	}
}

func TestInspectDecision_NilGovernance(t *testing.T) {
	env := &domain.PipelineRequest{TenantID: "t1"}
	if got := InspectDecision(env); got != DispatchContinue {
		t.Fatalf("nil governance = %v, want Continue", got)
	}
}

func TestInspectDecision_NilDecision(t *testing.T) {
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance()
	if got := InspectDecision(env); got != DispatchContinue {
		t.Fatalf("nil decision = %v, want Continue", got)
	}
}

func TestInspectDecision_ContinueIsTransparent(t *testing.T) {
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordDecision(&governance.Decision{Kind: governance.DecisionContinue})
	if got := InspectDecision(env); got != DispatchContinue {
		t.Fatalf("Continue = %v, want Continue (transparent)", got)
	}
}

func TestInspectDecision_MutateIsTransparent(t *testing.T) {
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordDecision(&governance.Decision{Kind: governance.DecisionMutate})
	if got := InspectDecision(env); got != DispatchContinue {
		t.Fatalf("Mutate = %v, want Continue (transparent — mutate handled in-place)", got)
	}
}

func TestInspectDecision_BlockShortCircuits(t *testing.T) {
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordDecision(&governance.Decision{Kind: governance.DecisionBlock, Reason: "jailbreak"})
	if got := InspectDecision(env); got != DispatchShortCircuit {
		t.Fatalf("Block = %v, want ShortCircuit", got)
	}
}

func TestInspectDecision_SuspendShortCircuits(t *testing.T) {
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordDecision(&governance.Decision{Kind: governance.DecisionSuspend, Reason: "critical"})
	if got := InspectDecision(env); got != DispatchShortCircuit {
		t.Fatalf("Suspend = %v, want ShortCircuit", got)
	}
}

func TestInspectDecision_TerminateShortCircuits(t *testing.T) {
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordDecision(&governance.Decision{Kind: governance.DecisionTerminate, Reason: "abuse"})
	if got := InspectDecision(env); got != DispatchShortCircuit {
		t.Fatalf("Terminate = %v, want ShortCircuit", got)
	}
}

func TestWriteDecisionResponse_NoDecision(t *testing.T) {
	code, body := WriteDecisionResponse(&domain.PipelineRequest{TenantID: "t1"})
	if code != http.StatusOK || body != nil {
		t.Fatalf("no decision: code=%d body=%s, want 200/nil", code, body)
	}
}

func TestWriteDecisionResponse_Block(t *testing.T) {
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordDecision(&governance.Decision{
		Kind:         governance.DecisionBlock,
		Reason:       "jailbreak attempt",
		TraceID:      "trace-123",
		SourcePlugin: "prompt_injection",
	})
	code, body := WriteDecisionResponse(env)
	if code != http.StatusForbidden {
		t.Fatalf("Block status = %d, want 403", code)
	}
	if len(body) == 0 {
		t.Fatal("Block body should not be empty")
	}
	// 验证关键字段
	if !contains(body, "governance_blocked") || !contains(body, "jailbreak attempt") {
		t.Fatalf("Block body missing key fields: %s", body)
	}
	if !contains(body, "trace-123") {
		t.Fatalf("Block body missing trace_id: %s", body)
	}
}

func TestWriteDecisionResponse_Suspend(t *testing.T) {
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordDecision(&governance.Decision{
		Kind:      governance.DecisionSuspend,
		Reason:    "awaiting approval",
		TraceID:   "trace-456",
		CreatedAt: now(),
		Suspension: &governance.SuspensionSpec{
			WaitFor: governance.WaitForApproval,
			Approval: &governance.ApprovalRequest{
				RiskLevel: "high",
			},
		},
	})
	code, body := WriteDecisionResponse(env)
	if code != http.StatusAccepted {
		t.Fatalf("Suspend status = %d, want 202", code)
	}
	if !contains(body, `"status":"pending"`) {
		t.Fatalf("Suspend body missing status: %s", body)
	}
	if !contains(body, "awaiting approval") {
		t.Fatalf("Suspend body missing reason: %s", body)
	}
	if !contains(body, "high") {
		t.Fatalf("Suspend body missing risk_level: %s", body)
	}
}

func TestWriteDecisionResponse_Terminate(t *testing.T) {
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordDecision(&governance.Decision{
		Kind:   governance.DecisionTerminate,
		Reason: "session abuse",
	})
	code, body := WriteDecisionResponse(env)
	if code != http.StatusGone {
		t.Fatalf("Terminate status = %d, want 410", code)
	}
	if !contains(body, "session_terminated") {
		t.Fatalf("Terminate body missing code: %s", body)
	}
}

func TestWriteDecisionResponse_ContinueTransparent(t *testing.T) {
	env := &domain.PipelineRequest{TenantID: "t1"}
	env.EnsureGovernance().RecordDecision(&governance.Decision{Kind: governance.DecisionContinue})
	code, body := WriteDecisionResponse(env)
	if code != http.StatusOK || body != nil {
		t.Fatalf("Continue: code=%d body=%v, want 200/nil", code, body)
	}
}

// ── helpers ────────────────────────────────────────────────────────────

func now() time.Time { return time.Now() }

func contains(haystack []byte, needle string) bool {
	if len(haystack) == 0 {
		return false
	}
	s := string(haystack)
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
