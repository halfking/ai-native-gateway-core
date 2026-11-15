package casdoor

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// Integration tests require a running Casdoor instance and PostgreSQL database.
// Set environment variables:
//   CASDOOR_ENDPOINT, CASDOOR_CLIENT_ID, CASDOOR_CLIENT_SECRET,
//   CASDOOR_ORGANIZATION, CASDOOR_APPLICATION
//   DATABASE_URL (PostgreSQL connection string)
//
// Run with: go test -tags=integration ./pkg/authplugin/casdoor -v

// skipIfNoIntegrationEnv skips the test if integration environment is not configured.
func skipIfNoIntegrationEnv(t *testing.T) *authplugin.CasdoorConfig {
	endpoint := os.Getenv("CASDOOR_ENDPOINT")
	if endpoint == "" {
		t.Skip("CASDOOR_ENDPOINT not set, skipping integration test")
	}

	config := &authplugin.CasdoorConfig{
		Endpoint:     endpoint,
		ClientID:     os.Getenv("CASDOOR_CLIENT_ID"),
		ClientSecret: os.Getenv("CASDOOR_CLIENT_SECRET"),
		Organization: os.Getenv("CASDOOR_ORGANIZATION"),
		Application:  os.Getenv("CASDOOR_APPLICATION"),
		Certificate:  os.Getenv("CASDOOR_CERTIFICATE"),
	}

	if config.ClientID == "" || config.ClientSecret == "" ||
		config.Organization == "" || config.Application == "" {
		t.Skip("Casdoor configuration incomplete, skipping integration test")
	}

	return config
}

// setupTestDB creates a test database connection.
func setupTestDB(t *testing.T) *pgxpool.Pool {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set, skipping integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err, "failed to connect to database")

	// Verify connection
	err = pool.Ping(ctx)
	require.NoError(t, err, "failed to ping database")

	return pool
}

// TestCasdoorAuthProvider_Integration tests authentication flow end-to-end.
func TestCasdoorAuthProvider_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	config := skipIfNoIntegrationEnv(t)
	db := setupTestDB(t)
	defer db.Close()

	// Create auth provider
	provider, err := NewCasdoorAuthProvider(config, db)
	require.NoError(t, err, "failed to create auth provider")
	assert.Equal(t, "casdoor", provider.Name())

	ctx := context.Background()

	// Test authentication with valid credentials
	// Note: This requires a test user in Casdoor
	testUsername := os.Getenv("CASDOOR_TEST_USERNAME")
	testPassword := os.Getenv("CASDOOR_TEST_PASSWORD")
	if testUsername == "" || testPassword == "" {
		t.Skip("CASDOOR_TEST_USERNAME or CASDOOR_TEST_PASSWORD not set")
	}

	t.Run("Authenticate", func(t *testing.T) {
		token, err := provider.Authenticate(ctx, map[string]string{
			"username": testUsername,
			"password": testPassword,
		})
		require.NoError(t, err, "authentication failed")
		assert.NotEmpty(t, token.AccessToken)
		assert.NotEmpty(t, token.RefreshToken)
		assert.Greater(t, token.ExpiresIn, int64(0))

		// Validate the token
		userID, err := provider.ValidateToken(ctx, token.AccessToken)
		require.NoError(t, err, "token validation failed")
		assert.Greater(t, userID, 0)

		// Refresh the token
		newToken, err := provider.RefreshToken(ctx, token.RefreshToken)
		require.NoError(t, err, "token refresh failed")
		assert.NotEmpty(t, newToken.AccessToken)
		assert.NotEqual(t, token.AccessToken, newToken.AccessToken)
	})

	t.Run("Authenticate_InvalidCredentials", func(t *testing.T) {
		_, err := provider.Authenticate(ctx, map[string]string{
			"username": testUsername,
			"password": "wrong-password",
		})
		assert.Error(t, err)
		assert.ErrorIs(t, err, authplugin.ErrInvalidCredentials)
	})
}

