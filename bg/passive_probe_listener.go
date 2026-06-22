// bg/passive_probe_listener.go — v6 (2026-06-22) Layer 5 passive observer
//
// Listens to request_logs.error_kind every 30s, applies the secondary
// verification rule (consecutive >= 3 or error_rate >= 0.6 with total >= 5),
// records findings in passive_probe_state, and after the 5-minute reviewing
// window resolves the credential: if still failing → mark unreachable;
// otherwise → clear reviewing and reset counters.
//
// The PASSIVE path NEVER directly writes to credential_model_bindings — it
// always goes through the "reviewing" state (5-minute observation window)
// before marking a credential as availability_state='unreachable'.
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/credentialstate"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// transientErrorKinds are the error types that indicate a credential/network
// problem (not a permanent issue like quota/auth). Only these trigger the
// reviewing → unreachable path, matching the spec:
// "≥3 次连续 transient/timeout/network 错误".
var transientErrorKinds = []string{
	"transient", "timeout", "network", "concurrent", "stream_timeout", "upstream_down",
}

// PassiveProbeListener scans request_logs every pollInterval for new errors
// and applies the secondary-verification trigger.
type PassiveProbeListener struct {
	db           *pgxpool.Pool
	stateWriter  *credentialstate.Writer
	cancel       context.CancelFunc
	done         chan struct{}
	pollInterval time.Duration
}

// NewPassiveProbeListener creates a listener with the given poll interval.
// Default: 30s. stateWriter is used to mark credentials unreachable after
// the reviewing window confirms persistent failures.
func NewPassiveProbeListener(db *pgxpool.Pool, stateWriter *credentialstate.Writer) *PassiveProbeListener {
	return &PassiveProbeListener{
		db:           db,
		stateWriter:  stateWriter,
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
	l.pollNewErrors(ctx)
	l.reviewPromotion(ctx)
	l.reviewResolution(ctx)

	ticker := time.NewTicker(l.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.pollNewErrors(ctx)
			l.reviewPromotion(ctx)
			l.reviewResolution(ctx)
		}
	}
}

// resetCountersOnSuccess clears consecutive_count for any (credential, model)
// pair that had a successful request since the last poll. This ensures
// "consecutive" really means consecutive — a single success breaks the streak.
func (l *PassiveProbeListener) resetCountersOnSuccess(ctx context.Context) {
	timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, err := l.db.Exec(timeout, `
		UPDATE passive_probe_state pps
		SET consecutive_count = 0
		FROM (
			SELECT DISTINCT credential_id, COALESCE(outbound_model, client_model) AS raw_model_name
			FROM request_logs
			WHERE success = TRUE
			  AND ts > NOW() - INTERVAL '5 minutes'
			  AND credential_id IS NOT NULL
			  AND outbound_model IS NOT NULL
		) AS success_pairs
		WHERE pps.credential_id = success_pairs.credential_id
		  AND pps.raw_model_name = success_pairs.raw_model_name
		  AND pps.consecutive_count > 0
	`)
	if err != nil {
		slog.Debug("passive probe: success reset failed", "error", err)
	}
}

