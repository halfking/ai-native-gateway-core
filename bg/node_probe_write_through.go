package bg

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE`
}

func healthyCredentialSQL() string {
	return `
		UPDATE credentials
		SET health_status = 'healthy',
		    health_error = NULL,
		    health_checked_at = now(),
		    availability_state = 'ready',
		    availability_recover_at = NULL,
		    quota_state = 'ok',
		    state_reason_code = NULL,
		    state_reason_detail = NULL,
		    state_updated_at = now()
		WHERE id = $1
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE`
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
