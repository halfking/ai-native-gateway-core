// Package bg/systemmonitor — audit.go
//
// system_probe_runs 审计写入（PG）。
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §6.2 (344_system_probe_runs.sql)
package systemmonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Audit writes task results to the system_probe_runs table.
type Audit struct {
	db *pgxpool.Pool
}

// NewAudit constructs an Audit helper.
func NewAudit(db *pgxpool.Pool) *Audit {
	return &Audit{db: db}
}

// Enabled reports whether the audit writer has a live DB backend.
func (a *Audit) Enabled() bool {
	return a != nil && a.db != nil
}

// Write persists a single task execution result.
//
// The audit row is partitioned by created_at; the executor populates
// StartedAt/FinishedAt so the row's created_at ≈ started_at UTC.
//
// extras contains optional fields (skip_reason, recent_request_id,
// dns_ms, tls_ms, ...); only known fields are persisted via the
// extras JSONB column to keep the schema migration-free for Phase 1.
func (a *Audit) Write(ctx context.Context, task *Task, result *ExecutorResult, extras map[string]any) error {
	if !a.Enabled() {
		return nil // best-effort
	}
	if task == nil {
		return fmt.Errorf("audit: task is nil")
	}

	startedAt := time.Now()
	if task.StartedAt != nil {
		startedAt = *task.StartedAt
	}
	finishedAt := time.Now()
	if task.FinishedAt != nil {
		finishedAt = *task.FinishedAt
	}

	httpStatus, latencyMs := 0, 0
	errCode, errDetail, requestURL := "", "", ""
	if result != nil && result.Result != nil {
		httpStatus = result.Result.HTTPStatus
		latencyMs = result.Result.LatencyMs
		if result.Result.ErrCode != "" {
			errCode = result.Result.ErrCode
		}
		if result.Result.ErrMsg != "" {
			errDetail = truncateForAudit(result.Result.ErrMsg, 1000)
		}
		if result.Result.RequestURL != "" {
			requestURL = result.Result.RequestURL
		}
	}
	if v, ok := extras["http_status"].(int); ok {
		httpStatus = v
	}
	if v, ok := extras["latency_ms"].(int); ok {
		latencyMs = v
	}
	if v, ok := extras["err_code"].(string); ok && v != "" {
		errCode = v
	}
	if v, ok := extras["err_detail"].(string); ok && v != "" {
		errDetail = truncateForAudit(v, 1000)
	}
	if v, ok := extras["request_url"].(string); ok && v != "" {
		requestURL = v
	}

	skipReason := ""
	recentRequestID := ""
	var recentRequestAt *time.Time
	if v, ok := extras["skip_reason"].(string); ok {
		skipReason = v
	} else if task.SkipReason != "" {
		skipReason = string(task.SkipReason)
	}
	if v, ok := extras["recent_request_id"].(string); ok {
		recentRequestID = v
	} else if task.RecentRequestID != "" {
		recentRequestID = task.RecentRequestID
	}
	if task.RecentRequestAt != nil && !task.RecentRequestAt.IsZero() {
		ts := task.RecentRequestAt.UTC()
		recentRequestAt = &ts
	}

	extrasJSON, _ := json.Marshal(extras)

	insertCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := a.db.Exec(insertCtx, `
		INSERT INTO system_probe_runs (
			task_id, task_type, automaticity, credential_id, provider_id, raw_model,
			source, worker_id, status, attempt, max_attempts,
			http_status, latency_ms,
			request_url, response_body_preview,
			err_code, err_detail,
			skip_reason, recent_request_id, recent_request_at,
			started_at, finished_at,
			created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			$12, $13,
			$14, $15,
			$16, $17,
			$18, $19, $20,
			$21, $22,
			now()
		)`,
		task.ID, string(task.TaskType), string(task.Automaticity), task.CredentialID, task.ProviderID, task.RawModel,
		string(task.Source), task.WorkerID, string(task.Status), task.Attempt, task.MaxAttempts,
		httpStatus, latencyMs,
		nullString(requestURL), nullString(string(extrasJSON)),
		nullString(errCode), nullString(errDetail),
		nullString(skipReason), nullString(recentRequestID), recentRequestAt,
		startedAt, finishedAt,
	)
	if err != nil {
		return fmt.Errorf("audit: insert system_probe_runs: %w", err)
	}
	return nil
}

// nullString converts empty string to nil for nullable TEXT columns.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
