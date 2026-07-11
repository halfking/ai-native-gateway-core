package dashboardapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPrepareDashboardRequestRejectsMissingAuthAndDatabase(t *testing.T) {
	t.Run("missing auth", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/dashboard?tenant_id=other", nil)

		if _, _, ok := prepareDashboardRequest(w, r, nil); ok {
			t.Fatal("prepareDashboardRequest() ok = true, want false")
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
		}
	})

	t.Run("nil database", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		r = r.WithContext(SetAuthInfoToContext(r.Context(), AuthInfo{UserRole: "admin_key"}))

		if _, _, ok := prepareDashboardRequest(w, r, nil); ok {
			t.Fatal("prepareDashboardRequest() ok = true, want false")
		}
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
		}
	})
}

func TestNormalizeDashboardScope(t *testing.T) {
	tests := []struct {
		name string
		auth AuthInfo
		want string
	}{
		{name: "super admin override", auth: AuthInfo{TenantID: "own", UserRole: "super_admin"}, want: "other"},
		{name: "admin key override", auth: AuthInfo{TenantID: "default", UserRole: "admin_key"}, want: "other"},
		{name: "tenant admin own tenant", auth: AuthInfo{TenantID: "own", UserRole: "tenant_admin"}, want: "own"},
		{name: "ordinary user own tenant", auth: AuthInfo{TenantID: "own", UserRole: "user", IsJWT: true}, want: "own"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := normalizeDashboardScope(QueryParams{TenantID: "other"}, tt.auth)
			if params.TenantID != tt.want {
				t.Fatalf("TenantID = %q, want %q", params.TenantID, tt.want)
			}
		})
	}
}

func TestAppendExecutionScope(t *testing.T) {
	params := normalizeDashboardScope(QueryParams{TenantID: "other"}, AuthInfo{
		TenantID: "tenant-a", UserRole: "user", Username: "alice", IsJWT: true,
	})
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	appendExecutionScope(&where, params, &args, &argIdx, "e")

	if len(where) != 2 {
		t.Fatalf("where = %#v, want tenant and owner predicates", where)
	}
	if where[0] != "e.tenant_id = $1" {
		t.Fatalf("tenant predicate = %q", where[0])
	}
	wantOwner := "EXISTS (SELECT 1 FROM session_summaries s WHERE s.session_key = e.gw_session_id AND s.owner_user = $2)"
	if where[1] != wantOwner {
		t.Fatalf("owner predicate = %q, want %q", where[1], wantOwner)
	}
	if len(args) != 2 || args[0] != "tenant-a" || args[1] != "alice" {
		t.Fatalf("args = %#v, want [tenant-a alice]", args)
	}
}

func TestBuildOwnerWhereForOrdinaryUser(t *testing.T) {
	args := []interface{}{}
	argIdx := 1
	clause := buildOwnerWhere(AuthInfo{UserRole: "user", IsJWT: true, Username: "alice"}, &args, &argIdx, "s")
	if clause != "s.owner_user = $1" {
		t.Fatalf("clause = %q, want %q", clause, "s.owner_user = $1")
	}
	if len(args) != 1 || args[0] != "alice" {
		t.Fatalf("args = %#v, want [alice]", args)
	}
}
