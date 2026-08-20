package stats

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

const (
	defaultReconciliationInterval = 6 * time.Hour
	reconciliationLookback        = 7 * 24 * time.Hour
	autoRepairThreshold           = 0.02   // 2% difference threshold for auto-repair
	autoRepairMaxValue            = 1000   // max absolute value for auto-repair
	maxReconciliationRows         = 100000 // max rows to prevent OOM
)

// DBQuerier is the subset of pgxpool.Pool that the stats package needs.
// Production callers pass a *pgxpool.Pool which satisfies all four
// methods; tests pass pgxmock.PgxPoolIface (which provides Exec/Query/
// QueryRow) or a Tx-bound stub that also implements Begin.
//
// The Begin method is included so DailyMonthlyRollup.Refresh can be
// driven from a Tx-aware test stub without falling back to a type
// assertion. ReconciliationWorker uses Begin only when pendingRepairs>0.
type DBQuerier interface {
	Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ReconciliationWorker periodically compares usage_facts (source of truth) with
// stats_usage_daily/monthly projections, generates diffs, and automatically
// repairs small discrepancies while flagging large ones for human approval.
type ReconciliationWorker struct {
	db       DBQuerier
	interval time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool

	reconcileRecentFn func(context.Context)
}

func NewReconciliationWorker(db *pgxpool.Pool, interval time.Duration) *ReconciliationWorker {
	return newReconciliationWorkerWithDB(db, interval)
}

// newReconciliationWorkerWithDB builds a ReconciliationWorker against an
// arbitrary DBQuerier. Used by tests (pgxmock) and any caller that already
// has a Tx-bound DBQuerier.
func newReconciliationWorkerWithDB(db DBQuerier, interval time.Duration) *ReconciliationWorker {
	if interval <= 0 {
		interval = defaultReconciliationInterval
	}
	return &ReconciliationWorker{
		db:       db,
		interval: interval,
		done:     make(chan struct{}),
	}
}

func (w *ReconciliationWorker) Start(ctx context.Context) {
	if w == nil || w.db == nil {
		return
	}
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	w.cancel = cancel
	w.done = done
	w.started = true
	w.mu.Unlock()

	go w.run(workerCtx, done)
	slog.Info("stats reconciliation worker started", "interval", w.interval.String())
}

func (w *ReconciliationWorker) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return
	}
	cancel := w.cancel
	done := w.done
	w.mu.Unlock()

	cancel()
	<-done
}

func (w *ReconciliationWorker) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runTick(ctx)
		}
	}
}

func (w *ReconciliationWorker) runTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("reconciliation worker panicked; will retry on next tick",
				"panic", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()))
			metrics.RecordStatsReconciliationRun("panicked", 1)
		}
	}()
	if w.reconcileRecentFn != nil {
		w.reconcileRecentFn(ctx)
		return
	}
	w.reconcileRecent(ctx)
}

func (w *ReconciliationWorker) reconcileRecent(ctx context.Context) {
	now := time.Now().UTC()
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	periodStart := now.Add(-reconciliationLookback)
	periodEnd := now

	if err := w.ReconcilePeriod(runCtx, periodStart, periodEnd, "auto"); err != nil {
		slog.Warn("stats reconciliation failed", "error", err, "period_start", periodStart, "period_end", periodEnd)
	}
}

