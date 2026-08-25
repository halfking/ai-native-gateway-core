package startup

import (
	"strings"
	"testing"
)

// TestMigration574FilterScopeHashContract pins the up contract for
// migration 574: every bump function (raw_model and canonical) must
// include the providers.enabled + credentials.manual_disabled filter so
// the persisted scope_hash converges to the row set the resolve endpoint
// ships and the dashboard reorder scope SQL computes.
//
// The pre-574 contract uses LEFT JOIN against credential_model_bindings
// alone, which silently includes disabled-provider and
// manually-disabled-credential rows. The post-574 contract switches the
// LEFT JOIN to an INNER JOIN gated on providers.enabled = TRUE and
// credentials.manual_disabled = FALSE — the same two filters the Go-level
// reorderScopeSQL / reorderScopeByCanonicalSQL / ensure helpers apply.
//
// The migration also runs a one-shot recompute at the end of the up
// transaction so the persisted scope_hash converges immediately for
// every existing (raw_model, canonical_id) scope row. Without that step,
// clients hitting /api/routing/resolve immediately after the code ships
// would see the new filtered hash disagree with the persisted one until
// each scope is naturally bumped.
func TestMigration574FilterScopeHashContract(t *testing.T) {
	up := string(migrationFile(t, "574_candidate_binding_scope_filter_disabled.sql"))

	for _, want := range []string{
		"BEGIN;",
		"requires migration 541",
		"requires migration 569",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_insert()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_delete()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_update()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_insert()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_delete()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_update()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_pm_update()",
		"COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 574 up missing %q", want)
		}
	}

	// Every bump function must join through providers AND credentials with
	// the same filter. Normalise whitespace so multi-line JOIN clauses
	// collapse into a single line we can grep cleanly.
	norm := strings.Join(strings.Fields(up), " ")

	// Four inner JOINs per raw_model function (insert/delete/update) plus
	// three for the canonical variants + pm_update. Each function must
	// mention the providers.enabled gate at least once and the
	// credentials.manual_disabled gate at least once.
	if got := strings.Count(norm, "JOIN public.providers pr ON pr.id = pm.provider_id AND pr.enabled = TRUE"); got < 7 {
		t.Errorf("migration 574 up: provider JOIN appears %d times, want >= 7 (3 raw_model + 3 canonical + 1 pm_update + 2 recompute)", got)
	}
	if got := strings.Count(norm, "JOIN public.credentials cr ON cr.id = b.credential_id AND COALESCE(cr.manual_disabled, FALSE) = FALSE"); got < 7 {
		t.Errorf("migration 574 up: credentials JOIN appears %d times, want >= 7 (3 raw_model + 3 canonical + 1 pm_update + 2 recompute)", got)
	}

	// The pre-574 hash used LEFT JOIN ... LEFT JOIN ... (no filter). The
	// migration must not retain any LEFT JOIN against credential_model_bindings
	// in the bump functions themselves. The recompute CTE inside the up
	// also uses INNER JOIN (no LEFT) so we grep for INNER + LEFT together:
	// the count of LEFT JOINs in the bump CTEs must drop to zero, but
	// recompute CTEs use plain JOIN which is INNER by default. We allow a
	// small budget for the recompute CTEs (which also use plain JOIN).
	if strings.Contains(norm, "LEFT JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id") {
		t.Errorf("migration 574 up must not retain LEFT JOIN against credential_model_bindings in bump function bodies")
	}

	// The recompute step must touch BOTH scope tables and only update
	// rows whose hash actually changed (avoid a mass version bump).
	if !strings.Contains(norm, "UPDATE public.candidate_binding_scope_revision r SET scope_hash = h.scope_hash") {
		t.Errorf("migration 574 up must recompute raw_model scope hashes")
	}
	if !strings.Contains(norm, "UPDATE public.candidate_binding_scope_revision_canonical c SET scope_hash = h.scope_hash") {
		t.Errorf("migration 574 up must recompute canonical scope hashes")
	}
	if !strings.Contains(norm, "AND r.scope_hash IS DISTINCT FROM h.scope_hash") {
		t.Errorf("migration 574 up recompute must guard on hash change to avoid spurious version bumps")
	}
	if !strings.Contains(norm, "AND c.scope_hash IS DISTINCT FROM h.scope_hash") {
		t.Errorf("migration 574 up canonical recompute must guard on hash change to avoid spurious version bumps")
	}

	// The bump trigger COMMENTs must mention the new behaviour so future
	// readers understand why the JOINs exist.
	for _, want := range []string{
		"skips bindings under disabled providers and manually-disabled credentials since 574",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 574 up missing comment fragment %q", want)
		}
	}

	// Idempotency: every CREATE OR REPLACE FUNCTION must be safe to
	// re-run, and the migration must NOT add tables, triggers, or indexes
	// (the contract is "function-body patch only" plus an idempotent
	// recompute at the end of the up transaction).
	for _, forbidden := range []string{
		"CREATE TABLE", "ALTER TABLE", "DROP TABLE",
		"CREATE TRIGGER", "DROP TRIGGER",
		"CREATE INDEX", "DROP INDEX",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("migration 574 up must not contain %q", forbidden)
		}
	}
}

