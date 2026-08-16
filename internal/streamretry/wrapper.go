package streamretry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch" //nolint:depguard // V3.2 state-transition logger
	"github.com/kaixuan/llm-gateway-go/internal/retryowner"
)

// requestIDCtxKey is the private context key used to thread the inbound
// X-Request-Id from the http.Request into the wrapper's retry loop. We
// deliberately avoid touching the streaming / identity packages to keep the
// streamretry package self-contained (the request id is already stamped
// upstream by domains/streaming/handler.go).
type requestIDCtxKey struct{}
type tenantCarrierCtxKey struct{}

type tenantCarrier struct {
	mu       sync.RWMutex
	tenantID string
}

func withTenantCarrier(ctx context.Context) context.Context {
	return context.WithValue(ctx, tenantCarrierCtxKey{}, &tenantCarrier{})
}

// SetAuthenticatedTenant records the tenant resolved by the trusted key verifier.
// It is intentionally a no-op unless the retry wrapper installed a request carrier.
func SetAuthenticatedTenant(ctx context.Context, tenantID string) {
	carrier, _ := ctx.Value(tenantCarrierCtxKey{}).(*tenantCarrier)
	if carrier == nil || strings.TrimSpace(tenantID) == "" {
		return
	}
	carrier.mu.Lock()
	carrier.tenantID = tenantID
	carrier.mu.Unlock()
}

func authenticatedTenantFromCtx(ctx context.Context) string {
	carrier, _ := ctx.Value(tenantCarrierCtxKey{}).(*tenantCarrier)
	if carrier == nil {
		return ""
	}
	carrier.mu.RLock()
	defer carrier.mu.RUnlock()
	return carrier.tenantID
}

// withRequestID attaches a request id to ctx (set by ServeHTTP from the
// inbound X-Request-Id header).
func withRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDCtxKey{}, requestID)
}

// requestIDFromCtx returns the request id stashed by withRequestID, or "".
func requestIDFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDCtxKey{}).(string); ok {
		return v
	}
	// Fallback: try the standard X-Request-Id header in case the caller
	// didn't route through ServeHTTP (e.g. legacy callers using Execute
	// directly). Some upstream callers place the id in the request itself,
	// not just the context.
	if req, ok := ctx.Value(httpReqCtxKey{}).(*http.Request); ok && req != nil {
		return strings.TrimSpace(req.Header.Get("X-Request-Id"))
	}
	return ""
}

// httpReqCtxKey is a secondary context key used by callers that prefer
// to pass the *http.Request directly (e.g. unit tests). Production flows
// use http.ServerMux → ServeHTTP → withRequestID.
type httpReqCtxKey struct{}

// StreamFunc represents a function that executes a streaming request.
// It should return an error if the stream fails, or nil if it completes successfully.
type StreamFunc func(ctx context.Context, w http.ResponseWriter) error

// WrapperMetrics holds metrics for retry behavior.
type WrapperMetrics struct {
	TotalAttempts  int
	SuccessAttempt int // 0-indexed, -1 if all failed
	TotalRetries   int
	LastError      error
}

// Wrapper wraps a streaming function with intelligent retry and keepalive.
type Wrapper struct {
	config    Config
	metricsMu sync.RWMutex
	metrics   WrapperMetrics
	logger    *slog.Logger
}

// NewWrapper creates a new retry wrapper with the given configuration.
func NewWrapper(config Config, logger *slog.Logger) *Wrapper {
	if logger == nil {
		logger = slog.Default()
	}
	return &Wrapper{
		config:  config,
		metrics: WrapperMetrics{SuccessAttempt: -1},
		logger:  logger,
	}
}

// Execute runs the streaming function with retry and keepalive.
//
// Flow:
//  1. Set up SSE headers and flush initial response
//  2. Start keepalive goroutine
//  3. Execute stream function
//  4. On failure: classify error, check if retriable, sleep with backoff, retry
//  5. On success: stop keepalive, return
//  6. On max retries: stop keepalive, return last error
//
// The wrapper is transparent to the client: keepalive events are SSE comments
// that don't trigger parsing errors in the client.
func (w *Wrapper) Execute(ctx context.Context, httpW http.ResponseWriter, streamFunc StreamFunc) error {
	_, err := w.ExecuteWithMetrics(ctx, httpW, streamFunc)
	return err
}

