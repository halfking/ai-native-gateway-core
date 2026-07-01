package streaming_test

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming"                     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// TestRequestLogContext_UpstreamDiagnostics tests the new upstream diagnostic fields
func TestRequestLogContext_UpstreamDiagnostics(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*streaming.RequestLogContext)
		validate func(*testing.T, *telemetry.RequestLogEntry)
	}{
		{
			name: "upstream_status_code is captured",
			setup: func(ctx *streaming.RequestLogContext) {
				ctx.SetUpstreamStatus(401)
			},
			validate: func(t *testing.T, entry *telemetry.RequestLogEntry) {
				if entry.UpstreamStatusCode == nil {
					t.Error("UpstreamStatusCode should not be nil")
					return
				}
				if *entry.UpstreamStatusCode != 401 {
					t.Errorf("Expected UpstreamStatusCode 401, got %d", *entry.UpstreamStatusCode)
				}
			},
		},
		{
			name: "client_timeout is captured",
			setup: func(ctx *streaming.RequestLogContext) {
				ctx.SetClientTimeout(true)
			},
			validate: func(t *testing.T, entry *telemetry.RequestLogEntry) {
				if entry.ClientTimeout == nil {
					t.Error("ClientTimeout should not be nil")
					return
				}
				if !*entry.ClientTimeout {
					t.Error("Expected ClientTimeout to be true")
				}
			},
		},
		{
			name: "client_endpoint is captured",
			setup: func(ctx *streaming.RequestLogContext) {
				ctx.SetClientEndpoint("/v1/chat/completions")
			},
			validate: func(t *testing.T, entry *telemetry.RequestLogEntry) {
				if entry.ClientEndpoint == nil {
					t.Error("ClientEndpoint should not be nil")
					return
				}
				if *entry.ClientEndpoint != "/v1/chat/completions" {
					t.Errorf("Expected ClientEndpoint /v1/chat/completions, got %s", *entry.ClientEndpoint)
				}
			},
		},
		{
			name: "stream_chunk_errors is incremented",
			setup: func(ctx *streaming.RequestLogContext) {
				ctx.IncrementStreamChunkErrors()
				ctx.IncrementStreamChunkErrors()
				ctx.IncrementStreamChunkErrors()
			},
			validate: func(t *testing.T, entry *telemetry.RequestLogEntry) {
				if entry.StreamChunkErrors == nil {
					t.Error("StreamChunkErrors should not be nil")
					return
				}
				if *entry.StreamChunkErrors != 3 {
					t.Errorf("Expected StreamChunkErrors 3, got %d", *entry.StreamChunkErrors)
				}
			},
		},
		{
			name: "stream_chunks_sent is incremented",
			setup: func(ctx *streaming.RequestLogContext) {
				for i := 0; i < 10; i++ {
					ctx.IncrementStreamChunksSent()
				}
			},
			validate: func(t *testing.T, entry *telemetry.RequestLogEntry) {
				if entry.StreamChunksSent == nil {
					t.Error("StreamChunksSent should not be nil")
					return
				}
				if *entry.StreamChunksSent != 10 {
					t.Errorf("Expected StreamChunksSent 10, got %d", *entry.StreamChunksSent)
				}
			},
		},
		{
			name: "all diagnostic fields together",
			setup: func(ctx *streaming.RequestLogContext) {
				ctx.SetUpstreamStatus(429)
				ctx.SetClientEndpoint("/v1/chat/completions")
				ctx.IncrementStreamChunksSent()
				ctx.IncrementStreamChunksSent()
				ctx.IncrementStreamChunkErrors()
			},
			validate: func(t *testing.T, entry *telemetry.RequestLogEntry) {
				if entry.UpstreamStatusCode == nil || *entry.UpstreamStatusCode != 429 {
					t.Error("UpstreamStatusCode should be 429")
				}
				if entry.ClientEndpoint == nil || *entry.ClientEndpoint != "/v1/chat/completions" {
					t.Error("ClientEndpoint should be /v1/chat/completions")
				}
				if entry.StreamChunksSent == nil || *entry.StreamChunksSent != 2 {
					t.Error("StreamChunksSent should be 2")
				}
				if entry.StreamChunkErrors == nil || *entry.StreamChunkErrors != 1 {
					t.Error("StreamChunkErrors should be 1")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a minimal RequestLogContext for testing
			ctx := &streaming.RequestLogContext{
				RequestID: "test-request-id",
				StartTime: time.Now(),
			}

			// Setup the context with test data
			tt.setup(ctx)

			// Build failure entry (this is what gets written to the database)
			entry := ctx.BuildFailureEntry("test_error", "test error message", nil, nil)

			// Validate the entry contains the expected fields
			tt.validate(t, entry)
		})
	}
}

