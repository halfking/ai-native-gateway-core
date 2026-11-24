// Package externalhost provides pipeline integration for external hooks (W3).
package externalhost

import (
	"context"
	"fmt"
	"io"
	"net/http"
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

	// Call onRequest hook
	outcome := p.host.OnRequest(ctx, hookCtx, req)

	// Track skipped hooks
	if outcome.Skipped {
		reqCtx.OnRequestSkipped = true
		reqCtx.OnRequestSucceeded = true // Still allow onResponse
		return nil, reqCtx, nil
	}

	// Check block decision first (before failure handling)
	if outcome.Decision == contract.DecisionBlock {
		return nil, reqCtx, fmt.Errorf("external hook blocked request: %s", outcome.Reason)
	}

	// Track failures
	if outcome.Failed() {
		p.mu.Lock()
		p.requestFailed++
		p.mu.Unlock()

		// W3.2: timeout/crash/protocol → pass through (fail-open)
		// protected_field/hook → pass through but mark failed
		// Pass through on failure, but don't call onResponse
		return nil, reqCtx, nil
	}

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
		outcome := p.host.OnResponse(ctx, hookCtx, resp)

		if outcome.Failed() {
			p.mu.Lock()
			p.responseFailed++
			p.mu.Unlock()
		}
	}()
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
		lower := http.CanonicalHeaderKey(k)
		if lower == "Authorization" || lower == "Cookie" || lower == "X-Api-Key" {
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
