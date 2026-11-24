package externalhost

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
)

// mockHost implements Host interface for testing
type mockHost struct {
	onRequestFunc  func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome
	onResponseFunc func(ctx context.Context, hookCtx *contract.Context, resp *contract.ResponsePayload) *Outcome
	timeout        time.Duration
}

func (m *mockHost) OnRequest(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
	if m.onRequestFunc != nil {
		return m.onRequestFunc(ctx, hookCtx, req)
	}
	return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureNone}
}

func (m *mockHost) OnResponse(ctx context.Context, hookCtx *contract.Context, resp *contract.ResponsePayload) *Outcome {
	if m.onResponseFunc != nil {
		return m.onResponseFunc(ctx, hookCtx, resp)
	}
	return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureNone}
}

func (m *mockHost) Timeout() time.Duration {
	if m.timeout > 0 {
		return m.timeout
	}
	return contract.DefaultTimeout
}

// TestInterceptRequest_Continue verifies pass-through on continue decision
func TestInterceptRequest_Continue(t *testing.T) {
	host := &mockHost{
		onRequestFunc: func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
			return &Outcome{
				Decision: contract.DecisionContinue,
				Failure:  contract.FailureNone,
			}
		},
	}
	
	integrator := NewPipelineIntegrator(host)
	
	ctx := context.Background()
	hookCtx := &contract.Context{RequestID: "req-001", Origin: contract.OriginLocal}
	reqPayload := &contract.RequestPayload{Method: "POST", Path: "/v1/chat"}
	
	rewritten, reqCtx, err := integrator.InterceptRequest(ctx, hookCtx, reqPayload)
	
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rewritten != nil {
		t.Errorf("expected nil rewritten payload, got %v", rewritten)
	}
	if !reqCtx.OnRequestSucceeded {
		t.Error("expected OnRequestSucceeded=true")
	}
	
	stats := integrator.Stats()
	if stats.RequestTotal != 1 {
		t.Errorf("RequestTotal = %d, want 1", stats.RequestTotal)
	}
	if stats.RequestFailed != 0 {
		t.Errorf("RequestFailed = %d, want 0", stats.RequestFailed)
	}
}

// TestInterceptRequest_Rewrite verifies request rewriting
func TestInterceptRequest_Rewrite(t *testing.T) {
	newModel := "claude-sonnet-4"
	host := &mockHost{
		onRequestFunc: func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
			rewritten := &contract.RequestPayload{
				Method: req.Method,
				Path:   req.Path,
				Model:  newModel,
				Body:   contract.Payload{Body: []byte(`{"rewritten": true}`)},
			}
			return &Outcome{
				Decision: contract.DecisionRewrite,
				Request:  rewritten,
				Failure:  contract.FailureNone,
			}
		},
	}
	
	integrator := NewPipelineIntegrator(host)
	
	ctx := context.Background()
	hookCtx := &contract.Context{RequestID: "req-002", Origin: contract.OriginLocal}
	reqPayload := &contract.RequestPayload{Method: "POST", Path: "/v1/chat", Model: "gpt-4"}
	
	rewritten, reqCtx, err := integrator.InterceptRequest(ctx, hookCtx, reqPayload)
	
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rewritten == nil {
		t.Fatal("expected rewritten payload, got nil")
	}
	if rewritten.Model != newModel {
		t.Errorf("Model = %q, want %q", rewritten.Model, newModel)
	}
	if !reqCtx.OnRequestSucceeded {
		t.Error("expected OnRequestSucceeded=true for rewrite")
	}
}

