// bg/auto_route_affinity_worker.go — learn task→model affinity from settled selections.
//
// Ref: docs/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md §2.3
//
// This is the learner half of the loop. It reads the rewards that
// AutoRouteSettleWorker stamped onto auto_route_selections and folds them into
// the task_model_affinity table that AffinityStore reads on the hot path.
//
// The maths is deliberately done in Go, not SQL:
//
//   - Bayesian shrinkage toward neutral (w = n/(n+K)) depends on sample count
//     in a way that is awkward in pure SQL and was already unit-tested in
//     autoroute.ShrinkAffinity.
//   - EMA across windows (alpha 0.15) needs the previous value, which is a
//     classic read-modify-write that is clearer in code than in a CTE.
//   - Staleness decay (rows unsampled for >7d drift back to neutral) is a
//     safety net for this very worker dying; it must run even on rows with no
//     new samples, which a pure aggregation query would skip.
//
// So the worker does three passes per sweep:
//
//  1. Aggregate settled selections from the learning window into per-(task,
//     profile, canonical, tenant) buckets.
//  2. Upsert each bucket: new sample counts, new avg_reward; fold avg_reward
//     into the stored EMA; recompute affinity via ShrinkAffinity; recompute
//     confidence. Touch every existing row's affinity with staleness decay
//     regardless of whether it had new samples.
//  3. Recompute rank within each (task, profile, tenant) scope.
//
// Tenant handling: both platform (tenant_id='') and tenant-scoped rows are
// produced. The store decides at read time which to use based on evidence; the
// worker just records both faithfully.

package bg

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin/distlock"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	affinityInterval = 15 * time.Minute
	affinityWindow   = 14 * 24 * time.Hour // learning lookback
	affinitySweepTO  = 3 * time.Minute

	// affinityDistLockTTL bounds how long the Redis-elected leader holds the
	// affinity token (R31 audit §四#1). Must exceed the sweep timeout
	// (affinitySweepTO = 3m) so a slow-but-alive sweep never loses its lease
	// mid-cycle; distlock auto-renews at ttl/3 while the process is alive.
	affinityDistLockTTL = 5 * time.Minute

	// affinityMinRewardN was removed: GROUP BY guarantees COUNT(*)>=1, so the
	// previous guard was a tautology. Real small-sample defence lives at read
	// time (AffinityMinSamples / AffinityTenantMinSamples).
)

var (
	autoRouteAffinityRows = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_autoroute_affinity_upserts_total",
			Help: "task_model_affinity rows upserted by the learner",
		},
		[]string{"scope"}, // platform / tenant
	)

	autoRouteAffinityRankChurn = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "llmgw_autoroute_affinity_rank_churn",
			Help:    "Per-sweep sum of |rank delta|; a spike signals ranking instability",
			Buckets: []float64{0, 1, 3, 10, 30, 100, 300},
		},
	)
)

// affinityAggregate is one learning bucket: everything a (task, profile,
// canonical, tenant) cell observed in the window.
type affinityAggregate struct {
	taskType       string
	profile        string
	canonicalID    int64
	canonicalModel string
	tenantID       string

	sampleCount  int
	successCount int
	avgLatencyMs int
	avgCostUSD   float64
	avgHealth    float64
	avgReward    float64
}

// AutoRouteAffinityWorker learns task_model_affinity from settled selections.
type AutoRouteAffinityWorker struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}

	stopOnce sync.Once
	started  atomic.Bool

	// distLock is the optional Redis-backed leader election manager (R31
	// audit §四#1). Nil (or Enabled()==false) makes every instance sweep
	// exactly as before this change.
	distLock distlock.Manager
}

// NewAutoRouteAffinityWorker constructs the worker.
func NewAutoRouteAffinityWorker(db *pgxpool.Pool) *AutoRouteAffinityWorker {
	return &AutoRouteAffinityWorker{db: db, done: make(chan struct{})}
}

