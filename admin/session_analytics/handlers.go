// Package sessionanalytics 会话分析系统 HTTP 处理器
package sessionanalytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/settings"
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
//
//	200: OverviewResponse
//	400: 参数错误
//	401: 未授权
//	403: 权限不足
//	500: 服务器错误
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

	// ★ 陈旧度门：视图在但数据停在上次刷新时，拒绝返回过期数字
	//   （仓内 245 实测过 9h37m 的陈旧数据被当成结论发出去，见 mv_guard.go）。
	if !h.requireFreshViews(w, r, "user_session_aggregates", "model_usage_aggregates") {
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
// GET /api/admin/session-analytics/user-ranking
//
// ★ 路径不是 /users：旧族（2026-07-07 用户画像）已占用该路径且前端在用，
//
//	重复注册会让 ServeMux panic ⇒ 整个网关起不来。见 RoutePaths 的注释。
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
//
//	200: UserRankingResponse
//	400: 参数错误
//	401: 未授权
//	403: 权限不足
//	500: 服务器错误
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

	// ★ 陈旧度门（同 overview 端点；见 mv_guard.go）。
	if !h.requireFreshViews(w, r, "user_session_aggregates") {
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
// 时区轮（2026-11-20）：走 settings.ParseSystemDate，与解析面同源。
func isValidDate(dateStr string) bool {
	_, err := settings.ParseSystemDate(dateStr)
	return err == nil
}

// isAdminFromRequest 从请求上下文检查用户是否为管理员
//
// ★ 上下文键：仓内的认证中间件 admin.AdminMiddleware 用的是**类型化** context 键
//
//	（admin.SetAuthContext / admin.GetAuthContext），不是字符串键。
//	本函数此前读 r.Context().Value("user_role") ⇒ 永远拿到 nil ⇒ 恒返回 false
//	⇒ 端点对**任何**人（含真管理员）都返回 403。
//	⇒ 这里改用 admin.GetAuthContext(r)（唯一真源）。
//
// ★ 角色词表：仓内的真实角色只有 super_admin / tenant_admin / admin_key
//
//	（真库 users 表实测：tenant_admin 21 个、super_admin 3 个；没有 "admin" 角色）。
//	本函数此前把 "admin" 当合法角色，是一条**永不命中**的分支。
func isAdminFromRequest(r *http.Request) bool {
	auth := admin.GetAuthContext(r)
	if auth == nil {
		return false
	}
	switch auth.Role {
	case "super_admin", "tenant_admin", "admin_key":
		return true
	default:
		return false
	}
}

// isSuperAdminFromRequest 从请求上下文检查用户是否为超级管理员
func isSuperAdminFromRequest(r *http.Request) bool {
	auth := admin.GetAuthContext(r)
	if auth == nil {
		return false
	}
	return auth.Role == "super_admin" || auth.Role == "admin_key"
}

// getUserTenantIDFromRequest 从请求上下文获取用户的租户 ID
func getUserTenantIDFromRequest(r *http.Request) string {
	auth := admin.GetAuthContext(r)
	if auth == nil {
		return ""
	}
	return auth.TenantID
}

// 路由清单（SSOT）—— 注册与钉测共用**同一份**，杜绝「文档写的」与
// 「注册的不是」两套路径。
//
// ★★ 为什么"人员排行"不能叫 /users（2026-11-20 211 部署事故实证）：
//
//	仓内**早就有**一个同名路径，且**前端在用**：
//	  · admin/handler.go:1334  mux.HandleFunc("/api/admin/session-analytics/users",
//	                                admin(h.handleUserAnalyticsList))
//	    —— 2026-07-07 的「P2 会话分析 / 用户画像」端点（读 357 族视图）；
//	  · web/src/api/admin.ts:239 fetchUserProfiles()、UserProfileListView.vue、
//	    appNav.ts:237 菜单项都指向它 ⇒ **活的前端契约**，不能占。
//
//	而设计文档（docs/design/session-analytics-system.md §API）写的是
//	`/api/admin/session-analytics/users` = 新系统的「人员排行」
//	⇒ 照文档抄 ⇒ **同一路径在同一 ServeMux 上注册两次**
//	⇒ Go 的 ServeMux 直接 panic（"conflicts with pattern"）
//	⇒ main.go 的 panic 兜底把它变成「启动失败 ⇒ 进程退出(1)」
//	⇒ 蓝绿候选槽起不来 ⇒ 部署回退 ⇒ 线上永远拿不到新端点。
//
//	⇒ 通用式：**URL 契约必须对着仓内真源核，不能照设计文档抄**——
//	  文档写的是"应该长什么样"，真源才是"现在长什么样"；
//	  两边不一致时，撞车的是**进程启动**，不是某个 404。
//
// ★ 新系统族的命名约定（与旧族并存，互不占用）：
//
//	/api/admin/session-analytics/overview      总体统计仪表板
//	/api/admin/session-analytics/user-ranking  人员排行
//	/api/admin/session-analytics/agent-ranking 智能体排行（Phase 3）
//	/api/admin/session-analytics/model-ranking 模型排行（Phase 3）
//
//	旧族（2026-07-07，勿动）：
//	/api/admin/session-analytics/clients|/tasks|/users(+子路径)
var (
	// RouteOverview 是总体统计仪表板端点。
	RouteOverview = "/api/admin/session-analytics/overview"
	// RouteUserRanking 是人员排行端点。
	//
	// ★ 不是 /users —— 见上面 RoutePaths 的注释（旧族已占用且前端在用）。
	RouteUserRanking = "/api/admin/session-analytics/user-ranking"
)

// RoutePaths 是本包注册到 ServeMux 上的**完整**路径清单（SSOT）。
//
// ★ 用途：cmd/gateway 的路由冲突守卫测试拿它跟"仓内其它已注册路径"
//
//	做比对 —— 任何一条撞车都会让**整个网关启动失败**，必须在单测里红。
var RoutePaths = []string{
	RouteOverview,
	RouteUserRanking,
}

// RegisterRoutes 注册会话分析系统的路由
//
// 将所有会话分析相关的路由注册到 HTTP ServeMux 中。
// 所有路由都需要管理员权限。
//
// 路由列表:
//   - GET /api/admin/session-analytics/overview     - 总体统计仪表板
//   - GET /api/admin/session-analytics/user-ranking - 人员排行
//
// ★ 冲突是**致命**的：同一个 ServeMux 上重复注册同一路径会 panic，
//
//	而 main.go 的 panic 兜底会让**整个网关**起不来（211 部署事故）。
//	⇒ 改路径前先看 RoutePaths 的注释。
//
// 中间件要求:
//   - adminWrap: 认证和权限中间件
func RegisterRoutes(mux *http.ServeMux, db *sql.DB, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	handler := NewHandler(db)

	// 总体统计
	mux.HandleFunc(RouteOverview, adminWrap(handler.HandleGetOverview))

	// 人员排行
	mux.HandleFunc(RouteUserRanking, adminWrap(handler.HandleGetUserRanking))
}

// ErrorResponse 错误响应
type ErrorResponse struct {
	Error string `json:"error"`
	// Code 是机器可读的错误码（与仓内 admin 包的错误响应同构）：
	//   analytics_view_stale / analytics_view_missing / ...
	// ★ 前端按 code 分流（文案兜底走 error 字段），运维按 code 查处置。
	Code string `json:"code,omitempty"`
}

// writeError 写入错误响应
func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(ErrorResponse{Error: message})
}

// writeErrorWithCode 写入带机器可读错误码的错误响应
func writeErrorWithCode(w http.ResponseWriter, code int, errCode string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(ErrorResponse{Error: message, Code: errCode})
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
