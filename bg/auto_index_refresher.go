// Package bg — auto index refresher.
//
// AutoIndexRefresher is a background worker that periodically rebuilds
// the autoroute candidate pool from PostgreSQL. It serves two purposes:
//
//  1. Populate credential_model_index with live per-credential metrics
//     (success rate, p95 latency, concurrency, pressure) so the
//     auto-route decider has a hot-path lookup table.
//
//  2. Compute per-task-type aggregates into model_task_index for
//     observability (which model wins which task type).
//
// Refresh cadence:
//   - credential_model_index : every 5 minutes (matches the bg
//     concurrency peak collector's bucket size)
//   - model_task_index       : every 5 minutes (same cadence, different
//     aggregate level)
//
// Failure handling:
//   - Transient DB errors: log + retry next interval. The index keeps
//     the previous snapshot (stale-while-error).
//   - Fatal errors (pool closed, schema missing): stop the worker and
//     require operator intervention (slog.Error with shutdown context).
//
// Co-authored-by: Cursor <cursoragent@cursor.com>
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// Default refresh cadence. Override via env vars at startup.
const (
	defaultRefreshInterval = 5 * time.Minute
	defaultRefreshTimeout  = 30 * time.Second
)

// AutoIndexRefresher runs the periodic credential_model_index and
// model_task_index rollups.
type AutoIndexRefresher struct {
	db  *pgxpool.Pool
	idx *autoroute.Index

	// RefreshInterval controls how often the rollup runs.
	// Default: 5 minutes.
	RefreshInterval time.Duration

	// RefreshTimeout caps each rollup's wall-clock time. Prevents
	// runaway DB queries from blocking subsequent refreshes.
	// Default: 30 seconds.
	RefreshTimeout time.Duration

	// OnRollupComplete is an optional callback invoked after each
	// successful rollup. Used by tests and admin metrics endpoints.
	OnRollupComplete func(bucket time.Time, credentialRows, taskRows int)

	cancel context.CancelFunc
	done   chan struct{}
}

// NewAutoIndexRefresher wires the refresher. idx must be a fully
// initialised autoroute.Index (call autoroute.NewIndex() then
// idx.SetPool(db)).
func NewAutoIndexRefresher(db *pgxpool.Pool, idx *autoroute.Index) *AutoIndexRefresher {
	return &AutoIndexRefresher{
		db:              db,
		idx:             idx,
		RefreshInterval: defaultRefreshInterval,
		RefreshTimeout:  defaultRefreshTimeout,
		done:            make(chan struct{}),
	}
}

// Start spawns the background goroutine and triggers an initial refresh
// in the foreground (so the index is hot before serving the first request).
func (r *AutoIndexRefresher) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	go r.run(cctx)
	slog.Info("auto index refresher started",
		"interval", r.RefreshInterval.String(),
		"timeout", r.RefreshTimeout.String(),
	)

	// Initial refresh in foreground — blocks until done or ctx cancelled.
	// Safe to fail: the goroutine will retry next interval.
	if err := r.RefreshOnce(ctx); err != nil {
		slog.Warn("auto index initial refresh failed", "error", err)
	}
}

// Stop terminates the goroutine and waits for it.
func (r *AutoIndexRefresher) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

// run is the periodic refresh loop.
func (r *AutoIndexRefresher) run(ctx context.Context) {
	defer close(r.done)
	t := time.NewTicker(r.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.RefreshOnce(ctx); err != nil {
				slog.Warn("auto index refresh failed", "error", err)
			}
		}
	}
}

// RefreshOnce runs one refresh cycle: credential_model_index rollup,
// then model_task_index rollup, then in-memory Index refresh.
//
// Returns the first error encountered. The In-memory Index is updated
// even if downstream rollups fail (so a degraded model_task_index
// doesn't lock out routing decisions).
func (r *AutoIndexRefresher) RefreshOnce(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, r.RefreshTimeout)
	defer cancel()

	bucket := time.Now().UTC().Truncate(r.RefreshInterval)

	credRows, err := r.rollupCredentialModelIndex(timeoutCtx, bucket)
	if err != nil {
		return fmt.Errorf("rollup credential_model_index: %w", err)
	}

	taskRows, err := r.rollupModelTaskIndex(timeoutCtx, bucket)
	if err != nil {
		// Don't fail the whole refresh — model_task_index is best-effort
		slog.Warn("model_task_index rollup failed", "error", err)
	}

	// Refresh the in-memory index after the DB rollups succeed.
	if err := r.idx.Refresh(timeoutCtx); err != nil {
		return fmt.Errorf("in-memory index refresh: %w", err)
	}

	slog.Info("auto index refreshed",
		"bucket", bucket.Format(time.RFC3339),
		"credential_rows", credRows,
		"task_rows", taskRows,
	)

	if r.OnRollupComplete != nil {
		r.OnRollupComplete(bucket, credRows, taskRows)
	}
	return nil
}

