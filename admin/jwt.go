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
	// Roles 多角色数组（三级配额制 P1-b，2026-11-27）。可选 claim：
	//   - 旧 token 无此字段 ⇒ 解析后为 nil ⇒ 中间件回落 [Role]（存量兼容）；
	//   - 新 token 由 SignToken 写入（含主角色在内的有效角色集）。
	Roles              []string `json:"roles,omitempty"`
	MustChangePassword bool     `json:"must_change_password,omitempty"`
	jwt.RegisteredClaims
}

// EffectiveRoles 返回本 token 的有效角色集：Roles 非空用它；否则回落 [Role]
//（旧 token / legacy 形态）。主角色恒为首元素（SignToken 侧维护）。
func (c *JWTClaims) EffectiveRoles() []string {
	if len(c.Roles) > 0 {
		return c.Roles
	}
	if c.Role == "" {
		return nil
	}
	return []string{c.Role}
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
	return 7 * 24 * time.Hour // SSOT: admin/auth_params.go JWTDefaultTTL
}

// JWTExpiry exposes the admin JWT TTL SSOT (jwtExpiry) to out-of-package
// callers that must stay TTL-aligned with admin tokens. Added for F-A2
// (2026-11-14 audit relay): the native authplugin signToken hardcoded the
// 7d default, so the LLM_GATEWAY_JWT_EXPIRY env override never reached
// natively-signed tokens — a deployment tightening the admin TTL would
// silently keep issuing 7d native tokens. Precedence: env override →
// JWTDefaultTTL (admin/auth_params.go, same fact source as CookieMaxAge).
func JWTExpiry() time.Duration {
	return jwtExpiry()
}

// SignToken creates a signed JWT string for the given user.
// Returns an error if no JWT signing key is configured (rule 20 §8.2).
//
// roles（三级配额制 P1-b，2026-11-27）：有效角色集（可 nil）。nil/空 ⇒ 只写
// 主角色 claim（与旧 token 逐字节同形）；非空 ⇒ Roles claim 带全量、Role 仍写
// 主角色（roles[0]，存量读者/多 issuer 校验用）。
func SignToken(userID int, tenantID, username, role, secretKey string, mustChangePassword bool) (string, time.Time, error) {
	return SignTokenWithRoles(userID, tenantID, username, role, nil, secretKey, mustChangePassword)
}

// SignTokenWithRoles 是 SignToken 的多角色变体（P1-b）：roles[0] 视为主角色
//（为空回落 role 参数）；claims 里 Role=主角色、Roles=全量有效集。
func SignTokenWithRoles(userID int, tenantID, username, role string, roles []string, secretKey string, mustChangePassword bool) (string, time.Time, error) {
	if !jwtSecretConfigured(secretKey) {
		return "", time.Time{}, fmt.Errorf("jwt signing secret not configured (set LLM_GATEWAY_JWT_SECRET or LLM_GATEWAY_SECRET_KEY)")
	}
	// 主角色恒为 Roles 首元素（AuthContext.HasRole/存量 Role== 读者都依赖该
	// 契约）：调用方传来的数组若缺主角色则前置，重复则去重保序。
	roles = effectiveLoginRoles(role, roles)
	expiry := jwtExpiry()
	expiresAt := time.Now().Add(expiry)

	claims := JWTClaims{
		UserID:             userID,
		TenantID:           tenantID,
		Username:           username,
		Role:               role,
		Roles:              roles,
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
	// 兼容旧 token（无 Roles claim）：Roles 为空 ⇒ 回落 [Role]（P1-b，2026-11-27）。
	if len(parsedClaims.Roles) == 0 && parsedClaims.Role != "" {
		parsedClaims.Roles = []string{parsedClaims.Role}
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
	issuedAt := time.Time{}
	if c.IssuedAt != nil {
		issuedAt = c.IssuedAt.Time
	}
	return &identity.LegacyClaims{
		UserID:             c.UserID,
		TenantID:           c.TenantID,
		Username:           c.Username,
		Role:               c.Role,
		Roles:              c.EffectiveRoles(), // P1-b：多角色有效集透传
		MustChangePassword: c.MustChangePassword,
		IssuedAt:           issuedAt,
	}, nil
}
