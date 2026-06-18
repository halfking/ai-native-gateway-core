package bg

import (
	"context"
	"log/slog"
	"os"
	"strconv"
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

	tag, err = r.db.Exec(timeoutCtx, `
		UPDATE credentials
		SET circuit_state = 'closed',
		    cooling_until = NULL,
		    consecutive_failures = 0,
		    state_updated_at = now()
		WHERE circuit_state = 'open'
		  AND (cooling_until IS NULL OR cooling_until <= now())
		  AND lifecycle_status = 'active'
	`)
	if err != nil {
		slog.Warn("circuit breaker recovery failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("circuit breakers closed", "count", tag.RowsAffected())
	}

	tag, err = r.db.Exec(timeoutCtx, `
		UPDATE credentials
		SET consecutive_failures = 0,
		    state_updated_at = now()
		WHERE consecutive_failures > 0
		  AND last_used_at < now() - INTERVAL '1 hour'
		  AND circuit_state = 'closed'
		  AND availability_state = 'ready'
		  AND lifecycle_status = 'active'
	`)
	if err != nil {
		slog.Warn("failure counter clear failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("stale failure counters cleared", "count", tag.RowsAffected())
	}

	// Reset stale health_status="unreachable" / "auth_failed" / "error" rows.
	//
	// Root cause (2026-06-12): v_routable_credential_models.is_routable
	// requires health_status IN ('healthy', 'unknown'). A single cycler/probe-v2
	// failure marks a credential as 'unreachable', which sets is_routable=FALSE.
	// Without this recovery branch, every credential flagged unreachable
	// stays unroutable until the next probe runs (up to 90 minutes). During
	// that window, all providers share the same root failure cause, and
	// users see a "every provider fails at the same time" outage.
	//
	// Recovery rule: re-probe a credential as soon as its health_status is
	// not 'healthy'/'unknown' for more than 2 minutes. The next cycler
	// (every hour) or probe-v2 (next :30 mark) will overwrite health_status
	// with a fresh result, and a successful probe restores routability.
	tag, err = r.db.Exec(timeoutCtx, `
		UPDATE credentials
		SET health_status = 'unknown',
		    health_error = NULL,
		    health_source = 'probe',
		    health_checked_at = NOW(),
		    state_updated_at = NOW()
		WHERE health_status NOT IN ('healthy', 'unknown')
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND (health_checked_at IS NULL OR health_checked_at < NOW() - INTERVAL '2 minutes')
		  AND COALESCE(availability_state, 'ready') NOT IN ('suspended', 'auth_failed')
	`)
	if err != nil {
		slog.Warn("health_status recovery failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Warn("stale health_status reset to 'unknown' (re-probe will rerun shortly)",
			"count", tag.RowsAffected(),
		)
	}

	tag, err = r.db.Exec(timeoutCtx, mnfCoolingRecoverySQL(), mnfCoolingRecoveryMinutes())
	if err != nil {
		slog.Warn("mnf_cooling binding recovery failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("mnf_cooling bindings recovered", "count", tag.RowsAffected())
	}
}

func mnfCoolingRecoveryMinutes() int {
	if v := os.Getenv("LLM_GATEWAY_MNF_COOL_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 2
}

func mnfCoolingRecoverySQL() string {
	return `
		UPDATE credential_model_bindings cmb
		SET available = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at = NULL
		FROM credentials c, providers p
		WHERE cmb.credential_id = c.id
		  AND c.provider_id = p.id
		  AND cmb.available = FALSE
		  AND cmb.unavailable_reason = 'mnf_cooling'
		  AND cmb.unavailable_at IS NOT NULL
		  AND cmb.unavailable_at <= NOW() - make_interval(mins => $1)
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
	`
}
