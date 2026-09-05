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

// stageSourceRowsSQL stages the NEW source rows this tick will process into a
// per-transaction temp table. The watermark parameter bounds the scan so
// steady-state ticks stage only rows not yet aggregated; those rows identify
// the affected buckets, which stageAffectedBucketRowsSQL then re-reads in
// full. Both staging statements share this select list verbatim so the temp
// tables have identical column shapes — see the columnar note above the
// staging Exec calls in aggregateErrors.
const stageSourceRowsSQL = `
CREATE TEMP TABLE provider_error_agg_src
ON COMMIT DROP AS
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
  -- The view's synthesized 'source' constant column ("hot"/"historical")
  -- crashes this plan on citus-columnar partitions with
  -- "cache lookup failed for attribute source of relation" (SQLSTATE XX000,
  -- citus 13.3; 2026-09-05 PG log audit). Nothing downstream reads it, so
  -- stage a NULL placeholder to keep the temp-table column shape identical.
  NULL::text AS source
 FROM candidate_failure_logs_unified c
 WHERE c.aggregation_id > $1`

// stageAffectedBucketRowsSQL re-reads the COMPLETE buckets touched by this
// tick: every row of candidate_failure_logs_unified whose aggregation key
// matches one of the staged new rows. This is what keeps the replace-style
// DO UPDATE exact — 2026-09-05 (round2 audit F-4): when commit 3344f3f17
// bounded staging by the watermark alone, the upsert replaced each cross-tick
// bucket with just this tick's increment, losing the accumulated occurrences
// and dragging first_seen_at to the newest window min. The re-read is
// deliberately watermark-free; the ts range predicates are logical
// consequences of the bucket-key join (a row in one of the staged 10-minute
// buckets necessarily falls between the earliest and latest staged bucket)
// and exist so the planner can prune the historical month partitions at
// execution time. NULL-safe IS NOT DISTINCT FROM comparisons mirror the
// pre-staging in-pipeline bucket_rows join that ran on this view for months.
const stageAffectedBucketRowsSQL = `
CREATE TEMP TABLE provider_error_agg_bucket_rows
ON COMMIT DROP AS
 SELECT c.aggregation_id,
  c.tenant_id,
  c.provider_id,
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
  -- Same citus-columnar shape guard as provider_error_agg_src above.
  NULL::text AS source
 FROM candidate_failure_logs_unified c
 JOIN (
  SELECT DISTINCT tenant_id, provider_id, credential_id, model_name, endpoint, error_type,
   error_code, aggregation_bucket
  FROM provider_error_agg_src
 ) b
  ON c.tenant_id IS NOT DISTINCT FROM b.tenant_id
 AND c.provider_id IS NOT DISTINCT FROM b.provider_id
 AND COALESCE(c.credential_id::text, '') IS NOT DISTINCT FROM b.credential_id
 AND c.raw_model_name IS NOT DISTINCT FROM b.model_name
 AND COALESCE(NULLIF(c.context->>'endpoint', ''),
              NULLIF(c.context->>'client_endpoint', ''),
              NULLIF(c.context->>'upstream_endpoint', ''), 'unknown') IS NOT DISTINCT FROM b.endpoint
 AND c.error_kind IS NOT DISTINCT FROM b.error_type
 AND NULLIF(COALESCE(c.upstream_status_code::text, ''), '') IS NOT DISTINCT FROM b.error_code
 AND (date_trunc('hour', c.ts) +
      floor(extract(minute FROM c.ts) / 10) * interval '10 minutes') IS NOT DISTINCT FROM b.aggregation_bucket
 WHERE c.ts >= (SELECT MIN(aggregation_bucket) FROM provider_error_agg_src)
 AND c.ts < (SELECT MAX(aggregation_bucket) FROM provider_error_agg_src) + interval '10 minutes'`

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
	// FOR UPDATE holds the state row until commit so the watermark cannot
	// move under us between staging and the pipeline below.
	if err := tx.QueryRow(timeoutCtx, "SELECT COALESCE(last_source_id, 0) FROM provider_error_aggregator_state WHERE id = 1 FOR UPDATE").Scan(&preWatermark); err != nil {
		// State row may not exist yet on the very first tick after migration
		// 622/627 install. Treat missing as zero and continue.
		preWatermark = 0
	}

	// Migration 622 adds aggregation_id, a dedicated monotonic key because the
	// legacy candidate failure id is explicitly non-unique. The two staging
	// passes below first identify the changed aggregate keys (new rows above
	// the watermark), then every source row in those buckets is re-read and
	// replace-upserted. Thus N rows plus one new row becomes N+1, and
	// replaying the transaction leaves the bucket at N+1. The lock, staging,
	// aggregation, upsert, and watermark advance all commit atomically.
	//
	// 2026-09-05 (round2 audit E-#3): error_message left the aggregation key
	// (both staging passes keep it as a sample column; migration 663 rebuilt
	// the unique index without it). Wording jitter on the same upstream error
	// used to split one logical bucket into many rows, corrupting occurrences
	// and first/last_seen. Now the bucket identity is
	// (tenant, provider, credential, model, endpoint, error_type, error_code,
	// aggregation_bucket); error_message rides along as the newest sample in
	// the bucket (DISTINCT ON ... ts DESC) and the UPSERT refreshes it.
	//
	// 2026-09-05 (round2 audit F-4): the affected-bucket re-read must NOT be
	// bounded by the watermark. Commit 3344f3f17 bounded the single staging
	// pass by the watermark alone, so the replace-style DO UPDATE received
	// only this tick's increment and clobbered cross-tick buckets — the
	// accumulated occurrences were overwritten by the last tick's count and
	// first_seen_at drifted to the newest window min (a 10-minute bucket's
	// rows naturally span several 10-minute ticks). Pass one stays
	// watermark-bounded (cheap; identifies the affected buckets); pass two
	// (stageAffectedBucketRowsSQL) is bucket-bounded and re-reads each
	// affected bucket in full across the whole unified view — including rows
	// already promoted to the historical partitions, so a bucket whose rows
	// straddle a promote is still aggregated exactly. The watermark itself
	// stays: it bounds pass one, drives the advance below, and powers the
	// regression sanity checks after commit.
	//
	// Migration 627 added aggregation_id to the partitioned parent
	// (candidate_failure_logs) and the unified view
	// candidate_failure_logs_unified (hot UNION ALL historical). We read from
	// the unified view so a row promoted between two aggregator ticks is still
	// visible to the watermark predicate. The view is SECURITY INVOKER so
	// pooled connections never retain elevated visibility beyond the bypass
	// already applied via set_config above.
	//
	// 2026-09-05 (PG log audit): the historical side of the unified view is
	// backed by citus-columnar month partitions, and the citus 13.3 build in
	// use aborts this specific plan shape (multi-CTE window/DISTINCT ON
	// pipeline directly over the UNION ALL view) with
	// "cache lookup failed for attribute source of relation" (SQLSTATE XX000)
	// as soon as any columnar partition is in scope — every tick failed and
	// the watermark never advanced. Simple scans over the same partitions are
	// fine, so the unified view is read only by the two plain CTAS staging
	// statements (provider_error_agg_src and provider_error_agg_bucket_rows —
	// plain scans/joins, no window functions over the view) and the
	// window/DISTINCT ON pipeline reads only the temp tables. Same
	// transaction, same semantics, no columnar partition in the complex plan.
	if _, err := tx.Exec(timeoutCtx, "DROP TABLE IF EXISTS pg_temp.provider_error_agg_bucket_rows"); err != nil {
		slog.Error("provider_error_aggregator: bucket temp table drop failed", "error", err)
		return
	}
	if _, err := tx.Exec(timeoutCtx, "DROP TABLE IF EXISTS pg_temp.provider_error_agg_src"); err != nil {
		slog.Error("provider_error_aggregator: temp table drop failed", "error", err)
		return
	}
	if _, err := tx.Exec(timeoutCtx, stageSourceRowsSQL, preWatermark); err != nil {
		slog.Error("provider_error_aggregator: source staging failed", "error", err)
		return
	}
	// Idle-tick guard (F-4 companion): with no new rows there are no affected
	// buckets, so pass two could only rescan the unified view to match zero
	// buckets. Roll back now — the temp tables and the FOR UPDATE state lock
	// release, the watermark stays untouched — and log the same idle signal
	// the groups==0 path below produces.
	var hasNewRows bool
	if err := tx.QueryRow(timeoutCtx, "SELECT EXISTS (SELECT 1 FROM provider_error_agg_src)").Scan(&hasNewRows); err != nil {
		slog.Error("provider_error_aggregator: staged new-row probe failed", "error", err)
		return
	}
	if !hasNewRows {
		slog.Debug("provider_error_aggregator: no new errors to aggregate",
			"aggregation_id", preWatermark,
			"duration", fmt.Sprintf("%.2fs", time.Since(startedAt).Seconds()))
		return
	}
	if _, err := tx.Exec(timeoutCtx, stageAffectedBucketRowsSQL); err != nil {
		slog.Error("provider_error_aggregator: affected bucket staging failed", "error", err)
		return
	}
	// aggregated reads the COMPLETE affected buckets staged by pass two, so
	// the replace-style DO UPDATE below is exact per tick: occurrences lands
	// on the full bucket count and first_seen_at on the bucket's original
	// onset (audit F-4), with error_message carried as the ts-DESC newest
	// sample (audit E-#3).
	query := `
WITH watermark AS (
 SELECT last_source_id
 FROM provider_error_aggregator_state
 WHERE id = 1
 FOR UPDATE
), new_source_rows AS (
 SELECT s.*
 FROM provider_error_agg_src s
 CROSS JOIN watermark w
 WHERE s.aggregation_id > w.last_source_id
), aggregated AS (
 SELECT DISTINCT ON (tenant_id, provider_id, credential_id, model_name, endpoint, error_type,
                     error_code, aggregation_bucket)
  tenant_id, provider_id, credential_id, model_name, endpoint, error_type, error_code,
  error_message, aggregation_bucket, request_id, context,
  min(ts) OVER (PARTITION BY tenant_id, provider_id, credential_id, model_name, endpoint,
                error_type, error_code, aggregation_bucket) AS first_seen_at,
  max(ts) OVER (PARTITION BY tenant_id, provider_id, credential_id, model_name, endpoint,
                error_type, error_code, aggregation_bucket) AS last_seen_at,
  count(*) OVER (PARTITION BY tenant_id, provider_id, credential_id, model_name, endpoint,
                 error_type, error_code, aggregation_bucket) AS occurrences
 FROM provider_error_agg_bucket_rows
 ORDER BY tenant_id, provider_id, credential_id, model_name, endpoint, error_type, error_code,
          aggregation_bucket, ts DESC
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
  COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
 )
 DO UPDATE SET
  occurrences = EXCLUDED.occurrences,
  -- E-#3: error_message is a sample column, not part of the identity. The
  -- upserted bucket keeps the most recent wording (EXCLUDED carries the
  -- ts-DESC sample chosen by the DISTINCT ON above).
  -- F-4: the replace semantics are exact because EXCLUDED aggregates the
  -- COMPLETE bucket (stageAffectedBucketRowsSQL re-reads every row of each
  -- affected bucket, not just this tick's increment) — occurrences lands on
  -- the full bucket count and first_seen_at retains the bucket's original
  -- onset regardless of message drift.
  error_message = EXCLUDED.error_message,
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
	var unknownEndpointGroups int64
	for rows.Next() {
		var providerID, occurrences int
		var errorType string
		var endpointUnknown bool
		// last_source_id is constant across rows because `advanced` returns at most
		// one row; we accept whatever value the last scan saw so the field tracks
		// the watermark this tick committed. Scan must consume all five SELECT
		// columns — a dest/column mismatch makes pgx fail every row, which once
		// silently zeroed groups/total and disabled the watermark sanity checks
		// below (2026-09-05 audit E-#1).
		if err := rows.Scan(&providerID, &errorType, &occurrences, &endpointUnknown, &newAggregationID); err != nil {
			slog.Warn("provider_error_aggregator: scan failed", "error", err)
			continue
		}
		groups++
		total += int64(occurrences)
		if endpointUnknown {
			unknownEndpointGroups++
		}
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
			"unknown_endpoint_groups", unknownEndpointGroups,
			"aggregation_id", newAggregationID,
			"duration", fmt.Sprintf("%.2fs", time.Since(startedAt).Seconds()))
	} else {
		slog.Debug("provider_error_aggregator: no new errors to aggregate",
			"aggregation_id", newAggregationID,
			"duration", fmt.Sprintf("%.2fs", time.Since(startedAt).Seconds()))
	}
}
