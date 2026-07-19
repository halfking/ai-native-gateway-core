package trace

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// writeStageEvents 解析 trace_events JSONB 并写入 request_stage_events 规范化表。
// 供 FlushToPG 调用，实现 migration 434 的阶段追踪功能。
func (r *RedisRecorder) writeStageEvents(ctx context.Context, db *pgxpool.Pool, requestID string, traceJSON string) error {
	if db == nil || requestID == "" || traceJSON == "" {
		return nil
	}

	// 解析 trace envelope
	trace, err := unmarshalTrace(traceJSON, requestID)
	if err != nil {
		return err
	}

	// 批量插入 request_stage_events
	batch := &pgx.Batch{}
	for _, ev := range trace.Events {
		// 提取关键字段
		httpStatus := extractInt(ev.Details, "http_status")
		responseBody := extractString(ev.Details, "response_body")
		failureHint := extractString(ev.Details, "failure_hint")

		// 构造 details JSONB（去除已提取的字段，避免冗余）
		details := make(map[string]any)
		for k, v := range ev.Details {
			if k != "http_status" && k != "response_body" && k != "failure_hint" {
				details[k] = v
			}
		}
		detailsJSON, _ := json.Marshal(details)
		if len(details) == 0 {
			detailsJSON = []byte("{}")
		}

		// Snapshot 转 JSON（如果存在）
		var snapshotJSON any
		if ev.Snapshot != nil {
			b, _ := json.Marshal(ev.Snapshot)
			snapshotJSON = b
		}

		batch.Queue(`
			INSERT INTO request_stage_events (
				request_id, seq, stage, module, event_timestamp, duration_ms, status,
				error_message, http_status, response_body, failure_hint, details, snapshot
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12::jsonb, $13::jsonb
			)
			ON CONFLICT DO NOTHING
		`, requestID, ev.Seq, ev.Stage, ev.Module, ev.Timestamp, ev.DurationMs, ev.Status,
			ev.Error, httpStatus, responseBody, failureHint, detailsJSON, snapshotJSON)
	}

	// 批量执行
	results := db.SendBatch(ctx, batch)
	defer results.Close()

	// 消费所有结果（忽略错误，非阻塞）
	for i := 0; i < len(trace.Events); i++ {
		_, _ = results.Exec()
	}

	return nil
}

// extractInt 从 map[string]any 中提取 int，不存在返回 nil
func extractInt(m map[string]any, key string) *int {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case int:
		return &val
	case int64:
		i := int(val)
		return &i
	case float64:
		i := int(val)
		return &i
	default:
		return nil
	}
}

// extractString 从 map[string]any 中提取 string，不存在返回空字符串
func extractString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
