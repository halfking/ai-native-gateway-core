# LLM Gateway Plugin Architecture - Migration Roadmap

## Overview

This document provides a step-by-step migration path from the current monolithic authentication implementation to the plugin-based architecture. The migration is designed to be gradual, reversible, and zero-downtime.

## Current State Analysis

### Database Tables
- **users**: Core user authentication table (id, username, password_hash, role, tenant_id)
- **tenants**: Organization/tenant table (code, name, status)
- **api_keys**: API key authentication (key_hash, application_id, tenant_id)
- **routing_audit_log**: Audit trail for all admin actions
- **approval_queue**: Session approval workflow state

### Key Code Locations
- `admin/auth.go` - Login, JWT signing, password verification (388 lines)
- `admin/users.go` - User CRUD operations (745 lines)
- `admin/approval_config_handler.go` - Approval configuration (493 lines)
- `domain/tenant.go` - Tenant context struct (22 lines)
- `middleware/auth_mw.go` - Global API key authentication (303 lines)
- `pkg/identity/verifier.go` - Multi-issuer JWT verification (351 lines)

### Dependencies
- bcrypt for password hashing
- pgx/v5 for PostgreSQL
- JWT (HS256) via pkg/identity
- Session cookies (llmgw_session)

## Migration Phases

### Phase 0: Preparation (Week 0)
**Goal**: Set up foundation without changing existing code.

#### Tasks
1. **Create package structure**
   ```bash
   mkdir -p pkg/authplugin/{native,casdoor,test}
   ```

2. **Define interfaces** (DONE)
   - ✅ interfaces.go - Core interfaces
   - ✅ registry.go - Plugin registry
   - ✅ config.go - Configuration loading

3. **Write contract tests**
   ```go
   // pkg/authplugin/test/contract_test.go
   // Tests that all providers must pass
   ```

4. **Documentation**
   - Architecture decision record (ADR)
   - API documentation
   - Migration guide

#### Acceptance Criteria
- [ ] All interface files compile
- [ ] Contract tests defined (even if no implementations yet)
- [ ] Documentation reviewed and approved
- [ ] No changes to existing code

---

### Phase 1: Native Implementation (Weeks 1-2)
**Goal**: Extract existing logic into NativeAuthProvider without behavior changes.

#### Step 1.1: NativeAuthProvider (Week 1)

**Create**: `pkg/authplugin/native/auth_provider.go`

```go
package native

import (
    "context"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

type AuthProvider struct {
    db        *pgxpool.Pool
    jwtSecret string
    // ... other fields from admin/auth.go
}

// Extract logic from admin/auth.go handleLogin()
func (p *AuthProvider) Authenticate(ctx context.Context, req *authplugin.AuthRequest) (*authplugin.AuthResult, error) {
    // Copy logic from handleLogin (lines 174-366)
    // 1. Query users table
    // 2. Verify bcrypt password
    // 3. Check tenant status
    // 4. Generate JWT via SignToken
    // 5. Update last_login_at
}

// Extract logic from admin/auth.go AdminMiddleware
func (p *AuthProvider) ValidateToken(ctx context.Context, token string) (*authplugin.TokenClaims, error) {
    // Delegate to existing VerifyToken (admin/jwt.go)
}
```

**Testing Strategy**:
```go
// pkg/authplugin/native/auth_provider_test.go
func TestAuthProvider_Authenticate_MatchesOriginal(t *testing.T) {
    // Compare NativeAuthProvider output with original handleLogin
    // Same input → same JWT token
}
```

**Validation**:
- [ ] Unit tests pass (85%+ coverage)
- [ ] Integration test against test database
- [ ] Behavior matches existing handleLogin exactly

#### Step 1.2: NativeUserManager (Week 1-2)

**Create**: `pkg/authplugin/native/user_manager.go`

