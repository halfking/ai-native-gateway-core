package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialhealth"
)

// ConcurrencyAutoScaleUp checks credentials with high success rate + high load
// and incrementally increases their concurrency_limit_auto.
type ConcurrencyAutoScaleUp struct {
	tuner    *credentialhealth.Tuner
	db       credentialhealth.DBQuerier
	interval time.Duration // default 1 hour
	stopCh   chan struct{}
}

// NewConcurrencyAutoScaleUp creates a scaleup worker.
func NewConcurrencyAutoScaleUp(
	db credentialhealth.DBQuerier,
	interval time.Duration,
) *ConcurrencyAutoScaleUp {
	if interval == 0 {
		interval = 1 * time.Hour
	}

	tuner := credentialhealth.NewTuner(db, credentialhealth.DefaultTunerConfig())

	return &ConcurrencyAutoScaleUp{
		tuner:    tuner,
		db:       db,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the scaleup loop.
func (w *ConcurrencyAutoScaleUp) Start(ctx context.Context) {
	slog.Info("concurrency_auto_scaleup started", "interval", w.interval)

	go func() {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				slog.Info("concurrency_auto_scaleup stopping")
				return
			case <-w.stopCh:
				slog.Info("concurrency_auto_scaleup stopped")
				return
			case <-ticker.C:
				if err := w.scaleUp(ctx); err != nil {
					slog.Error("concurrency_auto_scaleup failed", "error", err)
				}
			}
		}
	}()
}

// Stop gracefully stops the worker.
func (w *ConcurrencyAutoScaleUp) Stop() {
	close(w.stopCh)
}

// scaleUp finds credentials that are healthy + heavily loaded and increases their limit by 1.
//
// P1-5 fix (2026-06-22 audit): the original query gated scaleup on
// AVG(avg_concurrent) >= 0.8 * limit, but the aggregator never populated
// the avg_concurrent column (it's always NULL). NULL >= x is always FALSE
// in SQL, so the worker matched ZERO credentials and silently did nothing
// every hour since deployment.
//
// We now gate on call VOLUME instead of measured concurrency, which the
// aggregator actually records. A credential that handled >= 60 calls in
// the last hour (≈1/min) with ≥95% success and no 429/503 is clearly
// carrying real load and is a safe scaleup candidate. The threshold is
// deliberately conservative; it can be tuned via the constant below.
func (w *ConcurrencyAutoScaleUp) scaleUp(ctx context.Context) error {
	// minCallsInLastHour is the minimum volume to consider a credential
	// "heavily loaded". 60 ≈ 1 request/minute sustained. Below this the
	// credential isn't busy enough to warrant a higher limit.
	const minCallsInLastHour = 60

	rows, err := w.db.Query(ctx, `
		WITH recent_stats AS (
			SELECT
				credential_id,
				raw_model,
				COUNT(*) FILTER (WHERE error_rate_limit_count > 0 OR error_concurrent_count > 0) AS bad_windows,
				SUM(success_calls)::float / NULLIF(SUM(total_calls), 0) AS success_rate,
				SUM(total_calls) AS total_calls
			FROM credential_model_call_history
			WHERE window_start > now() - interval '1 hour'
			GROUP BY credential_id, raw_model
		)
		SELECT
			c.id,
			rs.raw_model,
			COALESCE(c.concurrency_limit_auto, c.concurrency_limit, 5) AS current_limit,
			rs.total_calls,
			rs.success_rate
		FROM credentials c
		JOIN recent_stats rs ON rs.credential_id = c.id
		WHERE rs.bad_windows = 0                                         -- no 429/503 in past hour
		  AND rs.success_rate >= 0.95                                    -- ≥95% success
		  AND rs.total_calls >= $1                                       -- sustained load
		  AND COALESCE(c.concurrency_limit_auto, c.concurrency_limit, 5) < 50  -- below max
		ORDER BY rs.total_calls DESC
		LIMIT 20
	`, minCallsInLastHour)
	if err != nil {
		return err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var credID int
		var model string
		var currentLimit int
		var totalCalls int
		var successRate float64

		if err := rows.Scan(&credID, &model, &currentLimit, &totalCalls, &successRate); err != nil {
			slog.Warn("concurrency_auto_scaleup: scan failed", "error", err)
			continue
		}

		if err := w.tuner.IncreaseConcurrency(ctx, credID, model); err != nil {
			slog.Warn("concurrency_auto_scaleup: increase failed",
				"credential_id", credID,
				"model", model,
				"error", err)
			continue
		}

		slog.Info("concurrency_auto_scaleup: candidate scaled up",
			"credential_id", credID,
			"model", model,
			"old_limit", currentLimit,
			"total_calls_1h", totalCalls,
			"success_rate", successRate)
		count++
	}

	if count > 0 {
		slog.Info("concurrency_auto_scaleup: scaled up", "count", count)
	}

	return nil
}
