package streaming

import (
	"encoding/json"
	"log/slog"
)

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
