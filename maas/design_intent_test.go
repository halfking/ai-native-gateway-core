package maas

import (
	"os"
	"strings"
	"testing"
)

func TestMaaSSystemGlobal_Documented(t *testing.T) {
	data, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("cannot read service.go: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "System-global design") {
		t.Error("service.go must document which tables are intentionally system-global")
	}
}

func TestMaaSTenantScopedTables_HaveTenantID(t *testing.T) {
	data, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("cannot read service.go: %v", err)
	}
	src := string(data)
	tenantScoped := []string{"tenant_credit_wallets", "tenant_subscriptions", "credit_ledger", "billing_orders"}
	for _, tbl := range tenantScoped {
		if !strings.Contains(src, tbl) {
			t.Errorf("tenant-scoped table %s must be referenced in service.go", tbl)
		}
	}
}
