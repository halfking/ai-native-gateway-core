package startup

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigration632_RoutingAnalyticsMaterializedView verifies that migration 632
// creates the routing analytics materialized views correctly.
func TestMigration632_RoutingAnalyticsMaterializedView(t *testing.T) {
	t.Run("migration_files_exist", func(t *testing.T) {
		// Check up migration exists
		up, err := os.ReadFile("up/632_routing_analytics_materialized_view.sql")
		require.NoError(t, err, "up migration file should exist")
		require.NotEmpty(t, up, "up migration should not be empty")

		// Check down migration exists
		down, err := os.ReadFile("down/632_routing_analytics_materialized_view.down.sql")
		require.NoError(t, err, "down migration file should exist")
		require.NotEmpty(t, down, "down migration should not be empty")

		// Verify transactional
		upSQL := string(up)
		downSQL := string(down)
		require.Contains(t, upSQL, "BEGIN;", "up migration must start with BEGIN")
		require.Contains(t, upSQL, "COMMIT;", "up migration must end with COMMIT")
		require.Contains(t, downSQL, "BEGIN;", "down migration must start with BEGIN")
		require.Contains(t, downSQL, "COMMIT;", "down migration must end with COMMIT")
	})

	t.Run("creates_required_views", func(t *testing.T) {
		up, err := os.ReadFile("up/632_routing_analytics_materialized_view.sql")
		require.NoError(t, err)
		upSQL := string(up)

		// Check for required materialized views
		require.Contains(t, upSQL, "CREATE MATERIALIZED VIEW IF NOT EXISTS routing_analytics_7d",
			"should create routing_analytics_7d materialized view")
		require.Contains(t, upSQL, "CREATE MATERIALIZED VIEW IF NOT EXISTS routing_audit_summary_7d",
			"should create routing_audit_summary_7d materialized view")

		// Check for required columns
		requiredColumns := []string{
			"time_bucket",
			"effective_task_type",
			"effective_model",
			"effective_work_type",
			"provider_id",
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
		up, err := os.ReadFile("up/632_routing_analytics_materialized_view.sql")
		require.NoError(t, err)
		upSQL := string(up)

		// Check for unique index (required for CONCURRENTLY refresh)
		require.Contains(t, upSQL, "CREATE UNIQUE INDEX IF NOT EXISTS routing_analytics_7d_pkey",
			"should create unique primary key index for CONCURRENTLY refresh")

		// Check for covering indexes
		requiredIndexes := []string{
			"routing_analytics_7d_task_model_idx",
			"routing_analytics_7d_time_idx",
			"routing_analytics_7d_tenant_idx",
			"routing_audit_summary_7d_pkey",
		}
		for _, idx := range requiredIndexes {
			require.Contains(t, upSQL, idx,
				"should create index %s", idx)
		}
	})

	t.Run("uses_base_view", func(t *testing.T) {
		up, err := os.ReadFile("up/632_routing_analytics_materialized_view.sql")
		require.NoError(t, err)
		upSQL := string(up)

		// Should query from the _without_customer_id view for performance
		require.Contains(t, upSQL, "FROM request_logs_with_current_month_without_customer_id",
			"should query from request_logs_with_current_month_without_customer_id view")

		// Should use 7-day window
		require.Contains(t, upSQL, "ts >= NOW() - INTERVAL '7 days'",
			"should use 7-day time window")
	})

	t.Run("initial_refresh", func(t *testing.T) {
		up, err := os.ReadFile("up/632_routing_analytics_materialized_view.sql")
		require.NoError(t, err)
		upSQL := string(up)

		// Should perform initial refresh
		require.Contains(t, upSQL, "REFRESH MATERIALIZED VIEW routing_analytics_7d",
			"should perform initial refresh of routing_analytics_7d")
		require.Contains(t, upSQL, "REFRESH MATERIALIZED VIEW routing_audit_summary_7d",
			"should perform initial refresh of routing_audit_summary_7d")
	})

	t.Run("down_migration_drops_views", func(t *testing.T) {
		down, err := os.ReadFile("down/632_routing_analytics_materialized_view.down.sql")
		require.NoError(t, err)
		downSQL := string(down)

		// Should drop both materialized views
		require.Contains(t, downSQL, "DROP MATERIALIZED VIEW IF EXISTS routing_analytics_7d",
			"should drop routing_analytics_7d")
		require.Contains(t, downSQL, "DROP MATERIALIZED VIEW IF EXISTS routing_audit_summary_7d",
			"should drop routing_audit_summary_7d")

		// Should use CASCADE to drop indexes
		require.Contains(t, downSQL, "CASCADE",
			"should use CASCADE to drop dependent indexes")
	})
}

