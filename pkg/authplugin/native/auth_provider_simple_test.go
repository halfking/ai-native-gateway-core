package native

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"golang.org/x/crypto/bcrypt"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// Additional targeted tests for AuthProvider to reach >90% coverage
// Focus on testable paths without complex integration

func TestAuthProvider_NewWithNilDB(t *testing.T) {
	_, err := NewNativeAuthProvider(nil, "secret")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "db must not be nil")
}

func TestAuthProvider_Authenticate_EmptyCredentials(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "secret")
	ctx := context.Background()
	
	// Empty username
	_, err := provider.Authenticate(ctx, &authplugin.AuthRequest{
		Username: "",
		Password: "password",
	})
	assert.Equal(t, authplugin.ErrInvalidCredentials, err)
	
	// Empty password
	_, err = provider.Authenticate(ctx, &authplugin.AuthRequest{
		Username: "testuser",
		Password: "",
	})
	assert.Equal(t, authplugin.ErrInvalidCredentials, err)
}

func TestAuthProvider_Authenticate_UserQueryError(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "secret")
	ctx := context.Background()
	
	mock.ExpectQuery(`SELECT id, tenant_id, username, password_hash`).
		WithArgs("testuser").
		WillReturnError(errors.New("no rows"))
	
	_, err := provider.Authenticate(ctx, &authplugin.AuthRequest{
		Username: "testuser",
		Password: "password",
	})
	
	assert.Equal(t, authplugin.ErrInvalidCredentials, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthProvider_Authenticate_PasswordMismatch(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "secret")
	ctx := context.Background()
	
	// Hash a known password
	correctPass := "CorrectPass123!"
	hash, _ := bcrypt.GenerateFromPassword([]byte(correctPass), bcrypt.DefaultCost)
	
	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "password_hash", "display_name", "email", "role", "roles", "enabled", "must_change_password",
	}).AddRow(
		1, "tenant1", "testuser", string(hash), "Test User", "test@example.com", "user", []string{"user"}, true, false,
	)
	
	mock.ExpectQuery(`SELECT id, tenant_id, username, password_hash`).
		WithArgs("testuser").
		WillReturnRows(rows)
	
	// Try with wrong password
	_, err := provider.Authenticate(ctx, &authplugin.AuthRequest{
		Username: "testuser",
		Password: "WrongPass123!",
	})
	
	assert.Equal(t, authplugin.ErrInvalidCredentials, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthProvider_Authenticate_DisabledUser(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "secret")
	ctx := context.Background()
	
	password := "TestPass123!"
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	
	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "password_hash", "display_name", "email", "role", "roles", "enabled", "must_change_password",
	}).AddRow(
		1, "tenant1", "testuser", string(hash), "Test User", "test@example.com", "user", []string{"user"}, false, false, // disabled=false
	)
	
	mock.ExpectQuery(`SELECT id, tenant_id, username, password_hash`).
		WithArgs("testuser").
		WillReturnRows(rows)
	
	_, err := provider.Authenticate(ctx, &authplugin.AuthRequest{
		Username: "testuser",
		Password: password,
	})
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthProvider_ValidateToken_EmptyToken(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "secret")
	ctx := context.Background()
	
	_, err := provider.ValidateToken(ctx, "")
	assert.Error(t, err)
}

func TestAuthProvider_ValidateToken_InvalidFormat(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "secret")
	ctx := context.Background()
	
	_, err := provider.ValidateToken(ctx, "not-a-valid-jwt")
	assert.Error(t, err)
}

func TestAuthProvider_Logout_IsNoOp(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "secret")
	ctx := context.Background()
	
	// Logout should always succeed (no-op for stateless JWT)
	err := provider.Logout(ctx, "any-token")
	assert.NoError(t, err)
	
	err = provider.Logout(ctx, "")
	assert.NoError(t, err)
}

// TestAuthProvider_IsTokenRevoked_NoRevocationRecord pins the **current**
// contract: no revocation record ⇒ not revoked.
//
// ★ 2026-11-17：旧版 mock 的是 `SELECT password_changed_at FROM users`
//   —— 那一列**全仓没有任何迁移创建** ⇒ 真机上必然查询报错 ⇒ 旧实现
//   fail-open ⇒ 吊销永不生效（mock 却把它钉成绿的）。
// ⇒ 现在走 admin 面的共用判定；mock 池不是真实 *pgxpool.Pool ⇒ 不查库。
func TestAuthProvider_IsTokenRevoked_NoRevocationRecord(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	provider, _ := NewNativeAuthProvider(mock, "secret")
	ctx := context.Background()

	// ★ 不设任何 ExpectQuery：mock 池 ⇒ 实现不应发起查询（发了我就失败）。
	// Should return false (not revoked)
	issuedAt := time.Now()
	revoked := provider.isTokenRevoked(ctx, 1, issuedAt)
	assert.False(t, revoked)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthProvider_RefreshToken_NotSupported(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "secret")
	ctx := context.Background()
	
	_, err := provider.RefreshToken(ctx, "some-token")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
}
