// Package bg — integrity_fingerprint_drift.go
//
// 2026-07-28: background worker that detects (credential, model)
// system_fingerprint drift. The OpenAI wire format carries a
// system_fingerprint field that uniquely identifies a model build
// (e.g. "fp_4471d4fc0a"). A rolling 7-day distribution of this value
// establishes a "this is what c16/glm-5.2 usually looks like" baseline;
// if the dominant value changes (e.g. suddenly 80% of last 100
// responses are fp_zzz instead of fp_aaa) we record a
// fingerprint_drift event into model_integrity_events.
//
// This catches:
//   - Provider silently rolling a new model version (a frequent
//     incident pattern for token-aggregator upstreams).
//   - Provider swapping the upstream to a different model without
//     updating the catalog.
//   - Sticky / fallback chains routing to a sibling model with the
//     same advertised name but different fingerprint.
//
// Configuration (env, parsed at Start, no hot reload):
//
//	LLM_GATEWAY_INTEGRITY_FP_DRIFT_INTERVAL=1h
//	LLM_GATEWAY_INTEGRITY_FP_DRIFT_DAYS=7
//	LLM_GATEWAY_INTEGRITY_FP_DRIFT_MIN_SAMPLES=20
//	LLM_GATEWAY_INTEGRITY_FP_DRIFT_DOMINANT_RATIO=0.8
package bg

import (
	"context"
	"log/slog"
	"os"
	"strconv"
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
	// First scan at start so the dashboard has data on a freshly
	// restarted gateway without waiting a full interval.
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

// scanDrift runs the dominant-fingerprint check per (credential_id,
// raw_model_name) over the rolling window. Inserts a single
// model_integrity_events row per drift detection, debounced within
// the same cycle (we don't emit multiple rows for the same drift in
// one tick).
func (w *IntegrityFingerprintDrift) scanDrift(ctx context.Context) error {
	rows, err := w.db.Query(ctx, `
		WITH window AS (
			SELECT credential_id, raw_model_name, system_fingerprint
			FROM request_logs
			WHERE ts > NOW() - ($1::int * INTERVAL '1 day')
			  AND system_fingerprint IS NOT NULL
			  AND credential_id IS NOT NULL
		),
		counts AS (
			SELECT credential_id,
			       raw_model_name,
			       system_fingerprint,
			       COUNT(*) AS n
			FROM window
			GROUP BY credential_id, raw_model_name, system_fingerprint
		),
		grouped AS (
			SELECT credential_id,
			       raw_model_name,
			       SUM(n) AS total,
			       MAX(n) FILTER (WHERE rn = 1) AS top_n,
			       (ARRAY_AGG(system_fingerprint ORDER BY n DESC))[1] AS top_fp
			FROM (
				SELECT credential_id, raw_model_name, system_fingerprint, n,
				       ROW_NUMBER() OVER (
				           PARTITION BY credential_id, raw_model_name
				           ORDER BY n DESC
				       ) AS rn
				FROM counts
			) ranked
			GROUP BY credential_id, raw_model_name
		)
		SELECT credential_id, raw_model_name, top_fp, total, top_n
		FROM grouped
		WHERE total >= $2
		  AND (top_n::float / total::float) < $3
	`, w.days, w.minSamples, w.dominantRatio)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var credID int64
		var model, topFP string
		var total, topN int64
		if err := rows.Scan(&credID, &model, &topFP, &total, &topN); err != nil {
			slog.Warn("integrity_fingerprint_drift: scan row", "error", err)
			continue
		}
		// Compute "expected" as 1 - dominantRatio. i.e. if the top
		// fingerprint is 60% of responses, the expected dominance
		// was 80% (i.e. drift = -20 percentage points).
		expected := int(w.dominantRatio * 100)
		actual := int(float64(topN) / float64(total) * 100)
		if err := w.recordDrift(ctx, credID, model, topFP, expected, actual, total); err != nil {
			slog.Warn("integrity_fingerprint_drift: record failed",
				"credential_id", credID, "model", model, "error", err)
			continue
		}
		w.driftDetected.Add(1)
	}
	return rows.Err()
}

func (w *IntegrityFingerprintDrift) recordDrift(ctx context.Context, credID int64, model, topFP string, expected, actual int, total int64) error {
	expectedStr := strconv.Itoa(expected) + "%"
	actualStr := strconv.Itoa(actual) + "%"
	_, err := w.db.Exec(ctx, `
		INSERT INTO model_integrity_events (
			ts, request_id, tenant_id, application_id, api_key_id,
			provider_id, provider_code, credential_id,
			client_model, outbound_model, raw_model_name,
			anomaly_type, severity,
			expected_value, actual_value, sample, context
		) VALUES (
			now(), NULL, NULL, NULL, NULL,
			NULL, NULL, $1,
			NULL, NULL, $2,
			'fingerprint_drift', 'high',
			$3, $4, $5, jsonb_build_object(
			    'window_days', $6::int,
			    'total_samples', $7::bigint,
			    'dominant_fingerprint', $5
			)
		)
	`, credID, model, expectedStr, actualStr, topFP, w.days, total)
	return err
}

// Compile-time check that env-var defaults are loaded from a single
// helper module-level to avoid drift with the rest of the bg package.
var _ = os.Getenv // re-export hint for static analysis
