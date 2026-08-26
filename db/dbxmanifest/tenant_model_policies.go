package dbxmanifest

import "github.com/kaixuan/llm-gateway-go/internal/dbx"

// TenantModelPolicies is the Phase 3 pilot manifest for the tenant model
// denylist table (sql/migrations/startup/024_tenant_model_policies.sql).
//
// Pilot status: ACTIVATED in DefaultRegistry (shadow reads + metadata gate).
// No production data path routes through the framework; the existing
// repository (admin/model_policies.go) stays authoritative until the pilot
// graduates.
//
// Drift history (resolved 2026-08-27): migration 024 and the idempotent
// bootstrap in db/db.go declare `id BIGSERIAL PRIMARY KEY`, but production
// 252 carried no primary-key constraint on this table (nor on
// tenant_model_policies_audit, which additionally held duplicate ids) -
// environments bootstrapped from the dump-shaped baseline kept the drift
// alive because 024's CREATE TABLE IF NOT EXISTS short-circuits. Migrations
// 608 (pkey, pure DDL) and 609 (audit re-key + pkey) repair existing
// environments; the baseline (sql/schema/01-schema.sql,
// sql/objects/constraints/) now carries both pkeys so fresh installs match
// 024's declared shape. The dbx gate reported DriftPKMismatch (blocking)
// against the drifted shape and must stay green against the repaired one.
//
// Column contract notes:
//   - deleted_by stays writable for explicit patches, but the framework's
//     DeleteByPK only stamps deleted_at (deleted_by is left NULL); the
//     legacy handler writes the actor there. This difference is accepted
//     for the shadow-read pilot.
//   - Undelete (deleted_at = NULL) is outside current framework semantics:
//     the soft-delete column is framework-managed and not writable. The
//     legacy undelete path remains authoritative.
func TenantModelPolicies() dbx.TableManifest {
	soft := "deleted_at"
	touch := "updated_at"
	return dbx.TableManifest{
		Table:        "tenant_model_policies",
		TenantColumn: "tenant_id",
		PrimaryKey:   "id",
		Columns: []dbx.ColumnSpec{
			// id: BIGSERIAL, DB-generated, never written by callers.
			{Name: "id", Kind: dbx.KindInt},
			// tenant_id: VARCHAR(64) NOT NULL, scope-injected.
			{Name: "tenant_id", Kind: dbx.KindText},
			{Name: "canonical_name", Kind: dbx.KindText, Writable: true, Insertable: true},
			{Name: "reason", Kind: dbx.KindText, Writable: true, Insertable: true},
			{Name: "created_by", Kind: dbx.KindText, Writable: true, Insertable: true},
			// deleted_at is the framework-managed soft-delete column.
			{Name: "deleted_at", Kind: dbx.KindTimestamp, Nullable: true},
			{Name: "deleted_by", Kind: dbx.KindText, Nullable: true, Writable: true},
			{Name: "created_at", Kind: dbx.KindTimestamp},
			{Name: "updated_at", Kind: dbx.KindTimestamp},
		},
		SoftDelete:    &soft,
		TouchColumn:   &touch,
		MaxUpdateRows: 1,
	}
}
