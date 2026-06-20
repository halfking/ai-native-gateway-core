package registry

import (
	"context"
	"fmt"
	"time"
)

// RecordToolCall 记录工具调用（Phase 3.3: 使用统计）
// 使用 UPSERT 更新每日统计
func (tr *ToolRegistry) RecordToolCall(ctx context.Context, toolID, tenantID, status string, latencyMs int, requestID, apiKey, errorCode string) error {
	if tr.db == nil {
		return nil // stats disabled
	}
	if tenantID == "" {
		tenantID = "default"
	}

	// 1. 更新每日统计表（UPSERT）
	query := `
		INSERT INTO tool_usage_stats 
			(tool_id, tenant_id, usage_date, call_count, success_count, error_count, avg_latency_ms, last_called_at)
		VALUES ($1, $2, CURRENT_DATE, 1, $3, $4, $5, NOW())
		ON CONFLICT (tool_id, tenant_id, usage_date)
		DO UPDATE SET 
			call_count = tool_usage_stats.call_count + 1,
			success_count = tool_usage_stats.success_count + $3,
			error_count = tool_usage_stats.error_count + $4,
			avg_latency_ms = (tool_usage_stats.avg_latency_ms * tool_usage_stats.call_count + $5) / (tool_usage_stats.call_count + 1),
			last_called_at = NOW(),
			updated_at = NOW()
	`

	var successDelta, errorDelta int64
	if status == "success" {
		successDelta = 1
	} else {
		errorDelta = 1
	}

	_, err := tr.db.Exec(ctx, query, toolID, tenantID, successDelta, errorDelta, latencyMs)
	if err != nil {
		return fmt.Errorf("failed to update tool_usage_stats: %w", err)
	}

	// 2. 记录详细事件（异步，不阻塞主流程）
	go func() {
		eventCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		eventQuery := `
			INSERT INTO tool_call_events 
				(tool_id, tenant_id, request_id, api_key, status, latency_ms, error_code)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`
		_, _ = tr.db.Exec(eventCtx, eventQuery, toolID, tenantID, requestID, apiKey, status, latencyMs, errorCode)
	}()

	return nil
}

// GetUsageStats 获取工具使用统计
func (tr *ToolRegistry) GetUsageStats(ctx context.Context, toolID, tenantID string, days int) ([]*UsageStats, error) {
	if tr.db == nil {
		return nil, nil
	}

	query := `
		SELECT tool_id, tenant_id, usage_date, call_count, success_count, error_count, avg_latency_ms, last_called_at
		FROM tool_usage_stats
		WHERE usage_date >= CURRENT_DATE - $1
	`
	args := []interface{}{days}

	if toolID != "" {
		query += " AND tool_id = $2"
		args = append(args, toolID)
	}
	if tenantID != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", len(args)+1)
		args = append(args, tenantID)
	}

	query += " ORDER BY usage_date DESC, tool_id"

	rows, err := tr.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []*UsageStats
	for rows.Next() {
		s := &UsageStats{}
		var lastCalled *time.Time
		err := rows.Scan(&s.ToolID, &s.TenantID, &s.UsageDate, &s.CallCount, &s.SuccessCount, &s.ErrorCount, &s.AvgLatencyMs, &lastCalled)
		if err != nil {
			return nil, err
		}
		if lastCalled != nil {
			s.LastCalledAt = *lastCalled
		}
		stats = append(stats, s)
	}

	return stats, nil
}

// GetTopTools 获取最常用的工具
func (tr *ToolRegistry) GetTopTools(ctx context.Context, tenantID string, limit int, days int) ([]*UsageStats, error) {
	if tr.db == nil {
		return nil, nil
	}

	query := `
		SELECT tool_id, tenant_id, SUM(call_count) as total_calls, SUM(success_count) as total_success, SUM(error_count) as total_error
		FROM tool_usage_stats
		WHERE usage_date >= CURRENT_DATE - $1
	`
	args := []interface{}{days}

	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}

	query += fmt.Sprintf(" GROUP BY tool_id, tenant_id ORDER BY total_calls DESC LIMIT $%d", len(args)+1)
	args = append(args, limit)

	rows, err := tr.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []*UsageStats
	for rows.Next() {
		s := &UsageStats{}
		err := rows.Scan(&s.ToolID, &s.TenantID, &s.CallCount, &s.SuccessCount, &s.ErrorCount)
		if err != nil {
			return nil, err
		}
		stats = append(stats, s)
	}

	return stats, nil
}