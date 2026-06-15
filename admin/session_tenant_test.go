package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTenantLogsClause_NonDefaultTenantAdmin(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{TenantID: "acme", Role: "tenant_admin", IsJWT: true})
	frag, args, next := tenantLogsClause(r, 3)
	if frag == "" || args[0] != "acme" || next != 4 {
		t.Fatalf("expected tenant filter, got frag=%q args=%v next=%d", frag, args, next)
	}
}

func TestTenantLogsClause_DefaultTenantBypass(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{TenantID: "default", Role: "tenant_admin", IsJWT: true})
	frag, args, next := tenantLogsClause(r, 3)
	if frag != "" || len(args) != 0 || next != 3 {
		t.Fatalf("default tenant should bypass isolation, got frag=%q args=%v next=%d", frag, args, next)
	}
}

func TestSessionLogsWhere_TenantAdminScoped(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{TenantID: "acme", Role: "tenant_admin", IsJWT: true})
	clause, args := sessionLogsWhere("task-1", sessionScope{Hours: 24}, r)
	if len(args) != 3 || args[2] != "acme" {
		t.Fatalf("expected tenant in args, got %v", args)
	}
	if !strings.Contains(clause, "tenant_id = $3") {
		t.Fatalf("expected tenant filter in clause, got %q", clause)
	}
}

func TestBuildMemoraSessionsSQL_DefaultTenantNoFilter(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{TenantID: "default", Role: "tenant_admin", IsJWT: true})
	sql, args := buildMemoraSessionsSQL(r, 24, 1, 50)
	if strings.Contains(sql, "tenant_id") {
		t.Fatal("default tenant list query should not filter by tenant_id")
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
}

func TestBuildMemoraSessionsSQL_TenantAdminFilter(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{TenantID: "acme", Role: "tenant_admin", IsJWT: true})
	sql, args := buildMemoraSessionsSQL(r, 24, 1, 50)
	if !strings.Contains(sql, "tenant_id = $2") {
		t.Fatalf("expected tenant filter in SQL, got %q", sql)
	}
	if len(args) != 4 || args[1] != "acme" {
		t.Fatalf("unexpected args: %v", args)
	}
}
