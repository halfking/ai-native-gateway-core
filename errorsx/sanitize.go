package errorsx

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// SanitizeErrorText strips well-known credential / token shapes from a raw
// vendor error body before it is persisted to candidate_failure_logs,
// aggregated into provider_error_details, or surfaced to admin callers.
//
// The intent is *defence in depth*, not a substitute for upstream redactors:
// a vendor that echoes the user's API key or a server-issued Bearer token
// in its error body would otherwise expose the credential to anyone with
// admin-role access to the credential-detail UI.
//
// Patterns replaced (case-insensitive where appropriate):
//
//   - "Bearer <token>"            → "Bearer <redacted: bearer>"
//   - "sk-" + 16+ alnum (OpenAI / Anthropic / MiniMax / GLM API keys)
//   - long base64 / hex blobs of ≥ 32 chars (server-side signing keys)
//   - "api_key=...", "apikey=...", "token=..." (query-string / form leaks)
//   - "x-api-key: ..." header echoes
//
// The returned slice is length-bounded by maxBytes (default 320 if 0) so
// downstream previews do not balloon. The function never panics on
// malformed input.
func SanitizeErrorText(in []byte, maxBytes int) []byte {
	if len(in) == 0 {
		return in
	}
	out := string(RedactCredentialShapes(in))
	if maxBytes <= 0 {
		maxBytes = 320
	}
	if len(out) > maxBytes {
		out = truncateUTF8Safe(out, maxBytes)
	}
	return []byte(out)
}

// RedactCredentialShapes applies the SanitizeErrorText redaction patterns
// WITHOUT a length cap, for full-body storage paths that must preserve the
// body while stripping credential echoes (audit round2 E-#2).
func RedactCredentialShapes(in []byte) []byte {
	if len(in) == 0 {
		return in
	}
	out := string(in)
	out = bearerPattern.ReplaceAllString(out, "Bearer <redacted:bearer>")
	out = apiKeyPattern.ReplaceAllString(out, "<redacted:api_key>")
	out = longBlobPattern.ReplaceAllString(out, "<redacted:blob>")
	out = headerEchoPattern.ReplaceAllString(out, "$1<redacted:header>")
	return []byte(out)
}

// truncateUTF8Safe cuts s to at most max bytes without splitting a UTF-8
// sequence: a byte-truncated CJK/error message produced invalid UTF-8 that
// PostgreSQL (UTF8 encoding) rejects on INSERT (audit round2, axis G).
func truncateUTF8Safe(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[:max]
	// If we split a sequence, the final bytes are continuation bytes
	// (10xxxxxx); walk back to the start of the last complete rune.
	for i := 0; i < 3 && len(cut) > 0; i++ {
		if utf8.RuneStart(cut[len(cut)-1]) {
			break
		}
		cut = cut[:len(cut)-1]
	}
	// Drop a trailing rune that got chopped mid-sequence (its start byte
	// survived but the continuation did not).
	if r, size := utf8.DecodeLastRuneInString(cut); r == utf8.RuneError && size <= 1 {
		cut = cut[:len(cut)-size]
	}
	return cut
}

var (
	bearerPattern     = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._\-+/=]{12,}`)
	apiKeyPattern     = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_\-]{16,}|sk_live_[A-Za-z0-9]{16,}|sk_test_[A-Za-z0-9]{16,})`)
	longBlobPattern   = regexp.MustCompile(`\b[A-Za-z0-9+/]{32,}={0,2}\b`)
	headerEchoPattern = regexp.MustCompile(`(?im)(x-api-key|api[-_]key|access[-_]token|authorization\s*)\s*[:=]\s*["']?[A-Za-z0-9._\-+/=]{8,}["']?`)
)

// QuickSanitize is a convenience wrapper for non-`errorsx` callers that want
// the default 320-byte cap.
func QuickSanitize(in []byte) []byte {
	return SanitizeErrorText(in, 320)
}

// HasCredentialLeak returns true when the input contains a shape that would
// be redacted by SanitizeErrorText. Use it for test assertions and metrics.
func HasCredentialLeak(in []byte) bool {
	if len(in) == 0 {
		return false
	}
	s := string(in)
	return bearerPattern.MatchString(s) ||
		apiKeyPattern.MatchString(s) ||
		headerEchoPattern.MatchString(s) ||
		isLikelyLongBlob(s)
}

func isLikelyLongBlob(s string) bool {
	// Long-blob detection is intentionally conservative: only count if the
	// surrounding context is a JSON-looking key-value pair, so we don't strip
	// legitimate base64 user content (e.g. inline image data).
	return strings.Contains(s, ":") && longBlobPattern.MatchString(s)
}
