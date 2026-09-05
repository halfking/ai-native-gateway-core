package ir

import (
	"strings"
	"testing"
)

// OpenAI-compatible providers emit in-band errors as a data line
// {"error":{...}} instead of choices[]. Previously this fell through to an
// empty delta chunk, so downstream failover and Responses/Gemini error
// surfacing never fired (round-2 audit A-#14).
func TestParseOpenAIStreamChunkInBandError(t *testing.T) {
	line := `data: {"error":{"message":"rate limited","type":"rate_limit_error","code":"429"}}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk failed: %v", err)
	}
	if chunk == nil {
		t.Fatalf("expected chunk, got nil")
	}
	if chunk.Type != ChunkTypeError {
		t.Fatalf("expected ChunkTypeError, got %q", chunk.Type)
	}
	if chunk.Error == nil {
		t.Fatalf("expected StreamError payload")
	}
	if chunk.Error.Message != "rate limited" || chunk.Error.Type != "rate_limit_error" || chunk.Error.Code != "429" {
		t.Fatalf("unexpected error payload: %+v", chunk.Error)
	}

	// Code falls back to type when absent (Anthropic-style payloads).
	chunk2, err := ParseOpenAIStreamChunk(`data: {"error":{"message":"boom","type":"server_error"}}`)
	if err != nil || chunk2 == nil || chunk2.Type != ChunkTypeError {
		t.Fatalf("type-only error frame not recognized: %v %+v", err, chunk2)
	}
	if chunk2.Error == nil || chunk2.Error.Code != "server_error" {
		t.Fatalf("code fallback to type failed: %+v", chunk2.Error)
	}

	// The Responses serializer must surface the error rather than a completed
	// empty response.
	serialized := chunk.SerializeOpenAI("chatcmpl-x", "gpt-x", 1)
	if !strings.Contains(serialized, `"error"`) {
		t.Fatalf("serialized error chunk lost error object: %q", serialized)
	}
}

// Normal chunks must not be misclassified after the error-frame addition.
func TestParseOpenAIStreamChunkNormalStillWorks(t *testing.T) {
	chunk, err := ParseOpenAIStreamChunk(`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`)
	if err != nil || chunk == nil {
		t.Fatalf("normal chunk parse failed: %v", err)
	}
	if chunk.Type == ChunkTypeError || chunk.Delta == nil || chunk.Delta.Content != "hi" {
		t.Fatalf("normal chunk misclassified: %+v", chunk)
	}
}
