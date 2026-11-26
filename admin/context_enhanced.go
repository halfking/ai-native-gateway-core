package admin

import (
	"net/http"
)

// EffectiveUserID 返回用户级查询的 user_id 过滤值：
//   - role=user: 返回自己的 user_id（强制限制）
//   - role>=tenant_admin: 返回 0（表示可查本租户所有用户）
//   - super_admin: 返回 0（表示可查全平台）
//
// 用法示例：
//
//	userID := EffectiveUserID(r)
//	if userID != 0 {
//	    query += " AND user_id = $n"
//	    args = append(args, userID)
//	}
func EffectiveUserID(r *http.Request) int {
	auth := GetAuthContext(r)
	if auth == nil {
		return 0
	}
	if auth.Role == RoleUser {
		return auth.UserID
	}
	return 0 // admin 角色可看全部
}

// RequireUserOrHigher 确保请求至少是普通用户。
// 返回 true 表示已拒绝请求（调用方应立即 return）。
func RequireUserOrHigher(w http.ResponseWriter, r *http.Request) bool {
	auth := GetAuthContext(r)
	if auth == nil || auth.UserID == 0 {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return true
	}
	return false
}

// CanAccessTenant 判断是否可访问指定租户的数据。
// super_admin 可访问任意租户，tenant_admin 只能访问自己的租户，
// user 不能直接访问租户级数据（应使用 CanAccessUserData）。
func CanAccessTenant(r *http.Request, tenantID string) bool {
	auth := GetAuthContext(r)
	if auth == nil {
		return false
	}
	if IsSuperAdminOrLegacy(r) {
		return true
	}
	// 只有 tenant_admin 可以访问租户级数据
	if IsTenantAdmin(r) && auth.TenantID == tenantID {
		return true
	}
	return false
}

// CanAccessUserData 判断是否可访问指定用户的数据。
//   - super_admin: 可访问任意用户
//   - tenant_admin: 可访问同租户的任意用户
//   - user: 只能访问同租户且同 user_id 的数据（即自己的数据）
func CanAccessUserData(r *http.Request, targetUserID int, targetTenantID string) bool {
	auth := GetAuthContext(r)
	if auth == nil {
		return false
	}
	// super_admin 可访问任意用户
	if IsSuperAdminOrLegacy(r) {
		return true
	}
	// tenant_admin 可访问同租户的任意用户
	if IsTenantAdmin(r) && auth.TenantID == targetTenantID {
		return true
	}
	// user 只能访问自己的数据（同租户 + 同 user_id）
	if auth.Role == RoleUser && auth.UserID == targetUserID && auth.TenantID == targetTenantID {
		return true
	}
	return false
}

// GetAuthUserID 返回当前认证用户的 user_id，未认证返回 0。
func GetAuthUserID(r *http.Request) int {
	auth := GetAuthContext(r)
	if auth == nil {
		return 0
	}
	return auth.UserID
}

// RequireTenantMatch 检查资源归属租户是否匹配当前用户租户。
// super_admin 总是通过，tenant_admin 和 user 必须匹配。
// 返回 true 表示已拒绝请求（调用方应立即 return）。
func RequireTenantMatch(w http.ResponseWriter, r *http.Request, resourceTenantID string) bool {
	if IsSuperAdminOrLegacy(r) {
		return false // super_admin 可跨租户
	}
	
	auth := GetAuthContext(r)
	if auth == nil || auth.TenantID != resourceTenantID {
		writeError(w, http.StatusForbidden, "access denied: resource belongs to different tenant")
		return true
	}
	return false
}

// RequireUserMatch 检查资源归属用户是否匹配当前用户。
// admin 角色通过租户检查后即可访问，user 必须完全匹配。
// 返回 true 表示已拒绝请求（调用方应立即 return）。
func RequireUserMatch(w http.ResponseWriter, r *http.Request, resourceUserID int, resourceTenantID string) bool {
	if !CanAccessUserData(r, resourceUserID, resourceTenantID) {
		writeError(w, http.StatusForbidden, "access denied: insufficient permissions to access this user's data")
		return true
	}
	return false
}
