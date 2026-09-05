// Package modelcatalog implements direct writes to provider_models and
// credential_model_bindings, bypassing the model_offers view triggers.
package modelcatalog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the minimum database surface used by this package. Both
// *pgxpool.Pool and pgxmock.PgxPoolIface satisfy it, which lets the admin
// refresh tests drive the upsert path without a real database.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// UpsertCredentialModel inserts or updates one credential→model binding.
//
// Lifecycle rules on duplicate (credential_id, provider_model_id):
//   - Admin-protected bindings (admin_protected=TRUE, i.e. manually added)
//     are skipped entirely: batch/auto refresh must not update these records.
//   - Manually disabled bindings (available=false AND unavailable_reason LIKE 'manual%')
//     keep their availability flags unchanged.
//   - All other states (including legacy soft-delete reason='deleted') are re-enabled.
//
// 2026-07-14: enforces the gateway-wide case rule:
//   - rawName is the PROVIDER-facing name (e.g. NVIDIA NIM "z-ai/glm-5.2",
//     Meta "meta/llama-3.3-70b-instruct"). It is stored in
//     provider_models.raw_model_name unchanged.
//   - canonicalRawName is the CLIENT-facing lowercase key used by all
//     internal SQL matching; stored in provider_models.canonical_raw_name
//     and enforced UNIQUE per provider (migration 395).
//   - standardizedName is the historical provider-canonical column, also
//     stored lowercase; kept for back-compat with /models listings.
func UpsertCredentialModel(ctx context.Context, db Querier, credentialID int, rawName, canonicalRawName, standardizedName string, canonicalID *int) error {
	if db == nil {
		return fmt.Errorf("database not configured")
	}
	_, err := db.Exec(ctx, upsertCredentialModelSQL, credentialID, rawName, canonicalRawName, standardizedName, canonicalID)
	return err
}

// DeriveBillingMode maps a credential's plan_type to the cmb.billing_mode
// value that discovery should write. Mirrors the CASE WHEN in
// upsertCredentialModelSQL and migrations/136. Kept in sync so the Go-side
// rule is unit-testable without a database.
//
// token → per_token (legacy alias); everything else passes through.
func DeriveBillingMode(planType string) string {
	if planType == "" || planType == "token" {
		return "per_token"
	}
	return planType
}

const upsertCredentialModelSQL = `
WITH cred AS (
    SELECT provider_id, plan_type FROM credentials WHERE id = $1
),
upsert_pm AS (
    INSERT INTO provider_models (
        provider_id,
        raw_model_name,
        canonical_raw_name,
        canonical_id,
        standardized_name,
        available,
        source,
        last_seen_at
    )
    SELECT cred.provider_id, $2, $3, $5, $4, TRUE, 'discovery', NOW() FROM cred
    ON CONFLICT (provider_id, raw_model_name) DO UPDATE SET
        canonical_raw_name = COALESCE(EXCLUDED.canonical_raw_name, provider_models.canonical_raw_name),
        canonical_id = COALESCE(EXCLUDED.canonical_id, provider_models.canonical_id),
        standardized_name = COALESCE(EXCLUDED.standardized_name, provider_models.standardized_name),
        source = 'discovery',
        last_seen_at = NOW(),
        available = TRUE,
        updated_at = NOW()
    RETURNING id
)
INSERT INTO credential_model_bindings (
    credential_id, provider_model_id, available,
    routing_tier, weight, manual_priority,
    success_rate, p95_latency_ms,
    billing_mode, plan_type_origin
)
SELECT
    $1, upsert_pm.id, TRUE, 2, 100, 99, 0.9, 0,
    CASE WHEN cred.plan_type = 'token' THEN 'per_token' ELSE COALESCE(cred.plan_type, 'per_token') END,
    'auto'
FROM upsert_pm, cred
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
    -- Batch/auto refresh must not update admin-protected (manually-added)
    -- bindings at all: skip them entirely so even updated_at is untouched.
    WHERE COALESCE(credential_model_bindings.admin_protected, FALSE) = FALSE
`

// ManualInsertParams carries the fields for a manually-enrolled
// credential×model binding (admin UI "手工加入").
type ManualInsertParams struct {
	CredentialID      int
	RawName           string
	CanonicalRawName  string
	StandardizedName  string
	CanonicalID       *int
	OutboundModelName *string
	Available         bool
	ContextWindow     *int // binding-level override; nil = inherit catalog
}

