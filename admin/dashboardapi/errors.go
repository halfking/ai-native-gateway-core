// Package dashboardapi - errors.go
// 错误统计 API：查询错误分布、错误趋势、常见错误
//
// 2026-07-13: 增加 session_module_executions_hot 表缺失时的优雅降级
//
// 2026-11-25 真源统一轮：真源从 `session_module_executions_hot` 换成
// **请求级日志** `request_logs_with_current_month`（见
// request_log_facts_source.go 的背景与口径三件套）。
//
// ★ 为什么换：该表的唯一写方 `moduleexec.Executor` 在生产从未装配 ⇒ 恒 0
//
//	（把「没有数据」读成了「没有错误」）。
//
// ★ 口径随真源走：失败集 = request_status IN ('failure','rate_limited')，
//
//	分母 = 终态集全量，延迟列 = latency_ms，窗口列 = ts。
//
// ★ 分类维度随真源换：请求级日志**没有模块维度** ⇒ 分布改按 error_kind 分类
//
//	（响应字段名 error_type 保持不变，以免破坏前端契约）。
package dashboardapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin/dashboarddegrade"
)

// ErrorsHandler 错误统计 Handler
type ErrorsHandler struct {
	db *pgxpool.Pool
}

// NewErrorsHandler 创建 Handler
func NewErrorsHandler(db *pgxpool.Pool) *ErrorsHandler {
	return &ErrorsHandler{db: db}
}

// ErrorStatsResponse 错误统计响应
type ErrorStatsResponse struct {
	// DataSource 披露本卡读的是哪一族真源（"request_logs"）——前端徽标跟随
	// 响应值，不跟随本地开关（缺席比假绿诚实）。
	DataSource string `json:"data_source"`
	// ProbeScope 披露探测口径（excluded / included / degraded_included）。
	ProbeScope   string           `json:"probe_scope"`
	Summary      ErrorSummary     `json:"summary"`
	Distribution []ErrorDistItem  `json:"distribution"`
	Trend        []ErrorTrendItem `json:"recent_errors"`
	TopErrors    []ErrorDetail    `json:"top_errors"`
}

// ErrorSummary 错误摘要
type ErrorSummary struct {
	TotalErrors     int     `json:"total_errors"`
	ErrorRate       float64 `json:"error_rate"`
	TotalRequests   int     `json:"total_requests"`
	AvgErrorLatency float64 `json:"avg_error_latency_ms"`
}

// ErrorDistItem 错误分布项
type ErrorDistItem struct {
	ErrorType  string  `json:"error_type"`
	Count      int     `json:"count"`
	Percentage float64 `json:"percentage"`
}

// ErrorTrendItem 错误趋势项
type ErrorTrendItem struct {
	Date       string `json:"date"`
	ErrorCount int    `json:"error_count"`
	TotalCount int    `json:"total_count"`
}

// ErrorDetail 错误详情
type ErrorDetail struct {
	ErrorMessage string    `json:"error_message"`
	Count        int       `json:"count"`
	LastOccurred time.Time `json:"last_occurred"`
	Module       string    `json:"module"`
}

