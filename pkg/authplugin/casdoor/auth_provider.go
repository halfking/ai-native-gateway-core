package casdoor

import (
	"context"
	"fmt"
	"time"

	casdoorsdk "github.com/casdoor/casdoor-go-sdk/casdoorsdk"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// CasdoorAuthProvider implements authplugin.AuthProvider using Casdoor.
// It handles authentication via Casdoor OAuth2/OIDC and maps external
// Casdoor user IDs to internal user IDs.
type CasdoorAuthProvider struct {
	client       *casdoorsdk.Client
	config       *authplugin.CasdoorConfig
	idMapper     *IDMapper
	organization string
	application  string
}

// NewCasdoorAuthProvider creates a Casdoor authentication provider.
// Requires valid Casdoor configuration with endpoint, clientID, and clientSecret.
// The database pool is used for ID mapping operations.
func NewCasdoorAuthProvider(config *authplugin.CasdoorConfig, db *pgxpool.Pool) (*CasdoorAuthProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("casdoor config is required")
	}

	// Validate required fields
	if config.Endpoint == "" {
		return nil, fmt.Errorf("casdoor endpoint is required")
	}
	if config.ClientID == "" {
		return nil, fmt.Errorf("casdoor client_id is required")
	}
	if config.ClientSecret == "" {
		return nil, fmt.Errorf("casdoor client_secret is required")
	}
	if config.Organization == "" {
		return nil, fmt.Errorf("casdoor organization is required")
	}
	if config.Application == "" {
		return nil, fmt.Errorf("casdoor application is required")
	}

	// Initialize Casdoor SDK client
	authConfig := &casdoorsdk.AuthConfig{
		Endpoint:         config.Endpoint,
		ClientId:         config.ClientID,
		ClientSecret:     config.ClientSecret,
		Certificate:      config.Certificate,
		OrganizationName: config.Organization,
		ApplicationName:  config.Application,
	}

	client := casdoorsdk.NewClientWithConf(authConfig)

	return &CasdoorAuthProvider{
		client:       client,
		config:       config,
		idMapper:     NewIDMapperFromPool(db),
		organization: config.Organization,
		application:  config.Application,
	}, nil
}

// Name returns the provider identifier.
func (p *CasdoorAuthProvider) Name() string {
	return "casdoor"
}

// Authenticate verifies username+password against Casdoor and returns auth result.
// This uses OAuth2 password grant flow to obtain an access token from Casdoor.
//
// Flow:
// 1. Call Casdoor OAuth2 password grant with username/password
// 2. Parse JWT token to extract user claims
// 3. Fetch full user details from Casdoor API
// 4. Check if user is enabled/disabled
// 5. Map Casdoor UUID to internal user ID (or create new mapping)
// 6. Return authentication result with tokens
func (p *CasdoorAuthProvider) Authenticate(ctx context.Context, req *authplugin.AuthRequest) (*authplugin.AuthResult, error) {
	if req == nil {
		return nil, fmt.Errorf("auth request is required")
	}
	if req.Username == "" || req.Password == "" {
		return nil, authplugin.ErrInvalidCredentials
	}

	// Call Casdoor OAuth2 password grant
	// This returns an access token if credentials are valid
	token, err := p.client.GetOAuthToken(req.Username, req.Password)
	if err != nil {
		// Casdoor returns error for invalid credentials
		return nil, authplugin.ErrInvalidCredentials
	}

	if token == nil || token.AccessToken == "" {
		return nil, authplugin.ErrInvalidCredentials
	}

	// Parse JWT to extract claims
	claims, err := p.client.ParseJwtToken(token.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("parse casdoor token: %w", err)
	}

	// Get full user details from Casdoor API
	// This provides more information than what's in the JWT
	user, err := p.client.GetUser(claims.Name)
	if err != nil {
		return nil, fmt.Errorf("get casdoor user: %w", err)
	}

	if user == nil {
		return nil, authplugin.ErrNotFound
	}

	// Check if user is disabled in Casdoor
	if user.IsForbidden {
		return nil, authplugin.ErrUserDisabled
	}

	// Map Casdoor user ID to internal ID (or create new mapping)
	internalID, err := p.idMapper.MapOrCreate(
		ctx,
		"casdoor",
		user.Id,         // Casdoor UUID
		user.Owner,      // Organization = tenant
		user.Name,       // Username
		user.DisplayName,
		user.Email,
	)
	if err != nil {
		return nil, fmt.Errorf("id mapping failed: %w", err)
	}

	// Map Casdoor role to internal role
	role := p.mapRole(user)

	// Calculate token expiration time
	expiresAt := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)

	return &authplugin.AuthResult{
		Success:      true,
		UserID:       internalID,
		ExternalID:   user.Id,
		Username:     user.Name,
		TenantID:     user.Owner,
		Role:         role,
		DisplayName:  user.DisplayName,
		Email:        user.Email,
		Enabled:      !user.IsForbidden,
		Token:        token.AccessToken,
		TokenType:    "Bearer",
		ExpiresAt:    expiresAt,
		RefreshToken: token.RefreshToken,
		Metadata: map[string]interface{}{
			"casdoor_id":   user.Id,
			"organization": user.Owner,
			"phone":        user.Phone, // Store phone in metadata
			"casdoor_roles": func() []string {
				roles := make([]string, len(user.Roles))
				for i, r := range user.Roles {
					roles[i] = r.Name
				}
				return roles
			}(),
			"provider": "casdoor",
		},
	}, nil
}