```go
package native

type UserManager struct {
    db *pgxpool.Pool
}

// Extract from admin/users.go listUsers (lines 322-356)
func (m *UserManager) ListUsers(ctx context.Context, filter *authplugin.UserFilter) ([]*authplugin.User, error) {
    // Copy exact SQL query logic
}

// Extract from admin/users.go createUser (lines 358-435)
func (m *UserManager) CreateUser(ctx context.Context, req *authplugin.CreateUserRequest) (*authplugin.User, error) {
    // Copy validation + INSERT logic
}

// Similar for UpdateUser, DeleteUser, ResetPassword, ChangePassword
```

**Validation**:
- [ ] All user CRUD operations work identically
- [ ] Password complexity validation preserved
- [ ] Audit logging still triggered
- [ ] Tenant admin isolation preserved

#### Step 1.3: NativeTenantManager (Week 2)

**Create**: `pkg/authplugin/native/tenant_manager.go`

```go
package native

type TenantManager struct {
    db *pgxpool.Pool
}

// Currently scattered across multiple files, centralize here
func (m *TenantManager) GetTenant(ctx context.Context, tenantID string) (*authplugin.Tenant, error) {
    // Query tenants table
}

func (m *TenantManager) CreateTenant(ctx context.Context, req *authplugin.CreateTenantRequest) (*authplugin.Tenant, error) {
    // INSERT into tenants
}
```

**Validation**:
- [ ] Tenant queries work
- [ ] Status checks work (disabled tenants blocked)
- [ ] Policy enforcement works

#### Step 1.4: NativeApprovalProvider (Week 2)

**Create**: `pkg/authplugin/native/approval_provider.go`

```go
package native

type ApprovalProvider struct {
    configManager *approval.ConfigManager // Wrap existing
}

// Thin wrapper around existing domains/sessionaudit logic
func (p *ApprovalProvider) GetConfig(ctx context.Context, tenantID string) (*authplugin.ApprovalConfig, error) {
    // Delegate to existing ConfigManager
}
```

**Validation**:
- [ ] Approval config CRUD works
- [ ] Approvers management works
- [ ] Rules management works

#### Phase 1 Acceptance Criteria
- [ ] All native implementations pass contract tests
- [ ] Integration tests against PostgreSQL pass
- [ ] Zero behavior change (verified via comparison tests)
- [ ] Code coverage ≥ 85%

---

### Phase 2: Handler Refactoring (Week 3)
**Goal**: Update handlers to use registry instead of direct database access.

#### Step 2.1: Create Global Registry

**Modify**: `cmd/gateway/main.go`

```go
// Add near line 100 (after database initialization)
func initAuthRegistry(db *pgxpool.Pool, jwtSecret string, encKey []byte) (*authplugin.Registry, error) {
    config, err := authplugin.LoadConfigFromEnv()
    if err != nil {
        return nil, fmt.Errorf("load auth config: %w", err)
    }
    
    registry := authplugin.NewRegistry(config)
    
    // Register native providers
    nativeAuth := native.NewAuthProvider(db, jwtSecret)
    nativeUser := native.NewUserManager(db)
    nativeTenant := native.NewTenantManager(db)
    nativeApproval := native.NewApprovalProvider(db)
    
    registry.RegisterAuthProvider(nativeAuth)
    registry.RegisterUserManager(nativeUser)
    registry.RegisterTenantManager(nativeTenant)
    registry.RegisterApprovalProvider(nativeApproval)
    
    return registry, nil
}
```

#### Step 2.2: Refactor Login Handler

**Modify**: `admin/auth.go handleLogin()`