// pollNewErrors scans request_logs for recent failures and updates
// passive_probe_state counters.
//
// Design: every poll cycle (30s), we count errors from the last 5 minutes
// but only those rows that have NOT been counted in the last 45 seconds.
// This avoids double-counting while still accumulating ALL errors across
// cycles — the ON CONFLICT DO UPDATE path adds the new COUNT to the
// existing counter each cycle.
//
// v6 audit fix (2026-06-22):
//   - BUG-F/H fix: call resetCountersOnSuccess() before polling, so a
//     successful request breaks the consecutive streak.
//   - BUG-G fix: only poll transient-class errors (transient/timeout/network/
//     concurrent/stream_timeout/upstream_down). Quota/auth/model_not_found
//     are handled by their own dedicated state machines
//     (credentialstate.Writer + model_probe), not the reviewing path.
//   - BUG-E fix: window_total_count now counts ALL requests (success+failure)
//     for the (credential, model) pair, not just failures, so the error_rate
//     ratio in reviewPromotion is a real error rate.
func (l *PassiveProbeListener) pollNewErrors(ctx context.Context) {
	// BUG-F/H: reset consecutive streak for pairs that had a success.
	l.resetCountersOnSuccess(ctx)

	timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Step 1: accumulate new transient-class errors into counters.
	_, err := l.db.Exec(timeout, `
		INSERT INTO passive_probe_state
		    (credential_id, raw_model_name, error_kind,
		     consecutive_count, total_recent_count, window_total_count,
		     first_seen_at, last_seen_at, last_response_body_preview)
		SELECT
		    rl.credential_id,
		    COALESCE(rl.outbound_model, rl.client_model) AS raw_model_name,
		    COALESCE(rl.error_kind, 'unknown') AS error_kind,
		    COUNT(*), COUNT(*), 0,
		    MIN(rl.ts), NOW(),
		    LEFT(COALESCE(MAX(rl.response_body::text), ''), 200)
		FROM request_logs rl
		LEFT JOIN passive_probe_state pps
		    ON pps.credential_id = rl.credential_id
		    AND pps.raw_model_name = COALESCE(rl.outbound_model, rl.client_model)
		    AND pps.error_kind = COALESCE(rl.error_kind, 'unknown')
		    AND pps.last_seen_at > NOW() - INTERVAL '45 seconds'
		WHERE rl.success = FALSE
		  AND rl.ts > NOW() - INTERVAL '5 minutes'
		  AND rl.error_kind = ANY($1)
		  AND rl.credential_id IS NOT NULL
		  AND rl.outbound_model IS NOT NULL
		  AND pps.credential_id IS NULL
		GROUP BY rl.credential_id, COALESCE(rl.outbound_model, rl.client_model), COALESCE(rl.error_kind, 'unknown')
		ON CONFLICT (credential_id, raw_model_name, error_kind)
		DO UPDATE SET
		    consecutive_count     = passive_probe_state.consecutive_count + EXCLUDED.consecutive_count,
		    total_recent_count    = passive_probe_state.total_recent_count + EXCLUDED.total_recent_count,
		    first_seen_at         = LEAST(passive_probe_state.first_seen_at, EXCLUDED.first_seen_at),
		    last_seen_at          = NOW(),
		    last_response_body_preview = EXCLUDED.last_response_body_preview
	`, transientErrorKinds)
	if err != nil {
		slog.Warn("passive probe: poll errors failed", "error", err)
		return
	}

	// Step 2 (BUG-E fix): refresh window_total_count to reflect the REAL
	// total request count (success + failure) in the current 5-minute
	// window. This makes the error_rate ratio in reviewPromotion correct.
	_, err = l.db.Exec(timeout, `
		UPDATE passive_probe_state pps
		SET window_total_count = COALESCE(win.total, 0)
		FROM (
		    SELECT credential_id,
		           COALESCE(outbound_model, client_model) AS raw_model_name,
		           COUNT(*) AS total
		    FROM request_logs
		    WHERE ts > NOW() - INTERVAL '5 minutes'
		      AND credential_id IS NOT NULL
		      AND outbound_model IS NOT NULL
		    GROUP BY credential_id, COALESCE(outbound_model, client_model)
		) AS win
		WHERE pps.credential_id = win.credential_id
		  AND pps.raw_model_name = win.raw_model_name
	`)
	if err != nil {
		slog.Debug("passive probe: window_total refresh failed", "error", err)
	}
}

// reviewPromotion checks if any passive_probe_state entries should be
// promoted to reviewing (i.e. meet the secondary-verification rule).
//
// Rule 1 (consecutive): consecutive_count >= 3
//   - 3+ consecutive transient/timeout/network errors with no success in between.
//
// Rule 2 (error rate): window_total_count >= 5 AND error_rate >= 0.6
//   - At least 5 total requests in the 5-minute window with ≥60% error rate.
//   - BUG-E fix: window_total_count is now the REAL total (success+failure),
//     so the ratio is a genuine error rate, not error/error.
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
		        AND window_total_count > 0
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

