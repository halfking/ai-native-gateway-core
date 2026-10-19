package credentialtext

import "testing"

func TestPlaceholderSecretValueExactOnly(t *testing.T) {
	for _, value := range []string{"[REDACTED]", "[REDACTED:secret]", "[MASKED]", "<REDACTED>", "****", "PASSWORD=[REDACTED]", `\1[REDACTED]`} {
		if !isPlaceholderSecretValue(value) {
			t.Errorf("mask rejected: %q", value)
		}
	}
	for _, value := range []string{"", "TrulySensitive987!", "[REDACTED", "[REDACTEDhunter2", "[REDACTED]hunter2", "hunter2[REDACTED]", "****hunter2", "${REAL}suffix", "<hunter2>", "PASSWORD=[MASKED]TrulySensitive987!"} {
		if isPlaceholderSecretValue(value) {
			t.Errorf("literal exempted: %q", value)
		}
	}
}
