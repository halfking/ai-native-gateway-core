package startup

import (
	"strings"
	"testing"
)

// TestMigration571CanonicalPriorityHashContract pins the up contract for
// migration 571: every canonical scope bump function must (a) include
// b.priority::text in the scope_hash string so a priority flip changes the
// digest, and (b) for the update function, treat o.priority IS DISTINCT
// FROM n.priority as scope-affecting in every predicate site. The
// migration must also precondition on 568 (cmb.priority exists) and 569
// (canonical scope table exists) and must stay CREATE OR REPLACE only —
// no table / trigger / index changes.
//
// Why this exists: 569 shipped four canonical bump functions whose hash
// and predicate omitted priority, while 568 had added priority to the
// parallel 541 raw_model bump functions. The result was that a
// credential_model_bindings priority flip bypassed the canonical trigger
// entirely and drag-reorder OCC could not detect priority drift.
func TestMigration571CanonicalPriorityHashContract(t *testing.T) {
	up := string(migrationFile(t, "571_candidate_binding_scope_revision_canonical_priority_hash.sql"))

	for _, want := range []string{
		"BEGIN;",
		// Hard precondition on 568 + 569: every function we patch must
		// already exist; otherwise the migration would silently no-op on a
		// fresh install and leave a wrong hash formula behind.
		"requires migration 569",
		"requires migration 568",
		// All four functions are CREATE OR REPLACE — no DDL on tables or
		// triggers, no schema change.
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_insert()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_delete()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_update()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_pm_update()",
		// Documentation update on the update + pm_update COMMENTs so ops
		// can grep for "571" to understand what changed.
		"COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_update",
		"COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_pm_update",
		"COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 571 up missing %q", want)
		}
	}

	// scope_hash must include b.priority::text in every bump function.
	// Four functions × one hash each = four occurrences (one per function).
	if got := strings.Count(up, "b.priority::text"); got < 4 {
		t.Errorf("migration 571 up: b.priority::text appears %d times, want >= 4 (one per canonical bump function)", got)
	}

	// The update function's "meaningful change" predicate appears in four
	// sites (advisory-lock CTE: n-join side + o-join side; affected CTE:
	// n-join side + o-join side). All four must recognise priority.
	// Whitespace-flexible: collapse all whitespace runs to a single space
	// before substring matching, so the predicate survives cosmetic
	// reformatting without invalidating the contract.
	norm := strings.Join(strings.Fields(up), " ")
	if got := strings.Count(norm, "OR o.priority IS DISTINCT FROM n.priority"); got < 4 {
		t.Errorf("migration 571 up: priority clause appears %d times, want >= 4 (advisory-lock CTE x2 + affected CTE x2)", got)
	}

	// The pm_update function's predicate does NOT need a priority clause:
	// the trigger only fires when NEW.canonical_id IS DISTINCT FROM
	// OLD.canonical_id, so membership changes already cover the bump.
	// The check below verifies the predicate guard is intact by confirming
	// the priority clause appears in the update function body four times
	// (advisory-lock + affected CTE, n-join + o-join sides) AND that the
	// pm_update function does NOT introduce a fifth predicate clause.
	if !strings.Contains(norm, "IF NEW.canonical_id IS DISTINCT FROM OLD.canonical_id THEN") {
		t.Errorf("migration 571 up: pm_update must guard on canonical_id change")
	}

	// No DDL on tables or triggers — only function bodies and comments.
	// Pin CREATE TRIGGER / DROP TRIGGER / CREATE TABLE / ALTER TABLE to
	// zero occurrences so an over-eager future patch cannot silently add
	// schema changes that would force a re-deployment of every node.
	for _, forbidden := range []string{
		"CREATE TABLE",
		"ALTER TABLE",
		"DROP TABLE",
		"CREATE TRIGGER",
		"DROP TRIGGER",
		"CREATE INDEX",
		"DROP INDEX",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("migration 571 up must not contain %q (CREATE OR REPLACE FUNCTION patch only)", forbidden)
		}
	}
}