package db

import (
	"os"
	"strings"
	"testing"
)

// TestEnsureProviderModelsCanonicalClearedAt_ShortCircuitsDDL pins the catalog
// guard added to ensureProviderModelsCanonicalClearedAt.
//
// Background (runbook §10.98.9): provider_models is the most heavily read table
// in the shared production database — ~12.48M reads/day (1.95M sequential +
// 10.53M index) against only 1,330 rows. The unguarded boot path took
// ACCESS EXCLUSIVE on it twice per start:
//
//	ALTER TABLE … ADD COLUMN IF NOT EXISTS  — needs the lock to decide whether
//	                                          the column exists
//	COMMENT ON COLUMN …                    — needs the lock to rewrite the
//	                                          catalog entry
//
// Production already carries both the column and the comment, so every boot
// locked the busiest table in the database twice to change nothing.
//
// Fifth instance of the same shape: credits_charged (§10.97),
// quality_fix_mode (§10.98.3), provider soft delete (§10.98.7), goal client
// signal (§10.98.8).
func TestEnsureProviderModelsCanonicalClearedAt_ShortCircuitsDDL(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	text := string(src)

	fn := text[strings.Index(text, "func (d *DB) ensureProviderModelsCanonicalClearedAt("):]
	if end := strings.Index(fn[1:], "\n}\n"); end > 0 {
		fn = fn[:end+1]
	}

	// ddlConst slices the guarded SQL out of the source. Anchors here were
	// copied from the file, not from memory: an anchor with an invented
	// delimiter matches nothing and leaves a permanently red gate (§10.98.8).
	ddlConst := func(t *testing.T) string {
		t.Helper()
		constOpen := "const providerModelsCanonicalClearedAtDDL = `"
		at := strings.Index(text, constOpen)
		if at < 0 {
			t.Fatal("db.go should declare providerModelsCanonicalClearedAtDDL separately")
		}
		// Start after the opening backtick: slicing from `at` finds that very
		// backtick first and collapses to the const header with no SQL in it.
		ddl := text[at+len(constOpen):]
		if end := strings.Index(ddl, "`"); end >= 0 {
			ddl = ddl[:end]
		}
		return ddl
	}

	t.Run("ddl_is_a_separate_constant_without_the_stamp", func(t *testing.T) {
		ddl := ddlConst(t)
		// ★ Boundary-delimited identifiers: §10.98.4's M97 proved a bare
		// Contains("<对象名>") is escapable — `provider_models_v2` contains
		// `provider_models`.
		for _, want := range []string{
			"ALTER TABLE public.provider_models\n",
			"ADD COLUMN IF NOT EXISTS canonical_cleared_at TIMESTAMPTZ;",
			"COMMENT ON COLUMN public.provider_models.canonical_cleared_at IS",
		} {
			if !strings.Contains(ddl, want) {
				t.Errorf("providerModelsCanonicalClearedAtDDL should carry %q", want)
			}
		}
		// The schema_migrations INSERT must stay in its own constant: it is an
		// idempotent stamp that has to run on every boot, including the boots
		// where the DDL is skipped.
		if strings.Contains(ddl, "schema_migrations") {
			t.Error("the schema_migrations stamp must not be inside the guarded DDL — " +
				"it is idempotent and must still run when the DDL is skipped")
		}
	})

	t.Run("guard_precedes_the_ddl_and_drives_control_flow", func(t *testing.T) {
		guardAt := strings.Index(fn, "d.providerModelsCanonicalClearedAtCurrent(ctx)")
		ddlAt := strings.Index(fn, "providerModelsCanonicalClearedAtDDL+")
		if guardAt < 0 {
			t.Fatal("ensureProviderModelsCanonicalClearedAt should consult providerModelsCanonicalClearedAtCurrent")
		}
		if ddlAt < 0 {
			t.Fatal("ensureProviderModelsCanonicalClearedAt should apply providerModelsCanonicalClearedAtDDL")
		}
		if guardAt > ddlAt {
			t.Error("the catalog guard must run BEFORE the DDL is applied")
		}
		// ★ Position alone is not control flow. `if false && …Current(ctx)` or a
		// discarded `_ = …Current(ctx)` still places the guard first while
		// locking the hottest table twice on every boot.
		if !strings.Contains(fn, "if d.providerModelsCanonicalClearedAtCurrent(ctx) {") {
			t.Error("the guard's result must be consumed as the if condition — " +
				"a disabled or discarded guard silently restores the lock")
		}
	})

	t.Run("guard_covers_the_comment_as_well_as_the_column", func(t *testing.T) {
		// Guarding only the ALTER would leave COMMENT ON COLUMN — same
		// ACCESS EXCLUSIVE — running unconditionally, so the guard would buy
		// nothing on the busiest table.
		probe := text[strings.Index(text, "func (d *DB) providerModelsCanonicalClearedAtCurrent("):]
		if end := strings.Index(probe[1:], "\n}\n"); end > 0 {
			probe = probe[:end+1]
		}
		for _, want := range []string{
			"information_schema.columns",
			"col_description(",
			"canonical_cleared_at",
		} {
			if !strings.Contains(probe, want) {
				t.Errorf("providerModelsCanonicalClearedAtCurrent must check %q", want)
			}
		}
		if !strings.Contains(probe, "col_description(a.attrelid, a.attnum) IS NOT NULL") {
			t.Error("the probe must require the column to already carry a comment")
		}
	})

	t.Run("probe_failure_falls_through_to_the_ddl", func(t *testing.T) {
		probe := text[strings.Index(text, "func (d *DB) providerModelsCanonicalClearedAtCurrent("):]
		if end := strings.Index(probe[1:], "\n}\n"); end > 0 {
			probe = probe[:end+1]
		}
		// Anchor copied from db.go, not from memory: the real line is
		// `slog.Warn("… applying DDL", "error", err)` — there is NO quote
		// character before `applying`, only the closing one after `applying DDL`.
		if !strings.Contains(probe, "applying DDL\", \"error\", err)\n\t\treturn false\n") {
			t.Error("a probe error must return false so the DDL still runs — " +
				"reading it as \"already current\" would skip the schema forever")
		}
	})

	t.Run("stamp_runs_on_both_paths", func(t *testing.T) {
		// Skipping the DDL must not skip the stamp.
		if !strings.Contains(fn, "providerModelsCanonicalClearedAtStamp") {
			t.Fatal("ensureProviderModelsCanonicalClearedAt must still write the migration stamp")
		}
		constOpen := "const providerModelsCanonicalClearedAtStamp = `"
		at := strings.Index(text, constOpen)
		if at < 0 {
			t.Fatal("db.go should declare providerModelsCanonicalClearedAtStamp separately")
		}
		// Start after the opening backtick, or the first backtick found is that
		// one and the slice collapses to the const header.
		stamp := text[at+len(constOpen):]
		if end := strings.Index(stamp, "`"); end >= 0 {
			stamp = stamp[:end]
		}
		for _, want := range []string{"schema_migrations", "693", "ON CONFLICT (version) DO NOTHING"} {
			if !strings.Contains(stamp, want) {
				t.Errorf("the stamp should still carry %q", want)
			}
		}
		// The guarded branch must also execute it, not just the DDL branch.
		//
		// ★ Bound the slice to the guard's own closing brace. Slicing to the end
		// of the function (mutation M125) lets the stamp's occurrence in the
		// *unguarded* path satisfy the assertion, so deleting it from the
		// guarded branch leaves this subtest green.
		startGuard := strings.Index(fn, "if d.providerModelsCanonicalClearedAtCurrent(ctx) {")
		if startGuard < 0 {
			t.Fatal("ensureProviderModelsCanonicalClearedAt should guard on providerModelsCanonicalClearedAtCurrent")
		}
		guardBranch := fn[startGuard:]
		if end := strings.Index(guardBranch, "\n\t}\n"); end > 0 {
			guardBranch = guardBranch[:end+1]
		}
		if !strings.Contains(guardBranch, "providerModelsCanonicalClearedAtStamp") {
			t.Error("the guarded (current) branch must still execute the migration stamp")
		}
	})

	t.Run("still_wired_into_the_startup_sequence", func(t *testing.T) {
		// ★ Assert the call site, not the bare identifier:
		// `_unused_ensureProviderModelsCanonicalClearedAt(` still contains the
		// substring `ensureProviderModelsCanonicalClearedAt(`.
		if !strings.Contains(text, "db.ensureProviderModelsCanonicalClearedAt(migCtx)") {
			t.Error("ensureProviderModelsCanonicalClearedAt must still be called from db.go's startup sequence")
		}
	})
}
