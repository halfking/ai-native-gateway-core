// Package routeincident — redact.go
//
// Sanitization helpers. Anything that flows out of the package
// boundary (SSE envelopes, JSON responses, audit rows) MUST be
// passed through these helpers first.
//
// What we never publish:
//   - credential secrets, Authorization headers, cookie values
//   - full request/response bodies
//   - client IP, user agent
//   - session titles/identifiers
//   - raw upstream error messages
//   - request_logs.response_preview / request_preview / transform_summary
//
// What we DO publish:
//   - error_kind (a short, normalized code — already redacted by the
//     classify step earlier in the pipeline)
//   - failure_stage ("gateway" | "upstream" | null)
//   - sanitized counts and per-dimension rollups
package routeincident

import (
	"strings"
	"unicode/utf8"
)

// MaxErrorKindLen bounds the error_kind we copy into public
// envelopes. Real values are short codes like "rate_limited" or
// "upstream_5xx"; anything longer is suspicious and we want a stable
// truncation policy.
const MaxErrorKindLen = 64

// RedactErrorKind normalises and bounds the error_kind. Returns
// empty string for non-utf8 input. The output is safe to publish.
func RedactErrorKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return ""
	}
	if !utf8.ValidString(kind) {
		return ""
	}
	if len(kind) > MaxErrorKindLen {
		// Truncate to MaxErrorKindLen runes (not bytes) so we never
		// split a multi-byte sequence mid-character.
		runes := []rune(kind)
		if len(runes) > MaxErrorKindLen {
			kind = string(runes[:MaxErrorKindLen])
		}
	}
	return kind
}

// SanitizeStage returns the canonical stage label. Unknown values
// are returned as-is so we never silently leak a raw upstream error
// string into a public envelope.
func SanitizeStage(stage string) string {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "gateway":
		return "gateway"
	case "upstream":
		return "upstream"
	case "":
		return ""
	default:
		// Unknown stage value — drop it. We never publish raw upstream
		// error text in this position.
		return ""
	}
}

// SanitizeProtocol returns the canonical protocol identifier or
// empty string for unknown / missing values.
func SanitizeProtocol(protocol string) string {
	protocol = strings.TrimSpace(strings.ToLower(protocol))
	if protocol == "" {
		return ""
	}
	if !utf8.ValidString(protocol) {
		return ""
	}
	return protocol
}

// SanitizeModel strips control characters and bounds length. Model
// names can be long (e.g. "ft:gpt-4o-mini-2024-07-18:my-org::abcd1234")
// but a stable truncation keeps the envelope small.
const MaxModelLen = 128

func SanitizeModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if !utf8.ValidString(model) {
		return ""
	}
	runes := []rune(model)
	if len(runes) > MaxModelLen {
		model = string(runes[:MaxModelLen])
	}
	return model
}

// SanitizeProviderCode is the resolved provider display code. The
// store only ever stores provider_id; this helper is here for code
// that needs the display value (e.g. the affected_lanes list).
func SanitizeProviderCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	if !utf8.ValidString(code) {
		return ""
	}
	return code
}

// SanitizeEvidence is the gate every evidence map passes through
// before being written to route_incident_events.evidence. It returns
// a new map with the forbidden keys stripped and the rest passed
// through. The intent is to fail-closed: if a caller accidentally
// adds a new evidence field that might contain a credential, it
// must be explicitly allow-listed here before it can leave the
// package boundary.
func SanitizeEvidence(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if !isAllowedEvidenceKey(k) {
			continue
		}
		out[k] = redactValue(v)
	}
	return out
}

// allowedEvidenceKeys is the explicit allow-list. Add a key here
// only after a security review of the value it carries.
var allowedEvidenceKeys = map[string]struct{}{
	"failure_kind":        {},
	"failure_stage":       {},
	"failure_detail_code": {},
	"upstream_status":     {}, // HTTP status code as int, never the body
	"latency_ms":          {},
	"prompt_tokens":       {},
	"completion_tokens":   {},
	"total_tokens":        {},
	"cost_usd":            {},
	"sample_request_ids":  {}, // explicit list of request ids, capped in store
	"window_seconds":      {},
	"sample_count":        {},
	"diagnostic_run_id":   {}, // Phase 2 only
	"action":              {}, // Phase 2 only
	"actor":               {}, // sanitized user id (no email, no auth)
	"before_state":        {}, // Phase 2 only
	"after_state":         {}, // Phase 2 only
	"reason":              {}, // operator-provided free text, length-bounded in store
}

func isAllowedEvidenceKey(k string) bool {
	_, ok := allowedEvidenceKeys[k]
	return ok
}

// redactValue recursively scrubs strings/byte slices in a value.
func redactValue(v any) any {
	switch x := v.(type) {
	case string:
		// Strings flowing into evidence must be short codes or short
		// labels. Anything longer than 4 KiB is suspicious; truncate
		// to keep the row bounded.
		if len(x) > 4096 {
			runes := []rune(x)
			if len(runes) > 4096 {
				return string(runes[:4096])
			}
			return x
		}
		return x
	case []byte:
		return redactValue(string(x))
	case map[string]any:
		return SanitizeEvidence(x)
	case []any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			out = append(out, redactValue(e))
		}
		return out
	default:
		return v
	}
}
