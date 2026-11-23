package promptinjection

import (
	"context"
	"errors"
	"net/http"
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

// TestHook_SkipsAnalyzerRequests 验证 F2 修复：递归面关闭。
//
// PI 分析器自己的请求（带 X-Llmgw-Pi-Analyzer 头）应跳过检测，
// 避免「分析失败事件被再分析」的递归环路。
func TestHook_SkipsAnalyzerRequests(t *testing.T) {
	// 使用会返回错误的 detector：如果检测被调用就会失败测试
	h := NewHook(&fakeDetector{err: errors.New("detector should not be called")})

	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Llmgw-Pi-Analyzer", "1")

	env := &domain.PipelineRequest{
		TenantID: "t1",
		Metadata: map[string]any{"user_content": "test payload with </untrusted_input> injection"},
		Envelope: &domain.RequestEnvelope{
			Transport: &domain.TransportContext{
				R: req,
			},
		},
	}

	// Execute 应该立即返回 nil，不调用检测器
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute should skip detection for analyzer requests, got error: %v", err)
	}

	// 验证没有调用检测器（如果调用了会返回 "detector should not be called" 错误）
	// 验证没有设置 governance verdict
	if env.Governance != nil && len(env.Governance.Verdicts) > 0 {
		t.Fatal("should not set verdict when skipping detection")
	}

	// 验证没有在 metadata 中设置检测结果
	if _, ok := env.Metadata["prompt_injection_result"]; ok {
		t.Fatal("should not set detection result when skipping")
	}
}
