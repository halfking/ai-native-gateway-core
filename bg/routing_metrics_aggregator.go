// bg/routing_metrics_aggregator.go — roll routing_feedback_log into
// routing_optimization_metrics.
//
// Ref: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §2.4
// ("5分钟后聚合到 metrics 表").
//
// The P2.2 feedback batch writer inserts one routing_feedback_log row per
// auto-routed request. Nothing populated the 5-minute metrics rollup the
// design doc promised, so admin stats (GetAggregatedMetrics) always read an
// empty table. This worker closes that loop.
//
// Per sweep (every 5 minutes), recompute the recent metric windows inside ONE
// transaction:
//  1. Aggregate the last routingMetricsLookback of feedback rows into buckets
//     aligned to routingMetricsBucket (5 min), GROUPING SETS over
//     ()/(task_type)/(predicted_provider) — i.e. a global, a per-task and a
//     per-provider row per bucket (matching the partial indexes on the table).
//  2. DELETE + re-INSERT those buckets instead of upserting. The table's
//     UNIQUE constraint does not dedupe NULL dimensions under ON CONFLICT
//     (NULL != NULL in PG unique semantics), so an upsert would stack a fresh
//     global row every sweep. Within one transaction readers see either the
//     old complete buckets or the new ones, never a half-written window.
//  3. Recomputing a lookback window (not just the last bucket) is what folds
//     in late arrivals: the P2.1 annotator import backfills
//     has_human_correction onto already-inserted rows via MarkHumanCorrection.
//
// human_accuracy_rate uses the same weighted formula as
// routingopt.WeightedAccuracy: (auto + 2×human) / (total + 2×human), where a
// human annotation "agrees" when predicted_provider = correct_provider.
//
// A bounded daily DELETE caps routing_feedback_log growth (aggregates survive
// in the metrics table; raw rows are only needed inside the learner's live
// window).

package bg

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin/distlock"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	// routingMetricsInterval is the sweep cadence; it matches the bucket size
	// so each completed bucket is recomputed at least once after it closes.
	routingMetricsInterval = 5 * time.Minute

	// routingMetricsBucket is the documented bucket alignment of
	// routing_optimization_metrics.time_bucket (see the COMMENT ON COLUMN in
	// migration 670).
	routingMetricsBucket = 5 * time.Minute

	// routingMetricsLookback is how far back each sweep recomputes. Six
	// buckets covers normal feedback-batch write latency (≤5s) and annotator
	// imports landing within half an hour of the request.
	routingMetricsLookback = 30 * time.Minute

	// routingMetricsFirstDelay staggers the first sweep off gateway boot (same
	// rationale as startAdaptiveMaintenance: never compete with the boot-time
	// DB migration/refresh window).
	routingMetricsFirstDelay = 90 * time.Second

	// routingFeedbackRetention bounds the raw feedback log; older rows are
	// deleted in batches once a day.
	routingFeedbackRetention = 30 * 24 * time.Hour

	// routingFeedbackTrimBatch bounds one DELETE round.
	routingFeedbackTrimBatch = 5000

	// routingFeedbackTrimMaxRounds bounds one daily trim sweep.
	routingFeedbackTrimMaxRounds = 40

	// routingMetricsDistLockTTL bounds how long the Redis-elected leader holds
	// the aggregation token. Same convention as settleDistLockTTL: comfortably
	// above the sweep timeout (4min) so a slow-but-alive sweep never loses its
	// lease mid-cycle.
	routingMetricsDistLockTTL = 6 * time.Minute
)

var (
	routingMetricsSweepsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_routingopt_metrics_sweeps_total",
			Help: "routing_metrics_aggregator sweeps, by outcome",
		},
		[]string{"outcome"}, // ok / error
	)

	routingMetricsRowsUpserted = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "llmgw_routingopt_metrics_rows_written",
			Help:    "routing_optimization_metrics rows written per sweep",
			Buckets: []float64{0, 5, 20, 50, 100, 250, 500, 1000},
		},
	)

	routingMetricsSweepSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "llmgw_routingopt_metrics_sweep_seconds",
			Help:    "Duration of routing_metrics_aggregator sweeps",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		},
	)

	routingFeedbackTrimmedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "llmgw_routingopt_feedback_trimmed_total",
			Help: "routing_feedback_log rows removed by retention trimming",
		},
	)
)

