package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration620ProviderErrorTenantScopeContract(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	startupPath := filepath.Join(root, "sql", "migrations", "startup", "620_provider_error_details_tenant_scope.sql")
	startupDownPath := filepath.Join(root, "sql", "migrations", "startup", "620_provider_error_details_tenant_scope.down.sql")

	startup, err := os.ReadFile(startupPath)
	if err != nil {
		t.Fatal(err)
	}
	startupDown, err := os.ReadFile(startupDownPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{
		"idx_provider_error_details_tenant_fingerprint",
		"COALESCE(tenant_id, '')",
		"COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"provider_error_details_tenant_isolation",
		"migration 616",
		"PostgreSQL 17+",
	} {
		if !strings.Contains(string(startup), contract) {
			t.Errorf("startup migration missing %q", contract)
		}
	}
	if !strings.Contains(string(startupDown), "legacy global fingerprint") {
		t.Error("rollback migration must document legacy fingerprint collision risk")
	}
}
