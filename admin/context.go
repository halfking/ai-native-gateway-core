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
	Issuer             string // original verified issuer
	Subject            string // original issuer's subject
	CanonicalUserID    string // explicit shared identity; empty for legacy
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

// IsChannelUser returns true if the request is authenticated as a channel user (W4.3).
func IsChannelUser(r *http.Request) bool {
	auth := GetAuthContext(r)
	return auth != nil && auth.Role == "channel"
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

// formatAnomalyTenantID 是格式异常监控读面的租户收窄值
//（2026-11-27 非管理员菜单重构轮）：
//
//	super_admin / admin_key → ""（全平台，bypass RLS 的授权前提不变）
//	tenant_admin / role=user → 本租户（RLS / WHERE tenant_id 收窄）
//
// ★ 为什么不直接用 EffectiveTenantIDAll：它对普通 role=user 返回空串
//   （空串 = bypass RLS = 全平台数据）。中间件本轮已放行 user 档读，
//   若沿用 EffectiveTenantIDAll，user 会拿到全平台异常明细 ⇒ 越权读。
//   user 档收本租户（与 piTenantFilter 同口径；表无 owner 列 ⇒ 不虚构
//   本人级过滤）。
func formatAnomalyTenantID(r *http.Request) string {
	if IsSuperAdminOrLegacy(r) {
		return ""
	}
	return GetTenantID(r)
}

// RequireSuperAdminForWrite returns true and writes a 403 response if the
// request is NOT from a full admin (super_admin / legacy admin_key).
//
// ★ 2026-11-27 非管理员菜单重构轮（P0 越权修复）：本闸门原先只拦 tenant_admin，
//   普通 role=user 的写操作**全部放行**——/api/models、prompt-injection、
//   error-playbook、pi-analysis 写端点共用本函数 ⇒ 普通用户可以直接改模型映射、
//   发布 PI 规则、执行 playbook 动作。修复口径：写操作只允许 IsSuperAdminOrLegacy。
//   （历史上只拦 tenant_admin 是因为当时所有调用点都在 requiresSuper 路由背后，
//   普通用户根本到不了这些端点；2026-11-27 起部分读面菜单对非管理员放开，
//   共用写闸的端点不再有路由层兜底 ⇒ 闸门本身必须收严。）
func RequireSuperAdminForWrite(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return false // read methods are always allowed
	}
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "write operations require super_admin")
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
