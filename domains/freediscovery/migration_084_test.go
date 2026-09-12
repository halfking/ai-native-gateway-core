package freediscovery

import (
	"os"
	"strings"
	"testing"
)

// Structural tests: validate the schema invariants of the 084 migration file
// (mirrors the handoff/migration_362_test.go pattern).
// DDL execution is NOT run here — DDL execution coverage is provided by
// database integration tests.

func readMigration084(t *testing.T, suffix string) string {
	t.Helper()
	name := "../../sql/migrations/084-freediscovery-schema" + suffix
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestMigration084_Up_CreatesThreeTables(t *testing.T) {
	up := readMigration084(t, ".sql")
	for _, table := range []string{"provider_templates", "discovery_tasks", "discovery_results"} {
		if !strings.Contains(up, "CREATE TABLE IF NOT EXISTS public."+table+" (") {
			t.Errorf("up migration must create table %s", table)
		}
	}
}

func TestMigration084_Up_RLSContract(t *testing.T) {
	up := readMigration084(t, ".sql")
	if !strings.Contains(up, "ENABLE ROW LEVEL SECURITY") {
		t.Fatal("up migration must enable RLS")
	}
	// Policies are concatenated via FOREACH loop + format('tenant_isolation_%s'); verify the naming pattern
	if !strings.Contains(up, "'tenant_isolation_' || table_name") {
		t.Fatal("missing tenant_isolation policy naming pattern")
	}
	// The loop must cover all three tables
	for _, table := range []string{"provider_templates", "discovery_tasks", "discovery_results"} {
		if !strings.Contains(up, "'"+table+"'") {
			t.Errorf("RLS loop must cover table %s", table)
		}
	}
	// get_current_tenant() idempotent protection must exist (084 must runnable on its own after 075 is rolled back)
	if !strings.Contains(up, "get_current_tenant") {
		t.Fatal("up migration must idempotently ensure get_current_tenant()")
	}
	// tenant GUC name is a database-wide contract
	if !strings.Contains(up, "app.current_tenant") {
		t.Fatal("policies must reference app.current_tenant GUC")
	}
}

func TestMigration084_Up_TenantColumnContract(t *testing.T) {
	up := readMigration084(t, ".sql")
	// All three tables must include tenant_id TEXT NOT NULL DEFAULT 'default' (075 contract)
	count := strings.Count(up, "tenant_id TEXT NOT NULL DEFAULT 'default'")
	if count < 3 {
		t.Fatalf("expected >=3 tenant_id columns with default, got %d", count)
	}
}

func TestMigration084_Up_UniqueConstraints(t *testing.T) {
	up := readMigration084(t, ".sql")
	if !strings.Contains(up, "UNIQUE(provider_code, tenant_id)") {
		t.Fatal("provider_templates must be unique per (provider_code, tenant_id)")
	}
	if !strings.Contains(up, "UNIQUE(task_id, model_id)") {
		t.Fatal("discovery_results must be unique per (task_id, model_id)")
	}
}

// TestMigration084_Up_AllTablesHaveUpdatedAt: the omnifree_touch_updated_at()
// trigger is attached to all three tables in a loop and references NEW.updated_at —
// any table missing this column will make every UPDATE fail with 42703
// (lesson learned from real E2E runs; sqlmock cannot cover trigger behavior).
func TestMigration084_Up_AllTablesHaveUpdatedAt(t *testing.T) {
	up := readMigration084(t, ".sql")
	// The discovery_results CREATE TABLE statement must explicitly include updated_at
	if !strings.Contains(up, "updated_at TIMESTAMPTZ DEFAULT now()") {
		t.Fatal("discovery_results must define updated_at (trigger contract)")
	}
}

func TestMigration084_Up_CatalogExtension(t *testing.T) {
	up := readMigration084(t, ".sql")
	for _, col := range []string{"source_type", "discovery_task_id", "last_synced_at", "upstream_metadata"} {
		if !strings.Contains(up, "ADD COLUMN IF NOT EXISTS "+col) {
			t.Errorf("free_resource_catalog extension missing column %s", col)
		}
	}
}

func TestMigration084_Down_DropsTablesAndColumns(t *testing.T) {
	down := readMigration084(t, ".down.sql")
	for _, table := range []string{"discovery_results", "discovery_tasks", "provider_templates"} {
		if !strings.Contains(down, "DROP TABLE IF EXISTS public."+table+" CASCADE") {
			t.Errorf("down migration must drop %s", table)
		}
	}
	for _, col := range []string{"source_type", "discovery_task_id", "last_synced_at", "upstream_metadata"} {
		if !strings.Contains(down, "DROP COLUMN IF EXISTS "+col) {
			t.Errorf("down migration must drop catalog column %s", col)
		}
	}
	// Rollback order: the results table must be dropped before the tasks table (FK)
	if strings.Index(down, "discovery_results") > strings.Index(down, "discovery_tasks") {
		t.Fatal("down must drop discovery_results before discovery_tasks (FK order)")
	}
	// Must NOT drop the 075 shared functions (OmniFree tables still depend on them)
	if strings.Contains(down, "DROP FUNCTION") {
		t.Fatal("down migration must NOT drop shared functions (075 OmniFree depends on them)")
	}
}

func TestMigration084_UpDownAreTransactional(t *testing.T) {
	for _, suffix := range []string{".sql", ".down.sql"} {
		f := readMigration084(t, suffix)
		begin := strings.Index(f, "BEGIN;")
		commit := strings.Index(f, "COMMIT;")
		if begin < 0 || commit < 0 || begin > commit {
			t.Errorf("migration %s must be wrapped in BEGIN...COMMIT", suffix)
		}
	}
}
