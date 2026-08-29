// Package bg — provider_error_aggregator.go
//
// Aggregates candidate failures into tenant-scoped, ten-minute error buckets.
// A monotonic source watermark makes retries and overlapping scheduler cycles
// idempotent.
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

const providerErrorAggregatorDefaultInterval = 10 * time.Minute

// ProviderErrorAggregator 从 candidate_failure_logs_hot 聚合错误到 provider_error_details。
type ProviderErrorAggregator struct {
	db       *pgxpool.Pool
	interval time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
	mu       sync.Mutex
	started  bool
	stopped  bool
}

func NewProviderErrorAggregator(db *pgxpool.Pool, interval time.Duration) *ProviderErrorAggregator {
	if interval <= 0 {
		interval = providerErrorAggregatorDefaultInterval
	}
	return &ProviderErrorAggregator{db: db, interval: interval, stopCh: make(chan struct{}), doneCh: make(chan struct{})}
}

// Start 启动后台聚合任务；重复调用不会启动多个 worker。
func (a *ProviderErrorAggregator) Start(ctx context.Context) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.started || a.stopped {
		a.mu.Unlock()
		return
	}
	a.started = true
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	go a.run(ctx)
}

// Stop 停止后台任务；重复调用及未 Start 都安全返回。
func (a *ProviderErrorAggregator) Stop() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	started := a.started
	close(a.stopCh)
	a.mu.Unlock()
	if started {
		<-a.doneCh
	}
}

