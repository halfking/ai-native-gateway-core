package ir

import (
	"strings"
	"testing"
)

// ─── T1.2 Stream Audio Output Tests ────────────────────────────────────

// TestParseOpenAIStreamChunk_AudioDelta verifies OpenAI audio output delta parsing.
func TestParseOpenAIStreamChunk_AudioDelta(t *testing.T) {
	line := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-audio-preview","choices":[{"index":0,"delta":{"audio":{"data":"QkFTRQ==","transcript":"Hello"}}}]}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if chunk.Type != ChunkTypeDelta {
		t.Errorf("Type = %v, want delta", chunk.Type)
	}
	if chunk.Delta == nil {
		t.Fatal("Delta is nil")
	}
	if chunk.Delta.AudioDelta == nil {
		t.Fatal("AudioDelta is nil")
	}
	if chunk.Delta.AudioDelta.Data != "QkFTRQ==" {
		t.Errorf("Data = %q", chunk.Delta.AudioDelta.Data)
	}
	if chunk.Delta.AudioDelta.Transcript != "Hello" {
		t.Errorf("Transcript = %q", chunk.Delta.AudioDelta.Transcript)
	}
	if chunk.Delta.DeltaType != "audio" {
		t.Errorf("DeltaType = %q, want audio", chunk.Delta.DeltaType)
	}
}

// ─── T1.3 Anthropic Signature Streaming Tests ──────────────────────────

// TestParseAnthropicStreamEvent_SignatureDelta verifies signature_delta propagation.
// This is critical for Anthropic multi-turn: without the signature, the next
// turn is rejected with HTTP 400 and the model loses prior tool_use context.
func TestParseAnthropicStreamEvent_SignatureDelta(t *testing.T) {
	data := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_abc123"}}`)

	chunk, err := ParseAnthropicStreamEvent("content_block_delta", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if chunk.Type != ChunkTypeDelta {
		t.Errorf("Type = %v, want delta", chunk.Type)
	}
	if chunk.Delta == nil {
		t.Fatal("Delta is nil")
	}
	if chunk.Delta.ThinkingSignature != "sig_abc123" {
		t.Errorf("ThinkingSignature = %q, want sig_abc123", chunk.Delta.ThinkingSignature)
	}
	if chunk.Delta.DeltaType != "signature" {
		t.Errorf("DeltaType = %q, want signature", chunk.Delta.DeltaType)
	}
}

// TestParseAnthropicStreamEvent_ThinkingDelta verifies thinking_delta routing.
func TestParseAnthropicStreamEvent_ThinkingDelta(t *testing.T) {
	data := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think..."}}`)

	chunk, err := ParseAnthropicStreamEvent("content_block_delta", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if chunk.Delta == nil {
		t.Fatal("Delta is nil")
	}
	if chunk.Delta.ReasoningContent != "Let me think..." {
		t.Errorf("ReasoningContent = %q", chunk.Delta.ReasoningContent)
	}
}

// ─── T1.4 Anthropic Cache Token Streaming Tests ────────────────────────

// TestParseAnthropicStreamEvent_MessageStartCacheTokens verifies Anthropic
// cache token extraction in message_start event.
func TestParseAnthropicStreamEvent_MessageStartCacheTokens(t *testing.T) {
	data := []byte(`{"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet","usage":{"input_tokens":100,"cache_creation_input_tokens":80,"cache_read_input_tokens":20}}}`)

	chunk, err := ParseAnthropicStreamEvent("message_start", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if chunk.Type != ChunkTypeUsage {
		t.Errorf("Type = %v, want usage", chunk.Type)
	}
	if chunk.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if chunk.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", chunk.Usage.PromptTokens)
	}
	if chunk.Usage.CacheWriteTokens == nil || *chunk.Usage.CacheWriteTokens != 80 {
		t.Errorf("CacheWriteTokens = %v, want 80", chunk.Usage.CacheWriteTokens)
	}
	if chunk.Usage.CacheReadTokens == nil || *chunk.Usage.CacheReadTokens != 20 {
		t.Errorf("CacheReadTokens = %v, want 20", chunk.Usage.CacheReadTokens)
	}
}

// ─── T1.5 OpenAI Detailed Usage Streaming Tests ────────────────────────

// TestParseOpenAIStreamChunk_DetailedUsage verifies OpenAI detailed usage extraction.
func TestParseOpenAIStreamChunk_DetailedUsage(t *testing.T) {
	line := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1500,"completion_tokens":50,"total_tokens":1550,"prompt_tokens_details":{"cached_tokens":100,"image_tokens":300},"completion_tokens_details":{"reasoning_tokens":20}}}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if chunk.Type != ChunkTypeUsage {
		t.Errorf("Type = %v, want usage", chunk.Type)
	}
	if chunk.Usage == nil {
		t.Fatal("Usage is nil")
	}

	if chunk.Usage.CacheReadTokens == nil || *chunk.Usage.CacheReadTokens != 100 {
		t.Errorf("CacheReadTokens = %v, want 100", chunk.Usage.CacheReadTokens)
	}
	if chunk.Usage.ImageTokens == nil || *chunk.Usage.ImageTokens != 300 {
		t.Errorf("ImageTokens = %v, want 300", chunk.Usage.ImageTokens)
	}
	if chunk.Usage.ReasoningTokens == nil || *chunk.Usage.ReasoningTokens != 20 {
		t.Errorf("ReasoningTokens = %v, want 20", chunk.Usage.ReasoningTokens)
	}
}

