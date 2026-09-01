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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const providerErrorAggregatorDefaultInterval = 10 * time.Minute

// ProviderErrorAggregator 从 candidate_failure_logs_hot 聚合错误到 provider_error_details。
type ProviderErrorAggregator struct {
	db       *pgxpool.Pool
	interval time.Duration

	// lifecycle 由 BaseWorker 统一管理（审计报告 2026-08-31 模式 C）。
	*BaseWorker
}

func NewProviderErrorAggregator(db *pgxpool.Pool, interval time.Duration) *ProviderErrorAggregator {
	if interval <= 0 {
		interval = providerErrorAggregatorDefaultInterval
	}
	return &ProviderErrorAggregator{
		db:         db,
		interval:   interval,
		BaseWorker: NewBaseWorker("provider-error-aggregator"),
	}
}

// Start 启动后台聚合任务；重复调用不会启动多个 worker。
func (a *ProviderErrorAggregator) Start(ctx context.Context) {
	if a == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.BaseWorker.Start(ctx, a.run)
}

// Stop 停止后台任务；重复调用及未 Start 都安全返回。
func (a *ProviderErrorAggregator) Stop() {
	if a == nil {
		return
	}
	a.BaseWorker.Stop()
}

func (a *ProviderErrorAggregator) run(ctx context.Context) {
	defer a.BaseWorker.NotifyStopped()
	a.aggregateErrors(ctx)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
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

	// 2026-08-31 (P2-4 audit-data-closure sanity): capture the existing
	// watermark BEFORE the aggregation query so we can detect backwards
	// movement of the high-water mark after the query. A backwards jump
	// (new_last < old_last) means the watermark CTEs returned a value
	// inconsistent with monotonic aggregation_id semantics, which would
	// otherwise cause the next tick to re-process every historical row
	// in the unified view (hot UNION ALL partitioned). With bigint-min
	// seeding from migration 627, the first post-deploy tick legitimately
	// jumps from very-negative to a positive sequence value — that's the
	// expected forward movement, not a regression.
	var preWatermark int64
	if err := tx.QueryRow(timeoutCtx, "SELECT COALESCE(last_source_id, 0) FROM provider_error_aggregator_state WHERE id = 1").Scan(&preWatermark); err != nil {
		// State row may not exist yet on the very first tick after migration
		// 622/627 install. Treat missing as zero and continue.
		preWatermark = 0
	}

	// Migration 622 adds aggregation_id, a dedicated monotonic key because the
	// legacy candidate failure id is explicitly non-unique. Watermark rows first
	// identify changed aggregate keys, then every source row in those buckets is
	// re-read and replace-upserted. Thus N rows plus one new row becomes N+1, and
	// replaying the transaction leaves the bucket at N+1. The lock, aggregation,
	// upsert, and watermark advance all commit atomically.
	//
	// Migration 627 added aggregation_id to the partitioned parent
	// (candidate_failure_logs) and the unified view
	// candidate_failure_logs_unified (hot UNION ALL historical). We read from
	// the unified view so a row promoted between two aggregator ticks is still
	// visible to the watermark predicate. The view is SECURITY INVOKER so
	// pooled connections never retain elevated visibility beyond the bypass
	// already applied via set_config above.
	query := `
WITH watermark AS (
 SELECT last_source_id
 FROM provider_error_aggregator_state
 WHERE id = 1
 FOR UPDATE
), all_source_rows AS NOT MATERIALIZED (
 SELECT c.aggregation_id,
  c.tenant_id,
  c.provider_id,
  -- 2026-09-01 (P0-1 24h-audit round2): credential_id joins the
  -- aggregation grain so per-credential error sets stay separable
  -- (migration 639 added the column + rebuilt the unique index).
  COALESCE(c.credential_id::text, '') AS credential_id,
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
  c.ts,
  c.source
 FROM candidate_failure_logs_unified c
), new_source_rows AS (
 SELECT s.*
 FROM all_source_rows s
 CROSS JOIN watermark w
 WHERE s.aggregation_id > w.last_source_id
), affected_buckets AS (
 SELECT DISTINCT tenant_id, provider_id, credential_id, model_name, endpoint, error_type,
  error_code, error_message, aggregation_bucket
 FROM new_source_rows
), bucket_rows AS (
 SELECT s.*
 FROM all_source_rows s
 JOIN affected_buckets b
  ON s.tenant_id IS NOT DISTINCT FROM b.tenant_id
 AND s.provider_id IS NOT DISTINCT FROM b.provider_id
 AND s.credential_id IS NOT DISTINCT FROM b.credential_id
 AND s.model_name IS NOT DISTINCT FROM b.model_name
 AND s.endpoint IS NOT DISTINCT FROM b.endpoint
 AND s.error_type IS NOT DISTINCT FROM b.error_type
 AND s.error_code IS NOT DISTINCT FROM b.error_code
 AND s.error_message IS NOT DISTINCT FROM b.error_message
 AND s.aggregation_bucket IS NOT DISTINCT FROM b.aggregation_bucket
), aggregated AS (
 SELECT DISTINCT ON (tenant_id, provider_id, credential_id, model_name, endpoint, error_type,
                     error_code, error_message, aggregation_bucket)
  tenant_id, provider_id, credential_id, model_name, endpoint, error_type, error_code,
  error_message, aggregation_bucket, request_id, context,
  min(ts) OVER (PARTITION BY tenant_id, provider_id, credential_id, model_name, endpoint,
                error_type, error_code, error_message, aggregation_bucket) AS first_seen_at,
  max(ts) OVER (PARTITION BY tenant_id, provider_id, credential_id, model_name, endpoint,
                error_type, error_code, error_message, aggregation_bucket) AS last_seen_at,
  count(*) OVER (PARTITION BY tenant_id, provider_id, credential_id, model_name, endpoint,
                 error_type, error_code, error_message, aggregation_bucket) AS occurrences
 FROM bucket_rows
 ORDER BY tenant_id, provider_id, credential_id, model_name, endpoint, error_type, error_code,
          error_message, aggregation_bucket, ts DESC
), inserted AS (
 INSERT INTO provider_error_details (
  provider_id, model_name, endpoint, error_type, error_code, error_message,
  request_id, user_id, tenant_id, aggregation_bucket, context, occurrences,
  first_seen_at, last_seen_at, resolved, created_at, updated_at, credential_id
 )
 SELECT provider_id, model_name, endpoint, error_type, error_code, error_message,
  request_id, NULL, tenant_id, aggregation_bucket, context, occurrences,
  first_seen_at, last_seen_at, FALSE, NOW(), NOW(), credential_id
 FROM aggregated
 ON CONFLICT (
  (COALESCE(tenant_id, '')), provider_id, (COALESCE(credential_id, '')),
  (COALESCE(model_name, '')),
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
 RETURNING provider_id, error_type, occurrences,
   (COALESCE(endpoint, '') = 'unknown') AS endpoint_unknown
), advanced AS (
 UPDATE provider_error_aggregator_state
 SET last_source_id = COALESCE(
       (SELECT MAX(aggregation_id) FROM new_source_rows),
       provider_error_aggregator_state.last_source_id),
     updated_at = NOW()
 WHERE id = 1
 RETURNING last_source_id
)
SELECT i.provider_id, i.error_type, i.occurrences, i.endpoint_unknown, advanced.last_source_id
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
	var newAggregationID int64
	for rows.Next() {
		var providerID, occurrences int
		var errorType string
		// last_source_id is constant across rows because `advanced` returns at most
		// one row; we accept whatever value the last scan saw so the field tracks
		// the watermark this tick committed.
		if err := rows.Scan(&providerID, &errorType, &occurrences, &newAggregationID); err != nil {
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
	// 2026-08-31 (P2-4 audit-data-closure sanity): the watermark advanced
	// must always be >= the watermark read at the start of this tick. A
	// backwards jump is impossible under the monotonic aggregation_id
	// contract (positive sequence values for hot rows, negative synthesized
	// values for historical rows from migration 627's unified view) and
	// would mean either the candidate_failure_logger wrote NULL / duplicate
	// values, or the watermark CTE returned the wrong row. Log loudly so
	// the operator can investigate before the next tick re-processes
	// historical rows.
	//
	// Guarded on groups > 0: the final SELECT uses
	//   FROM inserted i CROSS JOIN advanced
	// so when no rows were aggregated this tick, `inserted` is empty, the
	// cross join yields zero rows, the Scan loop never runs, and
	// newAggregationID keeps its zero value. Comparing zero against any
	// non-zero preWatermark would otherwise log a false-positive "watermark
	// regressed" error on every idle steady-state tick.
	if groups > 0 && newAggregationID < preWatermark {
		slog.Error("provider_error_aggregator: watermark regressed",
			"pre_watermark", preWatermark,
			"post_watermark", newAggregationID,
			"groups", groups,
			"duration", fmt.Sprintf("%.2fs", time.Since(startedAt).Seconds()))
	} else if newAggregationID == preWatermark && groups > 0 {
		// 2026-08-31 (P2-4 audit-data-closure sanity): groups>0 but watermark
		// did not advance means the aggregator inserted rows but failed to
		// push the watermark forward, so the next tick will re-process the
		// same buckets. The current SQL guarantees the watermark advances
		// whenever new_source_rows is non-empty, so this branch indicates
		// a query regression and warrants investigation.
		slog.Warn("provider_error_aggregator: watermark did not advance despite new groups",
			"watermark", preWatermark,
			"groups", groups,
			"duration", fmt.Sprintf("%.2fs", time.Since(startedAt).Seconds()))
	}
	if groups > 0 {
		slog.Info("provider_error_aggregator: aggregation completed",
			"error_groups", groups,
			"total_occurrences", total,
			"aggregation_id", newAggregationID,
			"duration", fmt.Sprintf("%.2fs", time.Since(startedAt).Seconds()))
	} else {
		slog.Debug("provider_error_aggregator: no new errors to aggregate",
			"aggregation_id", newAggregationID,
			"duration", fmt.Sprintf("%.2fs", time.Since(startedAt).Seconds()))
	}
}
