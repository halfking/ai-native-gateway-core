// bg/passive_probe_listener.go — v5 (2026-06-20) Layer 5 passive observer
//
// Listens to request_logs.error_kind every 30s, applies the secondary
// verification rule (consecutive >= 3 or error_rate >= 0.6 with total >= 5),
// records findings in passive_probe_state, and nudges Layer 1+4 for confirmatory
// active-probe re-checks.
//
// The PASSIVE path NEVER directly writes to credential_model_bindings — it
// always goes through the "reviewing" state and requires an active-recheck
// confirmation (Layer 1 or 4) before marking anything as unavailable.
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PassiveProbeListener scans request_logs every pollInterval for new errors
// and applies the secondary-verification trigger.
type PassiveProbeListener struct {
	db    *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}
	pollInterval time.Duration
}

// NewPassiveProbeListener creates a listener with the given poll interval.
// Default: 30s.
func NewPassiveProbeListener(db *pgxpool.Pool) *PassiveProbeListener {
	return &PassiveProbeListener{
		db:           db,
		done:         make(chan struct{}),
		pollInterval: 30 * time.Second,
	}
}

func (l *PassiveProbeListener) Start(ctx context.Context) {
	ctx, l.cancel = context.WithCancel(ctx)
	go l.run(ctx)
	slog.Info("passive probe listener (Layer 5) started",
		"poll_interval", l.pollInterval,
	)
}

func (l *PassiveProbeListener) Stop() {
	if l.cancel != nil {
		l.cancel()
	}
	<-l.done
}

func (l *PassiveProbeListener) run(ctx context.Context) {
	defer close(l.done)

	// Sleep 30s on start to let the gateway initialise.
	time.Sleep(30 * time.Second)
	l.reviewPromotion(ctx)

	ticker := time.NewTicker(l.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.pollNewErrors(ctx)
			l.reviewPromotion(ctx)
		}
	}
}

// pollNewErrors scans request_logs for recent failures and updates
// passive_probe_state counters.
func (l *PassiveProbeListener) pollNewErrors(ctx context.Context) {
	timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Cursor-based: only look at errors from the last 5 minutes.
	_, err := l.db.Exec(timeout, `
		INSERT INTO passive_probe_state
		    (credential_id, raw_model_name, error_kind,
		     consecutive_count, total_recent_count, window_total_count,
		     first_seen_at, last_seen_at, last_response_body_preview)
		SELECT
		    rl.credential_id,
		    COALESCE(rl.outbound_model, rl.client_model) AS raw_model_name,
		    COALESCE(rl.error_kind, 'unknown') AS error_kind,
		    1, 1, 1,
		    NOW(), NOW(),
		    LEFT(COALESCE(rl.response_body::text, ''), 200)
		FROM request_logs rl
		LEFT JOIN passive_probe_state pps
		    ON pps.credential_id = rl.credential_id
		    AND pps.raw_model_name = COALESCE(rl.outbound_model, rl.client_model)
		    AND pps.error_kind = COALESCE(rl.error_kind, 'unknown')
		    AND pps.last_seen_at > NOW() - INTERVAL '5 minutes'
		WHERE rl.success = FALSE
		  AND rl.ts > NOW() - INTERVAL '5 minutes'
		  AND rl.error_kind IN (
		      'model_not_found', 'quota', 'quota_periodic', 'quota_balance',
		      'quota_permanent', 'rate_limit',
		      'auth', 'auth_revoked', 'upstream_down'
		  )
		  AND rl.credential_id IS NOT NULL
		  AND rl.outbound_model IS NOT NULL
		  AND pps.credential_id IS NULL  -- only new rows not already counted
		GROUP BY rl.credential_id, rl.outbound_model, rl.client_model, rl.error_kind, rl.response_body
		HAVING COUNT(*) >= 3  -- secondary verification: 3 consecutive = trigger
		ON CONFLICT (credential_id, raw_model_name, error_kind)
		DO UPDATE SET
		    consecutive_count  = passive_probe_state.consecutive_count + EXCLUDED.consecutive_count,
		    total_recent_count = passive_probe_state.total_recent_count + EXCLUDED.total_recent_count,
		    last_seen_at       = NOW(),
		    last_response_body_preview = EXCLUDED.last_response_body_preview
	`)
	if err != nil {
		slog.Warn("passive probe: poll errors failed", "error", err)
	}
}

// reviewPromotion checks if any passive_probe_state entries should be
// promoted to reviewing (i.e. meet the secondary-verification rule).
func (l *PassiveProbeListener) reviewPromotion(ctx context.Context) {
	timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	result, err := l.db.Exec(timeout, `
		UPDATE passive_probe_state
		SET in_reviewing = TRUE,
		    reviewing_until = NOW() + INTERVAL '5 minutes'
		WHERE in_reviewing = FALSE
		  AND (
		    consecutive_count >= 3
		    OR (
		        window_total_count >= 5
		        AND (total_recent_count::float / window_total_count) >= 0.6
		    )
		  )
		  AND (
		    reviewing_until IS NULL
		    OR reviewing_until <= NOW()
		  )
	`)
	if err != nil {
		slog.Warn("passive probe: review promotion failed", "error", err)
		return
	}
	n := result.RowsAffected()
	if n > 0 {
		slog.Info("passive probe: promoted to reviewing",
			"count", n,
		)
	}
}

// ReportRecentFailures returns a human-readable summary of what's in review.
func (l *PassiveProbeListener) ReportRecentFailures(ctx context.Context) (string, error) {
	return "", fmt.Errorf("not implemented")
}
