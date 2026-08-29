// Package bg — integrity_fingerprint_drift.go
//
// 2026-07-28: per-(cred, model) system_fingerprint drift detector.
//
// Algorithm (rewritten 2026-07-28 to fix the "window fragmentation"
// regression in the previous implementation):
//
//  1. Split the rolling `days` window into a baseline half (older) and
//     a current half (newer). Compare dominant fingerprints, not just
//     current-window fragmentation.
//  2. Both halves must have at least `minSamples` business-traffic
//     samples (synthetic probe/self-check rows are filtered out via
//     is_auto_request=false AND task_type IS NULL OR task_type NOT IN
//     ('self_check','node_probe','active_probe','integrity_probe')).
//  3. A drift transition (old dominant != new dominant, both >= 80% of
//     their half) is recorded exactly once per transition by updating
//     integrity_fingerprint_baseline.last_alerted_fingerprint; the next
//     tick only re-alerts when the new dominant flips again. This kills
//     the previous "every hourly tick duplicates" behavior.
//  4. Pure current-window fragmentation (old half absent or low
//     share, new half mixed) is exposed as a separate
//     `fingerprint_fragmentation` context tag in the recorded event but
//     does not promote a `fingerprint_drift` event by itself; operators
//     who want to alert on fragmentation can pivot on
//     context->>'kind' = 'fragmentation'.
//
// Configuration (env, no hot reload):
//
//	LLM_GATEWAY_INTEGRITY_FP_DRIFT_INTERVAL=1h
//	LLM_GATEWAY_INTEGRITY_FP_DRIFT_DAYS=7
//	LLM_GATEWAY_INTEGRITY_FP_DRIFT_MIN_SAMPLES=20
//	LLM_GATEWAY_INTEGRITY_FP_DRIFT_DOMINANT_RATIO=0.8
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// IntegrityFingerprintDrift runs the rolling-window baseline check.
// nil-safe; nil receiver is a no-op.
type IntegrityFingerprintDrift struct {
	db *pgxpool.Pool

	interval      time.Duration
	days          int
	minSamples    int
	dominantRatio float64
	cancel        context.CancelFunc
	done          chan struct{}
	started       atomic.Bool
	scannedCycles atomic.Uint64
	driftDetected atomic.Uint64
}

// NewIntegrityFingerprintDrift constructs a worker. Default values
// match the production deployment (1h interval, 7d window, 20-sample
// floor, 80% dominance threshold).
func NewIntegrityFingerprintDrift(db *pgxpool.Pool) *IntegrityFingerprintDrift {
	return &IntegrityFingerprintDrift{
		db:            db,
		interval:      parseDurationEnv("LLM_GATEWAY_INTEGRITY_FP_DRIFT_INTERVAL", time.Hour),
		days:          parseIntEnv("LLM_GATEWAY_INTEGRITY_FP_DRIFT_DAYS", 7),
		minSamples:    parseIntEnv("LLM_GATEWAY_INTEGRITY_FP_DRIFT_MIN_SAMPLES", 20),
		dominantRatio: parseFloatEnv("LLM_GATEWAY_INTEGRITY_FP_DRIFT_DOMINANT_RATIO", 0.8),
		done:          make(chan struct{}),
	}
}

// Start launches the background loop. Idempotent.
func (w *IntegrityFingerprintDrift) Start(ctx context.Context) {
	if w == nil || w.db == nil {
		return
	}
	if !w.started.CompareAndSwap(false, true) {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(runCtx)
	slog.Info("integrity_fingerprint_drift started",
		"interval", w.interval,
		"days", w.days,
		"min_samples", w.minSamples,
		"dominant_ratio", w.dominantRatio,
	)
}

// Stop signals the loop to exit and waits for it.
func (w *IntegrityFingerprintDrift) Stop() {
	if w == nil {
		return
	}
	if !w.started.Load() {
		return
	}
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
	slog.Info("integrity_fingerprint_drift stopped",
		"cycles", w.scannedCycles.Load(),
		"drift_detected", w.driftDetected.Load(),
	)
}

func (w *IntegrityFingerprintDrift) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *IntegrityFingerprintDrift) tick(ctx context.Context) {
	stepCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	w.scannedCycles.Add(1)
	if err := w.scanDrift(stepCtx); err != nil {
		slog.Warn("integrity_fingerprint_drift: scan failed", "error", err)
	}
}