// RoutingMetricsAggregator rolls raw routing feedback into the 5-minute
// metrics table and trims the raw log. It only runs when the P2.2 optimizer
// is enabled (feedback rows only exist then); see cmd/gateway wiring.
type RoutingMetricsAggregator struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}

	// Same Start/Stop hardening as AutoRouteSettleWorker: Stop on a
	// never-started worker must not block, double Start must not double-run.
	stopOnce sync.Once
	started  atomic.Bool

	lastTrim time.Time

	// R31 pattern alignment (docs/audit/2026-09-16-r30-24h-audit-round.md
	// §四#1): token-bucket leader election so a blue-green pair does not
	// double-run every sweep. Correctness is already guaranteed by the
	// advisory lock inside recomputeMetrics — this saves the follower's
	// wasted 30min-window scan. Without Redis (nil manager / disabled /
	// Acquire error) both instances sweep exactly as before.
	distLock distlock.Manager
}

// SetDistLock wires the shared Redis token-bucket election manager.
// Optional: nil (default) keeps the worker sweeping on every instance,
// serialized only by the in-transaction advisory lock.
func (w *RoutingMetricsAggregator) SetDistLock(mgr distlock.Manager) {
	w.distLock = mgr
}

// NewRoutingMetricsAggregator constructs the worker. A nil pool is allowed at
// construction (the sweep no-ops) so callers can wire unconditionally.
func NewRoutingMetricsAggregator(db *pgxpool.Pool) *RoutingMetricsAggregator {
	return &RoutingMetricsAggregator{db: db, done: make(chan struct{})}
}

// Start launches the sweep loop. Returns immediately. Idempotent.
func (w *RoutingMetricsAggregator) Start(ctx context.Context) {
	if !w.started.CompareAndSwap(false, true) {
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	go w.run(ctx)
	slog.Info("routing metrics aggregator started",
		"interval", routingMetricsInterval.String(),
		"lookback", routingMetricsLookback.String())
}

// Stop cancels and waits for the loop to exit. Safe on a never-Started worker
// and safe to call twice.
func (w *RoutingMetricsAggregator) Stop() {
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

func (w *RoutingMetricsAggregator) run(ctx context.Context) {
	// Panic guard, same as AutoRouteSettleWorker: a sweep panic must neither
	// kill the process nor hang Stop on a never-closed done channel.
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("routing metrics aggregator panic", "recover", rec)
		}
	}()
	defer close(w.done)

	ticker := time.NewTicker(routingMetricsInterval)
	defer ticker.Stop()

	select {
	case <-ctx.Done():
		return
	case <-time.After(routingMetricsFirstDelay):
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
func (w *RoutingMetricsAggregator) safeSweep(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("routing metrics sweep panic (cycle skipped, worker alive)", "recover", rec)
		}
	}()
	w.sweep(ctx)
}

func (w *RoutingMetricsAggregator) sweep(ctx context.Context) {
	if w.db == nil {
		return
	}
	sweepCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	started := time.Now()

	// R31 pattern alignment: Redis-elected leader runs the sweep, followers
	// skip without queueing. Without Redis both instances sweep as before
	// (the advisory lock inside recomputeMetrics already kept that correct).
	if h := acquireSweepDistLock(sweepCtx, w.distLock, "routing_metrics_aggregate", routingMetricsDistLockTTL, "routing_metrics_aggregate"); h != nil {
		defer h.Release(context.WithoutCancel(sweepCtx))
		if !h.IsLeader() {
			slog.Info("routing metrics aggregation skipped, redis token held by another instance")
			return
		}
	}

	rows, err := w.recomputeMetrics(sweepCtx)
	if err != nil {
		routingMetricsSweepsTotal.WithLabelValues("error").Inc()
		slog.Warn("routing metrics aggregation sweep failed", "error", err)
		return
	}
	routingMetricsSweepsTotal.WithLabelValues("ok").Inc()
	routingMetricsRowsUpserted.Observe(float64(rows))
	routingMetricsSweepSeconds.Observe(time.Since(started).Seconds())

	w.trimFeedback(sweepCtx)
}

