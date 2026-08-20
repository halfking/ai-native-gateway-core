package identity

// F4 cross-repo integration test — exercises mint → verify round-trip
// using identity-go's shared verifier (in pkg/identity/token). This proves
// a token minted by llm-gateway-go can be verified by any peer that trusts
// the canonical allowlist + uses the same IDENTITY_SHARED_SECRET.
//
// The cross-process variant (mint at llm-gateway-go, verify at openpocket)
// is structurally identical because all 5 third_party/identity-go copies
// are byte-identical to pkg/identity/token. Running the verifier in this
// package is the same code path that openpocket's verifier runs.
//
// Constraints enforced by these tests:
//   - IDENTITY_SHARED_SECRET unset → mint fails (fail-closed)
//   - mint iss=llm-gateway, aud=openpocket-api → verify with aud=openpocket-api → PASS
//   - same token → verify with aud=redclaw-api → REJECT (audience mismatch)
//   - mint with iss=asm → never accepted (not in allowlist)
//   - mint with iss=llm-gateway, wrong secret → REJECT (signature mismatch)

import (
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/pkg/identity/token"
)

const (
	testSharedSecret = "integration-test-shared-secret-32-bytes!!"
	testPocketAud    = "openpocket-api"
	testRedclawAud   = "redclaw-api"
)

// withSharedSecret sets the canonical env var + clears cached state so the
// test sees a clean minting/verifying configuration.
func withSharedSecret(t *testing.T) {
	t.Helper()
	t.Setenv(EnvSharedSecret, testSharedSecret)
	ResetForTest()
	t.Cleanup(ResetForTest)
}

