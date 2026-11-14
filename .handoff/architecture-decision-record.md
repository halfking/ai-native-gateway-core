# Authentication Plugin Architecture - Architecture Decision Record (ADR)

**Date**: 2026-11-14  
**Status**: Proposed  
**Deciders**: Architecture Team, Backend Team  

## Context

llm-gateway-go currently implements authentication, user management, and tenant management using a monolithic approach with direct PostgreSQL access. All authentication logic is hardcoded in `admin/auth.go` and `admin/users.go`, making it impossible to switch identity providers without code changes.

### Current Problems

1. **Vendor Lock-in**: Cannot integrate with enterprise identity providers (Casdoor, LDAP, SAML)
2. **Code Duplication**: Auth logic repeated across multiple handlers
3. **Testing Difficulty**: Cannot mock authentication for unit tests
4. **Maintenance Burden**: Changes require modifying multiple files
5. **Migration Risk**: Adding new providers requires risky refactoring

### Business Requirements

- Support Casdoor for unified identity management across products
- Enable enterprise SSO integrations
- Maintain backward compatibility with existing PostgreSQL-based auth
- Zero downtime during provider migration
- Ability to switch providers per-tenant or per-deployment

## Decision

We will implement a **plugin-based authentication architecture** with the following characteristics:

### 1. Interface-Based Abstraction

Define four core interfaces:
- `AuthProvider` - Authentication operations (login, token validation)
- `UserManager` - User lifecycle management (CRUD)
- `TenantManager` - Tenant/organization management
- `ApprovalProvider` - Approval workflow configuration

### 2. Plugin Registry

Central registry that:
- Manages provider lifecycle
- Enables runtime provider selection
- Supports configuration-driven switching
- Thread-safe for concurrent access

### 3. Native Implementation First

Extract existing PostgreSQL logic into `NativeAuthProvider`, `NativeUserManager`, etc. This:
- Proves the interface design works
- Ensures zero behavior change
- Provides fallback option
- Enables incremental migration

### 4. Provider-Agnostic Handlers

Refactor handlers to use interfaces instead of direct database access:
```go
// OLD
func (h *Handler) handleLogin(...) {
    var user struct{...}
    h.db.QueryRow(ctx, "SELECT ... FROM users WHERE username = $1", ...).Scan(...)
}

// NEW
func (h *Handler) handleLogin(...) {
    authProvider, _ := h.registry.GetAuthProvider()
    result, err := authProvider.Authenticate(ctx, &authplugin.AuthRequest{...})
}
```

### 5. ID Mapping Layer

For external providers (Casdoor), map external IDs to internal integer IDs via `auth_id_mapping` table. This:
- Preserves existing foreign key relationships
- Enables hybrid deployments
- Supports multiple providers simultaneously

## Alternatives Considered

### Alternative 1: Full Casdoor Migration (Rejected)

**Approach**: Completely replace PostgreSQL users table with Casdoor.

**Pros**:
- Simpler architecture (single source of truth)
- No ID mapping needed
- Fewer tables to maintain

**Cons**:
- ❌ Breaking change for all existing deployments
- ❌ Forces everyone to use Casdoor
- ❌ Difficult rollback
- ❌ High migration risk

**Verdict**: Too risky, violates backward compatibility requirement.

### Alternative 2: Dual-Write Approach (Rejected)

**Approach**: Write user data to both PostgreSQL and Casdoor simultaneously.

**Pros**:
- Gradual migration possible
- Can fall back to PostgreSQL

**Cons**:
- ❌ Data consistency issues (what if one write fails?)
- ❌ Complex transaction management
- ❌ Temporary storage overhead
- ❌ Difficult to test

**Verdict**: Dual-write complexity outweighs benefits.

### Alternative 3: Adapter Pattern Only (Rejected)

**Approach**: Create adapters without a central registry.

**Pros**:
- Simpler than full plugin system
- Fewer moving parts

**Cons**:
- ❌ No central configuration
- ❌ Handlers need provider-specific logic
- ❌ Hard to test (can't mock providers easily)
- ❌ Doesn't support runtime switching

**Verdict**: Doesn't meet flexibility requirement.

### Alternative 4: Microservice Split (Rejected)

**Approach**: Extract auth into a separate microservice.

**Pros**:
- Independent scaling
- Clear separation of concerns

**Cons**:
- ❌ Massive architectural change
- ❌ Increases operational complexity
- ❌ Network latency for every auth check
- ❌ Doesn't solve the "support multiple providers" problem

**Verdict**: Overengineering for current needs.

## Consequences

### Positive

1. **Flexibility**: Can switch providers via environment variables
2. **Testability**: Mock providers for unit tests
3. **Extensibility**: Easy to add new providers (LDAP, SAML, etc.)
4. **Safety**: Gradual migration with rollback capability
5. **Enterprise-Ready**: Supports SSO and external identity management
6. **Zero Breaking Changes**: Existing deployments continue working

### Negative

1. **Initial Complexity**: More abstractions = more code to understand
2. **ID Mapping Overhead**: Extra database lookup for external providers (~1-2ms)
3. **Testing Burden**: Must test all provider implementations
4. **Migration Effort**: ~6-7 weeks of development + testing

### Neutral

1. **Learning Curve**: Team must learn plugin architecture patterns
2. **Documentation Needs**: Comprehensive guides for operators and developers
3. **Monitoring**: New metrics for provider health and performance

## Implementation Plan

See [migration-roadmap.md](./migration-roadmap.md) for detailed phases.

**Summary**:
- Phase 0: Preparation (interfaces, tests, docs) - Week 0
- Phase 1: Native implementation - Weeks 1-2
- Phase 2: Handler refactoring - Week 3
- Phase 3: Casdoor implementation - Weeks 4-5
- Phase 4: Production deployment - Weeks 6-7

## Validation

### Must-Haves (Non-Negotiable)

- [ ] Zero data loss during migration
- [ ] Zero downtime during deployment
- [ ] Existing auth flows work identically
- [ ] Can rollback in <15 minutes
- [ ] Performance regression <50ms

### Should-Haves (Important)

- [ ] Test coverage ≥85%
- [ ] Complete operator documentation
- [ ] Monitoring and alerting setup
- [ ] Casdoor integration tested

### Could-Haves (Nice to Have)

- [ ] Per-tenant provider selection
- [ ] Admin UI for provider switching
- [ ] Provider health dashboard
- [ ] Automated provider failover

## Future Enhancements

Once the plugin architecture is stable:

1. **Additional Providers**
   - LDAP for enterprise directory integration
   - OAuth2/OIDC for generic SSO
   - SAML for enterprise SSO

2. **Advanced Features**
   - Multi-factor authentication (MFA)
   - Passwordless authentication (WebAuthn)
   - Biometric authentication
   - Session management UI

3. **Operational Tools**
   - Provider health checks
   - Automated failover
   - Bulk user import/export
   - Sync jobs

## References

- [Plugin Architecture Design](./plugin-architecture-design.md)
- [Migration Roadmap](./migration-roadmap.md)
- Existing code:
  - `admin/auth.go` - Current authentication logic
  - `admin/users.go` - Current user management
  - `pkg/identity/verifier.go` - Existing JWT verification
- Casdoor: https://casdoor.org/docs/overview

## Approval

- [ ] Architecture Team Lead
- [ ] Backend Team Lead
- [ ] Security Team
- [ ] DevOps/SRE Team
- [ ] Product Owner

---

**Questions? Contact**: Architecture Team
