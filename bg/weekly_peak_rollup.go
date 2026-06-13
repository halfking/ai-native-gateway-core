package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WeeklyPeakRollup aggregates the previous week's peak data from
// credential_model_peak_1m into credential_model_weekly_peak.
// Runs daily at 00:05 UTC.
type WeeklyPeakRollup struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}
}

// NewWeeklyPeakRollup creates a new rollup worker.
func NewWeeklyPeakRollup(db *pgxpool.Pool) *WeeklyPeakRollup {
	return &WeeklyPeakRollup{db: db, done: make(chan struct{})}
}

// Start spawns the background cron-style goroutine.
func (w *WeeklyPeakRollup) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(cctx)
	slog.Info("weekly peak rollup started", "schedule", "00:05 UTC daily")
}

// Stop terminates the goroutine and waits for it.
func (w *WeeklyPeakRollup) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

func (w *WeeklyPeakRollup) run(ctx context.Context) {
	defer close(w.done)
	for {
		now := time.Now().UTC()
		next := time.Date(now.Year(), now.Month(), now.Day(), 0, 5, 0, 0, time.UTC)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
			w.rollup(ctx)
		}
	}
}

// rollup aggregates the previous Monday-to-Monday window into the
// credential_model_weekly_peak table.
func (w *WeeklyPeakRollup) rollup(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	now := time.Now().UTC()
	thisMonday := now.Truncate(24 * time.Hour)
	for thisMonday.Weekday() != time.Monday {
		thisMonday = thisMonday.Add(-24 * time.Hour)
	}
	lastMonday := thisMonday.Add(-7 * 24 * time.Hour)

	// Aggregate the previous week. We compute two peaks:
	//   1. peak_concurrent: max of 1-minute peak buckets (legacy, kept
	//      for backward compatibility with downstream dashboards).
	//   2. peak_concurrent_5min: max of any 5-minute rolling window
	//      (per (credential_id, raw_model)). This is the value that
	//      the slot-suggester and operator UIs use for "max concurrency
	//      in a 5-minute window" per the user spec.
	//
	// The current_limit comes from credentials.concurrency_limit (the
	// per-credential cap), NOT from routing_policy (which is a
	// singleton config row without credential_id).
	tag, err := w.db.Exec(timeoutCtx, `
		WITH
		-- 5-minute rolling peak per (credential, model): for each 1m
		-- bucket, take the max of the current row + the four preceding
		-- buckets. Then aggregate the window-max across the week.
		window_5m AS (
		    SELECT
		        p.credential_id,
		        p.raw_model,
		        MAX(window_peak) AS peak_concurrent_5min
		    FROM (
		        SELECT
		            credential_id,
		            raw_model,
		            bucket,
		            MAX(peak_concurrent) OVER (
		                PARTITION BY credential_id, raw_model
		                ORDER BY bucket
		                ROWS BETWEEN 4 PRECEDING AND CURRENT ROW
		            ) AS window_peak
		        FROM credential_model_peak_1m
		        WHERE bucket >= $1::timestamptz
		          AND bucket <  $2::timestamptz
		    ) p
		    GROUP BY p.credential_id, p.raw_model
		)
		INSERT INTO credential_model_weekly_peak (
			week_start, credential_id, raw_model,
			peak_concurrent, peak_concurrent_5min, p95_concurrent, avg_concurrent,
			total_requests, sample_days, current_limit,
			updated_at
		)
		SELECT
			$1::timestamptz AS week_start,
			p.credential_id,
			p.raw_model,
			COALESCE(MAX(p.peak_concurrent), 0)              AS peak_concurrent,
			COALESCE(w.peak_concurrent_5min, 0)             AS peak_concurrent_5min,
			COALESCE(
				PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY p.peak_concurrent),
				0
			)::numeric(8,2)                                  AS p95_concurrent,
			COALESCE(AVG(p.avg_concurrent), 0)::numeric(8,2) AS avg_concurrent,
			COALESCE(SUM(p.sample_count), 0)::bigint         AS total_requests,
			COUNT(DISTINCT date_trunc('day', p.bucket))::int AS sample_days,
			COALESCE(c.concurrency_limit, 0)::int            AS current_limit,
			NOW() AS updated_at
		FROM credential_model_peak_1m p
		LEFT JOIN window_5m w
		    ON w.credential_id = p.credential_id
		   AND w.raw_model     = p.raw_model
		LEFT JOIN credentials c
		    ON c.id = p.credential_id
		WHERE p.bucket >= $1::timestamptz
		  AND p.bucket <  $2::timestamptz
		GROUP BY p.credential_id, p.raw_model, w.peak_concurrent_5min, c.concurrency_limit
		ON CONFLICT (week_start, credential_id, raw_model) DO UPDATE SET
			peak_concurrent     = GREATEST(credential_model_weekly_peak.peak_concurrent, EXCLUDED.peak_concurrent),
			peak_concurrent_5min = GREATEST(
				COALESCE(credential_model_weekly_peak.peak_concurrent_5min, 0),
				COALESCE(EXCLUDED.peak_concurrent_5min, 0)
			),
			p95_concurrent      = EXCLUDED.p95_concurrent,
			avg_concurrent      = EXCLUDED.avg_concurrent,
			total_requests      = EXCLUDED.total_requests,
			sample_days         = EXCLUDED.sample_days,
			current_limit       = EXCLUDED.current_limit,
			updated_at          = NOW()
	`, lastMonday, thisMonday)

	if err != nil {
		slog.Error("weekly peak rollup failed", "error", err,
			"week_start", lastMonday.Format("2006-01-02"))
		return
	}
	slog.Info("weekly peak rollup completed",
		"week_start", lastMonday.Format("2006-01-02"),
		"rows_affected", tag.RowsAffected(),
	)
}
