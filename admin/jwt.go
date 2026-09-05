package admin

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/kaixuan/llm-gateway-go/pkg/identity"
)

// AudienceName is the JWT aud claim this gateway stamps on locally-issued
// user/admin tokens. Each project enforces its own resource-server audience
// boundary; sibling projects (pocket / memora / redclaw / acc) verify tokens
// against their own aud, never this one.
const AudienceName = "llm-gateway-api"

// JWTClaims extends the standard JWT claims with tenant/user info.
type JWTClaims struct {
	UserID             int    `json:"user_id"`
	TenantID           string `json:"tenant_id"`
	Username           string `json:"username"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"must_change_password,omitempty"`
	jwt.RegisteredClaims
}

// jwtSecret resolves the JWT signing key. Rule 20 §8.2: the hardcoded
// "default-jwt-secret-change-me" fallback has been removed because it lets
// any deployment sign valid admin tokens. Callers that need a guaranteed
// non-empty secret must check enabled first; SignToken returns an error
// when no secret is configured (rather than silently using an insecure
// default).
//
// Precedence (SSOT: admin/auth_params.go EnvJWTSecret, EnvSecretKey):
//
//	LLM_GATEWAY_JWT_SECRET env → fallbackKey (cfg.SecretKey).
func jwtSecret(fallbackKey string) []byte {
	if s := os.Getenv("LLM_GATEWAY_JWT_SECRET"); s != "" {
		return []byte(s)
	}
	return []byte(fallbackKey)
}

// jwtSecretConfigured reports whether a JWT signing key is available.
func jwtSecretConfigured(fallbackKey string) bool {
	return len(jwtSecret(fallbackKey)) > 0
}

func jwtExpiry() time.Duration {
	if s := os.Getenv("LLM_GATEWAY_JWT_EXPIRY"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			return d
		}
	}
	return 24 * time.Hour // SSOT: admin/auth_params.go JWTDefaultTTL
}

// SignToken creates a signed JWT string for the given user.
// Returns an error if no JWT signing key is configured (rule 20 §8.2).
func SignToken(userID int, tenantID, username, role, secretKey string, mustChangePassword bool) (string, time.Time, error) {
	if !jwtSecretConfigured(secretKey) {
		return "", time.Time{}, fmt.Errorf("jwt signing secret not configured (set LLM_GATEWAY_JWT_SECRET or LLM_GATEWAY_SECRET_KEY)")
	}
	expiry := jwtExpiry()
	expiresAt := time.Now().Add(expiry)

	claims := JWTClaims{
		UserID:             userID,
		TenantID:           tenantID,
		Username:           username,
		Role:               role,
		MustChangePassword: mustChangePassword,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "llm-gateway",
			Audience:  jwt.ClaimStrings{AudienceName},
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtSecret(secretKey))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign jwt: %w", err)
	}
	return signed, expiresAt, nil
}

// VerifyToken parses and validates a JWT string, returning the claims.
func VerifyToken(tokenStr, secretKey string) (*JWTClaims, error) {
	claims := &JWTClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return jwtSecret(secretKey), nil
	}, jwt.WithIssuer("llm-gateway"), jwt.WithAudience(AudienceName))
	if err != nil {
		return nil, fmt.Errorf("parse jwt: %w", err)
	}
	parsedClaims, ok := token.Claims.(*JWTClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid jwt claims")
	}
	return parsedClaims, nil
}

// VerifyLegacy is the adapter that pkg/identity uses to fall back to the
// single-secret JWT verifier. It returns an identity.LegacyClaims shape
// (defined in pkg/identity) so pkg/identity doesn't need to import this
// package's types directly.
func VerifyLegacy(tokenStr, secretKey string) (*identity.LegacyClaims, error) {
	c, err := VerifyToken(tokenStr, secretKey)
	if err != nil || c == nil {
		return nil, err
	}
	return &identity.LegacyClaims{
		UserID:             c.UserID,
		TenantID:           c.TenantID,
		Username:           c.Username,
		Role:               c.Role,
		MustChangePassword: c.MustChangePassword,
	}, nil
}