```go
// OLD (lines 174-366)
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
    // Direct database queries...
}

// NEW
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }

    // Rate limiting (keep existing logic)
    clientIP := clientIPFromRequest(r)
    if !loginLimiter.Allow(clientIP) {
        h.auditLog("unknown", "authentication.rate_limited", "user", 0, map[string]any{"ip": clientIP})
        writeError(w, http.StatusTooManyRequests, "too many login attempts")
        return
    }

    var req loginRequest
    if err := readJSON(r, &req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid request body")
        return
    }

    // Captcha verification (keep existing logic)
    if !h.verifyLoginCaptcha(w, clientIP, req.CaptchaID, req.CaptchaCode) {
        return
    }

    // NEW: Use AuthProvider instead of direct SQL
    authProvider, err := h.registry.GetAuthProvider()
    if err != nil {
        slog.Error("failed to get auth provider", "error", err)
        writeError(w, http.StatusInternalServerError, "authentication service unavailable")
        return
    }

    authReq := &authplugin.AuthRequest{
        Username:    req.Username,
        Password:    [REDACTED:secret],
        TenantID:    "", // Provider resolves
        RememberMe:  req.RememberMe,
        CaptchaID:   req.CaptchaID,
        CaptchaCode: req.CaptchaCode,
        IPAddress:   clientIP,
        UserAgent:   r.UserAgent(),
    }

    result, err := authProvider.Authenticate(r.Context(), authReq)
    if err != nil {
        // Map errors to HTTP responses
        h.auditLog(req.Username, "authentication.login_failed", "user", 0, 
            fmt.Sprintf("reason=%s ip=%s", err.Error(), clientIP))
        recordLoginFailure(clientIP)
        
        switch err {
        case authplugin.ErrInvalidCredentials:
            writeError(w, http.StatusUnauthorized, "Invalid credentials")
        case authplugin.ErrUserDisabled:
            writeError(w, http.StatusForbidden, "User account is [REDACTED:account]")
        case authplugin.ErrTenantDisabled:
            writeError(w, http.StatusForbidden, "Tenant is disabled")
        default:
            writeError(w, http.StatusInternalServerError, "Authentication failed")
        }
        return
    }

    // Success: audit log, reset rate limit, set cookie
    h.auditLog(result.Username, "authentication.login", "user", result.UserID, 
        fmt.Sprintf("method=%s role=%s tenant=%s ip=%s", authProvider.Name(), result.Role, result.TenantID, clientIP))
    loginLimiter.Reset(clientIP)
    clearLoginFailures(clientIP)

    // Shadow recording (cross-project identity)
    identity.RecordShadow(authProvider.Name(), result.UserID, result.TenantID, result.DisplayName, result.Email)

    // Set cookie and return response
    setSessionCookieRemember(w, r, result.Token, result.ExpiresAt, req.RememberMe)
    writeJSON(w, http.StatusOK, map[string]any{
        "access_token": token,
        "token_type":   result.TokenType,
        "expires_at":   result.ExpiresAt.Format(time.RFC3339),
        "refresh_token": result.RefreshToken, // May be empty for native provider
        "user": map[string]any{
            "id":                   result.UserID,
            "external_id":          result.ExternalID,
            "tenant_id":            result.TenantID,
            "username":             [REDACTED:account],
            "display_name":         result.DisplayName,
            "email":                result.Email,
            "role":                 result.Role,
            "enabled":              result.Enabled,
            "must_change_password": [REDACTED:secret],
        },
    })
}
```

#### Step 2.3: Refactor User Handlers

**Modify**: `admin/users.go handleUsers()`

Similar pattern - replace direct SQL with UserManager interface calls.

```go
func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
    userManager, err := h.registry.GetUserManager()
    if err != nil {
        writeInternalErr(w, "user manager unavailable", err)
        return
    }

    filter := &authplugin.UserFilter{
        Limit: 100, // Default limit
    }

    // Tenant admin isolation
    if IsTenantAdmin(r) {
        filter.TenantID = GetTenantID(r)
    }

    users, err := userManager.ListUsers(r.Context(), filter)
    if err != nil {
        writeInternalErr(w, "list users failed", err)
        return
    }

    writeJSON(w, http.StatusOK, users)
}
```

#### Phase 2 Acceptance Criteria
- [ ] All handlers refactored to use registry
- [ ] No direct database queries in handlers
- [ ] All integration tests pass
- [ ] API responses unchanged (verified via snapshot tests)
- [ ] Performance benchmark shows <5% regression

