package startup

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigration837_RoutingMVRefreshState pins migration 837, which removes
// `NOW() AS refreshed_at` from the two routing analytics materialized views and
// moves the refresh timestamp into routing_mv_refresh_state.
//
// The defect it fixes (252 production, runbook §10.75.6/§10.75.7):
// REFRESH MATERIALIZED VIEW CONCURRENTLY only rewrites rows whose recomputed
// tuple actually differs. A volatile NOW() column in the target list makes
// every tuple differ on every cycle, so the "concurrent" refresh became a
// full-table rewrite — 100.7% of rows per cycle, of which only 0.339% had any
// real difference. routing_analytics_7d was the 3rd largest WAL producer in
// the gateway database (56.33 GB, 8.98%).
//
// Most assertions below are scoped to a single CREATE MATERIALIZED VIEW block
// on purpose. A whole-file `NotContains(sql, "refreshed_at")` would be
// satisfied by nothing at all here — 837's own header and COMMENT strings
// legitimately mention the column they removed — and
// `Contains(sql, "refreshed_at")` would pass on the unrelated side table.
// Both failure modes were real: the 632 gate had the second one before this
// migration existed.
func TestMigration837_RoutingMVRefreshState(t *testing.T) {
	readFile := func(t *testing.T, name string) string {
		t.Helper()
		b, err := os.ReadFile(name)
		require.NoError(t, err, "%s should exist", name)
		return string(b)
	}

	const upName = "837_routing_mv_refresh_state.sql"
	const downName = "837_routing_mv_refresh_state.down.sql"

	upSQL := readFile(t, upName)
	downSQL := readFile(t, downName)

	views := []string{"public.routing_analytics_7d", "public.routing_audit_summary_7d"}

	t.Run("transactional", func(t *testing.T) {
		for name, sql := range map[string]string{upName: upSQL, downName: downSQL} {
			require.Contains(t, sql, "BEGIN;", "%s must be transactional", name)
			require.Contains(t, sql, "COMMIT;", "%s must be transactional", name)
		}
		// The 7-day aggregate exceeds the shared host's statement_timeout;
		// without a raised budget the rebuild is killed halfway.
		require.Contains(t, upSQL, "SET LOCAL statement_timeout",
			"rebuild must lift the statement timeout for this transaction")
	})

	t.Run("up_removes_refreshed_at_from_both_views", func(t *testing.T) {
		for _, view := range views {
			targets := matviewTargetList(t, upSQL, view)
			require.NotContains(t, targets, "refreshed_at",
				"%s must not carry refreshed_at after 837 — a volatile column forces a full rewrite", view)
			// No volatile expression may remain in the target list. Scoped to
			// the target list on purpose: the 7-day window filter legitimately
			// uses NOW(), and it cannot cause a rewrite because it never
			// contributes a column value.
			require.NotRegexp(t, regexp.MustCompile(`\bNOW\s*\(`), targets,
				"%s target list must contain no volatile expression after 837", view)
		}
	})

	t.Run("up_keeps_every_aggregate_column", func(t *testing.T) {
		// Two-sided gate: the previous check would also pass if someone deleted
		// the column list wholesale. These are the columns the analytics
		// queries actually read.
		body := matviewSelectBody(t, upSQL, "public.routing_analytics_7d")
		for _, col := range []string{
			"time_bucket",
			"effective_task_type",
			"effective_model",
			"effective_work_type",
			"effective_provider_id",
			"is_auto_request",
			"tenant_id",
			"request_count",
			"success_count",
			"auto_request_count",
			"specified_request_count",
			"p50_latency_ms",
			"p95_latency_ms",
			"p99_latency_ms",
			"total_cost_usd",
		} {
			require.Contains(t, body, col,
				"837 must not drop routing_analytics_7d column %s", col)
		}

		// The audit view's auto vs specified split is a FILTER predicate pair.
		// A copy/paste slip that gives both sides the same predicate still
		// produces every column name, so assert the predicates themselves.
		audit := matviewSelectBody(t, upSQL, "public.routing_audit_summary_7d")
		require.Contains(t, audit, "COUNT(*) FILTER (WHERE is_auto_request = TRUE) AS auto_request_count",
			"auto_request_count must count only auto requests")
		require.Contains(t, audit, "COUNT(*) FILTER (WHERE is_auto_request IS NOT TRUE) AS specified_request_count",
			"specified_request_count must count only specified requests")
	})

	t.Run("up_creates_the_refresh_state_table", func(t *testing.T) {
		require.Contains(t, upSQL,
			"CREATE TABLE IF NOT EXISTS public.routing_mv_refresh_state (\n  view_name TEXT PRIMARY KEY,\n  refreshed_at TIMESTAMPTZ NOT NULL\n)",
			"state table must be keyed by view and carry a non-null timestamp")
		require.Contains(t, upSQL, "COMMENT ON TABLE public.routing_mv_refresh_state",
			"state table should be documented in the catalog")
	})

	t.Run("up_stamps_both_views_because_it_just_rebuilt_them", func(t *testing.T) {
		// The up migration drops and recreates both views, so their content is
		// genuinely fresh. Not stamping here would leave every consumer on the
		// base-view fallback until the refresher's next cycle.
		require.Contains(t, upSQL,
			"VALUES ('routing_analytics_7d', NOW()), ('routing_audit_summary_7d', NOW())",
			"up must stamp both rebuilt views")
		require.Contains(t, upSQL, "ON CONFLICT (view_name) DO UPDATE",
			"stamp must be idempotent on replay")
		// The stamp must come after both CREATEs, not between them.
		require.Less(t,
			strings.LastIndex(upSQL, "CREATE MATERIALIZED VIEW"),
			strings.Index(upSQL, "INSERT INTO public.routing_mv_refresh_state"),
			"stamp must follow the view rebuilds, otherwise it stamps nothing")
	})

	t.Run("up_rebuilds_both_views_and_their_indexes", func(t *testing.T) {
		// CREATE MATERIALIZED VIEW cannot change an existing view's columns, so
		// convergence requires the drop. Same proven pattern as migration 649.
		for _, view := range views {
			require.Contains(t, upSQL, "DROP MATERIALIZED VIEW IF EXISTS "+view+" CASCADE;",
				"%s must be dropped so it can be recreated without refreshed_at", view)
		}
		for _, idx := range []string{
			"routing_analytics_7d_ukey",
			"routing_analytics_7d_task_model_idx",
			"routing_analytics_7d_time_idx",
			"routing_analytics_7d_tenant_idx",
			"routing_audit_summary_7d_ukey",
		} {
			require.Contains(t, upSQL, idx, "should recreate index %s", idx)
		}
	})

	t.Run("down_restores_the_pre_837_shape", func(t *testing.T) {
		for _, view := range views {
			body := matviewSelectBody(t, downSQL, view)
			require.Contains(t, body, "NOW() AS refreshed_at",
				"down must restore the pre-837 definition of %s", view)
		}
		require.Contains(t, downSQL, "DROP TABLE IF EXISTS public.routing_mv_refresh_state",
			"down must remove the state table the up migration introduced")
	})

	t.Run("embeddata_copy_is_byte_identical", func(t *testing.T) {
		// The installer ships its own copy; a drifted copy is a migration that
		// behaves differently depending on how the database was created.
		embed, err := os.ReadFile(
			"../../../installer/cmd/llm-gw-installer/embeddata/startup/" + upName)
		require.NoError(t, err, "installer embeddata copy should exist")
		require.Equal(t, upSQL, string(embed),
			"embeddata copy differs from the canonical migration")
	})

	t.Run("wired_into_the_installer_three_ways", func(t *testing.T) {
		mainGo := readFile(t, "../../../installer/cmd/llm-gw-installer/main.go")
		require.Contains(t, mainGo, "//go:embed embeddata/startup/"+upName,
			"embeddata file must be embedded")
		require.Contains(t, mainGo, `"startup/`+upName+`":`,
			"embedded file must be registered in embeddedSQLFiles")

		runnerGo := readFile(t, "../../../installer/internal/dbinit/runner.go")
		require.Contains(t, runnerGo, `"`+upName+`"`,
			"migration must be listed in StartupFiles or it never runs")

		// Ordering matters: 837 drops and recreates the views, and 649 creates
		// them with the old NOW() shape. Listed before 649, 837 would be undone.
		runner := runnerGo
		at837 := strings.Index(runner, `"`+upName+`"`)
		at649 := strings.Index(runner, `"649_routing_analytics_probe_filter.sql"`)
		require.GreaterOrEqual(t, at649, 0, "649 should still be listed")
		require.Greater(t, at837, at649,
			"837 must run after 649, which recreates the views with the pre-837 shape")
	})
}