// HandleErrors 处理错误统计请求
//
// GET /api/admin/dashboard/errors
func (h *ErrorsHandler) HandleErrors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "method not allowed", "")
		return
	}

	startTime := time.Now()
	apiStatus := "success"
	defer func() {
		recordAPIRequest("errors", apiStatus, time.Since(startTime))
	}()

	params, _, ok := prepareDashboardRequest(w, r, h.db)
	if !ok {
		return
	}
	// 2026-11-23 日期范围对齐轮：窗口解析与会话总览同尺（显式日历窗口优先）。
	win, werr := parseSessionWindow(r, params)
	if werr != nil {
		writeErrorJSON(w, http.StatusBadRequest, ErrCodeInvalidParam, werr.Error(), "")
		return
	}
	ctx, cancel := GetRequestContext(r, 15*time.Second)
	defer cancel()

	// 1. 错误摘要
	summary, err := h.queryErrorSummary(ctx, params, win)
	if err != nil {
		if requestLogFactsMissingRelation(err) {
			h.writeDegraded(w, startTime, "errors:summary", err, win)
			apiStatus = "degraded"
			return
		}
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query error summary", err.Error())
		return
	}

	// 2. 错误分布
	dist, err := h.queryErrorDistribution(ctx, params, win)
	if err != nil {
		if requestLogFactsMissingRelation(err) {
			h.writeDegraded(w, startTime, "errors:distribution", err, win)
			apiStatus = "degraded"
			return
		}
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query error distribution", err.Error())
		return
	}

	// 3. 最近错误
	recentErrors, err := h.queryRecentErrors(ctx, params, win)
	if err != nil {
		if requestLogFactsMissingRelation(err) {
			h.writeDegraded(w, startTime, "errors:recent", err, win)
			apiStatus = "degraded"
			return
		}
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query recent errors", err.Error())
		return
	}

	// 4. Top 错误
	topErrors, err := h.queryTopErrors(ctx, params, win)
	if err != nil {
		if requestLogFactsMissingRelation(err) {
			h.writeDegraded(w, startTime, "errors:top", err, win)
			apiStatus = "degraded"
			return
		}
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query top errors", err.Error())
		return
	}

	resp := ErrorStatsResponse{
		DataSource:   requestLogFactsDataSource,
		ProbeScope:   probeScopeValue(win),
		Summary:      *summary,
		Distribution: dist,
		Trend:        recentErrors,
		TopErrors:    topErrors,
	}

	metadata := &Metadata{
		GeneratedAt: time.Now(),
		TookMs:      time.Since(startTime).Milliseconds(),
	}
	writeSuccessJSON(w, resp, metadata)
}

