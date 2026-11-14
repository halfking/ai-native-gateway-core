// Package authplugin provides pluggable authentication interfaces for llm-gateway-go.
//
// This package enables switching between different authentication providers (native PostgreSQL,
// Casdoor, LDAP, etc.) without changing application code. Each provider implements a set of
// interfaces: AuthProvider, UserManager, TenantManager, and ApprovalProvider.
//
// Design principles:
//   - Interface-based abstraction
//   - Backward compatibility with existing native implementation
//   - Configuration-driven provider selection
//   - Zero breaking changes to existing code paths
//
// Example usage:
//
//	registry := authplugin.NewRegistry(config)
//	authProvider, _ := registry.GetAuthProvider()
//	result, err := authProvider.Authenticate(ctx, &authplugin.AuthRequest{
//	    Username: "admin",
//	    Password: "password",
//	})
package authplugin

import (
	"context"
	"errors"
	"time"
)

// ============================================================================
// AuthProvider Interface
// ============================================================================

// AuthProvider handles authentication operations (login, token validation, logout).
// Implementations: NativeAuthProvider (PostgreSQL), CasdoorAuthProvider, LDAPAuthProvider.
type AuthProvider interface {
	// Name returns the provider identifier (e.g., "native", "casdoor", "ldap").
	Name() string

	// Authenticate verifies username/password and returns auth result with JWT token.
	// Returns ErrInvalidCredentials if authentication fails.
	// Returns ErrUserDisabled if user account is disabled.
	// Returns ErrTenantDisabled if tenant is disabled.
	Authenticate(ctx context.Context, req *AuthRequest) (*AuthResult, error)

	// ValidateToken verifies a JWT token and returns claims.
	// Returns ErrInvalidToken if token is invalid or expired.
	ValidateToken(ctx context.Context, token string) (*TokenClaims, error)

	// RefreshToken generates a new token from a valid refresh token.
	// Returns error if provider doesn't support refresh tokens.
	RefreshToken(ctx context.Context, refreshToken string) (*AuthResult, error)

	// Logout invalidates a token (if provider supports token revocation).
	// For stateless JWT providers, this may be a no-op.
	Logout(ctx context.Context, token string) error

	// SupportsFeature checks if provider supports an optional feature.
	SupportsFeature(feature AuthFeature) bool
}

// AuthRequest contains authentication request parameters.
type AuthRequest struct {
	Username    string // Required: username or email
	Password    string // Required: plaintext password
	TenantID    string // Optional: tenant scope (provider may auto-resolve)
	RememberMe  bool   // Optional: whether to issue long-lived session
	CaptchaID   string // Optional: captcha verification ID
	CaptchaCode string // Optional: user-provided captcha answer
	IPAddress   string // Optional: client IP for rate limiting/audit
	UserAgent   string // Optional: client user agent for audit
}

// AuthResult contains authentication outcome and token information.
type AuthResult struct {
	Success            bool                   // Whether authentication succeeded
	UserID             int                    // Local database user ID (0 for external-only users)
	ExternalID         string                 // Provider-specific user ID (Casdoor UUID, LDAP DN, etc.)
	Username           string                 // Username
	TenantID           string                 // Resolved tenant ID
	Role               string                 // User role: "user", "tenant_admin", "super_admin"
	DisplayName        string                 // User display name
	Email              string                 // User email address
	Enabled            bool                   // Whether user account is enabled
	MustChangePassword bool                   // Whether user must change password on next login
	Token              string                 // JWT access token
	TokenType          string                 // Token type (always "Bearer")
	ExpiresAt          time.Time              // Token expiration time
	RefreshToken       string                 // Optional refresh token (if provider supports it)
	Metadata           map[string]interface{} // Provider-specific metadata
}

// TokenClaims represents verified JWT token claims.
type TokenClaims struct {
	UserID             int                    // Local user ID (0 if not mapped)
	ExternalID         string                 // Provider-specific user ID
	Username           string                 // Username
	TenantID           string                 // Tenant ID
	Role               string                 // User role
	Issuer             string                 // Token issuer (iss claim)
	Audience           string                 // Token audience (aud claim)
	Subject            string                 // Token subject (sub claim)
	IssuedAt           time.Time              // Token issued time (iat claim)
	ExpiresAt          time.Time              // Token expiration time (exp claim)
	MustChangePassword bool                   // Password change requirement flag
	Extra              map[string]interface{} // Additional provider-specific claims
}

