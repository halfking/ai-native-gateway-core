package handoff

import (
	"strings"
	"testing"
)

func TestRedactResumeSensitive_CommonCredentialFormats(t *testing.T) {
	cases := []string{
		"ghp_123456789012345678901234567890123456",
		"AKIAIOSFODNN7EXAMPLE",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature-value-123456789",
		"cookie: session-secret-value-123456",
		`{"token":"nested-secret-value-123456"}`,
	}
	for _, secret := range cases {
		t.Run(secret[:minInt(len(secret), 12)], func(t *testing.T) {
			redacted := redactResumeSensitive("context " + secret)
			if strings.Contains(redacted, secret) {
				t.Fatalf("credential was not redacted: %q", redacted)
			}
			if !strings.Contains(redacted, "[redacted]") {
				t.Fatalf("redaction marker missing: %q", redacted)
			}
		})
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
