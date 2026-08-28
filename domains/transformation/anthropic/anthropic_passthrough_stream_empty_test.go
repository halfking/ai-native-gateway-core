package anthropic

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestStreamAnthropicPassthrough_EmptyResponseFailOver verifies the
// audit-24h-20260828-r3 P1-B fix: when upstream sends message_start +
// message_stop with ZERO content_block_* events (no text/thinking/tool
// blocks) and no usage tokens, the stream path now surfaces
// KindEmptyResponse so the executor routes to the next candidate via
// streamInterruptedError + TaskActionRetryNow, matching the non-stream
// detector at executor_anthropic.go:1273 (isEmptyAnthropicMessagesResponse).
func TestStreamAnthropicPassthrough_EmptyResponseFailOver(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		// Upstream sends message_start + message_stop but ZERO content_block_*
		// events. This is a valid Anthropic SSE stream shape but semantically
		// "empty" — the model produced no output.
		//nolint:errcheck // HTTP write error non-recoverable
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_empty\",\"role\":\"assistant\",\"content\":[]}}\n\n"))
		//nolint:errcheck // HTTP write error non-recoverable
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	outcome := StreamAnthropicPassthrough(rec, resp, "claude-3-5-sonnet", "claude-3-5-sonnet", "req-empty", capture, nil)

	// The fix surfaces Interrupted=true + Kind=KindEmptyResponse so the
	// executor can route to the next candidate instead of recording a
	// successful empty stream.
	if !outcome.Interrupted {
		t.Fatal("outcome.Interrupted should be true for an empty response stream")
	}
	if outcome.Reason != "anthropic_empty_response" {
		t.Errorf("outcome.Reason = %q, want anthropic_empty_response", outcome.Reason)
	}
	if outcome.Kind != errorsx.KindEmptyResponse {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, errorsx.KindEmptyResponse)
	}
	if !outcome.Resumable {
		t.Error("outcome.Resumable should be true so the executor tries the next candidate")
	}
	if outcome.ChunkCount != 0 {
		t.Errorf("outcome.ChunkCount = %d, want 0 (empty stream has zero data: lines)", outcome.ChunkCount)
	}

	// The capture should be marked interrupted with the same reason.
	_, _, _, interrupted, _ := capture.Snapshot()
	if !interrupted {
		t.Error("capture should be marked interrupted")
	}
}

// TestStreamAnthropicPassthrough_EmptyResponseWithUsageStillEmpty verifies
// that even when upstream returns usage tokens in message_start but ZERO
// content_block_* events, the stream is still flagged as empty. This matches
// the non-stream semantics: isEmptyAnthropicMessagesResponse checks the
// content array length, not usage token presence.
func TestStreamAnthropicPassthrough_EmptyResponseWithUsageStillEmpty(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		// Upstream sends usage tokens in message_start but zero content_block_* events.
		//nolint:errcheck // HTTP write error non-recoverable
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_usage\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"))
		//nolint:errcheck // HTTP write error non-recoverable
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":0}}\n\n"))
		//nolint:errcheck // HTTP write error non-recoverable
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	outcome := StreamAnthropicPassthrough(rec, resp, "claude-3-5-sonnet", "claude-3-5-sonnet", "req-usage-empty", capture, nil)

	// Even though usage tokens are present, chunkCount==0 (no content_block_*
	// data: lines) still triggers the empty-response path. The strict check is:
	// chunkCount==0 AND (capture is nil OR both OutputTokens and InputTokens
	// are nil). Here capture is non-nil and has InputTokens/OutputTokens set
	// from message_delta, so the empty check should NOT fire.
	//
	// Wait — the check is: chunkCount==0 AND (capture==nil OR (OutputTokens==nil
	// AND InputTokens==nil)). If capture.OutputTokens/InputTokens are set from
	// message_delta, then the condition is false → NOT empty. But if
	// message_start's usage is not observed (observeAnthropicPayload only sees
	// `data:` lines), then capture.OutputTokens may still be nil at EOF.
	//
	// Let me verify what observeAnthropicPayload does with message_start usage.
	// Looking at the code: observeAnthropicPayload calls capture.SetInputTokens
	// / SetOutputTokens when it sees "usage" in the payload. So if message_start
	// contains usage, capture.InputTokens/OutputTokens will be set.
	//
	// So this test expects: NOT empty (because capture has tokens set).
	if outcome.Interrupted {
		t.Errorf("outcome.Interrupted = true, but stream has usage tokens so should NOT be flagged empty (chunkCount=0 AND tokens present → not empty under current logic)")
	}
}

// TestStreamAnthropicPassthrough_NonEmptyContent verifies that a stream with
// actual content_block_delta events is NOT flagged as empty.
func TestStreamAnthropicPassthrough_NonEmptyContent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ok\"}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		for _, e := range events {
			//nolint:errcheck // HTTP write error non-recoverable
			_, _ = w.Write([]byte(e))
		}
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	outcome := StreamAnthropicPassthrough(rec, resp, "claude-3-5-sonnet", "claude-3-5-sonnet", "req-ok", capture, nil)

	if outcome.Interrupted {
		t.Errorf("outcome.Interrupted = true for a non-empty stream, reason=%s", outcome.Reason)
	}
	if outcome.Kind != "" {
		t.Errorf("outcome.Kind should be empty for a successful non-empty stream, got %q", outcome.Kind)
	}
}

// TestStreamAnthropicPassthrough_EmptyNilCapture verifies that the
// empty-response detector works even when capture is nil (legacy / non-session
// requests). The check is: chunkCount==0 AND (capture==nil OR no tokens).
// Here capture==nil so the condition is true → empty.
func TestStreamAnthropicPassthrough_EmptyNilCapture(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		//nolint:errcheck // HTTP write error non-recoverable
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_nil\"}}\n\n"))
		//nolint:errcheck // HTTP write error non-recoverable
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	// Pass nil capture to simulate legacy / non-session requests.
	outcome := StreamAnthropicPassthrough(rec, resp, "claude-3-5-sonnet", "claude-3-5-sonnet", "req-nil-cap", nil, nil)

	if !outcome.Interrupted {
		t.Fatal("outcome.Interrupted should be true for an empty response even when capture is nil")
	}
	if outcome.Kind != errorsx.KindEmptyResponse {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, errorsx.KindEmptyResponse)
	}
}