// ValidateToken verifies a Casdoor JWT token and returns claims.
// This parses and validates the JWT signature using Casdoor's public key.
func (p *CasdoorAuthProvider) ValidateToken(ctx context.Context, token string) (*authplugin.TokenClaims, error) {
	if token == "" {
		return nil, authplugin.ErrInvalidToken
	}

	// Parse and validate JWT token
	claims, err := p.client.ParseJwtToken(token)
	if err != nil {
		return nil, authplugin.ErrInvalidToken
	}

	if claims == nil {
		return nil, authplugin.ErrInvalidToken
	}

	// Map external ID to internal ID
	// If user is not mapped yet, internal ID will be 0
	internalID, err := p.idMapper.GetByExternalID(ctx, "casdoor", claims.Id, claims.Owner)
	if err != nil {
		// User not mapped yet - this is OK for pure Casdoor tokens
		// They will get mapped on first login via Authenticate()
		internalID = 0
	}

	// Handle audience - take first element if it's a slice
	var audience string
	if len(claims.Audience) > 0 {
		audience = claims.Audience[0]
	}

	// Handle timestamps - NumericDate can be nil
	var issuedAt, expiresAt time.Time
	if claims.IssuedAt != nil {
		issuedAt = claims.IssuedAt.Time
	}
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time
	}

	return &authplugin.TokenClaims{
		UserID:     internalID,
		ExternalID: claims.Id,
		Username:   claims.Name,
		TenantID:   claims.Owner,
		Role:       p.mapRoleFromClaims(claims),
		Issuer:     claims.Issuer,
		Audience:   audience,
		IssuedAt:   issuedAt,
		ExpiresAt:  expiresAt,
	}, nil
}

// RefreshToken exchanges a refresh token for a new access token.
// This uses Casdoor's refresh token endpoint to obtain a fresh access token.
func (p *CasdoorAuthProvider) RefreshToken(ctx context.Context, refreshToken string) (*authplugin.AuthResult, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is required")
	}

	// Exchange refresh token for new access token
	token, err := p.client.RefreshOAuthToken(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("refresh casdoor token: %w", err)
	}

	if token == nil || token.AccessToken == "" {
		return nil, fmt.Errorf("invalid token response from casdoor")
	}

	// Parse new token to get user info
	claims, err := p.client.ParseJwtToken(token.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("parse refreshed token: %w", err)
	}

	// Map to internal ID
	internalID, err := p.idMapper.GetByExternalID(ctx, "casdoor", claims.Id, claims.Owner)
	if err != nil {
		return nil, fmt.Errorf("user not mapped: %w", err)
	}

	expiresAt := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)

	return &authplugin.AuthResult{
		Success:      true,
		UserID:       internalID,
		ExternalID:   claims.Id,
		Username:     claims.Name,
		TenantID:     claims.Owner,
		Role:         p.mapRoleFromClaims(claims),
		Token:        token.AccessToken,
		TokenType:    "Bearer",
		ExpiresAt:    expiresAt,
		RefreshToken: token.RefreshToken,
	}, nil
}

// Logout revokes a Casdoor session (if supported).
// Note: Casdoor JWT tokens are stateless and cannot be revoked server-side.
// This is a no-op for Casdoor provider.
func (p *CasdoorAuthProvider) Logout(ctx context.Context, token string) error {
	// Casdoor SDK does not provide token revocation API
	// JWT tokens are stateless and expire naturally
	return nil
}

// SupportsFeature reports which optional features this provider supports.
func (p *CasdoorAuthProvider) SupportsFeature(feature authplugin.AuthFeature) bool {
	switch feature {
	case authplugin.FeaturePasswordAuth:
		return true // OAuth2 password grant
	case authplugin.FeatureTokenValidation:
		return true // JWT validation
	case authplugin.FeatureRefreshToken:
		return true // OAuth2 refresh token
	case authplugin.FeatureSessionRevocation:
		return false // JWT tokens cannot be revoked
	case authplugin.FeatureMFA:
		return false // Not implemented yet (Casdoor supports it though)
	default:
		return false
	}
}

// mapRole converts Casdoor roles to internal role enum.
// Priority: super_admin > tenant_admin > user
func (p *CasdoorAuthProvider) mapRole(user *casdoorsdk.User) string {
	if user == nil || user.Roles == nil {
		return "user"
	}

	// Check roles in priority order
	for _, role := range user.Roles {
		if role == nil {
			continue
		}
		switch role.Name {
		case "admin", "super_admin", "super-admin":
			return "super_admin"
		case "tenant_admin", "org_admin", "tenant-admin", "org-admin":
			return "tenant_admin"
		}
	}

	return "user" // Default role
}

// mapRoleFromClaims extracts role from JWT claims.
// Used when we only have the JWT token without full user object.
func (p *CasdoorAuthProvider) mapRoleFromClaims(claims *casdoorsdk.Claims) string {
	if claims == nil {
		return "user"
	}

	// Note: claims.Tag is a string field in casdoor-go-sdk, not a map
	// We'll need to parse it or rely on other claim fields
	// For now, return default role - this can be enhanced based on Casdoor's claim structure
	return "user"
}
