// bg/auto_route_settle_worker.go — settle auto-route selections into rewards.
//
// Ref: docs/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md §2.3
//
// auto_route_selections rows are written at decision time, when the outcome is
// not yet known. This worker fills in the outcome and computes the reward that
// the affinity rollup then learns from.
//
// Per sweep (every 5 minutes):
//  1. Compute per-task-type COHORT baselines (p95 latency, p75 cost) across all
//     models. Cohort, not per-model: scoring a model against its own p95 always
//     yields ~neutral and cannot separate a fast model from a slow one, which
//     was the latent defect in the existing tuning_signals path (D2).
//  2. Claim a batch of unsettled rows older than the settle delay.
//  3. Join request_logs for success / latency / cost.
//  4. Where the session has settled and this model served >=80% of it, fold in
//     a routing-only session health component (see admin.RoutingHealthConfig:
//     compliance/injection/PII/toxicity/abandonment are excluded because they
//     are properties of the traffic, not of the model).
//  5. Compute reward and stamp settled_at.
//
// Rows whose request never appears in request_logs (dropped telemetry, or a
// request that died before logging) are stamped settled with a NULL reward
// after the abandon window, so the unsettled backlog cannot grow without bound.

package bg

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	// settleInterval is the sweep cadence.
	settleInterval = 5 * time.Minute

	// settleDelay is how long to wait after the decision before settling, so
	// request_logs has been written and the row is not settled prematurely.
	settleDelay = 2 * time.Minute

	// settleBatchSize bounds one sweep.
	settleBatchSize = 500

	// settleAbandonAfter is when a row with no matching request_log is given up
	// on and stamped settled with a NULL reward.
	settleAbandonAfter = 24 * time.Hour

	// baselineWindow is the lookback for cohort baselines.
	baselineWindow = 24 * time.Hour
)

var (
	autoRouteSettledTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_autoroute_settled_total",
			Help: "Auto-route selections settled, by outcome",
		},
		[]string{"outcome"}, // rewarded / abandoned
	)

	autoRouteSettleLagSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "llmgw_autoroute_settle_lag_seconds",
			Help:    "Seconds between the routing decision and its settlement",
			Buckets: []float64{60, 120, 300, 600, 1800, 3600, 21600, 86400},
		},
	)

	autoRouteRewardScore = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llmgw_autoroute_reward",
			Help:    "Distribution of routing-attributed reward",
			Buckets: []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0},
		},
		[]string{"task_type"},
	)
)

// taskBaseline is the per-task-type cohort baseline used to normalise latency
// and cost into comparable [0,1] scores.
type taskBaseline struct {
	P95LatencyMs int
	P75CostUSD   float64
}

// AutoRouteSettleWorker backfills outcomes and rewards.
type AutoRouteSettleWorker struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}

	// MEDIUM-4 fix: guard against Start() never being called (Stop would block
	// forever on a never-closed done channel) and against double Start() (would
	// launch a second run() that double-closes done). Mirrors the pattern in
	// bg/tuning_store_refresher.go which fixed this same regression in
	// 2026-07-27.
	stopOnce sync.Once
	started  atomic.Bool
}

// NewAutoRouteSettleWorker constructs the worker.
func NewAutoRouteSettleWorker(db *pgxpool.Pool) *AutoRouteSettleWorker {
	return &AutoRouteSettleWorker{db: db, done: make(chan struct{})}
}

// Start launches the sweep loop. Returns immediately. Idempotent: a second
// call is a no-op.
func (w *AutoRouteSettleWorker) Start(ctx context.Context) {
	if !w.started.CompareAndSwap(false, true) {
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	go w.run(ctx)
	slog.Info("auto-route settle worker started", "interval", settleInterval.String())
}

// Stop cancels and waits for the loop to exit. Safe on a never-Started worker
// and safe to call twice.
func (w *AutoRouteSettleWorker) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	if !w.started.Load() {
		// run() never launched, so done is never closed.
		return
	}
	<-w.done
}

