package streaming

import (
	"encoding/json"
	"log/slog"
)

// Top-level MiniMax fields observed in production (provider_id=14 on 252).
// Source: 2026-07-11 production capture, ~16400 successful responses.
// MiniMax returns standard {id, model, created, choices, usage} plus these.
var minimaxPrivateFields = []string{
	"nvext",
	"audio_content",
	"name",
	"input_sensitive",
	"input_sensitive_type",
	"output_sensitive",
	"output_sensitive_type",
	"output_sensitive_int",
	"service_tier",
	"base_resp",
	"request_id",
	"workflow_run_id",
	"created_by",
	"object",
	"system_fingerprint",
	"usage_extra",
}

// Nested fields that MiniMax adds under standard structures:
//   - usage.total_characters / usage.cache_read_tokens
//     (Anthropic-style extension surface added on top of OpenAI shape)
//   - usage.prompt_tokens_details / usage.completion_tokens_details
//     (when thinking mode emits reasoning_tokens)
//   - choices.0.message.reasoning
//     (legacy reasoning surface; production sees it in 4 responses of 16K)
var minimaxPrivateNestedFields = []string{
	"usage.total_characters",
	"usage.cache_read_tokens",
	"usage.prompt_tokens_details",
	"usage.completion_tokens_details",
	"choices.0.message.reasoning",
}

func StripMinimaxFieldsBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	stripped := 0
	for _, k := range minimaxPrivateFields {
		if _, ok := raw[k]; ok {
			delete(raw, k)
			stripped++
		}
	}
	for _, p := range minimaxPrivateNestedFields {
		if stripNestedPath(raw, p) {
			stripped++
		}
	}
	if stripped == 0 {
		return body
	}
	out, err := json.Marshal(raw)
	if err != nil {
		slog.Warn("strip_minimax: marshal failed, returning original body", "error", err)
		return body
	}
	return out
}
