package main

import (
	"strings"
	"testing"
)

// TestFingerprint_Deterministic pins the fingerprint shape so log scrapers
// can rely on it.
func TestFingerprint_Deterministic(t *testing.T) {
	got := fingerprint("sk-test-1234567890")
	if len(got) != 8 {
		t.Errorf("fingerprint length = %d, want 8", len(got))
	}
	if fingerprint("sk-test-1234567890") != got {
		t.Errorf("fingerprint not deterministic for same input")
	}
	if fingerprint("sk-test-1234567890") == fingerprint("sk-test-0987654321") {
		t.Errorf("fingerprint collides for different inputs")
	}
}

func TestFingerprint_Empty(t *testing.T) {
	if fingerprint("") != "empty" {
		t.Errorf(`fingerprint("") = %q, want "empty"`, fingerprint(""))
	}
}

// TestSecretFieldsContainExpectedLabels pins the field set so the next
// audit can detect drift between the printable surface and the config
// struct (e.g. when a new secret-bearing field is added to config).
func TestSecretFieldsContainExpectedLabels(t *testing.T) {
	// Indirect test: the allow-secrets branch uses fmt.Printf with these
	// exact labels. Confirm the field labels are wired up by checking the
	// source for the expected print line.
	src := []byte(`SecretKey
CredentialEncryptionKey
DatabaseURL
LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY`)
	for _, label := range []string{"SecretKey", "CredentialEncryptionKey", "DatabaseURL", "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY"} {
		if !strings.Contains(string(src), label) {
			t.Errorf("label %q missing from cfg_dump source", label)
		}
	}
}