// SetDistLock wires the Redis-backed distributed lock manager used for
// cross-instance sweep dedup (token-bucket leader election, R31 audit §四#1).
// Optional: when never called, or called with a manager whose Enabled() is
// false, every instance sweeps exactly as before. MUST be called before
// Start(): the field is read unsynchronized by the sweep goroutine (R34
// 2026-09-17 audit — the previous "safe after Start" wording promised a
// happens-before edge that does not exist).
//
// The affinity sweep has an extra reason to elect a single leader: the EMA
// fold in upsertAggregates is a read-modify-write per bucket, and two
// interleaved instances double-fold one window's reward (alpha 0.15 applied
// twice). It converges eventually, but the election removes the drift.
func (w *AutoRouteAffinityWorker) SetDistLock(mgr distlock.Manager) {
	w.distLock = mgr
}

// Start launches the sweep loop. Idempotent.
func (w *AutoRouteAffinityWorker) Start(ctx context.Context) {
	if !w.started.CompareAndSwap(false, true) {
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	go w.run(ctx)
	slog.Info("auto-route affinity worker started", "interval", affinityInterval.String())
}

// Stop cancels and waits. Safe on a never-Started worker and twice.
func (w *AutoRouteAffinityWorker) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	if !w.started.Load() {
		return
	}
	<-w.done
}

func (w *AutoRouteAffinityWorker) run(ctx context.Context) {
	// Panic guard (audit 2026-09-05 G-#1): a sweep panic must not kill the
	// process; it would also skip close(w.done) below and hang Stop forever.
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("auto-route affinity worker panic", "recover", rec)
		}
	}()
	defer close(w.done)

	ticker := time.NewTicker(affinityInterval)
	defer ticker.Stop()

	// Stagger the first sweep behind the settle worker, which must run first or
	// there is nothing settled to learn from.
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Minute):
		w.safeSweep(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.safeSweep(ctx)
		}
	}
}

// safeSweep wraps one sweep cycle in a per-cycle panic guard (R36 2026-09-17
// audit, closes R34 遗留#6) — see AutoRouteSettleWorker.safeSweep.
func (w *AutoRouteAffinityWorker) safeSweep(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("auto-route affinity sweep panic (cycle skipped, worker alive)", "recover", rec)
		}
	}()
	w.sweep(ctx)
}

func (w *AutoRouteAffinityWorker) sweep(ctx context.Context) {
	if w.db == nil {
		return
	}
	sweepCtx, cancel := context.WithTimeout(ctx, affinitySweepTO)
	defer cancel()

	// R31 (audit §四#1): token-bucket cross-instance dedup — one leader per
	// tick, followers skip without queueing. Distinct key from the settle
	// worker so the two intervals (5m / 15m) elect independently. Without
	// Redis both instances sweep as before.
	if h := acquireSweepDistLock(sweepCtx, w.distLock, "auto_route_affinity", affinityDistLockTTL, "auto_route_affinity"); h != nil {
		defer h.Release(context.WithoutCancel(sweepCtx))
		if !h.IsLeader() {
			slog.Info("auto-route affinity skipped, redis token held by another instance")
			return
		}
	}

	aggs, err := w.aggregate(sweepCtx)
	if err != nil {
		slog.Warn("auto-route affinity aggregate failed", "error", err)
		return
	}

	if err := w.applyStalenessDecay(sweepCtx); err != nil {
		// Non-fatal: stale rows will simply keep their old affinity a bit
		// longer. Logged for visibility.
		slog.Warn("auto-route affinity staleness decay failed", "error", err)
	}

	platformRows, tenantRows := w.upsertAggregates(sweepCtx, aggs)

	if err := w.recomputeRanks(sweepCtx); err != nil {
		slog.Warn("auto-route affinity rank recompute failed", "error", err)
	}

	if platformRows+tenantRows > 0 {
		slog.Info("auto-route affinity learned",
			"buckets", len(aggs), "platform_rows", platformRows, "tenant_rows", tenantRows)
	}
}