// recomputeMetrics rewrites the recent metric buckets in one transaction and
// returns how many rows it wrote. windowStart is bucket-aligned so a sweep
// never rewrites a bucket partially covered by the lookback.
func (w *RoutingMetricsAggregator) recomputeMetrics(ctx context.Context) (int, error) {
	now := time.Now()
	windowStart := now.Add(-routingMetricsLookback).Truncate(routingMetricsBucket)

	tx, err := w.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// R30 P2-3（2026-09-16）：双实例（蓝绿双活）同时聚合时，非 NULL 维度撞
	// UNIQUE 报错（下轮自愈），但 NULL 维度的 global 行没有任何唯一约束——
	// 交错提交会留下同桶重复行，GetAggregatedMetrics 的 SUM 双倍计数。
	// 事务级 advisory lock 把重算串行化。
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('public.routing_optimization_metrics_recompute', 0))`); err != nil {
		return 0, err
	}

	// Delete first, then insert: MVCC keeps readers on the pre-sweep snapshot
	// until commit, so metric consumers never observe a gap.
	if _, err := tx.Exec(ctx, `
		DELETE FROM routing_optimization_metrics
		WHERE time_bucket >= $1
	`, windowStart); err != nil {
		return 0, err
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO routing_optimization_metrics (
			time_bucket, task_type, predicted_provider,
			total_requests, successful_requests, failed_requests, accuracy_rate,
			avg_confidence, avg_latency_ms, avg_cost,
			p50_latency_ms, p95_latency_ms, p99_latency_ms,
			human_corrections, human_accuracy_rate
		)
		WITH agg AS (
			SELECT
				date_trunc('minute', created_at)
					- (EXTRACT(minute FROM created_at)::int % 5) * interval '1 minute' AS bucket,
				task_type,
				predicted_provider,
				COUNT(*) AS total,
				SUM(CASE WHEN success THEN 1 ELSE 0 END) AS successful,
				AVG(confidence) AS avg_confidence,
				AVG(actual_latency_ms) AS avg_latency_ms,
				AVG(actual_cost) AS avg_cost,
				percentile_cont(0.5)  WITHIN GROUP (ORDER BY actual_latency_ms) AS p50_latency_ms,
				percentile_cont(0.95) WITHIN GROUP (ORDER BY actual_latency_ms) AS p95_latency_ms,
				percentile_cont(0.99) WITHIN GROUP (ORDER BY actual_latency_ms) AS p99_latency_ms,
				SUM(CASE WHEN has_human_correction THEN 1 ELSE 0 END) AS human_count,
				SUM(CASE WHEN has_human_correction
				          AND predicted_provider = correct_provider
				         THEN 1 ELSE 0 END) AS human_agree
			FROM routing_feedback_log
			WHERE created_at >= $1
			GROUP BY bucket, GROUPING SETS ((), (task_type), (predicted_provider))
		)
		SELECT
			bucket,
			task_type,
			predicted_provider,
			total,
			successful,
			total - successful,
			successful::float / NULLIF(total, 0),
			avg_confidence,
			avg_latency_ms::int,
			avg_cost,
			p50_latency_ms::int,
			p95_latency_ms::int,
			p99_latency_ms::int,
			human_count,
			-- Weighted accuracy, identical to routingopt.WeightedAccuracy:
			-- human annotations double-count. 0 human corrections leaves this
			-- NULL (the column's own CHECK allows NULL) rather than 1.0.
			CASE WHEN human_count > 0 THEN
				(successful::float + 2.0 * human_agree)
					/ NULLIF(total::float + 2.0 * human_count, 0)
			END
		FROM agg
	`, windowStart)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// trimFeedback deletes retention-expired raw feedback rows at most once a
// day, in bounded batches. Best-effort: a failure only skips this sweep's
// trim; the next day's sweep retries.
func (w *RoutingMetricsAggregator) trimFeedback(ctx context.Context) {
	if !w.lastTrim.IsZero() && time.Since(w.lastTrim) < 24*time.Hour {
		return
	}
	w.lastTrim = time.Now()

	cutoff := time.Now().Add(-routingFeedbackRetention)
	trimmed := 0
	for round := 0; round < routingFeedbackTrimMaxRounds; round++ {
		tag, err := w.db.Exec(ctx, `
			DELETE FROM routing_feedback_log
			WHERE id IN (
				SELECT id FROM routing_feedback_log
				WHERE created_at < $1
				LIMIT $2
			)
		`, cutoff, routingFeedbackTrimBatch)
		if err != nil {
			slog.Warn("routing feedback trim failed", "error", err, "trimmed_so_far", trimmed)
			return
		}
		trimmed += int(tag.RowsAffected())
		if tag.RowsAffected() < routingFeedbackTrimBatch {
			break
		}
	}
	if trimmed > 0 {
		routingFeedbackTrimmedTotal.Add(float64(trimmed))
		slog.Info("routing feedback log trimmed", "rows", trimmed, "cutoff", cutoff.Format(time.RFC3339))
	}
}
