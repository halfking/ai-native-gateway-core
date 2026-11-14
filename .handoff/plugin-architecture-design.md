# LLM Gateway Authentication Plugin Architecture Design

**Version**: 1.0  
**Date**: 2026-11-14  
**Status**: Design Proposal

## Executive Summary

This document proposes a plugin-based authentication architecture for llm-gateway-go that enables switching between native PostgreSQL-based authentication and external identity providers (initially Casdoor). The design maintains full backward compatibility while introducing clear abstraction layers.

## 1. Current Architecture Analysis

### 1.1 Authentication Flow

```
Request → AuthMiddleware (global API key)
       → AdminMiddleware (JWT/cookie verification)
       → Handler (business logic)
```

### 1.2 Key Components

| Component | Location | Responsibility |
|-----------|----------|----------------|
| Login Handler | `admin/auth.go` | Password verification, JWT signing |
| User CRUD | `admin/users.go` | Direct PostgreSQL `users` table operations |
| Tenant Context | `domain/tenant.go` | Tenant metadata struct |
| JWT Verification | `pkg/identity/verifier.go` | Multi-issuer JWT validation |
| Approval Config | `admin/approval_config_handler.go` | Approval workflow configuration |
| Auth Middleware | `middleware/auth_mw.go` | API key gate |
| Admin Middleware | `admin/handler.go` | JWT/cookie authentication |

### 1.3 Database Schema Dependencies

**Critical Tables**:
- `users` (id, tenant_id, username, password_hash, role, enabled)
- `tenants` (code, name, status)
- `api_keys` (application_id, tenant_id, key_hash, enabled)
- `routing_audit_log` (actor, action, target_type, after_json)
- `approval_queue` (session审批相关)

### 1.4 Coupling Points

1. **Direct SQL queries** in handlers (admin/users.go, admin/auth.go)
2. **Hardcoded bcrypt** password hashing (admin/users.go:239)
3. **JWT claims structure** tied to users table schema
4. **Tenant validation** embedded in user creation flow
5. **Audit logging** directly writes to routing_audit_log

## 2. Plugin Architecture Design

### 2.1 Core Principles

1. **Interface-based abstraction**: All authentication operations behind interfaces
2. **Backward compatibility**: Native implementation matches current behavior exactly
3. **Configuration-driven**: Plugin selection via environment variables
4. **Progressive migration**: Can switch per-component (auth, users, tenants independently)
5. **Zero breaking changes**: Existing code paths remain functional

### 2.2 Architecture Diagram

```mermaid
graph TB
    subgraph "HTTP Layer"
        Handler[Handler]
    end
    
    subgraph "Plugin Registry"
        Registry[AuthRegistry]
        Config[Plugin Config]
    end
    
    subgraph "Plugin Interfaces"
        AuthProvider[AuthProvider Interface]
        UserManager[UserManager Interface]
        TenantManager[TenantManager Interface]
        ApprovalProvider[ApprovalProvider Interface]
    end
    
    subgraph "Native Implementation"
        NativeAuth[NativeAuthProvider]
        NativeUser[NativeUserManager]
        NativeTenant[NativeTenantManager]
        NativeApproval[NativeApprovalProvider]
    end
    
    subgraph "Casdoor Implementation"
        CasdoorAuth[CasdoorAuthProvider]
        CasdoorUser[CasdoorUserManager]
        CasdoorTenant[CasdoorOrganizationManager]
        CasdoorApproval[CasdoorApprovalAdapter]
    end
    
    subgraph "Data Layer"
        PG[(PostgreSQL)]
        Casdoor[Casdoor API]
    end
    
    Handler --> Registry
    Registry --> Config
    Registry --> AuthProvider
    Registry --> UserManager
    Registry --> TenantManager
    Registry --> ApprovalProvider
    
    AuthProvider -.-> NativeAuth
    AuthProvider -.-> CasdoorAuth
    UserManager -.-> NativeUser
    UserManager -.-> CasdoorUser
    TenantManager -.-> NativeTenant
    TenantManager -.-> CasdoorTenant
    ApprovalProvider -.-> NativeApproval
    ApprovalProvider -.-> CasdoorApproval
    
    NativeAuth --> PG
    NativeUser --> PG
    NativeTenant --> PG
    NativeApproval --> PG
    
    CasdoorAuth --> Casdoor
    CasdoorUser --> Casdoor
    CasdoorTenant --> Casdoor
    CasdoorApproval --> PG
```

### 2.3 Component Interaction Flow

```mermaid
sequenceDiagram
    participant Client
    participant Middleware
    participant Handler
    participant Registry
    participant AuthProvider
    participant UserManager
    participant Database
    
    Client->>Middleware: POST /api/auth/login
    Middleware->>Handler: Authenticated Request
    Handler->>Registry: GetAuthProvider()
    Registry-->>Handler: AuthProvider (Native/Casdoor)
    Handler->>AuthProvider: Authenticate(username, password)
    AuthProvider->>Database: Verify Credentials
    Database-->>AuthProvider: User Data
    AuthProvider-->>Handler: AuthResult + JWT Claims
    Handler->>Registry: GetUserManager()
    Registry-->>Handler: UserManager
    Handler->>UserManager: UpdateLastLogin(userID)
    Handler-->>Client: JWT Token + User Info
```

## 3. Interface Definitions

### 3.1 AuthProvider Interface

