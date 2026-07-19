package streaming

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/config"
)

func TestStartPreStreamKeepalive_WritesInitialComment(t *testing.T) {
	rec := httptest.NewRecorder()
	psk, ok := startPreStreamKeepalive(rec, time.Hour)
	if !ok {
		t.Fatal("expected flusher-backed recorder")
	}
	psk.stop()

	if got := rec.Body.String(); !strings.HasPrefix(got, sseKeepaliveComment) {
		t.Fatalf("body = %q, want prefix %q", got, sseKeepaliveComment)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}
}

func TestWritePrewarmedStreamError_WritesSSEError(t *testing.T) {
	rec := httptest.NewRecorder()
	writePrewarmedStreamError(rec, "upstream request failed", "server_error", "provider_error")
	got := rec.Body.String()
	if !strings.Contains(got, `data: {"error":{`) {
		t.Fatalf("body = %q, want SSE error envelope", got)
	}
	if !strings.Contains(got, `"code":"provider_error"`) {
		t.Fatalf("body = %q, want provider_error code", got)
	}
	if !strings.Contains(got, `"message":"upstream request failed"`) {
		t.Fatalf("body = %q, want message", got)
	}
}

func TestPreStreamKeepalive_DisabledByDefault(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE", "")
	streamConfigStore.Store(config.NewStore(&config.Config{}))
	if got := currentStreamRuntimeConfig().enablePreStreamKeepalive; got {
		t.Fatal("enable_pre_stream_keepalive should default to false")
	}
}

func TestPreStreamKeepalive_EnabledViaEnv(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE", "true")
	streamConfigStore.Store(config.NewStore(&config.Config{}))
	if got := currentStreamRuntimeConfig().enablePreStreamKeepalive; !got {
		t.Fatal("enable_pre_stream_keepalive should be true when LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE=true")
	}
}

// TestPreStreamKeepalive_InitialCommentArrivesBeforeContent simulates the
// real handler flow: prewarm commits 200 text/event-stream + initial comment
// first, then the real stream body is appended later. The initial comment
// must come before the first content line so a client waiting on first-byte
// stops its timer the moment it sees the comment, and the subsequent content
// is delivered without a separate WriteHeader.
func TestPreStreamKeepalive_InitialCommentArrivesBeforeContent(t *testing.T) {
	rec := httptest.NewRecorder()
	psk, ok := startPreStreamKeepalive(rec, time.Hour)
	if !ok {
		t.Fatal("expected flusher-backed recorder")
	}

	// Simulate "stream is now ready": append a real chunk and then stop.
	const chunk = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"
	_, _ = rec.Body.WriteString(chunk)
	psk.stop()

	got := rec.Body.String()
	idxComment := strings.Index(got, sseKeepaliveComment)
	idxChunk := strings.Index(got, chunk)
	if idxComment < 0 {
		t.Fatalf("body = %q, missing keep-alive comment", got)
	}
	if idxChunk < 0 {
		t.Fatalf("body = %q, missing content chunk", got)
	}
	if idxComment >= idxChunk {
		t.Fatalf("comment index %d >= chunk index %d (comment must arrive first)", idxComment, idxChunk)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestPreStreamKeepalive_StopIsIdempotent ensures stop() can be called twice
// without panicking — the handler path calls stop() from OnStreamReady and
// again from the deferred cleanup, so a double stop must be a no-op.
func TestPreStreamKeepalive_StopIsIdempotent(t *testing.T) {
	rec := httptest.NewRecorder()
	psk, ok := startPreStreamKeepalive(rec, time.Hour)
	if !ok {
		t.Fatal("expected flusher-backed recorder")
	}
	psk.stop()
	psk.stop() // must not panic
}

// TestPreStreamKeepalive_PrewarmThenSSEError covers the "all credentials
// failed after prewarm" branch: the body must be a single SSE error envelope
// (no JSON, no extra WriteHeader) because prewarm already committed 200.
func TestPreStreamKeepalive_PrewarmThenSSEError(t *testing.T) {
	rec := httptest.NewRecorder()
	psk, ok := startPreStreamKeepalive(rec, time.Hour)
	if !ok {
		t.Fatal("expected flusher-backed recorder")
	}

	writePrewarmedStreamError(rec, "upstream request failed", "server_error", "provider_error")
	psk.stop()

	got := rec.Body.String()
	if !strings.HasPrefix(got, sseKeepaliveComment) {
		t.Fatalf("body must start with keep-alive comment, got %q", got)
	}
	if !strings.Contains(got, `data: {"error":`) {
		t.Fatalf("body = %q, want SSE error envelope", got)
	}
	// The error envelope must come AFTER the comment so the order is
	// [comment, error, ...], matching what an SSE parser would expect.
	idxComment := strings.Index(got, sseKeepaliveComment)
	idxError := strings.Index(got, `data: {"error":`)
	if idxComment >= idxError {
		t.Fatalf("comment index %d >= error index %d", idxComment, idxError)
	}
}
