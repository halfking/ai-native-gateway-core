package casdoor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

func TestNewCasdoorUserManager_ValidConfig(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorUserManager(config, nil)

	require.NoError(t, err)
	assert.NotNil(t, manager)
	assert.Equal(t, "casdoor", manager.Name())
	assert.Equal(t, "test-org", manager.organization)
}

func TestNewCasdoorUserManager_MissingConfig(t *testing.T) {
	_, err := NewCasdoorUserManager(nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config is required")
}

func TestNewCasdoorUserManager_MissingFields(t *testing.T) {
	tests := []struct {
		name        string
		config      *authplugin.CasdoorConfig
		expectedErr string
	}{
		{
			name:        "missing endpoint",
			config:      &authplugin.CasdoorConfig{ClientID: "id", ClientSecret: "secret", Organization: "org", Application: "app"},
			expectedErr: "endpoint is required",
		},
		{
			name:        "missing client_id",
			config:      &authplugin.CasdoorConfig{Endpoint: "https://test.com", ClientSecret: "secret", Organization: "org", Application: "app"},
			expectedErr: "client_id is required",
		},
		{
			name:        "missing client_secret",
			config:      &authplugin.CasdoorConfig{Endpoint: "https://test.com", ClientID: "id", Organization: "org", Application: "app"},
			expectedErr: "client_secret is required",
		},
		{
			name:        "missing organization",
			config:      &authplugin.CasdoorConfig{Endpoint: "https://test.com", ClientID: "id", ClientSecret: "secret", Application: "app"},
			expectedErr: "organization is required",
		},
		{
			name:        "missing application",
			config:      &authplugin.CasdoorConfig{Endpoint: "https://test.com", ClientID: "id", ClientSecret: "secret", Organization: "org"},
			expectedErr: "application is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCasdoorUserManager(tt.config, nil)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestCasdoorUserManager_CreateUser_NilRequest(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorUserManager(config, nil)
	require.NoError(t, err)

	_, err = manager.CreateUser(context.Background(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "create user request is required")
}

func TestCasdoorUserManager_CreateUser_MissingUsername(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorUserManager(config, nil)
	require.NoError(t, err)

	req := &authplugin.CreateUserRequest{
		Username: "",
		Password: "password",
	}

	_, err = manager.CreateUser(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "username is required")
}

func TestCasdoorUserManager_UpdateUser_NilRequest(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorUserManager(config, nil)
	require.NoError(t, err)

	_, err = manager.UpdateUser(context.Background(), 1, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "update user request is required")
}

func TestCasdoorUserManager_SupportsFeature(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorUserManager(config, nil)
	require.NoError(t, err)

	tests := []struct {
		feature   authplugin.UserFeature
		supported bool
	}{
		{authplugin.FeatureUserSearch, false},
		{authplugin.FeatureUserGroups, true},
		{authplugin.FeatureUserAttributes, true},
		{authplugin.FeatureBulkOperations, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.feature), func(t *testing.T) {
			result := manager.SupportsFeature(tt.feature)
			assert.Equal(t, tt.supported, result)
		})
	}
}

func TestCasdoorUserManager_convertUser_Nil(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorUserManager(config, nil)
	require.NoError(t, err)

	user := manager.convertUser(nil, 0)
	assert.Nil(t, user)
}

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{"empty strings", "", "", false},
		{"empty s", "", "test", false},
		{"empty substr", "test", "", false},
		{"exact match", "test", "test", true},
		{"different strings", "test", "other", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contains(tt.s, tt.substr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Note: Full integration tests require a running Casdoor instance
// These unit tests focus on validation and edge cases

/*
Integration test example (requires Casdoor instance):

func TestCasdoorUserManager_CreateUser_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	config := &authplugin.CasdoorConfig{
		Endpoint:     os.Getenv("CASDOOR_ENDPOINT"),
		ClientID:     os.Getenv("CASDOOR_CLIENT_ID"),
		ClientSecret: os.Getenv("CASDOOR_CLIENT_SECRET"),
		Organization: os.Getenv("CASDOOR_ORGANIZATION"),
		Application:  os.Getenv("CASDOOR_APPLICATION"),
	}

	db := setupTestDB(t)
	defer db.Close()

	manager, err := NewCasdoorUserManager(config, db)
	require.NoError(t, err)

	// Create user
	req := &authplugin.CreateUserRequest{
		Username:    "test-user-" + uuid.New().String(),
		Password:    "Test@1234",
		DisplayName: "Test User",
		Email:       "test@example.com",
		Role:        "user",
	}

	user, err := manager.CreateUser(context.Background(), req)
	require.NoError(t, err)
	assert.NotNil(t, user)
	assert.Greater(t, user.ID, 0)
	assert.NotEmpty(t, user.ExternalID)

	// Get user
	retrieved, err := manager.GetUser(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.Username, retrieved.Username)

	// Update user
	newDisplayName := "Updated Name"
	updateReq := &authplugin.UpdateUserRequest{
		DisplayName: &newDisplayName,
	}
	updated, err := manager.UpdateUser(context.Background(), user.ID, updateReq)
	require.NoError(t, err)
	assert.Equal(t, newDisplayName, updated.DisplayName)

	// Delete user
	err = manager.DeleteUser(context.Background(), user.ID)
	require.NoError(t, err)

	// Verify deleted
	_, err = manager.GetUser(context.Background(), user.ID)
	assert.ErrorIs(t, err, authplugin.ErrNotFound)
}
*/
