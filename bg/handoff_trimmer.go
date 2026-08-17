package bg

// handoff_trimmer.go — daily TTL worker for handoff_logs.
//
// 2026-08-18 hot+columnar 架构：历史保留改由 drop_old_state_partitions 按月度
// columnar 分区整体 DROP（citus columnar 不支持行级 DELETE），本 trimmer 只负责
// 兜底清理热层 handoff_logs_hot —— 正常情况下 promote cron 在 8 小时窗口内已把
// 老数据搬走，这里仅在 promote 长期故障导致热层积压时生效。
//
// Retention: 14 days by default (hot-reloadable via
// lifecycle.handoff_logs_ttl_days). Cadence: 24h. The trim is
// bounded (LIMIT 5000 per batch) so even a large backlog drains
// over a few ticks without long locks.
//
// Pattern follows bg/audit_trimmer.go (same sync.Once idempotent
// Stop, same chan struct{} lifecycle).

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// defaultHandoffRetention is the fallback retention when settings
// is unavailable or the key is missing. 14 days balances forensic
// value (handoff prompts are the primary post-mortem artifact for
// context-overflow incidents) against storage cost.
const defaultHandoffRetention = 14 * 24 * time.Hour

// HandoffTrimmer periodically deletes expired handoff_logs rows.
type HandoffTrimmer struct {
	pool      *pgxpool.Pool
	retention time.Duration
	tick      time.Duration
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
}

// NewHandoffTrimmer constructs the worker with default 14-day
// retention and 24-hour tick.
func NewHandoffTrimmer(pool *pgxpool.Pool) *HandoffTrimmer {
	return &HandoffTrimmer{
		pool:      pool,
		retention: defaultHandoffRetention,
		tick:      24 * time.Hour,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start spawns the background goroutine. Returns immediately.
// Performs an initial trim on startup so a fresh deploy drains any
// pre-existing backlog without waiting 24h.
func (t *HandoffTrimmer) Start(ctx context.Context) {
	go t.run(ctx)
	slog.Info("handoff trimmer started",
		"retention", t.retention.String(),
		"interval", t.tick.String())
}

// Stop terminates the goroutine and waits for it to finish.
// Safe to call on a never-Started trimmer (no-op) and safe to
// call multiple times (idempotent via sync.Once).
func (t *HandoffTrimmer) Stop() {
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
// Returns the number of rows deleted and any error encountered.
// Bounded to LIMIT 5000 per call so a single tick cannot hold a
// long lock even if millions of rows are eligible.
func (t *HandoffTrimmer) TrimOnce(ctx context.Context) (int64, error) {
	if t.pool == nil {
		return 0, nil
	}

	ttlDays := settings.GetPlatformInt("lifecycle.handoff_logs_ttl_days", 14)
	if ttlDays < 1 {
		ttlDays = 14 // safety floor — never set to 0 (would wipe the table)
	}
	retention := time.Duration(ttlDays) * 24 * time.Hour

	start := time.Now()
	res, err := t.pool.Exec(ctx, `
		DELETE FROM handoff_logs_hot
		WHERE id IN (
			SELECT id FROM handoff_logs_hot
			WHERE created_at < NOW() - $1::interval
			ORDER BY created_at
			LIMIT 5000
		)
	`, retention.String())
	if err != nil {
		slog.Warn("handoff_trimmer: delete failed", "error", err)
		return 0, err
	}
	deleted := res.RowsAffected()
	if deleted > 0 {
		slog.Info("handoff_trimmer: trim complete",
			"deleted", deleted,
			"ttl_days", ttlDays,
			"duration_ms", time.Since(start).Milliseconds())
	}
	return deleted, nil
}

func (t *HandoffTrimmer) run(ctx context.Context) {
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
