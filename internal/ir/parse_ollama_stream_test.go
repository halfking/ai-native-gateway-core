package ir

import (
	"strings"
	"testing"
)

// 2026-09-21 audit (P2-1): Ollama's NDJSON chat-completion stream must parse
// into the IR StreamChunk type used by all other dialects, so downstream
// stream synthesizers don't have to special-case the framing.
func TestParseOllamaStreamChunk_Delta(t *testing.T) {
	line := []byte(`{"model":"llama3.1","created_at":"2024-09-21","message":{"role":"assistant","content":"Hel"},"done":false}`)
	chunk, err := ParseOllamaStreamChunk(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if chunk.SourceProtocol != ProtocolOllamaChat {
		t.Errorf("SourceProtocol = %q, want %q", chunk.SourceProtocol, ProtocolOllamaChat)
	}
	if chunk.Type != ChunkTypeDelta {
		t.Errorf("Type = %q, want %q", chunk.Type, ChunkTypeDelta)
	}
	if chunk.Delta == nil || chunk.Delta.Content != "Hel" {
		t.Errorf("Delta.Content = %v, want %q", chunk.Delta, "Hel")
	}
	if chunk.CumulativeContent != "Hel" {
		t.Errorf("CumulativeContent = %q, want %q", chunk.CumulativeContent, "Hel")
	}
}

func TestParseOllamaStreamChunk_Reasoning(t *testing.T) {
	// Ollama 0.5+ emits `message.thinking` separately from `content`.
	line := []byte(`{"model":"deepseek-r1","message":{"role":"assistant","content":"","thinking":"Let me think"},"done":false}`)
	chunk, err := ParseOllamaStreamChunk(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if chunk.Type != ChunkTypeDelta {
		t.Errorf("Type = %q, want %q", chunk.Type, ChunkTypeDelta)
	}
	if chunk.Delta == nil || chunk.Delta.ReasoningContent != "Let me think" {
		t.Errorf("Delta.ReasoningContent = %v, want %q", chunk.Delta, "Let me think")
	}
	if chunk.Delta.DeltaType != "reasoning" {
		t.Errorf("Delta.DeltaType = %q, want %q", chunk.Delta.DeltaType, "reasoning")
	}
}

func TestParseOllamaStreamChunk_Done(t *testing.T) {
	// Terminal chunk: done=true, carries usage + done_reason.
	line := []byte(`{"model":"llama3.1","message":{"role":"assistant","content":"Hello!"},"done_reason":"stop","done":true,"prompt_eval_count":10,"eval_count":20}`)
	chunk, err := ParseOllamaStreamChunk(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if chunk.Type != ChunkTypeDone {
		t.Errorf("Type = %q, want %q", chunk.Type, ChunkTypeDone)
	}
	if chunk.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", chunk.FinishReason, "stop")
	}
	if chunk.Usage == nil {
		t.Fatal("Usage = nil, want populated")
	}
	if chunk.Usage.PromptTokens != 10 || chunk.Usage.CompletionTokens != 20 || chunk.Usage.TotalTokens != 30 {
		t.Errorf("Usage = %+v, want {10,20,30}", chunk.Usage)
	}
}

func TestParseOllamaStreamChunk_Error(t *testing.T) {
	line := []byte(`{"error":"model 'foo' not found"}`)
	chunk, err := ParseOllamaStreamChunk(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if chunk.Type != ChunkTypeError {
		t.Errorf("Type = %q, want %q", chunk.Type, ChunkTypeError)
	}
	if chunk.Error == nil || !strings.Contains(chunk.Error.Message, "not found") {
		t.Errorf("Error = %+v, want error containing 'not found'", chunk.Error)
	}
}

func TestParseOllamaStreamChunk_EmptyLine(t *testing.T) {
	if _, err := ParseOllamaStreamChunk([]byte("")); err == nil {
		t.Errorf("expected error on empty line")
	}
	if _, err := ParseOllamaStreamChunk([]byte("   \n")); err == nil {
		t.Errorf("expected error on whitespace-only line")
	}
}

// mapOllamaDoneReason must collapse Ollama lifecycle signals to "stop" so
// downstream classifiers don't treat a model reload as a content filter.
func TestMapOllamaDoneReason(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "stop"},
		{"stop", "stop"},
		{"length", "length"},
		{"load", "stop"},   // model lifecycle signal — collapse
		{"unload", "stop"}, // ditto
		{"unknown", "unknown"},
	}
	for _, c := range cases {
		if got := mapOllamaDoneReason(c.in); got != c.want {
			t.Errorf("mapOllamaDoneReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
