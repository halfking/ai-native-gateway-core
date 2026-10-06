package startup

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigration838_AnalyzeSkipFrozenMonth pins migration 838, which stops
// re-analyzing last month's frozen partitions on every hourly pass of
// analyze_llm_gateway_table_stats.
//
// What it fixes (252 production, runbook §10.90-§10.92):
// that call is the #1 consumer of database time in the gateway database
// (81.0% of measured DB time, ~56 min/day). Per-relation measurement splits
// the pass into a hot-table half (23.0% of the per-column statistics work) and
// a partition half (77.0%), of which the previous-month group alone is 25.7% of
// the whole pass. A rolled-over partition receives no further writes, so its
// statistics cannot go stale, and every partition is analyzed while it is the
// current month — the previous-month pass is repeated work.
//
// The guard keys on "never analyzed" (no pg_statistic rows) rather than on a
// timestamp. Columnar partitions DO get pg_statistic rows from a manual
// ANALYZE (verified: credential_model_index_2026_09 has 17 rows for 17
// columns), which is what makes the guard usable at all — a guard that never
// became true would silently save 0% while looking correct.
//
// This file asserts the migration's TEXT. Its BEHAVIOUR — does the plpgsql
// actually install, and does it actually skip the rolled-over month while
// still covering a never-analyzed partition — is verified separately by
// scripts/.verify-analyze-838-behavior.sh, which runs a throwaway
// postgres:16-alpine container and asserts the analyzed count and the
// per-relation last_analyze stamps across separate transactions. That script
// is deliberately NOT wired into verify.sh: CI environments without docker
// would go red for no product reason.
//
// Most assertions are scoped to the extracted function body on purpose. The
// migration header and the COMMENT both legitimately name pg_statistic and
// "never analyzed", so whole-file Contains/NotContains checks would pass on
// prose and prove nothing about the executable body.
func TestMigration838_AnalyzeSkipFrozenMonth(t *testing.T) {
	const upName = "838_analyze_skip_frozen_month.sql"
	const downName = "838_analyze_skip_frozen_month.down.sql"

	readFile := func(t *testing.T, name string) string {
		t.Helper()
		b, err := os.ReadFile(name)
		require.NoError(t, err, "%s should exist", name)
		return string(b)
	}

	// funcBody returns the text between the $function$ delimiters of the
	// analyze_llm_gateway_table_stats definition, so assertions cannot be
	// satisfied by the surrounding comments. Two anchors are accepted because
	// the migrations use CREATE OR REPLACE while the canonical/baseline copies
	// use CREATE FUNCTION public....
	funcBody := func(t *testing.T, sql string) string {
		t.Helper()
		anchors := []string{
			"CREATE OR REPLACE FUNCTION analyze_llm_gateway_table_stats",
			"CREATE FUNCTION public.analyze_llm_gateway_table_stats",
		}
		i := -1
		for _, a := range anchors {
			if j := strings.Index(sql, a); j != -1 && (i == -1 || j < i) {
				i = j
			}
		}
		require.NotEqual(t, -1, i, "function definition not found")
		rest := sql[i:]
		// The migrations delimit with $function$, the canonical/baseline copies
		// with $_$. Both may appear later in the same file, so pick whichever
		// opens first — otherwise the body of an unrelated function is returned.
		nf := strings.Index(rest, "$function$")
		nq := strings.Index(rest, "$_$")
		delim := "$function$"
		switch {
		case nf == -1 && nq == -1:
			require.FailNow(t, "no dollar-quote delimiter after the function anchor")
		case nq != -1 && (nf == -1 || nq < nf):
			delim = "$_$"
		}
		rest = rest[strings.Index(rest, delim)+len(delim):]
		end := strings.Index(rest, delim)
		require.NotEqual(t, -1, end, "closing %s delimiter not found", delim)
		return rest[:end]
	}

	const guard = "AND (m = 0 OR NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid))"

	// pgStatisticLines returns the trimmed body lines that mention pg_statistic,
	// excluding comments. The migration's inline comment deliberately explains
	// the empty-table case and names pg_statistic, so comment lines must not
	// count or this helper would report the guard twice.
	pgStatisticLines := func(body string) []string {
		var out []string
		for _, l := range strings.Split(body, "\n") {
			t := strings.TrimSpace(l)
			if strings.Contains(t, "pg_statistic") && !strings.HasPrefix(t, "--") {
				out = append(out, t)
			}
		}
		return out
	}

	upSQL := readFile(t, upName)
	downSQL := readFile(t, downName)
	upBody := funcBody(t, upSQL)
	downBody := funcBody(t, downSQL)

	t.Run("transactional", func(t *testing.T) {
		for name, sql := range map[string]string{upName: upSQL, downName: downSQL} {
			require.Contains(t, sql, "BEGIN;", "%s must be transactional", name)
			require.Contains(t, sql, "COMMIT;", "%s must be transactional", name)
		}
	})

	t.Run("signature_preserved", func(t *testing.T) {
		// The Go caller passes an explicit 2 and the default must keep working
		// for migration 404's own `SELECT analyze_llm_gateway_table_stats(3)`.
		// The signature sits on the anchor line, which funcBody strips, so it is
		// asserted against the whole file rather than the extracted body.
		for name, sql := range map[string]string{upName: upSQL, downName: downSQL} {
			require.Contains(t, sql, "p_recent_months integer DEFAULT 2",
				"%s must keep the DEFAULT 2 signature — callers and migration 404 depend on it", name)
			require.Contains(t, sql, "RETURNS integer",
				"%s must keep returning the analyzed count", name)
		}
		for name, body := range map[string]string{upName: upBody, downName: downBody} {
			require.Contains(t, body, "analyzed := analyzed + 1;",
				"%s must keep counting analyzed relations", name)
		}
	})

	t.Run("up_adds_never_analyzed_guard_to_partition_loop", func(t *testing.T) {
		require.Contains(t, upBody, guard,
			"838 exists to skip rolled-over months that were already analyzed")
	})

	t.Run("up_keeps_current_month_unconditional", func(t *testing.T) {
		// The dangerous edit is any rewrite of the guard that drops the
		// `m = 0 OR` prefix: a bare NOT EXISTS also skips the current month,
		// which is written continuously and whose statistics do go stale. That
		// trades 25.7% of waste for silent plan-quality loss.
		//
		// Asserted positively on the guard line. A whole-body
		// NotContains("AND NOT EXISTS (...starelid = c.oid)") was tried first
		// and mutation M79 walked straight through it, because the natural way
		// to drop the prefix leaves a parenthesis behind:
		// `AND (NOT EXISTS (...))` != `AND NOT EXISTS (...)`.
		// A missing substring cannot be reasoned about; a required prefix can.
		lines := pgStatisticLines(upBody)
		require.Len(t, lines, 1,
			"the guard must be exactly one line in the function body, got %v", lines)
		require.True(t, strings.HasPrefix(lines[0], "AND (m = 0 OR"),
			"the guard must short-circuit on `m = 0` so the current month is always "+
				"analyzed; got %q", lines[0])
	})

	t.Run("guard_is_attached_to_the_partition_loop_only", func(t *testing.T) {
		require.Equal(t, 1, strings.Count(upBody, guard),
			"the guard must appear exactly once — hot tables are written continuously "+
				"and must keep their unconditional pass")
		// And it must sit after the month-suffix regex, i.e. inside the same
		// SELECT that matches <parent>_<YYYY_MM>, not in the hot-table branch.
		regexIdx := strings.Index(upBody, "|| suffix || '$')")
		guardIdx := strings.Index(upBody, guard)
		require.NotEqual(t, -1, regexIdx, "the month-suffix regex must survive")
		require.Greater(t, guardIdx, regexIdx,
			"the guard must be a conjunct of the month-partition query")
		hotIdx := strings.Index(upBody, "LIKE '%\\_hot' ESCAPE '\\'")
		require.Less(t, hotIdx, guardIdx,
			"the hot-table branch comes first and must stay un-guarded")
	})

	t.Run("up_keeps_hot_branch_and_parent_list", func(t *testing.T) {
		// Reverse gates. Without them, deleting the hot branch or shrinking the
		// parent list would still pass every assertion above — 838 must not be
		// able to shrink coverage as a side effect.
		require.Contains(t, upBody, "LIKE '%\\_hot' ESCAPE '\\'",
			"the hot-table branch must survive 838")
		for _, parent := range []string{
			"credential_model_index", "model_probe_runs", "request_logs",
			"routing_decision_log", "request_wal", "usage_ledger", "credit_ledger",
			"tool_usage_stats", "candidate_failure_logs", "handoff_logs",
			"request_logs_bodies",
		} {
			require.Contains(t, upBody, parent,
				"838 must not drop parent table %s from the analyze set", parent)
		}
	})

	t.Run("down_restores_unconditional_analysis", func(t *testing.T) {
		require.NotContains(t, downBody, guard,
			"rollback must restore the pre-838 unconditional re-analysis")
		require.NotContains(t, downBody, "pg_statistic",
			"rollback must not keep any pg_statistic-based skip")
		// ...and it must keep the same coverage it is rolling back to.
		require.Contains(t, downBody, "LIKE '%\\_hot' ESCAPE '\\'",
			"rollback must keep the hot-table branch")
		require.Contains(t, downBody, "|| suffix || '$')",
			"rollback must keep the month-partition regex")
	})

	t.Run("mirror_copies_carry_the_guard", func(t *testing.T) {
		// Fresh installs read the baseline/object SQL, not this migration file.
		// If only the migration is patched, a rebuilt database silently keeps the
		// old behavior and the change looks like it did nothing.
		mirrors := []string{
			"../../../sql/objects/functions/analyze_llm_gateway_table_stats_integer.sql",
			"../../../deploy/sql/schemas/baseline/01-schema.sql",
			"../../../sql/schema/01-schema.sql",
			"../../../installer/cmd/llm-gw-installer/embeddata/01-schema.sql",
		}
		// 2026-10-07 (migration 839): the exact `m = 0 OR NOT EXISTS(...)` form
		// was superseded — 839 hands the current month's HEAP partitions back to
		// autovacuum, so "analyze every current-month partition" is no longer the
		// intent. What 838 actually protects is the FIRST-COVERAGE invariant:
		// a rolled-over partition is only analysed when it has no pg_statistic
		// rows. That clause survives verbatim in 839's predicate, so assert the
		// invariant rather than the superseded spelling.
		//
		// Weakening a gate to make it green is only legitimate when the invariant
		// it protected is still asserted — hence checking both halves here:
		// the first-coverage clause must be present, AND if the old `m = 0`
		// form is gone then 839's columnar clause must be there to replace it.
		const firstCoverage = "NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid)"
		const columnarClause = "AND c.relam <> (SELECT oid FROM pg_am WHERE amname = 'heap')"
		for _, path := range mirrors {
			sql := readFile(t, path)
			body := funcBody(t, sql)
			require.Contains(t, body, firstCoverage,
				"%s must keep 838's first-coverage clause (never-analyzed partitions "+
					"must still be analysed)", path)
			if !strings.Contains(body, guard) {
				require.Contains(t, body, columnarClause,
					"%s dropped 838's `m = 0` form without gaining 839's columnar clause — "+
						"the current month would lose both the handoff and the columnar guard",
					path)
			}
		}
	})

	t.Run("installer_wiring", func(t *testing.T) {
		// A migration that is not wired into the installer never runs on a
		// rebuilt host — the code would look deployed and behave otherwise.
		const embedName = "../../../installer/cmd/llm-gw-installer/embeddata/startup/" + upName
		upBytes := readFile(t, embedName)
		require.Equal(t, upSQL, upBytes,
			"%s must be a byte-identical copy of %s", embedName, upName)

		main := readFile(t, "../../../installer/cmd/llm-gw-installer/main.go")
		require.Contains(t, main, "//go:embed embeddata/startup/"+upName,
			"the embed directive must reference 838")
		require.Contains(t, main, `"startup/`+upName+`":`,
			"the embeddedSQLFiles map must carry 838")
		require.Contains(t, main, "var analyzeSkipFrozenMonth838 []byte",
			"the embed variable must be declared")

		runner := readFile(t, "../../../installer/internal/dbinit/runner.go")
		require.Contains(t, runner, `"`+upName+`",`,
			"the startup migration list must run 838")
	})

	t.Run("migration_registry", func(t *testing.T) {
		tsv := readFile(t, "../../../sql/schema/installed_startup_migrations.tsv")
		require.Contains(t, tsv, upName+"\n",
			"838 must be registered in installed_startup_migrations.tsv")
	})
}
