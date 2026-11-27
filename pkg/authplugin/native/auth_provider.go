// Package native provides the NativeAuthProvider — a PostgreSQL-backed
// implementation of pkg/authplugin.AuthProvider that delegates to the
// existing admin JWT + bcrypt authentication stack.
//
// All existing security controls (bcrypt, rate limiting, captcha, tenant
// status checks, password-change flag, session revocation) are preserved
// and delegated back to the original admin package functions.
package native

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// NativeAuthProvider implements authplugin.AuthProvider using the existing
// PostgreSQL users table and HMAC-HS256 JWT stack.
type NativeAuthProvider struct {
	db        DBPool
	secretKey string // JWT signing key (LLM_GATEWAY_JWT_SECRET or SecretKey)
}

// NewNativeAuthProvider creates a NativeAuthProvider.
// db must be non-nil; secretKey is the JWT signing key fallback.
func NewNativeAuthProvider(db DBPool, secretKey string) (*NativeAuthProvider, error) {
	if db == nil {
		return nil, errors.New("native auth provider: db must not be nil")
	}
	return &NativeAuthProvider{db: db, secretKey: secretKey}, nil
}

// Name returns the provider identifier.
func (p *NativeAuthProvider) Name() string { return "native" }

// SupportsFeature reports which optional AuthFeature flags this provider handles.
func (p *NativeAuthProvider) SupportsFeature(f authplugin.AuthFeature) bool {
	switch f {
	case authplugin.FeaturePasswordAuth,
		authplugin.FeatureTokenValidation,
		authplugin.FeatureSessionRevocation:
		return true
	default:
		return false
	}
}

// Authenticate verifies username + password against the users table, issues a
// JWT, and returns an AuthResult.  It preserves the existing login logic from
// admin/auth.go handleLogin exactly, without introducing new behaviour.
func (p *NativeAuthProvider) Authenticate(ctx context.Context, req *authplugin.AuthRequest) (*authplugin.AuthResult, error) {
	if req.Username == "" || req.Password == "" {
		return nil, authplugin.ErrInvalidCredentials
	}

	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var u struct {
		ID                 int
		TenantID           string
		Username           string
		PasswordHash       string
		DisplayName        string
		Email              string
		Role               string
		Roles              []string
		Enabled            bool
		MustChangePassword bool
	}
	// ★ roles（P1-b 多角色，2026-11-28 审计轮补透传）：登录签发与 legacy 路径
	//   （admin/auth.go 同款 SELECT COALESCE(roles,'{}')）口径一致——否则 plugin
	//   路径签出的 token 无 roles claim、多角色在登录后到首次 /me 前不可见。
	err := p.db.QueryRow(qctx, `
		SELECT id, tenant_id, username, password_hash, display_name, email, role, COALESCE(roles, '{}'), enabled, must_change_password
		FROM users WHERE username = $1
	`, req.Username).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.PasswordHash,
		&u.DisplayName, &u.Email, &u.Role, &u.Roles, &u.Enabled, &u.MustChangePassword,
	)
	if err != nil {
		// user not found → map to ErrInvalidCredentials (don't leak existence)
		return nil, authplugin.ErrInvalidCredentials
	}

	if !u.Enabled {
		return nil, authplugin.ErrUserDisabled
	}

	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		return nil, authplugin.ErrInvalidCredentials
	}

	// Tenant status check (non-default tenants must be active)
	if u.TenantID != "" && u.TenantID != "default" {
		var status string
		if terr := p.db.QueryRow(qctx, `SELECT status FROM tenants WHERE code = $1`, u.TenantID).Scan(&status); terr != nil || status == "disabled" {
			return nil, authplugin.ErrTenantDisabled
		}
	}

	// Best-effort last-login update
	//nolint:errcheck
	p.db.Exec(qctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, u.ID)

	// ★ 2026-11-28 审计轮：改走 admin.SignTokenWithRoles（同一把 jwtSecret 链、
	//   主角色恒前置、TTL 同一 SSOT）——本地 signToken 的 jwtClaims 无 Roles 字段，
	//   签出的 token 无 roles claim ⇒ 多角色登录即丢。
	token, expiresAt, signErr := admin.SignTokenWithRoles(u.ID, u.TenantID, u.Username, u.Role, u.Roles, p.secretKey, u.MustChangePassword)
	if signErr != nil {
		slog.Error("native auth: sign token failed", "error", signErr)
		return nil, fmt.Errorf("native auth: sign token: %w", signErr)
	}

	return &authplugin.AuthResult{
		Success:            true,
		UserID:             u.ID,
		ExternalID:         fmt.Sprintf("native:%d", u.ID),
		Username:           u.Username,
		TenantID:           u.TenantID,
		Role:               u.Role,
		Roles:              u.Roles,
		DisplayName:        u.DisplayName,
		Email:              u.Email,
		Enabled:            u.Enabled,
		MustChangePassword: u.MustChangePassword,
		Token:              token,
		TokenType:          "Bearer",
		ExpiresAt:          expiresAt,
	}, nil
}