```go
package authplugin

import (
    "context"
    "time"
)

// AuthProvider handles authentication operations.
type AuthProvider interface {
    // Name returns the provider identifier (e.g., "native", "casdoor").
    Name() string
    
    // Authenticate verifies username/password and returns auth result.
    // Returns ErrInvalidCredentials if auth fails.
    Authenticate(ctx context.Context, req *AuthRequest) (*AuthResult, error)
    
    // ValidateToken verifies a JWT token and returns claims.
    // Returns ErrInvalidToken if validation fails.
    ValidateToken(ctx context.Context, token string) (*TokenClaims, error)
    
    // RefreshToken generates a new token from a valid refresh token.
    RefreshToken(ctx context.Context, refreshToken string) (*AuthResult, error)
    
    // Logout invalidates a token (if provider supports it).
    Logout(ctx context.Context, token string) error
    
    // SupportsFeature checks if provider supports a feature.
    SupportsFeature(feature AuthFeature) bool
}

// AuthRequest contains authentication parameters.
type AuthRequest struct {
    Username   string
    Password   string
    TenantID   string // May be empty, provider resolves
    RememberMe bool
    CaptchaID  string
    CaptchaCode string
    IPAddress  string
    UserAgent  string
}

// AuthResult contains authentication outcome.
type AuthResult struct {
    Success      bool
    UserID       int    // Local user ID (0 for external-only users)
    ExternalID   string // Provider-specific user ID
    Username     string
    TenantID     string
    Role         string
    DisplayName  string
    Email        string
    Enabled      bool
    MustChangePassword bool
    Token        string
    TokenType    string // "Bearer"
    ExpiresAt    time.Time
    RefreshToken string // Optional
    Metadata     map[string]interface{} // Provider-specific data
}

// TokenClaims represents verified JWT claims.
type TokenClaims struct {
    UserID       int
    ExternalID   string
    Username     string
    TenantID     string
    Role         string
    Issuer       string
    Audience     string
    Subject      string
    IssuedAt     time.Time
    ExpiresAt    time.Time
    MustChangePassword bool
    Extra        map[string]interface{}
}

// AuthFeature represents optional authentication features.
type AuthFeature string

const (
    FeatureRefreshToken     AuthFeature = "refresh_token"
    FeatureMFA              AuthFeature = "mfa"
    FeatureSSO              AuthFeature = "sso"
    FeaturePasswordPolicy   AuthFeature = "password_policy"
    FeatureSessionRevocation AuthFeature = "session_revocation"
)

// Standard errors
var (
    ErrInvalidCredentials = errors.New("invalid username or password")
    ErrInvalidToken       = errors.New("invalid or expired token")
    ErrUserDisabled       = errors.New("user account is disabled")
    ErrTenantDisabled     = errors.New("tenant is disabled")
    ErrMFARequired        = errors.New("MFA verification required")
    ErrPasswordExpired    = errors.New("password must be changed")
)
```

### 3.2 UserManager Interface

```go
package authplugin

import (
    "context"
    "time"
)

// UserManager handles user lifecycle operations.
type UserManager interface {
    // Name returns the manager identifier.
    Name() string
    
    // GetUser retrieves a user by ID.
    GetUser(ctx context.Context, userID int) (*User, error)
    
    // GetUserByUsername retrieves a user by username.
    GetUserByUsername(ctx context.Context, username string) (*User, error)
    
    // ListUsers returns users with optional filtering.
    ListUsers(ctx context.Context, filter *UserFilter) ([]*User, error)
    
    // CreateUser creates a new user.
    CreateUser(ctx context.Context, req *CreateUserRequest) (*User, error)
    
    // UpdateUser updates an existing user.
    UpdateUser(ctx context.Context, userID int, req *UpdateUserRequest) (*User, error)
    
    // DeleteUser deletes a user (soft or hard delete depends on provider).
    DeleteUser(ctx context.Context, userID int) error
    
    // ResetPassword resets a user's password (admin action).
    ResetPassword(ctx context.Context, userID int, newPassword string) error
    
    // ChangePassword changes user's own password (requires old password).
    ChangePassword(ctx context.Context, userID int, oldPassword, newPassword string) error
    
    // UpdateLastLogin updates last login timestamp.
    UpdateLastLogin(ctx context.Context, userID int) error
    
    // SupportsFeature checks if manager supports a feature.
    SupportsFeature(feature UserFeature) bool
}

// User represents a user entity.
type User struct {
    ID                 int
    ExternalID         string // Provider-specific ID
    TenantID           string
    Username           string
    DisplayName        string
    Email              string
    Role               string
    Enabled            bool
    MustChangePassword bool
    LastLoginAt        *time.Time
    CreatedAt          time.Time
    UpdatedAt          time.Time
    Metadata           map[string]interface{}
}

// UserFilter contains user query filters.
type UserFilter struct {
    TenantID   string
    Role       string
    Enabled    *bool
    SearchTerm string // Search in username/email/displayName
    Limit      int
    Offset     int
}

// CreateUserRequest contains user creation parameters.
type CreateUserRequest struct {
    TenantID    string
    Username    string
    Password    string
    DisplayName string
    Email       string
    Role        string
    Enabled     bool
    MustChangePassword bool
}

// UpdateUserRequest contains user update parameters.
type UpdateUserRequest struct {
    DisplayName        *string
    Email              *string
    Role               *string
    Enabled            *bool
    MustChangePassword *bool
    Password           *string // Optional password reset
}

// UserFeature represents optional user management features.
type UserFeature string

const (
    FeatureUserSearch       UserFeature = "user_search"
    FeatureUserGroups       UserFeature = "user_groups"
    FeatureUserAttributes   UserFeature = "user_attributes"
    FeatureBulkOperations   UserFeature = "bulk_operations"
)
```

### 3.3 TenantManager Interface

