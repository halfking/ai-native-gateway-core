// Package bg — provider_error_aggregator.go
//
// 2026-08-29: P0 fix for audit finding — provider_error_details 聚合逻辑
//
// Purpose:
//   从 candidate_failure_logs_hot 聚合错误到 provider_error_details 表，
//   用于错误趋势分析和运维看板。
//
// Design:
//   - 每 10 分钟运行一次，聚合最近 15 分钟的失败日志
//   - 使用错误指纹（provider_id + error_type + error_code + error_message）去重
//   - UPSERT 逻辑：相同指纹的错误合并，occurrences 递增
//   - 使用 advisory lock 防止多实例并发聚合冲突
//
// Error Fingerprint:
//   provider_id + model_name + endpoint + error_type + error_code + left(error_message, 200)
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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderErrorAggregator 从 candidate_failure_logs_hot 聚合错误到 provider_error_details。
type ProviderErrorAggregator struct {
	db       *pgxpool.Pool
	interval time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
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
	go a.run(ctx)
}

// Stop 停止后台任务。
func (a *ProviderErrorAggregator) Stop() {
	close(a.stopCh)
	<-a.doneCh
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

	// 聚合最近 15 分钟的失败日志
	// 使用 UPSERT 逻辑：相同错误指纹合并，occurrences 递增
	query := `
		WITH recent_failures AS (
			SELECT
				provider_id,
				raw_model_name as model_name,
				error_kind as error_type,
				COALESCE(upstream_status_code::text, '') as error_code,
				LEFT(COALESCE(error_message, ''), 200) as error_message_truncated,
				error_message as full_error_message,
				request_id,
				tenant_id,
				context,
				ts,
				MIN(ts) OVER (PARTITION BY 
					provider_id, 
					raw_model_name,
					error_kind,
					COALESCE(upstream_status_code::text, ''),
					LEFT(COALESCE(error_message, ''), 200)
				) as first_seen,
				MAX(ts) OVER (PARTITION BY 
					provider_id, 
					raw_model_name,
					error_kind,
					COALESCE(upstream_status_code::text, ''),
					LEFT(COALESCE(error_message, ''), 200)
				) as last_seen,
				COUNT(*) OVER (PARTITION BY 
					provider_id, 
					raw_model_name,
					error_kind,
					COALESCE(upstream_status_code::text, ''),
					LEFT(COALESCE(error_message, ''), 200)
				) as occurrence_count
			FROM candidate_failure_logs_hot
			WHERE ts > NOW() - INTERVAL '15 minutes'
		),
		aggregated AS (
			SELECT DISTINCT ON (
				provider_id, 
				model_name,
				error_type,
				error_code,
				error_message_truncated
			)
				provider_id,
				model_name,
				error_type,
				NULLIF(error_code, '') as error_code,
				-- 保持 200 字符截断，与唯一索引一致
				error_message_truncated as error_message,
				request_id,
				tenant_id,
				context,
				first_seen as first_seen_at,
				last_seen as last_seen_at,
				occurrence_count as occurrences
			FROM recent_failures
			ORDER BY 
				provider_id, 
				model_name,
				error_type,
				error_code,
				error_message_truncated,
				last_seen DESC
		)
		INSERT INTO provider_error_details (
			provider_id,
			model_name,
			endpoint,
			error_type,
			error_code,
			error_message,
			request_id,
			user_id,
			tenant_id,
			context,
			occurrences,
			first_seen_at,
			last_seen_at,
			created_at,
			updated_at
		)
		SELECT
			provider_id,
			model_name,
			'chat' as endpoint,  -- 默认为 chat，未来可从 context 提取
			error_type,
			error_code,
			error_message,
			request_id,
			NULL as user_id,  -- 未来可从 context 提取
			tenant_id,
			context,
			occurrences,
			first_seen_at,
			last_seen_at,
			NOW() as created_at,
			NOW() as updated_at
		FROM aggregated
		-- ON CONFLICT 需要使用索引的列表达式
		-- 注意：必须精确匹配唯一索引的列顺序和 COALESCE 表达式
		ON CONFLICT (
			provider_id,
			COALESCE(model_name, ''),
			COALESCE(endpoint, ''),
			error_type,
			COALESCE(error_code, ''),
			COALESCE(LEFT(error_message, 200), '')
		)
		DO UPDATE SET
			occurrences = provider_error_details.occurrences + EXCLUDED.occurrences,
			last_seen_at = GREATEST(provider_error_details.last_seen_at, EXCLUDED.last_seen_at),
			first_seen_at = LEAST(provider_error_details.first_seen_at, EXCLUDED.first_seen_at),
			updated_at = NOW(),
			-- 更新最新的 context 和 request_id
			context = EXCLUDED.context,
			request_id = EXCLUDED.request_id,
			tenant_id = EXCLUDED.tenant_id
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
