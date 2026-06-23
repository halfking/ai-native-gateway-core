package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── canAdministerTenant (核心安全检查) ──────────────────────────────────

func TestCanAdministerTenant(t *testing.T) {
	tests := []struct {
		name        string
		auth        *AuthContext
		tenantCode  string
		wantAllowed bool
	}{
		{
			name:        "no auth context",
			auth:        nil,
			tenantCode:  "acme",
			wantAllowed: false,
		},
		{
			name:        "super_admin can administer any tenant",
			auth:        &AuthContext{Role: "super_admin", TenantID: "platform"},
			tenantCode:  "acme",
			wantAllowed: true,
		},
		{
			name:        "admin_key can administer any tenant",
			auth:        &AuthContext{Role: "admin_key", TenantID: "platform"},
			tenantCode:  "any_tenant",
			wantAllowed: true,
		},
		{
			name:        "tenant_admin matches own tenant",
			auth:        &AuthContext{Role: "tenant_admin", TenantID: "acme"},
			tenantCode:  "acme",
			wantAllowed: true,
		},
		{
			name:        "tenant_admin denied other tenant (P0 security)",
			auth:        &AuthContext{Role: "tenant_admin", TenantID: "acme"},
			tenantCode:  "competitor",
			wantAllowed: false,
		},
		{
			name:        "tenant_admin with empty url param allowed (list-all case)",
			auth:        &AuthContext{Role: "tenant_admin", TenantID: "acme"},
			tenantCode:  "",
			wantAllowed: true,
		},
		{
			name:        "unknown role denied",
			auth:        &AuthContext{Role: "viewer", TenantID: "acme"},
			tenantCode:  "acme",
			wantAllowed: false,
		},
	}

	h := &Handler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.auth != nil {
				r = SetAuthContext(r, tt.auth)
			}
			if got := h.canAdministerTenant(r, tt.tenantCode); got != tt.wantAllowed {
				t.Errorf("canAdministerTenant(tenant=%q, role=%q) = %v, want %v",
					tt.tenantCode, authRole(tt.auth), got, tt.wantAllowed)
			}
		})
	}
}

// ── auditDetails (helper) ──────────────────────────────────────────────

