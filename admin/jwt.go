package admin

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTClaims extends the standard JWT claims with tenant/user info.
type JWTClaims struct {
	UserID   int    `json:"user_id"`
	TenantID string `json:"tenant_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

func jwtSecret(fallbackKey string) []byte {
	if s := os.Getenv("LLM_GATEWAY_JWT_SECRET"); s != "" {
		return []byte(s)
	}
	if fallbackKey != "" {
		return []byte(fallbackKey)
	}
	return []byte("default-jwt-secret-change-me")
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
func SignToken(userID int, tenantID, username, role, secretKey string) (string, time.Time, error) {
	expiry := jwtExpiry()
	expiresAt := time.Now().Add(expiry)

	claims := JWTClaims{
		UserID:   userID,
		TenantID: tenantID,
		Username: username,
		Role:     role,
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