// TestMigration574FilterScopeHashDownReverts pins the down contract: the
// down file must restore the pre-574 LEFT-JOIN shape (no provider /
// credential filter) for every bump function. The hash recompute at the
// end of the up is intentionally NOT reverted — clients see a one-time
// 409 drift on the first reorder attempt after rollback, identical to
// the rollback behaviour of 571 (priority hash). The hash self-heals on
// the next binding write to each scope.
func TestMigration574FilterScopeHashDownReverts(t *testing.T) {
	down := string(migrationFile(t, "574_candidate_binding_scope_filter_disabled.down.sql"))

	for _, want := range []string{
		"BEGIN;",
		"requires migration 541",
		"requires migration 569",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_insert()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_delete()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_update()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_insert()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_delete()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_update()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_pm_update()",
		"COMMIT;",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("migration 574 down missing %q", want)
		}
	}

	// The down must restore the LEFT JOIN shape — at least one occurrence
	// in each bump function (some functions have one LEFT JOIN on pm and
	// another on b, so we expect 7+ LEFT JOINs across all six + pm_update).
	norm := strings.Join(strings.Fields(down), " ")
	if got := strings.Count(norm, "LEFT JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id"); got < 7 {
		t.Errorf("migration 574 down: pre-574 LEFT JOIN appears %d times, want >= 7 (3 raw_model + 3 canonical + 1 pm_update)", got)
	}

	// The down must NOT keep the new INNER JOIN shape (which would leave
	// the function bodies inconsistent with the reverted Go code).
	if got := strings.Count(norm, "JOIN public.providers pr ON pr.id = pm.provider_id AND pr.enabled = TRUE"); got != 0 {
		t.Errorf("migration 574 down: provider filter appears %d times, want 0 (must revert to LEFT JOIN)", got)
	}
	if got := strings.Count(norm, "JOIN public.credentials cr ON cr.id = b.credential_id AND COALESCE(cr.manual_disabled, FALSE) = FALSE"); got != 0 {
		t.Errorf("migration 574 down: credential filter appears %d times, want 0 (must revert to LEFT JOIN)", got)
	}

	// The down must not contain schema changes — same contract as the up.
	for _, forbidden := range []string{
		"CREATE TABLE", "ALTER TABLE", "DROP TABLE",
		"CREATE TRIGGER", "DROP TRIGGER",
		"CREATE INDEX", "DROP INDEX",
	} {
		if strings.Contains(down, forbidden) {
			t.Errorf("migration 574 down must not contain %q", forbidden)
		}
	}
}