// ExecuteWithMetrics runs the stream and returns metrics for this execution.
// The returned snapshot is isolated from concurrent requests; Metrics remains
// available for callers that only need the latest completed execution.
func (w *Wrapper) ExecuteWithMetrics(ctx context.Context, httpW http.ResponseWriter, streamFunc StreamFunc) (metrics WrapperMetrics, err error) {
	// Initialize metrics for this execution
	metrics = WrapperMetrics{SuccessAttempt: -1}
	defer func() {
		w.metricsMu.Lock()
		w.metrics = metrics
		w.metricsMu.Unlock()
		recordExecutionMetrics(metrics, err)
	}()

	if !w.config.Enabled {
		// Fast path: retry disabled, execute once
		metrics.TotalAttempts = 1
		err = streamFunc(ctx, httpW)
		if err == nil {
			metrics.SuccessAttempt = 0
		} else {
			metrics.LastError = err
		}
		return metrics, err
	}

	// Initialize retry context
	rc := &RetryContext{
		Config:  w.config,
		Attempt: 0,
	}

	// Keepalive messages are sent synchronously before backoff. A background
	// ticker would race with the handler, which also writes to httpW.
	keepalive := NewKeepaliveWriter(httpW, w.config.KeepaliveInterval)
	if keepalive != nil {
		rc.Keepalive = keepalive
	}

	// Retry loop
	for {
		metrics.TotalAttempts++

		// Execute the stream function
		err := streamFunc(ctx, httpW)

		if err == nil {
			// Success!
			metrics.SuccessAttempt = rc.Attempt
			metrics.TotalRetries = rc.Attempt
			w.logger.Info("stream completed successfully",
				"attempt", rc.Attempt+1,
				"total_retries", rc.Attempt)
			return metrics, nil
		}

		// Record failure
		metrics.LastError = err

		// Check if we should retry
		if !rc.ShouldRetry(err) {
			classify := ClassifyError(err)
			w.logger.Warn("stream failed with non-retriable error",
				"error", err,
				"retriable", classify.Retriable,
				"reason", classify.Reason,
				"attempt", rc.Attempt+1)
			metrics.TotalRetries = rc.Attempt
			return metrics, err
		}

		// Log retry decision
		classify := ClassifyError(err)
		w.logger.Info("stream failed with retriable error, retrying",
			"error", err,
			"reason", classify.Reason,
			"attempt", rc.Attempt+1,
			"next_attempt", rc.Attempt+2)

		// 2026-08-14 V3.2 (BE-B1): record state transition for the retry.
		// nil-safe — no logger wired (DB disabled / test mode) → no-op.
		// Pulled from X-Request-Id header so the row joins request_logs.request_id.
		// ADR-V3-102: reuse the existing request_id, never mint a new one.
		// Retry transitions are emitted only after the inner handler's trusted key
		// verifier has populated the request-scoped tenant carrier.
		requestID := requestIDFromCtx(ctx)
		tenantID := authenticatedTenantFromCtx(ctx)
		if requestID != "" && tenantID != "" {
			dispatch.LogRetryGlobal(requestID, tenantID, rc.Attempt+1, classify.Reason,
				map[string]any{
					"next_attempt": rc.Attempt + 2,
					"retriable":    classify.Retriable,
					"reason":       classify.Reason,
				})
		}

		// Sleep with backoff (and send keepalive notification)
		if err := rc.Sleep(ctx); err != nil {
			// Context canceled during sleep
			w.logger.Info("retry canceled by context", "error", err)
			metrics.TotalRetries = rc.Attempt
			return metrics, err
		}

		// Increment attempt counter for next iteration
		rc.Attempt++
	}
}