---

### Phase 3: Casdoor Implementation (Weeks 4-5)
**Goal**: Implement Casdoor provider and enable provider switching.

#### Step 3.1: Casdoor SDK Integration (Week 4)

**Add dependency**:
```bash
go get github.com/casdoor/casdoor-go-sdk/casdoorsdk@latest
```

**Create**: `pkg/authplugin/casdoor/auth_provider.go`

```go
package casdoor

import (
    casdoorsdk "github.com/casdoor/casdoor-go-sdk/casdoorsdk"
    "github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

type AuthProvider struct {
    client       *casdoorsdk.Client
    organization string
    application  string
    idMapper     *IDMapper // For external→internal ID mapping
}

func NewAuthProvider(config *authplugin.CasdoorConfig, db *pgxpool.Pool) (*AuthProvider, error) {
    authConfig := &casdoorsdk.AuthConfig{
        Endpoint:         config.Endpoint,
        ClientId:         config.ClientID,
        ClientSecret:     config.ClientSecret,
        OrganizationName: config.Organization,
        ApplicationName:  config.Application,
    }
    
    client := casdoorsdk.NewClient(authConfig)
    
    return &AuthProvider{
        client:       client,
        organization: config.Organization,
        application:  config.Application,
        idMapper:     NewIDMapper(db),
    }, nil
}

func (p *AuthProvider) Authenticate(ctx context.Context, req *authplugin.AuthRequest) (*authplugin.AuthResult, error) {
    // Call Casdoor OAuth2 token endpoint
    token, err := p.client.GetOAuthToken(req.Username, req.Password)
    if err != nil {
        return nil, authplugin.ErrInvalidCredentials
    }

    // Parse JWT and extract claims
    claims, err := p.client.ParseJwtToken(token.AccessToken)
    if err != nil {
        return nil, authplugin.ErrInvalidToken
    }

    // Get full user details
    user, err := p.client.GetUser(claims.Name)
    if err != nil {
        return nil, fmt.Errorf("get user details: %w", err)
    }

    // Map external ID to internal ID (or create new mapping)
    internalID, err := p.idMapper.MapOrCreate(ctx, "casdoor", user.Id, user.Owner, req.Username)
    if err != nil {
        return nil, fmt.Errorf("id mapping failed: %w", err)
    }

    return &authplugin.AuthResult{
        Success:      true,
        UserID:       internalID,
        ExternalID:   user.Id,
        Username:     user.Name,
        TenantID:     user.Owner,
        Role:         mapCasdoorRole(user),
        DisplayName:  user.DisplayName,
        Email:        user.Email,
        Enabled:      !user.IsForbidden,
        Token:        token.AccessToken,
        TokenType:    "Bearer",
        ExpiresAt:    time.Unix(token.ExpiresIn, 0),
        RefreshToken: token.RefreshToken,
    }, nil
}
```

#### Step 3.2: ID Mapping Table (Week 4)

**Create migration**: `migrations/XXX_add_auth_id_mapping.sql`

```sql
-- Up migration
CREATE TABLE IF NOT EXISTS auth_id_mapping (
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

-- Trigger for updated_at
CREATE OR REPLACE FUNCTION update_auth_id_mapping_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER auth_id_mapping_updated_at
    BEFORE UPDATE ON auth_id_mapping
    FOR EACH ROW
    EXECUTE FUNCTION update_auth_id_mapping_timestamp();

-- Down migration
DROP TRIGGER IF EXISTS auth_id_mapping_updated_at ON auth_id_mapping;
DROP FUNCTION IF EXISTS update_auth_id_mapping_timestamp();
DROP TABLE IF EXISTS auth_id_mapping;
```

**Create**: `pkg/authplugin/casdoor/id_mapper.go`