func (w *AutoRouteSettleWorker) run(ctx context.Context) {
	defer close(w.done)

	ticker := time.NewTicker(settleInterval)
	defer ticker.Stop()

	// Stagger the first sweep so boot is not contended by every worker at once.
	select {
	case <-ctx.Done():
		return
	case <-time.After(90 * time.Second):
		w.sweep(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}

func (w *AutoRouteSettleWorker) sweep(ctx context.Context) {
	if w.db == nil {
		return
	}
	sweepCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()

	baselines, err := w.loadTaskBaselines(sweepCtx)
	if err != nil {
		// Without baselines latency/cost fall back to neutral rather than being
		// scored wrongly, so this is a degradation, not a failure.
		slog.Warn("auto-route settle: cohort baselines unavailable, using neutral", "error", err)
		baselines = map[string]taskBaseline{}
	}

	settled, abandoned, err := w.settleBatch(sweepCtx, baselines)
	if err != nil {
		slog.Warn("auto-route settle sweep failed", "error", err)
		return
	}
	if settled > 0 || abandoned > 0 {
		slog.Info("auto-route selections settled",
			"rewarded", settled, "abandoned", abandoned)
	}
}

// All three queries in this worker read from request_logs_hot ONLY.
//
// DO NOT add `UNION ALL request_logs` here. The request_logs partitions use the
// citus columnar access method, and a UNION ALL containing the partitioned
// PARENT fails at plan time with:
//     ERROR: invalid perminfoindex 0 in RTE with relid 0
// (PG 17.10 / citus 13.3, verified 2026-08-11; leaf partitions are fine, the
// parent is not). Hot-only is also sufficient: this worker never looks further
// back than settleAbandonAfter (24h) and baselineWindow (24h), both well inside
// request_logs_hot retention (~7 days, migration 399). See
// docs/HANDOFF_478_CRITICAL_FIXES.md CRITICAL-1.
//
//   - loadTaskBaselines: percentiles of latency/cost over recent hot rows.
//     Percentiles do NOT merge across separate tables; one statement is correct.
//   - settleBatch: outcome join (LEFT JOIN ... ON rl.request_id = ...).
//     All 2min-24h rows live in _hot.
//   - settleBatch LATERAL: count + retry_count over the session.
//
// If a future change ever needs to look past ~6 days through _hot, the answer
// is not to reintroduce this union. Either run two queries and merge in Go
// (lookup-style merge is safe; percentile merge is not), or use LATERAL per
// partition explicitly.

// loadTaskBaselines computes cohort p95 latency and p75 cost per task type over
// the recent window, across every model.
//
// These are the baselines the reward function normalises against. Using the
// cohort (rather than each model's own history) is what lets the reward
// distinguish a fast model from a slow one — a per-model baseline would score
// every model ~neutral against its own history.
func (w *AutoRouteSettleWorker) loadTaskBaselines(ctx context.Context) (map[string]taskBaseline, error) {
	rows, err := w.db.Query(ctx, `
		SELECT task_type,
		       COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::int AS p95_latency_ms,
		       COALESCE(percentile_cont(0.75) WITHIN GROUP (ORDER BY cost_usd), 0)        AS p75_cost_usd
		FROM request_logs_hot rl
		WHERE rl.ts >= NOW() - $1::interval
		  AND rl.is_auto_request = TRUE
		  AND rl.latency_ms IS NOT NULL
		GROUP BY task_type
	`, baselineWindow.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]taskBaseline)
	for rows.Next() {
		var (
			taskType string
			b        taskBaseline
		)
		// Scan failures in pgx v5 are FATAL to iteration (rows.fatal sets the
		// error and stops yielding rows). A `continue` here would silently drop
		// every remaining row. Treat any scan error as the sweep-fatal condition
		// it actually is and let rows.Err() report it.
		if err := rows.Scan(&taskType, &b.P95LatencyMs, &b.P75CostUSD); err != nil {
			return nil, err
		}
		if taskType != "" {
			out[taskType] = b
		}
	}
	return out, rows.Err()
}

// pendingSelection is one row awaiting settlement, joined with its outcome.
type pendingSelection struct {
	id            int64
	partitionDate time.Time
	requestID     string
	taskType      string
	canonicalID   *int64 // as written at decision time; nil on cache-reuse path
	ts            time.Time

	storageTier string

	// From request_logs_hot; nil when the request was never logged.
	success   *bool
	latencyMs *int
	costUSD   *float64

	// rlCanonicalID / rlTenantID are the values the settle worker backfills
	// onto auto_route_selections.canonical_id / tenant_id when the decision-time
	// value is missing. Both are nullable in request_logs_hot (canonical_id) and
	// left NULL by some legacy inserts (tenant_id).
	rlCanonicalID *int64
	rlTenantID    *string

	// Session attribution inputs.
	sessionHealth  *int // session_summaries.health_score, NULL until computed
	sessionErrors  *int
	sessionReqs    *int
	modelReqsInSes *int
	retryCount     *int
}

func (w *AutoRouteSettleWorker) settleBatch(
	ctx context.Context,
	baselines map[string]taskBaseline,
) (settled, abandoned int, err error) {
	// One query gathers the row, its request outcome, and the session context
	// needed for attribution. LEFT JOINs throughout: a missing session or a
	// not-yet-computed health score must not hide the row.
	//
	// Outcome comes from request_logs_hot only: at the 2-minute settle lag the
	// row is still in _hot (retention ~7d), and the columnar parent of
	// request_logs cannot participate in a set operation (see requestLogSource
	// comment + docs/HANDOFF_478_CRITICAL_FIXES.md).
	//
	// retry_count is the count of routing_attempts entries beyond the first —
	// i.e. actual credential-level failovers for THIS request. It is NOT
	// "calls to this model minus one": a normal multi-turn session making N
	// sequential calls is N requests, not N-1 retries. Earlier code made that
	// mistake and penalised healthy conversation flows.
	rows, qErr := w.db.Query(ctx, `
			SELECT s.id, s.partition_date, s.request_id, s.task_type, s.canonical_id, s.ts,
			       rl.success, rl.latency_ms, rl.cost_usd,
			       rl.canonical_id        AS rl_canonical_id,
			       LEFT(rl.tenant_id, 64) AS rl_tenant_id,
			       ss.health_score, ss.error_count, ss.request_count,
			       mr.model_reqs, mr.retry_count, s.storage_tier
			FROM (
				SELECT id, partition_date, request_id, task_type, canonical_id, ts, session_id, storage_tier
				FROM (
					SELECT id, partition_date, request_id, task_type, canonical_id, ts, session_id, 'hot'::text AS storage_tier
					FROM auto_route_selections_hot
					WHERE settled_at IS NULL AND ts < NOW() - $1::interval
					UNION ALL
					SELECT p.id, p.partition_date, p.request_id, p.task_type, p.canonical_id, p.ts, p.session_id, 'parent'::text AS storage_tier
					FROM auto_route_selections p
					WHERE p.settled_at IS NULL AND p.ts < NOW() - $1::interval
					  AND NOT EXISTS (
						SELECT 1 FROM auto_route_selections_hot h
						WHERE h.id = p.id AND h.partition_date = p.partition_date
					  )
				) pending
				ORDER BY CASE WHEN storage_tier = 'hot' THEN 0 ELSE 1 END, ts
				LIMIT $2
			) s
			LEFT JOIN request_logs_hot rl
			       ON rl.request_id = s.request_id
			LEFT JOIN session_summaries ss
			       ON ss.session_key = s.session_id
			LEFT JOIN LATERAL (
			       SELECT COUNT(*)::int AS model_reqs,
			              SUM(GREATEST(COALESCE(jsonb_array_length(r2.routing_attempts), 1) - 1, 0))::int AS retry_count
			       FROM request_logs_hot r2
			       WHERE s.session_id IS NOT NULL
			         AND r2.gw_session_id = s.session_id
			         AND s.canonical_id IS NOT NULL
			         AND r2.canonical_id = s.canonical_id
			) mr ON TRUE
	`, settleDelay.String(), settleBatchSize)
	if qErr != nil {
		return 0, 0, qErr
	}
	defer rows.Close()

	pending := make([]pendingSelection, 0, settleBatchSize)
	for rows.Next() {
		var p pendingSelection
		// Scan errors in pgx v5 terminate iteration. Treat as sweep-fatal.
		if scanErr := rows.Scan(
			&p.id, &p.partitionDate, &p.requestID, &p.taskType, &p.canonicalID, &p.ts,
			&p.success, &p.latencyMs, &p.costUSD,
			&p.rlCanonicalID, &p.rlTenantID,
			&p.sessionHealth, &p.sessionErrors, &p.sessionReqs,
			&p.modelReqsInSes, &p.retryCount, &p.storageTier,
		); scanErr != nil {
			return 0, 0, scanErr
		}
		pending = append(pending, p)
	}
	if rErr := rows.Err(); rErr != nil {
		return 0, 0, rErr
	}

	now := time.Now()
	for _, p := range pending {
		if p.success == nil {
			// No request_log yet. Give it time, then abandon so the backlog
			// (and the idx_ars_unsettled index) cannot grow forever.
			if now.Sub(p.ts) > settleAbandonAfter {
				if aErr := w.abandon(ctx, p); aErr == nil {
					abandoned++
					autoRouteSettledTotal.WithLabelValues("abandoned").Inc()
				}
			}
			continue
		}

		reward, source := computeSelectionReward(p, baselines[p.taskType])
		if uErr := w.writeReward(ctx, p, reward, source); uErr != nil {
			slog.Debug("auto-route settle write failed", "request_id", p.requestID, "error", uErr)
			continue
		}
		settled++
		autoRouteSettledTotal.WithLabelValues("rewarded").Inc()
		autoRouteSettleLagSeconds.Observe(now.Sub(p.ts).Seconds())
		autoRouteRewardScore.WithLabelValues(p.taskType).Observe(reward)
	}

	return settled, abandoned, nil
}

// computeSelectionReward turns a settled row into a reward in [0,1].
//
// Returns the reward and its attribution source: "session" when session-level
// health was folded in, "request" when only per-request signals were available.
func computeSelectionReward(p pendingSelection, base taskBaseline) (float64, string) {
	in := autoroute.RewardInput{
		HealthComponent: -1, // -1 => neutral; overridden below when attributable
	}

	if p.success != nil && *p.success {
		in.Success = 1.0
	}
	if p.latencyMs != nil {
		in.LatencyMs = *p.latencyMs
	}
	if p.costUSD != nil {
		in.CostUSD = *p.costUSD
	}
	in.P95BaselineMs = base.P95LatencyMs
	in.P75BaselineCost = base.P75CostUSD

	if p.modelReqsInSes != nil && p.retryCount != nil && *p.modelReqsInSes > 0 {
		in.RetryRatio = float64(*p.retryCount) / float64(*p.modelReqsInSes)
	}

	source := "request"
	// Attribute session health only when the session has settled AND this model
	// carried the clear majority of it. Otherwise a model that served one call
	// of twenty would inherit other models' failures.
	if p.sessionHealth != nil && p.modelReqsInSes != nil && p.sessionReqs != nil &&
		autoroute.ShouldAttributeSession(*p.modelReqsInSes, *p.sessionReqs) {
		in.HealthComponent = routingOnlyHealth(*p.sessionHealth, p)
		source = "session"
	}

	return autoroute.ComputeRoutingReward(in), source
}

// routingOnlyHealth converts session health into the [0,1] component the reward
// may use.
//
// session_summaries.health_score is 0-100 but includes penalties a model cannot
// be blamed for (abandonment, compliance, injection, PII, toxicity). Rather than
// reverse-engineering those out of the stored total — the individual penalty
// items are not persisted — this recomputes a routing-only view from the
// error-rate signal that IS available, and uses the stored score only as a
// sanity ceiling.
func routingOnlyHealth(storedHealth int, p pendingSelection) float64 {
	// Error rate is the routing-relevant part we can reconstruct exactly.
	errRate := 0.0
	if p.sessionErrors != nil && p.sessionReqs != nil && *p.sessionReqs > 0 {
		errRate = float64(*p.sessionErrors) / float64(*p.sessionReqs)
	}
	routing := 1.0 - errRate

	// The stored score is a ceiling: if the overall session went badly for
	// reasons we are excluding, do not credit the model with a perfect score.
	ceiling := float64(storedHealth) / 100.0
	if ceiling < 0 {
		ceiling = 0
	}
	if ceiling > 1 {
		ceiling = 1
	}
	// Average of the two: reflects routing quality while staying anchored to
	// the session's actual observed outcome.
	return (routing + ceiling) / 2.0
}

func (w *AutoRouteSettleWorker) writeReward(
	ctx context.Context,
	p pendingSelection,
	reward float64,
	source string,
) error {
	// COALESCE preserves any decision-time value (future Fix D path), so a
	// later write from a non-null decision never gets overwritten by a NULL
	// from request_logs_hot (e.g. legacy rows written before canonical_id was
	// a populated column).
	table := "auto_route_selections"
	if p.storageTier == "hot" {
		table = "auto_route_selections_hot"
	}
	_, err := w.db.Exec(ctx, `
		UPDATE `+table+`
		SET success       = $1,
		    latency_ms    = $2,
		    cost_usd      = $3,
		    reward        = $4,
		    reward_source = $5,
		    canonical_id  = COALESCE(canonical_id, $6),
		    tenant_id     = COALESCE(tenant_id,    $7),
		    settled_at    = NOW()
		WHERE id = $8 AND partition_date = $9
		  AND settled_at IS NULL
	`, p.success, p.latencyMs, p.costUSD, reward, source,
		p.rlCanonicalID, p.rlTenantID, p.id, p.partitionDate)
	return err
}

// abandon stamps a row settled with no reward, so it stops being scanned and is
// excluded from learning (the affinity rollup requires reward IS NOT NULL).
func (w *AutoRouteSettleWorker) abandon(ctx context.Context, p pendingSelection) error {
	table := "auto_route_selections"
	if p.storageTier == "hot" {
		table = "auto_route_selections_hot"
	}
	_, err := w.db.Exec(ctx, `
		UPDATE `+table+`
		SET settled_at = NOW(), reward_source = 'request'
		WHERE id = $1 AND partition_date = $2 AND settled_at IS NULL
	`, p.id, p.partitionDate)
	return err
}