```go
package authplugin

import (
    "context"
    "time"
)

// TenantManager handles tenant/organization operations.
type TenantManager interface {
    // Name returns the manager identifier.
    Name() string
    
    // GetTenant retrieves a tenant by ID/code.
    GetTenant(ctx context.Context, tenantID string) (*Tenant, error)
    
    // ListTenants returns all tenants with optional filtering.
    ListTenants(ctx context.Context, filter *TenantFilter) ([]*Tenant, error)
    
    // CreateTenant creates a new tenant.
    CreateTenant(ctx context.Context, req *CreateTenantRequest) (*Tenant, error)
    
    // UpdateTenant updates tenant metadata.
    UpdateTenant(ctx context.Context, tenantID string, req *UpdateTenantRequest) (*Tenant, error)
    
    // DeleteTenant deletes a tenant.
    DeleteTenant(ctx context.Context, tenantID string) error
    
    // EnableTenant enables a disabled tenant.
    EnableTenant(ctx context.Context, tenantID string) error
    
    // DisableTenant disables a tenant.
    DisableTenant(ctx context.Context, tenantID string) error
    
    // SupportsFeature checks if manager supports a feature.
    SupportsFeature(feature TenantFeature) bool
}

// Tenant represents a tenant/organization.
type Tenant struct {
    ID          string // Code/identifier
    Name        string
    DisplayName string
    Status      string // "active", "disabled", "suspended"
    Tags        []string
    IsInternal  bool
    IsForbidden bool
    Policy      *TenantPolicy
    CreatedAt   time.Time
    UpdatedAt   time.Time
    Metadata    map[string]interface{}
}

// TenantPolicy contains tenant-level policies.
type TenantPolicy struct {
    MaxConcurrent       int
    MaxTokensPerRequest int
    MaxRequestsPerMin   int
    AllowStreaming      bool
    AllowTools          bool
    AllowedModels       []string
}

// TenantFilter contains tenant query filters.
type TenantFilter struct {
    Status     string
    SearchTerm string
    Limit      int
    Offset     int
}

// CreateTenantRequest contains tenant creation parameters.
type CreateTenantRequest struct {
    ID          string
    Name        string
    DisplayName string
    Tags        []string
    IsInternal  bool
    Policy      *TenantPolicy
}

// UpdateTenantRequest contains tenant update parameters.
type UpdateTenantRequest struct {
    Name        *string
    DisplayName *string
    Tags        *[]string
    Policy      *TenantPolicy
}

// TenantFeature represents optional tenant management features.
type TenantFeature string

const (
    FeatureTenantHierarchy TenantFeature = "tenant_hierarchy"
    FeatureTenantQuotas    TenantFeature = "tenant_quotas"
    FeatureTenantIsolation TenantFeature = "tenant_isolation"
)
```

### 3.4 ApprovalProvider Interface

```go
package authplugin

import (
    "context"
)

// ApprovalProvider handles approval workflow operations.
type ApprovalProvider interface {
    // Name returns the provider identifier.
    Name() string
    
    // GetConfig retrieves approval configuration for a tenant.
    GetConfig(ctx context.Context, tenantID string) (*ApprovalConfig, error)
    
    // UpdateConfig updates approval configuration.
    UpdateConfig(ctx context.Context, tenantID string, config *ApprovalConfig) error
    
    // GetApprovers lists approvers for a tenant.
    GetApprovers(ctx context.Context, tenantID string) ([]*Approver, error)
    
    // AddApprover adds an approver.
    AddApprover(ctx context.Context, tenantID string, approver *Approver) error
    
    // UpdateApprover updates an approver.
    UpdateApprover(ctx context.Context, tenantID, userID string, approver *Approver) error
    
    // RemoveApprover removes an approver.
    RemoveApprover(ctx context.Context, tenantID, userID string) error
    
    // GetRules lists approval rules.
    GetRules(ctx context.Context, tenantID string) ([]*ApprovalRule, error)
    
    // AddRule adds an approval rule.
    AddRule(ctx context.Context, tenantID string, rule *ApprovalRule) error
    
    // RemoveRule removes an approval rule.
    RemoveRule(ctx context.Context, tenantID, ruleName string) error
    
    // SupportsFeature checks if provider supports a feature.
    SupportsFeature(feature ApprovalFeature) bool
}

// ApprovalConfig represents approval workflow configuration.
type ApprovalConfig struct {
    Enabled          bool
    RequireApproval  bool
    TimeoutMinutes   int
    TimeoutAction    string // "deny", "auto_approve"
    NotifyOnPending  bool
    NotifyOnApproved bool
    NotifyOnRejected bool
}

// Approver represents an approval authority.
type Approver struct {
    UserID      string
    Username    string
    DisplayName string
    Email       string
    Priority    int
    Enabled     bool
}

// ApprovalRule represents an approval trigger rule.
type ApprovalRule struct {
    Name        string
    Description string
    Conditions  map[string]interface{} // Rule-specific conditions
    Priority    int
    Enabled     bool
}

// ApprovalFeature represents optional approval features.
type ApprovalFeature string

const (
    FeatureMultiLevelApproval ApprovalFeature = "multi_level_approval"
    FeatureApprovalDelegation ApprovalFeature = "approval_delegation"
    FeatureApprovalEscalation ApprovalFeature = "approval_escalation"
)
```

## 4. Plugin Registry

### 4.1 Registry Interface

```go
package authplugin

import (
    "context"
    "fmt"
    "sync"
)

// Registry manages plugin lifecycle and selection.
type Registry struct {
    mu                sync.RWMutex
    authProviders     map[string]AuthProvider
    userManagers      map[string]UserManager
    tenantManagers    map[string]TenantManager
    approvalProviders map[string]ApprovalProvider
    config            *PluginConfig
}

// PluginConfig specifies which plugins to use.
type PluginConfig struct {
    AuthProvider     string // "native" or "casdoor"
    UserManager      string
    TenantManager    string
    ApprovalProvider string
    
    // Provider-specific configuration
    Native  *NativeConfig
    Casdoor *CasdoorConfig
}

// NativeConfig contains native implementation configuration.
type NativeConfig struct {
    DatabaseDSN string
    JWTSecret   string
    EncryptionKey []byte
}

// CasdoorConfig contains Casdoor connection parameters.
type CasdoorConfig struct {
    Endpoint      string
    ClientID      string
    ClientSecret  string
    Organization  string
    Application   string
    TLSSkipVerify bool
}

// NewRegistry creates a new plugin registry.
func NewRegistry(config *PluginConfig) *Registry {
    return &Registry{
        authProviders:     make(map[string]AuthProvider),
        userManagers:      make(map[string]UserManager),
        tenantManagers:    make(map[string]TenantManager),
        approvalProviders: make(map[string]ApprovalProvider),
        config:            config,
    }
}

// RegisterAuthProvider registers an authentication provider.
func (r *Registry) RegisterAuthProvider(provider AuthProvider) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    
    name := provider.Name()
    if _, exists := r.authProviders[name]; exists {
        return fmt.Errorf("auth provider %q already registered", name)
    }
    r.authProviders[name] = provider
    return nil
}

// GetAuthProvider returns the configured auth provider.
func (r *Registry) GetAuthProvider() (AuthProvider, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    
    name := r.config.AuthProvider
    provider, ok := r.authProviders[name]
    if !ok {
        return nil, fmt.Errorf("auth provider %q not found", name)
    }
    return provider, nil
}

// Similar methods for UserManager, TenantManager, ApprovalProvider...
```

