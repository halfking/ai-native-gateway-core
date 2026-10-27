package sessionid

import "strings"

const maxCorrelationIDLen = 128

// SanitizeCorrelationID strips control/space-adjacent junk from a client-
// supplied correlation token (session id, task id, …). Empty, overlong, or
// character-set-invalid inputs return "".
func SanitizeCorrelationID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxCorrelationIDLen {
		return ""
	}
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return ""
	}
	return value
}

// SanitizeGwHeader accepts only gateway-namespaced session ids:
//
//	gw_<uuid>  — user main session
//	gt_<...>   — auto-title branch
//	gs_<...>   — auto-summary branch
//
// Bare UUIDs and other client tokens are rejected here; callers that want to
// honour them must go through DeriveGatewayID after SanitizeCorrelationID.
func SanitizeGwHeader(v string) string {
	s := SanitizeCorrelationID(v)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "gw_") && !strings.HasPrefix(s, "gt_") && !strings.HasPrefix(s, "gs_") {
		return ""
	}
	return s
}
