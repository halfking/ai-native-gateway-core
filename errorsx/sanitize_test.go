package errorsx

import (
	"strings"
	"testing"
)

func TestSanitizeErrorText_RedactsBearerTokens(t *testing.T) {
	in := []byte(`{"error":{"message":"unauthorized","hint":"Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature"}}`)
	out := SanitizeErrorText(in, 0)
	if strings.Contains(string(out), "eyJhbGciOi") {
		t.Errorf("Bearer token leaked: %s", out)
	}
	if !strings.Contains(string(out), "<redacted:bearer>") {
		t.Errorf("redaction marker missing: %s", out)
	}
}

func TestSanitizeErrorText_RedactsOpenAIAndMiniMaxAPIKeys(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"openai sk-", `{"error":{"message":"Invalid API key sk-proj1234567890abcdefghij"}}`},
		{"minimax sk-", `upstream said: sk-LIVE-abcdef0123456789abcd provided`},
		{"stripe sk_live_", `body contained sk_live_abcdefghijklmnopqrstuv`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := SanitizeErrorText([]byte(tc.in), 0)
			if HasCredentialLeak(out) {
				t.Errorf("credential still present in output: %s", out)
			}
		})
	}
}

func TestSanitizeErrorText_RedactsAPIKeyHeader(t *testing.T) {
	in := []byte(`x-api-key: sk-proj-abcdef1234567890`)
	out := SanitizeErrorText(in, 0)
	if HasCredentialLeak(out) {
		t.Errorf("header echo leaked: %s", out)
	}
}

func TestSanitizeErrorText_RedactsQueryStringToken(t *testing.T) {
	in := []byte(`request failed: api_key=sk-proj-abcdef1234567890&model=gpt-4`)
	out := SanitizeErrorText(in, 0)
	if HasCredentialLeak(out) {
		t.Errorf("query token leaked: %s", out)
	}
}

func TestSanitizeErrorText_TruncatesAtMaxBytes(t *testing.T) {
	in := []byte("rate limit exceeded " + strings.Repeat("x", 1000))
	out := SanitizeErrorText(in, 100)
	if len(out) > 100 {
		t.Errorf("expected <=100 bytes after cap, got %d", len(out))
	}
	// Verify the function never returns more than the requested cap regardless
	// of how many redactions were applied.
}

func TestSanitizeErrorText_PreservesNonCredentialText(t *testing.T) {
	in := []byte(`{"error":{"message":"Rate limit exceeded","code":"rate_limit","retry_after":30}}`)
	out := SanitizeErrorText(in, 0)
	if string(out) != string(in) {
		t.Errorf("non-credential text was modified: got %q, want %q", out, in)
	}
}

func TestSanitizeErrorText_EmptyInput(t *testing.T) {
	if got := SanitizeErrorText(nil, 0); got != nil {
		t.Errorf("nil in should return nil, got %q", got)
	}
	if got := SanitizeErrorText([]byte{}, 0); len(got) != 0 {
		t.Errorf("empty in should return empty, got %q", got)
	}
}

func TestHasCredentialLeak_DetectsKnownShapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"clean text", "rate limit exceeded", false},
		{"openai key", "sk-proj1234567890abcdefghij", true},
		{"bearer", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signature", true},
		{"header echo", `x-api-key: 1234567890abcdef`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HasCredentialLeak([]byte(tc.in))
			if got != tc.want {
				t.Errorf("HasCredentialLeak(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
