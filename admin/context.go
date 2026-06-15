package admin

import (
	"context"
	"net/http"
)

// authContextKey is the context key for AuthContext.
type authContextKey struct{}

// AuthContext holds the authenticated identity for the current request.
type AuthContext struct {
	UserID   int    // 0 for legacy admin key auth
	TenantID string // tenant_id from JWT or "default" for legacy
	Username string // username from JWT or "admin" for legacy
	Role     string // super_admin | tenant_admin | admin_key
	IsJWT    bool   // true if authenticated via JWT
}

// SetAuthContext stores the AuthContext in the request context.
func SetAuthContext(r *http.Request, auth *AuthContext) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authContextKey{}, auth))
}

// GetAuthContext retrieves the AuthContext from the request context.
// Returns nil if no auth context is set.
func GetAuthContext(r *http.Request) *AuthContext {
	v, _ := r.Context().Value(authContextKey{}).(*AuthContext)
	return v
}

// GetTenantID returns the tenant_id from the request's AuthContext, or "default".
func GetTenantID(r *http.Request) string {
	if auth := GetAuthContext(r); auth != nil && auth.TenantID != "" {
		return auth.TenantID
	}
	return "default"
}

// IsTenantAdmin returns true if the request is authenticated as a tenant_admin.
func IsTenantAdmin(r *http.Request) bool {
	auth := GetAuthContext(r)
	return auth != nil && auth.Role == "tenant_admin"
}

// IsSuperAdminOrLegacy returns true if the request has full admin access.
func IsSuperAdminOrLegacy(r *http.Request) bool {
	auth := GetAuthContext(r)
	if auth == nil {
		return false
	}
	return auth.Role == "super_admin" || auth.Role == "admin_key"
}

// EffectiveTenantID returns the tenant_id to use in SQL queries.
func EffectiveTenantID(r *http.Request) string {
	if IsTenantAdmin(r) {
		return GetTenantID(r)
	}
	return "default"
}

// EffectiveTenantIDAll returns empty string for super_admin (meaning query all tenants),
// or the tenant's own ID for tenant_admin. Used for dashboard/summary queries.
func EffectiveTenantIDAll(r *http.Request) string {
	if IsTenantAdmin(r) {
		return GetTenantID(r)
	}
	return "" // empty string means query all tenants
}
