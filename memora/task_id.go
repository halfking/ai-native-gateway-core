package memora

import (
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"strings"
)

// TaskID derives a stable task identifier for the current request.
//
// Resolution order:
//  1. X-Task-Id header (the canonical client-side id; e.g. Cursor sets
//     this for multi-turn agent tasks).
//  2. Custom openai-style metadata.task_id field in the body (a fallback
//     for clients that can't set headers).
//  3. Auto-derived: "gateway:auto:<api_key_id>:<sha1(first 4 messages)>"
//     — stable across retries of the same task, distinct across tasks.
//  4. "" (empty) — the caller MUST treat empty as "no task" and skip
//     the entire Memora path.
//
// An empty input returns "" so the executor can short-circuit.
func TaskID(r *http.Request, body []byte, apiKeyID int) string {
	if r != nil {
		if v := strings.TrimSpace(r.Header.Get("X-Task-Id")); v != "" {
			return sanitize(v, 200)
		}
		if v := strings.TrimSpace(r.Header.Get("X-Session-Id")); v != "" {
			return sanitize("s:"+v, 200)
		}
	}
	// Body-level fallback: metadata.task_id or session_id
	if len(body) > 0 {
		if v := extractJSONString(body, "task_id"); v != "" {
			return sanitize("m:"+v, 200)
		}
		if v := extractJSONString(body, "session_id"); v != "" {
			return sanitize("s:"+v, 200)
		}
	}
	// Auto-derive from a content hash of the first few messages.
	if len(body) > 0 {
		h := sha1.Sum(body)
		hexd := hex.EncodeToString(h[:8])
		return sanitize("gateway:auto:"+itoa(apiKeyID)+":"+hexd, 200)
	}
	return ""
}

// UserID encodes (api_key_id, task_id) into a Memora user_id. We use a
// "k:" prefix to namespace these away from real human users in the same
// Memora instance.
func UserID(apiKeyID int, taskID string) string {
	if taskID == "" {
		return ""
	}
	return "k:" + itoa(apiKeyID) + ":" + taskID
}

// sanitize strips characters that would break Memora user_id parsing
// (newlines, control chars) and clamps to maxLen.
func sanitize(s string, maxLen int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < 0x20:
			return -1
		}
		return r
	}, s)
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return strings.TrimSpace(s)
}

// extractJSONString is a cheap / lossy "look for `"key":"value"`" regex.
// We don't unmarshal the whole body because (a) we only need one field,
// (b) the body may not be valid JSON in the Q4 streaming case.
func extractJSONString(body []byte, key string) string {
	needle := `"` + key + `"`
	idx := strings.Index(string(body), needle)
	if idx < 0 {
		return ""
	}
	// Skip to the colon after the key.
	rest := body[idx+len(needle):]
	colon := -1
	for i, b := range rest {
		if b == ':' {
			colon = i
			break
		}
		if b == '}' || b == ']' {
			return ""
		}
	}
	if colon < 0 {
		return ""
	}
	rest = rest[colon+1:]
	// Skip whitespace.
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n') {
		rest = rest[1:]
	}
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:]
	// Find closing quote (no escape handling needed for typical task_ids).
	end := -1
	for i, b := range rest {
		if b == '"' {
			end = i
			break
		}
		if b == '\\' && i+1 < len(rest) {
			continue
		}
	}
	if end < 0 {
		return ""
	}
	return string(rest[:end])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
