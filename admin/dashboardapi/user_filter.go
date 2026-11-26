package dashboardapi

import "fmt"

// user_filter.go 提供用户级数据过滤功能
//
// 权限模型：
//   - super_admin: 看全平台所有数据
//   - tenant_admin: 看本租户所有数据
//   - user: 只能看自己的数据
//
// 使用方式：
//   params := ParseQueryParams(r)
//   auth := extractAuthInfo(r)
//   params = NormalizeQueryParamsWithUserFilter(params, auth)

// NormalizeQueryParamsWithUserFilter 应用用户级过滤到查询参数
//
// 实施三级权限过滤：
//  1. super_admin: 不限制（看全平台）
//  2. tenant_admin: 限制到租户（params.TenantID）
//  3. user: 限制到用户（params.OwnerUser）
func NormalizeQueryParamsWithUserFilter(params QueryParams, auth AuthInfo) QueryParams {
	// 1. 租户级过滤（tenant_admin 及以下）
	if auth.UserRole != "super_admin" && auth.UserRole != "admin_key" {
		params.TenantID = auth.TenantID
	}

	// 2. 用户级过滤（普通用户）
	if auth.UserRole == "user" {
		// 关键：普通用户只能看自己的数据
		params.OwnerUser = auth.Username
	}

	// 3. 保存认证信息到参数中（供后续使用）
	params.auth = auth

	return params
}

// IsRegularUser 判断是否为普通用户
func IsRegularUser(auth AuthInfo) bool {
	return auth.UserRole == "user"
}

// IsTenantAdmin 判断是否为租户管理员
func IsTenantAdmin(auth AuthInfo) bool {
	return auth.UserRole == "tenant_admin"
}

// IsSuperAdmin 判断是否为超级管理员
func IsSuperAdmin(auth AuthInfo) bool {
	return auth.UserRole == "super_admin" || auth.UserRole == "admin_key"
}

// BuildUserFilterClause 构建用户过滤 SQL 子句
//
// 参数：
//   - auth: 认证信息
//   - columnName: 用户列名（如 "sd.owner_user"）
//   - argIdx: 当前参数索引
//
// 返回：
//   - clause: SQL WHERE 子句（如 " AND sd.owner_user = $3"）
//   - args: 参数值列表
//   - nextIdx: 下一个参数索引
//
// 使用示例：
//   userClause, userArgs, nextIdx := BuildUserFilterClause(auth, "sd.owner_user", argIdx)
//   if userClause != "" {
//       query += userClause
//       args = append(args, userArgs...)
//       argIdx = nextIdx
//   }
func BuildUserFilterClause(auth AuthInfo, columnName string, argIdx int) (clause string, args []interface{}, nextIdx int) {
	if auth.UserRole != "user" {
		// 非普通用户不需要用户过滤
		return "", nil, argIdx
	}

	// 构建 AND 子句
	clause = fmt.Sprintf(" AND %s = $%d", columnName, argIdx)
	args = []interface{}{auth.Username}
	nextIdx = argIdx + 1

	return clause, args, nextIdx
}