// reviewResolution (BUG-A/B/C/D fix, 2026-06-22) resolves entries whose
// reviewing window has expired. This is the missing "active probe
// confirmation" step that was completely absent in v5.
//
// For each entry where in_reviewing=TRUE AND reviewing_until <= NOW():
//  1. Check if the (credential, model) pair still had failures during the
//     reviewing window (last 5 minutes). A success during the window means
//     the credential recovered → clear reviewing, reset counters.
//  2. If still failing → mark the credential availability_state='unreachable'
//     via credentialstate.Writer (2-minute cooling for KindNetwork), which
//     removes it from the routable candidate pool. credential_recovery.go
//     will auto-restore it to 'ready' after the cooling period.
//  3. Always clear in_reviewing=FALSE so the entry can be re-promoted if
//     failures continue.
func (l *PassiveProbeListener) reviewResolution(ctx context.Context) {
	timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Fetch entries whose reviewing window has expired.
	rows, err := l.db.Query(timeout, `
		SELECT credential_id, raw_model_name, error_kind, total_recent_count
		FROM passive_probe_state
		WHERE in_reviewing = TRUE
		  AND reviewing_until IS NOT NULL
		  AND reviewing_until <= NOW()
	`)
	if err != nil {
		slog.Warn("passive probe: resolution query failed", "error", err)
		return
	}
	defer rows.Close()

	type pending struct {
		credentialID int
		rawModel     string
		errorKind    string
		errCount     int
	}
	var toResolve []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.credentialID, &p.rawModel, &p.errorKind, &p.errCount); err != nil {
			continue
		}
		toResolve = append(toResolve, p)
	}
	rows.Close()

	if len(toResolve) == 0 {
		return
	}

	markedUnreachable := 0
	for _, p := range toResolve {
		// Check if there was at least one success for this (credential, model)
		// pair during the reviewing window. If so, the credential recovered.
		var successes int
		err := l.db.QueryRow(ctx, `
			SELECT COUNT(*) FROM request_logs
			WHERE credential_id = $1
			  AND COALESCE(outbound_model, client_model) = $2
			  AND success = TRUE
			  AND ts > NOW() - INTERVAL '5 minutes'
		`, p.credentialID, p.rawModel).Scan(&successes)
		if err != nil {
			slog.Debug("passive probe: success check failed",
				"credential_id", p.credentialID,
				"error", err)
		}

		if successes > 0 {
			// Credential recovered during the reviewing window.
			slog.Info("passive probe: credential recovered during review",
				"credential_id", p.credentialID,
				"raw_model", p.rawModel,
				"successes", successes)
		} else if p.errCount > 0 {
			// Still failing — mark unreachable (BUG-C/D fix).
			if l.stateWriter != nil && l.stateWriter.Enabled() {
				if err := l.stateWriter.WriteOnError(ctx, p.credentialID, p.rawModel, credentialstate.Failure{
					Kind:   errorsx.KindNetwork,
					Detail: fmt.Sprintf("passive_probe_review_failed: %s on %s (%d errors)", p.errorKind, p.rawModel, p.errCount),
				}); err != nil {
					slog.Warn("passive probe: unreachable write failed",
						"credential_id", p.credentialID,
						"error", err)
				} else {
					markedUnreachable++
					slog.Warn("passive probe: marked credential unreachable after review",
						"credential_id", p.credentialID,
						"raw_model", p.rawModel,
						"error_kind", p.errorKind,
						"error_count", p.errCount,
					)
				}
			}
		}
	}

	// BUG-B fix: always clear reviewing state + reset counters for resolved
	// entries, regardless of outcome. This prevents the permanent-stuck
	// bug where in_reviewing was set but never cleared.
	if len(toResolve) > 0 {
		_, err = l.db.Exec(ctx, `
			UPDATE passive_probe_state
			SET in_reviewing = FALSE,
			    reviewing_until = NULL,
			    final_marked_at = NOW(),
			    unavailable_reason = CASE WHEN in_reviewing = TRUE THEN 'reviewed' ELSE NULL END,
			    consecutive_count = 0,
			    total_recent_count = 0
			WHERE in_reviewing = TRUE
			  AND reviewing_until IS NOT NULL
			  AND reviewing_until <= NOW()
		`)
		if err != nil {
			slog.Warn("passive probe: resolution clear failed", "error", err)
		}
	}

	if markedUnreachable > 0 {
		slog.Info("passive probe: review resolution complete",
			"resolved", len(toResolve),
			"marked_unreachable", markedUnreachable,
		)
	}
}

// ReportRecentFailures returns a human-readable summary of what's in review.
func (l *PassiveProbeListener) ReportRecentFailures(ctx context.Context) (string, error) {
	return "", fmt.Errorf("not implemented")
}
