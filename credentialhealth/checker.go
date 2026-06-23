package credentialhealth

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Checker detects continuous failures and marks credentials as degraded.
type Checker struct {
	recorder         *Recorder
	db               DBQuerier
	windowDuration   time.Duration // default 1 hour
	failureThreshold float64       // default 0.80 (80%)
	minSampleSize    int           // default 5
	degradedCooldown time.Duration // default 2 hours
	enableCheck      bool          // feature flag
	invalidateCache  func()        // candidate-cache invalidator (nil → no-op)
}

// CheckerConfig holds checker configuration.
type CheckerConfig struct {
	WindowDuration   time.Duration
	FailureThreshold float64
	MinSampleSize    int
	DegradedCooldown time.Duration
	EnableCheck      bool
	// InvalidateCandidateCache (optional) is invoked synchronously after a
	// successful state change so the routing layer picks up the new
	// (cred, model) availability on the very next request — no waiting
	// for the 30s availableModelsCache TTL. nil → no-op.
	InvalidateCandidateCache func()
}

// DefaultCheckerConfig returns sensible defaults.
func DefaultCheckerConfig() CheckerConfig {
	return CheckerConfig{
		WindowDuration:   1 * time.Hour,
		FailureThreshold: 0.80, // 80% failure rate
		MinSampleSize:    5,
		DegradedCooldown: 2 * time.Hour,
		EnableCheck:      true,
	}
}

// NewChecker creates a continuous failure checker.
func NewChecker(recorder *Recorder, db DBQuerier, cfg CheckerConfig) *Checker {
	return &Checker{
		recorder:         recorder,
		db:               db,
		windowDuration:   cfg.WindowDuration,
		failureThreshold: cfg.FailureThreshold,
		minSampleSize:    cfg.MinSampleSize,
		degradedCooldown: cfg.DegradedCooldown,
		enableCheck:      cfg.EnableCheck,
		invalidateCache:  cfg.InvalidateCandidateCache,
	}
}

// Enabled returns true if checking is enabled.
func (c *Checker) Enabled() bool {
	return c != nil && c.recorder != nil && c.db != nil && c.enableCheck
}

// CheckAndUpdate analyzes recent call history and marks credential as degraded if needed.
func (c *Checker) CheckAndUpdate(ctx context.Context, credentialID int, model string) error {
	if !c.Enabled() {
		return nil
	}

	// Get recent entries within window
	since := time.Now().Add(-c.windowDuration)
	entries, err := c.recorder.GetRecent(ctx, credentialID, model, since)
	if err != nil {
		return fmt.Errorf("get recent entries: %w", err)
	}

	// Check sample size
	if len(entries) < c.minSampleSize {
		return nil // not enough data
	}

	// Compute stats (exclude network errors)
	var total, failed int
	errorKinds := make(map[string]int)

	for _, e := range entries {
		// Skip network errors (transient, not credential issue)
		if e.ErrorKind == "network" {
			continue
		}
		total++
		if !e.Success {
			failed++
			if e.ErrorKind != "" {
				errorKinds[e.ErrorKind]++
			}
		}
	}

	if total < c.minSampleSize {
		return nil // not enough non-network samples
	}

	failureRate := float64(failed) / float64(total)

	// Check threshold
	if failureRate < c.failureThreshold {
		return nil // below threshold, credential is healthy
	}

	// Mark as degraded
	return c.markDegraded(ctx, credentialID, model, failureRate, errorKinds, total)
}

