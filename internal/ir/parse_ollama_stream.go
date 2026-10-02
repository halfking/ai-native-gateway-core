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
// # CONTRACT (incremental content — R66 audit round)
//
// Ollama NDJSON `message.content` carries INCREMENTAL delta bytes per
// frame, identical to OpenAI/Anthropic SSE — the full assistant text
// must be reconstructed by concatenating successive frames' content.
// WebFetch of https://github.com/ollama/ollama/blob/main/docs/api.md
// (2026-09-25) confirms the official example carries only delta
// fragments (e.g. "The"); the terminal frame carries content="".
// ParseOllamaStreamChunk therefore surfaces content via StreamDelta
// (DeltaType "text") and leaves CumulativeContent empty on content
// frames, matching every other dialect in this repo.
//
// The prior r0924 "cumulative" hypothesis was a misreading of the wire
// format (the comment block below used to claim Ollama repeats the
// full assistant text on every frame, which the official docs do not
// support). Consumers that now diff successive Delta.Content values
// reassemble the full text losslessly; consumers that previously read
// CumulativeContent were never shipped (zero callers, P4 not wired) so
// the contract reversal is safe.
//
// Reasoning content (`message.thinking`) was always incremental and is
// unaffected.
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

	// Delta content. CONTRACT: Ollama's `message.content` is incremental (see
	// the function-level contract comment) — it is surfaced through
	// StreamDelta.Content so it composes with every other dialect's delta
	// path. CumulativeContent stays empty so no consumer mistakes the
	// delta for a final snapshot.
	if raw.Message.Content != "" {
		c := next()
		c.Type = ChunkTypeDelta
		c.Delta = &StreamDelta{
			Content:   raw.Message.Content,
			DeltaType: "text",
		}
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
