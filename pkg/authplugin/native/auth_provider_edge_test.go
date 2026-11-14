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
		"id", "tenant_id", "username", "password_hash", "display_name", "email", "role", "enabled", "must_change_password",
	}).AddRow(
		1, "tenant1", "testuser", string(hash), "Test", "test@example.com", "user", true, false,
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
		"id", "tenant_id", "username", "password_hash", "display_name", "email", "role", "enabled", "must_change_password",
	}).AddRow(
		1, "tenant1", "testuser", string(hash), "Test", "test@example.com", "user", true, false,
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
		"id", "tenant_id", "username", "password_hash", "display_name", "email", "role", "enabled", "must_change_password",
	}).AddRow(
		1, "tenant1", "testuser", string(hash), "Test", "test@example.com", "user", true, true, // must_change_password=true
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
	
	// Mock password_changed_at check - no password change
	mock.ExpectQuery(`SELECT password_changed_at FROM users WHERE id`).
		WithArgs(123).
		WillReturnRows(pgxmock.NewRows([]string{"password_changed_at"}).AddRow(nil))
	
	result, err := provider.ValidateToken(ctx, token)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 123, result.UserID)
	assert.Equal(t, "alice", result.Username)
	
	assert.NoError(t, mock.ExpectationsWereMet())
}


func TestAuthProvider_IsTokenRevoked_NotRevokedYet(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	
	provider, _ := NewNativeAuthProvider(mock, "secret")
	ctx := context.Background()
	
	// Token issued just now
	issuedAt := time.Now()
	
	// Password was changed 1 hour ago (before token was issued)
	passwordChangedAt := time.Now().Add(-1 * time.Hour)
	
	mock.ExpectQuery(`SELECT password_changed_at FROM users WHERE id`).
		WithArgs(1).
		WillReturnRows(pgxmock.NewRows([]string{"password_changed_at"}).AddRow(passwordChangedAt))
	
	revoked := provider.isTokenRevoked(ctx, 1, issuedAt)
	assert.False(t, revoked) // Not revoked - token issued after password change
	
	assert.NoError(t, mock.ExpectationsWereMet())
}
