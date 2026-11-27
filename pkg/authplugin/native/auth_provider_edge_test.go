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

// Additional edge case tests to push coverage above 90%

func TestAuthProvider_Authenticate_TenantCheckError(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "test-secret-key")
	ctx := context.Background()
	
	password := "TestPass123!"
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	
	// User query succeeds
	userRows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "password_hash", "display_name", "email", "role", "roles", "enabled", "must_change_password",
	}).AddRow(
		1, "tenant1", "testuser", string(hash), "Test", "test@example.com", "user", []string{"user"}, true, false,
	)
	
	mock.ExpectQuery(`SELECT id, tenant_id, username, password_hash`).
		WithArgs("testuser").
		WillReturnRows(userRows)
	
	// Tenant query fails
	mock.ExpectQuery(`SELECT status FROM tenants WHERE code`).
		WithArgs("tenant1").
		WillReturnError(errors.New("tenant not found"))
	
	_, err := provider.Authenticate(ctx, &authplugin.AuthRequest{
		Username: "testuser",
		Password: password,
	})
	
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthProvider_Authenticate_TenantDisabled(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "test-secret-key")
	ctx := context.Background()
	
	password := "TestPass123!"
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	
	userRows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "password_hash", "display_name", "email", "role", "roles", "enabled", "must_change_password",
	}).AddRow(
		1, "tenant1", "testuser", string(hash), "Test", "test@example.com", "user", []string{"user"}, true, false,
	)
	
	mock.ExpectQuery(`SELECT id, tenant_id, username, password_hash`).
		WithArgs("testuser").
		WillReturnRows(userRows)
	
	tenantRows := pgxmock.NewRows([]string{"status"}).AddRow("disabled")
	mock.ExpectQuery(`SELECT status FROM tenants WHERE code`).
		WithArgs("tenant1").
		WillReturnRows(tenantRows)
	
	_, err := provider.Authenticate(ctx, &authplugin.AuthRequest{
		Username: "testuser",
		Password: password,
	})
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthProvider_Authenticate_SuccessWithMustChange(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "test-secret-key-1234567890")
	ctx := context.Background()
	
	password := "TestPass123!"
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	
	userRows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "password_hash", "display_name", "email", "role", "roles", "enabled", "must_change_password",
	}).AddRow(
		1, "tenant1", "testuser", string(hash), "Test", "test@example.com", "user", []string{"user"}, true, true, // must_change_password=true
	)
	
	mock.ExpectQuery(`SELECT id, tenant_id, username, password_hash`).
		WithArgs("testuser").
		WillReturnRows(userRows)
	
	tenantRows := pgxmock.NewRows([]string{"status"}).AddRow("active")
	mock.ExpectQuery(`SELECT status FROM tenants WHERE code`).
		WithArgs("tenant1").
		WillReturnRows(tenantRows)
	
	result, err := provider.Authenticate(ctx, &authplugin.AuthRequest{
		Username: "testuser",
		Password: password,
	})
	
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.MustChangePassword)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthProvider_ValidateToken_Success(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "test-secret-key-1234567890")
	ctx := context.Background()
	
	// Create a valid token
	token, _, err := signToken(123, "tenant1", "alice", "admin", "test-secret-key-1234567890", false)
	assert.NoError(t, err)
	
	// No mock needed: admin.RevokedByPasswordChange needs *pgxpool.Pool,
	// mock is not assignable ⇒ skips the check (fail-open, same as user_manager.go).
	
	result, err := provider.ValidateToken(ctx, token)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 123, result.UserID)
	assert.Equal(t, "alice", result.Username)
	
	assert.NoError(t, mock.ExpectationsWereMet())
}


// TestAuthProvider_IsTokenRevoked_NotRevokedYet pins the **current** contract.
//
// ★ 2026-11-17：这条测试此前 mock 的是 `SELECT password_changed_at FROM users`
//   —— 那一列**全仓没有任何迁移创建** ⇒ 真机上这条查询必然报错 ⇒ 旧实现
//   走「读失败一律放行」⇒ 吊销永远不生效。⇒ **mock 把一个坏契约钉住了**：
//   绿，但线上是坏的。
// ⇒ 现在改为与 admin 面**共用**的判定（admin.RevokedByPasswordChange），
//   而那份判定读的是 settings_kv 里的吊销纪元。
// ⇒ 判据形状：mock 池**不是**真实 *pgxpool.Pool ⇒ 不查库、fail-open ⇒
//   **不吊销**（与 native/user_manager.go 的换密侧同一范式：mock 跳过吊销）。
func TestAuthProvider_IsTokenRevoked_NotRevokedYet(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	provider, _ := NewNativeAuthProvider(mock, "secret")
	ctx := context.Background()

	// Token issued just now
	issuedAt := time.Now()

	// ★ 刻意**不设**任何 ExpectQuery：mock 池不是真实 *pgxpool.Pool ⇒
	//   实现不应发起任何查询；万一发了，mock 会因「无预期」而失败 ⇒
	//   这条断言同时钉住「不查库」。
	revoked := provider.isTokenRevoked(ctx, 1, issuedAt)
	assert.False(t, revoked) // mock 池 ⇒ fail-open ⇒ 不吊销

	assert.NoError(t, mock.ExpectationsWereMet())
}
