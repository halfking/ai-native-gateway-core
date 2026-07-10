// Package dashboardapi - errors.go
// 错误统计 API：查询错误分布、错误趋势、常见错误
package dashboardapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	Summary     ErrorSummary     `json:"summary"`
	Distribution []ErrorDistItem `json:"distribution"`
	Trend       []ErrorTrendItem `json:"recent_errors"`
	TopErrors   []ErrorDetail    `json:"top_errors"`
}

// ErrorSummary 错误摘要
type ErrorSummary struct {
	TotalErrors    int     `json:"total_errors"`
	ErrorRate      float64 `json:"error_rate"`
	TotalRequests  int     `json:"total_requests"`
	AvgErrorLatency float64 `json:"avg_error_latency_ms"`
}

// ErrorDistItem 错误分布项
type ErrorDistItem struct {
	ErrorType string `json:"error_type"`
	Count     int    `json:"count"`
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

	params := ParseQueryParams(r)
	ctx, cancel := GetRequestContext(r, 15*time.Second)
	defer cancel()

	// 1. 错误摘要
	summary, err := h.queryErrorSummary(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query error summary", err.Error())
		return
	}

	// 2. 错误分布
	dist, err := h.queryErrorDistribution(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query error distribution", err.Error())
		return
	}

	// 3. 最近错误
	recentErrors, err := h.queryRecentErrors(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query recent errors", err.Error())
		return
	}

	// 4. Top 错误
	topErrors, err := h.queryTopErrors(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query top errors", err.Error())
		return
	}

	resp := ErrorStatsResponse{
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

func (h *ErrorsHandler) queryErrorSummary(ctx context.Context, params QueryParams) (*ErrorSummary, error) {
	summary := &ErrorSummary{}
	where := []string{fmt.Sprintf("created_at >= NOW() - INTERVAL '%d days'", params.Days)}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total_requests,
			COUNT(*) FILTER (WHERE status = 'failed') as total_errors,
			COALESCE(AVG(duration_ms) FILTER (WHERE status = 'failed'), 0) as avg_error_latency
		FROM session_module_executions_hot
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

func (h *ErrorsHandler) queryErrorDistribution(ctx context.Context, params QueryParams) ([]ErrorDistItem, error) {
	where := []string{
		fmt.Sprintf("created_at >= NOW() - INTERVAL '%d days'", params.Days),
		"status = 'failed'",
	}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			COALESCE(module_name, 'unknown') as error_type,
			COUNT(*) as count
		FROM session_module_executions_hot
		%s
		GROUP BY module_name
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
	// 计算百分比
	for i := range items {
		if total > 0 {
			items[i].Percentage = float64(items[i].Count) * 100 / float64(total)
		}
	}
	return items, nil
}

func (h *ErrorsHandler) queryRecentErrors(ctx context.Context, params QueryParams) ([]ErrorTrendItem, error) {
	where := []string{fmt.Sprintf("created_at >= NOW() - INTERVAL '%d days'", params.Days)}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			TO_CHAR(DATE(created_at), 'YYYY-MM-DD') as date,
			COUNT(*) FILTER (WHERE status = 'failed') as error_count,
			COUNT(*) as total_count
		FROM session_module_executions_hot
		%s
		GROUP BY DATE(created_at)
		ORDER BY date
	`, whereClause)

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
	return items, nil
}

func (h *ErrorsHandler) queryTopErrors(ctx context.Context, params QueryParams) ([]ErrorDetail, error) {
	where := []string{
		fmt.Sprintf("created_at >= NOW() - INTERVAL '%d days'", params.Days),
		"status = 'failed'",
		"error_message IS NOT NULL",
	}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			LEFT(error_message, 200) as error_message,
			COUNT(*) as count,
			MAX(completed_at) as last_occurred,
			COALESCE(module_name, 'unknown') as module
		FROM session_module_executions_hot
		%s
		GROUP BY LEFT(error_message, 200), module_name
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
	return items, nil
}
