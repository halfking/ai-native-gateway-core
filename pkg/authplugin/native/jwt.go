package native

import (
	"fmt"
	"os"

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

// signToken 已删除（2026-11-28 审计轮）：本地 jwtClaims 无 Roles 字段，签出的
// token 无 roles claim ⇒ 多角色登录即丢（admin.SignTokenWithRoles 是唯一签发
// 口径，TTL/密钥链同一 SSOT；见 auth_provider.go 的调用点）。

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