### 4.2 Configuration Loading

```go
package authplugin

import (
    "os"
    "strings"
)

// LoadConfigFromEnv loads plugin configuration from environment variables.
func LoadConfigFromEnv() (*PluginConfig, error) {
    cfg := &PluginConfig{
        AuthProvider:     getEnvOrDefault("AUTH_PROVIDER", "native"),
        UserManager:      getEnvOrDefault("USER_MANAGER", "native"),
        TenantManager:    getEnvOrDefault("TENANT_MANAGER", "native"),
        ApprovalProvider: getEnvOrDefault("APPROVAL_PROVIDER", "native"),
    }
    
    // Load native config
    cfg.Native = &NativeConfig{
        DatabaseDSN:   os.Getenv("DATABASE_DSN"),
        JWTSecret:     os.Getenv("JWT_SECRET"),
        EncryptionKey: []byte(os.Getenv("ENCRYPTION_KEY")),
    }
    
    // Load Casdoor config
    if strings.Contains(cfg.AuthProvider, "casdoor") ||
       strings.Contains(cfg.UserManager, "casdoor") ||
       strings.Contains(cfg.TenantManager, "casdoor") {
        cfg.Casdoor = &CasdoorConfig{
            Endpoint:      os.Getenv("CASDOOR_ENDPOINT"),
            ClientID:      os.Getenv("CASDOOR_CLIENT_ID"),
            ClientSecret:  os.Getenv("CASDOOR_CLIENT_SECRET"),
            Organization:  os.Getenv("CASDOOR_ORGANIZATION"),
            Application:   os.Getenv("CASDOOR_APPLICATION"),
            TLSSkipVerify: os.Getenv("CASDOOR_TLS_SKIP_VERIFY") == "true",
        }
    }
    
    return cfg, nil
}

func getEnvOrDefault(key, defaultValue string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return defaultValue
}
```

## 5. Native Implementation

### 5.1 Native Auth Provider

```go
package native

import (
    "context"
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "time"
    
    "github.com/jackc/pgx/v5/pgxpool"
    "golang.org/x/crypto/bcrypt"
    "github.com/kaixuan/llm-gateway-go/authplugin"
)

// AuthProvider implements authplugin.AuthProvider using PostgreSQL.
type AuthProvider struct {
    db        *pgxpool.Pool
    jwtSecret string
}

// NewAuthProvider creates a native auth provider.
func NewAuthProvider(db *pgxpool.Pool, jwtSecret string) *AuthProvider {
    return &AuthProvider{
        db:        db,
        jwtSecret: jwtSecret,
    }
}

// Name returns "native".
func (p *AuthProvider) Name() string {
    return "native"
}

// Authenticate verifies credentials against the users table.
func (p *AuthProvider) Authenticate(ctx context.Context, req *authplugin.AuthRequest) (*authplugin.AuthResult, error) {
    // Query users table
    var user struct {
        ID                 int
        TenantID           string
        Username           string
        PasswordHash       string
        DisplayName        string
        Email              string
        Role               string
        Enabled            bool
        MustChangePassword bool
    }
    
    err := p.db.QueryRow(ctx, `
        SELECT id, tenant_id, username, password_hash, display_name, email, 
               role, enabled, must_change_password
        FROM users WHERE username = $1
    `, req.Username).Scan(
        &user.ID, &user.TenantID, &user.Username, &user.PasswordHash,
        &user.DisplayName, &user.Email, &user.Role, &user.Enabled,
        &user.MustChangePassword,
    )
    if err != nil {
        return nil, authplugin.ErrInvalidCredentials
    }
    
    // Check enabled
    if !user.Enabled {
        return nil, authplugin.ErrUserDisabled
    }
    
    // Verify password
    if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
        return nil, authplugin.ErrInvalidCredentials
    }
    
    // Check tenant status
    if user.TenantID != "default" {
        var tenantStatus string
        err := p.db.QueryRow(ctx, `SELECT status FROM tenants WHERE code = $1`, user.TenantID).Scan(&tenantStatus)
        if err != nil || tenantStatus == "disabled" {
            return nil, authplugin.ErrTenantDisabled
        }
    }
    
    // Generate JWT
    token, expiresAt, err := p.signToken(user.ID, user.TenantID, user.Username, user.Role, user.MustChangePassword)
    if err != nil {
        return nil, err
    }
    
    // Update last_login_at
    _, _ = p.db.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, user.ID)
    
    return &authplugin.AuthResult{
        Success:            true,
        UserID:             user.ID,
        Username:           user.Username,
        TenantID:           user.TenantID,
        Role:               user.Role,
        DisplayName:        user.DisplayName,
        Email:              user.Email,
        Enabled:            user.Enabled,
        MustChangePassword: user.MustChangePassword,
        Token:              token,
        TokenType:          "Bearer",
        ExpiresAt:          expiresAt,
    }, nil
}

// signToken generates a JWT (delegate to existing admin.SignToken).
func (p *AuthProvider) signToken(userID int, tenantID, username, role string, mustChange bool) (string, time.Time, error) {
    // Implementation delegates to existing admin/jwt.go logic
    // This is just the interface wrapper
    return "", time.Time{}, nil
}

// ValidateToken verifies a JWT using existing identity.Verify logic.
func (p *AuthProvider) ValidateToken(ctx context.Context, token string) (*authplugin.TokenClaims, error) {
    // Delegate to pkg/identity/verifier.go
    return nil, authplugin.ErrInvalidToken
}

// RefreshToken is not supported by native provider.
func (p *AuthProvider) RefreshToken(ctx context.Context, refreshToken string) (*authplugin.AuthResult, error) {
    return nil, fmt.Errorf("refresh tokens not supported by native provider")
}

// Logout is a no-op for native provider (stateless JWT).
func (p *AuthProvider) Logout(ctx context.Context, token string) error {
    return nil
}

// SupportsFeature reports native provider capabilities.
func (p *AuthProvider) SupportsFeature(feature authplugin.AuthFeature) bool {
    switch feature {
    case authplugin.FeatureSessionRevocation:
        return true // Via password change epoch
    case authplugin.FeaturePasswordPolicy:
        return true
    default:
        return false
    }
}
```

