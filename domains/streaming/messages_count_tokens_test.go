package streaming

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── /v1/messages/count_tokens ──────────────────────────────────────────────

func newCountTokensTestHandler() *CountTokensHandler {
	ch := &ChatHandler{}
	return NewCountTokensHandler(ch)
}

func TestCountTokens_ReturnsEstimate(t *testing.T) {
	h := newCountTokensTestHandler()
	body := `{"model":"minimax-m3","max_tokens":4096,"system":[{"type":"text","text":"You are helpful."}],"messages":[{"role":"user","content":[{"type":"text","text":"hello world this is a counting test"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if resp.InputTokens <= 0 {
		t.Fatalf("input_tokens = %d, want > 0", resp.InputTokens)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}

func TestCountTokens_MethodNotAllowed(t *testing.T) {
	h := newCountTokensTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/messages/count_tokens", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "type") {
		t.Errorf("error body not anthropic-shaped: %s", rec.Body.String())
	}
}

func TestCountTokens_UnknownFieldsTolerated(t *testing.T) {
	// Claude Code sends anthropic-beta payloads with context_management and
	// other fields the gateway does not model; estimation must not 4xx.
	h := newCountTokensTestHandler()
	body := `{"model":"claude-x","context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]},"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":1024}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	req.Header.Set("anthropic-beta", "context-management-2025-06-27")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// ── usage-zero 兜底 ────────────────────────────────────────────────────────

func TestPatchAnthropicUsageInput_FillsZeroUsage(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"ok"}],"id":"msg_x","model":"m","role":"assistant","stop_reason":"end_turn","stop_sequence":null,"type":"message","usage":{"input_tokens":0,"output_tokens":0}}`)
	out := patchAnthropicUsageInput(body, 178)
	var resp struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Usage.InputTokens != 178 {
		t.Errorf("input_tokens = %d, want 178", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 0 {
		t.Errorf("output_tokens = %d, want 0 (untouched)", resp.Usage.OutputTokens)
	}
}

func TestPatchAnthropicUsageInput_NonZeroUntouched(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":42,"output_tokens":7},"content":[]}`)
	out := patchAnthropicUsageInput(body, 999)
	if string(out) != string(body) {
		t.Errorf("non-zero usage must be returned unchanged:\n got %s\nwant %s", out, body)
	}
}

func TestPatchAnthropicUsageInput_MalformedSafe(t *testing.T) {
	body := []byte(`not-json`)
	out := patchAnthropicUsageInput(body, 5)
	if string(out) != string(body) {
		t.Errorf("malformed body must pass through unchanged")
	}
}

// ── writeNonStreamResponse usage 兜底集成 ──────────────────────────────────

func TestWriteNonStreamResponse_PatchesZeroUsageWithEstimate(t *testing.T) {
	h := &MessagesHandler{chatHandler: &ChatHandler{}}
	// Upstream OpenAI body WITHOUT usage (the shape observed live when the
	// pipeline drops it): converter yields usage 0/0, guard fills estimate.
	upstream := []byte(`{"id":"x1","choices":[{"index":0,"message":{"role":"assistant","content":"<think>hmm</think>\n\nok"},"finish_reason":"stop"}]}`)
	rec := httptest.NewRecorder()
	out := h.writeNonStreamResponse(rec, upstream, "minimax-m3", "req123", 210)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Content []map[string]any `json:"content"`
		Usage   struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, out)
	}
	if resp.Usage.InputTokens != 210 {
		t.Errorf("input_tokens = %d, want 210 (estimate)", resp.Usage.InputTokens)
	}
	// <think> split must survive the patch pass.
	if len(resp.Content) < 2 || resp.Content[0]["type"] != "thinking" {
		t.Errorf("thinking block lost: %+v", resp.Content)
	}
}

func TestWriteNonStreamResponse_RealUsageBeatsEstimate(t *testing.T) {
	h := &MessagesHandler{chatHandler: &ChatHandler{}}
	upstream := []byte(`{"id":"x2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":178,"completion_tokens":21,"total_tokens":199}}`)
	rec := httptest.NewRecorder()
	out := h.writeNonStreamResponse(rec, upstream, "minimax-m3", "req124", 999)
	var resp struct {
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Usage.InputTokens != 178 {
		t.Errorf("input_tokens = %d, want 178 (real usage wins over estimate)", resp.Usage.InputTokens)
	}
}
