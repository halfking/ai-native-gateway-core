package native

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// Integration tests for NativeUserManager that require a real database connection.
// Set TEST_DATABASE_URL or TEST_DB_URL to run these tests.

func setupUserTestDB(t *testing.T) (*pgxpool.Pool, *NativeUserManager) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL not set, skipping integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)

	mgr, err := NewNativeUserManager(pool)
	if err != nil {
		t.Fatalf("NewNativeUserManager: %v", err)
	}

	return pool, mgr
}

func cleanupTestUser(t *testing.T, pool *pgxpool.Pool, username string) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username = $1`, username)
}

func TestNativeUserManager_Integration_CreateAndGet(t *testing.T) {
	pool, mgr := setupUserTestDB(t)
	ctx := context.Background()

	username := "test_create_user_" + time.Now().Format("20060102150405")
	defer cleanupTestUser(t, pool, username)

	// Create user
	req := &authplugin.CreateUserRequest{
		TenantID:    "test-tenant",
		Username:    username,
		Password:    "TestPass123!",
		DisplayName: "Test User",
		Email:       "test@example.com",
		Role:        "user",
		Enabled:     true,
	}

	user, err := mgr.CreateUser(ctx, req)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	if user.ID == 0 {
		t.Error("Created user should have non-zero ID")
	}
	if user.Username != username {
		t.Errorf("Username = %v, want %v", user.Username, username)
	}
	if user.TenantID != "test-tenant" {
		t.Errorf("TenantID = %v, want test-tenant", user.TenantID)
	}
	if user.Role != "user" {
		t.Errorf("Role = %v, want user", user.Role)
	}

	// GetUser by ID
	retrieved, err := mgr.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if retrieved.Username != username {
		t.Errorf("Retrieved username = %v, want %v", retrieved.Username, username)
	}

	// GetUserByUsername
	byUsername, err := mgr.GetUserByUsername(ctx, username)
	if err != nil {
		t.Fatalf("GetUserByUsername failed: %v", err)
	}
	if byUsername.ID != user.ID {
		t.Errorf("GetUserByUsername ID = %v, want %v", byUsername.ID, user.ID)
	}

	// GetUserByExternalID
	externalID := user.ExternalID
	byExternal, err := mgr.GetUserByExternalID(ctx, externalID)
	if err != nil {
		t.Fatalf("GetUserByExternalID failed: %v", err)
	}
	if byExternal.ID != user.ID {
		t.Errorf("GetUserByExternalID ID = %v, want %v", byExternal.ID, user.ID)
	}
}

func TestNativeUserManager_Integration_CreateDuplicate(t *testing.T) {
	_, mgr := setupUserTestDB(t)
	ctx := context.Background()

	username := "test_dup_user_" + time.Now().Format("20060102150405")

	req := &authplugin.CreateUserRequest{
		TenantID: "test-tenant",
		Username: username,
		Password: "TestPass123!",
		Enabled:  true,
	}

	// Create first user
	user1, err := mgr.CreateUser(ctx, req)
	if err != nil {
		t.Fatalf("First CreateUser failed: %v", err)
	}
	defer func() {
		_ = mgr.DeleteUser(ctx, user1.ID)
	}()

	// Try to create duplicate
	_, err = mgr.CreateUser(ctx, req)
	if err == nil {
		t.Error("CreateUser with duplicate username should fail")
	}
	if err != nil && err != authplugin.ErrConflict {
		// Should return ErrConflict or wrap it
		t.Logf("Duplicate error: %v", err)
	}
}

func TestNativeUserManager_Integration_ListUsers(t *testing.T) {
	pool, mgr := setupUserTestDB(t)
	ctx := context.Background()

	// Create test users
	baseUsername := "test_list_user_" + time.Now().Format("20060102150405")
	userIDs := make([]int, 0, 3)

	for i := 0; i < 3; i++ {
		username := baseUsername + "_" + string(rune('a'+i))
		req := &authplugin.CreateUserRequest{
			TenantID: "test-tenant-list",
			Username: username,
			Password: "TestPass123!",
			Role:     "user",
			Enabled:  true,
		}
		user, err := mgr.CreateUser(ctx, req)
		if err != nil {
			t.Fatalf("CreateUser %d failed: %v", i, err)
		}
		userIDs = append(userIDs, user.ID)
		defer cleanupTestUser(t, pool, username)
	}

	// List all users for this tenant
	filter := &authplugin.UserFilter{
		TenantID: "test-tenant-list",
	}
	users, err := mgr.ListUsers(ctx, filter)
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}

	if len(users) < 3 {
		t.Errorf("ListUsers returned %d users, want at least 3", len(users))
	}

	// Test with search term
	searchFilter := &authplugin.UserFilter{
		TenantID:   "test-tenant-list",
		SearchTerm: baseUsername,
	}
	searchResults, err := mgr.ListUsers(ctx, searchFilter)
	if err != nil {
		t.Fatalf("ListUsers with search failed: %v", err)
	}
	if len(searchResults) < 3 {
		t.Errorf("Search returned %d users, want at least 3", len(searchResults))
	}

	// Test with limit
	limitFilter := &authplugin.UserFilter{
		TenantID: "test-tenant-list",
		Limit:    2,
	}
	limitedResults, err := mgr.ListUsers(ctx, limitFilter)
	if err != nil {
		t.Fatalf("ListUsers with limit failed: %v", err)
	}
	if len(limitedResults) > 2 {
		t.Errorf("Limited search returned %d users, want max 2", len(limitedResults))
	}
}

func TestNativeUserManager_Integration_UpdateUser(t *testing.T) {
	pool, mgr := setupUserTestDB(t)
	ctx := context.Background()

	username := "test_update_user_" + time.Now().Format("20060102150405")
	defer cleanupTestUser(t, pool, username)

	// Create user
	req := &authplugin.CreateUserRequest{
		TenantID: "test-tenant",
		Username: username,
		Password: "TestPass123!",
		Role:     "user",
		Enabled:  true,
	}
	user, err := mgr.CreateUser(ctx, req)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Update display name and email
	newDisplayName := "Updated Name"
	newEmail := "updated@example.com"
	updateReq := &authplugin.UpdateUserRequest{
		DisplayName: &newDisplayName,
		Email:       &newEmail,
	}

	updated, err := mgr.UpdateUser(ctx, user.ID, updateReq)
	if err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}

	if updated.DisplayName != newDisplayName {
		t.Errorf("DisplayName = %v, want %v", updated.DisplayName, newDisplayName)
	}
	if updated.Email != newEmail {
		t.Errorf("Email = %v, want %v", updated.Email, newEmail)
	}

	// Update role
	newRole := "tenant_admin"
	roleReq := &authplugin.UpdateUserRequest{
		Role: &newRole,
	}
	updated, err = mgr.UpdateUser(ctx, user.ID, roleReq)
	if err != nil {
		t.Fatalf("UpdateUser role failed: %v", err)
	}
	if updated.Role != newRole {
		t.Errorf("Role = %v, want %v", updated.Role, newRole)
	}

	// Update enabled status
	enabled := false
	statusReq := &authplugin.UpdateUserRequest{
		Enabled: &enabled,
	}
	updated, err = mgr.UpdateUser(ctx, user.ID, statusReq)
	if err != nil {
		t.Fatalf("UpdateUser status failed: %v", err)
	}
	if updated.Enabled != false {
		t.Error("User should be disabled")
	}
}

func TestNativeUserManager_Integration_DeleteUser(t *testing.T) {
	pool, mgr := setupUserTestDB(t)
	ctx := context.Background()

	username := "test_delete_user_" + time.Now().Format("20060102150405")
	defer cleanupTestUser(t, pool, username)

	// Create user
	req := &authplugin.CreateUserRequest{
		TenantID: "test-tenant",
		Username: username,
		Password: "TestPass123!",
		Enabled:  true,
	}
	user, err := mgr.CreateUser(ctx, req)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Delete user
	err = mgr.DeleteUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}

	// Verify user is deleted
	_, err = mgr.GetUser(ctx, user.ID)
	if err != authplugin.ErrNotFound {
		t.Error("GetUser on deleted user should return ErrNotFound")
	}

	// Try to delete non-existent user
	err = mgr.DeleteUser(ctx, 9999999)
	if err != authplugin.ErrNotFound {
		t.Errorf("DeleteUser non-existent should return ErrNotFound, got %v", err)
	}
}

func TestNativeUserManager_Integration_ResetPassword(t *testing.T) {
	pool, mgr := setupUserTestDB(t)
	ctx := context.Background()

	username := "test_reset_pwd_" + time.Now().Format("20060102150405")
	defer cleanupTestUser(t, pool, username)

	// Create user
	req := &authplugin.CreateUserRequest{
		TenantID: "test-tenant",
		Username: username,
		Password: "OldPass123!",
		Enabled:  true,
	}
	user, err := mgr.CreateUser(ctx, req)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Reset password
	newPassword := "NewPass456!"
	err = mgr.ResetPassword(ctx, user.ID, newPassword)
	if err != nil {
		t.Fatalf("ResetPassword failed: %v", err)
	}

	// Verify password was changed (would need auth provider to fully test)
	// For now, just verify no error
	retrieved, err := mgr.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser after reset failed: %v", err)
	}
	if retrieved.MustChangePassword != true {
		t.Error("must_change_password should be true after reset")
	}

	// Test with weak password
	err = mgr.ResetPassword(ctx, user.ID, "weak")
	if err == nil {
		t.Error("ResetPassword with weak password should fail")
	}
}

func TestNativeUserManager_Integration_ChangePassword(t *testing.T) {
	pool, mgr := setupUserTestDB(t)
	ctx := context.Background()

	username := "test_change_pwd_" + time.Now().Format("20060102150405")
	defer cleanupTestUser(t, pool, username)

	oldPassword := "OldPass123!"
	// Create user
	req := &authplugin.CreateUserRequest{
		TenantID: "test-tenant",
		Username: username,
		Password: oldPassword,
		Enabled:  true,
	}
	user, err := mgr.CreateUser(ctx, req)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Change password with correct old password
	newPassword := "NewPass456!"
	err = mgr.ChangePassword(ctx, user.ID, oldPassword, newPassword)
	if err != nil {
		t.Fatalf("ChangePassword failed: %v", err)
	}

	// Try to change with wrong old password
	err = mgr.ChangePassword(ctx, user.ID, "WrongPass123!", "AnotherPass789!")
	if err != authplugin.ErrInvalidCredentials {
		t.Errorf("ChangePassword with wrong old password should return ErrInvalidCredentials, got %v", err)
	}

	// Try with weak new password
	err = mgr.ChangePassword(ctx, user.ID, newPassword, "weak")
	if err == nil {
		t.Error("ChangePassword with weak new password should fail")
	}
}

func TestNativeUserManager_Integration_UpdateLastLogin(t *testing.T) {
	pool, mgr := setupUserTestDB(t)
	ctx := context.Background()

	username := "test_last_login_" + time.Now().Format("20060102150405")
	defer cleanupTestUser(t, pool, username)

	// Create user
	req := &authplugin.CreateUserRequest{
		TenantID: "test-tenant",
		Username: username,
		Password: "TestPass123!",
		Enabled:  true,
	}
	user, err := mgr.CreateUser(ctx, req)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Initially should be nil
	if user.LastLoginAt != nil {
		t.Error("New user should have nil LastLoginAt")
	}

	// Update last login
	time.Sleep(100 * time.Millisecond)
	err = mgr.UpdateLastLogin(ctx, user.ID)
	if err != nil {
		t.Fatalf("UpdateLastLogin failed: %v", err)
	}

	// Verify it was updated
	retrieved, err := mgr.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser after UpdateLastLogin failed: %v", err)
	}
	if retrieved.LastLoginAt == nil {
		t.Error("LastLoginAt should not be nil after update")
	}
}