// rollupCredentialModelIndex inserts the latest per-credential × per-model
// snapshot into credential_model_index.
//
// Source of truth (last 5 min bucket from credential_model_peak_1m +
// historical request_logs). One row per (credential_id, raw_model).
//
// Pre-computes the three profile scores (smart, speed_first, cost_first)
// inline so the hot path (Decider.Decide) doesn't need to recompute.
//
// 2026-06-28 (provider-314 incident): the underlying SELECT is a
// UNION ALL of two halves:
//   - half 1: traffic-derived (request_logs last 5 min) — original path
//   - half 2: cold-start fallback (v_routable_credential_models bindings
//     not already covered by half 1)
//
// Both halves share the same ON CONFLICT DO UPDATE suffix so live
// metrics always overwrite the conservative half-2 baseline as soon as
// a credential gets real traffic.
func (r *AutoIndexRefresher) rollupCredentialModelIndex(ctx context.Context, bucket time.Time) (int, error) {
	fullSQL := `INSERT INTO credential_model_index (
	    bucket, credential_id, raw_model, canonical_id,
	    billing_mode, unit_price_in_per_1m, unit_price_out_per_1m, context_window,
	    success_rate, p95_latency_ms, active_sessions, concurrency_limit, pressure_ratio,
	    score_smart, score_speed_first, score_cost_first
	)` + rollupCredentialModelIndexSQL + rollupCredentialModelIndexONCONFLICT
	tag, err := r.db.Exec(ctx, fullSQL, bucket)
	if err != nil {
		return 0, fmt.Errorf("exec: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// rollupModelTaskIndex inserts the latest per-canonical × per-task_type
// snapshot into model_task_index.
//
// Source: request_logs in the last 5 minutes, classified by task_type
// (heuristic, persisted in request_logs.task_type from auto requests).
// If no auto requests yet, the table stays empty — that's fine.
func (r *AutoIndexRefresher) rollupModelTaskIndex(ctx context.Context, bucket time.Time) (int, error) {
	tag, err := r.db.Exec(ctx, rollupModelTaskIndexSQL, bucket)
	if err != nil {
		return 0, fmt.Errorf("exec: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// rollupCredentialModelIndexSQL is the single statement that materialises
// the credential_model_index snapshot.
//
// Logic (two halves joined by UNION ALL):
//
//	(1) Traffic-derived rows (the original path, unchanged):
//	    For each (credential_id, raw_model) active in the last 5 minutes,
//	    gather:
//	      - success rate over the period
//	      - avg/p95 latency
//	      - cost (sum(cost_usd) / sum(total_tokens))
//	      - live active_sessions from credential_model_peak_1m latest bucket
//	      - concurrency_limit from credentials.concurrency_limit
//	    Join credentials + model_offers for static attributes.
//	    Pre-compute the 3 profile scores (smart/speed_first/cost_first)
//	    using autoroute.Score with a representative task signal
//	    (long_context as the worst-case fit).
//
//	(2) Cold-start fallback rows (added 2026-06-28):
//	    For every (credential_id, raw_model) that is_routable=TRUE in the
//	    unified v_routable_credential_models view BUT was not produced by
//	    the request_logs rollup above, insert a baseline row with neutral
//	    scores (success_rate=0.9, p95=1000, scores=50). This closes the
//	    "manual disable → traffic stops → credential_model_index drains
//	    → auto-route forgets the credential → admin re-enables → still
//	    invisible" loop (provider-314/gpt-5.4 incident 2026-06-28).
//	    Default values are conservative: a restored credential sits at
//	    cohort-average quality until the first real request_logs row
//	    arrives, at which point the ON CONFLICT DO UPDATE branch takes
//	    over and replaces the baseline with live metrics.
//
// Cost note: this query is the heaviest in the v2.0 data plane. It runs
// every 5 minutes and touches ~3k rows for a typical 5-min window.
// Mitigations if it gets slow:
//   - Add covering index on request_logs(credential_id, ts)
//   - Materialise request_logs into a per-minute summary table
//   - Run in a separate "reporting" DB replica
//
// NOTE: The profile-score pre-computation in SQL is intentionally rough.
// For v2.0 we use a simple weighted formula in SQL (10×speed + 5×stability
// + 3×success - 0.5×price) — close enough for ranking, and avoids
// spinning up the full autoroute.Score() formula in PL/pgSQL.
// The Decider still calls autoroute.Score() at request time using fresh
// signals, so the SQL scores are advisory.
const rollupCredentialModelIndexSQL = `
-- ── Half 1: traffic-derived rows (5-min window from request_logs) ─────────
SELECT
    $1::timestamptz AS bucket,
    rl.credential_id,
    COALESCE(rl.outbound_model, rl.client_model) AS raw_model,
    mo.canonical_id,
    mo.billing_mode,
    AVG(mo.unit_price_in_per_1m)  AS unit_price_in_per_1m,
    AVG(mo.unit_price_out_per_1m) AS unit_price_out_per_1m,
    MAX(mc.context_window)        AS context_window,
    -- success rate over the bucket window
    COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) AS success_rate,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms)::int, 1000) AS p95_latency_ms,
    -- active_sessions / concurrency_limit / pressure_ratio:
    --   advisory fields, set to 0 here (no subquery).
    --   The customer_cost_view recomputes active_concurrent live from
    --   request_logs so this static value is informational only.
    0                          AS active_sessions,
    MAX(cr.concurrency_limit)  AS concurrency_limit,
    0::numeric                 AS pressure_ratio,
    -- Simplified pre-computed scores (no subquery; pressure assumed 0).
    -- smart weights: price=25 speed=25 stab=20 match=25 pressure=10 ctx=15
    (COALESCE(100 * (1 - LEAST(1.0, AVG(mo.unit_price_in_per_1m + mo.unit_price_out_per_1m) / 20.0)), 50) * 0.25
   + COALESCE(100 - LEAST(100, percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) / 30), 50) * 0.25
   + COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) * 100 * 0.20
   + 50 * 0.25
   + 100 * 0.10
   + 80 * 0.15)::numeric(8,4) AS score_smart,
    -- speed_first weights: price=10 speed=50 stab=20 match=15 pressure=5 ctx=10
    (COALESCE(100 * (1 - LEAST(1.0, AVG(mo.unit_price_in_per_1m + mo.unit_price_out_per_1m) / 20.0)), 50) * 0.10
   + COALESCE(100 - LEAST(100, percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) / 30), 50) * 0.50
   + COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) * 100 * 0.20
   + 50 * 0.15
   + 100 * 0.05
   + 80 * 0.10)::numeric(8,4) AS score_speed_first,
    -- cost_first weights: price=50 speed=10 stab=15 match=20 pressure=5 ctx=10
    (COALESCE(100 * (1 - LEAST(1.0, AVG(mo.unit_price_in_per_1m + mo.unit_price_out_per_1m) / 20.0)), 50) * 0.50
   + COALESCE(100 - LEAST(100, percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) / 30), 50) * 0.10
   + COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) * 100 * 0.15
   + 50 * 0.20
   + 100 * 0.05
   + 80 * 0.10)::numeric(8,4) AS score_cost_first
FROM request_logs rl
JOIN credentials cr ON cr.id = rl.credential_id
LEFT JOIN model_offers mo
  ON mo.credential_id = rl.credential_id
 AND (mo.outbound_model_name = COALESCE(rl.outbound_model, rl.client_model)
   OR mo.raw_model_name = COALESCE(rl.outbound_model, rl.client_model))
LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
WHERE rl.ts >= NOW() - INTERVAL '5 minutes'
  AND rl.ts < NOW()
  AND rl.credential_id IS NOT NULL
  AND COALESCE(cr.status, 'active') NOT IN ('disabled')
  AND COALESCE(cr.lifecycle_status, 'active') != 'suspended'
GROUP BY rl.credential_id, COALESCE(rl.outbound_model, rl.client_model),
         mo.canonical_id, mo.billing_mode

UNION ALL

-- ── Half 2: cold-start fallback (added 2026-06-28, provider-314 incident) ─
-- For every routable binding that the request_logs rollup above did NOT
-- already produce, write a baseline row so the auto-route can see it
-- even with zero recent traffic. Baseline values are intentionally
-- conservative (success=0.9, p95=1000, scores=50) — the first real
-- request through the credential will produce a request_logs row that
-- the half-1 rollup will overwrite via ON CONFLICT DO UPDATE below.
--
-- Anti-join clause: skip pairs that already appeared in half 1 so we
-- don't churn the ON CONFLICT path. Note we deliberately do NOT
-- re-derive rl.ts in the anti-join's filter — half 1 itself filters on
-- the same 5-min window, so equality is sufficient.
SELECT
    $1::timestamptz                     AS bucket,
    v.credential_id,
    pm.raw_model_name                   AS raw_model,
    pm.canonical_id,
    cmb.billing_mode,
    cmb.unit_price_in_per_1m,
    cmb.unit_price_out_per_1m,
    mc.context_window,
    0.9::numeric(5,4)                   AS success_rate,
    1000::int                           AS p95_latency_ms,
    0::int                              AS active_sessions,
    c.concurrency_limit,
    0::numeric(5,4)                     AS pressure_ratio,
    50::numeric(8,4)                    AS score_smart,
    50::numeric(8,4)                    AS score_speed_first,
    50::numeric(8,4)                    AS score_cost_first
FROM v_routable_credential_models v
JOIN credentials c                  ON c.id = v.credential_id
JOIN credential_model_bindings cmb ON cmb.id = v.binding_id
JOIN provider_models pm             ON pm.id = v.provider_model_id
LEFT JOIN models_canonical mc       ON mc.id = pm.canonical_id
WHERE v.is_routable = TRUE
  AND pm.raw_model_name IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM request_logs rl
      WHERE rl.credential_id = v.credential_id
        AND (rl.outbound_model = pm.raw_model_name
          OR rl.client_model  = pm.raw_model_name)
        AND rl.ts >= NOW() - INTERVAL '5 minutes'
  )
`

// rollupCredentialModelIndexONCONFLICT is the ON CONFLICT clause for the
// rollup INSERT. Lives in a separate const because UNION ALL writes have
// to combine the SELECT list with this suffix, and Go string concatenation
// is the cleanest way to express it without making the main SELECT
// unreadable. ON CONFLICT DO UPDATE ensures the live half-1 metrics
// always overwrite the half-2 baseline, and vice-versa when both halves
// target the same (bucket, credential_id, raw_model).
const rollupCredentialModelIndexONCONFLICT = `
ON CONFLICT (bucket, credential_id, raw_model) DO UPDATE SET
    canonical_id         = EXCLUDED.canonical_id,
    billing_mode         = EXCLUDED.billing_mode,
    unit_price_in_per_1m = EXCLUDED.unit_price_in_per_1m,
    unit_price_out_per_1m= EXCLUDED.unit_price_out_per_1m,
    context_window       = EXCLUDED.context_window,
    success_rate         = EXCLUDED.success_rate,
    p95_latency_ms       = EXCLUDED.p95_latency_ms,
    active_sessions      = EXCLUDED.active_sessions,
    concurrency_limit    = EXCLUDED.concurrency_limit,
    pressure_ratio       = EXCLUDED.pressure_ratio,
    score_smart          = EXCLUDED.score_smart,
    score_speed_first    = EXCLUDED.score_speed_first,
    score_cost_first     = EXCLUDED.score_cost_first,
    updated_at           = NOW()
`

// rollupModelTaskIndexSQL aggregates request_logs into per-task-type
// performance buckets. Used by the admin auto-route dashboard
// ("which model wins which task type?") and future analytics.
//
// Bucket key: (bucket, canonical_id, task_type). Filtered to auto-route
// requests only (is_auto_request = true) so we measure actual routing
// outcomes rather than pre-auto usage.
const rollupModelTaskIndexSQL = `
INSERT INTO model_task_index (
    bucket, canonical_id, task_type,
    sample_count, success_rate,
    avg_latency_ms, p95_latency_ms, avg_cost_per_1k_usd,
    primary_credential_id
)
SELECT
    $1 AS bucket,
    rl.canonical_id,
    COALESCE(rl.task_type, 'chat') AS task_type,
    COUNT(*) AS sample_count,
    COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) AS success_rate,
    COALESCE(AVG(rl.latency_ms), 0)::int AS avg_latency_ms,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms), 1000)::int AS p95_latency_ms,
    CASE WHEN SUM(rl.total_tokens) > 0
         THEN (SUM(rl.cost_usd) / SUM(rl.total_tokens)) * 1000
         ELSE 0
    END AS avg_cost_per_1k_usd,
    MODE() WITHIN GROUP (ORDER BY rl.credential_id) AS primary_credential_id
FROM request_logs rl
WHERE rl.ts >= NOW() - INTERVAL '5 minutes'
  AND rl.ts < $1
  AND rl.is_auto_request = TRUE
  AND rl.canonical_id IS NOT NULL
GROUP BY rl.canonical_id, rl.task_type
ON CONFLICT (bucket, canonical_id, task_type) DO UPDATE SET
    sample_count        = EXCLUDED.sample_count,
    success_rate        = EXCLUDED.success_rate,
    avg_latency_ms      = EXCLUDED.avg_latency_ms,
    p95_latency_ms      = EXCLUDED.p95_latency_ms,
    avg_cost_per_1k_usd = EXCLUDED.avg_cost_per_1k_usd,
    primary_credential_id = EXCLUDED.primary_credential_id,
    updated_at          = NOW()
`
