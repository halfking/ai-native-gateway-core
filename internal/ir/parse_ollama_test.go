package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseOllamaResponse_ReasoningTokensAndUsage is the byte-level golden
// test pinned in docs/供应商协议优化-实施规划.md §6.2. Reasoning content,
// usage counts, and the protocol tag must round-trip identically.
func TestParseOllamaResponse_ReasoningTokensAndUsage(t *testing.T) {
	raw := []byte(`{
		"model":"llama3.1",
		"created_at":"2024-09-21T12:34:56.789Z",
		"message":{"role":"assistant","content":"Final answer","thinking":"Let me think..."},
		"done":true,
		"done_reason":"stop",
		"prompt_eval_count":15,
		"eval_count":4
	}`)
	resp, err := ParseOllamaResponse(raw)
	if err != nil {
		t.Fatalf("ParseOllamaResponse: %v", err)
	}

	if resp.Model != "llama3.1" {
		t.Errorf("Model = %q, want llama3.1", resp.Model)
	}
	if resp.SourceProtocol != ProtocolOllamaChat {
		t.Errorf("SourceProtocol = %q, want %q", resp.SourceProtocol, ProtocolOllamaChat)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "Final answer" {
		t.Errorf("Content = %+v, want one text block 'Final answer'", resp.Content)
	}
	if resp.ReasoningContent != "Let me think..." {
		t.Errorf("ReasoningContent = %q, want %q", resp.ReasoningContent, "Let me think...")
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", resp.FinishReason)
	}
	if resp.Usage.PromptTokens != 15 {
		t.Errorf("Usage.PromptTokens = %d, want 15", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 4 {
		t.Errorf("Usage.CompletionTokens = %d, want 4", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 19 {
		t.Errorf("Usage.TotalTokens = %d, want 19", resp.Usage.TotalTokens)
	}
	if resp.Created == 0 {
		t.Errorf("Created = 0, want non-zero unix timestamp")
	}
	if !strings.HasPrefix(resp.ID, "ollama-llama3-1-") {
		t.Errorf("ID = %q, want prefix ollama-llama3-1- (dots sanitized to dashes)", resp.ID)
	}
}

// TestParseOllamaResponse_NoReasoning verifies that the thinking field is
// NOT surfaced as content (Ollama keeps them separate even when empty).
func TestParseOllamaResponse_NoReasoning(t *testing.T) {
	raw := []byte(`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`)
	resp, err := ParseOllamaResponse(raw)
	if err != nil {
		t.Fatalf("ParseOllamaResponse: %v", err)
	}
	if resp.ReasoningContent != "" {
		t.Errorf("ReasoningContent = %q, want empty", resp.ReasoningContent)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "hi" {
		t.Errorf("Content = %+v", resp.Content)
	}
}

// TestParseOllamaResponse_NoUsageWhenDoneFalse guards §3.6 rule "Usage only
// emitted when done=true AND counts present". Interim chunks must not
// synthesize zero-valued usage.
func TestParseOllamaResponse_NoUsageWhenDoneFalse(t *testing.T) {
	raw := []byte(`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"mid"},"done":false,"prompt_eval_count":15,"eval_count":4}`)
	resp, err := ParseOllamaResponse(raw)
	if err != nil {
		t.Fatalf("ParseOllamaResponse: %v", err)
	}
	if resp.Usage.PromptTokens != 0 || resp.Usage.CompletionTokens != 0 || resp.Usage.TotalTokens != 0 {
		t.Errorf("interim chunk leaked usage: %+v", resp.Usage)
	}
}

// TestParseOllamaResponse_EmptyUsage guards the same rule from the other
// angle: done=true but no eval counts (the upstream may omit on
// length-limited or aborted completions).
func TestParseOllamaResponse_EmptyUsage(t *testing.T) {
	raw := []byte(`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`)
	resp, err := ParseOllamaResponse(raw)
	if err != nil {
		t.Fatalf("ParseOllamaResponse: %v", err)
	}
	if resp.Usage.PromptTokens != 0 || resp.Usage.TotalTokens != 0 {
		t.Errorf("zero usage leaked: %+v", resp.Usage)
	}
}

// TestParseOllamaResponse_UpstreamError surfaces an Ollama `error` field as
// a normal Go error so they propagate through the executor's error path.
func TestParseOllamaResponse_UpstreamError(t *testing.T) {
	raw := []byte(`{"error":"model 'nope' not found"}`)
	_, err := ParseOllamaResponse(raw)
	if err == nil {
		t.Fatal("expected error for upstream error envelope")
	}
	if !strings.Contains(err.Error(), "model 'nope' not found") {
		t.Errorf("err = %v, want message preserved", err)
	}
}

// TestParseOllamaResponse_EmptyBody returns an error so the executor can
// distinguish "empty body" from "valid empty response".
func TestParseOllamaResponse_EmptyBody(t *testing.T) {
	_, err := ParseOllamaResponse(nil)
	if err == nil {
		t.Fatal("expected error for empty body")
	}
	if !strings.Contains(err.Error(), "empty body") {
		t.Errorf("err = %v, want 'empty body'", err)
	}
}

// TestParseOllamaResponse_InvalidJSON returns an error so the executor can
// distinguish "malformed body" from "valid empty response".
func TestParseOllamaResponse_InvalidJSON(t *testing.T) {
	_, err := ParseOllamaResponse([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestParseOllamaResponse_TimestampFormats covers both RFC3339 and
// RFC3339Nano — Ollama has shipped both across versions.
func TestParseOllamaResponse_TimestampFormats(t *testing.T) {
	cases := []struct {
		name  string
		ts    string
		valid bool
	}{
		{"rfc3339nano", "2024-09-21T12:34:56.789012345Z", true},
		{"rfc3339", "2024-09-21T12:34:56Z", true},
		{"empty", "", false}, // Created stays 0; not an error
		{"garbage", "not-a-time", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"llama3.1","created_at":"` + tc.ts + `","message":{"role":"assistant","content":"x"},"done":true,"done_reason":"stop"}`)
			resp, err := ParseOllamaResponse(body)
			if err != nil {
				t.Fatalf("ParseOllamaResponse: %v", err)
			}
			if tc.valid && resp.Created == 0 {
				t.Errorf("Created = 0 for valid timestamp %q", tc.ts)
			}
			if !tc.valid && resp.Created != 0 {
				t.Errorf("Created = %d for invalid timestamp %q, want 0", resp.Created, tc.ts)
			}
		})
	}
}

// TestParseOllamaResponse_DoneReasonLength confirms the streaming-parser's
// length→length mapping is shared (cross-references the
// parse_ollama_stream.go contract).
func TestParseOllamaResponse_DoneReasonLength(t *testing.T) {
	raw := []byte(`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"x"},"done":true,"done_reason":"length"}`)
	resp, err := ParseOllamaResponse(raw)
	if err != nil {
		t.Fatalf("ParseOllamaResponse: %v", err)
	}
	if resp.FinishReason != "length" {
		t.Errorf("FinishReason = %q, want length", resp.FinishReason)
	}
}

// TestOllamaResponseIDStable locks the ID generation so byte-level golden
// tests in higher-level packages stay reproducible.
func TestOllamaResponseIDStable(t *testing.T) {
	a := ollamaResponseID("llama3.1", "2024-09-21T12:34:56.789Z")
	b := ollamaResponseID("llama3.1", "2024-09-21T12:34:56.789Z")
	if a != b {
		t.Errorf("ID not deterministic: %q vs %q", a, b)
	}
	if a == "" || a == "ollama" {
		t.Errorf("ID too short: %q", a)
	}
	if !strings.HasPrefix(a, "ollama-llama3-1-") {
		t.Errorf("ID = %q, want prefix ollama-llama3-1- (dots sanitized)", a)
	}
}

// TestOllamaResponseIDSanitizes checks that model names with spaces or
// colons do not produce unsafe IDs.
func TestOllamaResponseIDSanitizes(t *testing.T) {
	id := ollamaResponseID("model with spaces", "2024-09-21T12:34:56.789Z")
	if strings.Contains(id, " ") {
		t.Errorf("ID has space: %q", id)
	}
}

// TestParseOllamaResponse_RawMessageExtras sanity-checks we don't break the
// JSON shape downstreamers rely on (Content / ReasoningContent must be
// JSON-marshalable in the InternalResponse contract).
func TestParseOllamaResponse_RawMessageExtras(t *testing.T) {
	raw := []byte(`{"model":"llama3.1","created_at":"2024-09-21T12:34:56.789Z","message":{"role":"assistant","content":"hi","thinking":"r1"},"done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":2}`)
	resp, err := ParseOllamaResponse(raw)
	if err != nil {
		t.Fatalf("ParseOllamaResponse: %v", err)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	// Round-trip invariants
	var back InternalResponse
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if back.Model != resp.Model {
		t.Errorf("Model not preserved: %q vs %q", back.Model, resp.Model)
	}
	if back.ReasoningContent != resp.ReasoningContent {
		t.Errorf("ReasoningContent not preserved: %q vs %q", back.ReasoningContent, resp.ReasoningContent)
	}
	if back.Usage.TotalTokens != resp.Usage.TotalTokens {
		t.Errorf("Usage.TotalTokens not preserved: %d vs %d", back.Usage.TotalTokens, resp.Usage.TotalTokens)
	}
}