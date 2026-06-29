package admin

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

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
// Precedence: LLM_GATEWAY_JWT_SECRET env → fallbackKey (cfg.SecretKey).
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
	return 24 * time.Hour
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
	token, err := jwt.ParseWithClaims(tokenStr, &JWTClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return jwtSecret(secretKey), nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse jwt: %w", err)
	}
	claims, ok := token.Claims.(*JWTClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid jwt claims")
	}
	return claims, nil
}