// ValidateToken verifies a JWT token and returns its claims.
func (p *NativeAuthProvider) ValidateToken(ctx context.Context, token string) (*authplugin.TokenClaims, error) {
	claims, err := verifyToken(token, p.secretKey)
	if err != nil {
		return nil, authplugin.ErrInvalidToken
	}
	issuedAt := time.Time{}
	if claims.IssuedAt != nil {
		issuedAt = claims.IssuedAt.Time
	}
	expiresAt := time.Time{}
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time
	}

	// Session revocation check: if user changed password after token issuance,
	// reject the token (admin/auth.go authRevocationProbe equivalent).
	if claims.UserID > 0 && !issuedAt.IsZero() {
		if p.isTokenRevoked(ctx, claims.UserID, issuedAt) {
			return nil, authplugin.ErrInvalidToken
		}
	}

	return &authplugin.TokenClaims{
		UserID:             claims.UserID,
		ExternalID:         fmt.Sprintf("native:%d", claims.UserID),
		Username:           claims.Username,
		TenantID:           claims.TenantID,
		Role:               claims.Role,
		Issuer:             "llm-gateway",
		Audience:           "llm-gateway-api",
		IssuedAt:           issuedAt,
		ExpiresAt:          expiresAt,
		MustChangePassword: claims.MustChangePassword,
	}, nil
}

// RefreshToken is not supported by the native provider (stateless JWT).
func (p *NativeAuthProvider) RefreshToken(_ context.Context, _ string) (*authplugin.AuthResult, error) {
	return nil, fmt.Errorf("native auth: refresh tokens not supported")
}

// Logout is a no-op for the native provider (stateless JWT; revocation is
// handled via password-change epoch in ValidateToken).
func (p *NativeAuthProvider) Logout(_ context.Context, _ string) error {
	return nil
}

// isTokenRevoked checks whether the user changed their password after the
// token was issued.
//
// ★ 2026-11-17 修正（真实网关 + 全仓 SQL 扫描实测）：此前这里直接查
//
//	`users.password_changed_at` —— **全仓没有任何迁移创建这一列**
//	（吊销纪元实际存在 admin 面的 settings_kv 行里）⇒ 查询必然报错
//	⇒ 本函数按「读失败一律放行」返回 false ⇒ **改密之后旧 JWT 一直存活
//	到 TTL** ⇒ 吊销在 native 插件这条路径上形同虚设。
//	⇒ 症状与"没接吊销"完全一样，且**不会报错**（fail-open 把它吃掉了）。
//
// ⇒ 改用与 admin 面**共用**的判定 `admin.RevokedByPasswordChange`：
//
//	同一份实现、同一份 5s TTL 缓存、同一把「读失败一律放行」的尺子
//	⇒ 两边口径不会漂（与 admin/auth_revocation.go 的导出面契约一致）。
//	范式同 native/user_manager.go 的换密侧：只有拿到真实 *pgxpool.Pool
//	才查；mock/测试实现跳过 ⇒ 维持 fail-open（不把整个登录面锁死）。
func (p *NativeAuthProvider) isTokenRevoked(ctx context.Context, userID int, issuedAt time.Time) bool {
	pool, ok := p.db.(*pgxpool.Pool)
	if !ok {
		return false // 不是真实池（mock/测试）⇒ fail-open，与 admin 面同尺子
	}
	return admin.RevokedByPasswordChange(ctx, pool, userID, issuedAt)
}
