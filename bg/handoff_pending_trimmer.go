package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// HandoffPendingTrimmer marks expired capabilities terminal. Confirmation still
// enforces expiry in its transaction, so cleanup cannot open a replay window.
type HandoffPendingTrimmer struct {
	pool     *pgxpool.Pool
	tick     time.Duration
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func NewHandoffPendingTrimmer(pool *pgxpool.Pool) *HandoffPendingTrimmer {
	return &HandoffPendingTrimmer{pool: pool, tick: time.Minute, stop: make(chan struct{}), done: make(chan struct{})}
}

func (t *HandoffPendingTrimmer) Start(ctx context.Context) {
	if t != nil {
		go t.run(ctx)
	}
}

func (t *HandoffPendingTrimmer) Stop() {
	if t == nil || t.stop == nil || t.done == nil {
		return
	}
	t.stopOnce.Do(func() { close(t.stop) })
	select {
	case <-t.done:
	default:
	}
}

func (t *HandoffPendingTrimmer) TrimOnce(ctx context.Context) (int64, error) {
	if t == nil || t.pool == nil {
		return 0, nil
	}
	result, err := t.pool.Exec(ctx, `
UPDATE handoff_pending_confirmations
SET status = 'expired', updated_at = NOW()
WHERE id IN (
    SELECT id FROM handoff_pending_confirmations
    WHERE status = 'pending' AND expires_at <= NOW()
    ORDER BY expires_at
    LIMIT 5000
)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (t *HandoffPendingTrimmer) run(ctx context.Context) {
	defer close(t.done)
	t.trim(ctx)
	ticker := time.NewTicker(t.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stop:
			return
		case <-ticker.C:
			t.trim(ctx)
		}
	}
}

func (t *HandoffPendingTrimmer) trim(ctx context.Context) {
	count, err := t.TrimOnce(ctx)
	if err != nil {
		slog.Warn("handoff pending confirmation cleanup failed", "error", err)
		return
	}
	if count > 0 {
		slog.Info("handoff pending confirmations expired", "count", count)
	}
}
