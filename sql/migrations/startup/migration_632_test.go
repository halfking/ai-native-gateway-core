package startup

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigration632_RoutingAnalyticsMaterializedView verifies migration 632.
//
// The startup migration engine is Go-driven (db.applyMigrationsOnce); the
// .sql files here are DBA-facing mirrors of db.ensure* functions. These
// tests therefore assert on file contents AND on the wiring in db/db.go —
// a migration that exists only as SQL never runs (that exact gap shipped
// initially and is what the wiring assertions guard against).
func TestMigration632_RoutingAnalyticsMaterializedView(t *testing.T) {
	readUp := func(t *testing.T) string {
		t.Helper()
		up, err := os.ReadFile("up/632_routing_analytics_materialized_view.sql")
		require.NoError(t, err, "up migration file should exist")
		return string(up)
	}

	t.Run("files_exist_and_transactional", func(t *testing.T) {
		upSQL := readUp(t)
		require.NotEmpty(t, upSQL)

		down, err := os.ReadFile("down/632_routing_analytics_materialized_view.down.sql")
		require.NoError(t, err, "down migration file should exist")
		downSQL := string(down)

		require.Contains(t, upSQL, "BEGIN;", "up migration must be transactional")
		require.Contains(t, upSQL, "COMMIT;", "up migration must be transactional")
		require.Contains(t, downSQL, "BEGIN;", "down migration must be transactional")
		require.Contains(t, downSQL, "COMMIT;", "down migration must be transactional")
	})

	t.Run("creates_required_views_and_columns", func(t *testing.T) {
		upSQL := readUp(t)

		// Replayable rewrite (2026-09-05): the SQL mirror drops and recreates
		// both matviews so a direct replay always converges (same discipline
		// as migration 658 — a stale CREATE OR REPLACE cannot change columns).
		// Runtime behaviour stays owned by db.go's IF NOT EXISTS ensure path.
		require.Contains(t, upSQL,
			"DROP MATERIALIZED VIEW IF EXISTS public.routing_analytics_7d CASCADE;",
			"should drop routing_analytics_7d for replayable recreation")
		require.Contains(t, upSQL,
			"CREATE MATERIALIZED VIEW public.routing_analytics_7d AS",
			"should create routing_analytics_7d materialized view")
		require.Contains(t, upSQL,
			"DROP MATERIALIZED VIEW IF EXISTS public.routing_audit_summary_7d CASCADE;",
			"should drop routing_audit_summary_7d for replayable recreation")
		require.Contains(t, upSQL,
			"CREATE MATERIALIZED VIEW public.routing_audit_summary_7d AS",
			"should create routing_audit_summary_7d materialized view")

		requiredColumns := []string{
			"time_bucket",
			"effective_task_type",
			"effective_model",
			"effective_work_type",
			"effective_provider_id",
			"is_auto_request",
			"tenant_id",
			"request_count",
			"success_count",
			"p95_latency_ms",
			"total_cost_usd",
			"refreshed_at",
		}
		for _, col := range requiredColumns {
			require.Contains(t, upSQL, col,
				"routing_analytics_7d should include column %s", col)
		}
	})

	t.Run("creates_required_indexes", func(t *testing.T) {
		upSQL := readUp(t)

		// Plain-column unique index is required for REFRESH ... CONCURRENTLY
		// (PG rejects expression indexes, SQLSTATE 55000 — seen on prod
		// 2026-09-01). The mirror needs no IF NOT EXISTS: the matviews are
		// dropped and recreated in the same replay, and the legacy _pkey
		// expression indexes die with the DROP ... CASCADE above (db.go keeps
		// the explicit `DROP INDEX IF EXISTS routing_analytics_7d_pkey` for
		// its no-drop in-place repair path).
		require.Contains(t, upSQL,
			"CREATE UNIQUE INDEX routing_analytics_7d_ukey\n  ON public.routing_analytics_7d (",
			"should create plain-column unique index for CONCURRENTLY refresh")
		require.NotContains(t, upSQL, "routing_analytics_7d_pkey",
			"legacy expression index must not be recreated by the replay")

		for _, idx := range []string{
			"routing_analytics_7d_task_model_idx",
			"routing_analytics_7d_time_idx",
			"routing_analytics_7d_tenant_idx",
			"routing_audit_summary_7d_ukey",
		} {
			require.Contains(t, upSQL, idx, "should create index %s", idx)
		}
	})

	t.Run("null_safe_unique_index", func(t *testing.T) {
		// Regression guard (2026-08-31 audit): is_auto_request must be
		// normalized to FALSE in the view. With raw NULLs, GROUP BY keeps
		// NULL and FALSE in separate buckets while the unique index maps
		// both onto the same COALESCE key → CREATE UNIQUE INDEX fails with
		// a duplicate key → startup aborts.
		upSQL := readUp(t)
		require.Contains(t, upSQL, "COALESCE(is_auto_request, FALSE) AS is_auto_request",
			"is_auto_request must be NULL-normalized in the view definition")
	})

	t.Run("tenant_id_text_placeholder", func(t *testing.T) {
		// Regression guard: tenant_id is TEXT in this schema (verified on
		// prod 252 PG). The unique indexes must use plain tenant_id columns:
		// PG rejects expression indexes for REFRESH ... CONCURRENTLY
		// (SQLSTATE 55000), and integer COALESCE placeholders are type
		// errors against text. Plain columns are safe because GROUP BY
		// collapses NULL keys into a single row. Both the SQL mirror and
		// the Go ensure in db/db.go must stay in sync.
		upSQL := readUp(t)
		require.Contains(t, upSQL, "CREATE UNIQUE INDEX routing_audit_summary_7d_ukey\n  ON public.routing_audit_summary_7d (tenant_id);",
			"audit summary unique index must be the plain tenant_id column")

		dbSrc, err := os.ReadFile("../../../db/db.go")
		require.NoError(t, err)
		src := string(dbSrc)
		require.Contains(t, src, "routing_analytics_7d_ukey",
			"db.go must create the plain-column ukey index")
		require.Contains(t, src, "routing_audit_summary_7d_ukey",
			"db.go must create the plain-column audit ukey index")
		require.NotContains(t, src, "COALESCE(tenant_id, -1)",
			"db.go must not use integer placeholders for text tenant_id")
	})

	t.Run("provider_credential_fallback_parity", func(t *testing.T) {
		// Regression guard: the view's provider column must keep the same
		// COALESCE(provider_id, credential lookup) fallback as the base
		// L2→L3 query, or the Sankey 'unknown' provider share differs
		// between materialized and base paths.
		upSQL := readUp(t)
		require.Contains(t, upSQL, "FROM credentials cr WHERE cr.id = credential_id",
			"effective_provider_id must fall back to the credential's provider")
	})

	t.Run("excludes_probe_and_self_check_rows", func(t *testing.T) {
		upSQL := readUp(t)
		for _, fragment := range []string{
			"COALESCE(origin_stage, '') NOT IN",
			"COALESCE(task_type, '') <> 'probe_triggered'",
			"COALESCE(request_id, '') NOT LIKE 'probe-%'",
		} {
			require.Contains(t, upSQL, fragment, "routing materialized views must exclude probe traffic")
		}
		for _, stage := range []string{"self_check", "node_probe", "system_health", "probe_direct", "probe_v2", "model_probe", "passive_probe", "manual"} {
			require.Contains(t, upSQL, stage, "probe stage %s must be excluded", stage)
		}
	})

	t.Run("uses_narrow_source_view_and_7d_window", func(t *testing.T) {
		upSQL := readUp(t)
		// Replayable rewrite (2026-09-05): analytics aggregates the narrow
		// routing_analytics_source view (request_logs_hot UNION ALL
		// request_logs). The old frozen wrapper
		// request_logs_with_current_month_without_customer_id never gained
		// origin_stage, so replaying a wrapper-based definition fails with
		// SQLSTATE 42703 on any current database (observed 2026-09-04).
		require.Contains(t, upSQL,
			"CREATE VIEW public.routing_analytics_source AS",
			"should define the narrow routing_analytics_source view")
		require.Contains(t, upSQL, "FROM public.request_logs_hot\nUNION ALL",
			"source view must union the hot table with the parent")
		require.Contains(t, upSQL, "FROM public.request_logs;",
			"source view must include the parent request_logs table")
		require.Contains(t, upSQL, "FROM public.routing_analytics_source",
			"matviews must aggregate the narrow source view")
		require.NotContains(t, upSQL, "FROM request_logs_with_current_month_without_customer_id",
			"must not aggregate the frozen request-log wrapper (SQLSTATE 42703 on replay)")
		require.Contains(t, upSQL, "ts >= NOW() - INTERVAL '7 days'",
			"should aggregate a 7-day window")
	})

	t.Run("wired_into_go_migration_engine", func(t *testing.T) {
		// The SQL file is only a mirror; db.applyMigrationsOnce must call
		// the ensure function or none of this ever executes.
		dbSrc, err := os.ReadFile("../../../db/db.go")
		require.NoError(t, err, "db/db.go should be readable")
		src := string(dbSrc)

		require.Contains(t, src,
			"ensureRoutingAnalyticsMaterializedViews(migCtx)",
			"ensureRoutingAnalyticsMaterializedViews must be called from applyMigrationsOnce")
		require.Contains(t, src,
			"CREATE MATERIALIZED VIEW IF NOT EXISTS routing_analytics_7d",
			"db.go must define the routing_analytics_7d view SQL")
		require.Contains(t, src, "COALESCE(is_auto_request, FALSE) AS is_auto_request",
			"db.go view SQL must NULL-normalize is_auto_request")

		columnsAt := strings.Index(src, "ensureRoutingAnalyticsColumns(migCtx)")
		viewsAt := strings.Index(src, "ensureRoutingAnalyticsMaterializedViews(migCtx)")
		require.GreaterOrEqual(t, columnsAt, 0, "runtime must ensure analytics columns")
		require.Greater(t, viewsAt, columnsAt,
			"origin_stage/task_type columns must be ensured before analytics views")
		require.Contains(t, src, "ALTER TABLE IF EXISTS request_logs_hot",
			"runtime prerequisite must cover request_logs_hot")
		require.Contains(t, src, "ALTER TABLE IF EXISTS request_logs",
			"runtime prerequisite must cover request_logs")
	})

	t.Run("down_migration_drops_views", func(t *testing.T) {
		down, err := os.ReadFile("down/632_routing_analytics_materialized_view.down.sql")
		require.NoError(t, err)
		downSQL := string(down)

		require.Contains(t, downSQL, "DROP MATERIALIZED VIEW IF EXISTS routing_analytics_7d",
			"should drop routing_analytics_7d")
		require.Contains(t, downSQL, "DROP MATERIALIZED VIEW IF EXISTS routing_audit_summary_7d",
			"should drop routing_audit_summary_7d")
		require.True(t, strings.Contains(downSQL, "CASCADE"),
			"should CASCADE to drop dependent indexes")
	})
}
