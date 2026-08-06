package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionListQueryUsesAuthenticatedTenantAndOwner(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/sessions?tenant_id=attacker&project_id=p1", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "acme", Username: "alice", IsJWT: true})
	filters := parseSessionFilters(r)
	query, args := buildSessionListQueryForRequest(r, filters)
	if filters.TenantID != "acme" {
		t.Fatalf("tenant_id query parameter overrode authenticated tenant: %q", filters.TenantID)
	}
	if !strings.Contains(query, "ss.tenant_id = $1") || !strings.Contains(query, "sd.owner_user = $2") {
		t.Fatalf("missing authenticated scope in query: %s", query)
	}
	if len(args) < 3 || args[0] != "acme" || args[1] != "alice" || args[2] != "p1" {
		t.Fatalf("unexpected query args: %#v", args)
	}
}

func TestSessionListQueryAdminHasNoOwnerScope(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/sessions?tenant_id=ignored", nil)
	r = SetAuthContext(r, &AuthContext{Role: "super_admin", TenantID: "acme"})
	query, args := buildSessionListQueryForRequest(r, parseSessionFilters(r))
	if strings.Contains(query, "owner_user") || strings.Contains(query, "tenant_id = $1") || len(args) != 0 {
		t.Fatalf("super admin unexpectedly scoped: query=%s args=%#v", query, args)
	}
}

func TestScopedDimensionWhereFiltersBeforeAggregation(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/sessions/task-flow/t1?tenant_id=attacker", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "acme", Username: "alice", IsJWT: true})
	where, args := scopedDimensionWhere(r, "sd.task_id", "t1")
	if len(where) != 3 || !strings.Contains(where[1], "ss.tenant_id") || !strings.Contains(where[2], "sd.owner_user") {
		t.Fatalf("unexpected scoped where: %#v", where)
	}
	if len(args) != 3 || args[0] != "t1" || args[1] != "acme" || args[2] != "alice" {
		t.Fatalf("unexpected scoped args: %#v", args)
	}
}