// Metrics returns the latest completed execution metrics.
// Prefer ExecuteWithMetrics when the caller needs metrics for a specific
// request. This method is safe for concurrent execution, but another request
// may replace the snapshot before it is read.
func (w *Wrapper) Metrics() WrapperMetrics {
	w.metricsMu.RLock()
	defer w.metricsMu.RUnlock()
	return w.metrics
}

// WrapHTTPError is a helper to wrap HTTP response errors with status code
// for proper classification.
func WrapHTTPError(resp *http.Response, baseErr error) error {
	if resp == nil {
		return baseErr
	}

	if resp.StatusCode >= 400 {
		return &HTTPError{
			StatusCode: resp.StatusCode,
			Err:        baseErr,
		}
	}

	return baseErr
}

// ExtractHTTPStatus attempts to extract HTTP status code from an error.
// Returns 0 if the error doesn't contain status code information.
func ExtractHTTPStatus(err error) int {
	if err != nil {
		// Check if it's our HTTPError
		if e, ok := err.(*HTTPError); ok {
			return e.StatusCode
		}
		// Check wrapped error
		if e, ok := err.(interface{ Unwrap() error }); ok {
			return ExtractHTTPStatus(e.Unwrap())
		}
	}
	return 0
}

// IsRetriable is a convenience function to check if an error is retriable.
func IsRetriable(err error) bool {
	classified := ClassifyError(err)
	return classified.Retriable
}

// StreamExecutor is an interface for stream execution with retry support.
// Implementations can wrap existing executor logic.
type StreamExecutor interface {
	// ExecuteStream runs a streaming request with automatic retry and keepalive.
	ExecuteStream(ctx context.Context, w http.ResponseWriter, req *http.Request) error
}

// DefaultStreamExecutor wraps an existing stream handler with retry logic.
type DefaultStreamExecutor struct {
	wrapper *Wrapper
	handler http.Handler
}

// NewDefaultStreamExecutor creates a stream executor that wraps an http.Handler.
//
// Typical wiring in cmd/gateway/main.go:
//
//	srCfg := streamretry.Config{...}                       // from app config
//	wrapped := streamretry.NewDefaultStreamExecutor(chatHandler, srCfg)
//	mux.Handle("/v1/chat/completions", wrapped)            // wrapped implements http.Handler
//
// See README.md "在 ChatHandler 中接入" for the full integration example.
func NewDefaultStreamExecutor(handler http.Handler, config Config) *DefaultStreamExecutor {
	return &DefaultStreamExecutor{
		wrapper: NewWrapper(config, nil),
		handler: handler,
	}
}

// ServeHTTP makes *DefaultStreamExecutor implement http.Handler. The request
// context is forwarded to ExecuteStream so context cancellation (client
// disconnect, request timeout) propagates naturally and stops the retry loop.
//
// 2026-08-14 V3.2: extract X-Request-Id from the inbound header and stash
// it in the context so the retry loop can call LogRetryGlobal with the
// existing request id (ADR-V3-102 — never mint a new id).
func (e *DefaultStreamExecutor) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := withTenantCarrier(req.Context())
	ctx = withRequestID(ctx, strings.TrimSpace(req.Header.Get("X-Request-Id")))
	_ = e.ExecuteStream(ctx, w, req.WithContext(ctx))
}

// ExecuteStream implements the StreamExecutor interface.
func (e *DefaultStreamExecutor) ExecuteStream(ctx context.Context, w http.ResponseWriter, req *http.Request) error {
	_, err := e.ExecuteStreamWithMetrics(ctx, w, req)
	return err
}

