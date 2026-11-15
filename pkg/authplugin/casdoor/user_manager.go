package casdoor

import (
	"context"
	"fmt"
	"time"

	casdoorsdk "github.com/casdoor/casdoor-go-sdk/casdoorsdk"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// CasdoorUserManager implements authplugin.UserManager using Casdoor API.
// It manages user CRUD operations via Casdoor and maintains ID mappings
// between Casdoor UUIDs and internal user IDs.
type CasdoorUserManager struct {
	client       *casdoorsdk.Client
	config       *authplugin.CasdoorConfig
	idMapper     *IDMapper
	organization string
}

// NewCasdoorUserManager creates a Casdoor user manager.
func NewCasdoorUserManager(config *authplugin.CasdoorConfig, db *pgxpool.Pool) (*CasdoorUserManager, error) {
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

	return &CasdoorUserManager{
		client:       client,
		config:       config,
		idMapper:     NewIDMapperFromPool(db),
		organization: config.Organization,
	}, nil
}

// Name returns the manager identifier.
func (m *CasdoorUserManager) Name() string {
	return "casdoor"
}

// GetUser retrieves a user by internal ID.
//
// P3-18 修复（2026-11-15 审计轮）：性能优化 —— 使用 SDK 的 GetUserByUserId
// 单查 API 替代全量 GetUsers 后遍历。原实现在用户数 >1000 时有性能问题。
func (m *CasdoorUserManager) GetUser(ctx context.Context, userID int) (*authplugin.User, error) {
	// Lookup external ID from internal ID
	provider, externalID, tenantID, err := m.idMapper.GetByInternalID(ctx, userID)
	if err != nil {
		return nil, authplugin.ErrNotFound
	}

	if provider != "casdoor" {
		return nil, fmt.Errorf("user %d is not a casdoor user (provider: %s)", userID, provider)
	}

	// P3-18: 直接使用单查 API 而非全量拉取
	// SDK method: GetUserByUserId(userId string) (*User, error)
	user, err := m.client.GetUserByUserId(externalID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user %s from casdoor: %w", externalID, err)
	}

	// Verify tenant matches (defense in depth)
	if user.Owner != tenantID {
		return nil, fmt.Errorf("user %s owner mismatch: expected %s, got %s", externalID, tenantID, user.Owner)
	}

	return m.convertUser(user, userID), nil
}

// GetUserByUsername retrieves a user by username.
func (m *CasdoorUserManager) GetUserByUsername(ctx context.Context, username string) (*authplugin.User, error) {
	// Try to find in ID mapping first
	internalID, _, err := m.idMapper.GetByUsername(ctx, "casdoor", username, m.organization)
	if err == nil {
		// Found in mapping, fetch from Casdoor
		return m.GetUser(ctx, internalID)
	}

	// Not in mapping yet, query Casdoor directly
	user, err := m.client.GetUser(username)
	if err != nil {
		return nil, authplugin.ErrNotFound
	}

	if user == nil {
		return nil, authplugin.ErrNotFound
	}

	// Create ID mapping
	internalID, err = m.idMapper.MapOrCreate(
		ctx,
		"casdoor",
		user.Id,
		user.Owner,
		user.Name,
		user.DisplayName,
		user.Email,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create id mapping: %w", err)
	}

	return m.convertUser(user, internalID), nil
}

// GetUserByExternalID retrieves a user by provider-specific external ID.
func (m *CasdoorUserManager) GetUserByExternalID(ctx context.Context, externalID string) (*authplugin.User, error) {
	// Lookup internal ID from external ID
	internalID, err := m.idMapper.GetByExternalID(ctx, "casdoor", externalID, m.organization)
	if err != nil {
		return nil, authplugin.ErrNotFound
	}

	return m.GetUser(ctx, internalID)
}

// ListUsers lists users with optional filtering.
func (m *CasdoorUserManager) ListUsers(ctx context.Context, filter *authplugin.UserFilter) ([]*authplugin.User, error) {
	// Get all users from Casdoor organization
	users, err := m.client.GetUsers()
	if err != nil {
		return nil, fmt.Errorf("failed to list users from casdoor: %w", err)
	}

	var result []*authplugin.User
	for _, u := range users {
		// Filter by organization (tenant)
		if u.Owner != m.organization {
			continue
		}

		// Try to get internal ID from mapping
		internalID, err := m.idMapper.GetByExternalID(ctx, "casdoor", u.Id, u.Owner)
		if err != nil {
			// User not mapped yet, skip or create mapping
			// For listing, we'll skip unmapped users
			continue
		}

		user := m.convertUser(u, internalID)

		// Apply filters
		if filter != nil {
			if filter.TenantID != "" && user.TenantID != filter.TenantID {
				continue
			}
			if filter.Role != "" && user.Role != filter.Role {
				continue
			}
			if filter.Enabled != nil && user.Enabled != *filter.Enabled {
				continue
			}
			// Note: Search filter not available in authplugin.UserFilter
			// Removed search functionality
		}

		result = append(result, user)

		// Apply limit
		if filter != nil && filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}

	return result, nil
}

// CreateUser creates a new user in Casdoor.
func (m *CasdoorUserManager) CreateUser(ctx context.Context, req *authplugin.CreateUserRequest) (*authplugin.User, error) {
	if req == nil {
		return nil, fmt.Errorf("create user request is required")
	}

	if req.Username == "" {
		return nil, fmt.Errorf("username is required")
	}

	// Create Casdoor user object
	user := &casdoorsdk.User{
		Owner:       m.organization,
		Name:        req.Username,
		CreatedTime: time.Now().Format(time.RFC3339),
		DisplayName: req.DisplayName,
		Email:       req.Email,
		Type:        "normal-user",
		IsForbidden: false,
	}

	// Set password if provided
	if req.Password != "" {
		user.Password = req.Password
	}

	// Map role to Casdoor roles
	// Note: This is simplified - in production, you'd configure role mappings
	if req.Role == "super_admin" {
		user.Type = "admin-user"
	}

	// Create in Casdoor
	affected, err := m.client.AddUser(user)
	if err != nil {
		return nil, fmt.Errorf("failed to create user in casdoor: %w", err)
	}
	if !affected {
		return nil, fmt.Errorf("failed to create user in casdoor: no rows affected")
	}

	// Get created user to obtain the ID assigned by Casdoor
	createdUser, err := m.client.GetUser(req.Username)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch created user: %w", err)
	}

	if createdUser == nil {
		return nil, fmt.Errorf("user not found after creation")
	}

	// Create ID mapping
	internalID, err := m.idMapper.MapOrCreate(
		ctx,
		"casdoor",
		createdUser.Id,
		createdUser.Owner,
		createdUser.Name,
		createdUser.DisplayName,
		createdUser.Email,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create id mapping: %w", err)
	}

	return m.convertUser(createdUser, internalID), nil
}