// InsertManualCredentialModel upserts provider_models (source=manual) and
// credential_model_bindings with admin_protected=TRUE so discovery/refresh
// will not overwrite or expire the row.
//
// Returns the credential_model_bindings.id (model_offers.id).
func InsertManualCredentialModel(ctx context.Context, db Querier, p ManualInsertParams) (bindingID int64, err error) {
	if strings.TrimSpace(p.RawName) == "" {
		return 0, fmt.Errorf("raw_model_name required")
	}
	if db == nil {
		return 0, fmt.Errorf("database not configured")
	}
	err = db.QueryRow(ctx, insertManualCredentialModelSQL,
		p.CredentialID,
		p.RawName,
		p.CanonicalRawName,
		p.StandardizedName,
		p.CanonicalID,
		p.OutboundModelName,
		p.Available,
		p.ContextWindow,
	).Scan(&bindingID)
	return bindingID, err
}

const insertManualCredentialModelSQL = `
WITH cred AS (
    SELECT provider_id, plan_type FROM credentials WHERE id = $1
),
upsert_pm AS (
    INSERT INTO provider_models (
        provider_id,
        raw_model_name,
        canonical_raw_name,
        canonical_id,
        standardized_name,
        outbound_model_name,
        available,
        source,
        last_seen_at
    )
    SELECT cred.provider_id, $2, $3, $5, $4, $6, TRUE, 'manual', NOW() FROM cred
    ON CONFLICT (provider_id, raw_model_name) DO UPDATE SET
        canonical_raw_name = COALESCE(EXCLUDED.canonical_raw_name, provider_models.canonical_raw_name),
        canonical_id = COALESCE(EXCLUDED.canonical_id, provider_models.canonical_id),
        standardized_name = COALESCE(EXCLUDED.standardized_name, provider_models.standardized_name),
        outbound_model_name = COALESCE(EXCLUDED.outbound_model_name, provider_models.outbound_model_name),
        source = 'manual',
        last_seen_at = NOW(),
        available = TRUE,
        updated_at = NOW()
    RETURNING id
)
INSERT INTO credential_model_bindings (
    credential_id, provider_model_id, available,
    routing_tier, weight, manual_priority,
    success_rate, p95_latency_ms,
    billing_mode, plan_type_origin, admin_protected,
    context_window_override, context_window_source, context_window_updated_at
)
SELECT
    $1, upsert_pm.id, $7, 2, 100, 99, 0.9, 0,
    CASE WHEN cred.plan_type = 'token' THEN 'per_token' ELSE COALESCE(cred.plan_type, 'per_token') END,
    'manual', TRUE,
    $8,
    CASE WHEN $8 IS NOT NULL THEN 'manual' ELSE 'catalog' END,
    CASE WHEN $8 IS NOT NULL THEN NOW() ELSE NULL END
FROM upsert_pm, cred
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
    available = EXCLUDED.available,
    admin_protected = TRUE,
    context_window_override = COALESCE(EXCLUDED.context_window_override, credential_model_bindings.context_window_override),
    context_window_source = CASE
        WHEN EXCLUDED.context_window_override IS NOT NULL THEN 'manual'
        ELSE credential_model_bindings.context_window_source
    END,
    context_window_updated_at = CASE
        WHEN EXCLUDED.context_window_override IS NOT NULL THEN NOW()
        ELSE credential_model_bindings.context_window_updated_at
    END,
    updated_at = NOW()
RETURNING id
`

