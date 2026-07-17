package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppendOwnerFilterForRegularAddsOwnerPredicate(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "acme", Username: "alice", IsJWT: true})
	frag, args, next, added := appendOwnerFilterForRegular(r, "rl", "gw_session_id", 4)
	if !added {
		t.Fatal("expected owner filter to be added for regular user")
	}
	if !strings.Contains(frag, "owner_user = $4") {
		t.Fatalf("expected owner predicate in fragment, got %q", frag)
	}
	if len(args) != 1 || args[0] != "alice" || next != 5 {
		t.Fatalf("unexpected args/next: args=%v next=%d", args, next)
	}
}

func TestAppendOwnerFilterForRegularSkipsAdminTiers(t *testing.T) {
	for _, role := range []string{"super_admin", "admin_key", "tenant_admin"} {
		t.Run(role, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r = SetAuthContext(r, &AuthContext{Role: role, TenantID: "acme", Username: "alice"})
			frag, args, next, added := appendOwnerFilterForRegular(r, "rl", "gw_session_id", 3)
			if added || frag != "" || len(args) != 0 || next != 3 {
				t.Fatalf("admin tier must skip owner filter: frag=%q args=%v next=%d added=%v", frag, args, next, added)
			}
		})
	}
}

func TestAppendOwnerFilterForRegularEmptyUsernameDenies(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "acme", Username: "", IsJWT: true})
	frag, _, _, added := appendOwnerFilterForRegular(r, "rl", "gw_session_id", 3)
	if !added || !strings.Contains(frag, "owner_user = ''") {
		t.Fatalf("empty username should produce unsatisfiable predicate, got added=%v frag=%q", added, frag)
	}
}

func TestBuildWhereClauseIncludesOwnerForRegular(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "acme", Username: "alice", IsJWT: true})
	filters := &analyticsFilters{}
	where, args := buildWhereClause(r, filters, "rl")
	if !strings.Contains(where, "session_dim") {
		t.Fatalf("regular owner filter must join session_dim, got %q", where)
	}
	if !containsAny(args, "alice") {
		t.Fatalf("expected alice owner arg, got %v", args)
	}
}

func TestBuildSessionSummariesWhereClauseIncludesOwnerForRegular(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "acme", Username: "alice", IsJWT: true})
	filters := &analyticsFilters{}
	where, args := buildSessionSummariesWhereClause(r, filters)
	if !strings.Contains(where, "session_dim") {
		t.Fatalf("regular owner filter must reference session_dim, got %q", where)
	}
	if !containsAny(args, "alice") {
		t.Fatalf("expected alice owner arg, got %v", args)
	}
}

func containsAny(args []interface{}, needle string) bool {
	for _, a := range args {
		if s, ok := a.(string); ok && s == needle {
			return true
		}
	}
	return false
}