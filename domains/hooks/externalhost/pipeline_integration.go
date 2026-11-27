// Package externalhost provides pipeline integration for external hooks (W3).
package externalhost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
)

// HookHost defines the interface for external hook invocation.
// Both Host and test mocks implement this interface.
type HookHost interface {
	OnRequest(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome
	OnResponse(ctx context.Context, hookCtx *contract.Context, resp *contract.ResponsePayload) *Outcome
	Timeout() time.Duration
}

// PipelineIntegrator bridges external hooks into the gateway request pipeline.
//
// Integration points (W3 architecture):
//   - onRequest: Called before protocol translation (Authentication Stage)
//   - onResponse: Called after upstream response, fire-and-forget
//
// Orchestration semantics (W3.2/W3.3):
//   - onRequest failure/timeout → pass through without calling onResponse
//   - ctx.state flows from onRequest to onResponse
//   - onResponse uses fire-and-forget goroutine with 8MiB bounded copy
//   - Response capture completes even if client disconnects
type PipelineIntegrator struct {
	host HookHost

	// circuit 熔断器（可选；nil 表示不熔断）。
	// 连续失败达阈值后跳过钩子调用，原样直通（W3.2 fail-open 延伸）。
	circuit *CircuitBreaker

	// Metrics for monitoring
	mu              sync.Mutex
	requestTotal    int64
	requestFailed   int64
	responseTotal   int64
	responseFailed  int64
	responseSkipped int64
}

// NewPipelineIntegrator creates an integrator with the given host.
func NewPipelineIntegrator(host HookHost) *PipelineIntegrator {
	if host == nil {
		panic("externalhost: pipeline integrator requires non-nil host")
	}
	return &PipelineIntegrator{host: host}
}

// NewPipelineIntegratorWithCircuit 构造带熔断器的整合器。
//
// threshold <= 0 用默认 5 次连续失败；openWindow <= 0 用默认 30s。
func NewPipelineIntegratorWithCircuit(host HookHost, threshold int, openWindow time.Duration) *PipelineIntegrator {
	p := NewPipelineIntegrator(host)
	p.circuit = NewCircuitBreaker(threshold, openWindow)
	return p
}

// RequestContext holds state that flows from onRequest to onResponse.
type RequestContext struct {
	// HookState is the state bag returned by onRequest, passed to onResponse.
	HookState map[string]string
	// OnRequestSucceeded indicates whether onRequest completed successfully.
	// When false, onResponse should not be called (W3.2 orchestration).
	OnRequestSucceeded bool
	// OnRequestSkipped indicates the hook was skipped by policy.
	OnRequestSkipped bool
}

// InterceptRequest calls onRequest hook before protocol translation.
//
// Returns:
//   - rewritten request payload (if hook returned rewrite decision)
//   - request context for onResponse correlation
//   - error (only if hook returned block decision; timeout/crash pass through)
//
// Caller should:
//   - Use rewritten payload if non-nil
//   - Continue with original request on error=nil (pass-through)
//   - Abort request and return error if error != nil (block decision)
func (p *PipelineIntegrator) InterceptRequest(
	ctx context.Context,
	hookCtx *contract.Context,
	req *contract.RequestPayload,
) (*contract.RequestPayload, *RequestContext, error) {
	p.mu.Lock()
	p.requestTotal++
	p.mu.Unlock()

	reqCtx := &RequestContext{
		HookState:          nil,
		OnRequestSucceeded: false,
		OnRequestSkipped:   false,
	}

	// 熔断检查（W3 上线前必修项③）：熔断打开时跳过钩子调用，原样直通。
	if !p.circuit.Allow("on_request") {
		recordCall("on_request", "circuit_open", 0)
		reqCtx.OnRequestSkipped = true
		// 熔断跳过不视为失败——本请求仍允许收 onResponse（钩子本身健康，只是被暂时摘除）。
		reqCtx.OnRequestSucceeded = true
		return nil, reqCtx, nil
	}

	// Call onRequest hook
	start := time.Now()
	outcome := p.host.OnRequest(ctx, hookCtx, req)
	elapsed := time.Since(start)

	// Track skipped hooks
	if outcome.Skipped {
		recordCall("on_request", "skipped", elapsed)
		reqCtx.OnRequestSkipped = true
		reqCtx.OnRequestSucceeded = true // Still allow onResponse
		return nil, reqCtx, nil
	}

	// Check block decision first (before failure handling)
	if outcome.Decision == contract.DecisionBlock {
		recordCall("on_request", "block", elapsed)
		p.circuit.RecordSuccess("on_request")
		return nil, reqCtx, fmt.Errorf("external hook blocked request: %s", outcome.Reason)
	}

	// Track failures
	if outcome.Failed() {
		p.mu.Lock()
		p.requestFailed++
		p.mu.Unlock()

		recordCall("on_request", failureResultName(outcome.Failure), elapsed)
		p.circuit.RecordFailure("on_request")

		// W3.2: timeout/crash/protocol → pass through (fail-open)
		// protected_field/hook → pass through but mark failed
		// Pass through on failure, but don't call onResponse
		return nil, reqCtx, nil
	}

	recordCall("on_request", string(outcome.Decision), elapsed)
	p.circuit.RecordSuccess("on_request")

	// Success: extract state for onResponse correlation
	reqCtx.OnRequestSucceeded = true
	if hookCtx != nil && hookCtx.State != nil {
		reqCtx.HookState = hookCtx.State
	}

	// Apply rewrite if present
	if outcome.Decision == contract.DecisionRewrite && outcome.Request != nil {
		return outcome.Request, reqCtx, nil
	}

	// Continue with original request
	return nil, reqCtx, nil
}

// CaptureResponse captures the response in a fire-and-forget goroutine (W3.3).
//
// Key semantics:
//   - Does NOT block EOF to client
//   - Uses 8MiB bounded copy of response body
//   - Completes even if client disconnects
//   - Only called if reqCtx.OnRequestSucceeded is true
//
// Caller should invoke this AFTER sending response to client, passing:
//   - detached context (not tied to client connection)
//   - hookCtx with state from reqCtx
//   - response payload with bounded body copy
func (p *PipelineIntegrator) CaptureResponse(
	reqCtx *RequestContext,
	hookCtx *contract.Context,
	resp *contract.ResponsePayload,
) {
	// W3.2: Don't call onResponse if onRequest failed
	if !reqCtx.OnRequestSucceeded {
		p.mu.Lock()
		p.responseSkipped++
		p.mu.Unlock()
		return
	}

	p.mu.Lock()
	p.responseTotal++
	p.mu.Unlock()

	// 熔断检查（W3 上线前必修项③）。
	if !p.circuit.Allow("on_response") {
		recordCall("on_response", "circuit_open", 0)
		p.mu.Lock()
		p.responseSkipped++
		p.mu.Unlock()
		return
	}

	// Fire-and-forget goroutine (W3.3)
	go func() {
		// Use background context (not tied to client connection)
		ctx, cancel := context.WithTimeout(context.Background(), p.host.Timeout())
		defer cancel()

		// Restore state from onRequest
		if hookCtx != nil && reqCtx.HookState != nil {
			if hookCtx.State == nil {
				hookCtx.State = make(map[string]string)
			}
			for k, v := range reqCtx.HookState {
				hookCtx.State[k] = v
			}
		}

		// Call onResponse hook
		start := time.Now()
		outcome := p.host.OnResponse(ctx, hookCtx, resp)
		elapsed := time.Since(start)

		if outcome.Failed() {
			p.mu.Lock()
			p.responseFailed++
			p.mu.Unlock()
			recordCall("on_response", failureResultName(outcome.Failure), elapsed)
			p.circuit.RecordFailure("on_response")
			return
		}
		recordCall("on_response", string(outcome.Decision), elapsed)
		p.circuit.RecordSuccess("on_response")
	}()
}

// failureResultName 把失败归因映射为指标 label 值（低基数枚举）。
func failureResultName(kind contract.FailureKind) string {
	switch kind {
	case contract.FailureTimeout:
		return "timeout"
	case contract.FailureCrash:
		return "crash"
	case contract.FailureProtocol:
		return "protocol"
	case contract.FailureProtectedField:
		return "protected_field"
	case contract.FailureHook:
		return "hook_error"
	default:
		return "error"
	}
}

// CaptureResponseFromStream reads response body, creates bounded copy, and captures.
//
// This helper is for streaming responses where the body is read incrementally.
// It reads up to MaxBodyBytes, then calls CaptureResponse.
//
// Returns the body bytes read (for caller to process).
func (p *PipelineIntegrator) CaptureResponseFromStream(
	reqCtx *RequestContext,
	hookCtx *contract.Context,
	resp *contract.ResponsePayload,
	body io.Reader,
) ([]byte, error) {
	if body == nil {
		return nil, nil
	}

	// Read up to MaxBodyBytes + 1 to detect truncation
	buf := make([]byte, contract.MaxBodyBytes+1)
	n, err := io.ReadFull(body, buf)

	// EOF or unexpected EOF within limit is fine
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		if n == 0 {
			return nil, err
		}
	}

	// Create bounded payload
	payload := contract.Payload{
		OriginalBytes: n,
	}

	if n <= contract.MaxBodyBytes {
		payload.Body = buf[:n]
		payload.Truncated = false
	} else {
		payload.Body = buf[:contract.MaxBodyBytes]
		payload.Truncated = true
	}

	// Update response with captured body
	respCopy := *resp
	respCopy.Body = payload

	// Capture in background
	p.CaptureResponse(reqCtx, hookCtx, &respCopy)

	return buf[:n], nil
}