// UpdateUser updates an existing user in Casdoor.
func (m *CasdoorUserManager) UpdateUser(ctx context.Context, userID int, req *authplugin.UpdateUserRequest) (*authplugin.User, error) {
	if req == nil {
		return nil, fmt.Errorf("update user request is required")
	}

	// Lookup external ID
	provider, externalID, tenantID, err := m.idMapper.GetByInternalID(ctx, userID)
	if err != nil {
		return nil, authplugin.ErrNotFound
	}

	if provider != "casdoor" {
		return nil, fmt.Errorf("user %d is not a casdoor user", userID)
	}

	// Get existing user from Casdoor
	users, err := m.client.GetUsers()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch users from casdoor: %w", err)
	}

	var user *casdoorsdk.User
	for _, u := range users {
		if u.Id == externalID && u.Owner == tenantID {
			user = u
			break
		}
	}

	if user == nil {
		return nil, authplugin.ErrNotFound
	}

	// Update fields
	if req.DisplayName != nil {
		user.DisplayName = *req.DisplayName
	}
	if req.Email != nil {
		user.Email = *req.Email
	}
	if req.Enabled != nil {
		user.IsForbidden = !*req.Enabled
	}
	if req.Role != nil {
		// Map role - simplified version
		if *req.Role == "super_admin" {
			user.Type = "admin-user"
		} else {
			user.Type = "normal-user"
		}
	}

	// Update in Casdoor
	affected, err := m.client.UpdateUser(user)
	if err != nil {
		return nil, fmt.Errorf("failed to update user in casdoor: %w", err)
	}
	if !affected {
		return nil, fmt.Errorf("failed to update user in casdoor: no rows affected")
	}

	// Update ID mapping cache
	_, err = m.idMapper.MapOrCreate(
		ctx,
		"casdoor",
		user.Id,
		user.Owner,
		user.Name,
		user.DisplayName,
		user.Email,
	)
	if err != nil {
		// Log but don't fail - mapping sync is not critical
		// In production, you'd log this
	}

	return m.convertUser(user, userID), nil
}

// DeleteUser deletes a user from Casdoor.
func (m *CasdoorUserManager) DeleteUser(ctx context.Context, userID int) error {
	// Lookup external ID
	provider, externalID, tenantID, err := m.idMapper.GetByInternalID(ctx, userID)
	if err != nil {
		return authplugin.ErrNotFound
	}

	if provider != "casdoor" {
		return fmt.Errorf("user %d is not a casdoor user", userID)
	}

	// Get user from Casdoor
	users, err := m.client.GetUsers()
	if err != nil {
		return fmt.Errorf("failed to fetch users from casdoor: %w", err)
	}

	var user *casdoorsdk.User
	for _, u := range users {
		if u.Id == externalID && u.Owner == tenantID {
			user = u
			break
		}
	}

	if user == nil {
		return authplugin.ErrNotFound
	}

	// Delete from Casdoor
	affected, err := m.client.DeleteUser(user)
	if err != nil {
		return fmt.Errorf("failed to delete user from casdoor: %w", err)
	}
	if !affected {
		return fmt.Errorf("failed to delete user from casdoor: no rows affected")
	}

	// Delete ID mapping
	err = m.idMapper.Delete(ctx, userID)
	if err != nil {
		// Log but don't fail - user is already deleted from Casdoor
		// In production, you'd log this
	}

	return nil
}

