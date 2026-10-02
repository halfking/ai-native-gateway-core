// Package bg — integrity_fingerprint_probe.go (2026-09-25 audit round 8, D11)
//
// One-shot existence probe for the fingerprint drift worker's short-circuit.
// Kept in its own file on purpose: recent_surface_reads_test.go pins every
// request_logs-family FROM in integrity_fingerprint_drift.go to the
// _with_current_month view. That guard exists because the 7-day drift window
// is a recent-window READ; this probe is not a window read — it is an
// existence check whose answer must cover hot (not-yet-promoted) rows AND
// promoted rows, i.e. a superset of the view. Reading the view itself is not
// viable: the canonical view's anti-join arms made the probe exceed 55s on
// 252 (the same S1 pathology fixed for model_alternatives in round 7), so
// the probe unions the two raw surfaces instead, hot first (millisecond seq
// scan on 74k rows) and the bare parent only when hot found nothing
// (partition-pruned by the ts predicate to at most two monthly partitions,
// measured 12-28s — paid once per process).
package bg

import (
	"context"
)

// probeFingerprintTraffic reports whether any fingerprint-carrying row
// exists in the current half-window. ::int * INTERVAL keeps the
// SimpleProtocol inline literal typed (discipline: $n + INTERVAL must cast —
// see feature_stats_worker root fix, 252 audit round 2026-09-21).
func (w *IntegrityFingerprintDrift) probeFingerprintTraffic(ctx context.Context) (bool, error) {
	currentDays := w.days / 2
	if currentDays < 1 {
		currentDays = 1
	}
	// Hot arm: milliseconds, and when fingerprint traffic exists it is by
	// definition recent — short-circuit before paying the parent scan.
	var hotHasFP bool
	if err := w.db.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1
		      FROM request_logs_hot
		     WHERE ts > NOW() - ($1::int * INTERVAL '1 day')
		       AND system_fingerprint IS NOT NULL
		     LIMIT 1
		)`, currentDays).Scan(&hotHasFP); err != nil {
		return false, err
	}
	if hotHasFP {
		return true, nil
	}
	var parentHasFP bool
	err := w.db.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1
		      FROM request_logs -- sqlreadguard:allow D11 probe parent arm (hot arm checked first; existence superset of the view)
		     WHERE ts > NOW() - ($1::int * INTERVAL '1 day')
		       AND system_fingerprint IS NOT NULL
		     LIMIT 1
		)`, currentDays).Scan(&parentHasFP)
	return parentHasFP, err
}
