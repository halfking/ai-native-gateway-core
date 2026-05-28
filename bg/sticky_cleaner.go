package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type StickyCleaner struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}
}

func NewStickyCleaner(db *pgxpool.Pool) *StickyCleaner {
	return &StickyCleaner{db: db, done: make(chan struct{})}
}

func (c *StickyCleaner) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	go c.run(ctx)
	slog.Info("sticky session cleaner started", "interval", "300s")
}

func (c *StickyCleaner) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	<-c.done
}

func (c *StickyCleaner) run(ctx context.Context) {
	defer close(c.done)

	ticker := time.NewTicker(300 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.clean(ctx)
		}
	}
}

func (c *StickyCleaner) clean(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tag, err := c.db.Exec(timeoutCtx, `DELETE FROM sticky_sessions WHERE expires_at < now()`)
	if err != nil {
		slog.Debug("sticky session cleanup failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("sticky sessions cleaned", "count", tag.RowsAffected())
	}
}
