// Package identity — cross-project JWT verifier bridge for llm-gateway-go.
//
// This package wraps identity-go's multi-issuer verifier and provides a
// single entry point that falls back to the legacy admin.VerifyToken path
// when no shared secret is configured. The goal is:
//
//   - Production deploys with IDENTITY_SHARED_SECRET set → accept tokens
//     signed by pocket / memora / redclaw / acc / llm-gateway itself as
//     long as aud=llm-gateway-api.
//   - Legacy deploys without the shared secret → admin.VerifyToken path is
//     used (no behavior change).
//
// ai-session-manager ("asm") is deliberately NOT in the allowlist — it is a
// pure consumer of llm-gateway user tokens and must never appear as an
// independent identity provider.
package identity

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"

	"github.com/kaixuan/llm-gateway-go/pkg/identity/token"
)

// EnvSharedSecret is the canonical env var name shared across the 6 projects.
const EnvSharedSecret = "IDENTITY_SHARED_SECRET"

// EnvIssuerAllowlist is the comma-separated allowlist env var.
const EnvIssuerAllowlist = "IDENTITY_ISSUER_ALLOWLIST"

// EnvShadowDSN is the identity_shadow DSN env var.
const EnvShadowDSN = "IDENTITY_SHADOW_DSN"

// EnvExpectedAudience overrides the default resource-server audience for
// this verifier. Default is "llm-gateway-api".
const EnvExpectedAudience = "LLM_GATEWAY_EXPECTED_AUDIENCE"

// DefaultIssuerAllowlist matches the identity_shadow provider contract.
//
// `asm` is intentionally absent.
var DefaultIssuerAllowlist = []string{
	"redclaw", "memora", "llm-gateway", "pocket", "acc",
}

// DefaultAudience is the resource-server audience this gateway enforces.
const DefaultAudience = "llm-gateway-api"

// Principal is the normalized identity returned by Verify.
type Principal struct {
	UserID             int
	TenantID           string
	Username           string
	Role               string
	Issuer             string
	Audience           string
	ExpiresAt          time.Time
	Source             string // "legacy" | "multi_issuer"
	MustChangePassword bool   // only meaningful for Source=="legacy"
}

// ErrInvalidToken is returned when neither verify path accepts the input.
var ErrInvalidToken = errors.New("identity: invalid or expired token")

// LegacyVerifier is the contract that admin.VerifyToken satisfies.
//
// Defined as an interface so the verifier doesn't have to import admin/,
// which would cycle if admin/ ever depends on this package indirectly.
type LegacyVerifier interface {
	VerifyLegacy(token string) (*LegacyClaims, error)
}

// LegacyClaims is the legacy (admin.JWTClaims) shape used by VerifyLegacy.
type LegacyClaims struct {
	UserID             int
	TenantID           string
	Username           string
	Role               string
	MustChangePassword bool
}

// Verify tries, in order:
//  1. identity-go multi-issuer verifier (when IDENTITY_SHARED_SECRET is set)
//  2. legacy verifier (always available; backward-compat)
//
// Returns ErrInvalidToken when both paths reject.
func Verify(raw string, legacy LegacyVerifier) (*Principal, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, ErrInvalidToken
	}

	if issuers, ok := loadMultiIssuerConfig(); ok {
		aud := expectedAudience()
		if c, err := token.VerifyMultiIssuer(raw, issuers, aud); err == nil {
			userID := atoiOrZero(c.UserID)
			role := firstRole(c.Roles, c.Scope)
			if role == "" {
				role = "tenant_admin"
			}
			return &Principal{
				UserID:    userID,
				TenantID:  c.TenantID,
				Username:  firstNonEmpty(usernameFromExtra(c.Extra), c.Subject),
				Role:      role,
				Issuer:    c.Issuer,
				Audience:  aud,
				ExpiresAt: time.Unix(c.ExpiresAt, 0),
				Source:    "multi_issuer",
			}, nil
		}
	}

	if legacy != nil {
		if c, err := legacy.VerifyLegacy(raw); err == nil && c != nil {
			return &Principal{
				UserID:             c.UserID,
				TenantID:           c.TenantID,
				Username:           c.Username,
				Role:               c.Role,
				Issuer:             "llm-gateway",
				Audience:           DefaultAudience,
				ExpiresAt:          time.Time{},
				Source:             "legacy",
				MustChangePassword: c.MustChangePassword,
			}, nil
		}
	}

	return nil, ErrInvalidToken
}

