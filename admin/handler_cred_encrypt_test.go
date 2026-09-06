// Unit tests for the (previously untested) encryptCred / decryptCred pair.
// These cover the round-trip self-check added after the 2026-08-18 154
// incident, in which raw Fernet bytes were persisted without base64-encode.
//
// The tests construct a minimal Handler with only the encryption fields
// populated (no DB, no mux) so they run without a live PostgreSQL.
package admin

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/secret"
)

// testFernetKey returns a stable 32-byte key derived from a fixed seed. Real
// gateways derive this from LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY; here we
// just need a deterministic input for round-trip assertions.
func testFernetKey(t *testing.T) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte("check-credentials-test-key-2026-08-18"))
	return sum[:]
}

// newTestHandler constructs a Handler with only the credential-encryption
// fields populated; nil DB / nil keyring / zero pool.
func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	return &Handler{encKey: testFernetKey(t)}
}

// newTestHandlerWithKeyring constructs a Handler with an AES-GCM keyring
// (kid="k1", 32-byte zero key) populated. encryptCred should prefer the
// keyring branch when present.
func newTestHandlerWithKeyring(t *testing.T) *Handler {
	t.Helper()
	key := [32]byte{}
	kr, err := secret.NewKeyring(map[string][32]byte{"k1": key}, "k1")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return &Handler{encKey: testFernetKey(t), keyring: kr}
}

// TestEncryptDecryptRoundTrip_FernetPath is the happy path for the legacy
// Fernet branch (no keyring configured): encrypt → decrypt returns the same
// plaintext, and the envelope is base64-decodable text starting with the
// Fernet base64-url "gAAAAA" prefix (NOT raw 0x80 bytes).
func TestEncryptDecryptRoundTrip_FernetPath(t *testing.T) {
	h := newTestHandler(t)
	const plaintext = "sk-000000000000000000000000000000000000000000000000000000000000"

	envelope, err := h.encryptCred([]byte(plaintext))
	if err != nil {
		t.Fatalf("encryptCred: %v", err)
	}

	// The envelope MUST be base64-decodable text, not raw Fernet bytes.
	// The 154 incident was triggered by raw bytes being stored instead.
	if envelope == "" {
		t.Fatalf("empty envelope")
	}
	// admin/crypto.go encryptFernet uses base64.URLEncoding (with padding),
	// so URLEncoding is what we expect here. RawURLEncoding would reject the
	// trailing '=' padding.
	if _, err := base64.URLEncoding.DecodeString(envelope); err != nil {
		t.Fatalf("envelope is not base64-url: %v (envelope=%q)", err, envelope)
	}
	if !strings.HasPrefix(envelope, "gAAAAA") {
		t.Fatalf("envelope does not look like Fernet base64-url (got %q)", envelope[:20])
	}

	pt, isLegacy, err := h.decryptCred(envelope)
	if err != nil {
		t.Fatalf("decryptCred: %v", err)
	}
	if !isLegacy {
		t.Fatalf("expected isLegacy=true for Fernet path")
	}
	if pt != plaintext {
		t.Fatalf("round-trip mismatch: got %q want %q", pt, plaintext)
	}
}

// TestEncryptDecryptRoundTrip_KeyringPath exercises the AES-GCM branch when
// a keyring is configured.
func TestEncryptDecryptRoundTrip_KeyringPath(t *testing.T) {
	h := newTestHandlerWithKeyring(t)
	const plaintext = "sk-test-aes-gcm-2026-08-18"

	envelope, err := h.encryptCred([]byte(plaintext))
	if err != nil {
		t.Fatalf("encryptCred: %v", err)
	}
	if !strings.HasPrefix(envelope, "v1:k1:") {
		t.Fatalf("envelope missing AES-GCM kid prefix: %q", envelope)
	}
	pt, isLegacy, err := h.decryptCred(envelope)
	if err != nil {
		t.Fatalf("decryptCred: %v", err)
	}
	if isLegacy {
		t.Fatalf("expected isLegacy=false for AES-GCM path")
	}
	if pt != plaintext {
		t.Fatalf("round-trip mismatch: got %q want %q", pt, plaintext)
	}
}

