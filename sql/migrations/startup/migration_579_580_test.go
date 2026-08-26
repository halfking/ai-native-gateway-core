package startup

import (
	"strings"
	"testing"
)

// TestMigration580SessionModuleExecutionsPromoteContract pins the up
// contract for migration 580: the real session_module_executions_hot
// column set (19 columns from migration 382), the correct ensure function,
// the seeded retention setting, and a matching down script.
func TestMigration580SessionModuleExecutionsPromoteContract(t *testing.T) {
	up := string(migrationFile(t, "580_session_module_executions_hot_promote.sql"))

	for _, want := range []string{
		"BEGIN;",
		"CREATE OR REPLACE FUNCTION public.promote_session_module_executions_hot_to_partition(",
		"SELECT execution_id, gw_session_id, tenant_id, module_name, module_version,",
		"result_summary, result_detail, error_message, cache_key, ttl_seconds,",
		"expires_at, created_at, updated_at",
		"WHERE created_at < statement_timestamp() - p_retention",
		"ORDER BY created_at, execution_id",
		"FOR UPDATE SKIP LOCKED",
		"PERFORM public.ensure_session_module_executions_partition(v_month_value::date);",
		"lifecycle.session_module_executions_hot_retention_hours",
		"COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 580 up missing %q", want)
		}
	}
	migrationFile(t, "580_session_module_executions_hot_promote.down.sql")
}

// TestMigration579DashboardPromoteStructure pins the structural contract of
// the deployed 579 file. Its function body references a column projection
// that does not exist on dashboard_access_events_hot (migration 383) and is
// superseded by migration 607; the deployed file is checksum-frozen in
// repository_schema_migrations and must not be edited. What must hold is
// the surrounding structure: the function slot, the correct ensure function
// name in the post-condition, the seeded retention setting, and the down
// script.
func TestMigration579DashboardPromoteStructure(t *testing.T) {
	up := string(migrationFile(t, "579_dashboard_access_events_hot_promote.sql"))

	for _, want := range []string{
		"BEGIN;",
		"CREATE OR REPLACE FUNCTION public.promote_dashboard_access_events_hot_to_partition(",
		"to_regprocedure('public.ensure_dashboard_events_partition(date)') IS NOT NULL",
		"lifecycle.dashboard_access_events_hot_retention_hours",
		"COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 579 up missing %q", want)
		}
	}
	migrationFile(t, "579_dashboard_access_events_hot_promote.down.sql")
}

// TestMigration607RepairDashboardPromoteColumns pins the corrected
// dashboard_access_events promote body installed by migration 607:
// the real 23-column projection of migration 383 (verified against the
// 2026-08-25 252 production dump), created_at as drain predicate and
// partition-key bucket, and the real ensure function.
func TestMigration607RepairDashboardPromoteColumns(t *testing.T) {
	up := string(migrationFile(t, "607_repair_dashboard_access_events_promote_columns.sql"))

	for _, want := range []string{
		"BEGIN;",
		"CREATE OR REPLACE FUNCTION public.promote_dashboard_access_events_hot_to_partition(",
		"SELECT event_id, event_type, timestamp, tenant_id, user_id, user_role,",
		"status_code, response_time_ms, cache_hit, data_size, error_code,",
		"client_ip, user_agent, referer, db_query_time_ms,",
		"cache_query_time_ms, created_at",
		"WHERE created_at < statement_timestamp() - p_retention",
		"ORDER BY created_at, event_id",
		"FOR UPDATE SKIP LOCKED",
		"PERFORM public.ensure_dashboard_events_partition(v_month_value::date);",
		"COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 607 up missing %q", want)
		}
	}

	// Columns and identifiers that only exist in the unrelated schema
	// migration 579 shipped with. If any reappear, the promote function
	// throws SQLSTATE 42703 on the first PartitionManager tick again.
	for _, forbidden := range []string{
		"occurred_at",
		"dashboard_id",
		"widget_id",
		"route_path",
		"country_code",
		"ip_address",
		"referrer",
		"request_size_bytes",
		"response_size_bytes",
		"ensure_dashboard_access_events_partition",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("migration 607 up must not reference %q — not part of dashboard_access_events_hot (migration 383)", forbidden)
		}
	}
	migrationFile(t, "607_repair_dashboard_access_events_promote_columns.down.sql")
}
