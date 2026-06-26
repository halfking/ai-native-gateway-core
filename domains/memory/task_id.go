package memory

import (
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"strings"
)

func TaskID(r *http.Request, body []byte, apiKeyID int) string {
	if r != nil {
		if v := strings.TrimSpace(r.Header.Get("X-Task-Id")); v != "" {
			return sanitize(v, 200)
		}
		if v := strings.TrimSpace(r.Header.Get("X-Session-Id")); v != "" {
			return sanitize("s:"+v, 200)
		}
	}
	if len(body) > 0 {
		if v := extractJSONString(body, "task_id"); v != "" {
			return sanitize("m:"+v, 200)
		}
		if v := extractJSONString(body, "session_id"); v != "" {
			return sanitize("s:"+v, 200)
		}
	}
	if len(body) > 0 {
		h := sha1.Sum(body)
		hexd := hex.EncodeToString(h[:8])
		return sanitize("gateway:auto:"+itoa(apiKeyID)+":"+hexd, 200)
	}
	return ""
}

func UserID(tenantID string, apiKeyID int, taskID string) string {
	if taskID == "" {
		return ""
	}
	if tenantID == "" || tenantID == "default" {
		return "k:" + itoa(apiKeyID) + ":" + taskID
	}
	return "t:" + sanitize(tenantID, 64) + ":k:" + itoa(apiKeyID) + ":" + taskID
}

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

func extractJSONString(body []byte, key string) string {
	needle := `"` + key + `"`
	idx := strings.Index(string(body), needle)
	if idx < 0 {
		return ""
	}
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
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n') {
		rest = rest[1:]
	}
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:]
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
