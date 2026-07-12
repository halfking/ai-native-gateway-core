package streaming

import (
	"encoding/json"
	"log/slog"
)

// zhipuPrivateFields captures GLM/Zhipu fields that should not leak to clients.
// Source: 2026-07-11 production capture (252, request_logs_hot, provider_id=32).
// Verified against 524 successful responses on glm-5.x models.
// Top-level keys (id/model/created) are kept (standard OpenAI compat).
//
// 2026-07-12 audit fix (audit-09): 移除官方/standard OpenAI compatible 字段。
// - system_fingerprint 是 OpenAI compatible 标准字段，客户端会期望看到
// - request_id 是 Anthropic/通用追踪字段，是公开 API metadata，不清理
var zhipuPrivateFields = []string{
	"zhipu_request_id",
	"web_search_results",
	"retrieval_documents",
	"model_version",
	"sensitive_word_check",
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
	// 2026-07-12 audit fix: removed nested field stripping.
	// usage.prompt_tokens_details, usage.completion_tokens_details, and
	// choices.0.message.reasoning_content are now preserved for billing/audit.
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
