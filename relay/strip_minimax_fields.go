package relay

import (
	"encoding/json"
	"log/slog"
)

// minimaxPrivateFields are upstream-private fields that leak through
// the minimax OpenAI-compatible chat endpoint into our relay output.
// OpenAI SDKs ignore unknown fields, but they inflate payload size,
// expose upstream identity to the end user, and sometimes contain
// nvext.worker_id that includes the prefix a browser DevTools network
// tab flags as suspicious. Stripping them at the relay layer is the
// single point of control.
var minimaxPrivateFields = []string{
	"nvext",               // minimax routing extension: nvext.worker_id
	"audio_content",       // voice-mode specific
	"name",                // minimax attaches assistant name "MiniMax AI" etc.
	"input_sensitive",          // boolean: input was sensitive
		"input_sensitive_type",     // string: type classification
		"output_sensitive",         // boolean: output was sensitive
		"output_sensitive_type",    // string: type classification
		"output_sensitive_int",     // int: numeric sensitivity level
	"service_tier",        // minimax custom field; OpenAI spec is "service_tier" too
	"base_resp",           // minimax internal status code mirror
	"request_id",          // some minimax variants
	"workflow_run_id",     // minimax internal
	"created_by",          // minimax provenance
	"object",              // sometimes minimax returns a different object name
	"system_fingerprint",  // only OpenAI uses this
	"usage_extra",         // minimax extension
}

// StripMinimaxFieldsBody removes minimax-private top-level fields from
// an OpenAI-format chat response body in-place (returns a new slice
// since json.Decoder/Encoder on []byte works on a copy). The function
// is a no-op for empty input or when no fields are present.
//
// Wired from main.go into ChatExecutor.StripMinimaxFields so the
// routing package doesn't import relay (the import direction is
// routing -> relay; relay cannot import routing).
func StripMinimaxFieldsBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		// Body wasn't a JSON object — return unchanged. The caller
		// will surface a parse error to the client as usual.
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
		slog.Warn("StripMinimaxFieldsBody: marshal failed; returning original body",
			"error", err, "stripped", stripped)
		return body
	}
	slog.Debug("StripMinimaxFieldsBody: stripped minimax-private fields",
		"count", stripped, "in_bytes", len(body), "out_bytes", len(out))
	return out
}
