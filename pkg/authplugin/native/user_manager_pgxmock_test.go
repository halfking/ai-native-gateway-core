package native

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// Comprehensive mock-based tests for NativeUserManager using pgxmock
// These tests target >90% code coverage without requiring a real database

func TestNewNativeUserManager_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, err := NewNativeUserManager(mock)
	require.NoError(t, err)
	assert.NotNil(t, mgr)
	assert.Equal(t, "native", mgr.Name())
}

func TestNewNativeUserManager_NilDB(t *testing.T) {
	_, err := NewNativeUserManager(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "db must not be nil")
}

func TestUserManager_GetUser_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(
		123, "test-tenant", "testuser", "Test User", "test@example.com",
		"user", true, false, nil, time.Now(),
	)

	mock.ExpectQuery(`SELECT (.+) FROM users WHERE id`).
		WithArgs(123).
		WillReturnRows(rows)

	user, err := mgr.GetUser(ctx, 123)
	require.NoError(t, err)
	assert.Equal(t, 123, user.ID)
	assert.Equal(t, "testuser", user.Username)
	assert.Equal(t, "native:123", user.ExternalID)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_GetUser_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectQuery(`SELECT (.+) FROM users WHERE id`).
		WithArgs(999).
		WillReturnError(errors.New("no rows"))

	_, err = mgr.GetUser(ctx, 999)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_GetUserByUsername_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(
		1, "tenant1", "alice", "Alice", "alice@test.com",
		"user", true, false, nil, time.Now(),
	)

	mock.ExpectQuery(`SELECT (.+) FROM users WHERE username`).
		WithArgs("alice").
		WillReturnRows(rows)

	user, err := mgr.GetUserByUsername(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, "alice", user.Username)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_GetUserByUsername_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectQuery(`SELECT (.+) FROM users WHERE username`).
		WithArgs("nonexistent").
		WillReturnError(errors.New("no rows"))

	_, err = mgr.GetUserByUsername(ctx, "nonexistent")
	assert.Equal(t, authplugin.ErrNotFound, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_GetUserByExternalID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(
		42, "tenant1", "bob", "Bob", "bob@test.com",
		"user", true, false, nil, time.Now(),
	)

	mock.ExpectQuery(`SELECT (.+) FROM users WHERE id`).
		WithArgs(42).
		WillReturnRows(rows)

	user, err := mgr.GetUserByExternalID(ctx, "native:42")
	require.NoError(t, err)
	assert.Equal(t, 42, user.ID)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ListUsers_NoFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(
		1, "t1", "u1", "User 1", "u1@test.com",
		"user", true, false, nil, time.Now(),
	).AddRow(
		2, "t1", "u2", "User 2", "u2@test.com",
		"admin", true, false, nil, time.Now(),
	)

	mock.ExpectQuery(`SELECT (.+) FROM users ORDER BY id`).
		WillReturnRows(rows)

	users, err := mgr.ListUsers(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, users, 2)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ListUsers_WithLimitAndOffset(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(
		1, "tenant1", "user1", "User 1", "u1@test.com",
		"user", true, false, nil, time.Now(),
	)

	filter := &authplugin.UserFilter{
		Limit:  10,
		Offset: 5,
	}

	mock.ExpectQuery(`SELECT (.+) FROM users ORDER BY id LIMIT`).
		WithArgs(10, 5).
		WillReturnRows(rows)

	users, err := mgr.ListUsers(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, users, 1)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ListUsers_EmptyResult(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	})

	mock.ExpectQuery(`SELECT (.+) FROM users ORDER BY id`).
		WillReturnRows(rows)

	users, err := mgr.ListUsers(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, users, 0)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_CreateUser_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "created_at",
	}).AddRow(
		1, "tenant1", "newuser", "New User", "new@test.com",
		"user", true, false, time.Now(),
	)

	mock.ExpectQuery(`INSERT INTO users`).
		WithArgs("tenant1", "newuser", pgxmock.AnyArg(), "New User", "new@test.com",
			"user", true, false).
		WillReturnRows(rows)

	req := &authplugin.CreateUserRequest{
		TenantID:    "tenant1",
		Username:    "newuser",
		Password:    "ValidPass123!",
		DisplayName: "New User",
		Email:       "new@test.com",
		Role:        "user",
		Enabled:     true,
	}

	user, err := mgr.CreateUser(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "newuser", user.Username)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_CreateUser_WithDefaults(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "created_at",
	}).AddRow(
		1, "default", "user", "", "",
		"tenant_admin", false, false, time.Now(),
	)

	mock.ExpectQuery(`INSERT INTO users`).
		WithArgs("default", "user", pgxmock.AnyArg(), "", "",
			"tenant_admin", false, false).
		WillReturnRows(rows)

	req := &authplugin.CreateUserRequest{
		Username: "user",
		Password: "ValidPass123!",
	}

	user, err := mgr.CreateUser(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "default", user.TenantID)
	assert.Equal(t, "tenant_admin", user.Role)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_CreateUser_DuplicateUsername(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectQuery(`INSERT INTO users`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("duplicate key value violates unique constraint"))

	req := &authplugin.CreateUserRequest{
		Username: "existing",
		Password: "ValidPass123!",
	}

	_, err = mgr.CreateUser(ctx, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_UpdateUser_AllFields(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(
		1, "tenant1", "user", "Updated Name", "updated@test.com",
		"admin", false, true, nil, time.Now(),
	)

	name := "Updated Name"
	email := "updated@test.com"
	role := "admin"
	enabled := false
	mustChange := true
	password := "NewPass456!"

	mock.ExpectQuery(`UPDATE users SET`).
		WithArgs(name, email, role, enabled, mustChange, pgxmock.AnyArg(), pgxmock.AnyArg(), 1).
		WillReturnRows(rows)

	req := &authplugin.UpdateUserRequest{
		DisplayName:        &name,
		Email:              &email,
		Role:               &role,
		Enabled:            &enabled,
		MustChangePassword: &mustChange,
		Password:           &password,
	}

	user, err := mgr.UpdateUser(ctx, 1, req)
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", user.DisplayName)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_UpdateUser_NoChanges(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(
		1, "tenant1", "user", "User", "user@test.com",
		"user", true, false, nil, time.Now(),
	)

	mock.ExpectQuery(`SELECT (.+) FROM users WHERE id`).
		WithArgs(1).
		WillReturnRows(rows)

	req := &authplugin.UpdateUserRequest{}
	user, err := mgr.UpdateUser(ctx, 1, req)
	require.NoError(t, err)
	assert.Equal(t, 1, user.ID)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_DeleteUser_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`DELETE FROM users WHERE id`).
		WithArgs(1).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = mgr.DeleteUser(ctx, 1)
	assert.NoError(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_DeleteUser_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`DELETE FROM users WHERE id`).
		WithArgs(999).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	err = mgr.DeleteUser(ctx, 999)
	assert.Equal(t, authplugin.ErrNotFound, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ResetPassword_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`UPDATE users SET password_hash`).
		WithArgs(pgxmock.AnyArg(), 1).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = mgr.ResetPassword(ctx, 1, "NewValidPass123!")
	assert.NoError(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ResetPassword_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`UPDATE users SET password_hash`).
		WithArgs(pgxmock.AnyArg(), 999).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = mgr.ResetPassword(ctx, 999, "NewValidPass123!")
	assert.Equal(t, authplugin.ErrNotFound, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ChangePassword_WrongOldPassword(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{"password_hash"}).
		AddRow("$2a$10$somebcrypthash")

	mock.ExpectQuery(`SELECT password_hash FROM users WHERE id`).
		WithArgs(1).
		WillReturnRows(rows)

	err = mgr.ChangePassword(ctx, 1, "WrongOldPass", "NewValidPass123!")
	assert.Equal(t, authplugin.ErrInvalidCredentials, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_ChangePassword_UserNotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectQuery(`SELECT password_hash FROM users WHERE id`).
		WithArgs(999).
		WillReturnError(errors.New("no rows"))

	err = mgr.ChangePassword(ctx, 999, "OldPass123!", "NewValidPass123!")
	assert.Equal(t, authplugin.ErrNotFound, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserManager_UpdateLastLogin(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`UPDATE users SET last_login_at`).
		WithArgs(1).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = mgr.UpdateLastLogin(ctx, 1)
	assert.NoError(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

// F-A1 同族穷举收口（2026-11-14 审计轮）正向对照：UpdateUser 的合法密码
// 必须一路走到 UPDATE（不得被复杂度策略误拒）——弱密码侧的负向钉测在
// user_manager_test.go（db=nil，拒门先于 DB）。
func TestUserManager_UpdateUser_ValidPasswordReachesUpdate(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeUserManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email",
		"role", "enabled", "must_change_password", "last_login_at", "created_at",
	}).AddRow(1, "default", "u", "U", "", "user", true, false, nil, time.Now())

	mock.ExpectQuery(`UPDATE users SET password_hash = \$1, password_changed_at = \$2, updated_at = now\(\) WHERE id = \$3`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), 1).
		WillReturnRows(rows)

	ok := "Str0ngPass"
	user, err := mgr.UpdateUser(ctx, 1, &authplugin.UpdateUserRequest{Password: &ok})
	require.NoError(t, err)
	assert.NotNil(t, user)

	assert.NoError(t, mock.ExpectationsWereMet())
}
