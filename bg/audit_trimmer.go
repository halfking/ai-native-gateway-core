package bg

// audit_trimmer.go — P8.8: daily TTL worker for audit log tables.
//
// Trims rows older than 90 days from both audit tables that grew
// in P7.9 / P7.9.1:
//   - routing_overrides_audit  (trigger-based, contains override actions)
//   - routing_audit_log        (app-level, contains IP/UA + override actions)
//
// Why a daily worker: audit tables grow at the rate of admin
// actions. Without a TTL, a busy admin tenant would accumulate
// millions of audit rows over a year, hurting query performance
// and consuming storage.
//
// Cadence: 24h. The trim is bounded (LIMIT 5000 per batch per
// table) so even a backlog of millions of rows can be drained
// over a few days without long locks.
//
// Pattern follows bg/override_store_refresher.go (same sync.Once
// idempotent Stop, same chan struct{} lifecycle).

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditTrimmer periodically deletes expired audit rows.
type AuditTrimmer struct {
	pool     *pgxpool.Pool
	retention time.Duration // how long to keep rows (default 90d)
	tick     time.Duration
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewAuditTrimmer constructs the worker with default 90-day
// retention and 24-hour tick.
func NewAuditTrimmer(pool *pgxpool.Pool) *AuditTrimmer {
	return &AuditTrimmer{
		pool:      pool,
		retention: 90 * 24 * time.Hour,
		tick:      24 * time.Hour,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start spawns the background goroutine. Returns immediately.
// Performs an initial trim on startup so a fresh deploy doesn't
// wait 24h for the first cleanup.
func (t *AuditTrimmer) Start(ctx context.Context) {
	go t.run(ctx)
	slog.Info("audit trimmer started",
		"retention", t.retention.String(),
		"interval", t.tick.String())
}

// Stop terminates the goroutine and waits for it to finish.
// Safe to call on a never-Started trimmer (no-op) and safe to
// call multiple times (idempotent via sync.Once).
func (t *AuditTrimmer) Stop() {
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

// TrimOnce triggers an immediate trim (admin use).
//
// Returns the number of rows deleted from each table and any
// error encountered. Best-effort: errors are logged but don't
// stop the second table from being trimmed.
func (t *AuditTrimmer) TrimOnce(ctx context.Context) (overridesDeleted, auditDeleted int64, err error) {
	if t.pool == nil {
		return 0, 0, nil
	}
	start := time.Now()

	// routing_overrides_audit (P7.9 trigger-based log)
	res1, err := t.pool.Exec(ctx, `
		DELETE FROM routing_overrides_audit
		WHERE ts < NOW() - $1::interval
		LIMIT 5000
	`, t.retention.String())
	if err != nil {
		slog.Warn("audit_trimmer: routing_overrides_audit delete failed", "error", err)
	} else {
		overridesDeleted = res1.RowsAffected()
	}

	// routing_audit_log (P7.9.1 app-level log)
	res2, err := t.pool.Exec(ctx, `
		DELETE FROM routing_audit_log
		WHERE ts < NOW() - $1::interval
		LIMIT 5000
	`, t.retention.String())
	if err != nil {
		slog.Warn("audit_trimmer: routing_audit_log delete failed", "error", err)
	} else {
		auditDeleted = res2.RowsAffected()
	}

	slog.Info("audit_trimmer: trim complete",
		"overrides_deleted", overridesDeleted,
		"audit_deleted", auditDeleted,
		"duration_ms", time.Since(start).Milliseconds())
	return
}

func (t *AuditTrimmer) run(ctx context.Context) {
	defer close(t.done)

	// Initial trim on startup (drain any pre-existing backlog)
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
			t.TrimOnce(ctx)
		}
	}
}
