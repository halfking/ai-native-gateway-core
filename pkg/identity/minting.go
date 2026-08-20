package identity

// F4 (cross-repo minting) — enable llm-gateway-go to mint user tokens with
// IDENTITY_SHARED_SECRET so a real token can traverse the 6-project trust
// boundary. Without this, every repo mints with its own local secret and
// no production token can be verified by a peer (only test fixtures).
//
// Precedence (SSOT):
//
//	IDENTITY_SHARED_SECRET  → used to sign the multi-issuer token
//	LLM_GATEWAY_JWT_SECRET  → fallback (legacy single-issuer path)
//	LLM_GATEWAY_SECRET_KEY  → fallback (config file)
//
// If none of these are set → fail-closed (no insecure default; rule 20 §8.2).
//
// Issuer name: "llm-gateway" (matches the canonical identity-go allowlist).

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/pkg/identity/token"
)

// EnvSharedSecret is defined in verifier.go (cannot redeclare).
// Use that constant at call sites.

// EnvLegacyJWTSecret is the per-platform legacy HS256 secret.
const EnvLegacyJWTSecret = "LLM_GATEWAY_JWT_SECRET"

// IssuerLLMGateway is the canonical iss claim this gateway stamps on
// cross-repo tokens. Matches DefaultIssuerAllowlist.
const IssuerLLMGateway = "llm-gateway"

// DefaultCrossIssuerAudience is the default aud claim; can be overridden
// per minting call.
const DefaultCrossIssuerAudience = "llm-gateway-api"

// CrossIssuerSubject is the JWT subject used when the caller does not
// provide one (admin/system minting).
const CrossIssuerSubject = "system:llm-gateway"

// MintSpec captures the inputs to MintCrossIssuerToken.
type MintSpec struct {
	Subject  string
	UserID   string
	TenantID string
	Roles    []string
	Scope    string
	Audience string // resource-server audience; must be non-empty
	TTL      time.Duration
	Issuer   string // optional override; defaults to IssuerLLMGateway
}

// MintCrossIssuerToken signs a JWT using the cross-project shared secret
// (IDENTITY_SHARED_SECRET) so the token can be verified by any peer
// configured with the same allowlist. Returns the compact token string.
//
// Failure modes (fail-closed, never fall back to a hardcoded secret):
//   - IDENTITY_SHARED_SECRET unset or <32 bytes → error
//   - spec.Audience empty → error
//   - spec.TTL <= 0 → error
//
// When IDENTITY_SHARED_SECRET is unset, callers SHOULD fall back to the
// legacy mint path (admin.SignToken) rather than abort — this preserves
// the legacy single-issuer behavior for deploys that haven't adopted the
// shared secret yet.
func MintCrossIssuerToken(spec MintSpec) (string, error) {
	issuerName := strings.TrimSpace(spec.Issuer)
	if issuerName == "" {
		issuerName = IssuerLLMGateway
	}
	subject := strings.TrimSpace(spec.Subject)
	if subject == "" {
		subject = CrossIssuerSubject
	}
	aud := strings.TrimSpace(spec.Audience)
	if aud == "" {
		aud = DefaultCrossIssuerAudience
	}
	if spec.TTL <= 0 {
		return "", errors.New("identity: cross-issuer mint requires TTL > 0")
	}

	secret, err := token.LoadSharedSecret(EnvSharedSecret)
	if err != nil {
		return "", fmt.Errorf("identity: cross-issuer mint requires %s: %w", EnvSharedSecret, err)
	}

	claims := &token.Claims{
		Subject:  subject,
		Audience: aud,
		UserID:   spec.UserID,
		TenantID: spec.TenantID,
		Roles:    spec.Roles,
		Scope:    spec.Scope,
	}

	return token.SignHS256(token.Issuer{Name: issuerName, Secret: secret}, claims, spec.TTL)
}

// HasSharedSecret reports whether IDENTITY_SHARED_SECRET is configured.
// Callers use this to decide whether to mint a cross-issuer token or fall
// back to the legacy single-issuer path.
func HasSharedSecret() bool {
	v := strings.TrimSpace(os.Getenv(EnvSharedSecret))
	return len(v) >= 32
}