// TestCasdoorUserManager_Integration tests user management end-to-end.
func TestCasdoorUserManager_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	config := skipIfNoIntegrationEnv(t)
	db := setupTestDB(t)
	defer db.Close()

	// Create user manager
	manager, err := NewCasdoorUserManager(config, db)
	require.NoError(t, err, "failed to create user manager")
	assert.Equal(t, "casdoor", manager.Name())

	ctx := context.Background()

	// Generate unique username for test
	uniqueUsername := "test-user-" + time.Now().Format("20060102150405")

	var createdUser *authplugin.User

	t.Run("CreateUser", func(t *testing.T) {
		req := &authplugin.CreateUserRequest{
			Username:    uniqueUsername,
			Password:    "Test@12345",
			DisplayName: "Test User",
			Email:       uniqueUsername + "@example.com",
			Role:        "user",
		}

		user, err := manager.CreateUser(ctx, req)
		require.NoError(t, err, "user creation failed")
		assert.NotNil(t, user)
		assert.Greater(t, user.ID, 0)
		assert.NotEmpty(t, user.ExternalID)
		assert.Equal(t, req.Username, user.Username)
		assert.Equal(t, req.DisplayName, user.DisplayName)
		assert.Equal(t, req.Email, user.Email)
		assert.Equal(t, req.Role, user.Role)
		assert.True(t, user.Enabled)

		createdUser = user
	})

	t.Run("GetUser", func(t *testing.T) {
		require.NotNil(t, createdUser, "user must be created first")

		user, err := manager.GetUser(ctx, createdUser.ID)
		require.NoError(t, err, "get user failed")
		assert.Equal(t, createdUser.ID, user.ID)
		assert.Equal(t, createdUser.Username, user.Username)
	})

	t.Run("GetUserByUsername", func(t *testing.T) {
		require.NotNil(t, createdUser, "user must be created first")

		user, err := manager.GetUserByUsername(ctx, createdUser.Username)
		require.NoError(t, err, "get user by username failed")
		assert.Equal(t, createdUser.ID, user.ID)
		assert.Equal(t, createdUser.Username, user.Username)
	})

	t.Run("GetUserByExternalID", func(t *testing.T) {
		require.NotNil(t, createdUser, "user must be created first")

		user, err := manager.GetUserByExternalID(ctx, createdUser.ExternalID)
		require.NoError(t, err, "get user by external ID failed")
		assert.Equal(t, createdUser.ID, user.ID)
		assert.Equal(t, createdUser.ExternalID, user.ExternalID)
	})

	t.Run("UpdateUser", func(t *testing.T) {
		require.NotNil(t, createdUser, "user must be created first")

		newDisplayName := "Updated Test User"
		newEmail := "updated-" + createdUser.Email
		req := &authplugin.UpdateUserRequest{
			DisplayName: &newDisplayName,
			Email:       &newEmail,
		}

		user, err := manager.UpdateUser(ctx, createdUser.ID, req)
		require.NoError(t, err, "update user failed")
		assert.Equal(t, newDisplayName, user.DisplayName)
		assert.Equal(t, newEmail, user.Email)
	})

	t.Run("ListUsers", func(t *testing.T) {
		users, err := manager.ListUsers(ctx, &authplugin.UserFilter{
			Limit: 10,
		})
		require.NoError(t, err, "list users failed")
		assert.Greater(t, len(users), 0)
	})

	t.Run("DeleteUser", func(t *testing.T) {
		require.NotNil(t, createdUser, "user must be created first")

		err := manager.DeleteUser(ctx, createdUser.ID)
		require.NoError(t, err, "delete user failed")

		// Verify user is deleted
		_, err = manager.GetUser(ctx, createdUser.ID)
		assert.Error(t, err)
		assert.ErrorIs(t, err, authplugin.ErrNotFound)
	})
}

// TestCasdoorTenantManager_Integration tests tenant management end-to-end.
func TestCasdoorTenantManager_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	config := skipIfNoIntegrationEnv(t)

	// Create tenant manager
	manager, err := NewCasdoorTenantManager(config)
	require.NoError(t, err, "failed to create tenant manager")
	assert.Equal(t, "casdoor", manager.Name())

	ctx := context.Background()

	// Generate unique tenant ID for test
	uniqueTenantID := "test-org-" + time.Now().Format("20060102150405")

	var createdTenant *authplugin.Tenant

	t.Run("CreateTenant", func(t *testing.T) {
		req := &authplugin.CreateTenantRequest{
			ID:   uniqueTenantID,
			Name: "Test Organization",
		}

		tenant, err := manager.CreateTenant(ctx, req)
		require.NoError(t, err, "tenant creation failed")
		assert.NotNil(t, tenant)
		assert.Equal(t, req.ID, tenant.ID)
		assert.Equal(t, req.Name, tenant.DisplayName)
		assert.Equal(t, "active", tenant.Status)

		createdTenant = tenant
	})

	t.Run("GetTenant", func(t *testing.T) {
		require.NotNil(t, createdTenant, "tenant must be created first")

		tenant, err := manager.GetTenant(ctx, createdTenant.ID)
		require.NoError(t, err, "get tenant failed")
		assert.Equal(t, createdTenant.ID, tenant.ID)
		assert.Equal(t, createdTenant.DisplayName, tenant.DisplayName)
	})

	t.Run("UpdateTenant", func(t *testing.T) {
		require.NotNil(t, createdTenant, "tenant must be created first")

		newName := "Updated Test Organization"
		req := &authplugin.UpdateTenantRequest{
			Name: &newName,
		}

		tenant, err := manager.UpdateTenant(ctx, createdTenant.ID, req)
		require.NoError(t, err, "update tenant failed")
		assert.Equal(t, newName, tenant.DisplayName)
	})

	t.Run("ListTenants", func(t *testing.T) {
		tenants, err := manager.ListTenants(ctx, &authplugin.TenantFilter{
			Limit: 10,
		})
		require.NoError(t, err, "list tenants failed")
		assert.Greater(t, len(tenants), 0)
	})

	t.Run("DeleteTenant", func(t *testing.T) {
		require.NotNil(t, createdTenant, "tenant must be created first")

		err := manager.DeleteTenant(ctx, createdTenant.ID)
		require.NoError(t, err, "delete tenant failed")

		// Verify tenant is deleted
		_, err = manager.GetTenant(ctx, createdTenant.ID)
		assert.Error(t, err)
		assert.ErrorIs(t, err, authplugin.ErrNotFound)
	})
}

