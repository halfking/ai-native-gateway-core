package startup

import (
	"strings"
	"testing"
)

// TestMigration541ScopeRevisionUpContract pins the up migration's structural
// invariants. The backfill must happen before triggers are attached, otherwise
// it would increment every newly seeded scope. Reorder itself is a multi-row
// UPDATE, so the implementation must be statement-level and recompute a full
// scope hash rather than leaving the hash of whichever row fired last.
func TestMigration541ScopeRevisionUpContract(t *testing.T) {
	up := string(migrationFile(t, "541_candidate_binding_scope_revision.sql"))
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS public.candidate_binding_scope_revision",
		"raw_model       text PRIMARY KEY",
		"scope_version   bigint NOT NULL DEFAULT 1",
		"scope_hash      char(64) NOT NULL DEFAULT ''",
		"last_bumped_at  timestamptz NOT NULL DEFAULT now()",
		"last_bumped_by  text",
		"INSERT INTO public.candidate_binding_scope_revision (raw_model, scope_version, scope_hash)",
		"LEFT JOIN public.credential_model_bindings b",
		"ON CONFLICT (raw_model) DO NOTHING",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_insert()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_update()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_delete()",
		"current_setting('app.actor', true)",
		"REFERENCING NEW TABLE AS new_rows",
		"REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows",
		"REFERENCING OLD TABLE AS old_rows",
		"FOR EACH STATEMENT",
		"CREATE TRIGGER trg_bump_cmb_scope_revision_insert",
		"CREATE TRIGGER trg_bump_cmb_scope_revision_update",
		"CREATE TRIGGER trg_bump_cmb_scope_revision_delete",
		"string_agg(",
		"GROUP BY a.raw_model",
		"COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 541 up missing %q", want)
		}
	}
	if strings.Contains(up, "REFERENCES public.provider_models(raw_model_name)") {
		t.Error("migration 541 must not reference non-unique provider_models(raw_model_name)")
	}

	backfillIdx := strings.Index(up, "INSERT INTO public.candidate_binding_scope_revision (raw_model, scope_version, scope_hash)")
	firstTriggerIdx := strings.Index(up, "CREATE TRIGGER trg_bump_cmb_scope_revision_insert")
	if backfillIdx == -1 || firstTriggerIdx == -1 {
		t.Fatalf("migration 541 up must contain both backfill INSERT and CREATE TRIGGER")
	}
	if backfillIdx > firstTriggerIdx {
		t.Fatalf("migration 541 up backfill must run BEFORE CREATE TRIGGER (backfill idx=%d, trigger idx=%d)",
			backfillIdx, firstTriggerIdx)
	}

	for _, name := range []string{
		"trg_bump_cmb_scope_revision_insert",
		"trg_bump_cmb_scope_revision_update",
		"trg_bump_cmb_scope_revision_delete",
	} {
		idx := strings.Index(up, "CREATE TRIGGER "+name)
		if idx == -1 || idx < strings.Index(up, "CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision") {
			t.Errorf("migration 541 must create %s after the bump functions", name)
		}
	}
}

// TestMigration541ScopeRevisionDownContract pins teardown order: all triggers
// must be removed before their functions, and all functions before the table.
func TestMigration541ScopeRevisionDownContract(t *testing.T) {
	down := string(migrationFile(t, "541_candidate_binding_scope_revision.down.sql"))
	triggerNames := []string{
		"trg_bump_cmb_scope_revision_insert",
		"trg_bump_cmb_scope_revision_update",
		"trg_bump_cmb_scope_revision_delete",
	}
	functionNames := []string{
		"public.bump_candidate_binding_scope_revision_insert()",
		"public.bump_candidate_binding_scope_revision_update()",
		"public.bump_candidate_binding_scope_revision_delete()",
	}
	for _, name := range triggerNames {
		if !strings.Contains(down, "DROP TRIGGER IF EXISTS "+name+" ON public.credential_model_bindings") {
			t.Errorf("migration 541 down missing trigger drop for %q", name)
		}
	}
	for _, name := range functionNames {
		if !strings.Contains(down, "DROP FUNCTION IF EXISTS "+name) {
			t.Errorf("migration 541 down missing function drop for %q", name)
		}
	}
	if !strings.Contains(down, "DROP TABLE IF EXISTS public.candidate_binding_scope_revision") {
		t.Error("migration 541 down missing revision table drop")
	}
	if !strings.Contains(down, "COMMIT;") {
		t.Error("migration 541 down missing COMMIT")
	}

	firstFunction := strings.Index(down, "DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision_insert()")
	tableIdx := strings.Index(down, "DROP TABLE IF EXISTS public.candidate_binding_scope_revision")
	if firstFunction == -1 || tableIdx == -1 {
		t.Fatal("migration 541 down must contain function and table drops")
	}
	for _, name := range triggerNames {
		idx := strings.Index(down, "DROP TRIGGER IF EXISTS "+name)
		if idx == -1 || idx > firstFunction {
			t.Errorf("migration 541 down must drop %s before functions", name)
		}
	}
	for _, name := range functionNames {
		idx := strings.Index(down, "DROP FUNCTION IF EXISTS "+name)
		if idx == -1 || idx > tableIdx {
			t.Errorf("migration 541 down must drop %s before table", name)
		}
	}
}
