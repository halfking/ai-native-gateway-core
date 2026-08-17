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