// Stats returns current integration metrics.
func (p *PipelineIntegrator) Stats() PipelineStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	return PipelineStats{
		RequestTotal:    p.requestTotal,
		RequestFailed:   p.requestFailed,
		ResponseTotal:   p.responseTotal,
		ResponseFailed:  p.responseFailed,
		ResponseSkipped: p.responseSkipped,
	}
}

// PipelineStats tracks integration metrics.
type PipelineStats struct {
	RequestTotal    int64
	RequestFailed   int64
	ResponseTotal   int64
	ResponseFailed  int64
	ResponseSkipped int64
}

// BuildHookContext constructs a contract.Context from HTTP request metadata.
//
// This helper extracts common fields needed by external hooks.
// Caller should set Protocol, Model, and Origin based on authentication/routing.
func BuildHookContext(r *http.Request, requestID, sessionID, tenantID string) *contract.Context {
	ctx := &contract.Context{
		RequestID: requestID,
		Session:   sessionID,
		TenantID:  tenantID,
		State:     make(map[string]string),
		Abort:     false,
	}

	// Set deadline from request context if present
	if deadline, ok := r.Context().Deadline(); ok {
		ctx.DeadlineUnixNano = deadline.UnixNano()
	}

	return ctx
}

// sensitiveRequestHeaders 是绝不外传给外部钩子的请求头（大小写不敏感）。
//
// W3.1 合同红线：钩子不得依赖任何鉴权/凭据头。2026-11-27 修订——
// 原实现只过滤 Authorization/Cookie/X-Api-Key 三项，漏掉了网关自身的
// 内部凭据头（X-Gateway-Key / X-Internal-Token 等），等于把内部凭据
// 送给了进程外插件。现改为「精确黑名单 + 凭据语义后缀」双保险：
// 凡头名含 key/token/secret/credential/password/session-cookie 语义的一律剔除。
var sensitiveRequestHeaders = map[string]bool{
	"authorization":        true,
	"proxy-authorization":  true,
	"cookie":               true,
	"set-cookie":           true,
	"x-api-key":            true,
	"apikey":               true,
	"api-key":              true,
	"x-gateway-key":        true,
	"x-internal-token":     true,
	"x-session-token":      true,
	"x-auth-token":         true,
	"x-access-token":       true,
	"x-refresh-token":      true,
	"x-amz-security-token": true,
	"x-goog-api-key":       true,
}

