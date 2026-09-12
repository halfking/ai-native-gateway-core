package anthropic

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R16 (2026-09-12): unknown content blocks in the Q3 stream translator.
//
// Regression 1: server_tool_use / container_upload blocks stream their
// input via input_json_delta just like tool_use. Without the orphan guard,
// those fragments pooled into a synthesized EMPTY-NAME tool call
// (`"function":{"name":""}`) at content_block_stop — a malformed OpenAI
// tool call delivered straight to the client.
//
// Regression 2 (deliberate semantics, locked): an unknown-block-only stream
// still classifies as empty at EOF. Failover to the next candidate is the
// user-beneficial behavior for streams with nothing representable on the
// wire, and resumable empty outcomes do not demote the provider — the
// unknown block types are recorded on the quality flags for observability.
func TestAnthropicToOpenAIStream_UnknownBlocksWithInputJSONDeltaDoNotBecomeToolCalls(t *testing.T) {
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_u1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"anthropic\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"searching..."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(anthropicSSE))
	}))
	defer upstream.Close()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", upstream.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to get upstream response: %v", err)
	}

	outcome := StreamAnthropicSSEToOpenAI(w, resp, "claude-opus-4-8", "claude-opus-4-8", "test-req-unknown-blocks", nil, nil)

	body := w.Body.String()
	if strings.Contains(body, `"name":""`) {
		t.Fatalf("empty-name tool call must never be emitted; body:\n%s", body)
	}
	if strings.Contains(body, `"tool_calls"`) {
		t.Fatalf("server_tool_use fragments must not surface as tool calls; body:\n%s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("representable text must complete the stream normally; body:\n%s", body)
	}
	if outcome.Interrupted {
		t.Fatalf("mixed unknown+text stream must not be interrupted, got reason=%s", outcome.Reason)
	}
}

func TestAnthropicToOpenAIStream_UnknownOnlyStreamStillClassifiesEmpty(t *testing.T) {
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_u2","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":3}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"container_upload","id":"ctn_1"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(anthropicSSE))
	}))
	defer upstream.Close()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", upstream.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to get upstream response: %v", err)
	}

	outcome := StreamAnthropicSSEToOpenAI(w, resp, "claude-opus-4-8", "claude-opus-4-8", "test-req-unknown-only", nil, nil)

	if !outcome.Interrupted || outcome.Reason != "anthropic_empty_response" {
		t.Fatalf("unknown-only stream must keep empty classification (failover preserved), got interrupted=%v reason=%s kind=%s",
			outcome.Interrupted, outcome.Reason, outcome.Kind)
	}
	if outcome.Kind != "empty_response" {
		t.Fatalf("expected KindEmptyResponse taxonomy, got %q", outcome.Kind)
	}
}