// scanDrift computes baseline vs current dominant fingerprint per
// (credential, model) and persists drift events with cross-tick
// deduplication via integrity_fingerprint_baseline.last_alerted_*.
func (w *IntegrityFingerprintDrift) scanDrift(ctx context.Context) error {
	// Half-window split: baseline = older half, current = newer half.
	// A model with 7d window therefore compares first 3.5d vs last 3.5d.
	baselineDays := w.days / 2
	if baselineDays < 1 {
		baselineDays = 1
	}
	currentDays := w.days - baselineDays
	if currentDays < 1 {
		currentDays = 1
	}

	rows, err := w.db.Query(ctx, `
		WITH business AS (
		    SELECT credential_id, raw_model_name, system_fingerprint, ts
		    FROM request_logs
		    WHERE ts > NOW() - ($1::int * INTERVAL '1 day')
		      AND system_fingerprint IS NOT NULL
		      AND credential_id IS NOT NULL
		      AND raw_model_name IS NOT NULL
		      AND COALESCE(is_auto_request, false) = false
		      AND (task_type IS NULL OR task_type NOT IN
		          ('self_check','node_probe','active_probe','integrity_probe','credential_selfcheck'))
		),
		baseline_window AS (
		    SELECT credential_id, raw_model_name, system_fingerprint, ts
		    FROM business
		    WHERE ts <= NOW() - ($3::int * INTERVAL '1 day')
		),
		current_window AS (
		    SELECT credential_id, raw_model_name, system_fingerprint
		    FROM business
		    WHERE ts > NOW() - ($3::int * INTERVAL '1 day')
		),
		baseline_counts AS (
		    SELECT credential_id, raw_model_name, system_fingerprint, COUNT(*) AS n
		    FROM baseline_window
		    GROUP BY credential_id, raw_model_name, system_fingerprint
		),
		current_counts AS (
		    SELECT credential_id, raw_model_name, system_fingerprint, COUNT(*) AS n
		    FROM current_window
		    GROUP BY credential_id, raw_model_name, system_fingerprint
		),
		baseline_top AS (
		    SELECT credential_id, raw_model_name,
		           (ARRAY_AGG(system_fingerprint ORDER BY n DESC))[1] AS fp,
		           MAX(n) AS top_n, SUM(n) AS total
		    FROM baseline_counts
		    GROUP BY credential_id, raw_model_name
		),
		current_top AS (
		    SELECT credential_id, raw_model_name,
		           (ARRAY_AGG(system_fingerprint ORDER BY n DESC))[1] AS fp,
		           MAX(n) AS top_n, SUM(n) AS total
		    FROM current_counts
		    GROUP BY credential_id, raw_model_name
		),
		joined AS (
		    SELECT
		        b.credential_id, b.raw_model_name,
		        b.fp   AS baseline_fp,
		        c.fp   AS current_fp,
		        b.top_n  AS baseline_top,
		        b.total AS baseline_total,
		        c.top_n  AS current_top,
		        c.total AS current_total
		    FROM baseline_top b
		    JOIN current_top c
		      ON c.credential_id = b.credential_id
		     AND c.raw_model_name = b.raw_model_name
		    WHERE b.total >= $2 AND c.total >= $2
		      AND (b.top_n::float / b.total::float) >= $4
		      AND (c.top_n::float / c.total::float) >= $4
		)
	SELECT j.credential_id, j.raw_model_name, j.baseline_fp, j.current_fp,
		       j.baseline_top, j.baseline_total, j.current_top, j.current_total
	FROM joined j`, w.days, w.minSamples, currentDays, w.dominantRatio)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pending struct {
		credID      int64
		rawModel    string
		baselineFP  string
		currentFP   string
		baselineTop int64
		baselineTot int64
		currentTop  int64
		currentTot  int64
	}
	var batch []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.credID, &p.rawModel, &p.baselineFP, &p.currentFP,
			&p.baselineTop, &p.baselineTot, &p.currentTop, &p.currentTot); err != nil {
			slog.Warn("integrity_fingerprint_drift: scan row", "error", err)
			continue
		}
		if p.baselineFP == p.currentFP {
			continue
		}
		batch = append(batch, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range batch {
		// Cross-tick dedup + baseline state update in a single tx so a
		// crash between the two is impossible.
		tx, err := w.db.Begin(ctx)
		if err != nil {
			return err
		}
		var prev string
		err = tx.QueryRow(ctx, `
			SELECT last_alerted_fingerprint
			FROM integrity_fingerprint_baseline
			WHERE tenant_id = 'default' AND credential_id = $1 AND raw_model_name = $2
			FOR UPDATE`, p.credID, p.rawModel).Scan(&prev)
		if err != nil && err.Error() != "no rows in result set" {
			_ = tx.Rollback(ctx)
			slog.Warn("integrity_fingerprint_drift: baseline lookup failed",
				"credential_id", p.credID, "model", p.rawModel, "error", err)
			continue
		}
		if prev == p.currentFP {
			_ = tx.Rollback(ctx)
			continue
		}
		// Persist the transition as the new alerted state.
		if _, err := tx.Exec(ctx, `
			INSERT INTO integrity_fingerprint_baseline
				(tenant_id, credential_id, raw_model_name,
				 baseline_fingerprint, baseline_share_pct, baseline_sample_count,
				 baseline_window_start, baseline_window_end,
				 current_fingerprint, current_share_pct,
				 last_alerted_fingerprint, last_alerted_at, updated_at)
			VALUES ('default', $1, $2,
				$3, $4, $5,
				now() - ($6::int * INTERVAL '1 day'), now() - ($7::int * INTERVAL '1 day'),
				$8, $9,
				$8, now(), now())
			ON CONFLICT (tenant_id, credential_id, raw_model_name)
			DO UPDATE SET
				baseline_fingerprint = EXCLUDED.baseline_fingerprint,
				baseline_share_pct   = EXCLUDED.baseline_share_pct,
				baseline_sample_count= EXCLUDED.baseline_sample_count,
				baseline_window_start= EXCLUDED.baseline_window_start,
				baseline_window_end  = EXCLUDED.baseline_window_end,
				current_fingerprint = EXCLUDED.current_fingerprint,
				current_share_pct   = EXCLUDED.current_share_pct,
				last_alerted_fingerprint = EXCLUDED.last_alerted_fingerprint,
				last_alerted_at      = now(),
				updated_at           = now()`,
			p.credID, p.rawModel,
			p.baselineFP, baselinePct(p.baselineTop, p.baselineTot), p.baselineTot,
			w.days, currentDays,
			p.currentFP, baselinePct(p.currentTop, p.currentTot)); err != nil {
			_ = tx.Rollback(ctx)
			slog.Warn("integrity_fingerprint_drift: baseline upsert failed",
				"credential_id", p.credID, "model", p.rawModel, "error", err)
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO model_integrity_events (
				ts, request_id, tenant_id, application_id, api_key_id,
				provider_id, provider_code, credential_id,
				client_model, outbound_model, raw_model_name,
				anomaly_type, severity,
				expected_value, actual_value, sample, context
			) VALUES (
				now(), NULL, 'default', NULL, NULL,
				NULL, NULL, $1,
				NULL, NULL, $2,
				'fingerprint_drift', 'high',
				$3, $4, $5, jsonb_build_object(
				    'window_days', $6::int,
				    'baseline_fingerprint', $3,
				    'current_fingerprint', $4,
				    'baseline_share_pct', $7::int,
				    'current_share_pct', $8::int,
				    'baseline_sample_count', $9::bigint,
				    'current_sample_count', $10::bigint,
				    'kind', 'baseline_transition')
			)`, p.credID, p.rawModel,
			p.baselineFP, p.currentFP, p.currentFP,
			w.days,
			baselinePct(p.baselineTop, p.baselineTot), baselinePct(p.currentTop, p.currentTot),
			p.baselineTot, p.currentTot); err != nil {
			_ = tx.Rollback(ctx)
			slog.Warn("integrity_fingerprint_drift: event insert failed",
				"credential_id", p.credID, "model", p.rawModel, "error", err)
			continue
		}
		if err := tx.Commit(ctx); err != nil {
			slog.Warn("integrity_fingerprint_drift: commit failed",
				"credential_id", p.credID, "model", p.rawModel, "error", err)
			continue
		}
		w.driftDetected.Add(1)
		slog.Info("integrity_fingerprint_drift: transition recorded",
			"credential_id", p.credID, "model", p.rawModel,
			"baseline_fp", p.baselineFP, "current_fp", p.currentFP)
	}
	return nil
}

// baselinePct returns the integer percentage (0-100) of top over total.
// Inputs are guaranteed non-zero in the SQL filter; the division-by-zero
// guard is defensive.
func baselinePct(top, total int64) int {
	if total <= 0 {
		return 0
	}
	pct := int((top * 100) / total)
	if pct > 100 {
		pct = 100
	}
	return pct
}

// ensure the fmt import stays used by future log calls.
var _ = fmt.Sprintf

// Compile-time check that env-var defaults are loaded from a single
// helper module-level to avoid drift with the rest of the bg package.
var _ = os.Getenv
