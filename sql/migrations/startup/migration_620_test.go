package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration620TenantScope(t *testing.T) {
	migration, err := os.ReadFile("620_provider_error_details_tenant_scope.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(migration))
	for _, fragment := range []string{
		"aggregation_bucket",
		"idx_provider_error_details_tenant_fingerprint",
		"enable row level security",
		"force row level security",
		"app.current_tenant",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration missing %q", fragment)
		}
	}

	down, err := os.ReadFile("620_provider_error_details_tenant_scope.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, fragment := range []string{
		"drop index if exists public.idx_provider_error_details_tenant_fingerprint",
		"drop column if exists aggregation_bucket",
	} {
		if !strings.Contains(downSQL, fragment) {
			t.Errorf("rollback migration missing %q", fragment)
		}
	}
}
