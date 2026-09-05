package startup

import (
	"strings"
	"testing"
)

// TestMigration538TriggerKindEnum pins migration 538's audit and queue-source
// contract. Every unified-queue source must reach node_probe_runs without a
// CHECK rejection, including the credential self-check source.
func TestMigration538TriggerKindEnum(t *testing.T) {
	forward := string(migrationFile(t, "538_node_probe_runs_trigger_kind_unified_queue.sql"))
	for _, want := range []string{
		"'request_failure'",
		"'manual'",
		"'credential_recovery'",
		"'sync_request'",
		"'periodic'",
		"'admin'",
		"'integrity_probe_planner'",
		"'selfcheck'",
		"'external_async'",
		"DROP CONSTRAINT IF EXISTS node_probe_runs_trigger_kind_check",
		"ADD CONSTRAINT node_probe_runs_trigger_kind_check",
		"trigger_kind IN (",
		"DROP CONSTRAINT IF EXISTS credential_probe_queue_source_check",
		"ADD CONSTRAINT credential_probe_queue_source_check",
	} {
		if !strings.Contains(forward, want) {
			t.Errorf("migration 538 missing %q", want)
		}
	}
}

// TestMigration538DownRefusesIf538RowsExist guards the down migration: it must
// not restore the old audit enum while 538-only rows still exist, and must
// restore the preceding queue-source constraint at the same time.
func TestMigration538DownRefusesIf538RowsExist(t *testing.T) {
	down := string(migrationFile(t, "538_node_probe_runs_trigger_kind_unified_queue.down.sql"))
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
		"'sync_request'",
		"DROP CONSTRAINT IF EXISTS credential_probe_queue_source_check",
		"ADD CONSTRAINT credential_probe_queue_source_check",
		"'integrity_probe_planner'",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("migration 538 down missing %q", want)
		}
	}
}
