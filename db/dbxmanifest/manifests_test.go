package dbxmanifest

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/dbx"
)

func TestDefaultRegistryStaysEmpty(t *testing.T) {
	// The pilot must not leak into the default (production-shaped)
	// registry: no business code may reach the CRUD layer through it yet.
	reg, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry: %v", err)
	}
	if got := reg.Tables(); len(got) != 0 {
		t.Errorf("DefaultRegistry tables = %v, want empty until the pilot graduates", got)
	}
}

func TestPilotRegistryBuilds(t *testing.T) {
	reg, err := PilotRegistry()
	if err != nil {
		t.Fatalf("PilotRegistry: %v", err)
	}
	if got := reg.Tables(); len(got) != 1 || got[0] != "tenant_model_policies" {
		t.Fatalf("PilotRegistry tables = %v", got)
	}
}

func TestTenantModelPoliciesManifestContract(t *testing.T) {
	reg, err := PilotRegistry()
	if err != nil {
		t.Fatalf("PilotRegistry: %v", err)
	}
	m, err := reg.Lookup("tenant_model_policies")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if m.PrimaryKey != "id" || m.TenantColumn != "tenant_id" {
		t.Errorf("pk/tenant = %q/%q", m.PrimaryKey, m.TenantColumn)
	}
	if m.SoftDelete == nil || *m.SoftDelete != "deleted_at" {
		t.Error("soft delete column must be deleted_at")
	}
	if m.TouchColumn == nil || *m.TouchColumn != "updated_at" {
		t.Error("touch column must be updated_at")
	}
	if m.VersionColumn != nil {
		t.Error("table has no version column; manifest must not declare one")
	}
	// Tenant scope and audit columns are framework-protected.
	for _, protected := range []string{"id", "tenant_id", "deleted_at", "created_at", "updated_at"} {
		spec, ok := m.Column(protected)
		if !ok {
			t.Errorf("column %s missing from manifest", protected)
			continue
		}
		if spec.Writable || spec.Insertable {
			t.Errorf("column %s must not be writable/insertable", protected)
		}
	}
	// Business payload stays patchable.
	for _, writable := range []string{"canonical_name", "reason", "created_by", "deleted_by"} {
		spec, ok := m.Column(writable)
		if !ok {
			t.Errorf("column %s missing from manifest", writable)
			continue
		}
		if !spec.Writable {
			t.Errorf("column %s must stay writable", writable)
		}
	}
	// Insertable set mirrors the legacy INSERT column list.
	for _, insertable := range []string{"canonical_name", "reason", "created_by"} {
		spec, _ := m.Column(insertable)
		if !spec.Insertable {
			t.Errorf("column %s must be insertable", insertable)
		}
	}
	if spec, _ := m.Column("deleted_by"); spec.Insertable {
		t.Error("deleted_by must not be insertable (legacy INSERT omits it)")
	}
	// No JSONB columns: nothing to bound-check, but assert the kind set is
	// the intended small one.
	kinds := map[dbx.ColumnKind]bool{}
	for _, c := range m.Columns {
		kinds[c.Kind] = true
	}
	if kinds[dbx.KindJSONB] {
		t.Error("pilot table has no JSONB columns")
	}
}

// TestTenantModelPoliciesReconcilesWithMigrationDDL statically reconciles
// the manifest against the authoritative DDL so column renames or drops in
// a reviewed migration break this test instead of silently drifting.
func TestTenantModelPoliciesReconcilesWithMigrationDDL(t *testing.T) {
	raw, err := os.ReadFile("../../sql/migrations/startup/024_tenant_model_policies.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	ddl := string(raw)

	for _, fragment := range []string{
		"id              BIGSERIAL PRIMARY KEY",
		"tenant_id       VARCHAR(64) NOT NULL",
		"UNIQUE (tenant_id, canonical_name)",
		"deleted_at      TIMESTAMPTZ",
		"deleted_by      VARCHAR(128)",
		"created_at      TIMESTAMPTZ NOT NULL DEFAULT now()",
		"updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()",
		"canonical_name  TEXT NOT NULL",
		"reason          TEXT NOT NULL DEFAULT ''",
		"created_by      VARCHAR(128) NOT NULL DEFAULT ''",
		"CREATE POLICY tenant_isolation_tmp ON public.tenant_model_policies",
		"app.current_tenant",
	} {
		if !strings.Contains(ddl, fragment) {
			t.Errorf("migration DDL no longer contains %q", fragment)
		}
	}

	m := TenantModelPolicies()
	for _, c := range m.Columns {
		if !strings.Contains(ddl, c.Name) {
			t.Errorf("manifest column %q absent from migration DDL", c.Name)
		}
	}
}
