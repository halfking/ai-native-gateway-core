package startup

import (
	"os"
	"strings"
	"testing"
)

// TestMigration571CanonicalPriorityHashContract pins the up contract for
// migration 571: every canonical scope bump function must include priority
// in its hash and update predicate without adding schema changes.
func TestMigration571CanonicalPriorityHashContract(t *testing.T) {
	up := string(migrationFile(t, "571_candidate_binding_scope_revision_canonical_priority_hash.sql"))

	for _, want := range []string{
		"BEGIN;",
		"requires migration 569",
		"requires migration 568",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_insert()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_delete()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_update()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_pm_update()",
		"COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_update",
		"COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_pm_update",
		"COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 571 up missing %q", want)
		}
	}

	if got := strings.Count(up, "b.priority::text"); got < 4 {
		t.Errorf("migration 571 up: b.priority::text appears %d times, want >= 4", got)
	}
	norm := strings.Join(strings.Fields(up), " ")
	if got := strings.Count(norm, "OR o.priority IS DISTINCT FROM n.priority"); got < 4 {
		t.Errorf("migration 571 up: priority clause appears %d times, want >= 4", got)
	}
	if !strings.Contains(norm, "IF NEW.canonical_id IS DISTINCT FROM OLD.canonical_id THEN") {
		t.Errorf("migration 571 up: pm_update must guard on canonical_id change")
	}
	for _, forbidden := range []string{
		"CREATE TABLE", "ALTER TABLE", "DROP TABLE", "CREATE TRIGGER",
		"DROP TRIGGER", "CREATE INDEX", "DROP INDEX",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("migration 571 up must not contain %q", forbidden)
		}
	}
}

func TestMigration571UsesUnboundedNumericForTokenRatio(t *testing.T) {
	for _, path := range []string{
		"572_session_summary_large_token_ratio.sql",
		"../../objects/functions/update_session_summary.sql",
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "v_prompt_tokens::numeric / v_total_tokens::numeric") {
			t.Errorf("%s must use unbounded numeric token ratio", path)
		}
		if strings.Contains(text, "v_prompt_tokens::DECIMAL(10,6) / v_total_tokens::DECIMAL(10,6)") {
			t.Errorf("%s retains overflow-prone DECIMAL(10,6) token casts", path)
		}
	}
}

func TestMigration571DownRefusesUnsafeRollback(t *testing.T) {
	body, err := os.ReadFile("572_session_summary_large_token_ratio.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "cannot be rolled back safely") {
		t.Fatal("migration 571 down must refuse restoring overflow-prone token casts")
	}
}
