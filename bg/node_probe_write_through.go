package bg

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// healthyBindingSQL restores one (credential, model) binding after a direct
// probe or business success. It only touches rows that are currently
// unavailable and never overrides manual / admin-pinned bindings, so the
// per-request call from the executor is a 0-row no-op on a healthy node.
func healthyBindingSQL() string {
	return `
		UPDATE credential_model_bindings cmb
		SET available = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at = NULL,
		    unavailable_recover_at = NULL,
		    probe_revert_at = NULL,
		    updated_at = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		  AND COALESCE(cmb.available, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE`
}

// healthyCredentialSQL flips the credential surfaces back to healthy/ready/ok.
// A real upstream success is newer evidence than any stored quota_state, so
// hard quota states are cleared too (same contract as writeHealth's 2026-08-07
// guard). The trailing predicate keeps the UPDATE a no-op when nothing needs
// to change — MarkNodeProbeHealthy runs on every successful request.
func healthyCredentialSQL() string {
	return `
		UPDATE credentials
		SET health_status = 'healthy',
		    health_error = NULL,
		    health_checked_at = now(),
		    availability_state = 'ready',
		    availability_recover_at = NULL,
		    quota_state = 'ok',
		    quota_recover_at = NULL,
		    state_reason_code = NULL,
		    state_reason_detail = NULL,
		    state_updated_at = now()
		WHERE id = $1
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND (
		      health_status IS DISTINCT FROM 'healthy'
		      OR availability_state IS DISTINCT FROM 'ready'
		      OR COALESCE(quota_state, 'ok') <> 'ok'
		      OR quota_recover_at IS NOT NULL
		      OR availability_recover_at IS NOT NULL
		  )`
}

func syncHealthyNodeSurfaces(ctx context.Context, db *pgxpool.Pool, credentialID int, rawModel string) error {
	if db == nil {
		return nil
	}
	if _, err := db.Exec(ctx, healthyBindingSQL(), credentialID, rawModel); err != nil {
		return err
	}
	_, err := db.Exec(ctx, healthyCredentialSQL(), credentialID)
	return err
}