func (h *ErrorsHandler) queryErrorSummary(ctx context.Context, params QueryParams, win sessionWindow) (*ErrorSummary, error) {
	summary := &ErrorSummary{}
	// ★ 谓词一次成型（窗口 + 租户 + 终态 + 探测口径），后面的查询要加参数时
	//   接着返回的 argIdx 往后排（占位符与实参必须一一对应）。
	whereClause, args, _ := requestLogFactsWhere(win, params)

	// ★ 旧列 → 新列映射：status='failed' → request_status IN ('failure','rate_limited')
	//   （终态集里 failure 与 rate_limited 都算失败）；
	//   duration_ms → latency_ms；created_at → ts（窗口列）。
	// ★ 分母用终态集全量（requestLogFactsWhere 已把非终态行滤掉）⇒ 错误率
	//   不会被「还在飞」的行稀释。
	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total_requests,
			COUNT(*) FILTER (WHERE r.request_status IN ('failure','rate_limited')) as total_errors,
			COALESCE(AVG(r.latency_ms) FILTER (WHERE r.request_status IN ('failure','rate_limited')), 0) as avg_error_latency
		FROM request_logs_with_current_month r
		%s
	`, whereClause)

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&summary.TotalRequests, &summary.TotalErrors, &summary.AvgErrorLatency,
	)
	if err != nil {
		return nil, err
	}
	if summary.TotalRequests > 0 {
		summary.ErrorRate = float64(summary.TotalErrors) * 100 / float64(summary.TotalRequests)
	}
	return summary, nil
}

func (h *ErrorsHandler) queryErrorDistribution(ctx context.Context, params QueryParams, win sessionWindow) ([]ErrorDistItem, error) {
	whereClause, args, _ := requestLogFactsWhere(win, params)
	// ★ 只看失败行（终态集里的 failure / rate_limited）。
	whereClause += " AND r.request_status IN ('failure','rate_limited')"

	// ★ 旧列 → 新列映射：module_name → error_kind。
	//   请求级日志**没有模块维度**（模块执行是网关内部的执行单元，不在请求行上）
	//   ⇒ 分布改按 error_kind 分类；响应字段名 error_type 保持不变，以免破坏
	//   前端契约。NULLIF(…,'')：error_kind 可能是空串而非 NULL。
	query := fmt.Sprintf(`
		SELECT
			COALESCE(NULLIF(r.error_kind, ''), 'unknown') as error_type,
			COUNT(*) as count
		FROM request_logs_with_current_month r
		%s
		GROUP BY r.error_kind
		ORDER BY count DESC
	`, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]ErrorDistItem, 0)
	total := 0
	for rows.Next() {
		var item ErrorDistItem
		if err := rows.Scan(&item.ErrorType, &item.Count); err != nil {
			return nil, err
		}
		items = append(items, item)
		total += item.Count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 计算百分比
	for i := range items {
		if total > 0 {
			items[i].Percentage = float64(items[i].Count) * 100 / float64(total)
		}
	}
	return items, nil
}

func (h *ErrorsHandler) queryRecentErrors(ctx context.Context, params QueryParams, win sessionWindow) ([]ErrorTrendItem, error) {
	whereClause, args, argIdx := requestLogFactsWhere(win, params)

	// ★ 旧列 → 新列映射：created_at → ts（窗口列 / 日桶列同源）。
	dayExpr := win.dayExpr("r.ts", &args, &argIdx)

	query := `
		SELECT
			TO_CHAR(` + dayExpr + `, 'YYYY-MM-DD') as date,
			COUNT(*) FILTER (WHERE r.request_status IN ('failure','rate_limited')) as error_count,
			COUNT(*) as total_count
		FROM request_logs_with_current_month r
		` + whereClause + `
		GROUP BY ` + dayExpr + `
		ORDER BY date
	`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]ErrorTrendItem, 0)
	for rows.Next() {
		var item ErrorTrendItem
		if err := rows.Scan(&item.Date, &item.ErrorCount, &item.TotalCount); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (h *ErrorsHandler) queryTopErrors(ctx context.Context, params QueryParams, win sessionWindow) ([]ErrorDetail, error) {
	whereClause, args, _ := requestLogFactsWhere(win, params)
	whereClause += " AND r.request_status IN ('failure','rate_limited')"

	// ★ 旧列 → 新列映射：
	//   error_message → error_kind（★ 请求级日志**没有 error_message 列**：
	//     canonical 视图 118 列契约里只有 error_kind / failure_stage /
	//     failure_detail_code，见 sql/migrations/startup/890_drop_request_logs_v1_family.sql
	//     ⇒ 响应字段名 error_message 保持不变，但装的是权威错误分类；
	//     保留空串而不是置 NULL，免得前端把「无分类」渲染成缺口）。
	//   completed_at → ts（请求级日志只有一个时间轴：ts）。
	//   module_name → failure_stage（★ 该视图没有模块维度；failure_stage
	//     是「失败发生在哪一段」（gateway / upstream），是请求级日志里最接近
	//     「模块」的位置维度）。
	query := fmt.Sprintf(`
		SELECT
			COALESCE(NULLIF(r.error_kind, ''), 'unknown') as error_message,
			COUNT(*) as count,
			MAX(r.ts) as last_occurred,
			COALESCE(NULLIF(r.failure_stage, ''), 'unknown') as module
		FROM request_logs_with_current_month r
		%s
		GROUP BY r.error_kind, r.failure_stage
		ORDER BY count DESC
		LIMIT 10
	`, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]ErrorDetail, 0)
	for rows.Next() {
		var item ErrorDetail
		if err := rows.Scan(&item.ErrorMessage, &item.Count, &item.LastOccurred, &item.Module); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// writeDegraded 写一个"缺表降级"的 200 响应，包含调用时记录的
// op 名称 + 缺失视图。前端拿到 HTTP 200 + degraded:true 时按 0 值
// 渲染，仍然展示其它已经可用的指标卡片。
func (h *ErrorsHandler) writeDegraded(w http.ResponseWriter, startTime time.Time, op string, err error, win sessionWindow) {
	view := dashboarddegrade.ExtractRelationName(err)
	now := time.Now()
	writeSuccessJSON(w, ErrorStatsResponse{
		// ★ 降级路径**照样披露口径**：前端要能区分「这个面本来就没数据」
		//   与「真源视图缺失导致取不到数」。
		DataSource:   requestLogFactsDataSource,
		ProbeScope:   probeScopeValue(win),
		Summary:      ErrorSummary{},
		Distribution: []ErrorDistItem{},
		Trend:        []ErrorTrendItem{},
		TopErrors:    []ErrorDetail{},
	}, &Metadata{
		GeneratedAt: now,
		TookMs:      time.Since(startTime).Milliseconds(),
		Degraded:    true,
		MissingView: view,
		Hint:        "数据视图尚未初始化，请先执行数据聚合迁移",
	})
}