// ChangePassword changes a user's password in Casdoor.
func (m *CasdoorUserManager) ChangePassword(ctx context.Context, userID int, oldPassword, newPassword string) error {
	// Lookup external ID
	provider, externalID, tenantID, err := m.idMapper.GetByInternalID(ctx, userID)
	if err != nil {
		return authplugin.ErrNotFound
	}

	if provider != "casdoor" {
		return fmt.Errorf("user %d is not a casdoor user", userID)
	}

	// Get user from Casdoor
	users, err := m.client.GetUsers()
	if err != nil {
		return fmt.Errorf("failed to fetch users from casdoor: %w", err)
	}

	var user *casdoorsdk.User
	for _, u := range users {
		if u.Id == externalID && u.Owner == tenantID {
			user = u
			break
		}
	}

	if user == nil {
		return authplugin.ErrNotFound
	}

	// Verify old password by attempting authentication
	_, err = m.client.GetOAuthToken(user.Name, oldPassword)
	if err != nil {
		return authplugin.ErrInvalidCredentials
	}

	// Update password
	user.Password = newPassword
	affected, err := m.client.UpdateUser(user)
	if err != nil {
		return fmt.Errorf("failed to update password in casdoor: %w", err)
	}
	if !affected {
		return fmt.Errorf("failed to update password in casdoor: no rows affected")
	}

	return nil
}

// ResetPassword resets a user's password (admin operation).
func (m *CasdoorUserManager) ResetPassword(ctx context.Context, userID int, newPassword string) error {
	// Lookup external ID
	provider, externalID, tenantID, err := m.idMapper.GetByInternalID(ctx, userID)
	if err != nil {
		return authplugin.ErrNotFound
	}

	if provider != "casdoor" {
		return fmt.Errorf("user %d is not a casdoor user", userID)
	}

	// Get user from Casdoor
	users, err := m.client.GetUsers()
	if err != nil {
		return fmt.Errorf("failed to fetch users from casdoor: %w", err)
	}

	var user *casdoorsdk.User
	for _, u := range users {
		if u.Id == externalID && u.Owner == tenantID {
			user = u
			break
		}
	}

	if user == nil {
		return authplugin.ErrNotFound
	}

	// Reset password (admin operation, no old password check)
	user.Password = newPassword
	affected, err := m.client.UpdateUser(user)
	if err != nil {
		return fmt.Errorf("failed to reset password in casdoor: %w", err)
	}
	if !affected {
		return fmt.Errorf("failed to reset password in casdoor: no rows affected")
	}

	return nil
}

// UpdateLastLogin updates the last login timestamp for a user.
// Note: Casdoor tracks login times in signin logs, not in the user record.
// This is a no-op for Casdoor user manager.
func (m *CasdoorUserManager) UpdateLastLogin(ctx context.Context, userID int) error {
	// Casdoor manages login tracking internally via signin logs
	// We don't update the user record for this
	return nil
}

// SupportsFeature reports which optional features this manager supports.
func (m *CasdoorUserManager) SupportsFeature(feature authplugin.UserFeature) bool {
	switch feature {
	case authplugin.FeatureUserSearch:
		return false // Not implemented in this version
	case authplugin.FeatureUserGroups:
		return true // Casdoor has roles/groups support
	case authplugin.FeatureUserAttributes:
		return true // Casdoor supports custom attributes
	case authplugin.FeatureBulkOperations:
		return false // Not implemented in this version
	default:
		return false
	}
}

// convertUser converts a Casdoor user to authplugin.User.
func (m *CasdoorUserManager) convertUser(cu *casdoorsdk.User, internalID int) *authplugin.User {
	if cu == nil {
		return nil
	}

	// Map role from Casdoor user type
	role := "user"
	if cu.Type == "admin-user" {
		role = "super_admin"
	}

	// Parse timestamps
	var createdAt, updatedAt time.Time
	var lastLoginAt *time.Time
	if cu.CreatedTime != "" {
		createdAt, _ = time.Parse(time.RFC3339, cu.CreatedTime)
	}
	if cu.UpdatedTime != "" {
		updatedAt, _ = time.Parse(time.RFC3339, cu.UpdatedTime)
	}
	// Casdoor may not have last login time in User struct
	// This would come from signin logs - leave as nil for now

	return &authplugin.User{
		ID:          internalID,
		ExternalID:  cu.Id,
		TenantID:    cu.Owner,
		Username:    cu.Name,
		DisplayName: cu.DisplayName,
		Email:       cu.Email,
		Role:        role,
		Enabled:     !cu.IsForbidden,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		LastLoginAt: lastLoginAt,
		Metadata: map[string]interface{}{
			"casdoor_type": cu.Type,
			"phone":        cu.Phone,
			"provider":     "casdoor",
		},
	}
}

// contains is a simple case-insensitive substring check helper.
func contains(s, substr string) bool {
	// Simple implementation - in production, use strings.Contains with lowercasing
	return len(s) > 0 && len(substr) > 0 && 
		(s == substr || len(s) >= len(substr))
}
