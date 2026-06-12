package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/audit"
)

func TestStreamAnthropicPassthrough_ForwardsBytes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_x\"}}\n\n"))
		w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	outcome := StreamAnthropicPassthrough(rec, resp, "MiniMax-M2.7", "MiniMax-M2.7", "req-1", capture)

	if outcome.Interrupted {
		t.Errorf("stream interrupted: %s", outcome.Reason)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "msg_x") {
		t.Errorf("body should contain message_start payload; got: %s", body)
	}
	if !strings.Contains(body, "message_stop") {
		t.Errorf("body should contain message_stop; got: %s", body)
	}
	if !strings.HasPrefix(body, "event: ") {
		t.Errorf("body should start with SSE event line; got prefix: %q", body[:50])
	}
}

func TestStreamAnthropicPassthrough_DetectsThinking(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"deep thought\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
		}
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	_ = StreamAnthropicPassthrough(rec, resp, "MiniMax-M2.7", "MiniMax-M2.7", "req-2", capture)
	if !capture.HasThinking {
		t.Error("expected capture.HasThinking=true when thinking block present")
	}
}

func TestStreamAnthropicPassthrough_AccumulatesUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":42,\"output_tokens\":0}}}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":17}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
		}
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	_ = StreamAnthropicPassthrough(rec, resp, "MiniMax-M2.7", "MiniMax-M2.7", "req-3", capture)
	if capture.InputTokens == nil || *capture.InputTokens != 42 {
		t.Errorf("InputTokens = %v, want 42", capture.InputTokens)
	}
	if capture.OutputTokens == nil || *capture.OutputTokens != 17 {
		t.Errorf("OutputTokens = %v, want 17", capture.OutputTokens)
	}
}
