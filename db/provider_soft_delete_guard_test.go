package db

import (
	"os"
	"strings"
	"testing"
)

// TestEnsureProviderSoftDelete_ShortCircuitsProvidersDDL pins the catalog
// guard added to ensureProviderSoftDelete.
//
// Background (runbook §10.98.6): `ALTER TABLE … ADD COLUMN IF NOT EXISTS` does
// not spare the lock — PostgreSQL takes ACCESS EXCLUSIVE before it can check
// whether the column exists, and ACCESS EXCLUSIVE conflicts with the ACCESS
// SHARE that every routing read holds. providers carries ~10.66M reads/day
// (~479k of them sequential scans) in the shared production database, so an
// unconditional no-op ALTER + CREATE INDEX there stalls live routing traffic on
// every gateway boot.
//
// This is the third instance of the same shape already fixed for
// credits_charged (§10.97) and quality_fix_mode (§10.98.3).
func TestEnsureProviderSoftDelete_ShortCircuitsProvidersDDL(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	text := string(src)

	fn := text[strings.Index(text, "func (d *DB) ensureProviderSoftDelete("):]
	if end := strings.Index(fn[1:], "\n}\n"); end > 0 {
		fn = fn[:end+1]
	}

	t.Run("providers_ddl_is_a_separate_constant", func(t *testing.T) {
		constOpen := "const providerSoftDeleteDDL = `"
		at := strings.Index(text, constOpen)
		if at < 0 {
			t.Fatal("db.go should declare providerSoftDeleteDDL separately")
		}
		// Take the next backtick rather than slicing to "\n`": this SQL contains
		// no nested quoting, and the closing backtick sits on the same line as
		// the last statement.
		ddl := text[at+len(constOpen):]
		if end := strings.Index(ddl, "`"); end >= 0 {
			ddl = ddl[:end]
		}
		// ★ Every identifier assertion here is boundary-delimited. Mutation M97
		// (§10.98.4) proved that a bare Contains("…providers") is escapable by a
		// rename to `providers_disabled`, because the long name contains the
		// short one. The trailing newline / column list / parenthesis are what
		// make a rename fail the assertion.
		for _, want := range []string{
			"ALTER TABLE providers\n",
			"ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;",
			"CREATE INDEX IF NOT EXISTS idx_providers_live\n",
			"ON providers (id)",
			"WHERE deleted_at IS NULL;",
		} {
			if !strings.Contains(ddl, want) {
				t.Errorf("providerSoftDeleteDDL should carry %q", want)
			}
		}
		// The credentials CHECK-constraint block must NOT move into the guarded
		// constant. It carries its own pg_constraint definition guard; folding it
		// in here would be harmless today but would make the two guards
		// impossible to reason about independently.
		if strings.Contains(ddl, "credentials_status_check") {
			t.Error("the credentials constraint block must stay outside providerSoftDeleteDDL")
		}
	})

	t.Run("guard_precedes_the_ddl_and_its_result_drives_control_flow", func(t *testing.T) {
		guardAt := strings.Index(fn, "d.providerSoftDeleteCurrent(ctx)")
		ddlAt := strings.Index(fn, "providerSoftDeleteDDL)")
		if guardAt < 0 {
			t.Fatal("ensureProviderSoftDelete should consult providerSoftDeleteCurrent")
		}
		if ddlAt < 0 {
			t.Fatal("ensureProviderSoftDelete should apply providerSoftDeleteDDL")
		}
		if guardAt > ddlAt {
			t.Error("the catalog guard must run BEFORE the DDL is applied")
		}
		// ★ Position alone is not control flow. `if false && d.providerSoftDeleteCurrent(ctx)`
		// or `_ = d.providerSoftDeleteCurrent(ctx)` still places the guard before
		// the DDL while restoring the lock on every boot.
		if !strings.Contains(fn, "if !d.providerSoftDeleteCurrent(ctx) {") {
			t.Error("the guard's result must be consumed as the if condition — " +
				"a negated, disabled or discarded guard silently restores the lock")
		}
	})

	t.Run("guard_covers_column_and_index_the_ddl_creates", func(t *testing.T) {
		// A guard that checks only the column would skip the DDL on a database
		// that has deleted_at but never got idx_providers_live.
		probe := text[strings.Index(text, "func (d *DB) providerSoftDeleteCurrent("):]
		if end := strings.Index(probe[1:], "\n}\n"); end > 0 {
			probe = probe[:end+1]
		}
		for _, want := range []string{"deleted_at", "idx_providers_live"} {
			if !strings.Contains(probe, want) {
				t.Errorf("providerSoftDeleteCurrent must check %q", want)
			}
		}
		if !strings.Contains(probe, "information_schema.columns") ||
			!strings.Contains(probe, "pg_indexes") {
			t.Error("the probe must read both the column catalog and the index catalog")
		}
		// Fail-safe direction: a probe that errored must return false so the DDL
		// still runs. `return true` in the error branch would permanently skip
		// the schema on a database that needs it.
		// ★ The assertion pins the error branch specifically. A bare
		// Contains(probe, "return false") is satisfied by the nil/pool guard at
		// the top of the function, so it would stay green after the error branch
		// is flipped to `return true`.
		//
		// ★ Anchor on the slog message, not on the identifier: the Warn line is
		// `slog.Warn("… applying DDL", "error", err)` — there is no quote
		// character *before* `applying`, only the closing one after `applying
		// DDL`. Writing the anchor as `"applying DDL"` (with an opening quote)
		// silently never matches and turns this subtest permanently red.
		if !strings.Contains(probe, "applying DDL\", \"error\", err)\n\t\treturn false\n") {
			t.Error("the probe's error branch must return false so a failed probe " +
				"falls through to the DDL rather than skipping it")
		}
	})

	t.Run("credentials_constraint_batch_still_runs", func(t *testing.T) {
		// The credentials half of migration 631 has its own definition guard; it
		// must stay outside the providers guard so a current providers catalog
		// cannot suppress it.
		if !strings.Contains(fn, "credentials_status_check") {
			t.Fatal("ensureProviderSoftDelete must still carry the credentials CHECK-constraint batch")
		}
	})

	t.Run("still_wired_into_the_startup_sequence", func(t *testing.T) {
		// ★ Assert the call site, not the bare identifier. `_unused_ensureProviderSoftDelete(`
		// still contains the substring `ensureProviderSoftDelete(`, so a
		// Contains check over the whole file survives renaming the call away —
		// the same Contains-substring trap as §10.98.4's Contains("<对象名>").
		if !strings.Contains(text, "db.ensureProviderSoftDelete(migCtx)") {
			t.Error("ensureProviderSoftDelete must still be called from the startup sequence in db.go")
		}
	})
}
