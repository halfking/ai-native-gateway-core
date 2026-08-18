package startup

import (
	"strings"
	"testing"
)

// TestMigration541ScopeRevisionUpContract pins the up migration's structural
// invariants. The trigger must be created AFTER the backfill INSERT, otherwise
// the backfill would bump every scope by 1 (double-counting); the test asserts
// the textual ordering so a future refactor that swaps the steps fails CI
// before reaching production.
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
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision",
		"IF OLD.manual_priority",
		"AND OLD.provider_model_id = NEW.provider_model_id",
		"AND OLD.credential_id     = NEW.credential_id",
		"current_setting('app.actor', true)",
		"DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision ON public.credential_model_bindings",
		"AFTER INSERT OR UPDATE OF manual_priority, provider_model_id, credential_id",
		"OR DELETE",
		"FOR EACH ROW",
		"EXECUTE FUNCTION public.bump_candidate_binding_scope_revision()",
		"COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 541 up missing %q", want)
		}
	}

	backfillIdx := strings.Index(up, "INSERT INTO public.candidate_binding_scope_revision")
	triggerIdx := strings.Index(up, "CREATE TRIGGER trg_bump_cmb_scope_revision")
	if backfillIdx == -1 || triggerIdx == -1 {
		t.Fatalf("migration 541 up must contain both backfill INSERT and CREATE TRIGGER")
	}
	if backfillIdx > triggerIdx {
		t.Fatalf("migration 541 up backfill must run BEFORE CREATE TRIGGER (backfill idx=%d, trigger idx=%d)",
			backfillIdx, triggerIdx)
	}
}

// TestMigration541ScopeRevisionDownContract pins the down migration's
// teardown order: drop trigger before function before table, otherwise the
// DROP TABLE fails because the trigger still references the function.
func TestMigration541ScopeRevisionDownContract(t *testing.T) {
	down := string(migrationFile(t, "541_candidate_binding_scope_revision.down.sql"))
	for _, want := range []string{
		"DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision ON public.credential_model_bindings",
		"DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision()",
		"DROP TABLE IF EXISTS public.candidate_binding_scope_revision",
		"COMMIT;",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("migration 541 down missing %q", want)
		}
	}

	triggerIdx := strings.Index(down, "DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision")
	fnIdx := strings.Index(down, "DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision()")
	tableIdx := strings.Index(down, "DROP TABLE IF EXISTS public.candidate_binding_scope_revision")
	if triggerIdx == -1 || fnIdx == -1 || tableIdx == -1 {
		t.Fatalf("migration 541 down must contain trigger, function, and table drops")
	}
	if !(triggerIdx < fnIdx && fnIdx < tableIdx) {
		t.Fatalf("migration 541 down must drop trigger -> function -> table in order "+
			"(trigger=%d fn=%d table=%d)", triggerIdx, fnIdx, tableIdx)
	}
}
