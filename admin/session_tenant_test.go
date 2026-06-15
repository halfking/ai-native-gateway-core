package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTenantLogsClause_SuperAdminNoFilter(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "super_admin", TenantID: "default"})
	frag, args, next := tenantLogsClause(r, 3)
	if frag != "" || len(args) != 0 || next != 3 {
		t.Fatalf("expected no tenant filter for super_admin, got frag=%q args=%v next=%d", frag, args, next)
	}
}

func TestTenantLogsClause_TenantAdminFilter(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
	frag, args, next := tenantLogsClause(r, 3)
	if !strings.Contains(frag, "tenant_id = $3") {
		t.Fatalf("expected tenant_id placeholder, got %q", frag)
	}
	if len(args) != 1 || args[0] != "acme" || next != 4 {
		t.Fatalf("unexpected args/next: %v %d", args, next)
	}
}

func TestBuildMemoraSessionsSQL_TenantScoped(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
	sql, args := buildMemoraSessionsSQL(r, 24, 1, 50)
	if !strings.Contains(sql, "tenant_id = $2") {
		t.Fatalf("expected tenant filter in base CTE, sql snippet missing tenant_id")
	}
	if len(args) != 4 || args[1] != "acme" {
		t.Fatalf("expected 4 args with tenant at index 1, got %v", args)
	}
}
