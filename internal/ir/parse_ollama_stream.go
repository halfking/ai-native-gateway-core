package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ParseOllamaStreamChunk parses one line of Ollama's NDJSON chat-completion
// stream into IR StreamChunk. Added in 2026-09-21 audit (P2-1) so that
// gateways serving Ollama-backed providers can reuse the same downstream
// stream-synthesizer as the SSE-based dialects without special-casing the
// NDJSON framing in every executor.
//
// Ollama native chat stream format (application/x-ndjson, one JSON object per
// line, terminated by a line whose `done` is true):
//
//	{"model":"llama3.1","created_at":"2024-...","message":{"role":"assistant","content":""},"done":false}
//	{"model":"llama3.1","created_at":"2024-...","message":{"role":"assistant","content":"Hel"},"done":false}
//	...
//	{"model":"llama3.1","created_at":"2024-...","message":{"role":"assistant","content":"Hello!","done_reason":"stop"},
//	 "done":true,"total_duration":...,"prompt_eval_count":10,"eval_count":20}
//
// Note: Ollama's wire format uses:
//   - top-level `done` boolean (not `[DONE]` sentinel like OpenAI SSE)
//   - top-level `done_reason` only on the terminal chunk
//   - reasoning content as a sibling string field `message.thinking`
//     (NOT a content part; Ollama 0.5+ on supported models)
//
// The parser is intentionally tolerant: empty lines and unknown fields are
// ignored, only `done`/`message.content`/`message.thinking` are surfaced
// into the IR StreamChunk.
//
// # CONTRACT (cumulative content — audit r0924 fix-a task 1)
//
// Ollama NDJSON `message.content` is a CUMULATIVE value (the full assistant
// text so far), NOT incremental delta bytes like OpenAI/Anthropic SSE.
// ParseOllamaStreamChunk therefore carries it ONLY in
// StreamChunk.CumulativeContent and leaves StreamChunk.Delta nil on content
// frames. Every wire-level StreamDelta.Content in this repo is defined as
// incremental bytes; consumers MUST diff the cumulative value against the
// previously seen one before emitting delta.content downstream. Do NOT copy
// CumulativeContent into Delta.Content here — that re-emits the whole text
// on every frame (client-visible text duplication).
//
// A single NDJSON line may produce MORE THAN ONE chunk: a terminal line that
// also carries content (one-shot answers like "yes"/"no") yields the content
// chunk first, then the Done chunk, so the cumulative text and the
// usage/finish_reason are never collapsed into one ambiguous frame.
//
// Returns (nil, nil) for lines that carry neither error, content, thinking,
// nor done (e.g. Ollama's interim ping frames) — nothing to surface.
func ParseOllamaStreamChunk(line []byte) ([]*StreamChunk, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, fmt.Errorf("empty line")
	}

	var raw struct {
		Model     string `json:"model"`
		CreatedAt string `json:"created_at"`
		Message   struct {
			Role     string `json:"role"`
			Content  string `json:"content"`
			Thinking string `json:"thinking"`
		} `json:"message"`
		Done            bool   `json:"done"`
		DoneReason      string `json:"done_reason"`
		TotalDuration   int64  `json:"total_duration"`
		LoadDuration    int64  `json:"load_duration"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
		EvalDuration    int64  `json:"eval_duration"`
		Error           string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal ollama ndjson chunk: %w", err)
	}

	// Error chunk (rare, but Ollama surfaces some upstream errors here).
	if raw.Error != "" {
		return []*StreamChunk{{
			SourceProtocol: ProtocolOllamaChat,
			Model:          raw.Model,
			Type:           ChunkTypeError,
			Error: &StreamError{
				Type:    "upstream_error",
				Message: raw.Error,
			},
		}}, nil
	}

	next := func() *StreamChunk {
		return &StreamChunk{
			SourceProtocol: ProtocolOllamaChat,
			Model:          raw.Model,
		}
	}

	var chunks []*StreamChunk

	// Delta content. CONTRACT: Ollama's `message.content` is cumulative (see
	// the function-level contract comment) — it is surfaced through
	// CumulativeContent only; Delta stays nil so no consumer can mistake the
	// full text for incremental bytes and duplicate it on the wire.
	if raw.Message.Content != "" {
		c := next()
		c.Type = ChunkTypeDelta
		c.CumulativeContent = raw.Message.Content
		chunks = append(chunks, c)
	}

	// Reasoning content (Ollama's `message.thinking`, separate from `content`).
	if raw.Message.Thinking != "" {
		c := next()
		c.Type = ChunkTypeDelta
		c.Delta = &StreamDelta{
			ReasoningContent: raw.Message.Thinking,
			DeltaType:        "reasoning",
		}
		chunks = append(chunks, c)
	}

	// Terminal chunk carries usage + done_reason. It is its OWN chunk so a
	// one-line done+content response (small completions, e.g. "yes" / "no")
	// yields a clean (content delta, done) pair instead of one frame whose
	// Type overwrites the delta.
	if raw.Done {
		c := next()
		c.Type = ChunkTypeDone
		c.FinishReason = mapOllamaDoneReason(raw.DoneReason)
		if raw.PromptEvalCount > 0 || raw.EvalCount > 0 {
			c.Usage = &StreamUsage{
				PromptTokens:     raw.PromptEvalCount,
				CompletionTokens: raw.EvalCount,
				TotalTokens:      raw.PromptEvalCount + raw.EvalCount,
			}
		}
		chunks = append(chunks, c)
	}

	return chunks, nil
}

// mapOllamaDoneReason converts Ollama's terminal `done_reason` into the
// OpenAI-style FinishReason string the rest of the pipeline speaks.
//
// Ollama values (when present): "stop" | "load" | "unload" — only "stop"
// corresponds to a normal completion; the other two are model-lifecycle
// signals not produced during a chat request. Default to "stop" when the
// upstream omits the field on a `done` chunk.
func mapOllamaDoneReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "", "stop":
		return "stop"
	case "length":
		return "length"
	case "load", "unload":
		// Model lifecycle signals are not completion reasons; map to stop
		// so downstream classifiers don't treat the chunk as an error.
		return "stop"
	default:
		return reason
	}
}