// TestRequestLogContext_NilSafety tests that setters are nil-safe
func TestRequestLogContext_NilSafety(t *testing.T) {
	var ctx *streaming.RequestLogContext // nil context

	// These should not panic
	ctx.SetUpstreamStatus(401)
	ctx.SetClientTimeout(true)
	ctx.SetClientEndpoint("/test")
	ctx.IncrementStreamChunkErrors()
	ctx.IncrementStreamChunksSent()

	// No assertions needed - we're just testing that these don't panic
}

// TestRequestLogContext_ZeroValues tests edge cases with zero values
func TestRequestLogContext_ZeroValues(t *testing.T) {
	ctx := &streaming.RequestLogContext{
		RequestID: "test-request-id",
		StartTime: time.Now(),
	}

	// Zero or negative status code should not be set
	ctx.SetUpstreamStatus(0)
	ctx.SetUpstreamStatus(-1)

	entry := ctx.BuildFailureEntry("test_error", "test error message", nil, nil)

	if entry.UpstreamStatusCode != nil {
		t.Error("UpstreamStatusCode should be nil for zero/negative values")
	}

	// Empty endpoint should not be set
	ctx.SetClientEndpoint("")
	entry = ctx.BuildFailureEntry("test_error", "test error message", nil, nil)

	if entry.ClientEndpoint != nil {
		t.Error("ClientEndpoint should be nil for empty string")
	}

	// False timeout should not set the field
	ctx.SetClientTimeout(false)
	entry = ctx.BuildFailureEntry("test_error", "test error message", nil, nil)

	if entry.ClientTimeout != nil {
		t.Error("ClientTimeout should be nil when set to false")
	}
}

// TestRequestLogContext_StreamChunksSentDefaultValue tests that StreamChunksSent
// defaults to 0 (not nil) when no chunks were sent
func TestRequestLogContext_StreamChunksSentDefaultValue(t *testing.T) {
	ctx := &streaming.RequestLogContext{
		RequestID: "test-request-id",
		StartTime: time.Now(),
	}

	entry := ctx.BuildFailureEntry("test_error", "test error message", nil, nil)

	// StreamChunksSent should be set to 0, not nil
	if entry.StreamChunksSent == nil {
		t.Error("StreamChunksSent should not be nil (should default to 0)")
		return
	}
	if *entry.StreamChunksSent != 0 {
		t.Errorf("Expected StreamChunksSent to be 0, got %d", *entry.StreamChunksSent)
	}
}

// TestStreamChunksSentFromLogCtxNilSafe verifies the helpers added on
// 2026-07-01 to keep BuildSuccessEntry from leaving StreamChunksSent nil.
// Without these defaults the INSERT crashed with SQLSTATE 23502 against
// request_logs_2026_07 (NOT NULL column added by migration 320) and
// stopped every new row from being written on 184.
//
// Regression test for the P0 root cause documented at
// docs/2026-07-01-unknown-error-root-cause.md.
func TestStreamChunksSentFromLogCtxNilSafe(t *testing.T) {
	if got := streaming.StreamChunksSentFromLogCtxForTest(nil); got != 0 {
		t.Errorf("nil logCtx → want 0, got %d", got)
	}

	// Negative values are sentinel / uninitialised → coerce to 0.
	ctx := &streaming.RequestLogContext{RequestID: "x", StartTime: time.Now()}
	ctx.StreamChunksSent = -5
	if got := streaming.StreamChunksSentFromLogCtxForTest(ctx); got != 0 {
		t.Errorf("negative count → want 0, got %d", got)
	}

	// Positive values are passed through untouched.
	ctx.StreamChunksSent = 42
	if got := streaming.StreamChunksSentFromLogCtxForTest(ctx); got != 42 {
		t.Errorf("positive count → want 42, got %d", got)
	}

	// Zero is also passed through (the column accepts 0 by default).
	ctx.StreamChunksSent = 0
	if got := streaming.StreamChunksSentFromLogCtxForTest(ctx); got != 0 {
		t.Errorf("zero → want 0, got %d", got)
	}
}

func TestStreamChunkErrorsFromLogCtxNilSafe(t *testing.T) {
	if got := streaming.StreamChunkErrorsFromLogCtxForTest(nil); got != 0 {
		t.Errorf("nil logCtx → want 0, got %d", got)
	}
	ctx := &streaming.RequestLogContext{RequestID: "x", StartTime: time.Now()}
	ctx.StreamChunkErrors = -1
	if got := streaming.StreamChunkErrorsFromLogCtxForTest(ctx); got != 0 {
		t.Errorf("negative count → want 0, got %d", got)
	}
	ctx.StreamChunkErrors = 7
	if got := streaming.StreamChunkErrorsFromLogCtxForTest(ctx); got != 7 {
		t.Errorf("positive count → want 7, got %d", got)
	}
}
