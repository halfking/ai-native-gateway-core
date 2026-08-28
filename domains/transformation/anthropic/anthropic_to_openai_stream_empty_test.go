package anthropic

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestAnthropicToOpenAIStream_EmptyResponseFailOver verifies the
// audit-24h-20260828-r3 P1-B parity fix in the Q3 translator:
// when upstream sends message_start + message_stop with ZERO
// content_block_* events and no usage tokens, the stream now surfaces
// KindEmptyResponse so the executor routes to the next candidate via
// streamInterruptedError + TaskActionRetryNow, matching the non-stream
// detector at executor_anthropic.go:1273 and the passthrough detector
// at anthropic_passthrough_stream.go:147-178.
func TestAnthropicToOpenAIStream_EmptyResponseFailOver(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		// message_start only carries usage.input_tokens=0 — no content.
		//nolint:errcheck // HTTP write error non-recoverable
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_q3_empty\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n"))
		// message_delta with output_tokens=0 keeps usage empty.
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
	outcome := StreamAnthropicSSEToOpenAI(rec, resp, "gpt-4o", "claude-3-5-sonnet", "req-q3-empty", capture, nil)

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
}

// TestAnthropicToOpenAIStream_EmptyResponseWithUsageStillEmpty verifies
// the usage-only empty case: when upstream reports only input tokens
// but emits ZERO content_block_* events AND no output tokens, the stream
// is still flagged as empty. The check is:
//
//	IsAnthropicStreamEmpty(emittedContent, inputTokens, outputTokens)
//	  == !emittedContent && inputTokens == 0 && outputTokens == 0
//
// Mirrors the r3 raw passthrough semantics at
// anthropic_passthrough_stream.go:147-178: usage tokens present (either
// input OR output) make the stream non-empty, because the model produced
// observable work. A stream with zero content AND zero output tokens is
// a degenerate shape (upstream bug, NIM 13% empty burst, etc.) and must
// fail over. Note that the r3 raw passthrough writes tokens directly
// (no `>0` filter), whereas the Q3 IR parser drops output_tokens:0 (see
// anthropic_to_openai_stream.go:280) — both paths converge on the same
// IsAnthropicStreamEmpty result for this fixture.
func TestAnthropicToOpenAIStream_EmptyResponseWithUsageStillEmpty(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		// Both input_tokens and output_tokens are 0 — the upstream reported
		// an empty message with no semantic content. This is the
		// "model produced nothing observable" failure mode.
		//nolint:errcheck // HTTP write error non-recoverable
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_q3_usage\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n"))
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
	outcome := StreamAnthropicSSEToOpenAI(rec, resp, "gpt-4o", "claude-3-5-sonnet", "req-q3-usage-empty", capture, nil)

	if !outcome.Interrupted {
		t.Fatal("outcome.Interrupted should be true for zero-token zero-content stream")
	}
	if outcome.Kind != errorsx.KindEmptyResponse {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, errorsx.KindEmptyResponse)
	}
}

// TestAnthropicToOpenAIStream_NonEmptyContent verifies that a stream
// with actual content_block_delta events is NOT flagged as empty.
func TestAnthropicToOpenAIStream_NonEmptyContent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_q3_ok\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\n",
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
	outcome := StreamAnthropicSSEToOpenAI(rec, resp, "gpt-4o", "claude-3-5-sonnet", "req-q3-ok", capture, nil)

	if outcome.Interrupted {
		t.Errorf("outcome.Interrupted = true for a non-empty stream, reason=%s", outcome.Reason)
	}
	if outcome.Kind != "" {
		t.Errorf("outcome.Kind should be empty for a successful non-empty stream, got %q", outcome.Kind)
	}
}

// TestAnthropicToOpenAIStream_EmptyNilCapture verifies that the
// empty-response detector works even when capture is nil (legacy /
// non-session requests).
func TestAnthropicToOpenAIStream_EmptyNilCapture(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		//nolint:errcheck // HTTP write error non-recoverable
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_q3_nil\",\"role\":\"assistant\",\"content\":[]}}\n\n"))
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
	outcome := StreamAnthropicSSEToOpenAI(rec, resp, "gpt-4o", "claude-3-5-sonnet", "req-q3-nil-cap", nil, nil)

	if !outcome.Interrupted {
		t.Fatal("outcome.Interrupted should be true for an empty response even when capture is nil")
	}
	if outcome.Kind != errorsx.KindEmptyResponse {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, errorsx.KindEmptyResponse)
	}
}
