package bg

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BrokenProbeReviver re-queues model_probe_state rows stuck in
// 'broken_confirmed' so the ModelProbeRunner gives them another chance.
type BrokenProbeReviver struct {
	db          *pgxpool.Pool
	interval    time.Duration
	reviveAfter time.Duration
	stopCh      chan struct{}
	stopOnce    sync.Once
}

func NewBrokenProbeReviver(db *pgxpool.Pool, interval, reviveAfter time.Duration) *BrokenProbeReviver {
	if interval == 0 {
		interval = envDuration("LLM_GATEWAY_BROKEN_REVIVER_INTERVAL", 30*time.Minute)
	}
	if reviveAfter == 0 {
		reviveAfter = envDuration("LLM_GATEWAY_BROKEN_REVIVE_AFTER", 30*time.Minute)
	}
	return &BrokenProbeReviver{db: db, interval: interval, reviveAfter: reviveAfter, stopCh: make(chan struct{})}
}

func (w *BrokenProbeReviver) Start(ctx context.Context) {
	slog.Info("broken_probe_reviver started", "interval", w.interval, "revive_after", w.reviveAfter)
	go func() {
		w.runOnce(ctx)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("broken_probe_reviver stopping")
				return
			case <-w.stopCh:
				slog.Info("broken_probe_reviver stopped")
				return
			case <-ticker.C:
				if err := w.revive(ctx); err != nil {
					slog.Error("broken_probe_reviver failed", "error", err)
			}
			}
		}
	}()
}

func (w *BrokenProbeReviver) runOnce(ctx context.Context) {
	if err := w.revive(ctx); err != nil {
		slog.Error("broken_probe_reviver initial run failed", "error", err)
	}
}

func (w *BrokenProbeReviver) revive(ctx context.Context) error {
	if w.db == nil {
		return nil
	}
	tag, err := w.db.Exec(ctx, `
		UPDATE model_probe_state
		SET state = 'recovering', consecutive_failures = 1, next_retry_at = NOW(), last_state_change_at = NOW()
		WHERE state = 'broken_confirmed'
		  AND (last_attempt_at IS NULL OR last_attempt_at < NOW() - make_interval(secs => $1::float8))
	`, w.reviveAfter.Seconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		slog.Info("broken_probe_reviver: re-queued stuck broken_confirmed probes", "revived_count", tag.RowsAffected(), "revive_after", w.reviveAfter)
	}
	return nil
}

func (w *BrokenProbeReviver) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return time.Duration(n) * time.Second
}
