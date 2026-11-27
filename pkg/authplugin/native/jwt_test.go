package native

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kaixuan/llm-gateway-go/admin"
)

// signToken 测试薄委托（2026-11-28 审计轮）：签发口径已统一到
// admin.SignTokenWithRoles（主角色前置 + roles claim），本地签名实现已删除；
// 本 helper 让既有 TTL/claims 语义测试继续走同一把密钥链与同一 SSOT。
func signToken(userID int, tenantID, username, role, secretKey string, mustChange bool) (string, time.Time, error) {
	return admin.SignTokenWithRoles(userID, tenantID, username, role, nil, secretKey, mustChange)
}

// Additional tests to boost JWT coverage to help reach >90% total
// These test the simpler helper functions that don't require complex mocking

func TestResolveJWTSecret_FromEnv(t *testing.T) {
	// Test with environment variable set
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "env-secret-key")
	
	secret := resolveJWTSecret("fallback")
	assert.NotNil(t, secret)
	assert.Equal(t, []byte("env-secret-key"), secret)
}

func TestResolveJWTSecret_Fallback(t *testing.T) {
	// Test with fallback when env is not set
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "")
	
	secret := resolveJWTSecret("fallback-key-12345")
	assert.NotNil(t, secret)
	assert.Equal(t, []byte("fallback-key-12345"), secret)
}

func TestResolveJWTSecret_NoSecret(t *testing.T) {
	// Test when neither env nor fallback are set
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "")
	
	secret := resolveJWTSecret("")
	assert.Nil(t, secret)
}

func TestSignToken_Success(t *testing.T) {
	token, expiry, err := signToken(1, "tenant1", "testuser", "user", "test-secret-key-123456", false)
	
	assert.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.False(t, expiry.IsZero())
}

func TestSignToken_WithMustChangePassword(t *testing.T) {
	token, expiry, err := signToken(1, "tenant1", "testuser", "user", "test-secret-key-123456", true)
	
	assert.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.False(t, expiry.IsZero())
}

func TestSignToken_NoSecret(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "")
	
	_, _, err := signToken(1, "tenant1", "testuser", "user", "", false)
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "secret not configured")
}

func TestVerifyToken_ValidToken(t *testing.T) {
	// Create a valid token first
	token, _, err := signToken(123, "tenant1", "alice", "admin", "test-secret-456789", false)
	assert.NoError(t, err)
	
	// Verify it
	claims, err := verifyToken(token, "test-secret-456789")
	assert.NoError(t, err)
	assert.NotNil(t, claims)
	assert.Equal(t, 123, claims.UserID)
	assert.Equal(t, "alice", claims.Username)
	assert.Equal(t, "admin", claims.Role)
	assert.Equal(t, "tenant1", claims.TenantID)
	assert.False(t, claims.MustChangePassword)
}

func TestVerifyToken_InvalidSignature(t *testing.T) {
	// Create a token with one secret
	token, _, err := signToken(123, "tenant1", "alice", "admin", "secret1", false)
	assert.NoError(t, err)
	
	// Try to verify with different secret
	_, err = verifyToken(token, "different-secret-wrong")
	assert.Error(t, err)
}

func TestVerifyToken_MalformedToken(t *testing.T) {
	_, err := verifyToken("not.a.valid.jwt.token", "test-secret")
	assert.Error(t, err)
}

func TestVerifyToken_EmptyToken(t *testing.T) {
	_, err := verifyToken("", "test-secret")
	assert.Error(t, err)
}

func TestVerifyToken_NoSecret(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "")
	
	_, err := verifyToken("some.token.here", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "secret not configured")
}

// F-A2 (2026-11-14 audit relay): signToken must honour the admin JWT TTL
// SSOT. The previous hardcoded 7d silently dropped the
// LLM_GATEWAY_JWT_EXPIRY env override that admin.SignToken honours — a
// deployment tightening admin token lifetimes kept issuing 7d native tokens.
func TestSignToken_HonoursJWTExpirySSOT(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "test-secret-key-123456")
	t.Setenv("LLM_GATEWAY_JWT_EXPIRY", "1h")

	_, expiry, err := signToken(1, "tenant1", "testuser", "user", "", false)
	assert.NoError(t, err)
	ttl := time.Until(expiry)
	assert.Greater(t, ttl, 55*time.Minute, "env override 1h must be honoured, got %v", ttl)
	assert.Less(t, ttl, 65*time.Minute, "env override 1h must be honoured, got %v", ttl)
}

func TestSignToken_DefaultTTLIsSevenDays(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "test-secret-key-123456")
	t.Setenv("LLM_GATEWAY_JWT_EXPIRY", "")

	_, expiry, err := signToken(1, "tenant1", "testuser", "user", "", false)
	assert.NoError(t, err)
	ttl := time.Until(expiry)
	assert.Greater(t, ttl, 6*24*time.Hour)
	assert.LessOrEqual(t, ttl, 7*24*time.Hour)
}
