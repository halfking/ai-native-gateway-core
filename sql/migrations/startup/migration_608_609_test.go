package startup

import (
	"os"
	"strings"
	"testing"
)

// migration608Body reads a migration file and strips `--` comments so pattern
// assertions target executable SQL, not the incident narrative in the header
// (same approach as TestMigration602AtomicPromote).
func migration608Body(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

func TestMigration608AddsPolicyPkeyWithGuards(t *testing.T) {
	body := migration608Body(t, "608_tenant_model_policies_add_pkey.sql")

	for _, required := range []string{
		"BEGIN;",
		"COMMIT;",
		"count(*) FILTER (WHERE id IS NULL)",
		"count(*) - count(DISTINCT id)",
		"RAISE EXCEPTION",
		"pg_constraint",
		"ADD CONSTRAINT tenant_model_policies_pkey PRIMARY KEY (id)",
		"indisprimary",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("migration 608 must contain %q (self-guard / idempotency / post-check contract)", required)
		}
	}

	// 608 is the pure-DDL half of the PK repair: it must never mutate rows.
	for _, forbidden := range []string{"INSERT INTO", "UPDATE ", "DELETE FROM"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("migration 608 must stay pure DDL, found %q", forbidden)
		}
	}
}

func TestMigration609RekeysAuditDuplicatesBeforePkey(t *testing.T) {
	body := migration608Body(t, "609_tenant_model_policies_audit_rekey_pkey.sql")

	for _, required := range []string{
		"BEGIN;",
		"COMMIT;",
		// NULL ids get sequence values first, otherwise the NULL partition
		// keeps one NULL row behind the dup re-key.
		"WHERE id IS NULL",
		// Dup re-key keeps the earliest row per id and re-issues the rest.
		"row_number() OVER (PARTITION BY id ORDER BY ts ASC, ctid)",
		"nextval('public.tenant_model_policies_audit_id_seq')",
		// Sequence catch-up before the PK, so trigger inserts cannot collide.
		"setval('public.tenant_model_policies_audit_id_seq'",
		"RAISE EXCEPTION",
		"pg_constraint",
		"ADD CONSTRAINT tenant_model_policies_audit_pkey PRIMARY KEY (id)",
		"indisprimary",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("migration 609 must contain %q (re-key / guard / idempotency contract)", required)
		}
	}

	// The re-key must not delete audit rows: append-only trail survives.
	if strings.Contains(body, "DELETE FROM") {
		t.Error("migration 609 must not delete audit rows (append-only audit trail)")
	}
}

func TestMigration608And609DownsAreIdempotent(t *testing.T) {
	cases := map[string]string{
		"608_tenant_model_policies_add_pkey.down.sql":         "tenant_model_policies_pkey",
		"609_tenant_model_policies_audit_rekey_pkey.down.sql": "tenant_model_policies_audit_pkey",
	}
	for name, constraint := range cases {
		body := migration608Body(t, name)
		if !strings.Contains(body, "DROP CONSTRAINT IF EXISTS "+constraint) {
			t.Errorf("%s must DROP CONSTRAINT IF EXISTS %s (idempotent down)", name, constraint)
		}
	}
}

// TestBaselineCarriesTenantModelPolicyPkeys locks the dump-derived baseline to
// migration 024's declared shape. Fresh installs bootstrap from
// sql/schema/01-schema.sql and then short-circuit 024's CREATE TABLE IF NOT
// EXISTS, so a baseline without the pkeys reproduces the very drift 608/609
// repair (see 608 header "根因").
func TestBaselineCarriesTenantModelPolicyPkeys(t *testing.T) {
	raw, err := os.ReadFile("../../schema/01-schema.sql")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	baseline := string(raw)

	for _, fragment := range []string{
		"ADD CONSTRAINT tenant_model_policies_pkey PRIMARY KEY (id)",
		"ADD CONSTRAINT tenant_model_policies_audit_pkey PRIMARY KEY (id)",
	} {
		if !strings.Contains(baseline, fragment) {
			t.Errorf("sql/schema/01-schema.sql must contain %q", fragment)
		}
	}

	for _, object := range []string{
		"../../objects/constraints/tenant_model_policies_tenant_model_policies_pkey.sql",
		"../../objects/constraints/tenant_model_policies_audit_tenant_model_policies_audit_pkey.sql",
	} {
		if _, err := os.Stat(object); err != nil {
			t.Errorf("missing object slice %s: %v", object, err)
		}
	}
}
