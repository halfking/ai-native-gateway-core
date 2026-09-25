package ir

import (
	"strings"
	"testing"
)

// 2026-09-21 audit (P2-1): Ollama's NDJSON chat-completion stream must parse
// into the IR StreamChunk type used by all other dialects, so downstream
// stream synthesizers don't have to special-case the framing.
//
// R66 audit round: r0924's prior "cumulative content" hypothesis is
// reverted — WebFetch of https://github.com/ollama/ollama/blob/main/docs/api.md
// (2026-09-25) confirms Ollama's `message.content` is INCREMENTAL (each
// NDJSON frame carries only the delta bytes added since the previous
// frame, identical to OpenAI/Anthropic SSE; terminal frame has content="").
// Content frames therefore surface via StreamDelta (DeltaType "text") and
// CumulativeContent stays empty. ParseOllamaStreamChunk had zero production
// callers at the time of reversal (P4 not wired), so the contract switch
// is safe.
func TestParseOllamaStreamChunk_Delta(t *testing.T) {
	line := []byte(`{"model":"llama3.1","created_at":"2024-09-21","message":{"role":"assistant","content":"Hel"},"done":false}`)
	chunks, err := ParseOllamaStreamChunk(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	chunk := chunks[0]
	if chunk.SourceProtocol != ProtocolOllamaChat {
		t.Errorf("SourceProtocol = %q, want %q", chunk.SourceProtocol, ProtocolOllamaChat)
	}
	if chunk.Type != ChunkTypeDelta {
		t.Errorf("Type = %q, want %q", chunk.Type, ChunkTypeDelta)
	}
	// CONTRACT (R66, incremental): content is a per-frame delta fragment.
	// CumulativeContent MUST stay empty so no consumer treats a single
	// frame as the full assistant text; StreamDelta.Content carries the
	// delta and consumers reassemble by concatenation.
	if chunk.CumulativeContent != "" {
		t.Errorf("CumulativeContent = %q, want empty (incremental contract — full text is reconstructed by concatenating Delta.Content)", chunk.CumulativeContent)
	}
	if chunk.Delta == nil {
		t.Fatal("Delta = nil, want populated (incremental content must surface via Delta.Content)")
	}
	if chunk.Delta.Content != "Hel" {
		t.Errorf("Delta.Content = %q, want %q", chunk.Delta.Content, "Hel")
	}
	if chunk.Delta.DeltaType != "text" {
		t.Errorf("Delta.DeltaType = %q, want %q", chunk.Delta.DeltaType, "text")
	}
}

func TestParseOllamaStreamChunk_Reasoning(t *testing.T) {
	// Ollama 0.5+ emits `message.thinking` separately from `content`.
	line := []byte(`{"model":"deepseek-r1","message":{"role":"assistant","content":"","thinking":"Let me think"},"done":false}`)
	chunks, err := ParseOllamaStreamChunk(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	chunk := chunks[0]
	if chunk.Type != ChunkTypeDelta {
		t.Errorf("Type = %q, want %q", chunk.Type, ChunkTypeDelta)
	}
	if chunk.Delta == nil || chunk.Delta.ReasoningContent != "Let me think" {
		t.Errorf("Delta.ReasoningContent = %v, want %q", chunk.Delta, "Let me think")
	}
	if chunk.Delta.DeltaType != "reasoning" {
		t.Errorf("Delta.DeltaType = %q, want %q", chunk.Delta.DeltaType, "reasoning")
	}
	// Reasoning is incremental on Ollama's wire (not cumulative) and must not
	// touch the cumulative text channel.
	if chunk.CumulativeContent != "" {
		t.Errorf("CumulativeContent = %q, want empty on a reasoning-only frame", chunk.CumulativeContent)
	}
}

// TestParseOllamaStreamChunk_InterimNoopFrame: Ollama emits interim frames
// with done=false and empty content/thinking. They carry nothing surfaceable
// and must yield no chunks (not a zero-Typed chunk).
func TestParseOllamaStreamChunk_InterimNoopFrame(t *testing.T) {
	line := []byte(`{"model":"llama3.1","created_at":"2024-09-21","message":{"role":"assistant","content":""},"done":false}`)
	chunks, err := ParseOllamaStreamChunk(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("len(chunks) = %d, want 0 (interim frame carries nothing)", len(chunks))
	}
}

// TestParseOllamaStreamChunk_DoneWithContent covers the done+content same
// line case (r0924 fix-a task 2): the parser must emit TWO chunks — the
// incremental content delta first, then the terminal Done chunk — instead
// of letting chunk.Type overwrite the delta with Done.
func TestParseOllamaStreamChunk_DoneWithContent(t *testing.T) {
	line := []byte(`{"model":"llama3.1","message":{"role":"assistant","content":"Hello!"},"done_reason":"stop","done":true,"prompt_eval_count":10,"eval_count":20}`)
	chunks, err := ParseOllamaStreamChunk(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2 (content delta + done)", len(chunks))
	}

	delta := chunks[0]
	if delta.Type != ChunkTypeDelta {
		t.Errorf("chunks[0].Type = %q, want %q", delta.Type, ChunkTypeDelta)
	}
	// CONTRACT (R66, incremental): the terminal-line content "Hello!" is a
	// delta fragment, not a cumulative snapshot. Surface via Delta.Content;
	// CumulativeContent stays empty.
	if delta.CumulativeContent != "" {
		t.Errorf("chunks[0].CumulativeContent = %q, want empty (incremental contract)", delta.CumulativeContent)
	}
	if delta.Delta == nil || delta.Delta.Content != "Hello!" {
		t.Errorf("chunks[0].Delta = %+v, want Delta.Content=\"Hello!\"", delta.Delta)
	}
	if delta.Delta.DeltaType != "text" {
		t.Errorf("chunks[0].Delta.DeltaType = %q, want %q", delta.Delta.DeltaType, "text")
	}

	done := chunks[1]
	if done.Type != ChunkTypeDone {
		t.Errorf("chunks[1].Type = %q, want %q", done.Type, ChunkTypeDone)
	}
	if done.FinishReason != "stop" {
		t.Errorf("chunks[1].FinishReason = %q, want %q", done.FinishReason, "stop")
	}
	if done.Delta != nil || done.CumulativeContent != "" {
		t.Errorf("Done chunk must carry no content: Delta=%+v CumulativeContent=%q", done.Delta, done.CumulativeContent)
	}
	if done.Usage == nil {
		t.Fatal("chunks[1].Usage = nil, want populated")
	}
	if done.Usage.PromptTokens != 10 || done.Usage.CompletionTokens != 20 || done.Usage.TotalTokens != 30 {
		t.Errorf("Usage = %+v, want {10,20,30}", done.Usage)
	}
}

// TestParseOllamaStreamChunk_MultiFrameIncremental pins the R66 incremental
// contract end-to-end. WebFetch of upstream api.md confirms Ollama's wire
// format carries per-frame delta bytes only — concatenating successive
// frames' Delta.Content MUST yield the full assistant text. A consumer
// that mistook any single frame's content for the full text would emit
// wrong-size text downstream; this test catches that regression class.
func TestParseOllamaStreamChunk_MultiFrameIncremental(t *testing.T) {
	frames := []string{
		`{"model":"llama3.1","message":{"role":"assistant","content":"Hel"},"done":false}`,
		`{"model":"llama3.1","message":{"role":"assistant","content":"lo "},"done":false}`,
		`{"model":"llama3.1","message":{"role":"assistant","content":"world!"},"done":false}`,
		`{"model":"llama3.1","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`,
	}
	var assembled string
	for _, f := range frames {
		chunks, err := ParseOllamaStreamChunk([]byte(f))
		if err != nil {
			t.Fatalf("parse %q: %v", f, err)
		}
		for _, c := range chunks {
			if c.Type == ChunkTypeDelta && c.Delta != nil && c.Delta.DeltaType == "text" {
				assembled += c.Delta.Content
			}
		}
	}
	if assembled != "Hello world!" {
		t.Errorf("assembled Delta.Content = %q, want %q", assembled, "Hello world!")
	}
}

// TestParseOllamaStreamChunk_PureDoneLine: a terminal line with no
// content/thinking yields exactly one Done chunk.
func TestParseOllamaStreamChunk_PureDoneLine(t *testing.T) {
	line := []byte(`{"model":"llama3.1","done_reason":"stop","done":true,"prompt_eval_count":1,"eval_count":2}`)
	chunks, err := ParseOllamaStreamChunk(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1 (pure done line)", len(chunks))
	}
	if chunks[0].Type != ChunkTypeDone {
		t.Errorf("Type = %q, want %q", chunks[0].Type, ChunkTypeDone)
	}
	if chunks[0].FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", chunks[0].FinishReason)
	}
	if chunks[0].Usage == nil || chunks[0].Usage.TotalTokens != 3 {
		t.Errorf("Usage = %+v, want total 3", chunks[0].Usage)
	}
}

func TestParseOllamaStreamChunk_Error(t *testing.T) {
	line := []byte(`{"error":"model 'foo' not found"}`)
	chunks, err := ParseOllamaStreamChunk(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	chunk := chunks[0]
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
