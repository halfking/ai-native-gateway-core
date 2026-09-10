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
	//
	// Audit fix (2026-06-26): 030_tool_registry_enhancements.sql defines
	//   UNIQUE (tool_id, tenant_id, usage_date)
	// on the plain tool_usage_stats table. Local r112 / sandbox schemas
	// sometimes partition this table by created_at and the matching
	// constraint becomes
	//   UNIQUE (tool_id, tenant_id, usage_date, created_at)
	// (constraint name
	//   tool_usage_stats_partitioned_tool_id_tenant_id_usage_date_c_key).
	// INSERT targets tool_usage_stats_hot (the canonical write target per
	// the 2026-07 hot-table architecture, guaranteed by startup migration
	// 348 on every boot).
	//
	// 2026-09-10 (audit R9 candidate 11): the old fallback on hot-insert
	// failure wrote straight into the partitioned parent, splitting one
	// logical day across hot and parent rows and double-counting the
	// union view reads. Hot is a boot-migration guarantee — a missing hot
	// table is a schema fault and must surface as an error, not silently
	// fork the write path.
	query := `
		INSERT INTO tool_usage_stats_hot
			(tool_id, tenant_id, usage_date, call_count, success_count, error_count, avg_latency_ms, last_called_at)
		VALUES ($1, $2, CURRENT_DATE, 1, $3, $4, $5, NOW())
		ON CONFLICT (tool_id, tenant_id, usage_date)
		DO UPDATE SET
			call_count = tool_usage_stats_hot.call_count + 1,
			success_count = tool_usage_stats_hot.success_count + $3,
			error_count = tool_usage_stats_hot.error_count + $4,
			avg_latency_ms = (tool_usage_stats_hot.avg_latency_ms * tool_usage_stats_hot.call_count + $5) / (tool_usage_stats_hot.call_count + 1),
			last_called_at = NOW(),
			updated_at = NOW()
	`

	var successDelta, errorDelta int64
	if status == "success" {
		successDelta = 1
	} else {
		errorDelta = 1
	}

	if _, err := tr.db.Exec(ctx, query, toolID, tenantID, successDelta, errorDelta, latencyMs); err != nil {
		return fmt.Errorf("failed to update tool_usage_stats_hot: %w", err)
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
//
// 2026-09-10 (audit R9 candidate 11): 读 tool_usage_stats_with_current_month
// 视图（348 建，hot ∪ parent）而非直查父表——父表缺当前 8h 热窗口数据。
// 按天 GROUP BY 聚合：hot 与 parent 对同一 (tool,tenant,date) 理论上不重叠
// （写入只落 hot，promote 原子搬运），但历史 fallback 写入已造成过的双行
// 分裂若存在，聚合保证每天恰好一行、计数不丢。
func (tr *ToolRegistry) GetUsageStats(ctx context.Context, toolID, tenantID string, days int) ([]*UsageStats, error) {
	if tr.db == nil {
		return nil, nil
	}

	query := `
		SELECT tool_id, tenant_id, usage_date,
		       SUM(call_count)::bigint, SUM(success_count)::bigint, SUM(error_count)::bigint,
		       COALESCE(SUM(avg_latency_ms::bigint * call_count) / NULLIF(SUM(call_count), 0), 0)::int,
		       MAX(last_called_at)
		FROM tool_usage_stats_with_current_month
		WHERE usage_date >= CURRENT_DATE - ($1::int * INTERVAL '1 day')
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

	query += " GROUP BY tool_id, tenant_id, usage_date ORDER BY usage_date DESC, tool_id"

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
// 2026-09-10 (audit R9 candidate 11): 同 GetUsageStats，改查联合视图
// 覆盖 8h 热窗口；SUM 已按 union 全量求和，天然免疫双行分裂的丢读。
func (tr *ToolRegistry) GetTopTools(ctx context.Context, tenantID string, limit int, days int) ([]*UsageStats, error) {
	if tr.db == nil {
		return nil, nil
	}

	query := `
		SELECT tool_id, tenant_id, SUM(call_count)::bigint as total_calls, SUM(success_count)::bigint as total_success, SUM(error_count)::bigint as total_error
		FROM tool_usage_stats_with_current_month
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
