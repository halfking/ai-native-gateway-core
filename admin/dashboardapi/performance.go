// Package dashboardapi - performance.go
// 性能指标 API：查询系统性能指标（延迟、吞吐量、资源使用）
package dashboardapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PerformanceHandler 性能指标 Handler
type PerformanceHandler struct {
	db *pgxpool.Pool
}

// NewPerformanceHandler 创建 Handler
func NewPerformanceHandler(db *pgxpool.Pool) *PerformanceHandler {
	return &PerformanceHandler{db: db}
}

// PerformanceResponse 性能指标响应
type PerformanceResponse struct {
	Summary     PerformanceSummary  `json:"summary"`
	LatencyDist LatencyDistribution `json:"latency_distribution"`
	Throughput  []ThroughputPoint   `json:"throughput"`
	SlowQueries []SlowQueryItem     `json:"slow_queries"`
}

// PerformanceSummary 性能摘要
type PerformanceSummary struct {
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
	P50LatencyMs  float64 `json:"p50_latency_ms"`
	P95LatencyMs  float64 `json:"p95_latency_ms"`
	P99LatencyMs  float64 `json:"p99_latency_ms"`
	MaxLatencyMs  float64 `json:"max_latency_ms"`
	TotalRequests int     `json:"total_requests"`
	AvgThroughput float64 `json:"avg_throughput_rps"`
}

// LatencyDistribution 延迟分布
type LatencyDistribution struct {
	Under100ms  int `json:"under_100ms"`
	Under500ms  int `json:"under_500ms"`
	Under1000ms int `json:"under_1000ms"`
	Under5000ms int `json:"under_5000ms"`
	Over5000ms  int `json:"over_5000ms"`
}

// ThroughputPoint 吞吐量点
type ThroughputPoint struct {
	Date         string  `json:"date"`
	RequestCount int     `json:"request_count"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// SlowQueryItem 慢查询项
type SlowQueryItem struct {
	SessionKey   string    `json:"session_key"`
	ModuleName   string    `json:"module_name"`
	DurationMs   int       `json:"duration_ms"`
	ExecutedAt   time.Time `json:"executed_at"`
	ErrorMessage string    `json:"error_message,omitempty"`
}

// HandlePerformance 处理性能指标请求
//
// GET /api/admin/dashboard/performance
func (h *PerformanceHandler) HandlePerformance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "method not allowed", "")
		return
	}

	startTime := time.Now()
	apiStatus := "success"
	defer func() {
		recordAPIRequest("performance", apiStatus, time.Since(startTime))
	}()

	params, _, ok := prepareDashboardRequest(w, r, h.db)
	if !ok {
		return
	}
	ctx, cancel := GetRequestContext(r, 15*time.Second)
	defer cancel()

	// 1. 性能摘要
	summary, err := h.queryPerformanceSummary(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query performance summary", err.Error())
		return
	}

	// 2. 延迟分布
	latencyDist, err := h.queryLatencyDistribution(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query latency distribution", err.Error())
		return
	}

	// 3. 吞吐量趋势
	throughput, err := h.queryThroughput(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query throughput", err.Error())
		return
	}

	// 4. 慢查询
	slowQueries, err := h.querySlowQueries(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query slow queries", err.Error())
		return
	}

	resp := PerformanceResponse{
		Summary:     *summary,
		LatencyDist: *latencyDist,
		Throughput:  throughput,
		SlowQueries: slowQueries,
	}

	metadata := &Metadata{
		GeneratedAt: time.Now(),
		TookMs:      time.Since(startTime).Milliseconds(),
	}
	writeSuccessJSON(w, resp, metadata)
}

func (h *PerformanceHandler) queryPerformanceSummary(ctx context.Context, params QueryParams) (*PerformanceSummary, error) {
	summary := &PerformanceSummary{}
	where := []string{fmt.Sprintf("created_at >= NOW() - INTERVAL '%d days'", params.Days)}
	args := []interface{}{}
	argIdx := 1

	appendExecutionScope(&where, params, &args, &argIdx, "")
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total_requests,
			COALESCE(AVG(duration_ms), 0) as avg_latency,
			COALESCE(PERCENTILE_CONT(0.50) WITHIN GROUP (ORDER BY duration_ms), 0) as p50_latency,
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms), 0) as p95_latency,
			COALESCE(PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY duration_ms), 0) as p99_latency,
			COALESCE(MAX(duration_ms), 0) as max_latency
		FROM session_module_executions_hot
		%s
	`, whereClause)

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&summary.TotalRequests, &summary.AvgLatencyMs,
		&summary.P50LatencyMs, &summary.P95LatencyMs, &summary.P99LatencyMs, &summary.MaxLatencyMs,
	)
	if err != nil {
		return nil, err
	}

	// 计算平均吞吐量（请求/秒）
	if params.Days > 0 {
		summary.AvgThroughput = float64(summary.TotalRequests) / float64(params.Days*24*3600)
	}
	return summary, nil
}