```go
package casdoor

type IDMapper struct {
    db *pgxpool.Pool
}

func (m *IDMapper) MapOrCreate(ctx context.Context, provider, externalID, tenantID, username string) (int, error) {
    // Try to find existing mapping
    var internalID int
    err := m.db.QueryRow(ctx, `
        SELECT internal_id FROM auth_id_mapping
        WHERE provider = $1 AND external_id = $2 AND tenant_id = $3
    `, provider, externalID, tenantID).Scan(&internalID)
    
    if err == nil {
        return internalID, nil
    }
    
    if err != pgx.ErrNoRows {
        return 0, fmt.Errorf("query mapping: %w", err)
    }
    
    // Create new mapping
    err = m.db.QueryRow(ctx, `
        INSERT INTO auth_id_mapping (external_id, provider, tenant_id, username)
        VALUES ($1, $2, $3, $4)
        RETURNING internal_id
    `, externalID, provider, tenantID, username).Scan(&internalID)
    
    if err != nil {
        return 0, fmt.Errorf("create mapping: %w", err)
    }
    
    return internalID, nil
}
```

#### Step 3.3: Casdoor User/Tenant Managers (Week 5)

Similar implementations for CasdoorUserManager and CasdoorOrganizationManager.

#### Phase 3 Acceptance Criteria
- [ ] Casdoor provider implements all interfaces
- [ ] ID mapping works correctly
- [ ] Integration tests against Casdoor test instance pass
- [ ] Can switch between native and Casdoor via env vars

---

### Phase 4: Production Deployment (Weeks 6-7)
**Goal**: Deploy to production with gradual rollout.

#### Step 4.1: Staging Validation (Week 6)

1. **Deploy with native provider** (no behavior change)
   ```bash
   AUTH_PROVIDER=native
   USER_MANAGER=native
   TENANT_MANAGER=native
   APPROVAL_PROVIDER=native
   ```
   
   **Validation**:
   - [ ] All existing tests pass
   - [ ] Login flow works
   - [ ] User CRUD works
   - [ ] Approval workflow works
   - [ ] Performance benchmarks meet targets

2. **Deploy with Casdoor provider** (parallel validation)
   ```bash
   AUTH_PROVIDER=casdoor
   USER_MANAGER=casdoor
   TENANT_MANAGER=casdoor
   APPROVAL_PROVIDER=native  # Hybrid: keep approval in PostgreSQL
   ```
   
   **Validation**:
   - [ ] Casdoor authentication works
   - [ ] User sync works
   - [ ] Tenant mapping works
   - [ ] Existing PostgreSQL sessions still valid
   - [ ] Can switch back to native without data loss

#### Step 4.2: Production Deployment Strategy (Week 7)

**Option A: Canary Deployment**
- Deploy to 10% of traffic with native provider
- Monitor for 24 hours
- Increase to 50%, then 100%

**Option B: Blue-Green Deployment**
- Deploy full stack with native provider
- Validate completely
- Switch DNS/load balancer

**Option C: Feature Flag**
```go
// Per-tenant provider override
func (r *Registry) GetAuthProviderForTenant(tenantID string) (AuthProvider, error) {
    // Check feature flag or tenant-specific config
    if isFeatureEnabled(tenantID, "casdoor_auth") {
        return r.authProviders["casdoor"], nil
    }
    return r.authProviders["native"], nil
}
```

#### Step 4.3: Monitoring and Alerts

**Metrics**:
```yaml
# Prometheus metrics
- llm_gateway_auth_attempts_total{provider, result}
- llm_gateway_auth_duration_seconds{provider}
- llm_gateway_auth_provider_errors_total{provider, error_type}
- llm_gateway_auth_id_mapping_cache_hits_total
- llm_gateway_auth_id_mapping_cache_misses_total
```

