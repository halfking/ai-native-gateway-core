package admin

import (
	"context"
	"net/http"
)

// authContextKey is the context key for AuthContext.
type authContextKey struct{}

// AuthContext holds the authenticated identity for the current request.
type AuthContext struct {
	UserID             int    // 0 for legacy admin key auth
	TenantID           string // tenant_id from JWT or "default" for legacy
	Username           string // username from JWT or "admin" for legacy
	Role               string // super_admin | tenant_admin | admin_key
	IsJWT              bool   // true if authenticated via JWT
	MustChangePassword bool
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

// RequireSuperAdminForWrite returns true and writes a 403 response if the
// request is from a tenant_admin trying to perform a write operation.
// super_admin and legacy admin_key can always write.
func RequireSuperAdminForWrite(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return false // read methods are always allowed
	}
	if IsTenantAdmin(r) {
		writeError(w, http.StatusForbidden, "tenant_admin has read-only access; write operations require super_admin")
		return true
	}
	return false
}

// ApprovalCallerTenantID（2026-06-27 audit fix）返回传给 ApprovalManager
// 的 tenant_id 参数，用于决定是否触发跨租户 RLS bypass：
//   - tenant_admin → 返回自己 tenant_id，触发 RLS 兜底
//   - super_admin / admin_key → 返回空字符串，让 ApprovalManager 跳过
//     应用层 tenant 校验（事务内不设 GUC，RLS 也不限制）
//
// 这样 superadmin 才能跨租户审批，tenant_admin 仍被自己 tenant 锁住。
func ApprovalCallerTenantID(r *http.Request) string {
	if IsSuperAdminOrLegacy(r) {
		return "" // 不传 tenant → ApprovalManager 内部无 RLS 过滤
	}
	return GetTenantID(r)
}