// isSensitiveHeader 判断请求头是否含凭据语义。
//
// 命中精确黑名单直接剔除；否则看头名是否带凭据语义词根（key/token/secret/
// credential/password/auth），命中即剔除——保守方向是"宁可多剔"，
// 因为漏一个就等于把凭据送出了进程边界。
func isSensitiveHeader(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return true
	}
	if sensitiveRequestHeaders[lower] {
		return true
	}
	for _, marker := range []string{"key", "token", "secret", "credential", "password", "authorization", "auth-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// BuildRequestPayload constructs a contract.RequestPayload from HTTP request.
//
// Body should be the raw request body bytes (caller is responsible for reading).
// This helper extracts method, path, and sanitized headers.
func BuildRequestPayload(r *http.Request, body []byte, model string, stream bool) *contract.RequestPayload {
	payload := &contract.RequestPayload{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: make(map[string]string),
		Body:    contract.BoundedCopy(body),
		Model:   model,
		Stream:  stream,
	}

	// Copy sanitized headers (skip authorization/sensitive headers)
	for k, vals := range r.Header {
		if len(vals) == 0 {
			continue
		}
		// Skip sensitive headers (W3.1 contract)
		if isSensitiveHeader(k) {
			continue
		}
		payload.Headers[k] = vals[0]
	}

	return payload
}

// BuildResponsePayload constructs a contract.ResponsePayload from HTTP response.
//
// Body should be bounded copy (use contract.BoundedCopy or CaptureResponseFromStream).
func BuildResponsePayload(status int, headers http.Header, body []byte, model string, stream bool) *contract.ResponsePayload {
	payload := &contract.ResponsePayload{
		Status:  status,
		Headers: make(map[string]string),
		Body:    contract.BoundedCopy(body),
		Model:   model,
		Stream:  stream,
	}

	// Copy response headers
	for k, vals := range headers {
		if len(vals) > 0 {
			payload.Headers[k] = vals[0]
		}
	}

	return payload
}
