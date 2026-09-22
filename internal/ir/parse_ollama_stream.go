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
func ParseOllamaStreamChunk(line []byte) (*StreamChunk, error) {
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

	chunk := &StreamChunk{
		SourceProtocol: ProtocolOllamaChat,
		Model:          raw.Model,
	}

	// Error chunk (rare, but Ollama surfaces some upstream errors here).
	if raw.Error != "" {
		chunk.Type = ChunkTypeError
		chunk.Error = &StreamError{
			Type:    "upstream_error",
			Message: raw.Error,
		}
		return chunk, nil
	}

	// Delta content (cumulative — Ollama does not split the assistant text
	// into character-by-character chunks the way SSE does; downstream
	// synthesizers emit `delta.content` as the *new* bytes since the last
	// chunk by diffing against the previous cumulative value).
	if raw.Message.Content != "" {
		chunk.Type = ChunkTypeDelta
		chunk.CumulativeContent = raw.Message.Content
		chunk.Delta = &StreamDelta{Content: raw.Message.Content, DeltaType: "text"}
	}

	// Reasoning content (Ollama's `message.thinking`, separate from `content`).
	if raw.Message.Thinking != "" {
		if chunk.Delta == nil {
			chunk.Type = ChunkTypeDelta
			chunk.Delta = &StreamDelta{}
		}
		chunk.Delta.ReasoningContent = raw.Message.Thinking
		chunk.Delta.DeltaType = "reasoning"
	}

	// Terminal chunk carries usage + done_reason.
	if raw.Done {
		// If we already emitted a delta for this same line, the executor's
		// stream assembler will treat the *next* call as the terminator. We
		// flag the type as Done here so a one-line done+content response
		// (small completions, e.g. "yes" / "no") still surfaces the usage.
		chunk.Type = ChunkTypeDone
		chunk.FinishReason = mapOllamaDoneReason(raw.DoneReason)
		if raw.PromptEvalCount > 0 || raw.EvalCount > 0 {
			chunk.Usage = &StreamUsage{
				PromptTokens:     raw.PromptEvalCount,
				CompletionTokens: raw.EvalCount,
				TotalTokens:      raw.PromptEvalCount + raw.EvalCount,
			}
		}
	}

	return chunk, nil
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
