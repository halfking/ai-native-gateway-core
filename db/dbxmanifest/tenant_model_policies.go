package dbxmanifest

import "github.com/kaixuan/llm-gateway-go/internal/dbx"

// TenantModelPolicies is the Phase 3 pilot manifest for the tenant model
// denylist table (sql/migrations/startup/024_tenant_model_policies.sql).
//
// Pilot status: SHADOW-READ ONLY. It is deliberately NOT registered in
// DefaultRegistry and no production data path reaches it. The existing
// repository (admin/model_policies.go) stays authoritative.
//
// Known schema drift blocking activation: migration 024 and the idempotent
// bootstrap in db/db.go declare `id BIGSERIAL PRIMARY KEY`, but the
// production dump (sql/schema/01-schema.sql) shows no primary-key
// constraint on this table - only UNIQUE (tenant_id, canonical_name). The
// dbx metadata gate therefore reports DriftPKMismatch (blocking) against
// the dumped shape: UPDATE ... WHERE id = $1 has no single-column unique
// guarantee there. Activation requires a reviewed migration that restores
// the primary key; the framework never repairs schemas itself.
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

// PilotRegistry builds a registry containing only the pilot manifests for
// isolated tests and shadow reads. It must not be wired into request
// serving; DefaultRegistry stays empty until the pilot graduates.
func PilotRegistry() (*dbx.Registry, error) {
	return dbx.NewRegistry(TenantModelPolicies())
}
