package ir

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ErrSerializeOllamaMissingModel is returned by SerializeOllama when the
// InternalRequest lacks a model identifier — Ollama /api/chat refuses a
// request without a `model` field, and silently substituting a default
// would mask client bugs.
//
// RESERVED(r0924 supplier-protocol-optimization §3.6): the executor that
// drives this serializer is responsible for surfacing this error verbatim
// to the client (typically as a 400 with the message), not as a 500.
var ErrSerializeOllamaMissingModel = fmt.Errorf("serialize ollama: model is required")

// OllamaExtensionPrefix is the namespace under which Ollama-private top-level
// fields (options.*, format, keep_alive, raw) are passed through the IR
// Extensions map. The serialize layer reads these keys and lifts them to
// the right position in the Ollama body — the executor MUST NOT strip them
// upstream (see docs/供应商协议优化-实施规划.md §3.3 SSOT).
const OllamaExtensionPrefix = "ollama."

// SerializeOllama converts an InternalRequest into the Ollama /api/chat native
// request body. The wire shape differs from OpenAI Chat Completions in
// three material ways (see docs/vendor-formats/ollama.md):
//
//   1. Sampling params live under an `options` object (temperature, top_p,
//      top_k, num_predict ← max_tokens, seed, stop, repeat_penalty).
//   2. JSON-output mode is a top-level `format` string ("json") or schema
//      object — OpenAI's `response_format` object is REJECTED by Ollama.
//   3. Reasoning content lives in `message.thinking` (cumulative) — not in
//      `choices[].delta.reasoning_content`. On the request side this
//      function does not emit a thinking block; reasoning budget is
//      configured via `Extensions["ollama.options.num_ctx"]` and friends.
//
// Field mapping (cf. §2.2):
//
//	IR                                 → Ollama native wire
//	─────────────────────────────────────────────────────────────
//	req.Model                          → model
//	req.Messages (string content)      → messages[{role, content}]
//	req.Stream                         → stream
//	req.Temperature                    → options.temperature
//	req.TopP                           → options.top_p
//	req.TopK                           → options.top_k
//	req.MaxTokens (>0)                 → options.num_predict
//	req.Stop                           → options.stop
//	req.Seed                           → options.seed
//	req.RepetitionPenalty              → options.repeat_penalty
//	Extensions["ollama.options.<k>"]   → options.<k>      (private passthrough)
//	Extensions["ollama.format"]        → format           (top-level, string OR
//	                                                       schema object)
//	Extensions["ollama.keep_alive"]    → keep_alive
//	Extensions["ollama.raw"]           → raw
//	req.Tools                          → tools            (OpenAI-shape compatible,
//	                                                       Ollama 0.5+)
//
// Entries reserved in `Extensions` under any other "ollama.<key>" prefix
// are passed through verbatim to the top-level of the body — this is the
// "all Ollama-private keys land somewhere" contract (§2.6). The function
// will NOT emit fields it does not recognize, so unknown keys from other
// dialects (e.g. anthropic-style `system`) do not leak into Ollama.
//
// Errors:
//   - nil request → "serialize ollama: nil request"
//   - missing model → ErrSerializeOllamaMissingModel
//
// RESERVED(r0924 supplier-protocol-optimization §2.6.2 rule 1): callers MUST
// invoke this function only when req.SourceProtocol == ProtocolOllamaChat
// OR when an explicit decision-pass-through is in effect for the
// ollama-native endpoint. The dispatcher is the gatekeeper; do not call
// this from a generic serialize path.
func SerializeOllama(req *InternalRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("serialize ollama: nil request")
	}
	if req.Model == "" {
		return nil, ErrSerializeOllamaMissingModel
	}

	body := map[string]any{
		"model":    req.Model,
		"messages": serializeOllamaMessages(req.Messages),
	}
	// Always emit `stream` to match Ollama's documented body shape (the
	// Ollama server defaults stream=false; we still emit explicitly so
	// downstream proxies / mirrors do not have to reason about defaults).
	body["stream"] = req.Stream

	// 1. Sampling parameters → options sub-object.
	opts := map[string]any{}
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		opts["top_p"] = *req.TopP
	}
	if req.TopK != nil {
		opts["top_k"] = *req.TopK
	}
	if req.MaxTokens > 0 {
		// num_predict: Ollama's max_tokens-equivalent. IR's MaxTokens=0
		// means "unlimited"; we deliberately omit the key in that case.
		opts["num_predict"] = req.MaxTokens
	}
	if len(req.Stop) > 0 {
		opts["stop"] = req.Stop
	}
	if req.Seed != nil {
		opts["seed"] = *req.Seed
	}
	if req.RepetitionPenalty != nil {
		opts["repeat_penalty"] = *req.RepetitionPenalty
	}
	// Pass through private options.* — naming convention is
	// "ollama.options.<native-key>" so a future protocol normalization
	// step can strip provider-namespace without losing the inner key.
	for k, v := range req.Extensions {
		const optsNS = OllamaExtensionPrefix + "options."
		if strings.HasPrefix(k, optsNS) {
			opts[strings.TrimPrefix(k, optsNS)] = v
		}
	}
	if len(opts) > 0 {
		body["options"] = opts
	}

	// 2. Top-level Ollama-private keys (anything under "ollama." that
	// wasn't already lifted into `options`). Special-case `format` /
	// `keep_alive` / `raw` so callers don't have to remember the namespace.
	for k, v := range req.Extensions {
		if !strings.HasPrefix(k, OllamaExtensionPrefix) {
			continue
		}
		if strings.HasPrefix(k, OllamaExtensionPrefix+"options.") {
			continue // already lifted in step 1
		}
		topKey := strings.TrimPrefix(k, OllamaExtensionPrefix)
		if topKey == "" {
			continue
		}
		// Don't clobber keys we already emitted via the sampling block.
		// Today this only applies to `model` (never emitted under
		// extensions) and `messages` / `stream` (set above) — guard
		// explicitly so future additions stay safe.
		if _, ok := body[topKey]; ok {
			continue
		}
		body[topKey] = v
	}

	// 3. Tools — Ollama 0.5+ accepts the OpenAI tool schema verbatim.
	// We do NOT translate; the IR ToolDefinition's `Parameters` json.RawMessage
	// is forwarded as-is so the upstream sees the canonical OpenAI shape.
	if len(req.Tools) > 0 {
		body["tools"] = serializeOllamaTools(req.Tools)
	}

	return json.Marshal(body)
}