// TestDecryptCred_V1LegacyFernetEnvelope exercises the historical envelope
// format used by credential 17. It must use the Fernet fallback even when an
// AES-GCM keyring is also configured.
func TestDecryptCred_V1LegacyFernetEnvelope(t *testing.T) {
	h := newTestHandlerWithKeyring(t)
	const plaintext = "sk-test-v1-legacy-fernet"

	token, err := encryptFernet([]byte(plaintext), h.encKey)
	if err != nil {
		t.Fatalf("encryptFernet: %v", err)
	}
	envelope := "v1:legacy:" + base64.RawURLEncoding.EncodeToString(token)

	pt, isLegacy, err := h.decryptCred(envelope)
	if err != nil {
		t.Fatalf("decryptCred: %v", err)
	}
	if !isLegacy {
		t.Fatalf("expected isLegacy=true for v1:legacy Fernet envelope")
	}
	if pt != plaintext {
		t.Fatalf("decrypt mismatch: got %q want %q", pt, plaintext)
	}
	if _, _, err := h.decryptCred("v1:legacy:not-base64"); err == nil {
		t.Fatal("decryptCred must reject malformed v1:legacy payload")
	}
}

// TestDecryptCred_V1AESGCMRetainsDecryptError ensures malformed authenticated
// AES data keeps the direct AES error classification.
func TestDecryptCred_V1AESGCMRetainsDecryptError(t *testing.T) {
	h := newTestHandlerWithKeyring(t)
	wrong := [32]byte{1}
	wrongKR, err := secret.NewKeyring(map[string][32]byte{"k1": wrong}, "k1")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	envelope, err := secret.EncryptAESGCM([]byte("plaintext"), wrongKR)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}

	_, _, err = h.decryptCred(envelope)
	if err == nil {
		t.Fatal("decryptCred must reject mismatched AES-GCM data")
	}
	if !errors.Is(err, secret.ErrDecrypt) {
		t.Fatalf("expected ErrDecrypt, got %v", err)
	}
}

// TestDecryptCred_RejectsRawBinary asserts the legacy branch refuses to
// decrypt a raw 137-byte Fernet token (the 154-incident format). This is
// the existing guard inside decryptCred that we want to keep working.
func TestDecryptCred_RejectsRawBinary(t *testing.T) {
	h := newTestHandler(t)
	raw := make([]byte, 137)
	raw[0] = 0x80
	for i := 1; i < len(raw); i++ {
		raw[i] = byte(i & 0xff)
	}
	_, _, err := h.decryptCred(string(raw))
	if err == nil {
		t.Fatalf("decryptCred must reject raw Fernet binary")
	}
}

// TestEncryptCred_NoKeyringNoEncKey covers the fail-closed path: with neither
// a keyring nor a 32-byte Fernet key, encryptCred must return an error
// rather than persist an unreadable ciphertext. This mirrors the incident
// pre-condition that allowed the original corruption.
func TestEncryptCred_NoKeyringNoEncKey(t *testing.T) {
	h := &Handler{} // no keyring, no encKey
	_, err := h.encryptCred([]byte("anything"))
	if err == nil {
		t.Fatalf("encryptCred must fail closed when neither keyring nor encKey is configured")
	}
}

// TestDecryptCred_NoKeyringNoEncKey_V1Envelope asserts that a v1 envelope
// cannot be decrypted when the keyring is missing, even if a Fernet key is
// available. This guards against silent fallback that could mask misconfig.
func TestDecryptCred_NoKeyringNoEncKey_V1Envelope(t *testing.T) {
	h := &Handler{}
	_, _, err := h.decryptCred("v1:k1:someb64payload")
	if err == nil {
		t.Fatalf("decryptCred must fail when keyring missing for v1 envelope")
	}
}
