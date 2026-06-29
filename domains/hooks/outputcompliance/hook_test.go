package outputcompliance

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

type fakeChecker struct {
	result *ComplianceResult
	err    error
}

func (f *fakeChecker) Check(_ context.Context, _, _ string) (*ComplianceResult, error) {
	return f.result, f.err
}

func TestHook_NamePriority(t *testing.T) {
	h := NewHook(&fakeChecker{result: &ComplianceResult{}})
	if h.Name() != "output_compliance.check" {
		t.Fatalf("Name = %q", h.Name())
	}
	if h.Priority() != 50 {
		t.Fatalf("Priority = %d", h.Priority())
	}
}

func TestHook_DisabledWhenCheckerNil(t *testing.T) {
	h := NewHook(nil)
	env := &domain.PipelineRequest{FinalResponse: []byte("response")}
	if h.Enabled(context.Background(), env) {
		t.Fatal("nil checker should disable hook")
	}
}

func TestHook_DisabledWhenNoOutput(t *testing.T) {
	h := NewHook(&fakeChecker{result: &ComplianceResult{}})
	env := &domain.PipelineRequest{}
	if h.Enabled(context.Background(), env) {
		t.Fatal("no output should disable hook")
	}
}

func TestHook_PassThrough(t *testing.T) {
	h := NewHook(&fakeChecker{result: &ComplianceResult{
		Compliant: true, Blocked: false,
		RedactedOutput: "response",
	}})
	env := &domain.PipelineRequest{
		TenantID:      "t1",
		FinalResponse: []byte("response"),
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestHook_BlockReturnsError(t *testing.T) {
	h := NewHook(&fakeChecker{result: &ComplianceResult{
		Compliant: false, Blocked: true,
		Issues: []ComplianceIssue{
			{Type: "toxic", Subtype: "profanity", Severity: 9},
		},
	}})
	env := &domain.PipelineRequest{
		TenantID:      "t1",
		FinalResponse: []byte("bad output"),
	}
	err := h.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("blocked should return error")
	}
	h.OnError(context.Background(), env, err)
	if env.StatusCode != 403 {
		t.Fatalf("StatusCode = %d, want 403", env.StatusCode)
	}
}

func TestHook_RedactionWritesBack(t *testing.T) {
	original := "my phone is 13800138000"
	redacted := "my phone is ***********"
	h := NewHook(&fakeChecker{result: &ComplianceResult{
		Compliant:      true,
		RedactedOutput: redacted,
		Issues: []ComplianceIssue{
			{Type: "pii", Subtype: "phone", Severity: 5, Redacted: true},
		},
	}})
	env := &domain.PipelineRequest{
		TenantID:      "t1",
		FinalResponse: []byte(original),
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(env.FinalResponse) != redacted {
		t.Fatalf("FinalResponse = %q, want %q", env.FinalResponse, redacted)
	}
	if _, ok := env.Metadata["output_compliance_redacted"]; !ok {
		t.Fatal("redacted metadata not set")
	}
}

func TestHook_CheckerErrorDoesNotBlock(t *testing.T) {
	h := NewHook(&fakeChecker{err: errors.New("policy db down")})
	env := &domain.PipelineRequest{
		TenantID:      "t1",
		FinalResponse: []byte("response"),
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("checker error should not propagate: %v", err)
	}
	if _, ok := env.Metadata["output_compliance_error"]; !ok {
		t.Fatal("error should be logged in metadata")
	}
}