// TestInterceptRequest_Block verifies block decision returns error
func TestInterceptRequest_Block(t *testing.T) {
	blockReason := "content policy violation"
	host := &mockHost{
		onRequestFunc: func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
			return &Outcome{
				Decision: contract.DecisionBlock,
				Reason:   blockReason,
				Failure:  contract.FailureNone,
			}
		},
	}
	
	integrator := NewPipelineIntegrator(host)
	
	ctx := context.Background()
	hookCtx := &contract.Context{RequestID: "req-003", Origin: contract.OriginLocal}
	reqPayload := &contract.RequestPayload{Method: "POST", Path: "/v1/chat"}
	
	rewritten, reqCtx, err := integrator.InterceptRequest(ctx, hookCtx, reqPayload)
	
	if err == nil {
		t.Fatal("expected error for block decision, got nil")
	}
	if !strings.Contains(err.Error(), blockReason) {
		t.Errorf("error = %q, want to contain %q", err.Error(), blockReason)
	}
	if rewritten != nil {
		t.Errorf("expected nil rewritten on block, got %v", rewritten)
	}
	if reqCtx.OnRequestSucceeded {
		t.Error("expected OnRequestSucceeded=false for block")
	}
}

// TestInterceptRequest_Timeout verifies pass-through on timeout
func TestInterceptRequest_Timeout(t *testing.T) {
	host := &mockHost{
		onRequestFunc: func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
			return &Outcome{
				Decision: contract.DecisionContinue,
				Failure:  contract.FailureTimeout,
				Err:      fmt.Errorf("hook timeout"),
			}
		},
	}
	
	integrator := NewPipelineIntegrator(host)
	
	ctx := context.Background()
	hookCtx := &contract.Context{RequestID: "req-004", Origin: contract.OriginLocal}
	reqPayload := &contract.RequestPayload{Method: "POST", Path: "/v1/chat"}
	
	rewritten, reqCtx, err := integrator.InterceptRequest(ctx, hookCtx, reqPayload)
	
	// W3.2: timeout → pass through (fail-open)
	if err != nil {
		t.Errorf("expected nil error on timeout (pass-through), got %v", err)
	}
	if rewritten != nil {
		t.Errorf("expected nil rewritten on timeout, got %v", rewritten)
	}
	if reqCtx.OnRequestSucceeded {
		t.Error("expected OnRequestSucceeded=false for timeout (don't call onResponse)")
	}
	
	stats := integrator.Stats()
	if stats.RequestFailed != 1 {
		t.Errorf("RequestFailed = %d, want 1", stats.RequestFailed)
	}
}

// TestInterceptRequest_ProtectedField verifies rollback on protected field change
func TestInterceptRequest_ProtectedField(t *testing.T) {
	host := &mockHost{
		onRequestFunc: func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
			return &Outcome{
				Decision: contract.DecisionContinue,
				Failure:  contract.FailureProtectedField,
				Err:      contract.ErrProtectedField,
			}
		},
	}
	
	integrator := NewPipelineIntegrator(host)
	
	ctx := context.Background()
	hookCtx := &contract.Context{RequestID: "req-005", Origin: contract.OriginLocal}
	reqPayload := &contract.RequestPayload{Method: "POST", Path: "/v1/chat"}
	
	rewritten, reqCtx, err := integrator.InterceptRequest(ctx, hookCtx, reqPayload)
	
	// W3.2: protected field → pass through but mark failed
	if err != nil {
		t.Errorf("expected nil error on protected field (pass-through), got %v", err)
	}
	if rewritten != nil {
		t.Errorf("expected nil rewritten on protected field violation, got %v", rewritten)
	}
	if reqCtx.OnRequestSucceeded {
		t.Error("expected OnRequestSucceeded=false for protected field")
	}
	
	stats := integrator.Stats()
	if stats.RequestFailed != 1 {
		t.Errorf("RequestFailed = %d, want 1", stats.RequestFailed)
	}
}

