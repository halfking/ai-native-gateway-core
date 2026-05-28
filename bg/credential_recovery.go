package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type CredentialRecovery struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}
}

func NewCredentialRecovery(db *pgxpool.Pool) *CredentialRecovery {
	return &CredentialRecovery{db: db, done: make(chan struct{})}
}

func (r *CredentialRecovery) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	go r.run(ctx)
	slog.Info("credential recovery task started", "interval", "60s")
}

func (r *CredentialRecovery) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

func (r *CredentialRecovery) run(ctx context.Context) {
	defer close(r.done)

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.recover(ctx)
		}
	}
}

func (r *CredentialRecovery) recover(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := r.db.Exec(timeoutCtx, `
		UPDATE credentials
		SET availability_state = 'ready',
		    availability_recover_at = NULL,
		    state_updated_at = now()
		WHERE availability_state IN ('cooling','rate_limited','unreachable')
		  AND availability_recover_at IS NOT NULL
		  AND availability_recover_at <= now()
		  AND lifecycle_status = 'active'
	`)
	if err != nil {
		slog.Warn("credential availability recovery failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("credential availability recovered", "count", tag.RowsAffected())
	}

	tag, err = r.db.Exec(timeoutCtx, `
		UPDATE credentials
		SET quota_state = 'ok',
		    quota_recover_at = NULL,
		    state_updated_at = now()
		WHERE quota_state = 'periodic_exhausted'
		  AND quota_recover_at IS NOT NULL
		  AND quota_recover_at <= now()
		  AND lifecycle_status = 'active'
	`)
	if err != nil {
		slog.Warn("credential quota recovery failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("credential quota recovered", "count", tag.RowsAffected())
	}
}
