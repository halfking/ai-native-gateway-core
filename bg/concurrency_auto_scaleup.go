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
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("concurrency_auto_scaleup started", "interval", w.interval)

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
}

// Stop gracefully stops the worker.
func (w *ConcurrencyAutoScaleUp) Stop() {
	close(w.stopCh)
}

// scaleUp finds credentials that are healthy + heavily loaded and increases their limit by 1.
func (w *ConcurrencyAutoScaleUp) scaleUp(ctx context.Context) error {
	// Query: recent 1h stats, no 429/503, success rate >95%, avg concurrent >80% limit
	rows, err := w.db.Query(ctx, `
		WITH recent_stats AS (
			SELECT 
				credential_id,
				raw_model,
				COUNT(*) FILTER (WHERE error_rate_limit_count > 0 OR error_concurrent_count > 0) AS bad_windows,
				SUM(success_calls)::float / NULLIF(SUM(total_calls), 0) AS success_rate,
				AVG(avg_concurrent) AS avg_conc
			FROM credential_model_call_history
			WHERE window_start > now() - interval '1 hour'
			GROUP BY credential_id, raw_model
		)
		SELECT 
			c.id,
			rs.raw_model,
			c.concurrency_limit_auto,
			rs.avg_conc,
			rs.success_rate
		FROM credentials c
		JOIN recent_stats rs ON rs.credential_id = c.id
		WHERE rs.bad_windows = 0                                -- no 429/503 in past hour
		  AND rs.success_rate >= 0.95                           -- >95% success
		  AND rs.avg_conc >= COALESCE(c.concurrency_limit_auto, 5) * 0.8  -- load >80%
		  AND COALESCE(c.concurrency_limit_auto, 5) < 50        -- below max
		ORDER BY rs.avg_conc DESC
		LIMIT 20
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var credID int
		var model string
		var currentLimit int
		var avgConc, successRate float64

		if err := rows.Scan(&credID, &model, &currentLimit, &avgConc, &successRate); err != nil {
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

		count++
	}

	if count > 0 {
		slog.Info("concurrency_auto_scaleup: scaled up", "count", count)
	}

	return nil
}
