package admin

import (
	"encoding/json"
	"log/slog"
)

// decodeStoredJSON decodes a JSONB column captured for an API response.
//
// A decode failure must not be swallowed. The field is left nil, the API emits
// null, and that is byte-for-byte what "no body was stored for this turn" looks
// like — the same ambiguity that let a request_body loss bug survive six fix
// attempts (see docs/VIBECODING_GUIDELINES.md). Logging the failure is what
// makes "the UI shows no body" diagnosable.
//
// Returns the decoded value, or nil when raw is empty or undecodable. Only the
// field name, request id and byte size are logged — these columns hold user
// prompts.
func decodeStoredJSON(field, requestID string, raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		slog.Warn("data loss: stored body decode failed, field will render as null",
			"field", field,
			"request_id", requestID,
			"bytes", len(raw),
			"error", err)
		return nil
	}
	return v
}
