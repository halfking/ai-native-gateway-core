package streaming

import (
	"encoding/json"
	"log/slog"
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
