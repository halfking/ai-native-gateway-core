package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// defaultHandoffPendingRetention mirrors HandoffTrimmer's forensic TTL for
// handoff_logs: 14 days. Once a confirmation proposal (pending or already
// confirmed) is older than this, its audit trail is no longer needed and the
// large handoff_prompt TEXT column can be reclaimed.
const defaultHandoffPendingRetention = 14 * 24 * time.Hour

// HandoffPendingTrimmer marks expired capabilities terminal and physically
// removes proposals past the forensic TTL. Confirmation still enforces expiry
// in its transaction, so cleanup cannot open a replay window.
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
	// Step 1: expire any still-pending capabilities whose 5-minute window has
	// passed. Confirmation enforces expiry in its own transaction, so flipping
	// them here is purely bookkeeping and cannot open a replay window.
	expireRes, err := t.pool.Exec(ctx, `
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
	expired := expireRes.RowsAffected()

	// Step 2: physically delete rows past the forensic TTL. Both confirmed and
	// expired proposals are eligible — each holds a multi-MB handoff_prompt
	// TEXT column, so without this delete the table grows unbounded alongside
	// handoff_logs. Bounded to LIMIT 5000 per batch to avoid a long lock on a
	// large backlog.
	retention := pendingConfirmationRetention()
	delRes, err := t.pool.Exec(ctx, `
DELETE FROM handoff_pending_confirmations
WHERE id IN (
    SELECT id FROM handoff_pending_confirmations
    WHERE proposal_created_at < NOW() - $1::interval
    ORDER BY proposal_created_at
    LIMIT 5000
)`, retention.String())
	if err != nil {
		return expired, err
	}
	deleted := delRes.RowsAffected()
	return expired + deleted, nil
}

// pendingConfirmationRetention resolves the forensic TTL for confirmation
// proposals. Hot-reloadable via lifecycle.handoff_logs_ttl_days so the two
// handoff tables share one retention policy. A TTL below 1 day is floored to
// the default to prevent a misconfiguration from wiping the table.
func pendingConfirmationRetention() time.Duration {
	ttlDays := settings.GetPlatformInt("lifecycle.handoff_logs_ttl_days", 14)
	if ttlDays < 1 {
		ttlDays = 14
	}
	return time.Duration(ttlDays) * 24 * time.Hour
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