// aggregate reads settled selections from the learning window, grouped by the
// full (task, profile, canonical, tenant) key. Only rows with a reward are
// counted — abandoned rows (reward IS NULL) are excluded so they cannot drag a
// model's score down for reasons unrelated to model quality.
//
// R38 (R37 §四 #1 closing): synthetic rounds are excluded by an explicit
// origin_actor predicate (LEFT JOIN request_logs_hot). The reward IS NOT NULL
// guard alone was insufficient — a future write path that stamps a real
// reward on a goal-* row (e.g. ad-hoc backfill, manual reconcile) would slip
// through and pollute task_model_affinity. The view auto_route_selections_all
// does not project origin_actor (selections carry only features + reward),
// so the request_logs join is the only reliable way to enforce the
// synthetic-round gate at the aggregate face. NULL origin_actor is treated as
// ordinary traffic (legacy inserts predate the column).
//
// R46 F4 (affinity 盲窗三选一裁决, R43 §五#2 顺延收口): the old LEFT JOIN hit
// request_logs_hot only — settled rows older than the ~8h hot retention saw
// NULL origin_actor and slipped past the gate. Ruling (measured on the live
// DB, see docs/audit/2026-09-19-r46-48h-audit-round.md §四): NOT EXISTS probes
// over hot ∪ parent = 110ms / 14d window; literal view JOIN =
// 6.98s + the turns-preferred view injects duplicate request faces (rejected);
// settle-time redundant write + migration 727 = full five-point sync cost to
// defend a write path that does not exist today (rejected). The NOT EXISTS
// form is semantically identical (NULL actor passes, synthetic excluded) and
// retention-independent: hot and parent are mutually exclusive (promote is
// DELETE+INSERT), and each probe is an index descent on
// idx_request_logs_hot_request_id / the partitioned request_id index.
func (w *AutoRouteAffinityWorker) aggregate(ctx context.Context) ([]affinityAggregate, error) {
	rows, err := w.db.Query(ctx, `
		SELECT s.task_type,
		       s.profile,
		       s.canonical_id,
		       s.chosen_model,
		       COALESCE(s.tenant_id, ''),
		       COUNT(*)::int,
		       COUNT(*) FILTER (WHERE s.success)::int,
		       COALESCE(AVG(s.latency_ms), 0)::int,
		       COALESCE(AVG(s.cost_usd), 0),
		       COALESCE(AVG(ss.health_score), 0),
		       AVG(s.reward)
		FROM auto_route_selections_all s
		LEFT JOIN session_summaries ss
		       ON ss.session_key = s.session_id
		WHERE s.reward IS NOT NULL
		  AND s.canonical_id IS NOT NULL
		  AND s.settled_at >= NOW() - $1::interval
		  AND NOT EXISTS (
		      SELECT 1 FROM request_logs_hot rl
		      WHERE rl.request_id = s.request_id
		        AND (COALESCE(rl.origin_actor, '') LIKE 'goal-%'
		          OR COALESCE(rl.origin_actor, '') IN ('auto-title-generator','auto-summary-generator','session-summary')))
		  AND NOT EXISTS (
		      SELECT 1 FROM request_logs rl
		      WHERE rl.request_id = s.request_id
		        AND (COALESCE(rl.origin_actor, '') LIKE 'goal-%'
		          OR COALESCE(rl.origin_actor, '') IN ('auto-title-generator','auto-summary-generator','session-summary')))
		GROUP BY s.task_type, s.profile, s.canonical_id, s.chosen_model, COALESCE(s.tenant_id, '')
	`, affinityWindow.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]affinityAggregate, 0, 64)
	for rows.Next() {
		var a affinityAggregate
		// Scan errors in pgx v5 terminate iteration. Treat as sweep-fatal.
		if err := rows.Scan(
			&a.taskType, &a.profile, &a.canonicalID, &a.canonicalModel, &a.tenantID,
			&a.sampleCount, &a.successCount, &a.avgLatencyMs, &a.avgCostUSD,
			&a.avgHealth, &a.avgReward,
		); err != nil {
			return nil, err
		}
		// Aggregate always returns COUNT(*) >= 1; the previous `affinityMinRewardN`
		// guard was a tautology and is removed. Real small-sample defence is
		// AffinityMinSamples at read time.
		out = append(out, a)
	}
	return out, rows.Err()
}

