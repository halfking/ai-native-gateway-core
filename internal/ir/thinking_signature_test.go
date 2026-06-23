// Package ir — thinking block signature preservation tests.
//
// Why this file exists (PR-2, 2026-06-24):
//
// claude-opus-4-8 (and other Anthropic extended-thinking models) require
// thinking blocks to carry a `signature` field when they are echoed back
// in subsequent turns. Without the signature, Anthropic rejects the
// request with HTTP 400 "invalid_request_error: … signature: Input should
// be a valid string" — which manifests at the call-site as the model
// "losing" the tool_use block from the previous turn.
//
// The IR layer is the SSOT for cross-protocol request/response data, so
// every parse + serialize path that touches a thinking block must round-
// trip the signature. This file exercises each of those paths with a
// realistic opus-4-8 fixture (thinking + tool_use + signature).
package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// opus4Fixture is a representative claude-opus-4-8 response that has
// thinking + tool_use blocks. The signature is the opaque base64 token
// Anthropic returns to verify the chain-of-thought.
const opus4SignatureFixture = `{
	"id": "msg_opus4_8_001",
	"type": "message",
	"role": "assistant",
	"model": "claude-opus-4-8",
	"content": [
		{"type": "thinking", "thinking": "I need to look up the weather for Tokyo first.", "signature": "sig_opus_4_8_abc123def456"},
		{"type": "tool_use", "id": "toolu_opus_01", "name": "get_weather", "input": {"city": "Tokyo"}}
	],
	"stop_reason": "tool_use",
	"usage": {"input_tokens": 50, "output_tokens": 25}
}`

// ─── ParseAnthropic (request) ──────────────────────────────────────────────

