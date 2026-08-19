package stats

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// ReconciliationWorker periodically compares usage_facts (source of truth) with
// stats_usage_daily/monthly projections, generates diffs, and automatically
// repairs small discrepancies while flagging large ones for human approval.
type ReconciliationWorker struct {
	db       *pgxpool.Pool
	interval time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

func NewReconciliationWorker(db *pgxpool.Pool, interval time.Duration) *ReconciliationWorker {
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
			w.reconcileRecent(ctx)
		}
	}
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

	// Reconcile daily projections by comparing aggregated facts vs projections
	diffs, repaired, err := w.reconcileDaily(ctx, runID, start, end)
	if err != nil {
		w.finishRun(ctx, runID, "failed", eventsSeen, rowsCompared, rowsRepaired, diffCount, sourceWatermark, err.Error())
		metrics.RecordStatsReconciliationRun("failed", 1)
		return fmt.Errorf("reconcile daily: %w", err)
	}

	rowsCompared = diffs
	rowsRepaired = repaired
	diffCount = diffs - repaired

	w.finishRun(ctx, runID, "completed", eventsSeen, rowsCompared, rowsRepaired, diffCount, sourceWatermark, "")
	metrics.RecordStatsReconciliationRun("completed", 1)
	metrics.ObserveStatsReconciliationDiffs("open", diffCount)
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

// reconcileDaily compares usage_facts aggregation with stats_usage_daily projections.
// Returns (total_diffs, auto_repaired, error).
func (w *ReconciliationWorker) reconcileDaily(ctx context.Context, runID string, start, end time.Time) (int64, int64, error) {
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
		return 0, 0, fmt.Errorf("query usage_facts: %w", err)
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

	rowCount := 0
	for factRows.Next() {
		if rowCount >= maxReconciliationRows {
			return 0, 0, fmt.Errorf("reconciliation row limit exceeded: %d rows", maxReconciliationRows)
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
			return 0, 0, fmt.Errorf("scan fact row: %w", err)
		}
		factsMap[key] = metrics
		rowCount++
	}

	if err := factRows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterate facts: %w", err)
	}

	// Compare with projections
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
	`, start, end)
	if err != nil {
		return 0, 0, fmt.Errorf("query projections: %w", err)
	}
	defer projRows.Close()

	var totalDiffs, autoRepaired int64
	var pendingRepairs int64

	projRowCount := 0
	for projRows.Next() {
		if projRowCount >= maxReconciliationRows {
			return 0, 0, fmt.Errorf("projection row limit exceeded: %d rows", maxReconciliationRows)
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
			return 0, 0, fmt.Errorf("scan projection row: %w", err)
		}

		source, exists := factsMap[key]
		if !exists {
			// Projection exists but no source facts - this is a phantom row
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
			totalDiffs++

			dimensionKey := fmt.Sprintf("provider:%d:cred:%d:model:%d:%s",
				key.providerID, key.credentialID, key.canonicalID, key.modelName)

			// Determine if auto-repairable: small relative diff or small absolute value
			canAutoRepair := false
			if d.projected != 0 {
				relDiff := abs(diff / d.projected)
				if relDiff < autoRepairThreshold && abs(diff) < autoRepairMaxValue {
					canAutoRepair = true
				}
			} else if abs(diff) < autoRepairMaxValue {
				canAutoRepair = true
			}

			resolution := "open"
			if canAutoRepair {
				resolution = "auto_repair_pending"
				pendingRepairs++
			}

			// Record diff
			_, err := w.db.Exec(ctx, `
				INSERT INTO stats_reconciliation_diffs 
					(run_id, tenant_id, dimension_type, dimension_key, metric, 
					 source_value, projected_value, difference, resolution, created_at)
				VALUES ($1, $2, 'daily_rollup', $3, $4, $5, $6, $7, $8, now())
			`, runID, key.tenantID, dimensionKey, d.metric,
				d.source, d.projected, diff, resolution)
			if err != nil {
				slog.Warn("failed to record diff", "error", err, "run_id", runID)
			}
		}

		// Remove from map to track missing projections
		delete(factsMap, key)
		projRowCount++
	}

	if err := projRows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterate projections: %w", err)
	}

	// Any remaining entries in factsMap are facts without projections (missing data)
	for key, source := range factsMap {
		totalDiffs++
		dimensionKey := fmt.Sprintf("provider:%d:cred:%d:model:%d:%s",
			key.providerID, key.credentialID, key.canonicalID, key.modelName)

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
		rollup := NewDailyMonthlyRollup(w.db, 0)
		if err := rollup.Refresh(ctx, start, end); err != nil {
			w.resetPendingRepairs(ctx, runID)
			return 0, 0, fmt.Errorf("refresh projections: %w", err)
		}
		result, err := w.db.Exec(ctx, `
			UPDATE stats_reconciliation_diffs
			SET resolution = 'auto_repaired'
			WHERE run_id = $1 AND resolution = 'auto_repair_pending'`, runID)
		if err != nil {
			return 0, 0, fmt.Errorf("mark repaired diffs: %w", err)
		}
		autoRepaired = result.RowsAffected()
	}

	if autoRepaired > 0 {
		slog.Info("rebuild completed for auto-repaired diffs",
			"run_id", runID, "auto_repaired", autoRepaired)
	}

	return totalDiffs, autoRepaired, nil
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