// TestCaptureResponse_StateFlow verifies state flows from onRequest to onResponse
func TestCaptureResponse_StateFlow(t *testing.T) {
	var capturedState map[string]string
	var mu sync.Mutex
	done := make(chan struct{})
	
	host := &mockHost{
		onRequestFunc: func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
			// onRequest sets state
			hookCtx.State = map[string]string{"trace_id": "abc123", "user": "test"}
			return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureNone}
		},
		onResponseFunc: func(ctx context.Context, hookCtx *contract.Context, resp *contract.ResponsePayload) *Outcome {
			mu.Lock()
			capturedState = make(map[string]string)
			for k, v := range hookCtx.State {
				capturedState[k] = v
			}
			mu.Unlock()
			close(done)
			return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureNone}
		},
		timeout: 100 * time.Millisecond,
	}
	
	integrator := NewPipelineIntegrator(host)
	
	ctx := context.Background()
	hookCtx := &contract.Context{
		RequestID: "req-006",
		Origin:    contract.OriginLocal,
		State:     make(map[string]string),
	}
	reqPayload := &contract.RequestPayload{Method: "POST", Path: "/v1/chat"}
	
	// onRequest
	_, reqCtx, err := integrator.InterceptRequest(ctx, hookCtx, reqPayload)
	if err != nil {
		t.Fatalf("InterceptRequest failed: %v", err)
	}
	
	// Extract state from hookCtx (host modified it)
	reqCtx.HookState = hookCtx.State
	
	// onResponse
	respPayload := &contract.ResponsePayload{Status: 200}
	integrator.CaptureResponse(reqCtx, hookCtx, respPayload)
	
	// Wait for background goroutine (with timeout)
	select {
	case <-done:
		// OK
	case <-time.After(1 * time.Second):
		t.Fatal("onResponse not called within timeout")
	}
	
	mu.Lock()
	defer mu.Unlock()
	
	if capturedState == nil {
		t.Fatal("state not captured in onResponse")
	}
	if capturedState["trace_id"] != "abc123" {
		t.Errorf("trace_id = %q, want abc123", capturedState["trace_id"])
	}
	if capturedState["user"] != "test" {
		t.Errorf("user = %q, want test", capturedState["user"])
	}
}

// TestCaptureResponse_SkippedOnRequestFailed verifies onResponse not called if onRequest failed
func TestCaptureResponse_SkippedOnRequestFailed(t *testing.T) {
	onResponseCalled := false
	var mu sync.Mutex
	
	host := &mockHost{
		onRequestFunc: func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
			return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureTimeout, Err: fmt.Errorf("timeout")}
		},
		onResponseFunc: func(ctx context.Context, hookCtx *contract.Context, resp *contract.ResponsePayload) *Outcome {
			mu.Lock()
			onResponseCalled = true
			mu.Unlock()
			return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureNone}
		},
		timeout: 100 * time.Millisecond,
	}
	
	integrator := NewPipelineIntegrator(host)
	
	ctx := context.Background()
	hookCtx := &contract.Context{RequestID: "req-007", Origin: contract.OriginLocal}
	reqPayload := &contract.RequestPayload{Method: "POST", Path: "/v1/chat"}
	
	// onRequest fails
	_, reqCtx, _ := integrator.InterceptRequest(ctx, hookCtx, reqPayload)
	
	if reqCtx.OnRequestSucceeded {
		t.Fatal("expected OnRequestSucceeded=false for timeout")
	}
	
	// onResponse should be skipped
	respPayload := &contract.ResponsePayload{Status: 200}
	integrator.CaptureResponse(reqCtx, hookCtx, respPayload)
	
	// Wait briefly to ensure onResponse doesn't get called
	time.Sleep(200 * time.Millisecond)
	
	mu.Lock()
	defer mu.Unlock()
	
	if onResponseCalled {
		t.Error("onResponse should not be called when onRequest failed")
	}
	
	stats := integrator.Stats()
	if stats.ResponseSkipped != 1 {
		t.Errorf("ResponseSkipped = %d, want 1", stats.ResponseSkipped)
	}
}

