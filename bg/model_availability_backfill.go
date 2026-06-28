package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dbExec is the small subset of pgxpool.Pool that the backfill worker
// needs. Defined as an interface so tests can swap in pgxmock without
// casting concrete types.
type dbExec interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// AvailabilityCacheBackfill rebuilds Redis availability entries from
// model_probe_state when the cache has been flushed or lost. It is meant
// to be a safety net for cold start / Redis flush scenarios, NOT a
// replacement for the regular probe workers.
//
// The worker runs periodically, scans model_probe_state rows whose
// next_retry_at has elapsed in the last `lookback`, and re-writes their
// availability into Redis. The scan is bounded so a large DB does not
// stall startup.
type AvailabilityCacheBackfill struct {
	db        dbExec
	cache     *ModelAvailabilityCache
	reader    *ModelAvailabilityReader
	interval  time.Duration
	batchSize int
	lookback  time.Duration

	cancel context.CancelFunc
	done   chan struct{}
}

// AvailabilityCacheBackfillConfig controls backfill cadence and batching.
type AvailabilityCacheBackfillConfig struct {
	Interval  time.Duration // default 5min
	BatchSize int           // default 200
	Lookback  time.Duration // default 1h
}

// NewAvailabilityCacheBackfill constructs the worker. nil is returned if
// db/cache/reader is missing so wiring in main.go becomes a no-op.
func NewAvailabilityCacheBackfill(
	db *pgxpool.Pool,
	cache *ModelAvailabilityCache,
	reader *ModelAvailabilityReader,
	cfg AvailabilityCacheBackfillConfig,
) *AvailabilityCacheBackfill {
	if db == nil || cache == nil || reader == nil {
		return nil
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 200
	}
	if cfg.Lookback <= 0 {
		cfg.Lookback = time.Hour
	}
	return &AvailabilityCacheBackfill{
		db:        db,
		cache:     cache,
		reader:    reader,
		interval:  cfg.Interval,
		batchSize: cfg.BatchSize,
		lookback:  cfg.Lookback,
		done:      make(chan struct{}),
	}
}

// Start launches the periodic backfill loop. Call Stop to terminate.
func (w *AvailabilityCacheBackfill) Start(ctx context.Context) {
	if w == nil {
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	go w.run(ctx)
	slog.Info("availability cache backfill started",
		"interval", w.interval,
		"batch_size", w.batchSize,
		"lookback", w.lookback,
	)
}

// Stop cancels the worker and waits for it to exit.
func (w *AvailabilityCacheBackfill) Stop() {
	if w == nil || w.cancel == nil {
		return
	}
	w.cancel()
	<-w.done
}

// DB returns the underlying DB handle. Exposed so the admin on-demand
// rebuild endpoint can issue a parameterised query without re-creating
// the worker.
func (w *AvailabilityCacheBackfill) DB() dbExec {
	if w == nil {
		return nil
	}
	return w.db
}

// Cache returns the underlying Redis cache writer. Same rationale as
// DB().
func (w *AvailabilityCacheBackfill) Cache() *ModelAvailabilityCache {
	if w == nil {
		return nil
	}
	return w.cache
}

func (w *AvailabilityCacheBackfill) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce executes a single backfill pass. Exposed so admin handlers and
// tests can trigger it on demand.
func (w *AvailabilityCacheBackfill) RunOnce(ctx context.Context) (int, error) {
	if w == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	rows, err := w.db.Query(ctx, `
		SELECT credential_id, raw_model_name, state,
		       COALESCE(consecutive_successes, 0),
		       COALESCE(consecutive_failures, 0),
		       COALESCE(total_attempts, 0),
		       last_attempt_at, next_retry_at, last_status
		FROM model_probe_state
		WHERE next_retry_at IS NOT NULL
		  AND next_retry_at <= NOW() + make_interval(secs => $1)
		ORDER BY next_retry_at DESC
		LIMIT $2
	`, int(w.lookback.Seconds()), w.batchSize)
	if err != nil {
		slog.Warn("availability backfill: query failed", "error", err)
		return 0, err
	}
	defer rows.Close()

	type row struct {
		credID      int
		model       string
		state       string
		succ        int
		fail        int
		total       int
		lastAttempt *time.Time
		nextRetry   *time.Time
		lastStatus  *string
	}
	var entries []row
	for rows.Next() {
		var r row
		var lastAttempt *time.Time
		var nextRetry *time.Time
		var lastStatus *string
		if err := rows.Scan(&r.credID, &r.model, &r.state, &r.succ, &r.fail, &r.total,
			&lastAttempt, &nextRetry, &lastStatus); err != nil {
			continue
		}
		r.lastAttempt = lastAttempt
		r.nextRetry = nextRetry
		r.lastStatus = lastStatus
		entries = append(entries, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Idempotent backfill: only write if the Redis entry is missing or stale.
	// This avoids fighting the regular probe workers.
	written := 0
	for _, e := range entries {
		existing, _ := w.reader.Read(ctx, e.credID, e.model)
		if existing != nil && !shouldRefresh(existing.UpdatedAt, w.lookback) {
			continue
		}
		status := ""
		if e.lastStatus != nil {
			status = *e.lastStatus
		}
		available := !isUnavailable(e.state)
		fields := modelAvailabilityFields(
			e.credID,
			e.model,
			e.state,
			available,
			status,
			e.succ,
			e.fail,
			e.nextRetry,
			"backfill",
		)
		if err := w.cache.Set(ctx, e.credID, e.model, fields); err != nil {
			slog.Warn("availability backfill: cache write failed",
				"credential_id", e.credID,
				"raw_model", e.model,
				"error", err)
			continue
		}
		written++
	}
	if written > 0 {
		slog.Info("availability backfill: cache repopulated", "written", written)
	}
	return written, nil
}

func shouldRefresh(updatedAt *time.Time, lookback time.Duration) bool {
	if updatedAt == nil {
		return true
	}
	return time.Since(*updatedAt) > lookback
}

func isUnavailable(state string) bool {
	switch state {
	case "broken_confirmed", "failing", "unreachable":
		return true
	}
	return false
}