// TestIDMapper_Integration tests ID mapping end-to-end.
func TestIDMapper_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := setupTestDB(t)
	defer db.Close()

	mapper := NewIDMapperFromPool(db)
	ctx := context.Background()

	// Generate unique test data
	uniqueID := time.Now().Format("20060102150405")
	externalID := "ext-" + uniqueID
	username := "user-" + uniqueID

	var internalID int

	t.Run("MapOrCreate_NewMapping", func(t *testing.T) {
		id, err := mapper.MapOrCreate(
			ctx,
			"casdoor",
			externalID,
			"test-org",
			username,
			"Test User",
			username+"@example.com",
		)
		require.NoError(t, err)
		assert.Greater(t, id, 0)
		internalID = id
	})

	t.Run("MapOrCreate_ExistingMapping", func(t *testing.T) {
		id, err := mapper.MapOrCreate(
			ctx,
			"casdoor",
			externalID,
			"test-org",
			username,
			"Test User Updated",
			username+"@example.com",
		)
		require.NoError(t, err)
		assert.Equal(t, internalID, id, "should return same internal ID")
	})

	t.Run("GetByInternalID", func(t *testing.T) {
		provider, extID, tenantID, err := mapper.GetByInternalID(ctx, internalID)
		require.NoError(t, err)
		assert.Equal(t, "casdoor", provider)
		assert.Equal(t, externalID, extID)
		assert.Equal(t, "test-org", tenantID)
	})

	t.Run("GetByExternalID", func(t *testing.T) {
		id, err := mapper.GetByExternalID(ctx, "casdoor", externalID, "test-org")
		require.NoError(t, err)
		assert.Equal(t, internalID, id)
	})

	t.Run("GetByUsername", func(t *testing.T) {
		id, extID, err := mapper.GetByUsername(ctx, "casdoor", username, "test-org")
		require.NoError(t, err)
		assert.Equal(t, internalID, id)
		assert.Equal(t, externalID, extID)
	})

	t.Run("ListByProvider", func(t *testing.T) {
		mappings, err := mapper.ListByProvider(ctx, "casdoor", 10, 0)
		require.NoError(t, err)
		assert.Greater(t, len(mappings), 0)

		// Find our test mapping
		found := false
		for _, m := range mappings {
			if m.InternalID == internalID {
				found = true
				assert.Equal(t, externalID, m.ExternalID)
				assert.Equal(t, username, m.Username)
				break
			}
		}
		assert.True(t, found, "test mapping should be in results")
	})

	t.Run("Delete", func(t *testing.T) {
		err := mapper.Delete(ctx, internalID)
		require.NoError(t, err)

		// Verify deleted
		_, _, _, err = mapper.GetByInternalID(ctx, internalID)
		assert.Error(t, err)
	})
}

// TestFullAuthFlow_Integration tests complete authentication flow with ID mapping.
func TestFullAuthFlow_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	config := skipIfNoIntegrationEnv(t)
	db := setupTestDB(t)
	defer db.Close()

	testUsername := os.Getenv("CASDOOR_TEST_USERNAME")
	testPassword := os.Getenv("CASDOOR_TEST_PASSWORD")
	if testUsername == "" || testPassword == "" {
		t.Skip("CASDOOR_TEST_USERNAME or CASDOOR_TEST_PASSWORD not set")
	}

	// Create auth provider
	authProvider, err := NewCasdoorAuthProvider(config, db)
	require.NoError(t, err)

	// Create user manager
	userManager, err := NewCasdoorUserManager(config, db)
	require.NoError(t, err)

	ctx := context.Background()

	// Step 1: Authenticate
	token, err := authProvider.Authenticate(ctx, map[string]string{
		"username": testUsername,
		"password": testPassword,
	})
	require.NoError(t, err, "authentication failed")
	assert.NotEmpty(t, token.AccessToken)

	// Step 2: Validate token and get internal user ID
	userID, err := authProvider.ValidateToken(ctx, token.AccessToken)
	require.NoError(t, err, "token validation failed")
	assert.Greater(t, userID, 0)

	// Step 3: Fetch user details
	user, err := userManager.GetUser(ctx, userID)
	require.NoError(t, err, "get user failed")
	assert.Equal(t, userID, user.ID)
	assert.NotEmpty(t, user.ExternalID)
	assert.Equal(t, testUsername, user.Username)

	// Step 4: Verify ID mapping was created
	mapper := NewIDMapperFromPool(db)
	provider, externalID, tenantID, err := mapper.GetByInternalID(ctx, userID)
	require.NoError(t, err, "ID mapping not found")
	assert.Equal(t, "casdoor", provider)
	assert.Equal(t, user.ExternalID, externalID)
	assert.NotEmpty(t, tenantID)

	t.Logf("Full auth flow successful: user=%s, internal_id=%d, external_id=%s",
		user.Username, user.ID, user.ExternalID)
}
