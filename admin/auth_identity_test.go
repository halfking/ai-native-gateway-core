package admin

import (
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/kaixuan/llm-gateway-go/pkg/identity"
)

// sharedSecret is the 32-byte HS256 secret used by every project's
// IDENTITY_SHARED_SECRET in tests. Real deploys inject this via env.
const sharedSecret = "llm-gateway-test-shared-secret-32-bytes!!"

// legacySecret is the LLM_GATEWAY_JWT_SECRET used to test the
// backward-compatible legacy path.
const legacySecret = "llm-gateway-test-legacy-secret-32-bytes!!"

// withSharedSecret sets IDENTITY_SHARED_SECRET (and forces the
// multi-issuer cache to reload) for the duration of t.
func withSharedSecret(t *testing.T) {
	t.Helper()
	t.Setenv(identity.EnvSharedSecret, sharedSecret)
	identity.ResetForTest()
	t.Cleanup(identity.ResetForTest)
}

// mintMultiIssuerForTest signs a token with the shared secret so the
// identity-go allowlist accepts it.
func mintMultiIssuerForTest(t *testing.T, issuer, subject, audience, secret string, ttl time.Duration, extra jwt.MapClaims) string {
	t.Helper()
	if extra == nil {
		extra = jwt.MapClaims{}
	}
	now := time.Now()
	extra["iss"] = issuer
	extra["sub"] = subject
	extra["aud"] = audience
	extra["iat"] = now.Unix()
	extra["exp"] = now.Add(ttl).Unix()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, extra)
	signed, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return signed
}

func newTestSigner(t *testing.T) {
	t.Helper()
	os.Setenv("LLM_GATEWAY_JWT_SECRET", legacySecret)
}

func TestIdentity_LegacyOnly_NoSharedSecret(t *testing.T) {
	os.Unsetenv(identity.EnvSharedSecret)
	identity.ResetForTest()
	newTestSigner(t)

	tok, _, err := SignToken(42, "tenant-x", "alice", "tenant_admin", legacySecret, false)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	p, err := identity.Verify(tok, legacyAdapter(legacySecret))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.UserID != 42 || p.TenantID != "tenant-x" || p.Username != "alice" || p.Role != "tenant_admin" {
		t.Fatalf("unexpected: %+v", p)
	}
	if p.Source != "legacy" {
		t.Fatalf("source = %q want legacy", p.Source)
	}
}

func TestIdentity_AcceptsOwnIssuerViaSharedSecret(t *testing.T) {
	withSharedSecret(t)
	newTestSigner(t)

	tok, _, err := SignToken(7, "tenant-l", "bob", "tenant_admin", legacySecret, false)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Token was signed with legacySecret — multi-issuer will reject
	// (allowlist uses sharedSecret). legacy fallback rescues it.
	p, err := identity.Verify(tok, legacyAdapter(legacySecret))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.UserID != 7 || p.Source != "legacy" {
		t.Fatalf("unexpected: %+v", p)
	}
}

func TestIdentity_AcceptsForeignIssuer(t *testing.T) {
	withSharedSecret(t)
	newTestSigner(t)

	tok := mintMultiIssuerForTest(t, "pocket", "u-pocket", identity.DefaultAudience, sharedSecret, time.Hour, jwt.MapClaims{
		"user_id":   "999",
		"tenant_id": "tenant-pocket",
		"username":  "carol",
		"roles":     []any{"tenant_admin"},
	})

	p, err := identity.Verify(tok, legacyAdapter(legacySecret))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.Issuer != "pocket" || p.UserID != 999 || p.TenantID != "tenant-pocket" || p.Username != "carol" {
		t.Fatalf("unexpected foreign: %+v", p)
	}
	if p.Source != "multi_issuer" {
		t.Fatalf("source = %q want multi_issuer", p.Source)
	}
}

func TestIdentity_RejectsWrongAudience(t *testing.T) {
	withSharedSecret(t)
	newTestSigner(t)

	tok := mintMultiIssuerForTest(t, "pocket", "u", "pocket-api", sharedSecret, time.Hour, nil)

	if _, err := identity.Verify(tok, legacyAdapter(legacySecret)); err == nil {
		t.Fatalf("expected rejection for wrong audience")
	}
}

func TestIdentity_RejectsExpired(t *testing.T) {
	withSharedSecret(t)
	newTestSigner(t)

	tok := mintMultiIssuerForTest(t, "pocket", "u", identity.DefaultAudience, sharedSecret, -1*time.Minute, nil)

	if _, err := identity.Verify(tok, legacyAdapter(legacySecret)); err == nil {
		t.Fatalf("expected rejection for expired token")
	}
}

func TestIdentity_RejectsAsmIssuer(t *testing.T) {
	withSharedSecret(t)
	newTestSigner(t)

	// ai-session-manager must NEVER be a recognized identity provider.
	tok := mintMultiIssuerForTest(t, "asm", "u-asm", identity.DefaultAudience, sharedSecret, time.Hour, nil)

	if _, err := identity.Verify(tok, legacyAdapter(legacySecret)); err == nil {
		t.Fatalf("expected rejection for asm issuer (allowlist forbids)")
	}
}

func TestIdentity_RejectsWrongSecret(t *testing.T) {
	withSharedSecret(t)
	newTestSigner(t)

	tok := mintMultiIssuerForTest(t, "pocket", "u", identity.DefaultAudience, "rogue-secret-32-bytes!!!!", time.Hour, nil)

	if _, err := identity.Verify(tok, legacyAdapter(legacySecret)); err == nil {
		t.Fatalf("expected rejection for token signed with wrong secret")
	}
}

func TestIdentity_RejectsEmptyToken(t *testing.T) {
	withSharedSecret(t)
	if _, err := identity.Verify("", legacyAdapter(legacySecret)); err == nil {
		t.Fatalf("expected error for empty token")
	}
}

func TestSignToken_StampsAudience(t *testing.T) {
	newTestSigner(t)
	tok, _, err := SignToken(1, "default", "alice", "user", legacySecret, false)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		return []byte(legacySecret), nil
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims type")
	}
	aud, _ := mc["aud"].([]any)
	if len(aud) == 0 || aud[0] != AudienceName {
		t.Fatalf("aud = %v want %q", mc["aud"], AudienceName)
	}
	if mc["iss"] != "llm-gateway" {
		t.Fatalf("iss = %v want llm-gateway", mc["iss"])
	}
}
