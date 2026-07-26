package executors

import "unicode/utf8"

// truncateUTF8 returns a UTF-8 safe truncation of s to at most limit bytes.
//
// Slicing a string at an arbitrary byte offset can split a multi-byte rune and
// produce invalid UTF-8. When such a value is written to a PostgreSQL UTF8
// column the INSERT fails with SQLSTATE 22021 and the whole row is lost — see
// incident 2026-06-11, where s[:limit] silently dropped request_logs rows for
// glm-5.1 / minimax / doubao traffic (Chinese and emoji content).
//
// Callers that want a visible marker should append their own suffix to the
// result; this function never adds one, so the returned value is always a
// prefix of s.
func truncateUTF8(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	cut := 0
	for i, r := range s {
		next := i + utf8.RuneLen(r)
		if next > limit {
			cut = i
			break
		}
		cut = next
	}
	return s[:cut]
}
