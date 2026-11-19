// Package sessionanalytics 会话分析系统 HTTP 处理器
package sessionanalytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// Handler 会话分析系统的 HTTP 处理器
type Handler struct {
	db *sql.DB
}

// NewHandler 创建新的处理器
func NewHandler(db *sql.DB) *Handler {
	return &Handler{db: db}
}

// HandleGetOverview 处理总体统计请求
//
// GET /api/admin/session-analytics/overview
//
// 查询参数:
//   - tenant_id (可选): 租户 ID，超级管理员可用
//   - start_date (必需): 开始日期 (YYYY-MM-DD)
//   - end_date (必需): 结束日期 (YYYY-MM-DD)
//
// 响应:
//   200: OverviewResponse
//   400: 参数错误
//   401: 未授权
//   403: 权限不足
//   500: 服务器错误
func (h *Handler) HandleGetOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 获取查询参数
	params, err := parseOverviewParamsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 权限检查：只有管理员可以访问
	if !isAdminFromRequest(r) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}

	// 租户隔离：非超级管理员只能查看自己租户的数据
	tenantID := params.TenantID
	if !isSuperAdminFromRequest(r) {
		userTenantID := getUserTenantIDFromRequest(r)
		if userTenantID != "" {
			tenantID = &userTenantID
		}
	}

	// 查询数据
	resp, err := GetOverview(ctx, h.db, tenantID, params.StartDate, params.EndDate)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get overview")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleGetUserRanking 处理用户排行请求
