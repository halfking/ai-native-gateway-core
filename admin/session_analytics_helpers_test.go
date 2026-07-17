package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEffectiveScopeTenantRespectsRole(t *testing.T) {
	cases := []struct {
		name       string
		auth       *AuthContext
		wantTenant string
	}{
		{name: "super_admin sees all", auth: &AuthContext{Role: "super_admin", TenantID: "acme"}, wantTenant: ""},
		{name: "admin_key sees all", auth: &AuthContext{Role: "admin_key", TenantID: "acme"}, wantTenant: ""},
		{name: "tenant_admin scoped", auth: &AuthContext{Role: "tenant_admin", TenantID: "acme"}, wantTenant: "acme"},
		{name: "regular scoped", auth: &AuthContext{Role: "user", TenantID: "acme", Username: "alice", IsJWT: true}, wantTenant: "acme"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r = SetAuthContext(r, c.auth)
			if got := effectiveScopeTenant(r); got != c.wantTenant {
				t.Fatalf("effectiveScopeTenant = %q, want %q", got, c.wantTenant)
			}
		})
	}
}

func TestOwnerScopeClauseRequiresUsername(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "acme", IsJWT: true, Username: ""})
	fragment, args, nextIdx := ownerScopeClause(r, "sd.owner_user", 1)
	if !strings.Contains(fragment, "= $1") || nextIdx != 2 {
		t.Fatalf("expected unsatisfiable fragment, got fragment=%q args=%v next=%d", fragment, args, nextIdx)
	}
	if len(args) != 1 || args[0] != "" {
		t.Fatalf("expected empty owner arg, got %v", args)
	}
}

func TestOwnerScopeClauseIsEmptyForAdminTiers(t *testing.T) {
	for _, role := range []string{"super_admin", "admin_key", "tenant_admin"} {
		t.Run(role, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r = SetAuthContext(r, &AuthContext{Role: role, TenantID: "acme", Username: "alice"})
			fragment, args, nextIdx := ownerScopeClause(r, "sd.owner_user", 1)
			if fragment != "" || len(args) != 0 || nextIdx != 1 {
				t.Fatalf("admin tier should not append owner filter: fragment=%q args=%v next=%d", fragment, args, nextIdx)
			}
		})
	}
}

func TestOwnerScopeClauseBindsRegularUsername(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "acme", Username: "alice", IsJWT: true})
	fragment, args, nextIdx := ownerScopeClause(r, "sd.owner_user", 4)
	if !strings.Contains(fragment, "= $4") || nextIdx != 5 {
		t.Fatalf("expected owner clause at $4: fragment=%q next=%d", fragment, nextIdx)
	}
	if len(args) != 1 || args[0] != "alice" {
		t.Fatalf("expected owner=alice, got %v", args)
	}
}

func TestTenantSummariesClauseHonoursRole(t *testing.T) {
	cases := []struct {
		name       string
		auth       *AuthContext
		wantHasArg bool
	}{
		{name: "super_admin all tenants", auth: &AuthContext{Role: "super_admin", TenantID: "acme"}, wantHasArg: false},
		{name: "tenant_admin own tenant", auth: &AuthContext{Role: "tenant_admin", TenantID: "acme"}, wantHasArg: true},
		{name: "regular own tenant", auth: &AuthContext{Role: "user", TenantID: "acme", Username: "alice", IsJWT: true}, wantHasArg: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r = SetAuthContext(r, c.auth)
			fragment, args, _ := tenantSummariesClause(r, 3)
			if c.wantHasArg {
				if !strings.Contains(fragment, "= $3") || len(args) != 1 {
					t.Fatalf("expected tenant fragment with arg, got %q %v", fragment, args)
				}
			} else if fragment != "" || len(args) != 0 {
				t.Fatalf("expected empty fragment for super_admin, got %q %v", fragment, args)
			}
		})
	}
}

func TestValidateOrderByColumnCoversSessionSummaries(t *testing.T) {
	for _, col := range []string{"last_request_at", "total_cost_usd", "first_request_at", "total_tokens", "request_count", "quality_score"} {
		if err := ValidateOrderByColumn("session_summaries", col); err != nil {
			t.Errorf("ValidateOrderByColumn(session_summaries, %q) = %v", col, err)
		}
	}
	if err := ValidateOrderByColumn("session_summaries", "evil_column"); err == nil {
		t.Fatal("expected invalid column error")
	}
	if err := ValidateOrderByColumn("session_summaries", "DROP TABLE"); err == nil {
		t.Fatal("expected rejection of injection-style column")
	}
}

func TestReadOnlyTxRejectsNilPool(t *testing.T) {
	if err := withReadOnlyTx(t.Context(), (*pgxpool.Pool)(nil), func(pgx.Tx) error { return nil }); err == nil {
		t.Fatal("expected error for nil pool")
	}
}
