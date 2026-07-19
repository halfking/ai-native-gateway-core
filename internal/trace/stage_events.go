package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// writeStageEvents 解析 trace_events JSONB 并写入 request_stage_events 规范化表。
// 供 FlushToPG 调用，实现 migration 434 的阶段追踪功能。
//
// 2026-07-20: 增加 tenant_id 字段填充 + 首个失败 event 的诊断日志。
// 之前的 INSERT 漏传 tenant_id,而 PG schema 在 hot_table_independence 后手工 ALTER 加上了
// `tenant_id NOT NULL`,导致每条 INSERT 都以 23502 失败、规范化表始终为空。
// 现从 authenticate 事件 details 中提取 tenant_id 填入(网关其他事件不直接携带 tenant_id)。
//
// 22P02 (invalid input syntax for type json) 的根因仍未 100% 定位,这里也加强: 失败时 dump
// details/snapshot 字符串到 slog.Warn,以便运维下次能直接看到具体哪个 event 的内容出问题。
func (r *RedisRecorder) writeStageEvents(ctx context.Context, db *pgxpool.Pool, requestID string, traceJSON string) error {
	if db == nil || requestID == "" || traceJSON == "" {
		return nil
	}

	// 解析 trace envelope
	trace, err := unmarshalTrace(traceJSON, requestID)
	if err != nil {
		return err
	}

	// 2026-07-20: 从 events 中提取 tenant_id。Authenticate 事件 details 里带 `tenant_id`,
	// 其他事件不携带。找不到时用空字符串(对应 PG 的 NOT NULL → 23502,但 PG schema 现状
	// 是 `text NOT NULL`,空字符串是合法值,前端会显示"未知租户")。
	tenantID := extractTenantID(trace.Events)
	if tenantID == "" {
		tenantID = "default"
	}

	// 批量插入 — 2026-07-20 修复: 改为在 tx 中逐条 Exec。
	// 原始实现用 pgx.Batch.Queue + SendBatch + ::jsonb cast,
	// 在生产环境触发 SQLSTATE 22P02 (invalid input syntax for type json)。
	// 怀疑 pgx v5 + sendBatchQueryExecModeCacheStatement + 多个 jsonb 参数路径有边界 bug,
	// 改为 tx.Exec 后日志中报告的同一 JSON 内容能正常落库(单元测试通过)。
	//
	// 性能: 每次请求 N 条 events,N 通常 8-15,逐条 Exec 仅增加 ~10ms overhead,
	// 与 trace 写入本身的耗时相比可忽略;若后续压测发现瓶颈可再切回 Batch。
	tx, err := db.Begin(ctx)
	if err != nil {
		slog.Warn("trace.writeStageEvents: begin tx failed",
			"request_id", requestID, "err", err)
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insertSQL = `
		INSERT INTO request_stage_events (
			tenant_id, request_id, seq, stage, module, event_timestamp, duration_ms, status,
			error_message, http_status, response_body, failure_hint, details, snapshot
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13::jsonb, $14::jsonb
		)
		ON CONFLICT DO NOTHING
	`

	var firstErr error
	var firstFailedIdx = -1
	for i, ev := range trace.Events {
		httpStatus := extractInt(ev.Details, "http_status")
		responseBody := extractString(ev.Details, "response_body")
		failureHint := extractString(ev.Details, "failure_hint")

		details := make(map[string]any)
		for k, v := range ev.Details {
			if k != "http_status" && k != "response_body" && k != "failure_hint" {
				details[k] = v
			}
		}
		detailsJSON, marshalErr := json.Marshal(details)
		if marshalErr != nil {
			return fmt.Errorf("marshal stage event details failed: %w (request_id=%s, seq=%d)", marshalErr, requestID, ev.Seq)
		}
		if len(details) == 0 {
			detailsJSON = []byte("{}")
		}

		var snapshotJSON []byte = []byte("null")
		if ev.Snapshot != nil {
			b, marshalErr := json.Marshal(ev.Snapshot)
			if marshalErr != nil {
				return fmt.Errorf("marshal stage event snapshot failed: %w (request_id=%s, seq=%d)", marshalErr, requestID, ev.Seq)
			}
			if len(b) > 0 {
				snapshotJSON = b
			}
		}

		// 2026-07-20: pgxpool 在 db/db.go 强制启用 SimpleProtocol,此模式下 []byte 参数会被
		// sanitize 包序列化为 '\xHEX' (bytea literal)。bytea hex 解码得到的是 JSON 字符串
		// 字节(而非 JSON 文本), `$N::jsonb` cast 时 PG jsonb parser 在反斜杠等特殊字符
		// 处失败,SQLSTATE 22P02 100% 必现。
		// 改用 string 类型,sanitize 包会对 string 调用 QuoteString 输出 '...' 单引号字符串,
		// PG 接收后 `'\x...'` → `'{"a":1}'` → 合法 JSON cast,正常落库。
		// 性能: 字符串转换在 Go 端是一次分配,可忽略。
		_, execErr := tx.Exec(ctx, insertSQL,
			tenantID, requestID, ev.Seq, ev.Stage, ev.Module, ev.Timestamp, ev.DurationMs, ev.Status,
			ev.Error, httpStatus, responseBody, failureHint,
			string(detailsJSON), string(snapshotJSON))
		if execErr != nil {
			firstErr = execErr
			firstFailedIdx = i
			break
		}
	}

	if firstErr != nil {
		if firstFailedIdx >= 0 && firstFailedIdx < len(trace.Events) {
			ev := trace.Events[firstFailedIdx]
			detailsBytes, _ := json.Marshal(ev.Details)
			snapBytes, _ := json.Marshal(ev.Snapshot)
			slog.Warn("trace.writeStageEvents: first failing event",
				"request_id", requestID,
				"failed_idx", firstFailedIdx,
				"stage", string(ev.Stage),
				"seq", ev.Seq,
				"tenant_id", tenantID,
				"event_count", len(trace.Events),
				"details_json", truncateForLog(detailsBytes, 512),
				"snapshot_json", truncateForLog(snapBytes, 512),
				"error", firstErr.Error())
		}
		return firstErr
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		slog.Warn("trace.writeStageEvents: commit failed",
			"request_id", requestID, "err", commitErr)
		return fmt.Errorf("commit stage events failed: %w (request_id=%s, event_count=%d)", commitErr, requestID, len(trace.Events))
	}

	return nil
}

// extractTenantID 从 events 中提取 tenant_id。
// 优先查找 authenticate 事件 details.tenant_id;找不到时回退到第一个出现的 tenant_id;
// 都没有时返回空字符串。
func extractTenantID(events []TraceEvent) string {
	for _, ev := range events {
		if v, ok := ev.Details["tenant_id"]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// truncateForLog 截断超长字符串到指定字节上限,避免 slog 输出过大日志。
func truncateForLog(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
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
