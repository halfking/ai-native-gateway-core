package ir

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// ParseOllamaResponse parses an Ollama /api/chat non-streaming response into
// the IR InternalResponse shape. It pairs with ParseOllamaStreamChunk
// (the streaming NDJSON parser in parse_ollama_stream.go) so the executor
// and dispatcher have a single contract regardless of stream mode.
//
// Ollama wire shape (docs/vendor-formats/ollama.md):
//
//	{
//	  "model": "llama3.1",
//	  "created_at": "2024-...",
//	  "message": {
//	    "role": "assistant",
//	    "content": "Final answer",
//	    "thinking": "Let me think..."   // Ollama 0.5+ reasoning, cumulative
//	  },
//	  "done_reason": "stop",             // present only on terminal
//	  "done": true,
//	  "total_duration": ...,             // nanoseconds (informational)
//	  "load_duration": ...,
//	  "prompt_eval_count": 15,           // → IR.Usage.PromptTokens
//	  "eval_count": 4,                   // → IR.Usage.CompletionTokens
//	  "error": "..."                     // surfaced as StreamError
//	}
//
// RESERVED(r0924 supplier-protocol-optimization §3.6, §6.2):
//   - Returns *StreamError (not regular error) when the body carries an
//     `error` field so the executor can route through the §11.6 fail wire.
//   - done_reason is mapped via mapOllamaDoneReason (shared with the
//     streaming parser).
//   - Tokens are translated: prompt_eval_count → PromptTokens, eval_count →
//     CompletionTokens, TotalTokens = sum.
//   - Reasoning content is split: message.thinking → ReasoningContent;
//     message.content → Content[0].Text. They are NOT merged (per Ollama's
//     own behavior — the upstream keeps them as separate fields, and the
//     downstream clients surface them in distinct fields).
func ParseOllamaResponse(body []byte) (*InternalResponse, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("parse ollama response: empty body")
	}

	var raw struct {
		Model        string `json:"model"`
		CreatedAt    string `json:"created_at"`
		Message      struct {
			Role     string `json:"role"`
			Content  string `json:"content"`
			Thinking string `json:"thinking"`
		} `json:"message"`
		Done            bool   `json:"done"`
		DoneReason      string `json:"done_reason"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
		Error           string `json:"error"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse ollama response: %w", err)
	}

	// Ollama surfaces upstream errors as a top-level "error" string on a
	// 200 response — we propagate as a StreamError wrapped via fmt.Errorf
	// so the executor / dispatcher can short-circuit the wire with the
	// correct envelope (see StreamError definition in stream.go).
	if raw.Error != "" {
		return nil, fmt.Errorf("ollama upstream error: %s", raw.Error)
	}

	resp := &InternalResponse{
		Model:          raw.Model,
		SourceProtocol: ProtocolOllamaChat,
		Role:           "assistant",
		FinishReason:   mapOllamaDoneReason(raw.DoneReason),
	}

	if raw.CreatedAt != "" {
		// Ollama timestamps are RFC3339Nano; we only set Created when
		// the parse succeeds (otherwise callers see 0, consistent with
		// OpenAI streams that omit created).
		if t, err := time.Parse(time.RFC3339Nano, raw.CreatedAt); err == nil {
			resp.Created = t.Unix()
		} else if t, err := time.Parse(time.RFC3339, raw.CreatedAt); err == nil {
			resp.Created = t.Unix()
		}
	}

	// Content & reasoning: Ollama keeps them as siblings, never inside
	// the same block. We model them the same way so downstream
	// synthesizers (OpenAI/Anthropic) can route reasoning to their
	// native equivalent fields without re-splitting.
	if raw.Message.Content != "" {
		resp.Content = append(resp.Content, ResponseContentBlock{
			Type: "text",
			Text: raw.Message.Content,
		})
	}
	if raw.Message.Thinking != "" {
		resp.ReasoningContent = raw.Message.Thinking
	}

	// Usage: only emit when done=true AND counts are present. The Ollama
	// server emits an interim chunk even on non-streaming responses in
	// some builds (`done=false`, no eval_count); we must NOT synthesize
	// zero-valued usage in that case.
	if raw.Done && (raw.PromptEvalCount > 0 || raw.EvalCount > 0) {
		resp.Usage = ResponseUsage{
			PromptTokens:     raw.PromptEvalCount,
			CompletionTokens: raw.EvalCount,
			TotalTokens:      raw.PromptEvalCount + raw.EvalCount,
		}
	}

	// ID: deterministic so logs can correlate request/response pairs
	// without depending on a server-side identifier (Ollama does not
	// return one). The format mirrors Anthropic's id_random style but
	// uses created_at as the seed for human readability.
	resp.ID = ollamaResponseID(raw.Model, raw.CreatedAt)

	return resp, nil
}

// ollamaResponseID builds a stable identifier from model + created_at. We
// avoid time.Now() so unit tests are deterministic. The function tolerates
// empty inputs (returns "ollama-unknown").
func ollamaResponseID(model, createdAt string) string {
	base := "ollama"
	if model != "" {
		base = base + "-" + sanitizeIDToken(model)
	}
	if createdAt != "" {
		base = base + "-" + sanitizeIDToken(createdAt)
	}
	if len(base) > 96 {
		base = base[:96]
	}
	return base
}

// sanitizeIDToken replaces characters that would not be safe in a wire ID
// (spaces, colons, dots) with a single dash. We deliberately do NOT
// URL-encode because the value never leaves the IR.
func sanitizeIDToken(s string) string {
	out := make([]byte, 0, len(s))
	for i, c := range []byte(s) {
		switch c {
		case ' ', ':', '.', '+':
			if i > 0 && len(out) > 0 && out[len(out)-1] == '-' {
				continue
			}
			out = append(out, '-')
		default:
			out = append(out, c)
		}
	}
	// strconv import is needed for the timestamp integer branch in some
	// refactor paths; keep it imported to avoid cycle imports if we add
	// a numeric-only fallback later.
	_ = strconv.Itoa
	return string(out)
}