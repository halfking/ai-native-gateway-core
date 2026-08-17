package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration529RepairContract(t *testing.T) {
	up, err := os.ReadFile("529_repair_shared_pg_sticky_and_bodies_2026_07.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("529_repair_shared_pg_sticky_and_bodies_2026_07.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	upSQL := string(up)
	for _, required := range []string{
		"HAVING count(*) > 1",
		"RAISE EXCEPTION '529: sticky_sessions contains",
		"ADD CONSTRAINT uq_sticky_sessions_sticky_key UNIQUE (sticky_key)",
		"PARTITION OF public.request_logs_bodies",
		"FOR VALUES FROM ('2026-07-01 00:00:00+08')",
		"TO ('2026-08-01 00:00:00+08')",
		"pg_get_expr(c.relpartbound, c.oid)",
		"sticky_key unique conflict arbiter was not created",
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("migration 529 missing fail-closed contract %q", required)
		}
	}

	downSQL := string(down)
	if !strings.Contains(downSQL, "intentionally non-destructive") ||
		strings.Contains(downSQL, "DROP TABLE") || strings.Contains(downSQL, "DROP CONSTRAINT") {
		t.Error("migration 529 down must preserve the forward-compatible schema")
	}
	if strings.Contains(upSQL, "DELETE FROM public.sticky_sessions") {
		t.Error("migration 529 must not delete duplicate sticky data")
	}
}

func TestMigration529HistoricalPartitionIsSubjectToBodyTTL(t *testing.T) {
	migration, err := os.ReadFile("529_repair_shared_pg_sticky_and_bodies_2026_07.sql")
	if err != nil {
		t.Fatal(err)
	}
	partitionManager, err := os.ReadFile("../../../bg/partition_manager.go")
	if err != nil {
		t.Fatal(err)
	}
	cleanupFunction, err := os.ReadFile("../../../sql/objects/functions/drop_old_request_logs_bodies_partitions_integer.sql")
	if err != nil {
		t.Fatal(err)
	}

	managerSQL := string(partitionManager)
	cleanupSQL := string(cleanupFunction)
	for _, required := range []string{
		"lifecycle.request_logs_bodies_ttl_days",
		"drop_old_request_logs_bodies_partitions($1)",
	} {
		if !strings.Contains(managerSQL, required) {
			t.Errorf("partition manager missing body TTL cleanup contract %q", required)
		}
	}
	for _, required := range []string{
		"month_end <= cutoff_date",
		"DROP TABLE %I",
	} {
		if !strings.Contains(cleanupSQL, required) {
			t.Errorf("body partition cleanup missing expiry contract %q", required)
		}
	}

	// 529 repairs the July partition so the backlog can be promoted. It is
	// deliberately not a permanent-retention guarantee: the cleanup function
	// may drop it once its month end is outside the configured TTL cutoff.
	if !strings.Contains(string(migration), "expected_bound CONSTANT TEXT") {
		t.Error("migration 529 must document the repaired partition bounds")
	}
}