// TestCaptureResponse_FireAndForget verifies onResponse doesn't block
func TestCaptureResponse_FireAndForget(t *testing.T) {
	blockChan := make(chan struct{})
	
	host := &mockHost{
		onRequestFunc: func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
			return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureNone}
		},
		onResponseFunc: func(ctx context.Context, hookCtx *contract.Context, resp *contract.ResponsePayload) *Outcome {
			// Block to simulate slow hook
			<-blockChan
			return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureNone}
		},
		timeout: 5 * time.Second,
	}
	
	integrator := NewPipelineIntegrator(host)
	
	ctx := context.Background()
	hookCtx := &contract.Context{RequestID: "req-008", Origin: contract.OriginLocal}
	reqPayload := &contract.RequestPayload{Method: "POST", Path: "/v1/chat"}
	
	// onRequest
	_, reqCtx, err := integrator.InterceptRequest(ctx, hookCtx, reqPayload)
	if err != nil {
		t.Fatalf("InterceptRequest failed: %v", err)
	}
	
	// CaptureResponse should return immediately (fire-and-forget)
	start := time.Now()
	respPayload := &contract.ResponsePayload{Status: 200}
	integrator.CaptureResponse(reqCtx, hookCtx, respPayload)
	elapsed := time.Since(start)
	
	// Should return in < 10ms (not wait for hook to complete)
	if elapsed > 10*time.Millisecond {
		t.Errorf("CaptureResponse blocked for %v, expected immediate return", elapsed)
	}
	
	// Unblock the hook
	close(blockChan)
}

// TestCaptureResponseFromStream_BoundedCopy verifies 8MiB bounded copy
func TestCaptureResponseFromStream_BoundedCopy(t *testing.T) {
	var capturedBody []byte
	var truncated bool
	var mu sync.Mutex
	done := make(chan struct{})
	
	host := &mockHost{
		onRequestFunc: func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
			return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureNone}
		},
		onResponseFunc: func(ctx context.Context, hookCtx *contract.Context, resp *contract.ResponsePayload) *Outcome {
			mu.Lock()
			capturedBody = resp.Body.Body
			truncated = resp.Body.Truncated
			mu.Unlock()
			close(done)
			return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureNone}
		},
		timeout: 100 * time.Millisecond,
	}
	
	integrator := NewPipelineIntegrator(host)
	
	ctx := context.Background()
	hookCtx := &contract.Context{RequestID: "req-009", Origin: contract.OriginLocal}
	reqPayload := &contract.RequestPayload{Method: "POST", Path: "/v1/chat"}
	
	// onRequest
	_, reqCtx, err := integrator.InterceptRequest(ctx, hookCtx, reqPayload)
	if err != nil {
		t.Fatalf("InterceptRequest failed: %v", err)
	}
	
	// Create response body larger than MaxBodyBytes
	largeBody := bytes.Repeat([]byte("x"), contract.MaxBodyBytes+1000)
	bodyReader := bytes.NewReader(largeBody)
	
	respPayload := &contract.ResponsePayload{Status: 200}
	
	// Capture from stream
	readBytes, err := integrator.CaptureResponseFromStream(reqCtx, hookCtx, respPayload, bodyReader)
	if err != nil {
		t.Fatalf("CaptureResponseFromStream failed: %v", err)
	}
	
	// Wait for background capture
	select {
	case <-done:
		// OK
	case <-time.After(1 * time.Second):
		t.Fatal("onResponse not called within timeout")
	}
	
	mu.Lock()
	defer mu.Unlock()
	
	// Verify truncation
	if !truncated {
		t.Error("expected Truncated=true for large body")
	}
	if len(capturedBody) != contract.MaxBodyBytes {
		t.Errorf("captured body length = %d, want %d", len(capturedBody), contract.MaxBodyBytes)
	}
	if len(readBytes) != contract.MaxBodyBytes+1 {
		t.Errorf("read bytes length = %d, want %d", len(readBytes), contract.MaxBodyBytes+1)
	}
}

