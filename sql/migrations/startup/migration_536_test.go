package startup

import (
	"strings"
	"testing"
)

// TestMigration536TriggerKindEnum pins migration 536's contract: the
// node_probe_runs_trigger_kind_check must accept every task.Source value
// the unified credential_probe_queue can carry, otherwise the silent
// INSERT failure at probe_service.go (pre-fix) will freeze the audit
// table (handoff §7 P0 — 2026-08-17 13:45 freeze on prod 154).
//
// The set MUST be the union of:
//   - 425 values (request_failure, manual, credential_recovery, sync_request)
//   - 536 additions (periodic, admin, integrity_probe_planner, selfcheck,
//     external_async)
//
// Backward-compat: do NOT remove 425 values; legacy dashboards still key
// off them.
func TestMigration536TriggerKindEnum(t *testing.T) {
	forward := string(migrationFile(t, "536_node_probe_runs_trigger_kind_unified_queue.sql"))
	for _, want := range []string{
		// 425 — must remain
		"'request_failure'",
		"'manual'",
		"'credential_recovery'",
		"'sync_request'",
		// 536 — new sources
		"'periodic'",
		"'admin'",
		"'integrity_probe_planner'",
		"'selfcheck'",
		"'external_async'",
		// Mechanical shape
		"DROP CONSTRAINT IF EXISTS node_probe_runs_trigger_kind_check",
		"ADD CONSTRAINT node_probe_runs_trigger_kind_check",
		"CHECK (",
		"trigger_kind IN (",
	} {
		if !strings.Contains(forward, want) {
			t.Errorf("migration 536 missing %q — every unified-queue source must be in the enum", want)
		}
	}
}

// TestMigration536DownRefusesIf536RowsExist guards the down migration:
// running it while 536-only rows still live in node_probe_runs would
// re-create the audit freeze the up migration fixes. The down SQL must
// explicitly check + abort.
func TestMigration536DownRefusesIf536RowsExist(t *testing.T) {
	down := string(migrationFile(t, "536_node_probe_runs_trigger_kind_unified_queue.down.sql"))
	for _, want := range []string{
		"RAISE EXCEPTION",
		"node_probe_runs",
		"periodic",
		"admin",
		"integrity_probe_planner",
		"selfcheck",
		"external_async",
		"DROP CONSTRAINT IF EXISTS node_probe_runs_trigger_kind_check",
		"ADD CONSTRAINT node_probe_runs_trigger_kind_check",
		"'sync_request'", // 425 retained
	} {
		if !strings.Contains(down, want) {
			t.Errorf("migration 536 down missing %q", want)
		}
	}
}