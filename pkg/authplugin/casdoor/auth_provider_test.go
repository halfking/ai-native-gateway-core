package casdoor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

func TestNewCasdoorAuthProvider_ValidConfig(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	provider, err := NewCasdoorAuthProvider(config, nil)

	require.NoError(t, err)
	assert.NotNil(t, provider)
	assert.Equal(t, "casdoor", provider.Name())
	assert.Equal(t, "test-org", provider.organization)
	assert.Equal(t, "test-app", provider.application)
}

func TestNewCasdoorAuthProvider_MissingConfig(t *testing.T) {
	_, err := NewCasdoorAuthProvider(nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config is required")
}

func TestNewCasdoorAuthProvider_MissingFields(t *testing.T) {
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
			_, err := NewCasdoorAuthProvider(tt.config, nil)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestCasdoorAuthProvider_Authenticate_NilRequest(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	provider, err := NewCasdoorAuthProvider(config, nil)
	require.NoError(t, err)

	_, err = provider.Authenticate(context.Background(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "auth request is required")
}

func TestCasdoorAuthProvider_Authenticate_MissingCredentials(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	provider, err := NewCasdoorAuthProvider(config, nil)
	require.NoError(t, err)

	tests := []struct {
		name     string
		username string
		password string
	}{
		{"empty username", "", "password"},
		{"empty password", "username", ""},
		{"both empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &authplugin.AuthRequest{
				Username: tt.username,
				Password: tt.password,
			}

			_, err := provider.Authenticate(context.Background(), req)
			assert.ErrorIs(t, err, authplugin.ErrInvalidCredentials)
		})
	}
}

func TestCasdoorAuthProvider_ValidateToken_EmptyToken(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	provider, err := NewCasdoorAuthProvider(config, nil)
	require.NoError(t, err)

	_, err = provider.ValidateToken(context.Background(), "")
	assert.ErrorIs(t, err, authplugin.ErrInvalidToken)
}

func TestCasdoorAuthProvider_RefreshToken_EmptyToken(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	provider, err := NewCasdoorAuthProvider(config, nil)
	require.NoError(t, err)

	_, err = provider.RefreshToken(context.Background(), "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "refresh token is required")
}

func TestCasdoorAuthProvider_Logout(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	provider, err := NewCasdoorAuthProvider(config, nil)
	require.NoError(t, err)

	// Logout should be no-op for JWT tokens
	err = provider.Logout(context.Background(), "some-token")
	assert.NoError(t, err)
}

func TestCasdoorAuthProvider_SupportsFeature(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	provider, err := NewCasdoorAuthProvider(config, nil)
	require.NoError(t, err)

	tests := []struct {
		feature   authplugin.AuthFeature
		supported bool
	}{
		{authplugin.FeaturePasswordAuth, true},
		{authplugin.FeatureTokenValidation, true},
		{authplugin.FeatureRefreshToken, true},
		{authplugin.FeatureSessionRevocation, false},
		{authplugin.FeatureMFA, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.feature), func(t *testing.T) {
			result := provider.SupportsFeature(tt.feature)
			assert.Equal(t, tt.supported, result)
		})
	}
}

func TestCasdoorAuthProvider_mapRole(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	provider, err := NewCasdoorAuthProvider(config, nil)
	require.NoError(t, err)

	// Note: We can't easily test this without importing casdoorsdk types
	// This would require creating mock casdoorsdk.User objects
	// For now, we test the nil case
	role := provider.mapRole(nil)
	assert.Equal(t, "user", role)
}

func TestCasdoorAuthProvider_mapRoleFromClaims(t *testing.T) {
	config := &authplugin.CasdoorConfig{
		Endpoint:     "https://door.casdoor.com",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Organization: "test-org",
		Application:  "test-app",
	}

	provider, err := NewCasdoorAuthProvider(config, nil)
	require.NoError(t, err)

	// Test nil claims
	role := provider.mapRoleFromClaims(nil)
	assert.Equal(t, "user", role)
}

// Note: Integration tests would go here
// They require a running Casdoor instance with test credentials
// Run with: go test -tags=integration ./pkg/authplugin/casdoor
