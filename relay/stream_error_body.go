package relay

import (
	"encoding/json"
	"strings"
)

// jsonErrorBody is the inner error object inside the standard
// `{"error": {...}}` envelope used by OpenAI / Anthropic / most
// proxy-style upstreams. The fields are deliberately permissive:
//   - Type:    the upstream's semantic classification
//              (e.g. "service_unavailable", "insufficient_quota")
//   - Code:    the upstream's machine-readable code when Type is
//              absent (some upstreams use one or the other, never both)
//   - Message: human-readable reason
//   - Param:   optional structured field (kept so a future audit
//              column can surface it without re-parsing the body)
type jsonErrorBody struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
}

// jsonErrorEnvelope is a single struct that tolerates BOTH of
// the production shapes we have observed (2026-06-20 audit):
//
//	{"error": {"type": "...", "message": "..."}}    — OpenAI / proxies
//	{"error": {"code": "...", "message": "..."}}    — quota / billing
//	{"type": "...", "message": "..."}               — Anthropic-style
//
// The pointer + plain fields mean a single Unmarshal populates
// the right slot regardless of which shape arrived. The caller
// then resolves which slot is non-empty.
type jsonErrorEnvelope struct {
	Error   *jsonErrorBody `json:"error,omitempty"`
	Type    string         `json:"type,omitempty"`
	Code    string         `json:"code,omitempty"`
	Message string         `json:"message,omitempty"`
}

// resolveError returns the (kind, message) pair for an envelope
// regardless of which shape it took. kind is preferred from
// inside the "error" wrapper when present, then from the
// top-level type / code fields. message is preferred from the
// inner wrapper, then from the top-level message.
func (e *jsonErrorEnvelope) resolveError() (kind string, message string) {
	if e.Error != nil {
		kind = firstNonEmpty(e.Error.Code, e.Error.Type)
		message = e.Error.Message
	}
	if kind == "" {
		kind = firstNonEmpty(e.Code, e.Type)
	}
	if message == "" {
		message = e.Message
	}
	return kind, message
}

// isJSONErrorBody returns true when the given raw bytes look like
// a non-SSE JSON error body returned by an upstream provider. The
// caller is expected to pass either the entire body or just the
// first line; the function tolerates trailing whitespace, SSE
// "data: " prefixes, and both envelope shapes (with and without
// the outer "error" wrapper).
//
// Recognised shapes (audit 2026-06-20):
//   - {"error": {"type": "service_unavailable", "message": "..."}}
//   - {"error": {"code": "insufficient_quota", "message": "..."}}
//   - {"type": "upstream_error", "message": "..."}   (Anthropic bare)
//
// Returns (true, errorType, errorMessage) on match, (false, "", "")
// otherwise. errorType feeds the audit "failure_detail_code" column
// (so operators can group "积分不足" hits together) and errorMessage
// is the human-readable reason that goes into slog + response_body
// preview.
func isJSONErrorBody(body []byte) (bool, string, string) {
	if len(body) == 0 {
		return false, "", ""
	}
	// Trim trailing whitespace and stray SSE terminator fragments
	// so a body of `{"error":...}\n\n` still parses.
	trimmed := strings.TrimRight(string(body), " \t\r\n")
	if trimmed == "" {
		return false, "", ""
	}
	// Defensive: a stream reader might pass an SSE-prefixed line
	// to this helper by mistake. Strip the prefix and re-test
	// the JSON shape so the helper stays robust to that call site
	// bug. After stripping, we still require the body to start
	// with '{' so legitimate SSE comments (":heartbeat") and
	// "event:" lines don't false-positive.
	if strings.HasPrefix(trimmed, "data:") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	}
	if trimmed == "" || trimmed[0] != '{' {
		return false, "", ""
	}
	var env jsonErrorEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return false, "", ""
	}
	kind, msg := env.resolveError()
	if kind == "" && msg == "" {
		return false, "", ""
	}
	return true, kind, msg
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