func expectedAudience() string {
	if v := strings.TrimSpace(os.Getenv(EnvExpectedAudience)); v != "" {
		return v
	}
	return DefaultAudience
}

func atoiOrZero(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstRole(roles []string, scope string) string {
	for _, r := range roles {
		if strings.TrimSpace(r) != "" {
			return r
		}
	}
	if s := strings.TrimSpace(scope); s != "" {
		parts := strings.Fields(s)
		if len(parts) > 0 {
			return parts[0]
		}
	}
	return ""
}

func usernameFromExtra(extra map[string]any) string {
	if v, ok := extra["username"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

var (
	mu        sync.Mutex
	issuers   []token.Issuer
	loaded    bool
	disabled  bool
)

// ResetForTest clears the cached allowlist + disabled flag so a test that
// toggles IDENTITY_SHARED_SECRET can re-evaluate. Not safe to call from
// production code; only for unit tests.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	loaded = false
	disabled = false
	issuers = nil
}

// loadMultiIssuerConfig returns the cached identity-go allowlist. When the
// shared secret env var is unset it permanently disables the multi-issuer
// path for this process lifetime, so VerifyToken doesn't pay the parse cost
// on every request.
func loadMultiIssuerConfig() ([]token.Issuer, bool) {
	mu.Lock()
	defer mu.Unlock()

	if disabled {
		return nil, false
	}

	if strings.TrimSpace(os.Getenv(EnvSharedSecret)) == "" {
		disabled = true
		return nil, false
	}

	if loaded {
		return issuers, true
	}

	secret, err := token.LoadSharedSecret(EnvSharedSecret)
	if err != nil {
		slog.Default().Warn("identity: shared secret invalid", "err", err)
		disabled = true
		return nil, false
	}

	allowlist := strings.TrimSpace(os.Getenv(EnvIssuerAllowlist))
	if allowlist == "" {
		allowlist = strings.Join(DefaultIssuerAllowlist, ",")
	}

	parsed, err := token.Allowlist(allowlist, secret)
	if err != nil {
		slog.Default().Warn("identity: allowlist invalid", "err", err)
		disabled = true
		return nil, false
	}

	issuers = parsed
	loaded = true
	return issuers, true
}

// =====================================================================
// Shadow recording
// =====================================================================

var (
	shadowDBOnce sync.Once
	shadowDB     *sql.DB
	shadowLog    = slog.Default().With("component", "identity-shadow")
)

// ShadowDatabase returns the lazily-opened *sql.DB for the shadow store.
func ShadowDatabase() *sql.DB {
	shadowDBOnce.Do(func() {
		dsn := strings.TrimSpace(os.Getenv(EnvShadowDSN))
		if dsn == "" {
			shadowLog.Info("IDENTITY_SHADOW_DSN not set; shadow recording disabled")
			return
		}
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			shadowLog.Error("shadow: open db failed", "err", err)
			return
		}
		db.SetMaxOpenConns(2)
		db.SetMaxIdleConns(2)
		shadowDB = db
		shadowLog.Info("shadow DB initialized")
	})
	return shadowDB
}

// RecordShadow upserts a (provider, subject, tenant_id) → shadow_user_id
// mapping. Failures are logged but never propagate.
func RecordShadow(provider string, userID int, tenantID, displayName, primaryEmail string) {
	db := ShadowDatabase()
	if db == nil {
		return
	}
	if strings.TrimSpace(provider) == "" || userID == 0 {
		return
	}
	if tenantID == "" {
		tenantID = "default"
	}
	subject := intToString(userID)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const upsertSQL = `
INSERT INTO identity_shadow.shadow_users (provider, subject, tenant_id, display_name, primary_email, last_seen_at)
VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), now())
ON CONFLICT (provider, subject, tenant_id) DO UPDATE
    SET display_name  = COALESCE(EXCLUDED.display_name, identity_shadow.shadow_users.display_name),
        primary_email = COALESCE(EXCLUDED.primary_email, identity_shadow.shadow_users.primary_email),
        last_seen_at  = now()`
	if _, err := db.ExecContext(ctx, upsertSQL, provider, subject, tenantID, displayName, primaryEmail); err != nil {
		shadowLog.Warn("shadow upsert failed", "provider", provider, "subject", subject, "err", err)
	}
}

func intToString(n int) string {
	if n == 0 {
		return ""
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	if n == 0 {
		return "0"
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}