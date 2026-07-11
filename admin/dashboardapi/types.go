// Package dashboardapi 提供首页 Dashboard 的标准 API
//
// 设计原则：
//  1. RESTful 设计，清晰的资源路径
//  2. 统一的响应格式（success/error/code/message）
//  3. 完整的分页、排序、筛选支持
//  4. 权限控制（三层隔离）
//  5. 缓存友好（支持 ETag/Last-Modified）
//
// API 列表：
//   - GET  /api/admin/dashboard/session-overview         会话总览
//   - GET  /api/admin/dashboard/session-trend            会话趋势
//   - GET  /api/admin/dashboard/session-health           健康度分布
//   - GET  /api/admin/dashboard/session-active           活跃会话
//   - GET  /api/admin/dashboard/module-stats             模块执行统计
//   - GET  /api/admin/dashboard/errors                   错误统计
//   - GET  /api/admin/dashboard/performance              性能指标
//
// 通用查询参数：
//   - tenant_id    租户 ID（超级管理员可用）
//   - days         时间范围（1/7/30/90）
//   - refresh      是否强制刷新缓存（true/false）
package dashboardapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ────────────────────────────────────────────────────────────────
// 统一响应格式
// ────────────────────────────────────────────────────────────────

// Response 统一响应结构
type Response struct {
	Success   bool        `json:"success"`
	Code      string      `json:"code,omitempty"`
	Message   string      `json:"message,omitempty"`
	Data      interface{} `json:"data,omitempty"`
	Error     *ErrorInfo  `json:"error,omitempty"`
	Metadata  *Metadata   `json:"metadata,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// ErrorInfo 错误信息
type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// Metadata 元数据
type Metadata struct {
	Total       int       `json:"total,omitempty"`
	Page        int       `json:"page,omitempty"`
	Size        int       `json:"size,omitempty"`
	Pages       int       `json:"pages,omitempty"`
	CacheHit    bool      `json:"cache_hit,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
	TookMs      int64     `json:"took_ms,omitempty"`
}

// ────────────────────────────────────────────────────────────────
// 标准错误码
// ────────────────────────────────────────────────────────────────

const (
	ErrCodeInvalidParam    = "INVALID_PARAM"
	ErrCodeUnauthorized    = "UNAUTHORIZED"
	ErrCodeForbidden       = "FORBIDDEN"
	ErrCodeNotFound        = "NOT_FOUND"
	ErrCodeInternal        = "INTERNAL_ERROR"
	ErrCodeDatabaseError   = "DATABASE_ERROR"
	ErrCodeCacheError      = "CACHE_ERROR"
	ErrCodeTooManyRequests = "TOO_MANY_REQUESTS"
	ErrCodeDataNotReady    = "DATA_NOT_READY"
)

// ────────────────────────────────────────────────────────────────
// 标准查询参数
// ────────────────────────────────────────────────────────────────

// QueryParams 标准查询参数
type QueryParams struct {
	TenantID string `json:"tenant_id,omitempty"`
	Days     int    `json:"days,omitempty"`
	Page     int    `json:"page,omitempty"`
	Size     int    `json:"size,omitempty"`
	SortBy   string `json:"sort_by,omitempty"`
	SortDir  string `json:"sort_dir,omitempty"`
	Search   string `json:"search,omitempty"`
	Refresh  bool   `json:"refresh,omitempty"`
	auth     AuthInfo
}

// AuthInfo 认证信息，由上层 admin 包注入
type AuthInfo struct {
	TenantID     string
	UserID       string
	UserRole     string // super_admin / tenant_admin / user / admin_key
	Username     string
	IsSuperAdmin bool
	IsJWT        bool
}

