package casdoor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

func TestNewCasdoorTenantManager_ValidConfig(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorTenantManager(config)

	require.NoError(t, err)
	assert.NotNil(t, manager)
	assert.Equal(t, "casdoor", manager.Name())
}

func TestNewCasdoorTenantManager_MissingConfig(t *testing.T) {
	_, err := NewCasdoorTenantManager(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config is required")
}

func TestNewCasdoorTenantManager_MissingFields(t *testing.T) {
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
			_, err := NewCasdoorTenantManager(tt.config)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestCasdoorTenantManager_GetTenant_EmptyID(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorTenantManager(config)
	require.NoError(t, err)

	_, err = manager.GetTenant(context.Background(), "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestCasdoorTenantManager_CreateTenant_NilRequest(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorTenantManager(config)
	require.NoError(t, err)

	_, err = manager.CreateTenant(context.Background(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "create tenant request is required")
}

func TestCasdoorTenantManager_CreateTenant_MissingFields(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorTenantManager(config)
	require.NoError(t, err)

	tests := []struct {
		name        string
		req         *authplugin.CreateTenantRequest
		expectedErr string
	}{
		{
			name:        "missing ID",
			req:         &authplugin.CreateTenantRequest{Name: "Test Tenant"},
			expectedErr: "tenant ID is required",
		},
		{
			name:        "missing Name",
			req:         &authplugin.CreateTenantRequest{ID: "test-tenant"},
			expectedErr: "tenant name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := manager.CreateTenant(context.Background(), tt.req)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestCasdoorTenantManager_UpdateTenant_NilRequest(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorTenantManager(config)
	require.NoError(t, err)

	_, err = manager.UpdateTenant(context.Background(), "test-tenant", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "update tenant request is required")
}

func TestCasdoorTenantManager_UpdateTenant_EmptyID(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorTenantManager(config)
	require.NoError(t, err)

	req := &authplugin.UpdateTenantRequest{}
	_, err = manager.UpdateTenant(context.Background(), "", req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestCasdoorTenantManager_DeleteTenant_EmptyID(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorTenantManager(config)
	require.NoError(t, err)

	err = manager.DeleteTenant(context.Background(), "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestCasdoorTenantManager_SupportsFeature(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorTenantManager(config)
	require.NoError(t, err)

	tests := []struct {
		feature   authplugin.TenantFeature
		supported bool
	}{
		{authplugin.FeatureTenantHierarchy, false},
		{authplugin.FeatureTenantIsolation, true},
		{authplugin.FeatureTenantQuotas, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.feature), func(t *testing.T) {
			result := manager.SupportsFeature(tt.feature)
			assert.Equal(t, tt.supported, result)
		})
	}
}

func TestCasdoorTenantManager_convertTenant_Nil(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	manager, err := NewCasdoorTenantManager(config)
	require.NoError(t, err)

	tenant := manager.convertTenant(nil)
	assert.Nil(t, tenant)
}

// Note: Full integration tests require a running Casdoor instance
// These unit tests focus on validation and edge cases

/*
Integration test example (requires Casdoor instance):

func TestCasdoorTenantManager_CreateTenant_Integration(t *testing.T) {
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

	manager, err := NewCasdoorTenantManager(config)
	require.NoError(t, err)

	// Create tenant
	req := &authplugin.CreateTenantRequest{
		ID:   "test-org-" + uuid.New().String()[:8],
		Name: "Test Organization",
	}

	tenant, err := manager.CreateTenant(context.Background(), req)
	require.NoError(t, err)
	assert.NotNil(t, tenant)
	assert.Equal(t, req.ID, tenant.ID)
	assert.Equal(t, req.Name, tenant.Name)

	// Get tenant
	retrieved, err := manager.GetTenant(context.Background(), tenant.ID)
	require.NoError(t, err)
	assert.Equal(t, tenant.ID, retrieved.ID)

	// Update tenant
	newName := "Updated Organization"
	updateReq := &authplugin.UpdateTenantRequest{
		Name: &newName,
	}
	updated, err := manager.UpdateTenant(context.Background(), tenant.ID, updateReq)
	require.NoError(t, err)
	assert.Equal(t, newName, updated.Name)

	// Delete tenant
	err = manager.DeleteTenant(context.Background(), tenant.ID)
	require.NoError(t, err)

	// Verify deleted
	_, err = manager.GetTenant(context.Background(), tenant.ID)
	assert.ErrorIs(t, err, authplugin.ErrNotFound)
}
*/