func TestMintCrossIssuerToken_Succeeds(t *testing.T) {
	withSharedSecret(t)

	tok, err := MintCrossIssuerToken(MintSpec{
		Subject:  "u-integration",
		UserID:   "u-integration",
		TenantID: "tenant-int",
		Roles:    []string{"tenant_admin"},
		Scope:    "memory.read",
		Audience: testPocketAud,
		TTL:      1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if tok == "" {
		t.Fatalf("empty token")
	}
	// Compact JWT has exactly two dots.
	if strings.Count(tok, ".") != 2 {
		t.Fatalf("token shape: expected 2 dots, got %d in %q", strings.Count(tok, "."), tok)
	}
}

func TestMintCrossIssuerToken_FailsClosedWhenSecretUnset(t *testing.T) {
	// Unset the secret explicitly (t.Setenv cleanup only resets on test exit).
	t.Setenv(EnvSharedSecret, "")
	ResetForTest()

	_, err := MintCrossIssuerToken(MintSpec{
		Subject:  "u",
		TenantID: "t",
		Audience: testPocketAud,
		TTL:      time.Hour,
	})
	if err == nil {
		t.Fatalf("expected error when IDENTITY_SHARED_SECRET unset, got nil")
	}
	if !strings.Contains(err.Error(), EnvSharedSecret) {
		t.Fatalf("error should reference env var, got: %v", err)
	}
}

func TestMintCrossIssuerToken_VerifyRoundTripOK(t *testing.T) {
	withSharedSecret(t)

	tok, err := MintCrossIssuerToken(MintSpec{
		Subject:  "u-round-trip",
		UserID:   "u-round-trip",
		TenantID: "tenant-round-trip",
		Roles:    []string{"tenant_admin"},
		Audience: testPocketAud,
		TTL:      1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	// Build the verifier allowlist matching the canonical 5-issuer set.
	secret, err := token.LoadSharedSecret(EnvSharedSecret)
	if err != nil {
		t.Fatalf("load secret: %v", err)
	}
	allowlist := []token.Issuer{
		{Name: "redclaw", Secret: secret},
		{Name: "memora", Secret: secret},
		{Name: "llm-gateway", Secret: secret},
		{Name: "pocket", Secret: secret},
		{Name: "acc", Secret: secret},
	}

	claims, err := token.VerifyMultiIssuer(tok, allowlist, testPocketAud)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Issuer != "llm-gateway" {
		t.Fatalf("iss: got %q want %q", claims.Issuer, "llm-gateway")
	}
	if claims.Subject != "u-round-trip" {
		t.Fatalf("sub: got %q want %q", claims.Subject, "u-round-trip")
	}
	if claims.TenantID != "tenant-round-trip" {
		t.Fatalf("tenant_id: got %q want %q", claims.TenantID, "tenant-round-trip")
	}
	if claims.Audience != testPocketAud {
		t.Fatalf("aud: got %q want %q", claims.Audience, testPocketAud)
	}
}

func TestMintCrossIssuerToken_RejectsAudienceMismatch(t *testing.T) {
	withSharedSecret(t)

	tok, err := MintCrossIssuerToken(MintSpec{
		Subject:  "u-aud-mismatch",
		TenantID: "tenant-x",
		Audience: testPocketAud, // minted for openpocket
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	secret, err := token.LoadSharedSecret(EnvSharedSecret)
	if err != nil {
		t.Fatalf("load secret: %v", err)
	}
	allowlist := []token.Issuer{
		{Name: "redclaw", Secret: secret},
		{Name: "llm-gateway", Secret: secret},
	}

	// Try to verify against redclaw-api — must fail because aud != redclaw-api.
	if _, err := token.VerifyMultiIssuer(tok, allowlist, testRedclawAud); err == nil {
		t.Fatalf("expected audience-mismatch rejection, got nil")
	} else if !strings.Contains(err.Error(), "audience") {
		t.Fatalf("expected audience-mismatch error, got: %v", err)
	}
}

func TestMintCrossIssuerToken_RejectsAsmIssuer(t *testing.T) {
	withSharedSecret(t)

	secret, err := token.LoadSharedSecret(EnvSharedSecret)
	if err != nil {
		t.Fatalf("load secret: %v", err)
	}

	// Mint a token with iss="asm" (forbidden). Even if signed with the
	// shared secret, the allowlist MUST reject it because "asm" is not in
	// the canonical DefaultIssuerAllowlist.
	tok, err := token.SignHS256(
		token.Issuer{Name: "asm", Secret: secret},
		&token.Claims{
			Subject:  "u-asm",
			TenantID: "tenant-asm",
			Audience: testPocketAud,
		},
		time.Hour,
	)
	if err != nil {
		t.Fatalf("sign asm: %v", err)
	}

	allowlist := []token.Issuer{
		{Name: "redclaw", Secret: secret},
		{Name: "llm-gateway", Secret: secret},
	}
	if _, err := token.VerifyMultiIssuer(tok, allowlist, testPocketAud); err == nil {
		t.Fatalf("expected asm rejection, got nil")
	} else if !strings.Contains(err.Error(), "not in allowlist") {
		t.Fatalf("expected 'not in allowlist' error, got: %v", err)
	}
}

func TestMintCrossIssuerToken_RejectsWrongSecret(t *testing.T) {
	withSharedSecret(t)

	tok, err := MintCrossIssuerToken(MintSpec{
		Subject:  "u-wrong-secret",
		TenantID: "tenant-ws",
		Audience: testPocketAud,
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	// Try to verify with a different secret.
	wrongAllowlist := []token.Issuer{
		{Name: "llm-gateway", Secret: []byte("rogue-secret-32-bytes-padding!")},
	}
	if _, err := token.VerifyMultiIssuer(tok, wrongAllowlist, testPocketAud); err == nil {
		t.Fatalf("expected wrong-secret rejection, got nil")
	}
}

func TestHasSharedSecret_TrueAndFalse(t *testing.T) {
	t.Setenv(EnvSharedSecret, testSharedSecret)
	if !HasSharedSecret() {
		t.Fatalf("expected true with 41-byte secret set")
	}
	t.Setenv(EnvSharedSecret, "")
	if HasSharedSecret() {
		t.Fatalf("expected false with empty secret")
	}
	t.Setenv(EnvSharedSecret, "too-short")
	if HasSharedSecret() {
		t.Fatalf("expected false with <32 byte secret")
	}
}