// serializeOllamaMessages flattens IR messages into Ollama's wire shape.
// Ollama native chat accepts {role, content}; role ∈ {user, assistant,
// system, tool}. We coalesce multi-block content into a single string when
// every block is text; otherwise we fall back to leaving Content as the
// empty string and surface a textual summary to preserve fidelity (full
// multimodal Ollama support is out of P3 scope — the executor MUST reject
// multimodal requests when the inbound protocol was Ollama-native, per
// schema gate in §2.6.1).
func serializeOllamaMessages(msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{
			"role":    m.Role,
			"content": ollamaMessageContent(m),
		})
	}
	return out
}

// ollamaMessageContent returns the string content for an Ollama message,
// or "" if the message carries non-text blocks. Multi-block messages with
// only text blocks are joined with "\n" to preserve semantic structure for
// the upstream model.
func ollamaMessageContent(m Message) string {
	if len(m.Content) == 0 {
		return ""
	}
	allText := true
	for _, b := range m.Content {
		if b.Type != "text" {
			allText = false
			break
		}
	}
	if !allText {
		// Non-text block present (image / audio / tool_use / etc.). The
		// Ollama-native contract in §2.6.1 says passthrough schema must
		// reject multimodal at the gate; arriving here means the gate
		// was bypassed. We return "" rather than fabricating a payload —
		// Ollama will respond with `error: "invalid chat message"`, which
		// is the documented behavior for an IR-truncated block.
		return ""
	}
	parts := make([]string, 0, len(m.Content))
	for _, b := range m.Content {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// serializeOllamaTools converts IR ToolDefinitions into Ollama's tool
// schema. Ollama 0.5+ accepts the OpenAI tool format verbatim:
// {"type": "function", "function": {"name": ..., "description": ...,
// "parameters": <JSON Schema>}} — which matches IR ToolDefinition with
// Parameters as json.RawMessage. The conversion passes Parameters through
// directly via RawMessage so the upstream sees the canonical schema.
func serializeOllamaTools(tools []ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		fn := map[string]any{
			"name": t.Name,
		}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		if len(t.Parameters) > 0 {
			fn["parameters"] = json.RawMessage(t.Parameters)
		}
		out = append(out, map[string]any{
			"type":     "function",
			"function": fn,
		})
	}
	return out
}