// ParseQueryParams 解析查询参数
func ParseQueryParams(r *http.Request) QueryParams {
	q := r.URL.Query()

	params := QueryParams{
		TenantID: q.Get("tenant_id"),
		Days:     parseIntDefault(q.Get("days"), 7),
		Page:     parseIntDefault(q.Get("page"), 1),
		Size:     parseIntDefault(q.Get("size"), 20),
		SortBy:   q.Get("sort_by"),
		SortDir:  q.Get("sort_dir"),
		Search:   q.Get("search"),
		Refresh:  q.Get("refresh") == "true",
	}

	// 参数验证
	if params.Days < 1 || params.Days > 90 {
		params.Days = 7
	}
	if params.Page < 1 {
		params.Page = 1
	}
	if params.Size < 1 || params.Size > 100 {
		params.Size = 20
	}
	if params.SortDir == "" {
		params.SortDir = "desc"
	}

	return params
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

// ────────────────────────────────────────────────────────────────
// DashboardHandler 核心 Handler
// ────────────────────────────────────────────────────────────────

// DashboardHandler Dashboard API 核心处理器
type DashboardHandler struct {
	db       *pgxpool.Pool
	recorder interface {
		RecordAccess(tenantID, userID, userRole, sessionID, apiPath, apiMethod string, statusCode int, responseTime time.Duration, cacheHit bool)
	}
}

// NewDashboardHandler 创建 DashboardHandler
func NewDashboardHandler(db *pgxpool.Pool, recorder interface {
	RecordAccess(tenantID, userID, userRole, sessionID, apiPath, apiMethod string, statusCode int, responseTime time.Duration, cacheHit bool)
}) *DashboardHandler {
	return &DashboardHandler{db: db, recorder: recorder}
}

// ────────────────────────────────────────────────────────────────
// 响应写入工具
// ────────────────────────────────────────────────────────────────

// writeSuccessJSON 写入成功 JSON 响应
func writeSuccessJSON(w http.ResponseWriter, data interface{}, metadata *Metadata) {
	resp := Response{
		Success:   true,
		Data:      data,
		Metadata:  metadata,
		Timestamp: time.Now(),
	}
	writeJSONResponse(w, http.StatusOK, resp)
}

// writeErrorJSON 写入错误 JSON 响应
func writeErrorJSON(w http.ResponseWriter, status int, code, message, details string) {
	resp := Response{
		Success: false,
		Code:    code,
		Message: message,
		Error: &ErrorInfo{
			Code:    code,
			Message: message,
			Details: details,
		},
		Timestamp: time.Now(),
	}
	writeJSONResponse(w, status, resp)
}

// writeJSONResponse 写入 JSON 响应（底层）
func writeJSONResponse(w http.ResponseWriter, status int, v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		//nolint:errcheck
		w.Write([]byte(`{"success":false,"code":"INTERNAL_ERROR","message":"json marshal failed","timestamp":"` + time.Now().Format(time.RFC3339) + `"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	//nolint:errcheck
	w.Write(data)
	//nolint:errcheck
	w.Write([]byte("\n"))
}

// ────────────────────────────────────────────────────────────────
// 通用查询工具
// ────────────────────────────────────────────────────────────────

// joinStrings 连接字符串切片
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

// GetRequestContext 获取带超时的请求上下文
func GetRequestContext(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}

// resolveTenantID 解析最终使用的 tenant_id
// super_admin 可通过 query 参数指定，其他角色使用自身 tenant_id
func resolveTenantID(params QueryParams, auth AuthInfo) string {
	if auth.IsSuperAdmin && params.TenantID != "" {
		return params.TenantID
	}
	if auth.TenantID != "" {
		return auth.TenantID
	}
	return ""
}

// buildTenantWhere 构建 tenant_id 过滤条件
func buildTenantWhere(tenantID string, args *[]interface{}, argIdx *int) string {
	if tenantID == "" {
		return ""
	}
	clause := fmt.Sprintf("tenant_id = $%d", *argIdx)
	*args = append(*args, tenantID)
	*argIdx++
	return clause
}

func appendDashboardScope(where *[]string, params QueryParams, args *[]interface{}, argIdx *int, tableAlias string, filterOwner bool) {
	if params.TenantID != "" {
		column := "tenant_id"
		if tableAlias != "" {
			column = tableAlias + ".tenant_id"
		}
		*where = append(*where, fmt.Sprintf("%s = $%d", column, *argIdx))
		*args = append(*args, params.TenantID)
		*argIdx++
	}
	if filterOwner {
		if clause := buildOwnerWhere(params.auth, args, argIdx, tableAlias); clause != "" {
			*where = append(*where, clause)
		}
	}
}

func appendExecutionScope(where *[]string, params QueryParams, args *[]interface{}, argIdx *int, executionAlias string) {
	appendDashboardScope(where, params, args, argIdx, executionAlias, false)
	if params.auth.UserRole != "user" || !params.auth.IsJWT || params.auth.Username == "" {
		return
	}
	prefix := ""
	if executionAlias != "" {
		prefix = executionAlias + "."
	}
	*where = append(*where, fmt.Sprintf(
		"EXISTS (SELECT 1 FROM session_summaries s WHERE s.session_key = %sgw_session_id AND s.owner_user = $%d)",
		prefix, *argIdx,
	))
	*args = append(*args, params.auth.Username)
	*argIdx++
}

// buildOwnerWhere 为普通用户构建 owner_user 过滤条件
func buildOwnerWhere(auth AuthInfo, args *[]interface{}, argIdx *int, tableAlias string) string {
	if auth.UserRole != "user" || !auth.IsJWT || auth.Username == "" {
		return ""
	}
	col := "owner_user"
	if tableAlias != "" {
		col = tableAlias + ".owner_user"
	}
	clause := fmt.Sprintf("%s = $%d", col, *argIdx)
	*args = append(*args, auth.Username)
	*argIdx++
	return clause
}
