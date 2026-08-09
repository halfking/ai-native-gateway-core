package attachments

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// AuthMode 定义附件访问的认证模式。
type AuthMode string

const (
	// AuthModeNone 无需认证（公开访问，仅路径遍历防护）
	AuthModeNone AuthMode = "none"

	// AuthModeAPIKey 使用 Bearer token 认证（复用网关 API Key）
	// 客户端请求头：Authorization: Bearer sk-xxx
	AuthModeAPIKey AuthMode = "apikey"

	// AuthModeSigned 使用 HMAC 签名链接（现有机制，用于 session 附件）
	// URL 格式：/api/sessions/{sid}/attachments/{aid}?token={jwt}&expires={ts}
	AuthModeSigned AuthMode = "signed"

	// AuthModeAdmin 使用 Admin session cookie（现有机制，用于 admin 面板）
	// 依赖 admin 中间件注入的 session 上下文
	AuthModeAdmin AuthMode = "admin"
)

// AuthContext 认证上下文，包含认证后的身份信息。
type AuthContext struct {
	// TenantID 租户 ID（从 API Key 或 session 中提取）
	TenantID string

	// KeyID API Key ID（仅 AuthModeAPIKey 时有值）
	KeyID int

	// KeyPrefix API Key 前缀（用于审计日志）
	KeyPrefix string

	// UserID 用户 ID（仅 AuthModeAdmin 时有值）
	UserID *int64

	// Authenticated 是否通过认证
	Authenticated bool
}

// KeyVerifier 是 domains/authentication.KeyVerifier 的最小化接口。
// 用于解耦 attachments 包与 authentication 包，避免循环依赖。
type KeyVerifier interface {
	Verify(ctx context.Context, rawKey string) (*KeyInfo, error)
	Enabled() bool
}

// KeyInfo 是 authentication.KeyInfo 的最小化版本。
type KeyInfo struct {
	ID        int
	KeyPrefix string
	TenantID  string
	Status    string
	Enabled   bool
}

// Authenticator 附件访问认证器。
type Authenticator struct {
	mode        AuthMode
	keyVerifier KeyVerifier
}

// NewAuthenticator 创建认证器。
func NewAuthenticator(mode AuthMode, keyVerifier KeyVerifier) *Authenticator {
	return &Authenticator{
		mode:        mode,
		keyVerifier: keyVerifier,
	}
}

// Authenticate 认证请求并返回认证上下文。
func (a *Authenticator) Authenticate(r *http.Request) (*AuthContext, error) {
	switch a.mode {
	case AuthModeNone:
		return &AuthContext{Authenticated: true}, nil

	case AuthModeAPIKey:
		return a.authenticateAPIKey(r)

	case AuthModeSigned:
		// TODO: 实现 HMAC 签名验证（Phase 3）
		return nil, errors.New("signed mode not implemented yet")

	case AuthModeAdmin:
		// TODO: 从 request context 读取 admin session（Phase 3）
		return nil, errors.New("admin mode not implemented yet")

	default:
		return nil, errors.New("unknown auth mode")
	}
}

// authenticateAPIKey 使用 Bearer token 认证。
func (a *Authenticator) authenticateAPIKey(r *http.Request) (*AuthContext, error) {
	// 1. 提取 Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, errors.New("missing Authorization header")
	}

	// 2. 解析 Bearer token
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return nil, errors.New("invalid Authorization header format, expected: Bearer <token>")
	}
	rawKey := strings.TrimSpace(parts[1])

	// 3. 验证 API Key
	if a.keyVerifier == nil || !a.keyVerifier.Enabled() {
		return nil, errors.New("key verifier not configured")
	}

	keyInfo, err := a.keyVerifier.Verify(r.Context(), rawKey)
	if err != nil {
		return nil, err
	}

	// 4. 检查 key 状态
	if !keyInfo.Enabled || keyInfo.Status != "active" {
		return nil, errors.New("api key is disabled or inactive")
	}

	// 5. 返回认证上下文
	return &AuthContext{
		TenantID:      keyInfo.TenantID,
		KeyID:         keyInfo.ID,
		KeyPrefix:     keyInfo.KeyPrefix,
		Authenticated: true,
	}, nil
}
