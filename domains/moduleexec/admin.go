// Package moduleexec - admin.go
// 提供管理员接口：查询、清理由会话模块执行记录表产生的数据
package moduleexec

import (
	"context"
	"fmt"
	"time"
)

// AdminService 管理员服务
type AdminService struct {
	db       DBTX
	executor *Executor
}

// NewAdminService 创建管理员服务
func NewAdminService(db DBTX, executor *Executor) *AdminService {
	return &AdminService{
		db:       db,
		executor: executor,
	}
}

// ModuleExecutionStats 模块执行统计
type ModuleExecutionStats struct {
	ModuleName        string  `json:"module_name"`
	Status            string  `json:"status"`
	ExecutionCount    int64   `json:"execution_count"`
	UniqueSessions    int64   `json:"unique_sessions"`
	AvgDurationMs     float64 `json:"avg_duration_ms"`
	P50DurationMs     float64 `json:"p50_duration_ms"`
	P95DurationMs     float64 `json:"p95_duration_ms"`
	P99DurationMs     float64 `json:"p99_duration_ms"`
	ExecutionsLastHour int64  `json:"executions_last_hour"`
}

// ModuleCacheHitRate 模块缓存命中率
type ModuleCacheHitRate struct {
	ModuleName    string  `json:"module_name"`
	TotalExecutions int64 `json:"total_executions"`
	CacheSkips    int64   `json:"cache_skips"`
	SkipRatePct   float64 `json:"skip_rate_pct"`
}

// SessionExecutionSummary 会话执行汇总
type SessionExecutionSummary struct {
	GwSessionID    string                  `json:"gw_session_id"`
	TenantID       string                  `json:"tenant_id"`
	ModuleStatuses map[string]string       `json:"module_statuses"`
	LastUpdated    time.Time               `json:"last_updated"`
}

// GetModuleStats 获取模块执行统计
func (s *AdminService) GetModuleStats(ctx context.Context, hours int) ([]ModuleExecutionStats, error) {
	if hours <= 0 {
		hours = 24
	}

	query := `
		SELECT 
			module_name,
			status,
			COUNT(*) as execution_count,
			COUNT(DISTINCT gw_session_id) as unique_sessions,
			AVG(duration_ms)::FLOAT as avg_duration_ms,
			PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY duration_ms)::FLOAT as p50_duration_ms,
			PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms)::FLOAT as p95_duration_ms,
			PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY duration_ms)::FLOAT as p99_duration_ms,
			COUNT(*) FILTER (WHERE created_at > NOW() - INTERVAL '1 hour') as executions_last_hour
		FROM session_module_executions_hot
		WHERE created_at > NOW() - ($1 || ' hours')::INTERVAL
		GROUP BY module_name, status
		ORDER BY module_name, status
	`

	rows, err := s.db.Query(ctx, query, hours)
	if err != nil {
		return nil, fmt.Errorf("query module stats: %w", err)
	}
	defer rows.Close()

	stats := make([]ModuleExecutionStats, 0)
	for rows.Next() {
		var stat ModuleExecutionStats
		if err := rows.Scan(
			&stat.ModuleName,
			&stat.Status,
			&stat.ExecutionCount,
			&stat.UniqueSessions,
			&stat.AvgDurationMs,
			&stat.P50DurationMs,
			&stat.P95DurationMs,
			&stat.P99DurationMs,
			&stat.ExecutionsLastHour,
		); err != nil {
			return nil, fmt.Errorf("scan module stats: %w", err)
		}
		stats = append(stats, stat)
	}

	return stats, nil
}

// GetCacheHitRate 获取缓存命中率
func (s *AdminService) GetCacheHitRate(ctx context.Context, hours int) ([]ModuleCacheHitRate, error) {
	if hours <= 0 {
		hours = 24
	}

	query := `
		SELECT 
			module_name,
			COUNT(*) FILTER (WHERE status = 'completed') as total_executions,
			COUNT(*) FILTER (WHERE status = 'skipped') as cache_skips,
			ROUND(
				COUNT(*) FILTER (WHERE status = 'skipped') * 100.0 / 
				NULLIF(COUNT(*), 0), 2
			)::FLOAT as skip_rate_pct
		FROM session_module_executions_hot
		WHERE created_at > NOW() - ($1 || ' hours')::INTERVAL
		GROUP BY module_name
		ORDER BY module_name
	`

	rows, err := s.db.Query(ctx, query, hours)
	if err != nil {
		return nil, fmt.Errorf("query cache hit rate: %w", err)
	}
	defer rows.Close()

	rates := make([]ModuleCacheHitRate, 0)
	for rows.Next() {
		var rate ModuleCacheHitRate
		if err := rows.Scan(
			&rate.ModuleName,
			&rate.TotalExecutions,
			&rate.CacheSkips,
			&rate.SkipRatePct,
		); err != nil {
			return nil, fmt.Errorf("scan cache hit rate: %w", err)
		}
		rates = append(rates, rate)
	}

	return rates, nil
}