### 5.2 Native User Manager

The native user manager wraps existing admin/users.go logic into the UserManager interface. Similar pattern to AuthProvider.

### 5.3 Native Tenant Manager

Wraps tenant operations from existing tenant-related queries.

### 5.4 Native Approval Provider

Wraps existing approval_config_handler.go ConfigManager logic.

## 6. Casdoor Implementation

### 6.1 Casdoor Auth Provider

```go
package casdoor

import (
    "context"
    "fmt"
    
    casdoorsdk "github.com/casdoor/casdoor-go-sdk/casdoorsdk"
    "github.com/kaixuan/llm-gateway-go/authplugin"
)

// AuthProvider implements authplugin.AuthProvider using Casdoor.
type AuthProvider struct {
    client       *casdoorsdk.Client
    organization string
    application  string
}

// NewAuthProvider creates a Casdoor auth provider.
func NewAuthProvider(config *authplugin.CasdoorConfig) (*AuthProvider, error) {
    authConfig := &casdoorsdk.AuthConfig{
        Endpoint:      config.Endpoint,
        ClientId:      config.ClientID,
        ClientSecret:  config.ClientSecret,
        Certificate:   "",
        OrganizationName: config.Organization,
        ApplicationName:  config.Application,
    }
    
    client := casdoorsdk.NewClient(authConfig)
    
    return &AuthProvider{
        client:       client,
        organization: config.Organization,
        application:  config.Application,
    }, nil
}

// Name returns "casdoor".
func (p *AuthProvider) Name() string {
    return "casdoor"
}

// Authenticate verifies credentials via Casdoor API.
func (p *AuthProvider) Authenticate(ctx context.Context, req *authplugin.AuthRequest) (*authplugin.AuthResult, error) {
    // Call Casdoor login API
    token, err := p.client.GetOAuthToken(req.Username, req.Password)
    if err != nil {
        return nil, authplugin.ErrInvalidCredentials
    }
    
    // Parse token and get user info
    claims, err := p.client.ParseJwtToken(token.AccessToken)
    if err != nil {
        return nil, authplugin.ErrInvalidToken
    }
    
    // Get full user details
    user, err := p.client.GetUser(claims.Name)
    if err != nil {
        return nil, fmt.Errorf("failed to get user details: %w", err)
    }
    
    // Map Casdoor fields to AuthResult
    return &authplugin.AuthResult{
        Success:      true,
        ExternalID:   user.Id,
        Username:     user.Name,
        TenantID:     user.Owner, // Casdoor organization maps to tenant
        Role:         mapCasdoorRole(user),
        DisplayName:  user.DisplayName,
        Email:        user.Email,
        Enabled:      !user.IsForbidden,
        Token:        token.AccessToken,
        TokenType:    "Bearer",
        ExpiresAt:    token.ExpiresAt,
        RefreshToken: token.RefreshToken,
        Metadata: map[string]interface{}{
            "casdoor_id": user.Id,
            "avatar":     user.Avatar,
        },
    }, nil
}

// ValidateToken verifies a Casdoor JWT.
func (p *AuthProvider) ValidateToken(ctx context.Context, token string) (*authplugin.TokenClaims, error) {
    claims, err := p.client.ParseJwtToken(token)
    if err != nil {
        return nil, authplugin.ErrInvalidToken
    }
    
    return &authplugin.TokenClaims{
        ExternalID: claims.Id,
        Username:   claims.Name,
        TenantID:   claims.Owner,
        Issuer:     claims.Issuer,
        Subject:    claims.Subject,
        ExpiresAt:  claims.ExpiresAt.Time,
    }, nil
}

// RefreshToken uses Casdoor refresh token flow.
func (p *AuthProvider) RefreshToken(ctx context.Context, refreshToken string) (*authplugin.AuthResult, error) {
    token, err := p.client.RefreshOAuthToken(refreshToken)
    if err != nil {
        return nil, fmt.Errorf("refresh failed: %w", err)
    }
    
    // Parse and return new result
    claims, err := p.client.ParseJwtToken(token.AccessToken)
    if err != nil {
        return nil, authplugin.ErrInvalidToken
    }
    
    return &authplugin.AuthResult{
        Success:      true,
        ExternalID:   claims.Id,
        Username:     claims.Name,
        Token:        token.AccessToken,
        TokenType:    "Bearer",
        ExpiresAt:    token.ExpiresAt,
        RefreshToken: token.RefreshToken,
    }, nil
}

// Logout revokes token at Casdoor (if supported).
func (p *AuthProvider) Logout(ctx context.Context, token string) error {
    // Casdoor may support token revocation
    return nil
}

// SupportsFeature reports Casdoor capabilities.
func (p *AuthProvider) SupportsFeature(feature authplugin.AuthFeature) bool {
    switch feature {
    case authplugin.FeatureRefreshToken:
        return true
    case authplugin.FeatureMFA:
        return true
    case authplugin.FeatureSSO:
        return true
    default:
        return false
    }
}

// mapCasdoorRole maps Casdoor roles to gateway roles.
func mapCasdoorRole(user *casdoorsdk.User) string {
    // Map based on Casdoor user attributes
    // This needs to be configured based on your Casdoor setup
    if user.IsAdmin {
        return "super_admin"
    }
    return "tenant_admin"
}
```

### 6.2 Casdoor User Manager

