// Package modelcatalog implements direct writes to provider_models and
// credential_model_bindings, bypassing the model_offers view triggers.
package modelcatalog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UpsertCredentialModel inserts or updates one credential→model binding.
//
// Lifecycle rules on duplicate (credential_id, provider_model_id):
//   - Manually disabled bindings (available=false AND unavailable_reason LIKE 'manual%')
//     keep their availability flags unchanged.
//   - All other states (including legacy soft-delete reason='deleted') are re-enabled.
func UpsertCredentialModel(ctx context.Context, db *pgxpool.Pool, credentialID int, rawName, standardizedName string, canonicalID *int) error {
	if db == nil {
		return fmt.Errorf("database not configured")
	}
	_, err := db.Exec(ctx, upsertCredentialModelSQL, credentialID, rawName, standardizedName, canonicalID)
	return err
}

const upsertCredentialModelSQL = `
WITH cred AS (
    SELECT provider_id FROM credentials WHERE id = $1
),
upsert_pm AS (
    INSERT INTO provider_models (provider_id, raw_model_name, canonical_id, standardized_name, available, last_seen_at)
    SELECT cred.provider_id, $2, $4, $3, TRUE, NOW() FROM cred
    ON CONFLICT (provider_id, raw_model_name) DO UPDATE SET
        canonical_id = COALESCE(EXCLUDED.canonical_id, provider_models.canonical_id),
        standardized_name = COALESCE(EXCLUDED.standardized_name, provider_models.standardized_name),
        last_seen_at = NOW(),
        available = TRUE,
        updated_at = NOW()
    RETURNING id
)
INSERT INTO credential_model_bindings (
    credential_id, provider_model_id, available,
    routing_tier, weight, manual_priority,
    success_rate, p95_latency_ms
)
SELECT $1, upsert_pm.id, TRUE, 2, 100, 99, 0.9, 0 FROM upsert_pm
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
    updated_at = NOW(),
    available = CASE
        WHEN credential_model_bindings.available = FALSE
             AND (
                 -- Preserve manual disables
                 credential_model_bindings.unavailable_reason LIKE 'manual%'
                 -- Preserve admin-protected disables (e.g., from passive probe, model probe)
                 OR COALESCE(credential_model_bindings.admin_protected, FALSE) = TRUE
             )
        THEN credential_model_bindings.available
        ELSE TRUE
    END,
    unavailable_reason = CASE
        WHEN credential_model_bindings.available = FALSE
             AND (
                 credential_model_bindings.unavailable_reason LIKE 'manual%'
                 OR COALESCE(credential_model_bindings.admin_protected, FALSE) = TRUE
             )
        THEN credential_model_bindings.unavailable_reason
        ELSE NULL
    END,
    unavailable_at = CASE
        WHEN credential_model_bindings.available = FALSE
             AND (
                 credential_model_bindings.unavailable_reason LIKE 'manual%'
                 OR COALESCE(credential_model_bindings.admin_protected, FALSE) = TRUE
             )
        THEN credential_model_bindings.unavailable_at
        ELSE NULL
    END
`

// ClearProviderBindings hard-deletes all credential_model_bindings for a
// provider and removes orphan provider_models rows. This bypasses the
// model_offers view DELETE trigger which only soft-deletes (reason=deleted).
//
// All DELETE statements run in a single transaction so a failure on the
// orphan cleanup rolls back the bindings deletion. Without the transaction,
// a partial failure would leave the provider with no bindings but its
// provider_models rows still present, making the next list/fetch see stale
// model entries.
func ClearProviderBindings(ctx context.Context, db *pgxpool.Pool, providerID int) (bindingsDeleted int64, err error) {
	if db == nil {
		return 0, fmt.Errorf("database not configured")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	// Rollback is a no-op after a successful Commit.
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !strings.Contains(rbErr.Error(), "tx is closed") {
			slog.Warn("clear provider bindings rollback failed", "error", rbErr, "provider_id", providerID)
		}
	}()

	tag, err := tx.Exec(ctx, `
		DELETE FROM credential_model_bindings
		WHERE credential_id IN (
			SELECT id FROM credentials WHERE provider_id = $1
		)
	`, providerID)
	if err != nil {
		return 0, fmt.Errorf("delete bindings: %w", err)
	}
	bindingsDeleted = tag.RowsAffected()

	_, err = tx.Exec(ctx, `
		DELETE FROM provider_models pm
		WHERE pm.provider_id = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM credential_model_bindings cmb
		      WHERE cmb.provider_model_id = pm.id
		  )
	`, providerID)
	if err != nil {
		return bindingsDeleted, fmt.Errorf("delete orphan provider_models: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return bindingsDeleted, fmt.Errorf("commit: %w", err)
	}
	return bindingsDeleted, nil
}

// PreserveManualDisable reports whether an existing binding's disable state
// should survive a vendor re-fetch upsert.
//
// DEPRECATED: This function only checks unavailable_reason, not admin_protected.
// The SQL ON CONFLICT logic now handles both. Keep this for legacy callers but
// prefer checking both fields directly.
func PreserveManualDisable(available bool, unavailableReason *string) bool {
	if available || unavailableReason == nil {
		return false
	}
	return strings.HasPrefix(*unavailableReason, "manual")
}

// PreserveDisableState reports whether an existing binding's disable state
// should survive a vendor re-fetch upsert, checking both manual and admin-protected.
func PreserveDisableState(available bool, unavailableReason *string, adminProtected bool) bool {
	if available {
		return false
	}
	// Preserve if manually disabled OR admin-protected
	if adminProtected {
		return true
	}
	if unavailableReason != nil && strings.HasPrefix(*unavailableReason, "manual") {
		return true
	}
	return false
}