// upsertAggregates folds each bucket into task_model_affinity and returns the
// number of platform- vs tenant-scoped rows written, for metrics.
//
// CRITICAL/HIGH-1 fix (verified 2026-08-11): sample_count MUST be this window's
// count, not prevSamples + this window. The window IS the cumulative truth
// (it's the rolling 14 days), so re-adding prevSamples inflates sample_count
// by N per sweep and eventually bypasses AffinityMinSamples / AffinityTenantMinSamples
// entirely — a single lucky hit could become "high confidence" within 2.5 hours.
//
// Historical evidence is carried by the EMA (alpha 0.15) over avg_reward,
// not by re-counting the same window. Shrinkage therefore pulls toward the
// EMA-weighted reward, gated by this window's sample_count.
func (w *AutoRouteAffinityWorker) upsertAggregates(ctx context.Context, aggs []affinityAggregate) (platformRows, tenantRows int) {
	for _, a := range aggs {
		// Look up the existing EMA so it can be blended.
		var prevEMA float64
		// R34 (2026-09-17 audit): a failed read must not fold as prevEMA=0 —
		// UpdateEMA weights the previous value at 0.85, so a transient DB
		// error silently zeroed this bucket's whole learning history and the
		// upsert then overwrote it. ErrNoRows is the legitimate new-bucket
		// case and keeps the zero baseline; anything else skips the window.
		switch scanErr := w.db.QueryRow(ctx, `
			SELECT COALESCE(ema_reward, 0)
			FROM task_model_affinity
			WHERE task_type = $1 AND profile = $2 AND canonical_id = $3 AND tenant_id = $4
		`, a.taskType, a.profile, a.canonicalID, a.tenantID).Scan(&prevEMA); {
		case errors.Is(scanErr, pgx.ErrNoRows):
			// New bucket — prevEMA stays 0.
		case scanErr != nil:
			slog.Warn("auto_route_affinity: prev EMA read failed, skipping bucket",
				"task_type", a.taskType, "profile", a.profile,
				"canonical_id", a.canonicalID, "tenant_id", a.tenantID,
				"error", scanErr)
			continue
		}

		// Fold this window's average reward into the running EMA. The EMA is the
		// signal shrinkage pulls toward (via avg_reward); a single bad window
		// cannot erase a long good history (alpha 0.15).
		emaReward := autoroute.UpdateEMA(prevEMA, a.avgReward)

		// sample_count = this window's count. The window already covers the
		// "history"" callers care about (rolling 14d). Adding prevSamples was
		// double-counting and inflated confidence without bound.
		totalSamples := a.sampleCount
		affinity := autoroute.ShrinkAffinity(emaReward, totalSamples)
		confidence := autoroute.AffinityConfidence(totalSamples)

		successRate := 0.0
		if a.sampleCount > 0 {
			successRate = float64(a.successCount) / float64(a.sampleCount)
		}

		_, err := w.db.Exec(ctx, `
			INSERT INTO task_model_affinity (
				task_type, profile, canonical_id, canonical_model, tenant_id,
				sample_count, success_count, success_rate,
				avg_reward, ema_reward, avg_latency_ms, avg_cost_usd, avg_health,
				affinity, confidence, first_seen_at, last_sampled_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8,
				$9, $10, $11, $12, $13,
				$14, $15, NOW(), NOW(), NOW()
			)
			ON CONFLICT (task_type, profile, canonical_id, tenant_id) DO UPDATE SET
				canonical_model = EXCLUDED.canonical_model,
				sample_count    = EXCLUDED.sample_count,
				success_count   = EXCLUDED.success_count,
				success_rate    = EXCLUDED.success_rate,
				avg_reward      = EXCLUDED.avg_reward,
				ema_reward      = EXCLUDED.ema_reward,
				avg_latency_ms  = EXCLUDED.avg_latency_ms,
				avg_cost_usd    = EXCLUDED.avg_cost_usd,
				avg_health      = EXCLUDED.avg_health,
				affinity        = EXCLUDED.affinity,
				confidence      = EXCLUDED.confidence,
				last_sampled_at = NOW(),
				updated_at      = NOW()
		`,
			a.taskType, a.profile, a.canonicalID, a.canonicalModel, a.tenantID,
			totalSamples, a.successCount, successRate,
			a.avgReward, emaReward, a.avgLatencyMs, a.avgCostUSD, a.avgHealth,
			affinity, confidence,
		)
		if err != nil {
			slog.Debug("affinity upsert failed",
				"task_type", a.taskType, "canonical_id", a.canonicalID, "error", err)
			continue
		}

		scope := "platform"
		if a.tenantID != "" {
			scope = "tenant"
			tenantRows++
		} else {
			platformRows++
		}
		autoRouteAffinityRows.WithLabelValues(scope).Inc()
	}
	return platformRows, tenantRows
}

