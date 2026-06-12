package routing

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestAnthropicExecutor_BuildRequest_Passthrough(t *testing.T) {
	ae := &AnthropicExecutor{}
	cand := provider.Candidate{
		BaseURL:  "https://api.minimaxi.com/anthropic",
		Protocol: "anthropic-messages",
		APIKey:   "sk-cp-test",
	}
	body := []byte(`{"model":"MiniMax-M2.7","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`)

	req, err := ae.BuildRequest(cand, body, false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.URL.String() != "https://api.minimaxi.com/anthropic/v1/messages" {
		t.Errorf("URL = %q, want https://api.minimaxi.com/anthropic/v1/messages", req.URL.String())
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization header should be empty for Anthropic, got %q", got)
	}
	if got := req.Header.Get("x-api-key"); got != "sk-cp-test" {
		t.Errorf("x-api-key = %q, want sk-cp-test", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
	bodyBytes, _ := io.ReadAll(req.Body)
	if !strings.Contains(string(bodyBytes), `"MiniMax-M2.7"`) {
		t.Errorf("body should be unmodified, got: %s", string(bodyBytes))
	}
}

func TestAnthropicExecutor_StreamResponse_Passthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"MiniMax-M2.7\",\"usage\":{\"input_tokens\":12,\"output_tokens\":0}}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	ae := &AnthropicExecutor{}
	outcome := ae.StreamResponse(rec, resp)
	if outcome.Interrupted {
		t.Errorf("stream should not be interrupted: %s", outcome.Reason)
	}
	body := rec.Body.String()
	for _, expected := range []string{"message_start", "content_block_start", "content_block_delta", "message_stop"} {
		if !strings.Contains(body, expected) {
			t.Errorf("passthrough lost event %q\nfull body: %s", expected, body)
		}
	}
}

func TestAnthropicExecutor_CheckSoftMismatch(t *testing.T) {
	ae := &AnthropicExecutor{}
	mismatched, reason := ae.CheckSoftMismatch("MiniMax-XYZ", "MiniMax-M3")
	if !mismatched {
		t.Errorf("expected soft mismatch (minimax silent fallback)")
	}
	if reason == "" {
		t.Error("reason should be set for diagnostics")
	}
	mismatched, _ = ae.CheckSoftMismatch("MiniMax-M2.7", "MiniMax-M2.7")
	if mismatched {
		t.Error("matching models should not be flagged")
	}
}
