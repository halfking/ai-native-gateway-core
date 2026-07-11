// Package dashboardapi - module_stats.go
// 模块执行统计 API：查询各模块的执行次数、耗时、缓存命中率
package dashboardapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ModuleStatsHandler 模块执行统计 Handler
type ModuleStatsHandler struct {
	db *pgxpool.Pool
}

// NewModuleStatsHandler 创建 Handler
func NewModuleStatsHandler(db *pgxpool.Pool) *ModuleStatsHandler {
	return &ModuleStatsHandler{db: db}
}

// ModuleStatsItem 模块统计项
type ModuleStatsItem struct {
	ModuleName      string     `json:"module_name"`
	ModuleVersion   string     `json:"module_version"`
	TotalExecutions int        `json:"total_executions"`
	SuccessCount    int        `json:"success_count"`
	FailedCount     int        `json:"failed_count"`
	SkippedCount    int        `json:"skipped_count"`
	AvgDurationMs   float64    `json:"avg_duration_ms"`
	P95DurationMs   float64    `json:"p95_duration_ms"`
	CacheHitRate    float64    `json:"cache_hit_rate"`
	UniqueSessions  int        `json:"unique_sessions"`
	LastExecutedAt  *time.Time `json:"last_executed_at,omitempty"`
}

// ModuleStatsResponse 模块统计响应
type ModuleStatsResponse struct {
	Modules     []ModuleStatsItem  `json:"modules"`
	Summary     ModuleStatsSummary `json:"summary"`
	PeriodStart time.Time          `json:"period_start"`
	PeriodEnd   time.Time          `json:"period_end"`
}

// ModuleStatsSummary 模块统计摘要
type ModuleStatsSummary struct {
	TotalModules    int     `json:"total_modules"`
	TotalExecutions int     `json:"total_executions"`
	AvgCacheHitRate float64 `json:"avg_cache_hit_rate"`
	AvgDurationMs   float64 `json:"avg_duration_ms"`
}

// HandleModuleStats 处理模块执行统计请求
//
// GET /api/admin/dashboard/module-stats
func (h *ModuleStatsHandler) HandleModuleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "method not allowed", "")
		return
	}

	startTime := time.Now()
	apiStatus := "success"
	defer func() {
		recordAPIRequest("module-stats", apiStatus, time.Since(startTime))
	}()

	params, _, ok := prepareDashboardRequest(w, r, h.db)
	if !ok {
		return
	}
	ctx, cancel := GetRequestContext(r, 15*time.Second)
	defer cancel()

	where := []string{"created_at >= NOW() - INTERVAL '7 days'"}
	args := []interface{}{}
	argIdx := 1
	appendExecutionScope(&where, params, &args, &argIdx, "")

	query := fmt.Sprintf(`
		SELECT
			module_name,
			MAX(module_version) as module_version,
			COUNT(*) as total_executions,
			COUNT(*) FILTER (WHERE status = 'completed') as success_count,
			COUNT(*) FILTER (WHERE status = 'failed') as failed_count,
			COUNT(*) FILTER (WHERE status = 'skipped') as skipped_count,
			COALESCE(AVG(duration_ms) FILTER (WHERE status = 'completed'), 0) as avg_duration_ms,
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE status = 'completed'), 0) as p95_duration_ms,
			COUNT(DISTINCT gw_session_id) as unique_sessions,
			MAX(completed_at) as last_executed_at
			FROM session_module_executions_hot
			WHERE %s
			GROUP BY module_name
			ORDER BY total_executions DESC
		`, joinStrings(where, " AND "))

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query module stats", err.Error())
		return
	}
	defer rows.Close()

	modules := make([]ModuleStatsItem, 0)
	totalExecs := 0
	totalCacheRate := 0.0
	totalDuration := 0.0

	for rows.Next() {
		var item ModuleStatsItem
		if err := rows.Scan(
			&item.ModuleName, &item.ModuleVersion,
			&item.TotalExecutions, &item.SuccessCount, &item.FailedCount, &item.SkippedCount,
			&item.AvgDurationMs, &item.P95DurationMs,
			&item.UniqueSessions, &item.LastExecutedAt,
		); err != nil {
			apiStatus = "error"
			writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to scan module stats", err.Error())
			return
		}
		// 计算缓存命中率（skipped 表示缓存命中）
		if item.TotalExecutions > 0 {
			item.CacheHitRate = float64(item.SkippedCount) * 100 / float64(item.TotalExecutions)
		}
		modules = append(modules, item)
		totalExecs += item.TotalExecutions
		totalCacheRate += item.CacheHitRate
		totalDuration += item.AvgDurationMs
	}

	summary := ModuleStatsSummary{
		TotalModules:    len(modules),
		TotalExecutions: totalExecs,
	}
	if len(modules) > 0 {
		summary.AvgCacheHitRate = totalCacheRate / float64(len(modules))
		summary.AvgDurationMs = totalDuration / float64(len(modules))
	}

	now := time.Now()
	resp := ModuleStatsResponse{
		Modules:     modules,
		Summary:     summary,
		PeriodStart: now.AddDate(0, 0, -7),
		PeriodEnd:   now,
	}

	metadata := &Metadata{
		GeneratedAt: now,
		TookMs:      time.Since(startTime).Milliseconds(),
	}
	writeSuccessJSON(w, resp, metadata)
}
