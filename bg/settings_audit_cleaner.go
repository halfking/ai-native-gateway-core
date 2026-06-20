package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// SettingsAuditCleaner runs daily to enforce the 7-day retention on
// settings_audit (Q6: C). Without this worker, settings_audit would
// grow unbounded.
type SettingsAuditCleaner struct {
	db        *pgxpool.Pool
	interval  time.Duration
	retention time.Duration
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewSettingsAuditCleaner wires the cleaner with sensible defaults:
// interval 24h, retention 7d.
func NewSettingsAuditCleaner(db *pgxpool.Pool) *SettingsAuditCleaner {
	return &SettingsAuditCleaner{
		db:        db,
		interval:  24 * time.Hour,
		retention: 7 * 24 * time.Hour,
		done:      make(chan struct{}),
	}
}

// Start begins the daily cleanup loop. Call Stop() to terminate.
func (c *SettingsAuditCleaner) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	go c.run(ctx)
	slog.Info("settings_audit cleaner started",
		"interval", c.interval.String(),
		"retention", c.retention.String())
}

// Stop terminates the cleanup loop and waits for it to exit.
func (c *SettingsAuditCleaner) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	<-c.done
}

func (c *SettingsAuditCleaner) run(ctx context.Context) {
	defer close(c.done)

	// Run once at startup so a fresh deployment doesn't sit on 7 days of
	// accumulated audit from the first day.
	c.runOnce(ctx)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runOnce(ctx)
		}
	}
}

func (c *SettingsAuditCleaner) runOnce(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	deleted, err := settings.CleanupOldAudit(runCtx, c.db, c.retention)
	if err != nil {
		slog.Warn("settings_audit cleaner failed", "err", err)
		return
	}
	if deleted > 0 {
		slog.Info("settings_audit cleaner: deleted rows", "count", deleted)
	}
}