// TestParseAnthropicRequest_ThinkingBlockWithSignature verifies that when
// an Anthropic request echoes back a previous assistant message that
// contains a thinking block with a signature, the IR layer preserves
// both the thinking text and the signature. Without this, downstream
// re-serialization to Anthropic drops the signature and opus-4-8
// rejects the next-turn request with HTTP 400.
func TestParseAnthropicRequest_ThinkingBlockWithSignature(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-8",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "what's the weather in Tokyo?"},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "I need to call get_weather.", "signature": "sig_prev_turn_xyz"},
					{"type": "tool_use", "id": "toolu_prev", "name": "get_weather", "input": {"city": "Tokyo"}}
				]
			},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_prev", "content": "sunny, 25C"}]}
		]
	}`)

	got, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}

	if len(got.Messages) != 3 {
		t.Fatalf("Messages len = %d, want 3", len(got.Messages))
	}

	assistant := got.Messages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("Messages[1].Role = %q, want assistant", assistant.Role)
	}
	if len(assistant.Content) != 2 {
		t.Fatalf("Assistant content len = %d, want 2", len(assistant.Content))
	}

	thinking := assistant.Content[0]
	if thinking.Type != "thinking" {
		t.Fatalf("Content[0].Type = %q, want thinking", thinking.Type)
	}
	if thinking.Thinking == nil {
		t.Fatal("Content[0].Thinking is nil")
	}
	if thinking.Thinking.Thinking != "I need to call get_weather." {
		t.Errorf("Thinking.Thinking = %q, want %q",
			thinking.Thinking.Thinking, "I need to call get_weather.")
	}
	if thinking.Thinking.Signature != "sig_prev_turn_xyz" {
		t.Errorf("Thinking.Signature = %q, want %q (round-trip must preserve signature)",
			thinking.Thinking.Signature, "sig_prev_turn_xyz")
	}
}

// ─── ParseAnthropicResponse (response, non-streaming) ─────────────────────

// TestParseAnthropicResponse_ThinkingWithSignature verifies that
// ParseAnthropicResponse captures the signature alongside the thinking
// text. This is the non-streaming path used by the relay's Q3 mode
// (openai client ← anthropic upstream) when emitting a complete
// message to the OpenAI-style caller — the response builder needs
// the signature if it ever decides to round-trip the data back.
func TestParseAnthropicResponse_ThinkingWithSignature(t *testing.T) {
	got, err := ParseAnthropicResponse([]byte(opus4SignatureFixture))
	if err != nil {
		t.Fatalf("ParseAnthropicResponse: %v", err)
	}

	if len(got.Content) != 2 {
		t.Fatalf("Content len = %d, want 2 (thinking + tool_use)", len(got.Content))
	}

	// First content block: thinking (must carry signature)
	thinking := got.Content[0]
	if thinking.Type != "thinking" {
		t.Fatalf("Content[0].Type = %q, want thinking", thinking.Type)
	}
	if thinking.Thinking != "I need to look up the weather for Tokyo first." {
		t.Errorf("Content[0].Thinking = %q, want thinking text", thinking.Thinking)
	}
	if thinking.Signature != "sig_opus_4_8_abc123def456" {
		t.Errorf("Content[0].Signature = %q, want %q (signature MUST survive parse)",
			thinking.Signature, "sig_opus_4_8_abc123def456")
	}

	// Second content block: tool_use
	toolUse := got.Content[1]
	if toolUse.Type != "tool_use" {
		t.Errorf("Content[1].Type = %q, want tool_use", toolUse.Type)
	}
	if toolUse.ID != "toolu_opus_01" {
		t.Errorf("Content[1].ID = %q, want toolu_opus_01", toolUse.ID)
	}

	// Tool call list mirror should also be populated
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "toolu_opus_01" {
		t.Errorf("ToolCalls = %+v, want 1 tool call with id=toolu_opus_01", got.ToolCalls)
	}
}

// ─── Stream parsing (content_block_start + content_block_delta) ───────────

// TestParseAnthropicStreamEvent_ContentBlockStart_Thinking verifies that
// the streaming parser recognises `type: "thinking"` content blocks.
// Previously it fell through to the "text or thinking block start"
// branch with an empty Delta, which worked for delta emission but made
// the thinking-type explicit. This test pins the behaviour so a future
// refactor cannot accidentally drop the type signal.
func TestParseAnthropicStreamEvent_ContentBlockStart_Thinking(t *testing.T) {
	data := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)

	got, err := ParseAnthropicStreamEvent("content_block_start", data)
	if err != nil {
		t.Fatalf("ParseAnthropicStreamEvent: %v", err)
	}

	if got.Type != ChunkTypeDelta {
		t.Errorf("Type = %q, want %q", got.Type, ChunkTypeDelta)
	}
	if got.Delta == nil {
		t.Fatal("Delta is nil — thinking block start must produce an IR chunk")
	}
	// At start-of-thinking there is no text yet, so Delta is empty
	// (the signature arrives later via signature_delta). Pin the
	// explicit "empty but non-nil" contract.
	if got.Delta.Content != "" || got.Delta.ReasoningContent != "" {
		t.Errorf("Delta has premature content: %+v", got.Delta)
	}
}

// TestParseAnthropicStreamEvent_ContentBlockDelta_SignatureDelta verifies
// that the streaming parser recognises Anthropic's `signature_delta`
// payload that closes a thinking block. The IR layer must NOT crash
// on this delta type (it did, before PR-2: the unknown default branch
// produced a parse error and the chunk was dropped, which on opus-4-8
// severed the chain-of-thought verification on the next turn).
func TestParseAnthropicStreamEvent_ContentBlockDelta_SignatureDelta(t *testing.T) {
	data := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_streaming_abc123"}}`)

	got, err := ParseAnthropicStreamEvent("content_block_delta", data)
	if err != nil {
		t.Fatalf("ParseAnthropicStreamEvent returned error for signature_delta: %v", err)
	}

	if got.Type != ChunkTypeDelta {
		t.Errorf("Type = %q, want %q", got.Type, ChunkTypeDelta)
	}
	if got.Delta == nil {
		t.Fatal("Delta is nil — signature_delta must produce a parseable IR chunk")
	}
	// The signature itself has no OpenAI-protocol equivalent, so the
	// delta stays empty. Pin the contract: no crash, no leak into
	// content/reasoning_content.
	if got.Delta.Content != "" {
		t.Errorf("signature_delta leaked into content: %q", got.Delta.Content)
	}
	if got.Delta.ReasoningContent != "" {
		t.Errorf("signature_delta leaked into reasoning_content: %q", got.Delta.ReasoningContent)
	}
}

// TestParseAnthropicStreamEvent_ThinkingBlockFullSequence walks the
// full opus-4-8 streaming sequence (start → delta → signature_delta
// → stop) end-to-end. Every step must parse without error, which is
// the regression guard for the "tool_use lost on second turn" symptom.
func TestParseAnthropicStreamEvent_ThinkingBlockFullSequence(t *testing.T) {
	events := []struct {
		eventType string
		data      string
	}{
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think about Tokyo weather."}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_full_123"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		// Then the tool_use block follows.
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_tokyo","name":"get_weather","input":{}}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Tokyo\"}"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":1}`},
	}

	var (
		thinkingAccum strings.Builder
		toolArgs      strings.Builder
		toolName      string
		toolID        string
	)

	for _, ev := range events {
		chunk, err := ParseAnthropicStreamEvent(ev.eventType, []byte(ev.data))
		if err != nil {
			t.Fatalf("event %s: %v\npayload: %s", ev.eventType, err, ev.data)
		}
		if chunk == nil {
			t.Fatalf("event %s: nil chunk", ev.eventType)
		}
		if chunk.Delta == nil {
			continue
		}
		if chunk.Delta.ReasoningContent != "" {
			thinkingAccum.WriteString(chunk.Delta.ReasoningContent)
		}
		for _, tc := range chunk.Delta.ToolCalls {
			if tc.Name != "" {
				toolName = tc.Name
			}
			if tc.ID != "" {
				toolID = tc.ID
			}
			if tc.Arguments != "" {
				toolArgs.WriteString(tc.Arguments)
			}
		}
	}

	if got := thinkingAccum.String(); got != "Let me think about Tokyo weather." {
		t.Errorf("thinking accum = %q, want %q", got, "Let me think about Tokyo weather.")
	}
	if toolName != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", toolName)
	}
	if toolID != "toolu_tokyo" {
		t.Errorf("tool id = %q, want toolu_tokyo", toolID)
	}
	if got := toolArgs.String(); got != `{}{"city":"Tokyo"}` {
		// Note: ParseAnthropicStreamEvent emits the tool_use block's
		// initial `input:{}` as a first Arguments chunk (it's the
		// content_block_start payload verbatim), then the
		// input_json_delta chunks append to it. The "{}" prefix is
		// therefore expected — downstream consumers (relay layer)
		// filter it out before writing the wire tool_calls delta.
		t.Errorf("tool args = %q, want %q", got, `{}{"city":"Tokyo"}`)
	}
}

