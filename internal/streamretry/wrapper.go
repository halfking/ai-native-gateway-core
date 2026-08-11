package streamretry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
)

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

	// Start keepalive if response writer supports flushing
	keepalive := NewKeepaliveWriter(httpW, w.config.KeepaliveInterval)
	if keepalive != nil {
		rc.Keepalive = keepalive
		keepaliveCtx, keepaliveCancel := context.WithCancel(ctx)
		defer keepaliveCancel()
		go keepalive.Start(keepaliveCtx)
		defer keepalive.Stop()
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
			classified := rc.LastError.(*RetryableError)
			w.logger.Warn("stream failed with non-retriable error",
				"error", err,
				"retriable", classified.Retriable,
				"reason", classified.Reason,
				"attempt", rc.Attempt+1)
			metrics.TotalRetries = rc.Attempt
			return metrics, err
		}

		// Log retry decision
		classified := rc.LastError.(*RetryableError)
		w.logger.Info("stream failed with retriable error, retrying",
			"error", err,
			"reason", classified.Reason,
			"attempt", rc.Attempt+1,
			"next_attempt", rc.Attempt+2)

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
// This is the entry point used by net/http.ServeMux when the executor is
// registered directly as a route handler.
func (e *DefaultStreamExecutor) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	_ = e.ExecuteStream(req.Context(), w, req)
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

// errorRecorder captures errors from http.Handler execution.
type errorRecorder struct {
	http.ResponseWriter
	err error
}

// Flush preserves SSE behavior through the retry wrapper.
func (r *errorRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap exposes the underlying writer to middleware that needs to inspect it.
func (r *errorRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// WriteHeader captures error status codes.
func (r *errorRecorder) WriteHeader(statusCode int) {
	if statusCode >= 400 {
		r.err = &HTTPError{
			StatusCode: statusCode,
			Err:        fmt.Errorf("HTTP %d", statusCode),
		}
	}
	r.ResponseWriter.WriteHeader(statusCode)
}

// Write captures write errors.
func (r *errorRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseWriter.Write(p)
	if err != nil && r.err == nil {
		// Classify write error (could be connection drop)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			r.err = err
		}
	}
	return n, err
}