// ClearCredentialBindings hard-deletes bindings for one credential.
// When includeProtected is false, admin_protected rows are kept.
// Orphan provider_models for that credential's provider are cleaned up.
func ClearCredentialBindings(ctx context.Context, db *pgxpool.Pool, credentialID int, includeProtected bool) (bindingsDeleted int64, err error) {
	if db == nil {
		return 0, fmt.Errorf("database not configured")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !strings.Contains(rbErr.Error(), "tx is closed") {
			slog.Warn("clear credential bindings rollback failed", "error", rbErr, "credential_id", credentialID)
		}
	}()

	var providerID int
	if err := tx.QueryRow(ctx, `SELECT provider_id FROM credentials WHERE id = $1`, credentialID).Scan(&providerID); err != nil {
		return 0, fmt.Errorf("lookup credential: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		DELETE FROM credential_model_bindings
		WHERE credential_id = $1
		  AND ($2 OR COALESCE(admin_protected, FALSE) = FALSE)
	`, credentialID, includeProtected)
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

// DefaultProbeModelSourceRefreshLatest is the source label stamped on
// credentials.default_probe_model_source when AutoFillDefaultProbeModel
// picks a value. It is distinct from 'manual' (operator-pinned) and the
// shared_pick.go labels ('auto:request_log', 'auto:domestic_featured',
// 'auto:domestic_random') so daily repicks (DefaultProbePicker) can
// overwrite a refresh-latest pick with a more informed one (e.g. a
// request_log hot model) while still refusing to overwrite 'manual'.
const DefaultProbeModelSourceRefreshLatest = "auto:refresh_latest"

// AutoFillDefaultProbeModel seeds credentials.default_probe_model when the
// operator never set one. It picks the most recently FIRST-observed
// (provider_models.created_at DESC) routable binding under the
// credential and writes its provider-facing name
// (COALESCE(outbound_model_name, raw_model_name)) into default_probe_model.
//
// Why "newest created_at" and not "newest last_seen_at":
//   - last_seen_at is bumped on EVERY refresh, so it converges across
//     the whole cmb set the moment a refresh completes — useless for
//     ranking.
//   - created_at is set ONCE at first INSERT and only changes when the
//     row is dropped/recreated (which ClearCredentialBindings handles
//     explicitly). It is the canonical "this model was added at time T"
//     timestamp and matches the user-intent reading of "最新的模型".
//
// Why outbound/raw and NOT standardized_name (2026-08-31 round-6 audit
// correction): default_probe_model is sent verbatim to the upstream in
// the probe chat request (credential_probe_v2.probeCredential), so it
// must be a name the upstream accepts. standardized_name is the
// client-facing normalized key (prefix-stripped, lowercased) — for
// NIM-style vendors whose raw name carries a vendor prefix
// ("z-ai/glm-5.2"), storing "glm-5.2" would 404 every probe. This
// matches the established picker convention in bg/shared_pick.go
// (COALESCE(pm.outbound_model_name, pm.raw_model_name)), per
// .handoff/selfcheck-audit-2026-08-26.md §2.2.
//
// 2026-08-31 hzx-2 round-4: closes the "credential with no
// default_probe_model never gets probed" gap exposed by PeriodicQuotaProbe
// (see bg/periodic_quota_probe.go). Without this hook the per-model
// upsert loop populates cmb but leaves default_probe_model empty, so the
// credential is silently skipped by the periodic / balance / fast probe
// paths even though it has routable bindings.
//
// Guards:
//   - default_probe_model is NULL/empty: never overwrite an existing
//     pick. Operators who set a manual pin keep their pin.
//   - default_probe_model_source <> 'manual': defensive — even if
//     default_probe_model happens to be empty, never stomp a manual
//     source marker (the admin path writes both atomically but the
//     ordering is not guaranteed during a migration).
//   - The pick only runs when the credential has at least one routable
//     binding (cmb.available = TRUE AND pm.available = TRUE) — empty
//     cmb is a no-op.
//
// Returns the model name actually written (may be empty when no eligible
// pick was found), so the caller can log success vs skip.
func AutoFillDefaultProbeModel(ctx context.Context, db Querier, credentialID int) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database not configured")
	}
	if credentialID <= 0 {
		return "", nil
	}
	var picked string
	err := db.QueryRow(ctx, `
		UPDATE credentials c
		SET default_probe_model = sub.probe_model,
		    default_probe_model_source = $2,
		    default_probe_model_picked_at = NOW(),
		    state_updated_at = NOW()
		FROM (
		    SELECT COALESCE(pm.outbound_model_name, pm.raw_model_name) AS probe_model
		    FROM credential_model_bindings cmb
		    JOIN provider_models pm ON pm.id = cmb.provider_model_id
		    WHERE cmb.credential_id = $1
		      AND COALESCE(cmb.available, FALSE) = TRUE
		      AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		      AND COALESCE(pm.available, FALSE) = TRUE
		    ORDER BY pm.created_at DESC, pm.id DESC
		    LIMIT 1
		) sub
		WHERE c.id = $1
		  AND (c.default_probe_model IS NULL OR c.default_probe_model = '')
		  AND COALESCE(c.default_probe_model_source, '') <> 'manual'
		RETURNING sub.probe_model
	`, credentialID, DefaultProbeModelSourceRefreshLatest).Scan(&picked)
	if err != nil {
		// pgx returns ErrNoRows when the UPDATE matched zero rows —
		// that's the expected "already pinned / no eligible binding"
		// outcome and should not surface as an error to the caller.
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return picked, nil
}