// TestBuildHookContext verifies context construction from HTTP request
func TestBuildHookContext(t *testing.T) {
	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(5*time.Second))
	defer cancel()
	req = req.WithContext(ctx)
	
	hookCtx := BuildHookContext(req, "req-010", "sess-123", "tenant-456")
	
	if hookCtx.RequestID != "req-010" {
		t.Errorf("RequestID = %q, want req-010", hookCtx.RequestID)
	}
	if hookCtx.Session != "sess-123" {
		t.Errorf("Session = %q, want sess-123", hookCtx.Session)
	}
	if hookCtx.TenantID != "tenant-456" {
		t.Errorf("TenantID = %q, want tenant-456", hookCtx.TenantID)
	}
	if hookCtx.DeadlineUnixNano == 0 {
		t.Error("expected DeadlineUnixNano to be set")
	}
	if hookCtx.State == nil {
		t.Error("expected State map to be initialized")
	}
}

// TestBuildRequestPayload verifies request payload construction
func TestBuildRequestPayload(t *testing.T) {
	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")  // Should be filtered
	req.Header.Set("X-Request-ID", "req-011")
	
	body := []byte(`{"messages": [{"role": "user", "content": "hello"}]}`)
	
	payload := BuildRequestPayload(req, body, "claude-sonnet-4", true)
	
	if payload.Method != "POST" {
		t.Errorf("Method = %q, want POST", payload.Method)
	}
	if payload.Path != "/v1/chat/completions" {
		t.Errorf("Path = %q, want /v1/chat/completions", payload.Path)
	}
	if payload.Model != "claude-sonnet-4" {
		t.Errorf("Model = %q, want claude-sonnet-4", payload.Model)
	}
	if !payload.Stream {
		t.Error("expected Stream=true")
	}
	
	// Verify sensitive headers filtered
	if _, exists := payload.Headers["Authorization"]; exists {
		t.Error("Authorization header should be filtered")
	}
	
	// Verify non-sensitive headers included
	if ct := payload.Headers["Content-Type"]; ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if xrid := payload.Headers["X-Request-Id"]; xrid != "req-011" {
		t.Errorf("X-Request-Id = %q, want req-011", xrid)
	}
}

// TestBuildResponsePayload verifies response payload construction
func TestBuildResponsePayload(t *testing.T) {
	headers := http.Header{}
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("X-Request-ID", "req-012")
	
	body := []byte(`data: {"content": "hello"}\n\n`)
	
	payload := BuildResponsePayload(200, headers, body, "claude-sonnet-4", true)
	
	if payload.Status != 200 {
		t.Errorf("Status = %d, want 200", payload.Status)
	}
	if payload.Model != "claude-sonnet-4" {
		t.Errorf("Model = %q, want claude-sonnet-4", payload.Model)
	}
	if !payload.Stream {
		t.Error("expected Stream=true")
	}
	if ct := payload.Headers["Content-Type"]; ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

// TestStats verifies metrics tracking
func TestStats(t *testing.T) {
	host := &mockHost{
		onRequestFunc: func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
			return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureNone}
		},
	}
	
	integrator := NewPipelineIntegrator(host)
	
	ctx := context.Background()
	hookCtx := &contract.Context{RequestID: "req-013", Origin: contract.OriginLocal}
	reqPayload := &contract.RequestPayload{Method: "POST", Path: "/v1/chat"}
	
	// Make 3 requests
	for i := 0; i < 3; i++ {
		integrator.InterceptRequest(ctx, hookCtx, reqPayload)
	}
	
	stats := integrator.Stats()
	if stats.RequestTotal != 3 {
		t.Errorf("RequestTotal = %d, want 3", stats.RequestTotal)
	}
	if stats.RequestFailed != 0 {
		t.Errorf("RequestFailed = %d, want 0", stats.RequestFailed)
	}
}