// ─── T1.6 StreamDeltaType Routing Tests ────────────────────────────────

// TestParseOpenAIStreamChunk_DeltaTypeRouting verifies DeltaType is set correctly.
func TestParseOpenAIStreamChunk_DeltaTypeRouting(t *testing.T) {
	cases := []struct {
		name        string
		deltaJSON   string
		wantType    string
		wantContent string
		wantReason  string
	}{
		{
			name:        "text delta",
			deltaJSON:   `{"content":"Hello"}`,
			wantType:    "text",
			wantContent: "Hello",
		},
		{
			name:       "reasoning delta",
			deltaJSON:  `{"reasoning_content":"thinking..."}`,
			wantType:   "reasoning",
			wantReason: "thinking...",
		},
		{
			name:      "audio delta",
			deltaJSON: `{"audio":{"data":"abc","transcript":"hi"}}`,
			wantType:  "audio",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":` + tc.deltaJSON + `}]}`
			chunk, err := ParseOpenAIStreamChunk(line)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if chunk.Delta == nil {
				t.Fatal("Delta is nil")
			}
			if chunk.Delta.DeltaType != tc.wantType {
				t.Errorf("DeltaType = %q, want %q", chunk.Delta.DeltaType, tc.wantType)
			}
			if tc.wantContent != "" && chunk.Delta.Content != tc.wantContent {
				t.Errorf("Content = %q, want %q", chunk.Delta.Content, tc.wantContent)
			}
			if tc.wantReason != "" && chunk.Delta.ReasoningContent != tc.wantReason {
				t.Errorf("ReasoningContent = %q, want %q", chunk.Delta.ReasoningContent, tc.wantReason)
			}
		})
	}
}

// ─── T1.7 Backward Compatibility Tests ─────────────────────────────────

// TestLegacyStreamChunk_StillWorks verifies existing stream chunks still parse correctly.
func TestLegacyStreamChunk_StillWorks(t *testing.T) {
	// Old-style OpenAI chunk without reasoning_content/audio_delta
	line := `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if chunk.Delta.Content != "Hello" {
		t.Errorf("Content = %q", chunk.Delta.Content)
	}
	if chunk.Delta.AudioDelta != nil {
		t.Error("AudioDelta should be nil for legacy")
	}
	if chunk.Delta.ThinkingSignature != "" {
		t.Error("ThinkingSignature should be empty for legacy")
	}
}

// TestParseAnthropicStreamEvent_KeepAlive ensures ping events still work.
func TestParseAnthropicStreamEvent_KeepAlive(t *testing.T) {
	data := []byte(`{"type":"ping"}`)
	chunk, err := ParseAnthropicStreamEvent("ping", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if chunk.Type != ChunkTypeDelta {
		t.Errorf("Type = %v", chunk.Type)
	}
	// Verify delta is set even for keepalive
	if chunk.Delta == nil {
		t.Error("Delta should be set for keepalive")
	}
}

// ─── T1.8 Stream Chunk Serialize with Audio ────────────────────────────

// TestSerializeOpenAI_AudioDelta verifies audio output is preserved on serialize.
func TestSerializeOpenAI_AudioDelta(t *testing.T) {
	chunk := &StreamChunk{
		Type:  ChunkTypeDelta,
		ID:    "test",
		Model: "gpt-4o-audio-preview",
		Delta: &StreamDelta{
			AudioDelta: &StreamAudioDelta{
				Data:       "QkFTRQ==",
				Transcript: "Hi",
			},
		},
	}

	out := chunk.SerializeOpenAI("test", "gpt-4o-audio-preview", 1)
	if !strings.Contains(out, "audio") {
		t.Error("audio field not in output")
	}
	if !strings.Contains(out, "QkFTRQ==") {
		t.Error("audio data not in output")
	}
	if !strings.Contains(out, "Hi") {
		t.Error("transcript not in output")
	}
}