// markDegraded updates the (credential, model) binding to unavailable.
//
// Why the credential_model_bindings row and not credentials.availability_state:
// v_routable_credential_models.is_routable reads cmb.available, so writing
// to credentials.availability_state alone has zero effect on routing — the
// binding stays routable while the admin UI shows the credential as
// "degraded", and a single bad model is enough to flip the whole credential.
// Updating the specific (credential_id, raw_model_name) row keeps sibling
// models on the same credential routable (per the 2026-06-22 audit on
// cross-model collateral damage).
func (c *Checker) markDegraded(ctx context.Context, credentialID int, model string, rate float64, kinds map[string]int, sampleSize int) error {
	recoverAt := time.Now().Add(c.degradedCooldown)

	tag, err := c.db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available          = FALSE,
		    unavailable_reason = 'continuous_failure',
		    unavailable_at     = now(),
		    unavailable_recover_at = $3,
		    updated_at         = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND COALESCE(pm.outbound_model_name, pm.raw_model_name) = $2
		  AND cmb.available = TRUE
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
	`, credentialID, model, recoverAt)
	if err != nil {
		return fmt.Errorf("update credential_model_bindings: %w", err)
	}

	if tag.RowsAffected() > 0 {
		// Mirror to model_offers so /api/routing/resolve ("test route")
		// surfaces the same unavailability — admin UI and production
		// routing must stay in lock-step.
		if _, moErr := c.db.Exec(ctx, `
			UPDATE model_offers mo
			SET available              = FALSE,
			    unavailable_reason     = 'continuous_failure',
			    unavailable_at         = now(),
			    unavailable_recover_at = $3
			FROM provider_models pm
			WHERE pm.raw_model_name = mo.raw_model_name
			  AND pm.id IN (
			      SELECT cmb.provider_model_id
			      FROM credential_model_bindings cmb
			      WHERE cmb.credential_id = $1
			        AND cmb.unavailable_reason = 'continuous_failure'
			        AND cmb.unavailable_at    = $2
			  )
			  AND mo.credential_id = $1
			  AND mo.available = TRUE
			  AND COALESCE(mo.admin_protected, FALSE) = FALSE
		`, credentialID, recoverAt, recoverAt); moErr != nil {
			slog.Warn("checker: model_offers mirror write failed",
				"credential_id", credentialID, "model", model, "error", moErr)
		}

		if c.invalidateCache != nil {
			c.invalidateCache()
		}
	}

	slog.Warn("credential binding marked degraded due to continuous failures",
		"credential_id", credentialID,
		"model", model,
		"failure_rate", rate,
		"sample_size", sampleSize,
		"error_kinds", kinds,
		"recover_at", recoverAt,
		"window", c.windowDuration,
		"rows_affected", tag.RowsAffected())

	return nil
}

// RecoverExpired checks for expired degraded credentials and restores them.
// This is called by the background health_auto_recover worker.
//
// It restores THREE state surfaces in the same call:
//  1. credential_model_bindings  (production router's source of truth)
//  2. model_offers               (/api/routing/resolve "test route" + admin UI)
//  3. credentials.availability_state  (the candidate loader's v_routable filter)
//
// 2026-06-23 (PR-3 T3): also clear credentials.availability_state in the same
// tick. Without this, a credential whose availability_recover_at has passed
// would still show is_routable=FALSE in the candidate loader for up to 60s
// (until the next bg/credential_recovery.go tick), producing the
// "cmb=TRUE but availability=cooling" false negative that hid the cred-11/
// minimax-m3 incident. Mirrors the SQL in bg/credential_recovery.go:recover()
// for defence-in-depth: if either worker tick is delayed, the other still
// covers the recovery.
//
// Historical note: the previous version of this function deliberately did
// NOT touch availability_state, on the assumption that the router reads
// only cmb. That assumption was wrong — v_routable_credential_models.is_routable
// also requires availability_state='ready' (see 2026-06-22 defect 4).
func RecoverExpired(ctx context.Context, db DBQuerier) (int, error) {
	cmbTag, err := db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available              = TRUE,
		    unavailable_reason     = NULL,
		    unavailable_at         = NULL,
		    unavailable_recover_at = NULL,
		    updated_at             = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.available = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND cmb.unavailable_reason <> 'model_probe_broken'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_recover_at,
		               cmb.unavailable_at + INTERVAL '30 seconds') < now()
	`)
	if err != nil {
		return 0, fmt.Errorf("recover expired credential_model_bindings: %w", err)
	}

	// Mirror to model_offers in the same tick so /api/routing/resolve
	// reflects the recovery immediately. Skip rows that are admin-pinned
	// (cmb.unavailable_reason LIKE 'manual%' was preserved on the cmb
	// side, so any model_offers row with reason LIKE 'manual%' is also
	// pinned here).
	//
	// NOTE: model_offers is a VIEW, so it doesn't have an updated_at column.
	// The underlying credential_model_bindings.updated_at was already set above.
	moTag, err := db.Exec(ctx, `
		UPDATE model_offers mo
		SET available          = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at     = NULL
		WHERE mo.available = FALSE
		  AND COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%'
		  AND mo.unavailable_reason <> 'model_probe_broken'
		  AND COALESCE(mo.admin_protected, FALSE) = FALSE
		  AND COALESCE(mo.unavailable_at + INTERVAL '30 seconds', now() + INTERVAL '1 hour') < now()
	`)
	if err != nil {
		return int(cmbTag.RowsAffected()), fmt.Errorf("recover expired model_offers: %w", err)
	}

	// ALSO clear credentials.availability_state when its recover_at has
	// passed. Same broken_confirmed guard as bg/credential_recovery.go:
	// do not auto-restore a credential that the probe worker has marked
	// permanently broken — the probe worker only un-marks a binding via
	// a manual nudge (TriggerManual), so guarding here cannot strand a
	// credential that has actually recovered.
	credTag, err := db.Exec(ctx, `
		UPDATE credentials
		SET availability_state      = 'ready',
		    availability_recover_at = NULL,
		    state_updated_at        = now()
		WHERE availability_state IN ('cooling','rate_limited','unreachable')
		  AND availability_recover_at IS NOT NULL
		  AND availability_recover_at <= now()
		  AND lifecycle_status = 'active'
		  AND NOT EXISTS (
		      SELECT 1
		      FROM model_probe_state mps
		      JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
		      JOIN credential_model_bindings cmb
		           ON cmb.credential_id = mps.credential_id
		          AND cmb.provider_model_id = pm.id
		      WHERE mps.credential_id = credentials.id
		        AND mps.state = 'broken_confirmed'
		        AND cmb.available = FALSE
		  )
	`)
	if err != nil {
		slog.Warn("availability_state recovery in RecoverExpired failed", "error", err)
	}

	rowsAffected := int(cmbTag.RowsAffected())
	if rowsAffected > 0 {
		slog.Info("auto recovered expired bindings",
			"cmb_count", cmbTag.RowsAffected(),
			"model_offers_count", moTag.RowsAffected(),
			"credentials_availability_count", credTag.RowsAffected(),
		)
	}

	return rowsAffected, nil
}
