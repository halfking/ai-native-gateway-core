package streaming

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// Top-level MiniMax fields observed in production (provider_id=14 on 252).
// Source: 2026-07-11 production capture, ~16400 successful responses.
// MiniMax returns standard {id, model, created, choices, usage} plus these.
//
// 2026-07-12 audit fix (audit-09): 移除 OpenAI compatible 标准字段。
// - object、system_fingerprint 是 OpenAI compatible 标准字段，保留供客户端解析
// - service_tier 是请求/响应官方定价字段，billing 依赖，保留
var minimaxPrivateFields = []string{
	"nvext",
	"audio_content",
	"name",
	"input_sensitive",
	"input_sensitive_type",
	"output_sensitive",
	"output_sensitive_type",
	"output_sensitive_int",
	"base_resp",
	"request_id",
	"workflow_run_id",
	"created_by",
	"usage_extra",
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
	if choicesRaw, ok := raw["choices"]; ok {
		var choices []map[string]json.RawMessage
		if json.Unmarshal(choicesRaw, &choices) == nil {
			for i, choice := range choices {
				for _, field := range []string{"message", "delta"} {
					messageRaw, ok := choice[field]
					if !ok {
						continue
					}
					var message map[string]json.RawMessage
					if json.Unmarshal(messageRaw, &message) != nil {
						continue
					}
					if cleanMinimaxLeakFields(message) {
						choices[i][field], _ = json.Marshal(message)
						stripped++
					}
				}
			}
			raw["choices"], _ = json.Marshal(choices)
		}
	}
	// 2026-07-12 audit fix: removed nested field stripping.
	// usage detail (total_characters, cache_read_tokens, prompt/completion_tokens_details)
	// and choices.0.message.reasoning are now preserved for billing/audit.
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

// cleanMinimaxLeakFields removes the private function-call envelope that some
// MiniMax-compatible upstreams leak into reasoning_content/content. It is
// deliberately gated on <function_calls> so ordinary XML/code in an answer is
// preserved. A malformed 245 sample omitted the final '>' in the closing tag;
// when no closing marker is present the leaked suffix is still not user-facing
// content, so it is discarded through the end of the field.
func cleanMinimaxLeakFields(message map[string]json.RawMessage) bool {
	changed := false
	for _, field := range []string{"reasoning_content", "content"} {
		raw, ok := message[field]
		if !ok {
			continue
		}
		var text string
		if json.Unmarshal(raw, &text) != nil || !strings.Contains(text, "<function_calls>") {
			continue
		}
		start := strings.Index(text, "<function_calls>")
		if start < 0 {
			continue
		}
		cleaned := strings.TrimSpace(text[:start])
		if end := strings.Index(text[start:], "</tool_call>"); end >= 0 {
			before := strings.TrimSpace(text[:start])
			after := strings.TrimSpace(text[start+end+len("</tool_call>"):])
			switch {
			case before == "":
				cleaned = after
			case after == "":
				cleaned = before
			default:
				cleaned = before + " " + after
			}
		}
		message[field], _ = json.Marshal(cleaned)
		changed = true
	}
	return changed
}
