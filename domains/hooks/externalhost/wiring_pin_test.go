package externalhost

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
)

// ── 修②凭据头脱敏钉测（2026-11-27）─────────────────────────────────────

func TestIsSensitiveHeader(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// 精确黑名单
		{"Authorization", true},
		{"authorization", true},
		{"Cookie", true},
		{"X-Api-Key", true},
		{"X-Gateway-Key", true},
		{"X-Internal-Token", true},
		{"Proxy-Authorization", true},
		{"x-goog-api-key", true},
		// 语义后缀（key/token/secret/credential/password/auth）
		{"X-Custom-Key", true},
		{"X-Access-Token", true},
		{"Client-Secret", true},
		{"X-Auth-Token", true},
		{"Some-Password", true},
		{"X-Credential-Id", true},
		// 非敏感
		{"Content-Type", false},
		{"X-Request-Id", false},
		{"User-Agent", false},
		{"Accept", false},
		{"X-Magpie-Session", false},
		{"", true}, // 空头名按敏感处理（保守）
	}
	for _, tc := range cases {
		if got := isSensitiveHeader(tc.name); got != tc.want {
			t.Errorf("isSensitiveHeader(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBuildRequestPayloadRedactsCredentials(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "http://gw/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer sk-secret-1")
	r.Header.Set("X-Api-Key", "sk-secret-2")
	r.Header.Set("X-Gateway-Key", "internal-cred")
	r.Header.Set("X-Internal-Token", "svc-token")
	r.Header.Set("X-Custom-Credential", "leak-me")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Request-Id", "req-123")

	payload := BuildRequestPayload(r, []byte(`{}`), "gpt-4", false)

	for _, leaked := range []string{"Authorization", "X-Api-Key", "X-Gateway-Key", "X-Internal-Token", "X-Custom-Credential"} {
		if _, ok := payload.Headers[leaked]; ok {
			t.Errorf("credential header %q leaked into hook payload", leaked)
		}
	}
	if payload.Headers["Content-Type"] != "application/json" {
		t.Errorf("non-sensitive header Content-Type dropped: %v", payload.Headers)
	}
	if payload.Headers["X-Request-Id"] != "req-123" {
		t.Errorf("non-sensitive header X-Request-Id dropped: %v", payload.Headers)
	}
}

// ── 修⑤送出侧有界副本钉测（2026-11-27）─────────────────────────────────

func TestBoundedRequestResponse(t *testing.T) {
	// 超限正文被截断
	big := make([]byte, contract.MaxBodyBytes+100)
	req := &contract.RequestPayload{Method: "POST", Model: "m", Body: contract.Payload{Body: big, OriginalBytes: len(big)}}
	br := boundedRequest(req)
	if len(br.Body.Body) != contract.MaxBodyBytes {
		t.Fatalf("bounded request body = %d, want %d", len(br.Body.Body), contract.MaxBodyBytes)
	}
	if !br.Body.Truncated || br.Body.OriginalBytes != len(big) {
		t.Errorf("truncation metadata wrong: truncated=%v original=%d", br.Body.Truncated, br.Body.OriginalBytes)
	}
	// 其余字段原样保留（受保护字段比对依赖）
	if br.Method != "POST" || br.Model != "m" {
		t.Errorf("bounded request lost non-body fields: %+v", br)
	}

	// 未超限正文原样
	small := []byte("hello")
	req2 := &contract.RequestPayload{Body: contract.Payload{Body: small, OriginalBytes: 5}}
	br2 := boundedRequest(req2)
	if string(br2.Body.Body) != "hello" || br2.Body.Truncated {
		t.Errorf("small body should pass through unchanged: %+v", br2.Body)
	}

	// nil 安全
	if boundedRequest(nil) != nil || boundedResponse(nil) != nil {
		t.Error("nil payload should stay nil")
	}
}

// ── 修③熔断器钉测（2026-11-27）─────────────────────────────────────────

func TestCircuitBreakerOpensAndHalfOpens(t *testing.T) {
	cb := NewCircuitBreaker(3, 50*time.Millisecond)

	// 前 2 次失败：仍允许
	for i := 0; i < 2; i++ {
		if !cb.Allow("on_request") {
			t.Fatalf("call %d should be allowed before threshold", i)
		}
		cb.RecordFailure("on_request")
	}
	if _, open := cb.State(); open {
		t.Fatal("circuit should not open below threshold")
	}

	// 第 3 次失败：打开
	if !cb.Allow("on_request") {
		t.Fatal("threshold call should be allowed")
	}
	cb.RecordFailure("on_request")
	if _, open := cb.State(); !open {
		t.Fatal("circuit should open at threshold")
	}
	if cb.Allow("on_request") {
		t.Fatal("open circuit should deny")
	}

	// 窗口到期：半开态放一次探测
	time.Sleep(60 * time.Millisecond)
	if !cb.Allow("on_request") {
		t.Fatal("half-open should allow one probe")
	}
	if cb.Allow("on_request") {
		t.Fatal("concurrent probes should be denied")
	}

	// 探测成功：闭合
	cb.RecordSuccess("on_request")
	if _, open := cb.State(); open {
		t.Fatal("circuit should close after successful probe")
	}
	if !cb.Allow("on_request") {
		t.Fatal("closed circuit should allow again")
	}
}

func TestPipelineIntegratorCircuitSkipsAndPassesThrough(t *testing.T) {
	calls := 0
	host := &stubHookHost{onRequest: func() *Outcome {
		calls++
		return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureTimeout}
	}}
	// 阈值 2、窗口 10s（测试内不等待半开）
	p := NewPipelineIntegratorWithCircuit(host, 2, 10*time.Second)

	ctx := context.Background()
	hookCtx := &contract.Context{RequestID: "r1"}
	req := &contract.RequestPayload{Method: "POST"}

	for i := 0; i < 5; i++ {
		p.InterceptRequest(ctx, hookCtx, req)
	}
	// 前 2 次真实调用（达阈值），后 3 次被熔断跳过
	if calls != 2 {
		t.Fatalf("hook calls = %d, want 2 (circuit should skip after threshold)", calls)
	}

	// 熔断跳过的请求仍允许 onResponse（钩子本身健康，只是被暂时摘除）
	reqCtx := &RequestContext{OnRequestSucceeded: true}
	p.CaptureResponse(reqCtx, hookCtx, &contract.ResponsePayload{})
}

func TestFailureResultName(t *testing.T) {
	cases := map[contract.FailureKind]string{
		contract.FailureTimeout:        "timeout",
		contract.FailureCrash:          "crash",
		contract.FailureProtocol:       "protocol",
		contract.FailureProtectedField: "protected_field",
		contract.FailureHook:           "hook_error",
		contract.FailureNone:           "error",
	}
	for kind, want := range cases {
		if got := failureResultName(kind); got != want {
			t.Errorf("failureResultName(%v) = %q, want %q", kind, got, want)
		}
	}
}

// stubHookHost 是钉测用的宿主桩。
type stubHookHost struct {
	onRequest  func() *Outcome
	onResponse func() *Outcome
}

func (s *stubHookHost) OnRequest(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
	if s.onRequest != nil {
		return s.onRequest()
	}
	return &Outcome{Decision: contract.DecisionContinue}
}

func (s *stubHookHost) OnResponse(ctx context.Context, hookCtx *contract.Context, resp *contract.ResponsePayload) *Outcome {
	if s.onResponse != nil {
		return s.onResponse()
	}
	return &Outcome{Decision: contract.DecisionContinue}
}

func (s *stubHookHost) Timeout() time.Duration { return time.Second }