// AuthFeature represents optional authentication features.
type AuthFeature string

const (
	FeaturePasswordAuth      AuthFeature = "password_auth"      // Supports username+password authentication
	FeatureTokenValidation   AuthFeature = "token_validation"   // Can validate tokens it issues
	FeatureRefreshToken      AuthFeature = "refresh_token"      // Supports token refresh
	FeatureMFA               AuthFeature = "mfa"                // Supports multi-factor authentication
	FeatureSSO               AuthFeature = "sso"                // Supports single sign-on
	FeaturePasswordPolicy    AuthFeature = "password_policy"    // Enforces password complexity rules
	FeatureSessionRevocation AuthFeature = "session_revocation" // Supports explicit session revocation
)

// Standard authentication errors.
var (
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("already exists")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidToken       = errors.New("invalid or expired token")
	ErrUserDisabled       = errors.New("user account is disabled")
	ErrTenantDisabled     = errors.New("tenant is disabled")
	ErrMFARequired        = errors.New("multi-factor authentication required")
	ErrPasswordExpired    = errors.New("password must be changed")
	ErrProviderUnavailable = errors.New("authentication provider unavailable")
)

// ============================================================================
// UserManager Interface
// ============================================================================

// UserManager handles user lifecycle operations (CRUD, password management).
// Implementations: NativeUserManager (PostgreSQL users table), CasdoorUserManager.
type UserManager interface {
	// Name returns the manager identifier.
	Name() string

	// GetUser retrieves a user by internal ID.
	GetUser(ctx context.Context, userID int) (*User, error)

	// GetUserByUsername retrieves a user by username.
	GetUserByUsername(ctx context.Context, username string) (*User, error)

	// GetUserByExternalID retrieves a user by provider-specific external ID.
	GetUserByExternalID(ctx context.Context, externalID string) (*User, error)

	// ListUsers returns users with optional filtering and pagination.
	ListUsers(ctx context.Context, filter *UserFilter) ([]*User, error)

	// CreateUser creates a new user account.
	CreateUser(ctx context.Context, req *CreateUserRequest) (*User, error)

	// UpdateUser updates an existing user's attributes.
	UpdateUser(ctx context.Context, userID int, req *UpdateUserRequest) (*User, error)

	// DeleteUser deletes a user (soft or hard delete depends on provider).
	DeleteUser(ctx context.Context, userID int) error

	// ResetPassword resets a user's password (admin action, no old password required).
	ResetPassword(ctx context.Context, userID int, newPassword string) error

	// ChangePassword changes user's own password (requires old password verification).
	ChangePassword(ctx context.Context, userID int, oldPassword, newPassword string) error

	// UpdateLastLogin updates the last login timestamp for a user.
	UpdateLastLogin(ctx context.Context, userID int) error

	// SupportsFeature checks if manager supports an optional feature.
	SupportsFeature(feature UserFeature) bool
}

// User represents a user entity.
type User struct {
	ID                 int                    // Internal database ID
	ExternalID         string                 // Provider-specific ID (Casdoor UUID, LDAP DN, etc.)
	TenantID           string                 // Tenant/organization ID
	Username           string                 // Unique username
	DisplayName        string                 // User display name
	Email              string                 // Email address
	Role               string                 // User role: "user", "tenant_admin", "super_admin"
	Enabled            bool                   // Whether user account is enabled
	MustChangePassword bool                   // Whether password change is required
	LastLoginAt        *time.Time             // Last successful login timestamp
	CreatedAt          time.Time              // Account creation timestamp
	UpdatedAt          time.Time              // Last update timestamp
	Metadata           map[string]interface{} // Provider-specific metadata (avatar, phone, etc.)
}

// UserFilter contains query filters for listing users.
type UserFilter struct {
	TenantID   string // Filter by tenant ID (empty = all tenants)
	Role       string // Filter by role (empty = all roles)
	Enabled    *bool  // Filter by enabled status (nil = all)
	SearchTerm string // Search in username/email/displayName
	Limit      int    // Maximum results to return (0 = no limit)
	Offset     int    // Number of results to skip (for pagination)
}

