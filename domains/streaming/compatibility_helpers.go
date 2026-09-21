package streaming

import "strings"

// 2026-08-26: compatibility wrappers retained during the local-priority merge.
// The streaming tests exercise the old shouldFlushRequestTrace surface, while
// handler is implemented by shouldTraceRequest. Keep both names until the next
// repo-wide refactor unifies the naming contract.

func shouldFlushRequestTrace(method string) bool {
	return shouldTraceRequest(method)
}

func synthesizeStreamBodyFromText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	return `{"id":"partial-stream","object":"chat.completion.partial","created":0,"choices":[{"index":0,"message":{"role":"assistant","content":` + jsonEscapeStringLiteral(trimmed) + `},"finish_reason":"length"}]}`
}

func jsonEscapeStringLiteral(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
