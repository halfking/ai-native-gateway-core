package promptinjection

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

type fakeDetector struct {
	result *DetectionResult
	err    error
}

func (f *fakeDetector) Detect(_ context.Context, _, _ string) (*DetectionResult, error) {
	return f.result, f.err
}

func TestHook_NamePriority(t *testing.T) {
	h := NewHook(&fakeDetector{result: &DetectionResult{}})
	if h.Name() != "prompt_injection.detect" {
		t.Fatalf("Name = %q", h.Name())
	}
	if h.Priority() != 120 {
		t.Fatalf("Priority = %d", h.Priority())
	}
}

func TestHook_DisabledWhenDetectorNil(t *testing.T) {
	h := NewHook(nil)
	env := &domain.PipelineRequest{Metadata: map[string]any{"user_content": "hi"}}
	if h.Enabled(context.Background(), env) {
		t.Fatal("nil detector should disable hook")
	}
}

func TestHook_DisabledWhenNoContent(t *testing.T) {
	h := NewHook(&fakeDetector{result: &DetectionResult{}})
	env := &domain.PipelineRequest{Metadata: map[string]any{}}
	if h.Enabled(context.Background(), env) {
		t.Fatal("no user_content should disable hook")
	}
}

func TestHook_PassThrough(t *testing.T) {
	h := NewHook(&fakeDetector{result: &DetectionResult{
		Score: 0, RiskLevel: "low", ActionTaken: "pass", Blocked: false,
	}})
	env := &domain.PipelineRequest{
		TenantID: "t1",
		Metadata: map[string]any{"user_content": "hello"},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if env.StatusCode != 0 {
		t.Fatalf("StatusCode should be 0 on pass, got %d", env.StatusCode)
	}
	if env.Governance == nil {
		t.Fatal("Governance should be initialized by EnsureGovernance")
	}
}

func TestHook_BlockReturnsError(t *testing.T) {
	h := NewHook(&fakeDetector{result: &DetectionResult{
		Score: 10, RiskLevel: "critical", ActionTaken: "block", Blocked: true,
		Evidence: "jailbreak detected",
	}})
	env := &domain.PipelineRequest{
		TenantID: "t1",
		Metadata: map[string]any{"user_content": "ignore previous"},
	}
	err := h.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("blocked should return error")
	}
	// OnError 应把 status 设 403
	_ = h.OnError(context.Background(), env, err) //nolint:errcheck // OnError is being tested for state mutation, not return
	if env.StatusCode != 403 {
		t.Fatalf("StatusCode = %d, want 403", env.StatusCode)
	}
	// 验证 verdict 写入
	if env.Governance == nil || len(env.Governance.Verdicts) == 0 {
		t.Fatal("verdict not recorded")
	}
	v := env.Governance.Verdicts[0]
	if v.PluginName != "prompt_injection" || v.Severity != 3 || v.Allow {
		t.Fatalf("verdict mismatch: %+v", v)
	}
}

func TestHook_DetectorErrorDoesNotBlock(t *testing.T) {
	h := NewHook(&fakeDetector{err: errors.New("db down")})
	env := &domain.PipelineRequest{
		TenantID: "t1",
		Metadata: map[string]any{"user_content": "hello"},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("detector error should not propagate: %v", err)
	}
	if _, ok := env.Metadata["prompt_injection_error"]; !ok {
		t.Fatal("error should be logged in metadata")
	}
}

// 验证 governance.RecordVerdict 真的接受我们构造的 *Verdict。
// 这是接口契约测试，避免将来 governance 包改字段时静默失败。
func TestGovernanceVerdictShape(t *testing.T) {
	v := toGovernanceVerdict(&DetectionResult{
		RiskLevel:   "high",
		ActionTaken: "sanitize",
		Score:       8,
	})
	if v.Severity != 2 {
		t.Errorf("high → Severity 2, got %d", v.Severity)
	}
	if v.FixAction != "sanitize_input" {
		t.Errorf("sanitize action → FixAction sanitize_input, got %q", v.FixAction)
	}
	if v.Code != "prompt_injection.high" {
		t.Errorf("Code, got %q", v.Code)
	}
	_ = governance.Verdict{} // ensure package still exports the type
}