```go
package casdoor

import (
    "context"
    "fmt"
    "time"
    
    casdoorsdk "github.com/casdoor/casdoor-go-sdk/casdoorsdk"
    "github.com/kaixuan/llm-gateway-go/authplugin"
)

// UserManager implements authplugin.UserManager using Casdoor.
type UserManager struct {
    client       *casdoorsdk.Client
    organization string
}

// NewUserManager creates a Casdoor user manager.
func NewUserManager(config *authplugin.CasdoorConfig) (*UserManager, error) {
    authConfig := &casdoorsdk.AuthConfig{
        Endpoint:         config.Endpoint,
        ClientId:         config.ClientID,
        ClientSecret:     config.ClientSecret,
        OrganizationName: config.Organization,
        ApplicationName:  config.Application,
    }
    
    return &UserManager{
        client:       casdoorsdk.NewClient(authConfig),
        organization: config.Organization,
    }, nil
}

// Name returns "casdoor".
func (m *UserManager) Name() string {
    return "casdoor"
}

// GetUser retrieves user from Casdoor.
func (m *UserManager) GetUser(ctx context.Context, userID int) (*authplugin.User, error) {
    // Note: Casdoor uses string IDs, need mapping logic
    // This might require a local shadow table for ID mapping
    return nil, fmt.Errorf("get by int ID requires shadow mapping")
}

// GetUserByUsername retrieves user by username.
func (m *UserManager) GetUserByUsername(ctx context.Context, username string) (*authplugin.User, error) {
    user, err := m.client.GetUser(username)
    if err != nil {
        return nil, fmt.Errorf("casdoor: get user failed: %w", err)
    }
    
    return &authplugin.User{
        ExternalID:  user.Id,
        TenantID:    user.Owner,
        Username:    user.Name,
        DisplayName: user.DisplayName,
        Email:       user.Email,
        Role:        mapCasdoorRole(user),
        Enabled:     !user.IsForbidden,
        CreatedAt:   parseTime(user.CreatedTime),
        UpdatedAt:   parseTime(user.UpdatedTime),
        Metadata: map[string]interface{}{
            "avatar": user.Avatar,
            "phone":  user.Phone,
        },
    }, nil
}

// ListUsers lists users from Casdoor.
func (m *UserManager) ListUsers(ctx context.Context, filter *authplugin.UserFilter) ([]*authplugin.User, error) {
    // Casdoor API call to list users
    casdoorUsers, err := m.client.GetUsers()
    if err != nil {
        return nil, fmt.Errorf("casdoor: list users failed: %w", err)
    }
    
    var users []*authplugin.User
    for _, cu := range casdoorUsers {
        // Apply filters
        if filter.TenantID != "" && cu.Owner != filter.TenantID {
            continue
        }
        
        users = append(users, &authplugin.User{
            ExternalID:  cu.Id,
            TenantID:    cu.Owner,
            Username:    cu.Name,
            DisplayName: cu.DisplayName,
            Email:       cu.Email,
            Role:        mapCasdoorRole(cu),
            Enabled:     !cu.IsForbidden,
            CreatedAt:   parseTime(cu.CreatedTime),
        })
    }
    
    return users, nil
}

// CreateUser creates a user in Casdoor.
func (m *UserManager) CreateUser(ctx context.Context, req *authplugin.CreateUserRequest) (*authplugin.User, error) {
    casdoorUser := &casdoorsdk.User{
        Owner:       m.organization,
        Name:        req.Username,
        CreatedTime: time.Now().Format(time.RFC3339),
        DisplayName: req.DisplayName,
        Email:       req.Email,
        Password:    req.Password,
        IsForbidden: !req.Enabled,
    }
    
    affected, err := m.client.AddUser(casdoorUser)
    if err != nil || !affected {
        return nil, fmt.Errorf("casdoor: create user failed: %w", err)
    }
    
    return m.GetUserByUsername(ctx, req.Username)
}

// UpdateUser updates a user in Casdoor.
func (m *UserManager) UpdateUser(ctx context.Context, userID int, req *authplugin.UpdateUserRequest) (*authplugin.User, error) {
    // Need to fetch user first to get Casdoor ID
    // Then update via Casdoor API
    return nil, fmt.Errorf("update by int ID requires shadow mapping")
}

// DeleteUser deletes a user from Casdoor.
func (m *UserManager) DeleteUser(ctx context.Context, userID int) error {
    // Similar ID mapping challenge
    return fmt.Errorf("delete by int ID requires shadow mapping")
}

// ResetPassword resets password in Casdoor.
func (m *UserManager) ResetPassword(ctx context.Context, userID int, newPassword string) error {
    return fmt.Errorf("password reset requires implementation")
}

// ChangePassword changes password in Casdoor.
func (m *UserManager) ChangePassword(ctx context.Context, userID int, oldPassword, newPassword string) error {
    return fmt.Errorf("password change requires implementation")
}

// UpdateLastLogin is a no-op (Casdoor tracks this internally).
func (m *UserManager) UpdateLastLogin(ctx context.Context, userID int) error {
    return nil
}

// SupportsFeature reports Casdoor user management capabilities.
func (m *UserManager) SupportsFeature(feature authplugin.UserFeature) bool {
    switch feature {
    case authplugin.FeatureUserSearch:
        return true
    case authplugin.FeatureUserGroups:
        return true
    default:
        return false
    }
}

func parseTime(s string) time.Time {
    t, _ := time.Parse(time.RFC3339, s)
    return t
}
```

### 6.3 Casdoor Organization → Tenant Mapping

Casdoor's "Organization" concept maps to llm-gateway's "Tenant". The TenantManager implementation will interact with Casdoor's organization API.

### 6.4 Approval Provider Strategy

Casdoor doesn't have a native approval workflow system. Two options:

1. **Hybrid**: Use native PostgreSQL approval tables even with Casdoor auth
2. **External**: Integrate with a workflow engine (Temporal, etc.)

Initial recommendation: **Option 1 (Hybrid)** - Approval stays in PostgreSQL.

## 7. Migration Path

### 7.1 Phase 1: Foundation (Week 1-2)

1. Create `authplugin` package with interfaces
2. Implement `Registry` and configuration loading
3. Add environment variables
4. Write unit tests for interfaces

**Deliverables**:
- `/pkg/authplugin/*.go` - All interface definitions
- `/pkg/authplugin/registry.go` - Plugin registry
- `/pkg/authplugin/config.go` - Configuration loader
- Tests with 80%+ coverage

### 7.2 Phase 2: Native Implementation (Week 3-4)

