package bg

// session_summaries_trimmer.go — 2026-09-09 (审计 R3#4): daily TTL worker
// for archived session_summaries rows.
//
// Migration 471 added the archived_at marker (domains/sessionarchive marks
// 30d-inactive summaries with archived_at = NOW()), but archived rows were
// never deleted — the table grew unboundedly. This trimmer DELETEs rows
// that are archived AND older than the retention window:
//
//	archived_at IS NOT NULL AND archived_at < NOW() - ttl
//
// Active rows (archived_at IS NULL) are never touched.
//
// Retention: lifecycle.session_summaries_ttl_days (default 90,
// hot-reloadable). Backed by idx_session_summaries_archived (migration
// 690, partial index on archived_at) so each batch is an index descent.
//
// Cadence: 24h. Bounded LIMIT 5000 per batch, at most maxBatchesPerTick
// batches per run (5000 × 20 = 100k rows/tick ceiling) so even a
// multi-million-row backlog drains over days without long locks.
//
// Pattern follows bg/opslog_trimmer.go (same sync.Once idempotent Stop,
// same chan struct{} lifecycle).

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	// defaultSessionSummariesRetention is the archived-row keep-window:
	// 90d after archival, matching lifecycle.session_summaries_ttl_days.
	defaultSessionSummariesRetention = 90 * 24 * time.Hour

	// sessionSummariesBatchSize bounds each DELETE (single batch ≤5000
	// rows per the audit mandate — same bound as the other trimmers).
	sessionSummariesBatchSize = 5000

	// sessionSummariesMaxBatchesPerTick caps the work of a single run.
	sessionSummariesMaxBatchesPerTick = 20
)

// SessionSummariesTrimmer periodically deletes expired archived rows from
// session_summaries.
type SessionSummariesTrimmer struct {
	pool      *pgxpool.Pool
	retention time.Duration
	tick      time.Duration
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
}

// NewSessionSummariesTrimmer constructs the worker with the default
// 90-day archived-row retention and 24-hour tick.
func NewSessionSummariesTrimmer(pool *pgxpool.Pool) *SessionSummariesTrimmer {
	return &SessionSummariesTrimmer{
		pool:      pool,
		retention: defaultSessionSummariesRetention,
		tick:      24 * time.Hour,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start spawns the background goroutine. Returns immediately.
// Performs an initial trim on startup so a fresh deploy drains any
// pre-existing backlog without waiting 24h.
func (t *SessionSummariesTrimmer) Start(ctx context.Context) {
	go t.run(ctx)
	slog.Info("session_summaries trimmer started",
		"retention", t.retention.String(),
		"interval", t.tick.String(),
		"batch_size", sessionSummariesBatchSize)
}

// Stop terminates the goroutine and waits for it to finish.
// Safe to call on a never-Started trimmer (no-op) and safe to call
// multiple times (idempotent via sync.Once).
func (t *SessionSummariesTrimmer) Stop() {
	if t.stop == nil || t.done == nil {
		return
	}
	t.stopOnce.Do(func() {
		close(t.stop)
	})
	select {
	case <-t.done:
	default:
		// goroutine never started
	}
}

// TrimOnce triggers an immediate trim (admin use / startup drain).
// Returns the number of archived rows deleted. Errors are logged and
// abort the current run (next tick retries); batches loop until a
// short batch or the per-tick batch cap.
func (t *SessionSummariesTrimmer) TrimOnce(ctx context.Context) (int64, error) {
	if t.pool == nil {
		return 0, nil
	}

	ttlDays := settings.GetPlatformInt("lifecycle.session_summaries_ttl_days",
		int(defaultSessionSummariesRetention.Hours()/24))
	if ttlDays < 1 {
		ttlDays = int(defaultSessionSummariesRetention.Hours() / 24) // safety floor — never 0
	}
	retention := time.Duration(ttlDays) * 24 * time.Hour

	start := time.Now()
	var total int64
	for batch := 0; batch < sessionSummariesMaxBatchesPerTick; batch++ {
		// ctid pattern: session_summaries has no `id` column (PK is
		// session_key), and ctid keeps the batch bound exact regardless.
		res, err := t.pool.Exec(ctx, `
			DELETE FROM session_summaries
			WHERE ctid IN (
				SELECT ctid FROM session_summaries
				WHERE archived_at IS NOT NULL
				  AND archived_at < NOW() - $1::interval
				LIMIT 5000
			)
		`, retention.String())
		if err != nil {
			slog.Error("session_summaries trimmer: delete failed",
				"error", err, "ttl_days", ttlDays,
				"deleted_before_failure", total)
			return total, err
		}
		n := res.RowsAffected()
		total += n
		if n < sessionSummariesBatchSize {
			break // backlog drained for this tick
		}
	}

	slog.Info("session_summaries trimmer: trim complete",
		"deleted", total, "ttl_days", ttlDays,
		"duration_ms", time.Since(start).Milliseconds())
	return total, nil
}

func (t *SessionSummariesTrimmer) run(ctx context.Context) {
	defer close(t.done)

	// Initial trim on startup (drain any pre-existing backlog).
	//nolint:errcheck // best-effort trim, non-critical
	t.TrimOnce(ctx)

	tk := time.NewTicker(t.tick)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stop:
			return
		case <-tk.C:
			//nolint:errcheck // best-effort trim, non-critical
			t.TrimOnce(ctx)
		}
	}
}
