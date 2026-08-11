package streamretry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	config  Config
	metrics *WrapperMetrics
	logger  *slog.Logger
}

// NewWrapper creates a new retry wrapper with the given configuration.
func NewWrapper(config Config, logger *slog.Logger) *Wrapper {
	if logger == nil {
		logger = slog.Default()
	}
	return &Wrapper{
		config:  config,
		metrics: &WrapperMetrics{SuccessAttempt: -1},
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
	// Initialize metrics for this execution
	w.metrics = &WrapperMetrics{SuccessAttempt: -1}

	if !w.config.Enabled {
		// Fast path: retry disabled, execute once
		w.metrics.TotalAttempts = 1
		err := streamFunc(ctx, httpW)
		if err == nil {
			w.metrics.SuccessAttempt = 0
		} else {
			w.metrics.LastError = err
		}
		return err
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
		w.metrics.TotalAttempts++

		// Execute the stream function
		err := streamFunc(ctx, httpW)

		if err == nil {
			// Success!
			w.metrics.SuccessAttempt = rc.Attempt
			w.metrics.TotalRetries = rc.Attempt
			w.logger.Info("stream completed successfully",
				"attempt", rc.Attempt+1,
				"total_retries", rc.Attempt)
			return nil
		}

		// Record failure
		w.metrics.LastError = err

		// Check if we should retry
		if !rc.ShouldRetry(err) {
			classified := rc.LastError.(*RetryableError)
			w.logger.Warn("stream failed with non-retriable error",
				"error", err,
				"retriable", classified.Retriable,
				"reason", classified.Reason,
				"attempt", rc.Attempt+1)
			w.metrics.TotalRetries = rc.Attempt
			return err
		}

		// Increment attempt counter before checking max retries
		// (so we know how many retries we've done)
		retryCount := rc.Attempt + 1

		// Check if max retries exceeded (before next retry)
		if retryCount > w.config.MaxRetries {
			w.logger.Error("stream failed after max retries",
				"error", err,
				"max_retries", w.config.MaxRetries,
				"total_attempts", rc.Attempt+1)
			w.metrics.TotalRetries = rc.Attempt
			return fmt.Errorf("stream failed after %d retries: %w", rc.Attempt+1, err)
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
			w.metrics.TotalRetries = rc.Attempt
			return err
		}

		// Increment attempt counter for next iteration
		rc.Attempt++
	}
}

// Metrics returns the accumulated retry metrics.
func (w *Wrapper) Metrics() WrapperMetrics {
	return *w.metrics
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
func NewDefaultStreamExecutor(handler http.Handler, config Config) *DefaultStreamExecutor {
	return &DefaultStreamExecutor{
		wrapper: NewWrapper(config, nil),
		handler: handler,
	}
}

// ExecuteStream implements the StreamExecutor interface.
func (e *DefaultStreamExecutor) ExecuteStream(ctx context.Context, w http.ResponseWriter, req *http.Request) error {
	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		// Wrap the handler in a recorder to capture errors
		rec := &errorRecorder{ResponseWriter: w}
		e.handler.ServeHTTP(rec, req.WithContext(ctx))
		return rec.err
	}

	return e.wrapper.Execute(ctx, w, streamFunc)
}

// errorRecorder captures errors from http.Handler execution.
type errorRecorder struct {
	http.ResponseWriter
	err error
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