//
// GET /api/admin/session-analytics/users
//
// 查询参数:
//   - tenant_id (可选): 租户 ID
//   - start_date (必需): 开始日期
//   - end_date (必需): 结束日期
//   - project (可选): 项目名称筛选
//   - sort_by (可选): 排序字段 (sessions|tokens|cost|last_active)
//   - order (可选): 排序方向 (asc|desc)
//   - limit (可选): 每页数量，默认 50
//   - offset (可选): 偏移量，默认 0
//
// 响应:
//   200: UserRankingResponse
//   400: 参数错误
//   401: 未授权
//   403: 权限不足
//   500: 服务器错误
func (h *Handler) HandleGetUserRanking(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 获取查询参数
	params, err := parseUserRankingParamsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 权限检查
	if !isAdminFromRequest(r) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}

	// 租户隔离
	tenantID := params.TenantID
	if !isSuperAdminFromRequest(r) {
		userTenantID := getUserTenantIDFromRequest(r)
		if userTenantID != "" {
			tenantID = &userTenantID
		}
	}

	// 查询数据
	resp, err := GetUserRanking(
		ctx,
		h.db,
		tenantID,
		params.StartDate,
		params.EndDate,
		params.Project,
		params.SortBy,
		params.Order,
		params.Limit,
		params.Offset,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get user ranking")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// overviewParams 总体统计查询参数
type overviewParams struct {
	TenantID  *string
	StartDate string
	EndDate   string
}

// userRankingParams 用户排行查询参数
type userRankingParams struct {
	TenantID  *string
	StartDate string
	EndDate   string
	Project   *string
	SortBy    string
	Order     string
	Limit     int
	Offset    int
}

// parseOverviewParamsFromRequest 从 HTTP 请求解析总体统计查询参数
func parseOverviewParamsFromRequest(r *http.Request) (*overviewParams, error) {
	params := &overviewParams{}
	query := r.URL.Query()

	// 租户 ID（可选）
	if tenantID := query.Get("tenant_id"); tenantID != "" {
		params.TenantID = &tenantID
	}

	// 开始日期（必需）
	startDate := query.Get("start_date")
	if startDate == "" {
		return nil, errorf("start_date is required")
	}
	if !isValidDate(startDate) {
		return nil, errorf("invalid start_date format, expected YYYY-MM-DD")
	}
	params.StartDate = startDate

	// 结束日期（必需）
	endDate := query.Get("end_date")
	if endDate == "" {
		return nil, errorf("end_date is required")
	}
	if !isValidDate(endDate) {
		return nil, errorf("invalid end_date format, expected YYYY-MM-DD")
	}
	params.EndDate = endDate

	// 验证日期范围
	if startDate > endDate {
		return nil, errorf("start_date must be before or equal to end_date")
	}

	return params, nil
}

// parseUserRankingParamsFromRequest 从 HTTP 请求解析用户排行查询参数
func parseUserRankingParamsFromRequest(r *http.Request) (*userRankingParams, error) {
	params := &userRankingParams{
		SortBy: "tokens", // 默认按 Token 排序
		Order:  "desc",   // 默认降序
		Limit:  50,       // 默认每页 50 条
		Offset: 0,        // 默认从第一条开始
	}
	query := r.URL.Query()

	// 租户 ID（可选）
	if tenantID := query.Get("tenant_id"); tenantID != "" {
		params.TenantID = &tenantID
	}

	// 开始日期（必需）
	startDate := query.Get("start_date")
	if startDate == "" {
		return nil, errorf("start_date is required")
	}
	if !isValidDate(startDate) {
		return nil, errorf("invalid start_date format")
	}
	params.StartDate = startDate

	// 结束日期（必需）
	endDate := query.Get("end_date")
	if endDate == "" {
		return nil, errorf("end_date is required")
	}
	if !isValidDate(endDate) {
		return nil, errorf("invalid end_date format")
	}
	params.EndDate = endDate

	// 项目筛选（可选）
	if project := query.Get("project"); project != "" {
		params.Project = &project
	}

	// 排序字段（可选）
	if sortBy := query.Get("sort_by"); sortBy != "" {
		validSortFields := map[string]bool{
			"sessions":    true,
			"tokens":      true,
			"cost":        true,
			"last_active": true,
		}
		if !validSortFields[sortBy] {
			return nil, errorf("invalid sort_by, must be one of: sessions, tokens, cost, last_active")
		}
		params.SortBy = sortBy
	}

	// 排序方向（可选）
	if order := query.Get("order"); order != "" {
		if order != "asc" && order != "desc" {
			return nil, errorf("invalid order, must be asc or desc")
		}
		params.Order = order
	}

	// 分页参数（可选）
	if limitStr := query.Get("limit"); limitStr != "" {
		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit < 1 || limit > 200 {
			return nil, errorf("invalid limit, must be between 1 and 200")
		}
		params.Limit = limit
	}

	if offsetStr := query.Get("offset"); offsetStr != "" {
		offset, err := strconv.Atoi(offsetStr)
		if err != nil || offset < 0 {
			return nil, errorf("invalid offset, must be non-negative")
		}
		params.Offset = offset
	}

	return params, nil
}

// isValidDate 验证日期格式 (YYYY-MM-DD)
func isValidDate(dateStr string) bool {
	_, err := time.Parse("2006-01-02", dateStr)
	return err == nil
}

// isAdminFromRequest 从请求上下文检查用户是否为管理员
func isAdminFromRequest(r *http.Request) bool {
	// 从上下文获取用户信息
	userRole := r.Context().Value("user_role")
	if userRole == nil {
		return false
	}

	role, ok := userRole.(string)
	if !ok {
		return false
	}

	// 管理员角色：admin, super_admin, tenant_admin
	return role == "admin" || role == "super_admin" || role == "tenant_admin"
}

// isSuperAdminFromRequest 从请求上下文检查用户是否为超级管理员
func isSuperAdminFromRequest(r *http.Request) bool {
	userRole := r.Context().Value("user_role")
	if userRole == nil {
		return false
	}

	role, ok := userRole.(string)
	if !ok {
		return false
	}

	return role == "super_admin"
}

// getUserTenantIDFromRequest 从请求上下文获取用户的租户 ID
func getUserTenantIDFromRequest(r *http.Request) string {
	tenantID := r.Context().Value("tenant_id")
	if tenantID == nil {
		return ""
	}

	tid, ok := tenantID.(string)
	if !ok {
		return ""
	}

	return tid
}

// RegisterRoutes 注册会话分析系统的路由
//
// 将所有会话分析相关的路由注册到 HTTP ServeMux 中。
// 所有路由都需要管理员权限。
//
// 路由列表:
//   - GET /api/admin/session-analytics/overview - 总体统计仪表板
//   - GET /api/admin/session-analytics/users - 用户排行
//
// 中间件要求:
//   - adminWrap: 认证和权限中间件
func RegisterRoutes(mux *http.ServeMux, db *sql.DB, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	handler := NewHandler(db)

	// 总体统计
	mux.HandleFunc("/api/admin/session-analytics/overview", adminWrap(handler.HandleGetOverview))

	// 用户排行
	mux.HandleFunc("/api/admin/session-analytics/users", adminWrap(handler.HandleGetUserRanking))
}

// ErrorResponse 错误响应
type ErrorResponse struct {
	Error string `json:"error"`
}

// writeError 写入错误响应
func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(ErrorResponse{Error: message})
}

// writeJSON 写入 JSON 响应
func writeJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}

// errorf 创建错误
func errorf(format string, args ...interface{}) error {
	return &errorString{s: format}
}

type errorString struct {
	s string
}

func (e *errorString) Error() string {
	return e.s
}

// withTimeout 为查询添加超时控制
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

// 默认查询超时时间
const defaultQueryTimeout = 30 * time.Second