func TestAuditDetails(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want string // expected compact JSON
	}{
		{
			name: "nil map serialises to {}",
			in:   nil,
			want: "{}",
		},
		{
			name: "empty map serialises to {}",
			in:   map[string]any{},
			want: "{}",
		},
		{
			name: "single key serialises in compact form",
			in:   map[string]any{"policy_id": int64(42)},
			want: `{"policy_id":42}`,
		},
		{
			name: "multiple keys (sorted alphabetically by encoding/json)",
			in:   map[string]any{"reason": "test", "actor": "admin"},
			want: `{"actor":"admin","reason":"test"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := auditDetails(tt.in)
			if got != tt.want {
				t.Errorf("auditDetails(%v) = %q, want %q", tt.in, got, tt.want)
			}
			// Round-trip: must be valid JSON
			var rt map[string]any
			if err := json.Unmarshal([]byte(got), &rt); err != nil {
				t.Errorf("auditDetails output is not valid JSON: %v", err)
			}
		})
	}
}

// ── handleTenantModelPolicies dispatcher (HTTP layer) ───────────────────

func TestHandleTenantModelPolicies_EmptyPathRouting(t *testing.T) {
	// With h.db == nil, every endpoint should return 503 Service Unavailable
	// (the early-return guard in handleTenantModelPolicies). This protects
	// production: if the DB pool is misconfigured at startup, no write can
	// leak through with 200 OK.
	h := &Handler{} // h.db == nil

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/admin/tenants/acme/model-policies"},
		{http.MethodPost, "/api/admin/tenants/acme/model-policies"},
		{http.MethodPatch, "/api/admin/tenants/acme/model-policies/1"},
		{http.MethodDelete, "/api/admin/tenants/acme/model-policies/1"},
		{http.MethodPost, "/api/admin/tenants/acme/model-policies/1/undelete"},
		{http.MethodPost, "/api/admin/tenants/acme/model-policies/check"},
		{http.MethodGet, "/api/admin/tenants/acme/model-policies/audit"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			r := httptest.NewRequest(ep.method, ep.path, nil)
			// super_admin auth so we get past canAdministerTenant
			r = SetAuthContext(r, &AuthContext{Role: "super_admin"})
			w := httptest.NewRecorder()

			h.handleTenantModelPolicies(w, r, "acme")

			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("expected 503 (db not configured), got %d body=%s",
					w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "database not configured") {
				t.Errorf("expected 'database not configured' in body, got: %s", w.Body.String())
			}
		})
	}
}

func TestHandleTenantModelPolicies_MethodNotAllowed(t *testing.T) {
	// PUT is not registered on /model-policies. After the h.db guard we don't
	// reach dispatcher, so 503 wins. If h.db were configured, dispatcher would
	// return 405. This test pins the current behaviour (db guard runs first).
	h := &Handler{}
	r := httptest.NewRequest(http.MethodPut, "/api/admin/tenants/acme/model-policies", nil)
	r = SetAuthContext(r, &AuthContext{Role: "super_admin"})
	w := httptest.NewRecorder()

	h.handleTenantModelPolicies(w, r, "acme")

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 from db guard, got %d", w.Code)
	}
}

// ── handleTenants routing regression (incident 2026-06-23) ─────────────
//
// The bug: handleTenants split the path with SplitN(path, "/", 2), so for
// "/tenants/hansi/model-policies/audit" the sub-resource was the full tail
// "model-policies/audit", which never matched the bare `sub == "model-policies"`
// check. The request then fell through to the generic switch and returned:
//
//   GET  .../model-policies/audit  → 404 "unknown sub-resource: model-policies/audit"
//   POST .../model-policies/check  → 405 "method not allowed" (POST ≠ GET)
//
// handleTenants has a `h.db == nil → 503` guard at the very top, so a
// black-box HTTP test cannot distinguish correct routing from the broken
// routing (both surface as 503 when db is nil). The routing rule itself
// is therefore extracted into isModelPoliciesSubResource and tested as a
// pure function here — this is the test that actually catches the regression.
func TestIsModelPoliciesSubResource(t *testing.T) {
	cases := []struct {
		sub  string
		want bool
		why  string
	}{
		{"model-policies", true, "bare list/create endpoint"},
		{"model-policies/audit", true, "REGRESSION: was false → 'unknown sub-resource: model-policies/audit'"},
		{"model-policies/check", true, "REGRESSION: was false → 'method not allowed' on POST /check"},
		{"model-policies/42", true, "PATCH/DELETE by id"},
		{"model-policies/42/undelete", true, "POST undelete by id"},
		{"model-policies/a/b/c", true, "arbitrary deeper path still belongs to the sub-tree"},

		// Neighbours must NOT be swallowed by an over-broad match.
		{"users", false, "sibling GET /users"},
		{"keys", false, "sibling GET /keys"},
		{"stats", false, "sibling GET /stats"},
		{"", false, "empty sub = tenant root, not model-policies"},
		{"nonexistent-sub", false, "unknown sub-resource must 404"},
		{"model", false, "guard against over-broad prefix (HasPrefix('model'))"},
		{"model-policies-x", false, "sibling sharing the 'model-policies' prefix but not the path segment"},
	}
	for _, tc := range cases {
		t.Run(tc.sub, func(t *testing.T) {
			if got := isModelPoliciesSubResource(tc.sub); got != tc.want {
				t.Errorf("isModelPoliciesSubResource(%q) = %v, want %v (%s)",
					tc.sub, got, tc.want, tc.why)
			}
		})
	}
}

// ── context helpers (defensive: ensure the auth plumbing works) ─────────

func TestAuthContextRoundTrip(t *testing.T) {
	// canAdministerTenant relies on GetAuthContext returning the same value
	// SetAuthContext put in. If this round-trip breaks, all RBAC breaks.
	original := &AuthContext{UserID: 7, TenantID: "acme", Username: "alice", Role: "tenant_admin"}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = SetAuthContext(r, original)

	got := GetAuthContext(r)
	if got == nil {
		t.Fatal("GetAuthContext returned nil after SetAuthContext")
	}
	if got.UserID != 7 || got.TenantID != "acme" || got.Username != "alice" || got.Role != "tenant_admin" {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, original)
	}

	// nil context should not panic
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := GetAuthContext(r2); got != nil {
		t.Errorf("expected nil for no auth, got %+v", got)
	}
}

func TestAuthContextUniquenessKey(t *testing.T) {
	// NOTE: we *cannot* test that two struct{} context keys differ by
	// pointer because the Go compiler may collapse `&struct{}{}` literals
	// to the same address (as we just discovered empirically). The
	// real safety guarantee is that authContextKey is a private type
	// defined in admin/context.go, so external packages literally cannot
	// construct a value of that type to inject into context.WithValue.
	// The test for that is `go vet` + the package boundary itself.
	//
	// This test is kept as documentation of the design intent, not as
	// an actual assertion. It always passes.
	k1 := &authContextKey{}
	k2 := &authContextKey{}
	t.Logf("k1=%p k2=%p (addresses may be equal due to Go struct{} optimisation; safety is enforced by type privacy, not pointer identity)", k1, k2)
}

// ── helpers ────────────────────────────────────────────────────────────

func authRole(a *AuthContext) string {
	if a == nil {
		return "<nil>"
	}
	return a.Role
}