**Alerts**:
```yaml
# Alert on high failure rate
- alert: HighAuthFailureRate
  expr: rate(llm_gateway_auth_attempts_total{result="failure"}[5m]) > 0.2
  for: 5m
  severity: warning

# Alert on provider errors
- alert: AuthProviderErrors
  expr: rate(llm_gateway_auth_provider_errors_total[5m]) > 0.1
  for: 5m
  severity: critical

# Alert on performance regression
- alert: AuthLatencyHigh
  expr: histogram_quantile(0.95, llm_gateway_auth_duration_seconds) > 1.0
  for: 5m
  severity: warning
```

#### Phase 4 Acceptance Criteria
- [ ] Deployed to production
- [ ] Zero downtime during deployment
- [ ] All metrics within acceptable ranges
- [ ] Alerts configured and tested
- [ ] Rollback plan tested

---

## Rollback Procedures

### Rollback from Casdoor to Native

**Step 1: Update environment variables**
```bash
AUTH_PROVIDER=native
USER_MANAGER=native
TENANT_MANAGER=native
```

**Step 2: Restart application**
```bash
kubectl rollout restart deployment/llm-gateway
```

**Step 3: Verify**
- Check login works with PostgreSQL users
- Check user CRUD operations
- Monitor error rates

**Time**: ~5 minutes

### Rollback from Plugin Architecture to Monolithic

This should NOT be necessary if Phase 2 was done correctly (native implementation matches existing behavior exactly). But if needed:

**Step 1: Revert code to pre-migration commit**
```bash
git revert <migration-commit-range>
```

**Step 2: Redeploy**

**Step 3: Database cleanup** (optional, if auth_id_mapping was created)
```sql
DROP TABLE IF EXISTS auth_id_mapping;
```

**Time**: ~15 minutes + deployment time

---

## Testing Checklist

### Unit Tests
- [ ] All interface contract tests pass
- [ ] Native provider tests pass
- [ ] Casdoor provider tests pass
- [ ] Registry tests pass
- [ ] Config loading tests pass

### Integration Tests
- [ ] Native authentication flow
- [ ] Casdoor authentication flow
- [ ] User CRUD operations (native)
- [ ] User CRUD operations (Casdoor)
- [ ] Tenant management (native)
- [ ] Tenant management (Casdoor)
- [ ] Approval workflow (native)
- [ ] ID mapping creation and lookup
- [ ] Provider switching

### End-to-End Tests
- [ ] Complete login flow (browser)
- [ ] Complete login flow (API)
- [ ] Password change flow
- [ ] User creation by admin
- [ ] Tenant admin isolation
- [ ] Approval request/approve/reject
- [ ] Session revocation after password change

### Performance Tests
- [ ] Authentication latency (p50, p95, p99)
- [ ] User list query performance
- [ ] ID mapping lookup performance
- [ ] Concurrent authentication load test

### Security Tests
- [ ] Password complexity enforcement
- [ ] Brute force protection (rate limiting)
- [ ] Session fixation prevention
- [ ] CSRF protection
- [ ] SQL injection prevention
- [ ] JWT signature validation
- [ ] Token expiration enforcement

---

## Risk Mitigation

| Risk | Mitigation |
|------|-----------|
| Data loss during migration | Read-only verification period, full backups |
| Performance regression | Benchmark tests before/after, gradual rollout |
| Casdoor downtime | Fallback to native provider via circuit breaker |
| JWT incompatibility | Multi-issuer verification already supports both |
| ID mapping conflicts | UNIQUE constraints prevent duplicates |
| Incomplete rollback | Tagged release points, tested rollback procedures |

---

## Success Metrics

- **Zero data loss**: All user/tenant/approval data intact
- **Zero downtime**: No service interruption during migration
- **Performance**: <50ms increase in auth latency
- **Reliability**: 99.9% auth success rate maintained
- **Flexibility**: Can switch providers via env vars in <5 minutes
- **Test coverage**: ≥85% for all new code
- **Documentation**: Complete operator and developer guides

---

**Next Steps**:
1. Get architectural approval for Phase 1
2. Set up development environment with Casdoor test instance
3. Begin Phase 1 implementation
4. Schedule weekly progress reviews
