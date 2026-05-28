package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type EnvelopeCleaner struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}
}

func NewEnvelopeCleaner(db *pgxpool.Pool) *EnvelopeCleaner {
	return &EnvelopeCleaner{db: db, done: make(chan struct{})}
}

func (c *EnvelopeCleaner) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	go c.run(ctx)
	slog.Info("envelope cleaner started", "interval", "3600s")
}

func (c *EnvelopeCleaner) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	<-c.done
}

func (c *EnvelopeCleaner) run(ctx context.Context) {
	defer close(c.done)

	ticker := time.NewTicker(3600 * time.Second)
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

func (c *EnvelopeCleaner) clean(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := c.db.Exec(timeoutCtx, `DELETE FROM request_envelope WHERE expires_at < now()`)
	if err != nil {
		slog.Debug("envelope cleanup failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("envelopes cleaned", "count", tag.RowsAffected())
	}
}
