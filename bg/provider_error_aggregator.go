// Package bg — provider_error_aggregator.go
//
// 2026-08-29: P0 fix for audit finding — provider_error_details 聚合逻辑
//
// Purpose:
//
//	从 candidate_failure_logs_hot 聚合错误到 provider_error_details 表，
//	用于错误趋势分析和运维看板。
//
// Design:
//   - 每 10 分钟运行一次，聚合最近 15 分钟的失败日志
//   - 使用错误指纹（provider_id + error_type + error_code + error_message）去重
//   - UPSERT 逻辑：相同指纹的错误合并，occurrences 递增
//   - 使用 advisory lock 防止多实例并发聚合冲突
//
// Error Fingerprint:
//
//	provider_id + model_name + endpoint + error_type + error_code + left(error_message, 200)
//
// Related:
//   - sql/migrations/startup/435_provider_quality_tables.sql (provider_error_details 表定义)
//   - domains/streaming/executors/candidate_failure_logger.go (数据源)
//   - docs/供应商画像/02-数据库设计.md
package bg

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderErrorAggregator 从 candidate_failure_logs_hot 聚合错误到 provider_error_details。
type ProviderErrorAggregator struct {
	db       *pgxpool.Pool
	interval time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
	lifeMu   sync.Mutex
	started  bool
	stopped  bool
}

// NewProviderErrorAggregator 创建聚合器实例。
func NewProviderErrorAggregator(db *pgxpool.Pool, interval time.Duration) *ProviderErrorAggregator {
	if interval == 0 {
		interval = 10 * time.Minute // 默认 10 分钟
	}
	return &ProviderErrorAggregator{
		db:       db,
		interval: interval,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Start 启动后台聚合任务。
func (a *ProviderErrorAggregator) Start(ctx context.Context) {
	if a == nil {
		return
	}
	a.lifeMu.Lock()
	if a.started || a.stopped {
		a.lifeMu.Unlock()
		return
	}
	a.started = true
	a.lifeMu.Unlock()
	go a.run(ctx)
}

// Stop 停止后台任务。未启动、重复和并发 Stop 都是安全的。
func (a *ProviderErrorAggregator) Stop() {
	if a == nil {
		return
	}
	a.lifeMu.Lock()
	if a.stopped {
		a.lifeMu.Unlock()
		return
	}
	a.stopped = true
	started := a.started
	close(a.stopCh)
	a.lifeMu.Unlock()
	if started {
		<-a.doneCh
	}
}

func (a *ProviderErrorAggregator) run(ctx context.Context) {
	defer close(a.doneCh)

	// 立即执行一次
	a.aggregateErrors(ctx)

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case <-ticker.C:
			a.aggregateErrors(ctx)
		}
	}
}