// ─── Serialize round-trip (request) ────────────────────────────────────────

// TestSerializeAnthropic_PreservesThinkingSignature round-trips a
// thinking block with signature through the request serializer.
// If the signature is dropped on the way out, opus-4-8 will reject
// the next turn — so this is the primary regression guard.
func TestSerializeAnthropic_PreservesThinkingSignature(t *testing.T) {
	req := &InternalRequest{
		Model:          "claude-opus-4-8",
		MaxTokens:      1024,
		SourceProtocol: ProtocolAnthropicMessages,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "what's the weather?"}}},
			{Role: "assistant", Content: []ContentBlock{
				{Type: "thinking", Thinking: &ThinkingBlock{
					Thinking:  "I need to call get_weather.",
					Signature: "sig_round_trip_999",
				}},
				{Type: "tool_use", ToolUse: &ToolUse{
					ID:    "toolu_rt",
					Name:  "get_weather",
					Input: json.RawMessage(`{"city":"Tokyo"}`),
				}},
			}},
			{Role: "user", Content: []ContentBlock{
				{Type: "tool_result", ToolResult: &ToolResult{
					ToolUseID: "toolu_rt",
					Content:   []ContentBlock{{Type: "text", Text: "sunny"}},
				}},
			}},
		},
	}

	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}

	msgs, ok := got["messages"].([]any)
	if !ok || len(msgs) != 3 {
		t.Fatalf("messages = %+v, want 3 entries", got["messages"])
	}
	assistant := msgs[1].(map[string]any)
	content, ok := assistant["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("assistant content = %+v, want 2 blocks (thinking + tool_use)", assistant["content"])
	}

	thinking := content[0].(map[string]any)
	if thinking["type"] != "thinking" {
		t.Errorf("block[0].type = %v, want thinking", thinking["type"])
	}
	if thinking["thinking"] != "I need to call get_weather." {
		t.Errorf("block[0].thinking = %v", thinking["thinking"])
	}
	if thinking["signature"] != "sig_round_trip_999" {
		t.Errorf("block[0].signature = %v, want %q (signature must round-trip on serialize)",
			thinking["signature"], "sig_round_trip_999")
	}

	toolUse := content[1].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "toolu_rt" {
		t.Errorf("block[1] = %+v, want tool_use/toolu_rt", toolUse)
	}
}

// TestSerializeAnthropicResponse_PreservesThinkingSignature round-trips
// a thinking block with signature through the response serializer. The
// non-streaming Q3 path uses this to produce Anthropic-shaped responses
// for clients that need to echo the data back; signature preservation
// is what keeps opus-4-8 multi-turn stable.
func TestSerializeAnthropicResponse_PreservesThinkingSignature(t *testing.T) {
	ir := &InternalResponse{
		ID:             "msg_resp_001",
		Model:          "claude-opus-4-8",
		Role:           "assistant",
		SourceProtocol: ProtocolAnthropicMessages,
		FinishReason:   "tool_use",
		Content: []ResponseContentBlock{
			{Type: "thinking", Thinking: "I need weather for Tokyo.", Signature: "sig_resp_777"},
			{Type: "tool_use", ID: "toolu_resp_1", Name: "get_weather", Input: json.RawMessage(`{"city":"Tokyo"}`)},
		},
		Usage: ResponseUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}

	out, err := SerializeAnthropicResponse(ir, "claude-opus-4-8")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}

	content, ok := got["content"].([]any)
	if !ok || len(content) < 2 {
		t.Fatalf("content = %+v, want >= 2 blocks", got["content"])
	}
	thinking := content[0].(map[string]any)
	if thinking["type"] != "thinking" {
		t.Errorf("block[0].type = %v, want thinking", thinking["type"])
	}
	if thinking["signature"] != "sig_resp_777" {
		t.Errorf("block[0].signature = %v, want %q (response must carry signature through)",
			thinking["signature"], "sig_resp_777")
	}
}
