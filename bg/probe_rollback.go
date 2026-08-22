// bg/probe_rollback.go — delayed-rollback worker for tentative probe restores.
//
// 需求 6, bullet 3: 自检后对凭据节点的状态的修改、回退的延时处理.
//
// A probe that tentatively restores a node stamps credential_model_bindings
// .probe_revert_at = now()+T (via MarkTentativeRestore). A subsequent
// confirming probe success CLEARS probe_revert_at (see updateBindingAvailability
// success branch), so the revert is a one-shot guard against a probe that
// "passed" in isolation but whose restore should not persist without
// corroboration. This worker reverts bindings whose probe_revert_at has elapsed
// and were not confirmed.
//
// Mirrors the bg/credential_recovery.go ticker pattern (one-table-per-concern,
// no generic job framework). Respects admin_protected and manual% unavailable
// reasons (never fights an operator).
package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProbeRollbackConfig tunes the delayed-rollback scan.
type ProbeRollbackConfig struct {
	Interval time.Duration // scan cadence; defaults 10s
}

// ProbeRollback reverts tentative probe restores whose revert window elapsed.
type ProbeRollback struct {
	db     *pgxpool.Pool
	cfg    ProbeRollbackConfig
	cancel context.CancelFunc
	done   chan struct{}
}

// NewProbeRollback constructs the worker. Interval<=0 defaults to 10s.
func NewProbeRollback(db *pgxpool.Pool, cfg ProbeRollbackConfig) *ProbeRollback {
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	return &ProbeRollback{db: db, cfg: cfg, done: make(chan struct{})}
}

// Start launches the scan loop. No-op without a db.
func (r *ProbeRollback) Start(ctx context.Context) {
	if r == nil || r.db == nil {
		if r != nil {
			close(r.done)
		}
		return
	}
	ctx, r.cancel = context.WithCancel(ctx)
	go r.run(ctx)
}

// Stop cancels the loop and waits for it to exit.
func (r *ProbeRollback) Stop() {
	if r == nil {
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

func (r *ProbeRollback) run(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := r.revertDue(ctx); err != nil {
				slog.Warn("probe_rollback: revert scan failed", "error", err)
			} else if n > 0 {
				slog.Info("probe_rollback: reverted tentative restores", "count", n)
			}
		}
	}
}

// revertDue flips available→FALSE for bindings whose probe_revert_at has
// elapsed and clears the timer (one-shot). Guards:
//   - admin_protected bindings are never touched;
//   - manual% unavailable_reason bindings are never touched;
//   - only available=TRUE rows are reverted (a row already marked unavailable
//     by another path just has its timer cleared).
//
// The race with a concurrent confirming probe is closed by the WHERE clause:
// updateBindingAvailability's success branch sets probe_revert_at=NULL in the
// SAME row, so once a confirm committed, this UPDATE matches zero rows.
func (r *ProbeRollback) revertDue(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tag, err := r.db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available = FALSE,
		    unavailable_reason = COALESCE(cmb.unavailable_reason, 'probe_revert_timeout'),
		    unavailable_at = COALESCE(cmb.unavailable_at, now()),
		    unavailable_recover_at = COALESCE(cmb.unavailable_recover_at, now() + interval '5 minutes'),
		    probe_revert_at = NULL,
		    updated_at = now()
		WHERE cmb.probe_revert_at IS NOT NULL
		  AND cmb.probe_revert_at <= now()
		  AND cmb.available = TRUE
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MarkTentativeRestore stamps a (credential, model) binding as tentatively
// restored: available=TRUE now, but auto-reverted at revertAfter unless a
// confirming probe success clears probe_revert_at first. Used by "smart
// fallback" probe scenarios (需求 6 bullet 6: 需要智能回退的). Best-effort.
func MarkTentativeRestore(ctx context.Context, db *pgxpool.Pool, credID int, model string, revertAfter time.Duration) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at = NULL,
		    unavailable_recover_at = NULL,
		    probe_revert_at = now() + $3::interval,
		    updated_at = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE`,
		credID, model, int(revertAfter.Seconds()))
	return err
}
