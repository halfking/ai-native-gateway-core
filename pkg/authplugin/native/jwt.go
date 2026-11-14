package native

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwtClaims is the internal JWT structure matching admin/jwt.go JWTClaims.
type jwtClaims struct {
	UserID             int    `json:"user_id"`
	TenantID           string `json:"tenant_id"`
	Username           string `json:"username"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"must_change_password,omitempty"`
	jwt.RegisteredClaims
}

const audienceName = "llm-gateway-api"

// signToken creates a JWT token matching the existing admin/jwt.go MakeToken logic.
func signToken(userID int, tenantID, username, role, secretKey string, mustChange bool) (string, time.Time, error) {
	key := resolveJWTSecret(secretKey)
	if key == nil {
		return "", time.Time{}, fmt.Errorf("JWT secret not configured")
	}

	now := time.Now()
	expiresAt := now.Add(24 * time.Hour * 7) // 7 days default

	claims := &jwtClaims{
		UserID:             userID,
		TenantID:           tenantID,
		Username:           username,
		Role:               role,
		MustChangePassword: mustChange,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "llm-gateway",
			Audience:  jwt.ClaimStrings{audienceName},
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(key)
	if err != nil {
		return "", time.Time{}, err
	}
	return tokenStr, expiresAt, nil
}

// verifyToken verifies a JWT token and returns the parsed claims.
func verifyToken(tokenStr, secretKey string) (*jwtClaims, error) {
	key := resolveJWTSecret(secretKey)
	if key == nil {
		return nil, fmt.Errorf("JWT secret not configured")
	}

	token, err := jwt.ParseWithClaims(tokenStr, &jwtClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return key, nil
	}, jwt.WithIssuer("llm-gateway"), jwt.WithAudience(audienceName))

	if err != nil {
		return nil, fmt.Errorf("parse jwt: %w", err)
	}

	parsedClaims, ok := token.Claims.(*jwtClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid jwt claims")
	}

	return parsedClaims, nil
}

// resolveJWTSecret returns the JWT signing key from LLM_GATEWAY_JWT_SECRET
// environment variable or the provided secretKey fallback.
func resolveJWTSecret(fallback string) []byte {
	if s := os.Getenv("LLM_GATEWAY_JWT_SECRET"); s != "" {
		return []byte(s)
	}
	if fallback != "" {
		return []byte(fallback)
	}
	return nil
}