// GetSessionSummary 获取会话执行汇总
func (s *AdminService) GetSessionSummary(ctx context.Context, sessionID string) (*SessionExecutionSummary, error) {
	query := `
		SELECT 
			tenant_id,
			module_name,
			status,
			MAX(updated_at) as last_updated
		FROM session_module_executions_hot
		WHERE gw_session_id = $1
		GROUP BY tenant_id, module_name, status
	`

	rows, err := s.db.Query(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session summary: %w", err)
	}
	defer rows.Close()

	summary := &SessionExecutionSummary{
		GwSessionID:    sessionID,
		ModuleStatuses: make(map[string]string),
	}

	moduleStatusMap := make(map[string]map[string]int) // module -> status -> count

	for rows.Next() {
		var (
			tenantID, moduleName, status string
			lastUpdated                  time.Time
		)
		if err := rows.Scan(&tenantID, &moduleName, &status, &lastUpdated); err != nil {
			return nil, fmt.Errorf("scan session summary: %w", err)
		}

		summary.TenantID = tenantID
		if lastUpdated.After(summary.LastUpdated) {
			summary.LastUpdated = lastUpdated
		}

		if _, ok := moduleStatusMap[moduleName]; !ok {
			moduleStatusMap[moduleName] = make(map[string]int)
		}
		moduleStatusMap[moduleName][status]++
	}

	// 确定每个模块的主导状态
	for module, statuses := range moduleStatusMap {
		maxCount := 0
		dominantStatus := "unknown"
		for status, count := range statuses {
			if count > maxCount {
				maxCount = count
				dominantStatus = status
			}
		}
		summary.ModuleStatuses[module] = dominantStatus
	}

	return summary, nil
}

// CleanupExpiredRecords 清理过期记录
func (s *AdminService) CleanupExpiredRecords(ctx context.Context) (int64, error) {
	query := `
		DELETE FROM session_module_executions_hot
		WHERE expires_at < NOW() - INTERVAL '1 day'
	`

	result, err := s.db.Exec(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("cleanup expired: %w", err)
	}

	return result.RowsAffected(), nil
}

// ArchiveOldRecords 归档旧记录（调用数据库函数）
func (s *AdminService) ArchiveOldRecords(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		retentionDays = 7
	}

	var archived int64
	err := s.db.QueryRow(ctx, "SELECT * FROM archive_session_module_executions($1)", retentionDays).Scan(&archived)
	if err != nil {
		return 0, fmt.Errorf("archive old records: %w", err)
	}

	return archived, nil
}

// GetFailedExecutions 获取最近的失败执行
func (s *AdminService) GetFailedExecutions(ctx context.Context, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT 
			execution_id,
			gw_session_id,
			tenant_id,
			module_name,
			error_message,
			duration_ms,
			started_at,
			completed_at
		FROM session_module_executions_hot
		WHERE status = 'failed'
		ORDER BY completed_at DESC
		LIMIT $1
	`

	rows, err := s.db.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("query failed executions: %w", err)
	}
	defer rows.Close()

	failures := make([]map[string]interface{}, 0)
	for rows.Next() {
		var (
			execID       int64
			sessionID    string
			tenantID     string
			moduleName   string
			errorMessage *string
			durationMs   int
			startedAt    time.Time
			completedAt  *time.Time
		)
		if err := rows.Scan(&execID, &sessionID, &tenantID, &moduleName, &errorMessage, &durationMs, &startedAt, &completedAt); err != nil {
			return nil, fmt.Errorf("scan failed executions: %w", err)
		}

		failure := map[string]interface{}{
			"execution_id": execID,
			"session_id":   sessionID,
			"tenant_id":    tenantID,
			"module_name":  moduleName,
			"duration_ms":  durationMs,
			"started_at":   startedAt,
		}
		if errorMessage != nil {
			failure["error_message"] = *errorMessage
		}
		if completedAt != nil {
			failure["completed_at"] = *completedAt
		}

		failures = append(failures, failure)
	}

	return failures, nil
}
