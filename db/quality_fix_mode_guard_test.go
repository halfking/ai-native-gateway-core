package db

import (
	"os"
	"strings"
	"testing"
)

// TestEnsureQualityFixModeSchema_ShortCircuitsProvidersDDL pins the catalog
// guard added to ensureQualityFixModeSchema.
//
// Background (runbook §10.98): `ALTER TABLE … ADD COLUMN IF NOT EXISTS` does
// not spare the lock — PostgreSQL takes ACCESS EXCLUSIVE before it can check
// whether the column exists. Measured on production against providers:
//
//	calls 522 · mean 895 ms · worst single call 61,672 ms · 7.8 min cumulative
//
// for a statement that has not changed anything for months. columnsAllPresent
// is the repo's existing guard helper (db.go:189): it returns false when the
// table is missing, any column is missing, or the probe errors — i.e. it fails
// into the DDL, which is the safe direction.
func TestEnsureQualityFixModeSchema_ShortCircuitsProvidersDDL(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	text := string(src)

	fn := text[strings.Index(text, "func (d *DB) ensureQualityFixModeSchema("):]
	if end := strings.Index(fn[1:], "\n}\n"); end > 0 {
		fn = fn[:end+1]
	}

	t.Run("providers_alter_is_a_separate_constant", func(t *testing.T) {
		if !strings.Contains(text, "const qualityFixModeDDL = `") {
			t.Fatal("db.go should declare qualityFixModeDDL separately")
		}
		// The constant's closing backtick sits on the same line as its last SQL
		// statement (`…'fix'));`), so slicing to a "\n`" would run past the end of
		// the constant and into whatever follows it in db.go. Take the next
		// backtick instead — this SQL contains no nested quoting.
		constOpen := "const qualityFixModeDDL = `"
		ddl := text[strings.Index(text, constOpen)+len(constOpen):]
		if end := strings.Index(ddl, "`"); end >= 0 {
			ddl = ddl[:end]
		}
		// The trailing newline is load-bearing: without it, a rename to
		// `providers_disabled` would still satisfy Contains("ALTER TABLE
		// providers"). Mutation M97 proved that shape of check is escapable.
		if !strings.Contains(ddl, "ALTER TABLE providers\n") ||
			!strings.Contains(ddl, "ADD COLUMN IF NOT EXISTS quality_fix_mode") {
			t.Error("qualityFixModeDDL should still carry the providers ALTER")
		}
		// The rollup table must NOT be inside the guarded constant: skipping it
		// would leave a database that has the column but lacks the rollup table
		// permanently unfixed.
		if strings.Contains(ddl, "provider_quality_rollup") {
			t.Error("provider_quality_rollup must stay in the unguarded batch — " +
				"guarding it would skip the table creation entirely")
		}
	})

	t.Run("guard_uses_the_repo_helper_and_precedes_the_ddl", func(t *testing.T) {
		guardAt := strings.Index(fn, `d.columnsAllPresent(ctx, "providers", []string{"quality_fix_mode"})`)
		ddlAt := strings.Index(fn, "qualityFixModeDDL)")
		if guardAt < 0 {
			t.Fatal("the guard should use columnsAllPresent(ctx, \"providers\", …quality_fix_mode)")
		}
		if ddlAt < 0 {
			t.Fatal("EnsureMaasSchema-shaped: the DDL constant should be applied here")
		}
		if guardAt > ddlAt {
			t.Error("the catalog guard must run BEFORE the DDL is applied")
		}
		// ★ Position alone is not control flow. `if !false && d.columnsAllPresent(...)`
		// or `_ = d.columnsAllPresent(...)` still shows the guard before the DDL
		// while doing nothing.
		if !strings.Contains(fn, `if !d.columnsAllPresent(ctx, "providers", []string{"quality_fix_mode"}) {`) {
			t.Error("the guard's result must be consumed as the if condition — " +
				"a negated, disabled or discarded guard silently restores the lock")
		}
	})

	t.Run("guard_covers_the_column_that_the_ddl_creates", func(t *testing.T) {
		// A guard checking a different column would let a database missing
		// quality_fix_mode skip the DDL and never receive it.
		if !strings.Contains(fn, `"quality_fix_mode"`) {
			t.Error("the guard must check quality_fix_mode")
		}
		if strings.Contains(fn, "columnsAllPresent(ctx, \"provider_quality_rollup\"") {
			t.Error("the guard should check providers, not the rollup table")
		}
	})

	t.Run("rollup_batch_is_preserved", func(t *testing.T) {
		// Same boundary trap as above: `provider_quality_rollup_disabled`
		// contains `provider_quality_rollup`. The parenthesised forms pin the
		// identifier exactly.
		for _, want := range []string{
			"CREATE TABLE IF NOT EXISTS provider_quality_rollup (",
			"bad_requests",
			"fixed_requests",
			"ON provider_quality_rollup (bucket_start DESC)",
		} {
			if !strings.Contains(fn, want) {
				t.Errorf("EnsureQualityFixModeSchema must keep %q in the unguarded batch", want)
			}
		}
	})

	t.Run("still_wired_into_the_startup_sequence", func(t *testing.T) {
		if !strings.Contains(text, "ensureQualityFixModeSchema(") {
			t.Error("ensureQualityFixModeSchema must still be called from db.go")
		}
	})
}