// CreateUserRequest contains parameters for creating a new user.
type CreateUserRequest struct {
	TenantID           string // Tenant ID (required)
	Username           string // Unique username (required)
	Password           string // Initial password (required, plaintext - will be hashed)
	DisplayName        string // Display name (optional)
	Email              string // Email address (optional)
	Role               string // Role: "user", "tenant_admin", "super_admin" (default: "user")
	Enabled            bool   // Whether account is enabled (default: true)
	MustChangePassword bool   // Whether password change is required (default: false)
}

// UpdateUserRequest contains parameters for updating a user.
// All fields are optional (nil = no change).
type UpdateUserRequest struct {
	DisplayName        *string // New display name
	Email              *string // New email address
	Role               *string // New role
	Enabled            *bool   // New enabled status
	MustChangePassword *bool   // New password change requirement
	Password           *string // New password (plaintext - will be hashed)
}

// UserFeature represents optional user management features.
type UserFeature string

const (
	FeatureUserSearch     UserFeature = "user_search"     // Supports full-text search
	FeatureUserGroups     UserFeature = "user_groups"     // Supports user groups/roles
	FeatureUserAttributes UserFeature = "user_attributes" // Supports custom attributes
	FeatureBulkOperations UserFeature = "bulk_operations" // Supports bulk import/export
)

// ============================================================================
// TenantManager Interface
// ============================================================================

// TenantManager handles tenant/organization lifecycle operations.
// Implementations: NativeTenantManager (PostgreSQL tenants table), CasdoorOrganizationManager.
type TenantManager interface {
	// Name returns the manager identifier.
	Name() string

	// GetTenant retrieves a tenant by ID/code.
	GetTenant(ctx context.Context, tenantID string) (*Tenant, error)

	// ListTenants returns all tenants with optional filtering.
	ListTenants(ctx context.Context, filter *TenantFilter) ([]*Tenant, error)

	// CreateTenant creates a new tenant/organization.
	CreateTenant(ctx context.Context, req *CreateTenantRequest) (*Tenant, error)

	// UpdateTenant updates tenant metadata and policies.
	UpdateTenant(ctx context.Context, tenantID string, req *UpdateTenantRequest) (*Tenant, error)

	// DeleteTenant deletes a tenant (may fail if tenant has users).
	DeleteTenant(ctx context.Context, tenantID string) error

	// EnableTenant enables a previously disabled tenant.
	EnableTenant(ctx context.Context, tenantID string) error

	// DisableTenant disables a tenant (prevents user logins).
	DisableTenant(ctx context.Context, tenantID string) error

	// SupportsFeature checks if manager supports an optional feature.
	SupportsFeature(feature TenantFeature) bool
}

// Tenant represents a tenant/organization entity.
type Tenant struct {
	ID          string                 // Tenant code/identifier (unique)
	Name        string                 // Internal name (used in database queries)
	DisplayName string                 // Human-readable display name
	Status      string                 // Status: "active", "disabled", "suspended"
	Tags        []string               // Optional tags for categorization
	IsInternal  bool                   // Whether this is an internal/system tenant
	IsForbidden bool                   // Whether tenant is explicitly forbidden
	Policy      *TenantPolicy          // Tenant-level rate limits and policies
	CreatedAt   time.Time              // Creation timestamp
	UpdatedAt   time.Time              // Last update timestamp
	Metadata    map[string]interface{} // Provider-specific metadata
}

// TenantPolicy contains tenant-level rate limits and feature flags.
type TenantPolicy struct {
	MaxConcurrent       int      // Maximum concurrent requests
	MaxTokensPerRequest int      // Maximum tokens per request
	MaxRequestsPerMin   int      // Maximum requests per minute
	AllowStreaming      bool     // Whether streaming is allowed
	AllowTools          bool     // Whether tool/function calling is allowed
	AllowedModels       []string // Whitelist of allowed models (empty = all)
}

// TenantFilter contains query filters for listing tenants.
type TenantFilter struct {
	Status     string // Filter by status (empty = all)
	SearchTerm string // Search in name/displayName
	Limit      int    // Maximum results (0 = no limit)
	Offset     int    // Pagination offset
}

// CreateTenantRequest contains parameters for creating a new tenant.
type CreateTenantRequest struct {
	ID          string        // Tenant code (required, must be unique)
	Name        string        // Internal name (required)
	DisplayName string        // Display name (optional, defaults to Name)
	Tags        []string      // Optional tags
	IsInternal  bool          // Whether this is an internal tenant
	Policy      *TenantPolicy // Optional initial policy
}