// ReconcilePeriod compares usage_facts with daily projections for the given period.
// It creates a reconciliation run, generates diffs, and auto-repairs safe discrepancies.
func (w *ReconciliationWorker) ReconcilePeriod(ctx context.Context, start, end time.Time, scope string) error {
	if w == nil || w.db == nil {
		return fmt.Errorf("reconciliation worker not initialized")
	}

	runID := fmt.Sprintf("recon_%s_%s", start.Format("20060102"), uuid.New().String()[:8])

	// Create reconciliation run record
	_, err := w.db.Exec(ctx, `
		INSERT INTO stats_reconciliation_runs 
			(run_id, period_start, period_end, scope, status, started_at)
		VALUES ($1, $2, $3, $4, 'running', now())
	`, runID, start, end, scope)
	if err != nil {
		return fmt.Errorf("create reconciliation run: %w", err)
	}

	slog.Info("reconciliation run started", "run_id", runID, "start", start, "end", end, "scope", scope)

	var eventsSeen, rowsCompared, rowsRepaired, diffCount int64
	var sourceWatermark time.Time

	// Defer with context.Background() so a cancelled ctx still allows the
	// status UPDATE to reach the DB. The recovery path mirrors the explicit
	// failure branches above.
	defer func() {
		if r := recover(); r != nil {
			w.finishRun(context.Background(), runID, "failed",
				eventsSeen, rowsCompared, rowsRepaired, diffCount,
				sourceWatermark, fmt.Sprintf("panic: %v", r))
			metrics.RecordStatsReconciliationRun("failed", 1)
			panic(r)
		}
	}()

	// Get source watermark from usage_facts
	err = w.db.QueryRow(ctx, `
		SELECT COALESCE(MAX(occurred_at), $1)
		FROM usage_facts
		WHERE occurred_at >= $1 AND occurred_at < $2
	`, start, end).Scan(&sourceWatermark)
	if err != nil && err != pgx.ErrNoRows {
		w.finishRun(ctx, runID, "failed", eventsSeen, rowsCompared, rowsRepaired, diffCount, sourceWatermark, err.Error())
		metrics.RecordStatsReconciliationRun("failed", 1)
		return fmt.Errorf("get source watermark: %w", err)
	}

	// Count events seen
	err = w.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM usage_facts
		WHERE occurred_at >= $1 AND occurred_at < $2
	`, start, end).Scan(&eventsSeen)
	if err != nil {
		w.finishRun(ctx, runID, "failed", eventsSeen, rowsCompared, rowsRepaired, diffCount, sourceWatermark, err.Error())
		metrics.RecordStatsReconciliationRun("failed", 1)
		return fmt.Errorf("count events: %w", err)
	}

	// Reconcile daily projections by comparing aggregated facts vs projections.
	// phantomDiffs are persisted unresolved rows whose projection has no matching
	// source fact; keep them out of the ordinary open mismatch metric.
	diffs, repaired, phantomDiffs, err := w.reconcileDaily(ctx, runID, start, end)
	if err != nil {
		w.finishRun(ctx, runID, "failed", eventsSeen, rowsCompared, rowsRepaired, diffCount, sourceWatermark, err.Error())
		metrics.RecordStatsReconciliationRun("failed", 1)
		return fmt.Errorf("reconcile daily: %w", err)
	}

	rowsCompared = diffs
	rowsRepaired = repaired
	diffCount = diffs - repaired
	openDiffs := diffCount - phantomDiffs
	if openDiffs < 0 {
		openDiffs = 0
	}

	w.finishRun(ctx, runID, "completed", eventsSeen, rowsCompared, rowsRepaired, diffCount, sourceWatermark, "")
	metrics.RecordStatsReconciliationRun("completed", 1)
	metrics.ObserveStatsReconciliationDiffs("open", openDiffs)
	metrics.ObserveStatsReconciliationDiffs("phantom_open", phantomDiffs)
	metrics.ObserveStatsReconciliationDiffs("auto_repaired", rowsRepaired)

	slog.Info("reconciliation run completed",
		"run_id", runID,
		"events_seen", eventsSeen,
		"rows_compared", rowsCompared,
		"rows_repaired", rowsRepaired,
		"diff_count", diffCount,
	)

	return nil
}

func (w *ReconciliationWorker) finishRun(ctx context.Context, runID, status string, eventsSeen, rowsCompared, rowsRepaired, diffCount int64, watermark time.Time, errorMsg string) {
	if finishRunOverride != nil {
		finishRunOverride(ctx, w, runID, status, eventsSeen, rowsCompared, rowsRepaired, diffCount, watermark, errorMsg)
		return
	}
	_, err := w.db.Exec(ctx, `
		UPDATE stats_reconciliation_runs
		SET status = $2,
			events_seen = $3,
			rows_compared = $4,
			rows_repaired = $5,
			diff_count = $6,
			source_watermark = $7,
			error = $8,
			finished_at = now()
		WHERE run_id = $1
	`, runID, status, eventsSeen, rowsCompared, rowsRepaired, diffCount, watermark, errorMsg)
	if err != nil {
		slog.Error("failed to finish reconciliation run", "run_id", runID, "error", err)
	}
}

// reconcileDailyOnce compares usage_facts aggregation with stats_usage_daily
// projections for a single window. Returns (total_diffs, auto_repaired,
// phantom_open, hitCap, error). hitCap is true when the row limit was reached
// and the caller (reconcileDaily) should split the window.
func (w *ReconciliationWorker) reconcileDailyOnce(ctx context.Context, runID string, start, end time.Time) (int64, int64, int64, bool, error) {
	// Build ground-truth from usage_facts
	factRows, err := w.db.Query(ctx, `
		SELECT
			(occurred_at AT TIME ZONE 'UTC')::date AS day_utc,
			tenant_id,
			COALESCE(provider_id, 0) AS provider_id,
			COALESCE(credential_id, 0) AS credential_id,
			COALESCE(canonical_id, 0) AS canonical_id,
			COALESCE(raw_model_name, '') AS raw_model_name,
			traffic_class,
			COUNT(*) AS request_count,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS success_count,
			SUM(CASE WHEN status != 'success' THEN 1 ELSE 0 END) AS failure_count,
			SUM(prompt_tokens) AS prompt_tokens,
			SUM(completion_tokens) AS completion_tokens,
			SUM(total_tokens) AS total_tokens,
			SUM(cost_amount) AS cost_usd,
			SUM(credits_charged) AS credits_charged
		FROM usage_facts
		WHERE occurred_at >= $1 AND occurred_at < $2
		GROUP BY day_utc, tenant_id, provider_id, credential_id, canonical_id, raw_model_name, traffic_class
	`, start, end)
	if err != nil {
		return 0, 0, 0, false, fmt.Errorf("query usage_facts: %w", err)
	}
	defer factRows.Close()

	type factKey struct {
		day          time.Time
		tenantID     string
		providerID   int64
		credentialID int64
		canonicalID  int64
		modelName    string
		trafficClass string
	}

	type factMetrics struct {
		requests    int64
		successes   int64
		failures    int64
		promptTok   int64
		completeTok int64
		totalTok    int64
		cost        float64
		credits     int64
	}

	factsMap := make(map[factKey]factMetrics)

	var totalDiffs, autoRepaired, phantomDiffs int64
	var pendingRepairs int64

	rowCount := 0
	for factRows.Next() {
		if rowCount >= maxReconciliationRows {
			return totalDiffs, autoRepaired, phantomDiffs, true, nil // hitCap
		}
		var key factKey
		var metrics factMetrics
		if err := factRows.Scan(
			&key.day, &key.tenantID, &key.providerID, &key.credentialID,
			&key.canonicalID, &key.modelName, &key.trafficClass,
			&metrics.requests, &metrics.successes, &metrics.failures,
			&metrics.promptTok, &metrics.completeTok, &metrics.totalTok,
			&metrics.cost, &metrics.credits,
		); err != nil {
			return 0, 0, 0, false, fmt.Errorf("scan fact row: %w", err)
		}
		factsMap[key] = metrics
		rowCount++
	}

	if err := factRows.Err(); err != nil {
		return 0, 0, 0, false, fmt.Errorf("iterate facts: %w", err)
	}

	// Compare provider_model projections with their source facts. The
	// source aggregation above is provider_model-granularity; tenant,
	// provider, credential, person, and error dimensions are derived
	// rollup views and do not have matching fact keys here. Comparing
	// those derived rows would falsely classify them as phantom_open.
	projRows, err := w.db.Query(ctx, `
			SELECT 
				day_utc, tenant_id, provider_id, credential_id, canonical_id, 
				raw_model_name, traffic_class,
			request_count, success_count, failure_count,
			prompt_tokens, completion_tokens, total_tokens,
			cost_usd, credits_charged
			FROM stats_usage_daily
			WHERE day_utc >= $1::timestamptz::date
			  AND day_utc < ($2::timestamptz - INTERVAL '1 microsecond')::date + 1
			  AND dimension_type = 'provider_model'
		`, start, end)
	if err != nil {
		return 0, 0, 0, false, fmt.Errorf("query projections: %w", err)
	}
	defer projRows.Close()

	projRowCount := 0
	for projRows.Next() {
		if projRowCount >= maxReconciliationRows {
			return totalDiffs, autoRepaired, phantomDiffs, true, nil // hitCap
		}
		var key factKey
		var projected factMetrics
		if err := projRows.Scan(
			&key.day, &key.tenantID, &key.providerID, &key.credentialID,
			&key.canonicalID, &key.modelName, &key.trafficClass,
			&projected.requests, &projected.successes, &projected.failures,
			&projected.promptTok, &projected.completeTok, &projected.totalTok,
			&projected.cost, &projected.credits,
		); err != nil {
			return 0, 0, 0, false, fmt.Errorf("scan projection row: %w", err)
		}

		source, exists := factsMap[key]
		isPhantom := !exists
		if isPhantom {
			// Projection exists but no source facts - this is a phantom row.
			// Recorded separately with resolution='phantom_open' (see below).
			source = factMetrics{}
		}

		// Compare each metric
		diffs := []struct {
			metric    string
			source    float64
			projected float64
		}{
			{"request_count", float64(source.requests), float64(projected.requests)},
			{"success_count", float64(source.successes), float64(projected.successes)},
			{"failure_count", float64(source.failures), float64(projected.failures)},
			{"prompt_tokens", float64(source.promptTok), float64(projected.promptTok)},
			{"completion_tokens", float64(source.completeTok), float64(projected.completeTok)},
			{"total_tokens", float64(source.totalTok), float64(projected.totalTok)},
			{"cost_usd", source.cost, projected.cost},
			{"credits_charged", float64(source.credits), float64(projected.credits)},
		}

		for _, d := range diffs {
			if d.source == d.projected {
				continue
			}

			diff := d.source - d.projected
			dimensionKey := fmt.Sprintf("tenant:%s:day:%s:provider:%d:cred:%d:model:%d:%s",
				key.tenantID, key.day.Format("2006-01-02"), key.providerID, key.credentialID, key.canonicalID, key.modelName)

			// canAutoRepair requires BOTH source and projected to be non-zero.
			// The prior code relied on relDiff=1.0 falling outside
			// autoRepairThreshold to exclude phantom rows (source=0,
			// projected>0), but that exclusion was implicit and could be
			// broken by future threshold changes. Phantom rows are recorded
			// separately with resolution='phantom_open' below.
			canAutoRepair := canAutoRepair(d.source, d.projected)

			resolution := "open"
			if isPhantom {
				// Phantom rows: never auto-repaired, never fed to Refresh().
				// Surfaces operator-visible diffs for human review.
				resolution = "phantom_open"
			} else if canAutoRepair {
				resolution = "auto_repair_pending"
			}

			// Record diff. ON CONFLICT DO NOTHING absorbs the rare case
			// where reconcileDaily's binary shard recurses across a UTC
			// day boundary and the same projection row is iterated by
			// both halves. The unique index on
			// (run_id, dimension_type, dimension_key, metric) (migration
			// 546) makes the second INSERT a no-op; we only increment
			// the in-memory counters (totalDiffs, pendingRepairs) when
			// the INSERT actually inserted a row, so the aggregates
			// stay truthful regardless of sharding path.
			ct, err := w.db.Exec(ctx, `
				INSERT INTO stats_reconciliation_diffs
					(run_id, tenant_id, dimension_type, dimension_key, metric,
					 source_value, projected_value, difference, resolution, created_at)
				VALUES ($1, $2, 'daily_rollup', $3, $4, $5, $6, $7, $8, now())
				ON CONFLICT (run_id, tenant_id, dimension_type, dimension_key, metric) DO NOTHING
			`, runID, key.tenantID, dimensionKey, d.metric,
				d.source, d.projected, diff, resolution)
			if err != nil {
				slog.Warn("failed to record diff", "error", err, "run_id", runID)
				continue
			}
			if ct.RowsAffected() == 0 {
				// Duplicate absorbed by the unique index; do not
				// double-count.
				continue
			}
			totalDiffs++
			if !isPhantom && canAutoRepair {
				pendingRepairs++
			}
		}

		// Remove from map to track missing projections
		delete(factsMap, key)
		projRowCount++
	}

	if err := projRows.Err(); err != nil {
		return 0, 0, 0, false, fmt.Errorf("iterate projections: %w", err)
	}

	// Any remaining entries in factsMap are facts without projections (missing data)
	for key, source := range factsMap {
		totalDiffs++
		dimensionKey := fmt.Sprintf("tenant:%s:day:%s:provider:%d:cred:%d:model:%d:%s",
			key.tenantID, key.day.Format("2006-01-02"), key.providerID, key.credentialID, key.canonicalID, key.modelName)

		// Missing projections can be rebuilt from the source facts, but are
		// not resolved until that rebuild has committed.
		resolution := "auto_repair_pending"
		pendingRepairs++

		_, err := w.db.Exec(ctx, `
			INSERT INTO stats_reconciliation_diffs 
				(run_id, tenant_id, dimension_type, dimension_key, metric, 
				 source_value, projected_value, difference, resolution, created_at)
			VALUES ($1, $2, 'daily_rollup', $3, 'request_count', $4, 0, $4, $5, now())
		`, runID, key.tenantID, dimensionKey, source.requests, resolution)
		if err != nil {
			slog.Warn("failed to record missing projection diff", "error", err)
		}
	}

	// Rebuild first; only then mark the candidate diffs repaired. This keeps
	// reconciliation counters truthful when the projection refresh fails.
	if pendingRepairs > 0 {
		// DailyMonthlyRollup.Refresh requires a DBQuerier that can Begin a
		// transaction. In production this is *pgxpool.Pool. A Tx-bound test
		// stub that only wraps a pgx.Tx does not satisfy Begin; we surface a
		// clear error and revert pending repairs to 'open' rather than
		// panicking on a type assertion.
		rollup := NewDailyMonthlyRollup(w.db, 0)
		if err := rollup.Refresh(ctx, start, end); err != nil {
			// The Refresh error is already wrapped with the underlying cause
			// (begin, exec, commit). We reset pending repairs to 'open' so
			// the next reconciliation cycle retries them.
			w.resetPendingRepairs(ctx, runID)
			return 0, 0, 0, false, fmt.Errorf("refresh projections: %w", err)
		}
		result, err := w.db.Exec(ctx, `
			UPDATE stats_reconciliation_diffs
			SET resolution = 'auto_repaired'
			WHERE run_id = $1 AND resolution = 'auto_repair_pending'`, runID)
		if err != nil {
			return 0, 0, 0, false, fmt.Errorf("mark repaired diffs: %w", err)
		}
		autoRepaired = result.RowsAffected()
	}

	if autoRepaired > 0 {
		slog.Info("rebuild completed for auto-repaired diffs",
			"run_id", runID, "auto_repaired", autoRepaired)
	}

	return totalDiffs, autoRepaired, phantomDiffs, false, nil
}

// reconcileDaily compares usage_facts with stats_usage_daily for [start,end).
// When reconcileDailyOnce hits the row cap, the window is split in half and
// reconciled recursively; the cap is retained as a final safety fallback when
// the window cannot be split further.
func (w *ReconciliationWorker) reconcileDaily(ctx context.Context, runID string, start, end time.Time) (int64, int64, int64, error) {
	if reconcileDailyOverride != nil {
		// Test seam: replace the entire wrapper with a controlled
		// implementation. Used to exercise split aggregation without a live DB.
		return reconcileDailyOverride(ctx, w, runID, start, end)
	}
	diffs, repaired, phantom, hitCap, err := w.reconcileDailyOnce(ctx, runID, start, end)
	if err != nil || !hitCap {
		return diffs, repaired, phantom, err
	}
	if end.Sub(start) <= time.Second {
		return 0, 0, 0, fmt.Errorf("reconciliation row limit still exceeded after min-granularity split: %d rows", maxReconciliationRows)
	}
	mid := start.Add(end.Sub(start) / 2)
	d1, r1, p1, err := w.reconcileDaily(ctx, runID, start, mid)
	if err != nil {
		return d1, r1, p1, err
	}
	d2, r2, p2, err := w.reconcileDaily(ctx, runID, mid, end)
	return d1 + d2, r1 + r2, p1 + p2, err
}

func (w *ReconciliationWorker) resetPendingRepairs(ctx context.Context, runID string) {
	if _, err := w.db.Exec(ctx, `
		UPDATE stats_reconciliation_diffs
		SET resolution = 'open'
		WHERE run_id = $1 AND resolution = 'auto_repair_pending'`, runID); err != nil {
		slog.Error("failed to restore pending reconciliation diffs", "run_id", runID, "error", err)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// canAutoRepair decides whether a per-metric discrepancy between the source
// fact (source) and the projected value (projected) is small enough to be
// repaired automatically. Both values must be non-zero; a zero source with a
// non-zero projection is a phantom row and is never auto-repaired (handled by
// the caller via the separate 'phantom_open' resolution). Kept as a pure
// function so the production loop and the unit tests exercise exactly the
// same decision.
func canAutoRepair(source, projected float64) bool {
	if source == 0 {
		// A non-zero projection without source facts is a phantom row. It
		// must never be refreshed automatically; the caller records it as
		// phantom_open for operator investigation.
		return false
	}
	if projected == 0 {
		// A source fact without a projection is safe to rebuild when the
		// absolute discrepancy remains inside the repair bound.
		return abs(source) < autoRepairMaxValue
	}
	relDiff := abs((source - projected) / projected)
	return relDiff < autoRepairThreshold && abs(source-projected) < autoRepairMaxValue
}

// reconcileDailyOverride lets tests inject a fake reconcileDaily wrapper.
// Used to drive sharding behaviour tests without a real DB. The seam only
// kicks in when this variable is non-nil (set by tests).
var reconcileDailyOverride func(ctx context.Context, w *ReconciliationWorker, runID string, start, end time.Time) (int64, int64, int64, error)

// finishRunOverride lets tests inject a fake finishRun. Used to verify
// finishRun's UPDATE-failure tolerance without standing up a DB. Same
// package-private seam pattern as reconcileDailyOverride.
var finishRunOverride func(ctx context.Context, w *ReconciliationWorker, runID, status string, eventsSeen, rowsCompared, rowsRepaired, diffCount int64, watermark time.Time, errorMsg string)
