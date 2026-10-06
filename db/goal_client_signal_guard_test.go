package db

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestEnsureGoalClientSignalSchema_ShortCircuitsSessionSummariesDDL pins the
// catalog guard added to ensureGoalClientSignalSchema.
//
// Background (runbook §10.98.8): the DDL was unguarded on every boot.
// Measured on production, session_summaries carries 465 MB of heap behind 21
// indexes, ~198k reads/day and ~58k writes/day; `ALTER TABLE … ADD COLUMN IF
// NOT EXISTS` takes ACCESS EXCLUSIVE on the table *and every one of its
// indexes* before it can decide the columns are already there. All six
// goal_sessions columns, both session_summaries columns and
// idx_session_summaries_parent are already present, so each boot locked 22
// relations to change nothing.
//
// Same shape as the three guards already fixed: credits_charged (§10.97),
// quality_fix_mode (§10.98.3), provider soft delete (§10.98.7).
func TestEnsureGoalClientSignalSchema_ShortCircuitsSessionSummariesDDL(t *testing.T) {
	src, err := os.ReadFile("goal_client_signal_schema.go")
	if err != nil {
		t.Fatalf("read goal_client_signal_schema.go: %v", err)
	}
	text := string(src)

	fn := text[strings.Index(text, "func (d *DB) ensureGoalClientSignalSchema("):]
	if end := strings.Index(fn[1:], "\n}\n"); end > 0 {
		fn = fn[:end+1]
	}

	t.Run("ddl_is_a_separate_constant", func(t *testing.T) {
		constOpen := "const goalClientSignalDDL = `"
		at := strings.Index(text, constOpen)
		if at < 0 {
			t.Fatal("goal_client_signal_schema.go should declare goalClientSignalDDL separately")
		}
		// Take the next backtick rather than slicing to "\n`": the SQL contains
		// no nested quoting, and the closing backtick shares a line with the last
		// statement.
		ddl := text[at+len(constOpen):]
		if end := strings.Index(ddl, "`"); end >= 0 {
			ddl = ddl[:end]
		}
		// ★ Every identifier assertion is boundary-delimited — §10.98.4's M97
		// proved a bare Contains("<对象名>") is escapable by a rename to
		// `<对象名>_disabled`, because the long name contains the short one.
		for _, want := range []string{
			"ALTER TABLE public.goal_sessions\n",
			"ALTER TABLE public.session_summaries\n",
			"ADD COLUMN IF NOT EXISTS continue_attempt",
			"ADD COLUMN IF NOT EXISTS last_completion_judgement",
			"ADD COLUMN IF NOT EXISTS sub_agents_total",
			"ADD COLUMN IF NOT EXISTS sub_agents_completed",
			"ADD COLUMN IF NOT EXISTS sub_agents_pending",
			"ADD COLUMN IF NOT EXISTS last_sub_agents_report_at",
			"ADD COLUMN IF NOT EXISTS parent_session_key",
			"ADD COLUMN IF NOT EXISTS handoff_reason",
			"CREATE INDEX IF NOT EXISTS idx_session_summaries_parent\n",
			"ON public.session_summaries(tenant_id, parent_session_key)",
		} {
			if !strings.Contains(ddl, want) {
				t.Errorf("goalClientSignalDDL should carry %q", want)
			}
		}
	})

	t.Run("guard_precedes_the_ddl_and_drives_control_flow", func(t *testing.T) {
		guardAt := strings.Index(fn, "d.goalClientSignalCurrent(ctx)")
		ddlAt := strings.Index(fn, "goalClientSignalDDL)")
		if guardAt < 0 {
			t.Fatal("ensureGoalClientSignalSchema should consult goalClientSignalCurrent")
		}
		if ddlAt < 0 {
			t.Fatal("ensureGoalClientSignalSchema should apply goalClientSignalDDL")
		}
		if guardAt > ddlAt {
			t.Error("the catalog guard must run BEFORE the DDL is applied")
		}
		// ★ Position alone is not control flow. `if false && d.goalClientSignalCurrent(ctx)`
		// or `_ = d.goalClientSignalCurrent(ctx)` still puts the guard before the
		// DDL while locking 22 relations on every boot.
		if !strings.Contains(fn, "if d.goalClientSignalCurrent(ctx) {") {
			t.Error("the guard's result must be consumed as the if condition — " +
				"a disabled or discarded guard silently restores the lock")
		}
	})

	t.Run("guard_covers_both_tables_and_the_index", func(t *testing.T) {
		// A guard that checked only the columns would let a database missing
		// idx_session_summaries_parent skip the DDL and never get the index.
		probe := text[strings.Index(text, "func (d *DB) goalClientSignalCurrent("):]
		if end := strings.Index(probe[1:], "\n}\n"); end > 0 {
			probe = probe[:end+1]
		}
		for _, want := range []string{
			`d.columnsAllPresent(ctx, "goal_sessions", goalClientSignalColumns)`,
			`d.columnsAllPresent(ctx, "session_summaries", goalClientSignalSummaryColumns)`,
			"pg_indexes",
			// ★ Boundary-delimited: a bare Contains("idx_session_summaries_parent")
			// is satisfied by `idx_session_summaries_parent_v2` (§10.98.4's
			// Contains-substring trap).
			"indexname='idx_session_summaries_parent'",
		} {
			if !strings.Contains(probe, want) {
				t.Errorf("goalClientSignalCurrent must cover %q", want)
			}
		}
	})

	t.Run("column_lists_match_the_ddl_exactly", func(t *testing.T) {
		// A column dropped from either list silently stops being guarded while
		// the DDL keeps adding it — the exact drift this guard exists to prevent.
		// The column names are pulled out with a regexp: hand-rolled string
		// surgery over a Go composite literal is how the first version of this
		// subtest ended up reporting columns that were plainly in the DDL.
		for _, list := range []string{"goalClientSignalColumns", "goalClientSignalSummaryColumns"} {
			at := strings.Index(text, list+" = []string{")
			if at < 0 {
				t.Fatalf("%s should be declared as a []string", list)
			}
			block := text[at:]
			stop := strings.Index(block, "}")
			if stop < 0 {
				t.Fatalf("%s literal is not terminated", list)
			}
			block = block[:stop]
			var cols []string
			for _, m := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(block, -1) {
				cols = append(cols, m[1])
			}
			if len(cols) == 0 {
				t.Fatalf("%s listed no columns — the extractor is broken, not the code", list)
			}
			for _, col := range cols {
				if !strings.Contains(text, "ADD COLUMN IF NOT EXISTS "+col) {
					t.Errorf("%s guards column %q but the DDL never creates it", list, col)
				}
			}
		}
		// Reverse direction: every column the DDL adds must be guarded somewhere.
		constOpen := "const goalClientSignalDDL = `"
		at := strings.Index(text, constOpen)
		if at < 0 {
			t.Fatal("goalClientSignalDDL constant not found")
		}
		// Start *after* the opening backtick, otherwise the first backtick found
		// is the one that opens the literal and the slice collapses to the const
		// declaration header with none of the SQL in it.
		ddl := text[at+len(constOpen):]
		if end := strings.Index(ddl, "`"); end >= 0 {
			ddl = ddl[:end]
		}
		added := regexp.MustCompile(`ADD COLUMN IF NOT EXISTS ([a-z_]+)`).
			FindAllStringSubmatch(ddl, -1)
		if len(added) == 0 {
			t.Fatal("no ADD COLUMN found in the DDL — the extractor is broken, not the code")
		}
		for _, m := range added {
			if !strings.Contains(text, `"`+m[1]+`"`) {
				t.Errorf("DDL creates column %q but no guard list mentions it", m[1])
			}
		}
	})

	t.Run("probe_failure_falls_through_to_the_ddl", func(t *testing.T) {
		probe := text[strings.Index(text, "func (d *DB) goalClientSignalCurrent("):]
		if end := strings.Index(probe[1:], "\n}\n"); end > 0 {
			probe = probe[:end+1]
		}
		// ★ Interpreted string literal, not a backtick raw string. In a raw
		// string `\n` is a literal backslash+n, so the anchor silently never
		// matches and this subtest is permanently red — a gate that can only
		// fail teaches you nothing.
		if !strings.Contains(probe, "\"error\", err)\n\t\treturn false\n") {
			t.Error("a probe error must return false so the DDL still runs — " +
				"reading it as \"already current\" would skip the schema forever")
		}
	})

	t.Run("still_wired_into_the_startup_sequence", func(t *testing.T) {
		// ★ Assert the call site, not the bare identifier:
		// `_unused_ensureGoalClientSignalSchema(` still contains the substring
		// `ensureGoalClientSignalSchema(` (§10.98.4's Contains-substring trap).
		if !strings.Contains(text, "ensureGoalClientSignalSchema(") {
			t.Error("ensureGoalClientSignalSchema must remain defined")
		}
		dbgo, err := os.ReadFile("db.go")
		if err != nil {
			t.Fatalf("read db.go: %v", err)
		}
		if !strings.Contains(string(dbgo), "ensureGoalClientSignalSchema(migCtx)") {
			t.Error("ensureGoalClientSignalSchema must still be called from db.go's startup sequence")
		}
	})
}