// ExecuteStreamWithMetrics returns metrics for the current request without
// sharing mutable per-request state between concurrent handlers.
func (e *DefaultStreamExecutor) ExecuteStreamWithMetrics(ctx context.Context, w http.ResponseWriter, req *http.Request) (WrapperMetrics, error) {
	body, streaming := snapshotRequestBody(req)
	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		if body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		// Wrap the handler in a recorder to capture errors
		rec := &errorRecorder{ResponseWriter: w}
		e.handler.ServeHTTP(rec, req.WithContext(ctx))
		if rec.err != nil && rec.committed {
			return &retryBlockedError{err: rec.err}
		}
		return rec.err
	}

	if !streaming {
		rec := &errorRecorder{ResponseWriter: w}
		e.handler.ServeHTTP(rec, req.WithContext(ctx))
		metrics := WrapperMetrics{TotalAttempts: 1, SuccessAttempt: -1}
		if rec.err == nil {
			metrics.SuccessAttempt = 0
		} else {
			metrics.LastError = rec.err
		}
		e.wrapper.setMetrics(metrics)
		recordExecutionMetrics(metrics, rec.err)
		return metrics, rec.err
	}

	// Request-survival ownership (docs/修订0811/18 §5, docs/修订0811/19
	// SR-W0): when the survival coordinator owns this request's retries, this
	// wrapper must execute the handler exactly once and never loop — nested
	// ownership would multiply provider calls and cost. The owner is frozen
	// at the HTTP boundary via context; hot-toggling the survival flag does
	// not affect in-flight requests.
	if retryowner.OwnerFrom(ctx) == retryowner.Survival {
		metrics := WrapperMetrics{TotalAttempts: 1, SuccessAttempt: -1}
		err := streamFunc(ctx, w)
		if err == nil {
			metrics.SuccessAttempt = 0
		} else {
			metrics.LastError = err
		}
		e.wrapper.setMetrics(metrics)
		recordExecutionMetrics(metrics, err)
		return metrics, err
	}

	return e.wrapper.ExecuteWithMetrics(ctx, w, streamFunc)
}

// snapshotRequestBody reads and restores a request body before the first
// attempt. Each retry then receives a fresh reader, which is required because
// the wrapped handlers consume r.Body.
func snapshotRequestBody(req *http.Request) ([]byte, bool) {
	if req == nil || req.Body == nil {
		// Preserve the original executor behavior for direct callers that do
		// not provide a body: treat the request as a stream attempt.
		return nil, true
	}
	original := req.Body
	body, err := io.ReadAll(original)
	_ = original.Close()
	if err != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		return body, false
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) == 0 {
		return body, true
	}

	var envelope struct {
		Stream bool `json:"stream"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return body, false
	}
	return body, envelope.Stream
}

func (w *Wrapper) setMetrics(metrics WrapperMetrics) {
	w.metricsMu.Lock()
	w.metrics = metrics
	w.metricsMu.Unlock()
}

// errorRecorder captures errors from http.Handler execution. The
// `committed` flag tracks whether the handler has started a successful
// streaming response (2xx + body bytes). Once committed, retrying would
// corrupt the stream. Error status codes (4xx/5xx) without body bytes
// are NOT committed — they are retriable.
type errorRecorder struct {
	http.ResponseWriter
	err       error
	committed bool
}

// Flush preserves SSE behavior through the retry wrapper.
func (r *errorRecorder) Flush() {
	_ = r.FlushError()
}

// FlushError preserves connection errors through the retry wrapper.
func (r *errorRecorder) FlushError() error {
	if !r.committed {
		return nil
	}
	if flusher, ok := r.ResponseWriter.(interface{ FlushError() error }); ok {
		return flusher.FlushError()
	}
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// Unwrap exposes the underlying writer to middleware that needs to inspect it.
func (r *errorRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// WriteHeader captures error status codes. Error statuses (>= 400) are
// retriable and do NOT mark the writer committed. Success statuses (2xx)
// mark the writer committed since retrying would change the status code.
func (r *errorRecorder) WriteHeader(statusCode int) {
	r.committed = true
	if statusCode >= 400 {
		r.err = &HTTPError{
			StatusCode: statusCode,
			Err:        fmt.Errorf("HTTP %d", statusCode),
		}
	}
	r.ResponseWriter.WriteHeader(statusCode)
}

// Write captures write errors. Once body bytes are written (whether
// part of a success or error response), the response is committed to
// the client and retrying would produce a corrupt response.
func (r *errorRecorder) Write(p []byte) (int, error) {
	r.committed = true
	n, err := r.ResponseWriter.Write(p)
	if err != nil && r.err == nil {
		// Classify write error (could be connection drop)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			r.err = err
		}
	}
	return n, err
}