// aggregateErrors 执行一次聚合操作。
func (a *ProviderErrorAggregator) aggregateErrors(ctx context.Context) {
	if a == nil || a.db == nil {
		return
	}

	// 使用 advisory lock 防止多实例并发聚合
	// 使用 FNV-1a 计算 "llm-gateway:provider_error_aggregator" 的 hash
	const lockPrefix = "llm-gateway:provider_error_aggregator"
	h := fnv.New64a()
	h.Write([]byte(lockPrefix))
	lockKey := int64(h.Sum64())

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	tx, err := a.db.Begin(timeoutCtx)
	if err != nil {
		slog.Error("provider_error_aggregator: begin failed", "error", err)
		return
	}
	defer tx.Rollback(timeoutCtx)
	// This worker aggregates all tenants. The bypass is transaction-local so
	// pooled connections never retain elevated tenant visibility.
	if _, err := tx.Exec(timeoutCtx, "SELECT set_config('app.current_role', 'super_admin', true)"); err != nil {
		slog.Error("provider_error_aggregator: set role failed", "error", err)
		return
	}
	if _, err := tx.Exec(timeoutCtx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		slog.Error("provider_error_aggregator: set RLS bypass failed", "error", err)
		return
	}

	// 尝试获取锁，如果其他实例正在聚合则跳过
	var locked bool
	if err := tx.QueryRow(timeoutCtx,
		"SELECT pg_try_advisory_xact_lock($1)", lockKey,
	).Scan(&locked); err != nil {
		slog.Error("provider_error_aggregator: lock query failed", "error", err)
		return
	}

	if !locked {
		slog.Debug("provider_error_aggregator: skipped (peer holds lock)")
		return
	}

	// Aggregate complete ten-minute buckets from the recent source window.
	// Replaying a bucket is safe because the upsert replaces that bucket's
	// count instead of adding it again; tenant_id is part of the identity.
	query := `
			WITH aggregated AS (
				SELECT
					tenant_id,
					provider_id,
					raw_model_name AS model_name,
					'chat' AS endpoint,
					error_kind AS error_type,
					NULLIF(COALESCE(upstream_status_code::text, ''), '') AS error_code,
					LEFT(COALESCE(error_message, ''), 200) AS error_message,
					(date_trunc('hour', ts) + floor(extract(minute FROM ts) / 10) * interval '10 minutes') AS aggregation_bucket,
					(min(ts)) AS first_seen_at,
					(max(ts)) AS last_seen_at,
					count(*) AS occurrences,
					(array_agg(request_id ORDER BY ts DESC))[1] AS request_id,
					(array_agg(context ORDER BY ts DESC))[1] AS context
				FROM candidate_failure_logs_hot
				WHERE ts > NOW() - INTERVAL '15 minutes'
				GROUP BY tenant_id, provider_id, raw_model_name, error_kind,
					COALESCE(upstream_status_code::text, ''), LEFT(COALESCE(error_message, ''), 200),
					(date_trunc('hour', ts) + floor(extract(minute FROM ts) / 10) * interval '10 minutes')
			)
			INSERT INTO provider_error_details (
				provider_id, model_name, endpoint, error_type, error_code,
				error_message, request_id, user_id, tenant_id, aggregation_bucket,
				context, occurrences, first_seen_at, last_seen_at, created_at, updated_at
			)
			SELECT provider_id, model_name, endpoint, error_type, error_code,
				error_message, request_id, NULL AS user_id, tenant_id, aggregation_bucket,
				context, occurrences, first_seen_at, last_seen_at, NOW(), NOW()
			FROM aggregated
			ON CONFLICT (
				(COALESCE(tenant_id, '')), provider_id, (COALESCE(model_name, '')),
				(COALESCE(endpoint, '')), error_type, (COALESCE(error_code, '')),
				(COALESCE(LEFT(error_message, 200), '')), aggregation_bucket
			)
			DO UPDATE SET
				occurrences = EXCLUDED.occurrences,
				first_seen_at = EXCLUDED.first_seen_at,
				last_seen_at = EXCLUDED.last_seen_at,
				updated_at = NOW(),
				context = EXCLUDED.context,
				request_id = EXCLUDED.request_id,
				tenant_id = EXCLUDED.tenant_id,
				aggregation_bucket = EXCLUDED.aggregation_bucket
			RETURNING provider_id, error_type, occurrences
		`

	rows, err := tx.Query(timeoutCtx, query)
	if err != nil {
		slog.Error("provider_error_aggregator: aggregation query failed", "error", err)
		return
	}
	defer rows.Close()

	var aggregated int
	var totalOccurrences int64
	for rows.Next() {
		var providerID int
		var errorType string
		var occurrences int
		if err := rows.Scan(&providerID, &errorType, &occurrences); err != nil {
			slog.Warn("provider_error_aggregator: scan failed", "error", err)
			continue
		}
		aggregated++
		totalOccurrences += int64(occurrences)
	}

	if err := rows.Err(); err != nil {
		slog.Error("provider_error_aggregator: rows iteration failed", "error", err)
		return
	}

	if err := tx.Commit(timeoutCtx); err != nil {
		slog.Error("provider_error_aggregator: commit failed", "error", err)
		return
	}

	if aggregated > 0 {
		slog.Info("provider_error_aggregator: aggregation completed",
			"error_groups", aggregated,
			"total_occurrences", totalOccurrences,
			"duration", fmt.Sprintf("%.2fs", time.Since(time.Now()).Seconds()),
		)
	} else {
		slog.Debug("provider_error_aggregator: no new errors to aggregate")
	}
}