1. Extract existing auth logic into `NativeAuthProvider`
2. Extract user CRUD into `NativeUserManager`
3. Extract tenant operations into `NativeTenantManager`
4. Wrap approval config into `NativeApprovalProvider`
5. Update handlers to use registry

**Deliverables**:
- `/pkg/authplugin/native/*.go` - All native implementations
- Refactored `admin/auth.go` to use `NativeAuthProvider`
- Refactored `admin/users.go` to use `NativeUserManager`
- Integration tests confirming zero behavior change

### 7.3 Phase 3: Casdoor Implementation (Week 5-6)

1. Implement `CasdoorAuthProvider`
2. Implement `CasdoorUserManager`
3. Implement `CasdoorTenantManager`
4. Add ID shadow mapping table
5. Write integration tests against Casdoor test instance

**Deliverables**:
- `/pkg/authplugin/casdoor/*.go` - Casdoor implementations
- `/migrations/XXX_add_id_shadow_mapping.sql`
- Docker Compose with Casdoor for local testing
- Integration test suite

### 7.4 Phase 4: Production Deployment (Week 7-8)

1. Add feature flags for gradual rollout
2. Deploy to staging with native provider (verify no regression)
3. Deploy to staging with Casdoor provider (parallel validation)
4. Production deployment strategy
5. Monitoring and alerting setup

**Deliverables**:
- Deployment runbook
- Rollback procedures
- Metrics dashboard
- Alert rules

## 8. Configuration Examples

### 8.1 Native Configuration (Default)

```bash
# .env
AUTH_PROVIDER=native
USER_MANAGER=native
TENANT_MANAGER=native
APPROVAL_PROVIDER=native

DATABASE_DSN=postgres://user:pass@localhost:5432/llmgateway
JWT_SECRET=your-jwt-secret-key
ENCRYPTION_KEY=your-32-byte-encryption-key
```

### 8.2 Casdoor Configuration

```bash
# .env
AUTH_PROVIDER=casdoor
USER_MANAGER=casdoor
TENANT_MANAGER=casdoor
APPROVAL_PROVIDER=native  # Keep approval in native database

# Casdoor connection
CASDOOR_ENDPOINT=https://casdoor.example.com
CASDOOR_CLIENT_ID=your-client-id
CASDOOR_CLIENT_SECRET=your-client-secret
CASDOOR_ORGANIZATION=llm-gateway
CASDOOR_APPLICATION=llm-gateway-app
CASDOOR_TLS_SKIP_VERIFY=false

# Still need database for approval and other features
DATABASE_DSN=postgres://user:pass@localhost:5432/llmgateway
JWT_SECRET=your-jwt-secret-key
```

### 8.3 Hybrid Configuration (Example)

```bash
# Use Casdoor for auth, native for everything else
AUTH_PROVIDER=casdoor
USER_MANAGER=native
TENANT_MANAGER=native
APPROVAL_PROVIDER=native
```

## 9. Compatibility Strategy

### 9.1 JWT Token Compatibility

**Challenge**: Native and Casdoor issue different JWT structures.

**Solution**: Unified token claims via `pkg/identity` verifier:
- Native tokens: issued by `llm-gateway`, verified by legacy path
- Casdoor tokens: issued by `casdoor`, verified by multi-issuer path
- Both map to common `Principal` struct

### 9.2 User ID Mapping

**Challenge**: Casdoor uses string IDs, native uses integer IDs.

**Solution**: Shadow mapping table:

```sql
CREATE TABLE auth_id_mapping (
    internal_id SERIAL PRIMARY KEY,
    external_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    tenant_id TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE(provider, external_id, tenant_id)
);

CREATE INDEX idx_auth_id_mapping_external ON auth_id_mapping(external_id, provider);
```

Resolve flow:
1. Casdoor authentication succeeds → get external ID
2. Look up in `auth_id_mapping` → get internal ID
3. Use internal ID for session/audit/approval tables
4. Create mapping on first login if not exists

### 9.3 Database Schema Evolution

**Principle**: Additive changes only, no breaking migrations.

**Strategy**:
- Keep existing `users` table for native provider
- Add `auth_id_mapping` for Casdoor
- Approval/audit tables reference `internal_id`
- No foreign key constraints that break provider flexibility

## 10. Testing Strategy

### 10.1 Unit Tests

- Interface contract tests (all providers must pass)
- Mock provider for handler testing
- Registry configuration parsing

### 10.2 Integration Tests

- Native provider against test PostgreSQL
- Casdoor provider against Casdoor test instance
- Provider switching without downtime

### 10.3 End-to-End Tests

- Login flow (native)
- Login flow (Casdoor)
- User CRUD operations
- Tenant management
- Approval workflow
- Token refresh and revocation

## 11. Security Considerations

### 11.1 Token Security

- Native: HS256 JWT, rotate secret regularly
- Casdoor: Verify signature against Casdoor public key
- Always verify token expiration
- Implement token revocation via password change epoch

### 11.2 Password Handling

- Native: bcrypt with cost 12+
- Casdoor: Delegated to Casdoor (never store passwords locally)
- Enforce password complexity in both providers
- Support password change enforcement

### 11.3 Audit Logging

- Log all authentication attempts (success/failure)
- Log provider switching events
- Log all admin actions (user create/update/delete)
- Store logs in `routing_audit_log` table

### 11.4 Secrets Management

- Never commit secrets to git
- Use environment variables for configuration
- Rotate Casdoor client secrets regularly
- Encrypt sensitive data at rest (existing encryption key)

## 12. Monitoring and Observability

### 12.1 Metrics

```go
// Prometheus metrics
var (
    authAttempts = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_auth_attempts_total",
            Help: "Total authentication attempts by provider and result",
        },
        []string{"provider", "result"},
    )
    
    authLatency = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "llm_gateway_auth_duration_seconds",
            Help: "Authentication request duration",
            Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
        },
        []string{"provider"},
    )
    
    providerErrors = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_auth_provider_errors_total",
            Help: "Provider errors by type",
        },
        []string{"provider", "error_type"},
    )
)
```

### 12.2 Alerts

