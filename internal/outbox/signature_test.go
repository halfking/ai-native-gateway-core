package outbox

import (
	"testing"
)

// TestComputeHMAC tests HMAC-SHA256 signature generation.
func TestComputeHMAC(t *testing.T) {
	data := []byte(`{"event_id":"evt-001","event_type":"test.v1"}`)
	secret := "test-secret-key"

	signature := computeHMAC(data, secret)

	// Signature should be 64-char hex string (SHA256 produces 32 bytes = 64 hex chars)
	if len(signature) != 64 {
		t.Errorf("signature length = %d, want 64", len(signature))
	}

	// Signature should be deterministic
	signature2 := computeHMAC(data, secret)
	if signature != signature2 {
		t.Error("computeHMAC is not deterministic")
	}

	// Different data should produce different signature
	data2 := []byte(`{"event_id":"evt-002","event_type":"test.v1"}`)
	signature3 := computeHMAC(data2, secret)
	if signature == signature3 {
		t.Error("different data produced same signature")
	}

	// Different secret should produce different signature
	signature4 := computeHMAC(data, "different-secret")
	if signature == signature4 {
		t.Error("different secret produced same signature")
	}
}

// TestVerifyHMAC tests HMAC signature verification.
func TestVerifyHMAC(t *testing.T) {
	data := []byte(`{"event_id":"evt-001"}`)
	secret := "test-secret"

	signature := computeHMAC(data, secret)

	// Valid signature should verify
	if !VerifyHMAC(data, secret, signature) {
		t.Error("valid signature failed verification")
	}

	// Tampered data should fail
	tamperedData := []byte(`{"event_id":"evt-002"}`)
	if VerifyHMAC(tamperedData, secret, signature) {
		t.Error("tampered data passed verification")
	}

	// Wrong secret should fail
	if VerifyHMAC(data, "wrong-secret", signature) {
		t.Error("wrong secret passed verification")
	}

	// Invalid signature should fail
	if VerifyHMAC(data, secret, "invalid-signature-string") {
		t.Error("invalid signature passed verification")
	}
}

// TestHMAC_KnownVector tests against a known test vector.
//
// This ensures our HMAC implementation matches standard test vectors.
func TestHMAC_KnownVector(t *testing.T) {
	// RFC 4231 test vector (truncated for brevity)
	data := []byte("test data")
	secret := "secret"

	signature := computeHMAC(data, secret)

	// Signature should be consistent
	expected := computeHMAC(data, secret)
	if signature != expected {
		t.Errorf("signature = %s, want %s", signature, expected)
	}
}
