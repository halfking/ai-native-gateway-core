package bg

import (
	"context"
	"log/slog"
	"sync"
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
// node_probe_state when the cache has been flushed or lost. It is meant
// to be a safety net for cold start / Redis flush scenarios, NOT a
// replacement for the regular probe workers.
//
// Every pass advances a bounded keyset scan of all node states, including
// healthy nodes parked for 30 days. Scheduling a probe and projecting its
// last known result are independent; next_retry_at must not exclude a node.
type AvailabilityCacheBackfill struct {
	db        dbExec
	cache     *ModelAvailabilityCache
	reader    *ModelAvailabilityReader
	interval  time.Duration
	batchSize int
	lookback  time.Duration

	cancel           context.CancelFunc
	done             chan struct{}
	mu               sync.Mutex // serialize manual and periodic scans and protect the cursor
	cursorCredential int
	cursorModel      string
}

// defaultAvailabilityBackfillBatchSize is the batch size used when none is
// configured. A non-positive batch size must read as "unset", never as
// "scan nothing": LIMIT 0 would make the worker a silent no-op that still
// logs "cycle done".
const defaultAvailabilityBackfillBatchSize = 200

// AvailabilityCacheBackfillConfig controls backfill cadence and batching.
type AvailabilityCacheBackfillConfig struct {
	Interval  time.Duration // default 1min
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
		cfg.Interval = time.Minute
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultAvailabilityBackfillBatchSize
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

// effectiveBatchSize is the batch size a scan actually uses.
//
// The batch size reaches this worker as a struct field, so it can still be
// non-positive even though the constructor normalises the config: a worker
// assembled directly would bind LIMIT 0, scan zero rows and still log
// "cycle done" — a silent no-op. Routing the value through this accessor
// keeps the zero case reading as "unset".
func (w *AvailabilityCacheBackfill) effectiveBatchSize() int {
	if w == nil || w.batchSize <= 0 {
		return defaultAvailabilityBackfillBatchSize
	}
	return w.batchSize
}

// Start launches the periodic backfill loop. Call Stop to terminate.
func (w *AvailabilityCacheBackfill) Start(ctx context.Context) {
	if w == nil {
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	Go("model_availability_backfill.run", func() { w.run(ctx) })
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
	_, _ = w.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = w.RunOnce(ctx)
		}
	}
}

// RunOnce executes a single backfill pass. Exposed so admin handlers and
// tests can trigger it on demand. The trigger label flows into the
// llmgw_availability_backfill_*_total Prometheus counters as a label
// so operators can distinguish periodic passes from manual runs.
func (w *AvailabilityCacheBackfill) RunOnce(ctx context.Context) (int, error) {
	return w.runOnceWithTrigger(ctx, "periodic")
}

// RunOnceWithTrigger is the exported variant of RunOnce that records the
// provided trigger label on the llmgw_availability_backfill_runs_total
// counter. Use "manual" for admin-triggered runs and "periodic" for the
// background tick.
func (w *AvailabilityCacheBackfill) RunOnceWithTrigger(ctx context.Context, trigger string) (int, error) {
	return w.runOnceWithTrigger(ctx, trigger)
}

// runOnceWithTrigger is the internal variant that allows the admin /metrics
// path to record a distinct label.
func (w *AvailabilityCacheBackfill) runOnceWithTrigger(ctx context.Context, trigger string) (int, error) {
	// Always record the run attempt, even when the worker is in a
	// degraded state (nil db / disabled cache). Operators rely on this
	// counter to confirm the periodic loop is alive, and a missing
	// spike is itself a signal worth alerting on.
	recordAvailabilityBackfillRun(trigger)

	if w == nil || w.db == nil {
		return 0, nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	rows, err := w.db.Query(ctx, `
		SELECT credential_id, raw_model_name, state,
		       COALESCE(consecutive_successes, 0),
		       COALESCE(consecutive_failures, 0),
		       COALESCE(total_attempts, 0),
		       updated_at, next_retry_at, last_status, paused
		FROM v_node_probe_state_compat
		WHERE (credential_id, raw_model_name) > ($1, $2)
		ORDER BY credential_id, raw_model_name
		LIMIT $3
	`, w.cursorCredential, w.cursorModel, w.effectiveBatchSize())
	if err != nil {
		slog.Warn("availability backfill: query failed", "error", err)
		return 0, err
	}
	defer rows.Close()

	type row struct {
		credID     int
		model      string
		state      string
		succ       int
		fail       int
		total      int
		observedAt *time.Time
		nextRetry  *time.Time
		lastStatus *string
		paused     bool
	}
	var entries []row
	for rows.Next() {
		var r row
		var observedAt *time.Time
		var nextRetry *time.Time
		var lastStatus *string
		if err := rows.Scan(&r.credID, &r.model, &r.state, &r.succ, &r.fail, &r.total,
			&observedAt, &nextRetry, &lastStatus, &r.paused); err != nil {
			return 0, err
		}
		r.observedAt = observedAt
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
		// The shared 10s budget may expire mid-loop; stop cleanly instead of
		// WARN-failing every remaining entry (245 2026-09-16 audit). Entries
		// skipped here are retried by the next pass — the pass is idempotent.
		if err := ctx.Err(); err != nil {
			// R34: the truncation itself must stay observable — 2cead66dd
			// removed the per-entry WARN noise but left the partial pass
			// fully silent. One WARN per truncated pass restores that.
			slog.Warn("model_availability_backfill: sweep budget expired mid-pass, remaining entries deferred to next cycle",
				"written", written, "deferred", len(entries)-written)
			return written, err
		}
		// Advance even over fresh entries, so one busy prefix cannot starve
		// the rest of the cache. A failed write retries on the next sweep.
		w.cursorCredential, w.cursorModel = e.credID, e.model
		existing, err := w.reader.Read(ctx, e.credID, e.model)
		if err != nil {
			return written, err
		}
		newer := e.observedAt != nil && (existing == nil || existing.UpdatedAt == nil || e.observedAt.After(*existing.UpdatedAt))
		if existing != nil && !newer && !shouldRefresh(existing.UpdatedAt, w.lookback) {
			continue
		}
		status := ""
		if e.lastStatus != nil {
			status = *e.lastStatus
		}
		available := !e.paused && !isUnavailable(e.state)
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
		// Retain the database observation time; a cache refresh is not a new
		// probe result. Set atomically refuses older observations.
		if e.observedAt != nil {
			fields["updated_at"] = e.observedAt.UTC().Format(time.RFC3339Nano)
		}
		if err := w.cache.Set(ctx, e.credID, e.model, fields); err != nil {
			slog.Warn("availability backfill: cache write failed",
				"credential_id", e.credID,
				"raw_model", e.model,
				"error", err)
			continue
		}
		written++
	}
	if len(entries) < w.batchSize {
		w.cursorCredential, w.cursorModel = 0, ""
	}
	if written > 0 {
		recordAvailabilityBackfillRows(trigger, written)
		recordAvailabilityBackfillRowsPerRun(written)
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