```yaml
# alerts.yml
groups:
  - name: authentication
    rules:
      - alert: HighAuthFailureRate
        expr: |
          rate(llm_gateway_auth_attempts_total{result="failure"}[5m]) > 0.5
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High authentication failure rate"
          
      - alert: CasdoorProviderDown
        expr: |
          rate(llm_gateway_auth_provider_errors_total{provider="casdoor"}[5m]) > 0.1
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Casdoor provider experiencing errors"
```

### 12.3 Logging

- Structured logging with slog
- Include provider name in all auth-related logs
- Log provider switching events at INFO level
- Log authentication failures at WARN level
- Never log passwords or tokens

## 13. Future Enhancements

### 13.1 Additional Providers

- **LDAP**: Enterprise directory integration
- **OAuth2/OIDC**: Generic OpenID Connect support
- **SAML**: Enterprise SSO
- **API Key Only**: Simplified auth for API-only deployments

### 13.2 Advanced Features

- **Multi-factor authentication**: OTP, WebAuthn
- **Passwordless**: Magic links, WebAuthn only
- **Session management**: Active session list, remote logout
- **Audit trail**: Detailed access logs
- **Rate limiting**: Per-user, per-tenant
- **IP whitelisting**: Tenant-level IP restrictions

### 13.3 Operational Tools

- **Provider health checks**: Automated provider availability monitoring
- **Failover**: Automatic fallback to native on provider failure
- **Migration tools**: Bulk user import/export
- **Sync jobs**: Periodic user/tenant synchronization

## 14. Documentation Requirements

### 14.1 Architecture Docs

- Update `docs/architecture/authentication.md`
- Add `docs/architecture/auth-plugins.md`
- Sequence diagrams for each provider

### 14.2 Operator Guides

- `docs/ops/auth-provider-setup.md` - Provider configuration
- `docs/ops/migration-to-casdoor.md` - Migration runbook
- `docs/ops/troubleshooting-auth.md` - Common issues

### 14.3 Developer Guides

- `docs/dev/adding-auth-providers.md` - How to add new providers
- `docs/dev/auth-plugin-api.md` - API reference
- Example implementations

## 15. Risks and Mitigations

| Risk | Impact | Probability | Mitigation |
|------|--------|-------------|------------|
| Casdoor API changes | High | Medium | Version pin SDK, integration tests |
| Token compatibility issues | High | Low | Multi-issuer JWT framework already exists |
| Performance regression | Medium | Low | Benchmark tests, gradual rollout |
| Data loss during migration | High | Very Low | Read-only verification period |
| Provider downtime | Medium | Medium | Fallback to native, circuit breaker |
| Incomplete mapping | Medium | Medium | Shadow table + manual reconciliation |

## 16. Success Criteria

1. **Zero regression**: All existing authentication flows work identically
2. **Performance**: Auth latency increase <50ms for Casdoor
3. **Reliability**: 99.9% auth success rate maintained
4. **Flexibility**: Switch providers without code changes
5. **Test coverage**: >85% for all new code
6. **Documentation**: Complete operator and developer guides

## Appendix A: API Examples

### Login with Native Provider

```bash
curl -X POST https://gateway.example.com/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "username": "admin",
    "password": "secure-password",
    "remember_me": true
  }'
```

Response:
```json
{
  "access_token": "eyJhbGci...",
  "token_type": "Bearer",
  "expires_at": "2026-11-15T10:00:00Z",
  "user": {
    "id": 1,
    "username": "admin",
    "tenant_id": "default",
    "role": "super_admin",
    "display_name": "Administrator"
  }
}
```

### Login with Casdoor Provider

Same API, different backend:

```bash
curl -X POST https://gateway.example.com/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "username": "alice@example.com",
    "password": "secure-password"
  }'
```

Response includes Casdoor-issued token:
```json
{
  "access_token": "eyJhbGci...",
  "token_type": "Bearer",
  "expires_at": "2026-11-15T10:00:00Z",
  "refresh_token": "refresh_token_here",
  "user": {
    "external_id": "casdoor-user-uuid",
    "username": "alice",
    "tenant_id": "tenant-org",
    "role": "tenant_admin",
    "display_name": "Alice Smith",
    "email": "alice@example.com"
  }
}
```

## Appendix B: Database Schema

### ID Mapping Table

```sql
-- Shadow mapping for external user IDs
CREATE TABLE auth_id_mapping (
    internal_id SERIAL PRIMARY KEY,
    external_id TEXT NOT NULL,
    provider TEXT NOT NULL CHECK (provider IN ('native', 'casdoor', 'ldap', 'oidc')),
    tenant_id TEXT NOT NULL,
    username TEXT,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE(provider, external_id, tenant_id),
    UNIQUE(provider, username, tenant_id)
);

CREATE INDEX idx_auth_id_mapping_external ON auth_id_mapping(external_id, provider);
CREATE INDEX idx_auth_id_mapping_internal ON auth_id_mapping(internal_id);
CREATE INDEX idx_auth_id_mapping_username ON auth_id_mapping(username, provider);

-- Trigger to update updated_at
CREATE OR REPLACE FUNCTION update_auth_id_mapping_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER auth_id_mapping_updated_at
    BEFORE UPDATE ON auth_id_mapping
    FOR EACH ROW
    EXECUTE FUNCTION update_auth_id_mapping_updated_at();
```

### Provider Configuration Table (Optional)

```sql
-- Runtime provider configuration (alternative to env vars)
CREATE TABLE auth_provider_config (
    id SERIAL PRIMARY KEY,
    provider_name TEXT NOT NULL UNIQUE,
    provider_type TEXT NOT NULL,
    enabled BOOLEAN DEFAULT true,
    config JSONB NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

-- Example row:
INSERT INTO auth_provider_config (provider_name, provider_type, config) VALUES
('casdoor-main', 'casdoor', '{
    "endpoint": "https://casdoor.example.com",
    "organization": "llm-gateway",
    "application": "llm-gateway-app"
}');
```

---

**End of Design Document**

**Next Steps**:
1. Review with architecture team
2. Prototype Phase 1 (interfaces)
3. Validate against existing code
4. Begin Phase 2 implementation
