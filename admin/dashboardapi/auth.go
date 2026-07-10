// Package dashboardapi - auth.go
// Dashboard API 权限校验辅助函数
package dashboardapi

import (
	"context"
	"net/http"
)

// contextKey 类型用于避免 context key 冲突
type contextKey string

const authInfoContextKey contextKey = "dashboardapi_auth_info"

// GetAuthInfoFromContext 从 Context 中提取认证信息
// 如果未找到，返回空的 AuthInfo（表示未认证）
func GetAuthInfoFromContext(ctx context.Context) AuthInfo {
	if auth, ok := ctx.Value(authInfoContextKey).(AuthInfo); ok {
		return auth
	}
	// 返回空的 AuthInfo（未认证状态）
	return AuthInfo{}
}

// SetAuthInfoToContext 将认证信息注入到 Context
func SetAuthInfoToContext(ctx context.Context, auth AuthInfo) context.Context {
	return context.WithValue(ctx, authInfoContextKey, auth)
}

// RequireAuth 认证中间件包装器
// 从 admin 包的 AuthContext 提取信息并转换为 dashboardapi.AuthInfo
func RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 从 admin 包的 Context 提取认证信息
		// admin.AdminMiddleware 已经完成了认证，这里只需要转换格式
		auth := extractAuthFromAdminContext(r)
		
		// 注入到新的 Context
		ctx := SetAuthInfoToContext(r.Context(), auth)
		next(w, r.WithContext(ctx))
	}
}

// extractAuthFromAdminContext 从 admin 包的 Context 提取认证信息
// 这是一个桥接函数，将 admin.AuthContext 转换为 dashboardapi.AuthInfo
func extractAuthFromAdminContext(r *http.Request) AuthInfo {
	// 尝试从 Context 中读取 admin 包的认证信息
	// admin.SetAuthContext 设置的 key 是 "auth_context"
	ctx := r.Context()
	
	// 注意：由于循环依赖问题，我们不能直接导入 admin 包
	// 所以这里使用 interface{} 并通过反射或类型断言获取字段
	// 更好的方案是在 admin/handler.go 中已经处理好认证后调用 Dashboard API
	
	authRaw := ctx.Value("auth_context")
	if authRaw == nil {
		// 未认证，返回空 AuthInfo
		return AuthInfo{}
	}
	
	// 类型断言（这里需要与 admin.AuthContext 保持一致）
	// 由于不能导入 admin 包，我们使用 map 来传递信息
	if authMap, ok := authRaw.(map[string]interface{}); ok {
		auth := AuthInfo{}
		if v, ok := authMap["tenant_id"].(string); ok {
			auth.TenantID = v
		}
		if v, ok := authMap["user_id"].(string); ok {
			auth.UserID = v
		}
		if v, ok := authMap["username"].(string); ok {
			auth.Username = v
		}
		if v, ok := authMap["role"].(string); ok {
			auth.UserRole = v
			auth.IsSuperAdmin = (v == "super_admin")
		}
		if v, ok := authMap["is_jwt"].(bool); ok {
			auth.IsJWT = v
		}
		return auth
	}
	
	return AuthInfo{}
}

// CheckTenantAccess 检查租户访问权限
// super_admin 可以访问所有租户，其他角色只能访问自己的租户
func CheckTenantAccess(auth AuthInfo, requestedTenantID string) bool {
	if auth.IsSuperAdmin {
		return true // super_admin 可以访问任何租户
	}
	if requestedTenantID == "" {
		return true // 未指定租户，允许访问自己的租户
	}
	return auth.TenantID == requestedTenantID
}

// ResolveTenantID 解析最终使用的租户 ID
// super_admin 可以通过参数指定租户，其他角色使用自己的租户
func ResolveTenantID(auth AuthInfo, requestedTenantID string) string {
	if auth.IsSuperAdmin && requestedTenantID != "" {
		return requestedTenantID
	}
	return auth.TenantID
}