func (h *PerformanceHandler) queryLatencyDistribution(ctx context.Context, params QueryParams) (*LatencyDistribution, error) {
	dist := &LatencyDistribution{}
	where := []string{fmt.Sprintf("created_at >= NOW() - INTERVAL '%d days'", params.Days)}
	args := []interface{}{}
	argIdx := 1

	appendExecutionScope(&where, params, &args, &argIdx, "")
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) FILTER (WHERE duration_ms < 100) as under_100ms,
			COUNT(*) FILTER (WHERE duration_ms >= 100 AND duration_ms < 500) as under_500ms,
			COUNT(*) FILTER (WHERE duration_ms >= 500 AND duration_ms < 1000) as under_1000ms,
			COUNT(*) FILTER (WHERE duration_ms >= 1000 AND duration_ms < 5000) as under_5000ms,
			COUNT(*) FILTER (WHERE duration_ms >= 5000) as over_5000ms
		FROM session_module_executions_hot
		%s
	`, whereClause)

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&dist.Under100ms, &dist.Under500ms, &dist.Under1000ms, &dist.Under5000ms, &dist.Over5000ms,
	)
	return dist, err
}

func (h *PerformanceHandler) queryThroughput(ctx context.Context, params QueryParams) ([]ThroughputPoint, error) {
	where := []string{fmt.Sprintf("created_at >= NOW() - INTERVAL '%d days'", params.Days)}
	args := []interface{}{}
	argIdx := 1

	appendExecutionScope(&where, params, &args, &argIdx, "")
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			TO_CHAR(DATE(created_at), 'YYYY-MM-DD') as date,
			COUNT(*) as request_count,
			COALESCE(AVG(duration_ms), 0) as avg_latency
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

	items := make([]ThroughputPoint, 0)
	for rows.Next() {
		var item ThroughputPoint
		if err := rows.Scan(&item.Date, &item.RequestCount, &item.AvgLatencyMs); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (h *PerformanceHandler) querySlowQueries(ctx context.Context, params QueryParams) ([]SlowQueryItem, error) {
	where := []string{
		fmt.Sprintf("created_at >= NOW() - INTERVAL '%d days'", params.Days),
		"duration_ms > 1000",
	}
	args := []interface{}{}
	argIdx := 1

	appendExecutionScope(&where, params, &args, &argIdx, "")
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			gw_session_id,
			COALESCE(module_name, 'unknown') as module_name,
			duration_ms,
			COALESCE(completed_at, created_at) as executed_at,
			COALESCE(error_message, '') as error_message
		FROM session_module_executions_hot
		%s
		ORDER BY duration_ms DESC
		LIMIT 20
	`, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]SlowQueryItem, 0)
	for rows.Next() {
		var item SlowQueryItem
		if err := rows.Scan(&item.SessionKey, &item.ModuleName, &item.DurationMs, &item.ExecutedAt, &item.ErrorMessage); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