// applyStalenessDecay pulls every row that was NOT sampled this sweep back
// toward neutral, proportional to how long it has been idle.
//
// This is the worker-death safety net: if this worker stops running, affinity
// converges to "no opinion" rather than pinning routing to a frozen verdict.
// Rows that WERE sampled this sweep already have a fresh last_sampled_at and are
// untouched by the decay function.
func (w *AutoRouteAffinityWorker) applyStalenessDecay(ctx context.Context) error {
	// Decaying in SQL would need the exact decay formula mirrored here; doing it
	// row-by-row in Go reuses the already-tested DecayAffinity and stays
	// consistent with how AffinityStore.Lookup reads it back.
	rows, err := w.db.Query(ctx, `
		SELECT task_type, profile, canonical_id, tenant_id, affinity, last_sampled_at
		FROM task_model_affinity
		WHERE last_sampled_at IS NOT NULL
		  AND last_sampled_at < NOW() - $1::interval
	`, autoroute.AffinityStaleAfter.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	type staleRow struct {
		taskType, profile, tenantID string
		canonicalID                 int64
		affinity                    float64
		lastSampled                 time.Time
	}
	var stale []staleRow
	for rows.Next() {
		var r staleRow
		// Scan errors in pgx v5 terminate iteration. Treat as sweep-fatal.
		if err := rows.Scan(&r.taskType, &r.profile, &r.canonicalID, &r.tenantID,
			&r.affinity, &r.lastSampled); err != nil {
			return err
		}
		stale = append(stale, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	now := time.Now()
	for _, r := range stale {
		decayed := autoroute.DecayAffinity(r.affinity, r.lastSampled, now)
		// Skip the write if decay moved the value by less than the column's
		// precision — avoids churning updated_at on every sweep for no effect.
		if absFloat(decayed-r.affinity) < 0.005 {
			continue
		}
		_, _ = w.db.Exec(ctx, `
			UPDATE task_model_affinity
			SET affinity = $5, updated_at = NOW()
			WHERE task_type = $1 AND profile = $2 AND canonical_id = $3 AND tenant_id = $4
		`, r.taskType, r.profile, r.canonicalID, r.tenantID, decayed)
	}
	return nil
}

// recomputeRanks re-numbers rows within each (task, profile, tenant) scope by
// affinity descending, so the store's Ranking read and the admin view agree.
func (w *AutoRouteAffinityWorker) recomputeRanks(ctx context.Context) error {
	// Window function in one statement: rank within the scope, ordered by the
	// stored affinity (which already incorporates decay), tie-broken by sample
	// count so a better-evidenced row outranks a thin one at equal affinity.
	_, err := w.db.Exec(ctx, `
		WITH ranked AS (
			SELECT task_type, profile, canonical_id, tenant_id,
			       ROW_NUMBER() OVER (
			           PARTITION BY task_type, profile, tenant_id
			           ORDER BY affinity DESC, sample_count DESC
			       ) AS new_rank
			FROM task_model_affinity
		)
		UPDATE task_model_affinity tma
		SET rank = ranked.new_rank, updated_at = NOW()
		FROM ranked
		WHERE tma.task_type = ranked.task_type
		  AND tma.profile   = ranked.profile
		  AND tma.canonical_id = ranked.canonical_id
		  AND tma.tenant_id  = ranked.tenant_id
		  AND (tma.rank IS DISTINCT FROM ranked.new_rank)
	`)
	return err
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