func (a *ProviderErrorAggregator) run(ctx context.Context) {
	defer close(a.doneCh)
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

func (a *ProviderErrorAggregator) aggregateErrors(ctx context.Context) {
	if a == nil || a.db == nil {
		return
	}
	startedAt := time.Now()
	h := fnv.New64a()
	_, _ = h.Write([]byte("llm-gateway:provider_error_aggregator"))
	lockKey := int64(h.Sum64())
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	tx, err := a.db.Begin(timeoutCtx)
	if err != nil {
		slog.Error("provider_error_aggregator: begin failed", "error", err)
		return
	}
	defer tx.Rollback(timeoutCtx)

	// This worker intentionally aggregates all tenants. Both settings are
	// transaction-local so pooled connections never retain elevated visibility.
	for _, statement := range []string{
		"SELECT set_config('app.current_role', 'super_admin', true)",
		"SELECT set_config('app.bypass_rls', 'true', true)",
	} {
		if _, err := tx.Exec(timeoutCtx, statement); err != nil {
			slog.Error("provider_error_aggregator: set transaction visibility failed", "error", err)
			return
		}
	}

	var locked bool
	if err := tx.QueryRow(timeoutCtx, "SELECT pg_try_advisory_xact_lock($1)", lockKey).Scan(&locked); err != nil {
		slog.Error("provider_error_aggregator: lock query failed", "error", err)
		return
	}
	if !locked {
		slog.Debug("provider_error_aggregator: skipped (peer holds lock)")
		return
	}

	// Migration 622 adds aggregation_id, a dedicated monotonic key because the
	// legacy candidate failure id is explicitly non-unique. The source watermark,
	// bucket aggregation, upsert, and watermark advance all commit atomically.
	query := `
WITH watermark AS (
 SELECT last_source_id
 FROM provider_error_aggregator_state
 WHERE id = 1
 FOR UPDATE
), source_rows AS (
 SELECT c.aggregation_id,
  c.tenant_id,
  c.provider_id,
  c.raw_model_name AS model_name,
  COALESCE(NULLIF(c.context->>'endpoint', ''),
           NULLIF(c.context->>'client_endpoint', ''),
           NULLIF(c.context->>'upstream_endpoint', ''), 'unknown') AS endpoint,
  c.error_kind AS error_type,
  NULLIF(COALESCE(c.upstream_status_code::text, ''), '') AS error_code,
  LEFT(COALESCE(c.error_message, ''), 200) AS error_message,
  (date_trunc('hour', c.ts) +
   floor(extract(minute FROM c.ts) / 10) * interval '10 minutes') AS aggregation_bucket,
  c.request_id,
  c.context,
  c.ts
 FROM candidate_failure_logs_hot c
 CROSS JOIN watermark w
 WHERE c.aggregation_id > w.last_source_id
), aggregated AS (
 SELECT DISTINCT ON (tenant_id, provider_id, model_name, endpoint, error_type,
                     error_code, error_message, aggregation_bucket)
  tenant_id, provider_id, model_name, endpoint, error_type, error_code,
  error_message, aggregation_bucket, request_id, context,
  min(ts) OVER (PARTITION BY tenant_id, provider_id, model_name, endpoint,
                error_type, error_code, error_message, aggregation_bucket) AS first_seen_at,
  max(ts) OVER (PARTITION BY tenant_id, provider_id, model_name, endpoint,
                error_type, error_code, error_message, aggregation_bucket) AS last_seen_at,
  count(*) OVER (PARTITION BY tenant_id, provider_id, model_name, endpoint,
                 error_type, error_code, error_message, aggregation_bucket) AS occurrences,
  max(aggregation_id) OVER () AS max_source_id
 FROM source_rows
 ORDER BY tenant_id, provider_id, model_name, endpoint, error_type, error_code,
          error_message, aggregation_bucket, ts DESC
), inserted AS (
 INSERT INTO provider_error_details (
  provider_id, model_name, endpoint, error_type, error_code, error_message,
  request_id, user_id, tenant_id, aggregation_bucket, context, occurrences,
  first_seen_at, last_seen_at, resolved, created_at, updated_at
 )
 SELECT provider_id, model_name, endpoint, error_type, error_code, error_message,
  request_id, NULL, tenant_id, aggregation_bucket, context, occurrences,
  first_seen_at, last_seen_at, FALSE, NOW(), NOW()
 FROM aggregated
 ON CONFLICT (
  (COALESCE(tenant_id, '')), provider_id, (COALESCE(model_name, '')),
  (COALESCE(endpoint, '')), error_type, (COALESCE(error_code, '')),
  (COALESCE(LEFT(error_message, 200), '')),
  COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
 )
 DO UPDATE SET
  occurrences = EXCLUDED.occurrences,
  first_seen_at = EXCLUDED.first_seen_at,
  last_seen_at = EXCLUDED.last_seen_at,
  updated_at = NOW(),
  context = EXCLUDED.context,
  request_id = EXCLUDED.request_id,
  tenant_id = EXCLUDED.tenant_id,
  aggregation_bucket = EXCLUDED.aggregation_bucket,
  resolved = FALSE
 RETURNING provider_id, error_type, occurrences
), advanced AS (
 UPDATE provider_error_aggregator_state
 SET last_source_id = COALESCE(
       (SELECT MAX(max_source_id) FROM aggregated),
       provider_error_aggregator_state.last_source_id),
     updated_at = NOW()
 WHERE id = 1
 RETURNING last_source_id
)
SELECT i.provider_id, i.error_type, i.occurrences
FROM inserted i
CROSS JOIN advanced`

	rows, err := tx.Query(timeoutCtx, query)
	if err != nil {
		slog.Error("provider_error_aggregator: aggregation query failed", "error", err)
		return
	}
	defer rows.Close()

	groups := 0
	var total int64
	for rows.Next() {
		var providerID, occurrences int
		var errorType string
		if err := rows.Scan(&providerID, &errorType, &occurrences); err != nil {
			slog.Warn("provider_error_aggregator: scan failed", "error", err)
			continue
		}
		groups++
		total += int64(occurrences)
	}
	if err := rows.Err(); err != nil {
		slog.Error("provider_error_aggregator: rows iteration failed", "error", err)
		return
	}
	if err := tx.Commit(timeoutCtx); err != nil {
		slog.Error("provider_error_aggregator: commit failed", "error", err)
		return
	}
	if groups > 0 {
		slog.Info("provider_error_aggregator: aggregation completed",
			"error_groups", groups,
			"total_occurrences", total,
			"duration", fmt.Sprintf("%.2fs", time.Since(startedAt).Seconds()))
	} else {
		slog.Debug("provider_error_aggregator: no new errors to aggregate",
			"duration", fmt.Sprintf("%.2fs", time.Since(startedAt).Seconds()))
	}
}
