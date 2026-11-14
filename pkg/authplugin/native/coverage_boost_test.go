package native

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// Additional tests to push coverage above 90%
// Targeting: ChangePassword success, ListUsers errors, ResetPassword errors, DeleteUser errors

func TestUserManager_ChangePassword_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	// Generate a valid bcrypt hash for the old password
	oldPassword := "OldPass123!"
	hash, _ := bcrypt.GenerateFromPassword([]byte(oldPassword), bcrypt.DefaultCost)

	rows := pgxmock.NewRows([]string{"password_hash"}).
		AddRow(string(hash))

	mock.ExpectQuery(`SELECT password_hash FROM users WHERE id`).
		WithArgs(1).
		WillReturnRows(rows)

	mock.ExpectExec(`UPDATE users SET password_hash`).
		WithArgs(pgxmock.AnyArg(), 1).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = mgr.ChangePassword(ctx, 1, oldPassword, "NewValidPass123!")
	assert.NoError(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ChangePassword_UpdateFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	oldPassword := "OldPass123!"
	hash, _ := bcrypt.GenerateFromPassword([]byte(oldPassword), bcrypt.DefaultCost)

	rows := pgxmock.NewRows([]string{"password_hash"}).
		AddRow(string(hash))

	mock.ExpectQuery(`SELECT password_hash FROM users WHERE id`).
		WithArgs(1).
		WillReturnRows(rows)

	mock.ExpectExec(`UPDATE users SET password_hash`).
		WithArgs(pgxmock.AnyArg(), 1).
		WillReturnError(errors.New("database error"))

	err = mgr.ChangePassword(ctx, 1, oldPassword, "NewValidPass123!")
	assert.Error(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ResetPassword_UpdateFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`UPDATE users SET password_hash`).
		WithArgs(pgxmock.AnyArg(), 1).
		WillReturnError(errors.New("database error"))

	err = mgr.ResetPassword(ctx, 1, "NewValidPass123!")
	assert.Error(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_DeleteUser_ExecFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`DELETE FROM users WHERE id`).
		WithArgs(1).
		WillReturnError(errors.New("database error"))

	err = mgr.DeleteUser(ctx, 1)
	assert.Error(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ListUsers_QueryFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectQuery(`SELECT (.+) FROM users ORDER BY id`).
		WillReturnError(errors.New("query failed"))

	_, err = mgr.ListUsers(ctx, nil)
	assert.Error(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ListUsers_WithTenantFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(
		1, "tenant1", "user1", "User 1", "[REDACTED:email]",
		"user", true, false, nil, time.Now(),
	)

	filter := &authplugin.UserFilter{
		TenantID: "tenant1",
	}

	mock.ExpectQuery(`SELECT (.+) FROM users WHERE`).
		WithArgs("tenant1").
		WillReturnRows(rows)

	users, err := mgr.ListUsers(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, users, 1)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ListUsers_WithRoleFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(
		1, "tenant1", "admin1", "Admin 1", "[REDACTED:email]",
		"admin", true, false, nil, time.Now(),
	)

	filter := &authplugin.UserFilter{
		Role: "admin",
	}

	mock.ExpectQuery(`SELECT (.+) FROM users WHERE`).
		WithArgs("admin").
		WillReturnRows(rows)

	users, err := mgr.ListUsers(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, users, 1)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ListUsers_WithEnabledFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(
		1, "tenant1", "user1", "User 1", "[REDACTED:email]",
		"user", true, false, nil, time.Now(),
	)

	enabled := true
	filter := &authplugin.UserFilter{
		Enabled: &enabled,
	}

	mock.ExpectQuery(`SELECT (.+) FROM users WHERE`).
		WithArgs(true).
		WillReturnRows(rows)

	users, err := mgr.ListUsers(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, users, 1)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ListUsers_WithSearchTerm(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(
		1, "tenant1", "alice", "Alice", "[REDACTED:email]",
		"user", true, false, nil, time.Now(),
	)

	filter := &authplugin.UserFilter{
		SearchTerm: "alice",
	}

	mock.ExpectQuery(`SELECT (.+) FROM users WHERE`).
		WithArgs("%alice%").
		WillReturnRows(rows)

	users, err := mgr.ListUsers(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, users, 1)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ListUsers_RowScanError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	// Create rows with wrong number of columns to trigger scan error
	rows := pgxmock.NewRows([]string{
		"id", "tenant_id",
	}).AddRow(1, "tenant1")

	mock.ExpectQuery(`SELECT (.+) FROM users ORDER BY id`).
		WillReturnRows(rows)

	users, err := mgr.ListUsers(ctx, nil)
	// Should still return successfully, just skipping bad rows
	require.NoError(t, err)
	assert.Len(t, users, 0)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_DeleteTenant_ExecFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`DELETE FROM tenants WHERE code`).
		WithArgs("t1").
		WillReturnError(errors.New("database error"))

	err = mgr.DeleteTenant(ctx, "t1")
	assert.Error(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_SetTenantStatus_ExecFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`UPDATE tenants SET status`).
		WithArgs("active", "t1").
		WillReturnError(errors.New("database error"))

	err = mgr.setTenantStatus(ctx, "t1", "active")
	assert.Error(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_ListTenants_QueryFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectQuery(`SELECT (.+) FROM tenants ORDER BY code`).
		WillReturnError(errors.New("query failed"))

	_, err = mgr.ListTenants(ctx, nil)
	assert.Error(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_ListTenants_RowScanError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	// Create rows with wrong structure to trigger scan error
	rows := pgxmock.NewRows([]string{"code"}).AddRow("t1")

	mock.ExpectQuery(`SELECT (.+) FROM tenants ORDER BY code`).
		WillReturnRows(rows)

	tenants, err := mgr.ListTenants(ctx, nil)
	// Should still return successfully, skipping bad rows
	require.NoError(t, err)
	assert.Len(t, tenants, 0)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_CreateUser_EmptyPassword(t *testing.T) {
	mgr := &NativeUserManager{}
	ctx := context.Background()

	req := &authplugin.CreateUserRequest{
		Username: "[REDACTED:account]",
		Password: "",
	}

	_, err := mgr.CreateUser(ctx, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestUserManager_CreateUser_WeakPassword(t *testing.T) {
	mgr := &NativeUserManager{}
	ctx := context.Background()

	req := &authplugin.CreateUserRequest{
		Username: "[REDACTED:account]",
		Password: "weak",
	}

	_, err := mgr.CreateUser(ctx, req)
	assert.Error(t, err)
}