// TestMigration837_GoPathAgreesWithSQL pins the Go-side owners of the same
// contract. The SQL mirror never runs on a Go-driven startup; db.ensureRouting-
// AnalyticsMaterializedViews and bg.MaterializedViewRefresher do. A 837 that
// only changed the .sql files would look complete and change nothing.
func TestMigration837_GoPathAgreesWithSQL(t *testing.T) {
	read := func(t *testing.T, path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		require.NoError(t, err, "%s should be readable", path)
		return string(b)
	}

	dbGo := read(t, "../../../db/db.go")
	refresherGo := read(t, "../../../bg/materialized_view_refresher.go")
	adminGo := read(t, "../../../admin/analytics_materialized.go")

	t.Run("db_ensure_defines_the_views_without_refreshed_at", func(t *testing.T) {
		for _, view := range []string{"routing_analytics_7d", "routing_audit_summary_7d"} {
			targets := matviewTargetList(t, dbGo, view)
			require.NotContains(t, targets, "refreshed_at",
				"db.go %s must not carry refreshed_at after 837", view)
			require.NotRegexp(t, regexp.MustCompile(`\bNOW\s*\(`), targets,
				"db.go %s target list must contain no volatile expression after 837", view)
		}
		require.Contains(t, dbGo, "CREATE TABLE IF NOT EXISTS routing_mv_refresh_state",
			"db.go must create the state table")
	})

	t.Run("db_ensure_rebuilds_existing_views", func(t *testing.T) {
		// The fast path checks only for origin_stage, which is identical in the
		// old and new definitions. Without a negative check for the column that
		// was removed, an already-deployed database reports "up to date" and
		// keeps the defective view forever. This is the gate that makes 837
		// actually reach production.
		//
		// Scoped per block, not whole-file. There are two independent probes
		// that each need the negative check — the fast-path gate and the
		// stale-definition probe — and they carry identical text. A
		// whole-file Contains is satisfied by either one alone, so deleting
		// the other still passes; that hole was real and mutation M74 hit it.
		// Counting one of the two symmetric strings misses the same way: it
		// only notices when the half it counted disappears.
		fastPath := sliceBetween(t, dbGo,
			"SELECT EXISTS (SELECT 1 FROM pg_matviews WHERE schemaname='public' AND matviewname='routing_analytics_7d')",
			").Scan(&upToDate)")
		staleProbe := sliceBetween(t, dbGo,
			"(to_regclass('public.routing_analytics_7d') IS NOT NULL",
			").Scan(&viewsExistedBefore, &staleDefinition)")

		for _, blk := range []struct {
			what string
			body string
		}{
			{"fast-path gate", fastPath},
			{"stale-definition probe", staleProbe},
		} {
			for _, view := range []string{"routing_analytics_7d", "routing_audit_summary_7d"} {
				// Built with real concatenation, not with '+view+' written
				// inside one quoted literal: that form searches for the text
				// "+view+" and can never match, which would make this gate red
				// for a reason unrelated to what it claims to check.
				require.Contains(t, blk.body,
					"POSITION('refreshed_at' IN COALESCE(pg_get_viewdef(to_regclass('public."+
						view+"'), true), '')) = 0",
					"%s must reject a %s definition that still carries refreshed_at", blk.what, view)
			}
		}

		require.Contains(t, fastPath,
			"to_regclass('public.routing_mv_refresh_state') IS NOT NULL",
			"fast path must also verify the state table exists")
		require.Contains(t, fastPath, "AND EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='routing_analytics_7d_ukey')",
			"fast path must keep the existing ukey index-repair check")
	})

	t.Run("db_stamp_is_shared_not_duplicated", func(t *testing.T) {
		require.Contains(t, dbGo, "StampRoutingMVRefreshSQL",
			"db package must export the stamp statement for bg to reuse")
		require.Contains(t, dbGo, "VALUES ($1, NOW())",
			"the stamp must take the view name as a parameter")
		// refreshAll refreshes the two views independently. Stamping both after
		// one succeeded would report a failed view as fresh, which is the one
		// thing the side table must never do.
		require.Contains(t, dbGo, "RoutingMVViewNames",
			"db must expose the tracked view names")
		require.Equal(t, 1, strings.Count(dbGo, "const StampRoutingMVRefreshSQL"),
			"there must be exactly one definition of the stamp statement")
	})

	t.Run("refresher_stamps_only_after_a_successful_refresh", func(t *testing.T) {
		require.Contains(t, refresherGo, "func (r *MaterializedViewRefresher) execRefresh",
			"refresh execution must be centralized so both branches stamp")

		start := strings.Index(refresherGo, "func (r *MaterializedViewRefresher) execRefresh")
		require.GreaterOrEqual(t, start, 0)
		body := refresherGo[start:]

		refreshAt := strings.Index(body, "REFRESH MATERIALIZED VIEW CONCURRENTLY")
		stampAt := strings.Index(body, "StampRoutingMVRefreshSQL")
		require.GreaterOrEqual(t, refreshAt, 0, "execRefresh must run the REFRESH")
		require.GreaterOrEqual(t, stampAt, 0, "execRefresh must record the refresh time")
		require.Less(t, refreshAt, stampAt,
			"the stamp must come after the REFRESH, never before")

		// And it must be unreachable when the REFRESH fails.
		earlyReturn := strings.Index(body, "if _, err := conn.Exec(ctx, \"REFRESH MATERIALIZED VIEW CONCURRENTLY \"+viewName); err != nil {\n\t\treturn err\n\t}")
		require.GreaterOrEqual(t, earlyReturn, 0, "a failed REFRESH must return before stamping")
		require.Less(t, earlyReturn, stampAt,
			"a failed REFRESH must not stamp the view as fresh")

		// Both call sites must route through execRefresh; a second raw
		// REFRESH+Exec would be an unstamped branch.
		require.Equal(t, 2, strings.Count(refresherGo, "r.execRefresh(ctx, conn, viewName)"),
			"both refresh branches must go through execRefresh")
		// Exactly one raw REFRESH in the whole file, and it lives inside
		// execRefresh — that is what makes "every refresh stamps" structural
		// rather than a promise repeated at each call site.
		require.Equal(t, 1, strings.Count(refresherGo,
			`conn.Exec(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY "+viewName)`),
			"the REFRESH statement must exist exactly once, inside execRefresh")
	})

	t.Run("admin_reads_the_stamp_and_still_checks_existence", func(t *testing.T) {
		require.Contains(t, adminGo, "FROM routing_mv_refresh_state WHERE view_name = $1",
			"the freshness gate must read the side table")
		// Two independent properties. The stamp survives a DROP, so relying on
		// it alone would report a rebuilt-but-dropped view as fresh.
		require.Contains(t, adminGo,
			"EXISTS (SELECT 1 FROM pg_matviews WHERE schemaname = 'public' AND matviewname = $1)",
			"the gate must still verify the view exists")
		require.Contains(t, adminGo, "!viewExists || refreshedAt == nil",
			"a missing view or a missing stamp must both fall back")
		// The old form aggregated over the whole view to find one timestamp.
		require.NotContains(t, adminGo, "MAX(refreshed_at) FROM",
			"the pre-837 freshness query must be gone")
	})
}

// sliceBetween returns the text from the first occurrence of start up to (not
// including) the first occurrence of end after it. Used to pin a check to one
// specific SQL block when the same text appears in more than one place.
func sliceBetween(t *testing.T, s, start, end string) string {
	t.Helper()
	i := strings.Index(s, start)
	require.GreaterOrEqual(t, i, 0, "should find start anchor: %s", start)
	j := strings.Index(s[i:], end)
	require.Greater(t, j, 0, "should find end anchor after %s: %s", start, end)
	return s[i : i+j]
}
