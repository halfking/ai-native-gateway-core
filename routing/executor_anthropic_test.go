package routing

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestExecutor_DispatchesAnthropic verifies the Q4 dispatcher in
// executor.go: when a candidate has protocol=anthropic-messages, the
// Executor.executeAnthropic() method must actually send the request to
// the upstream with x-api-key auth (not Bearer) and the /v1/messages
// path. The upstream stub verifies these invariants.
//
// This is the integration test that closes Phase 2: the dispatcher no
// longer just returns "not yet implemented".
func TestExecutor_DispatchesAnthropic(t *testing.T) {
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	e := &Executor{
		Circuit:         cm,
		Limiter:         lim,
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}

	// Stub upstream: capture headers + path, return Anthropic-shaped JSON.
	var seenAPIKey, seenPath, seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAPIKey = r.Header.Get("x-api-key")
		seenAuth = r.Header.Get("Authorization")
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M2.7","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1},"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(""))
	r.Header.Set("X-Request-Id", "test-req-1")
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:             rec,
		R:             r,
		BodyBytes:     []byte(`{"model":"MiniMax-M2.7","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		ClientModel:   "MiniMax-M2.7",
		OutboundModel: "MiniMax-M2.7",
	}
	cand := provider.Candidate{
		ProviderID:   14,
		CredentialID: 6,
		BaseURL:      srv.URL,
		Protocol:     "anthropic-messages",
		APIKey:       "sk-cp-test",
	}

	_, err := e.executeAnthropic(params, cand, 2, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeAnthropic: %v", err)
	}
	if seenAPIKey != "sk-cp-test" {
		t.Errorf("upstream saw x-api-key = %q, want sk-cp-test", seenAPIKey)
	}
	if seenAuth != "" {
		t.Errorf("upstream saw Authorization = %q, want empty (Anthropic uses x-api-key)", seenAuth)
	}
	if seenPath != "/v1/messages" {
		t.Errorf("upstream saw path = %q, want /v1/messages", seenPath)
	}
	// Verify the client got a 200 back.
	if rec.Code != 200 {
		t.Errorf("client got status %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"msg_1"`) {
		t.Errorf("client body should contain Anthropic message; got: %s", rec.Body.String())
	}
}

func TestPrepareAnthropicRequestBody_CompressesOpenAIClient(t *testing.T) {
	ctxWin := 50
	long := strings.Repeat("a", 200)
	openaiBody := []byte(`{"model":"minimax-m3","messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"latest"}
	]}`)

	e := &Executor{
		ChatToAnthropic: func(body []byte) ([]byte, error) {
			var req struct {
				Model    string            `json:"model"`
				Messages []json.RawMessage `json:"messages"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, err
			}
			out, err := json.Marshal(map[string]any{
				"model":      req.Model,
				"max_tokens": 256,
				"messages":   req.Messages,
			})
			return out, err
		},
	}

	var before struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(openaiBody, &before)

	out, err := e.prepareAnthropicRequestBody(&ExecParams{
		ClientProtocol: "openai-completions",
		ClientModel:    "minimax-m3",
	}, provider.Candidate{ContextWindow: &ctxWin}, openaiBody)
	if err != nil {
		t.Fatalf("prepareAnthropicRequestBody: %v", err)
	}

	var anthropic struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(out, &anthropic); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if len(anthropic.Messages) >= len(before.Messages) {
		t.Fatalf("expected trimmed anthropic messages; before=%d after=%d", len(before.Messages), len(anthropic.Messages))
	}
}
