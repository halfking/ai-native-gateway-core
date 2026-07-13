package bg

// opslog_trimmer.go — daily TTL worker for operational detail tables.
//
// These tables hold short-lived diagnostic detail that is only
// queried over a 5-minute window (see bg/candidate_failure_monitor.go
// and bg/model_probe.go applyPassiveBoosts). Rows older than that
// window have no live code path reading them, but we keep a longer
// buffer for operational forensics.
//
// Targets:
//   - candidate_failure_logs         (ts column, 7-day default)
//     Driven by request failures; all query windows are 5 minutes,
//     so 7 days is 2000x the live window — ample for incident RCA.
//   - credential_probe_model_log     (created_at column, 30-day default)
//     Audit of admin force-recover + default probe picker model
//     switches; low write volume but useful for long-trend analysis.
//
// Both retentions are hot-reloadable via settings_kv:
//   - lifecycle.candidate_failure_logs_ttl_days
//   - lifecycle.credential_probe_model_log_ttl_days
//
// Cadence: 24h. Bounded LIMIT 5000 per batch per table.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	defaultCflRetention = 7 * 24 * time.Hour
	defaultCpmRetention = 30 * 24 * time.Hour
)

// OpslogTrimmer periodically deletes expired rows from the two
// operational detail tables.
type OpslogTrimmer struct {
	pool         *pgxpool.Pool
	cflRetention time.Duration
	cpmRetention time.Duration
	tick         time.Duration
	stop         chan struct{}
	done         chan struct{}
	stopOnce     sync.Once
}

// NewOpslogTrimmer constructs the worker with default 7-day
// (candidate_failure_logs) and 30-day (credential_probe_model_log)
// retention, 24-hour tick.
func NewOpslogTrimmer(pool *pgxpool.Pool) *OpslogTrimmer {
	return &OpslogTrimmer{
		pool:         pool,
		cflRetention: defaultCflRetention,
		cpmRetention: defaultCpmRetention,
		tick:         24 * time.Hour,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// Start spawns the background goroutine. Returns immediately.
// Performs an initial trim on startup so a fresh deploy drains any
// pre-existing backlog without waiting 24h.
func (t *OpslogTrimmer) Start(ctx context.Context) {
	go t.run(ctx)
	slog.Info("opslog trimmer started",
		"cfl_retention", t.cflRetention.String(),
		"cpm_retention", t.cpmRetention.String(),
		"interval", t.tick.String())
}

// Stop terminates the goroutine and waits for it to finish.
// Safe to call on a never-Started trimmer (no-op) and safe to
// call multiple times (idempotent via sync.Once).
func (t *OpslogTrimmer) Stop() {
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
//
// Returns the number of rows deleted from each table and any error.
// Best-effort: errors are logged but don't stop the second table
// from being trimmed.
func (t *OpslogTrimmer) TrimOnce(ctx context.Context) (cflDeleted, cpmDeleted int64, err error) {
	if t.pool == nil {
		return 0, 0, nil
	}

	cflDays := settings.GetPlatformInt("lifecycle.candidate_failure_logs_ttl_days", 7)
	if cflDays < 1 {
		cflDays = 7
	}
	cflRetention := time.Duration(cflDays) * 24 * time.Hour

	cpmDays := settings.GetPlatformInt("lifecycle.credential_probe_model_log_ttl_days", 30)
	if cpmDays < 1 {
		cpmDays = 30
	}
	cpmRetention := time.Duration(cpmDays) * 24 * time.Hour

	start := time.Now()

	// candidate_failure_logs (uses ts column per migration 300 schema).
	res1, err := t.pool.Exec(ctx, `
		DELETE FROM candidate_failure_logs
		WHERE id IN (
			SELECT id FROM candidate_failure_logs
			WHERE ts < NOW() - $1::interval
			ORDER BY ts
			LIMIT 5000
		)
	`, cflRetention.String())
	if err != nil {
		slog.Warn("opslog_trimmer: candidate_failure_logs delete failed", "error", err)
	} else {
		cflDeleted = res1.RowsAffected()
	}

	// credential_probe_model_log (uses created_at column).
	res2, err := t.pool.Exec(ctx, `
		DELETE FROM credential_probe_model_log
		WHERE id IN (
			SELECT id FROM credential_probe_model_log
			WHERE created_at < NOW() - $1::interval
			ORDER BY created_at
			LIMIT 5000
		)
	`, cpmRetention.String())
	if err != nil {
		slog.Warn("opslog_trimmer: credential_probe_model_log delete failed", "error", err)
	} else {
		cpmDeleted = res2.RowsAffected()
	}

	slog.Info("opslog_trimmer: trim complete",
		"cfl_deleted", cflDeleted,
		"cpm_deleted", cpmDeleted,
		"cfl_ttl_days", cflDays,
		"cpm_ttl_days", cpmDays,
		"duration_ms", time.Since(start).Milliseconds())
	return
}

func (t *OpslogTrimmer) run(ctx context.Context) {
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