// UpdateTenantRequest contains parameters for updating a tenant.
// All fields are optional (nil = no change).
type UpdateTenantRequest struct {
	Name        *string       // New internal name
	DisplayName *string       // New display name
	Tags        *[]string     // New tags (replaces existing)
	Policy      *TenantPolicy // New policy (replaces existing)
}

// TenantFeature represents optional tenant management features.
type TenantFeature string

const (
	FeatureTenantHierarchy TenantFeature = "tenant_hierarchy" // Supports parent/child relationships
	FeatureTenantQuotas    TenantFeature = "tenant_quotas"    // Supports usage quotas
	FeatureTenantIsolation TenantFeature = "tenant_isolation" // Enforces strict data isolation
)

// ============================================================================
// ApprovalProvider Interface
// ============================================================================

// ApprovalProvider handles approval workflow configuration.
// Note: Approval *execution* (queue management) remains in domains/sessionaudit.
// This interface only handles configuration (approvers, rules, settings).
//
// Implementations: NativeApprovalProvider (PostgreSQL), CasdoorApprovalAdapter (hybrid).
type ApprovalProvider interface {
	// Name returns the provider identifier.
	Name() string

	// GetConfig retrieves approval configuration for a tenant.
	GetConfig(ctx context.Context, tenantID string) (*ApprovalConfig, error)

	// UpdateConfig updates approval configuration.
	UpdateConfig(ctx context.Context, tenantID string, config *ApprovalConfig) error

	// GetApprovers lists approvers for a tenant.
	GetApprovers(ctx context.Context, tenantID string) ([]*Approver, error)

	// AddApprover adds an approver to a tenant.
	AddApprover(ctx context.Context, tenantID string, approver *Approver) error

	// UpdateApprover updates an existing approver.
	UpdateApprover(ctx context.Context, tenantID, userID string, approver *Approver) error

	// RemoveApprover removes an approver from a tenant.
	RemoveApprover(ctx context.Context, tenantID, userID string) error

	// GetRules lists approval rules for a tenant.
	GetRules(ctx context.Context, tenantID string) ([]*ApprovalRule, error)

	// AddRule adds an approval rule.
	AddRule(ctx context.Context, tenantID string, rule *ApprovalRule) error

	// RemoveRule removes an approval rule by name.
	RemoveRule(ctx context.Context, tenantID, ruleName string) error

	// SupportsFeature checks if provider supports an optional feature.
	SupportsFeature(feature ApprovalFeature) bool
}

// ApprovalConfig represents approval workflow configuration for a tenant.
type ApprovalConfig struct {
	Enabled          bool   // Whether approval workflow is enabled
	RequireApproval  bool   // Whether approval is required for all sessions
	TimeoutMinutes   int    // Approval timeout in minutes (default: 15)
	TimeoutAction    string // Action on timeout: "deny" or "auto_approve"
	NotifyOnPending  bool   // Send notification when approval is pending
	NotifyOnApproved bool   // Send notification when approved
	NotifyOnRejected bool   // Send notification when rejected
}

// Approver represents an approval authority for a tenant.
type Approver struct {
	UserID      string // User ID (internal or external)
	Username    string // Username for display
	DisplayName string // User display name
	Email       string // Email for notifications
	Priority    int    // Priority level (1 = highest)
	Enabled     bool   // Whether approver is active
}

// ApprovalRule represents a condition that triggers approval requirement.
type ApprovalRule struct {
	Name        string                 // Unique rule name
	Description string                 // Human-readable description
	Conditions  map[string]interface{} // Rule-specific conditions (model, token count, etc.)
	Priority    int                    // Rule priority (1 = highest)
	Enabled     bool                   // Whether rule is active
}

// ApprovalFeature represents optional approval workflow features.
type ApprovalFeature string

const (
	FeatureMultiLevelApproval ApprovalFeature = "multi_level_approval" // Requires multiple approvers
	FeatureApprovalDelegation ApprovalFeature = "approval_delegation"  // Approvers can delegate
	FeatureApprovalEscalation ApprovalFeature = "approval_escalation"  // Auto-escalate after timeout
)
