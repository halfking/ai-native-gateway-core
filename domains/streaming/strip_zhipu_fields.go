package streaming

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
)

// zhipuPrivateFields captures GLM/Zhipu fields that should not leak to clients.
// Source: 2026-07-11 production capture (252, request_logs_hot, provider_id=32).
// Verified against 524 successful responses on glm-5.x models.
// Top-level keys (id/model/created) are kept (standard OpenAI compat).
var zhipuPrivateFields = []string{
	"system_fingerprint",
	"zhipu_request_id",
	"web_search_results",
	"retrieval_documents",
	"model_version",
	"sensitive_word_check",
	"request_id",
}

// zhipuPrivateNestedFields are dotted paths inside the response JSON.
// Real captures show GLM-5.x exposes Anthropic-style usage extensions and a
// MiniMax/DeepSeek-R1-style reasoning chain in the assistant message.
var zhipuPrivateNestedFields = []string{
	"usage.prompt_tokens_details",     // {cached_tokens: 0}
	"usage.completion_tokens_details", // {reasoning_tokens: 2}
	"choices.0.message.reasoning_content",
}

func StripZhipuFieldsBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	stripped := 0
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	for _, k := range zhipuPrivateFields {
		if _, ok := raw[k]; ok {
			delete(raw, k)
			stripped++
		}
	}
	for _, p := range zhipuPrivateNestedFields {
		if stripNestedPath(raw, p) {
			stripped++
		}
	}
	if stripped == 0 {
		return body
	}
	out, err := json.Marshal(raw)
	if err != nil {
		slog.Warn("strip_zhipu: marshal failed, returning original body", "error", err)
		return body
	}
	return out
}

// stripNestedPath deletes a dotted JSON path and reports whether anything was removed.
// Example paths:
//
//	"usage.prompt_tokens_details"        - drops the keyed object under `usage`
//	"choices.0.message.reasoning_content" - traverses array index, then nested objects
//
// Returns true when the final segment existed and was deleted. The path
// implementation re-unmarshals the root into interface{} so it can descend
// through arbitrary JSON structures, then re-marshals back into root.
// This is acceptable: stripper is on the response post-processing path, not hot.
func stripNestedPath(root map[string]json.RawMessage, path string) bool {
	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return false
	}
	buf, err := json.Marshal(root)
	if err != nil {
		return false
	}
	var anyRoot map[string]any
	if err := json.Unmarshal(buf, &anyRoot); err != nil {
		return false
	}
	if !stripPathDescend(anyRoot, parts) {
		return false
	}
	buf2, err := json.Marshal(anyRoot)
	if err != nil {
		return false
	}
	if err := json.Unmarshal(buf2, &root); err != nil {
		return false
	}
	return true
}

func stripPathDescend(node any, parts []string) bool {
	cur := node
	for i, seg := range parts {
		switch n := cur.(type) {
		case map[string]any:
			child, ok := n[seg]
			if !ok {
				return false
			}
			if i == len(parts)-1 {
				delete(n, seg)
				return true
			}
			cur = child
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(n) {
				return false
			}
			if i == len(parts)-1 {
				return false
			}
			cur = n[idx]
		default:
			return false
		}
	}
	return false
}
