package dispatch

import (
	"os"
	"strings"
	"testing"
)

func TestStateTransitionTenantRepairMigrationContract(t *testing.T) {
	up := readMigration(t, "../../sql/migrations/startup/521_repair_state_transitions_tenant.sql")
	down := readMigration(t, "../../sql/migrations/startup/521_repair_state_transitions_tenant.down.sql")

	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default'",
		"idx_state_transitions_tenant_request",
		"ENABLE ROW LEVEL SECURITY",
		"state_transitions_tenant_isolation",
		"state_transitions_super_admin_bypass",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("repair migration missing %q", required)
		}
	}
	if !strings.Contains(down, "DROP COLUMN IF EXISTS tenant_id") {
		t.Fatal("repair migration rollback must remove tenant_id")
	}
}

func readMigration(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration failed: %v (path=%s)", err, path)
	}
	return string(body)
}
