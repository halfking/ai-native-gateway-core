package admin

// provider_access_test.go — tests for ProviderConsoleMiddleware
// (2026-09-04: default 租户 tenant_admin 的供应商控制台角色).
//
// Unit cases run without a database; the provider-row tenant-scope check is
// exercised by the TEST_DATABASE_URL integration test at the bottom.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func mintConsoleToken(t *testing.T, role, tenantID string) string {
	t.Helper()
	tok, _, err := SignToken(42, tenantID, "op", role, "test-secret", false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	return tok
}

func consoleRequest(t *testing.T, method, path, role, tenantID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+mintConsoleToken(t, role, tenantID))
	return req
}

func TestProviderConsoleMiddlewareSuperAdminUnchanged(t *testing.T) {
	called := false
	next := func(w http.ResponseWriter, r *http.Request) {
		called = true
		ctx := GetAuthContext(r)
		if ctx == nil || ctx.Role != "super_admin" {
			t.Fatalf("expected super_admin auth context, got %+v", ctx)
		}
		w.WriteHeader(http.StatusOK)
	}
	mw := ProviderConsoleMiddleware(next, nil, "test-secret")

	// Read and write both pass for super_admin regardless of tenant.
	for _, path := range []string{"/api/providers", "/api/providers/", "/api/providers/24", "/api/providers/24/credentials/3/rotate-primary-key"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
			called = false
			rr := httptest.NewRecorder()
			mw(rr, consoleRequest(t, method, path, "super_admin", "hansi"))
			if rr.Code != http.StatusOK || !called {
				t.Fatalf("super_admin %s %s: expected passthrough 200, got %d (called=%v)", method, path, rr.Code, called)
			}
		}
	}
}

func TestProviderConsoleMiddlewareDefaultTenantAdminMatrix(t *testing.T) {
	reached := false
	next := func(w http.ResponseWriter, r *http.Request) {
		reached = true
		ctx := GetAuthContext(r)
		if ctx == nil || ctx.Role != "tenant_admin" {
			t.Fatalf("expected tenant_admin auth context, got %+v", ctx)
		}
		w.WriteHeader(http.StatusOK)
	}
	mw := ProviderConsoleMiddleware(next, nil, "test-secret")

	tests := []struct {
		name   string
		method string
		path   string
		want   int
		pass   bool
	}{
		{name: "list providers read", method: http.MethodGet, path: "/api/providers", want: http.StatusOK, pass: true},
		{name: "root tree read", method: http.MethodGet, path: "/api/providers/", want: http.StatusOK, pass: true},
		// Numeric provider-id targets require a DB for the tenant-scope check;
		// with no pool wired they fail closed (403). The scoped allow/deny
		// matrix lives in the integration test below. The non-numeric variant
		// here isolates the rotate-primary-key suffix allowlist itself.
		{name: "provider detail read nil db fails closed", method: http.MethodGet, path: "/api/providers/24", want: http.StatusForbidden},
		{name: "rotate suffix allowlist, non-numeric id segment", method: http.MethodPost, path: "/api/providers/x/credentials/3/rotate-primary-key", want: http.StatusOK, pass: true},
		{name: "rotate real path nil db fails closed", method: http.MethodPost, path: "/api/providers/24/credentials/3/rotate-primary-key", want: http.StatusForbidden},
		{name: "create provider denied", method: http.MethodPost, path: "/api/providers", want: http.StatusForbidden},
		{name: "patch credential denied", method: http.MethodPatch, path: "/api/providers/24/credentials/3", want: http.StatusForbidden},
		{name: "delete credential denied", method: http.MethodDelete, path: "/api/providers/24/credentials/3", want: http.StatusForbidden},
		{name: "reveal key denied", method: http.MethodPost, path: "/api/providers/24/credentials/3/reveal", want: http.StatusForbidden},
		{name: "toggle provider denied", method: http.MethodPatch, path: "/api/providers/24/toggle", want: http.StatusForbidden},
		{name: "delete provider denied", method: http.MethodDelete, path: "/api/providers/24", want: http.StatusForbidden},
		{name: "check credential denied", method: http.MethodPost, path: "/api/providers/24/credentials/3/check", want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached = false
			rr := httptest.NewRecorder()
			mw(rr, consoleRequest(t, tt.method, tt.path, "tenant_admin", "default"))
			if rr.Code != tt.want {
				t.Fatalf("expected %d, got %d: %s", tt.want, rr.Code, rr.Body.String())
			}
			if tt.pass != reached {
				t.Fatalf("handler reached=%v, want %v", reached, tt.pass)
			}
		})
	}
}

func TestProviderConsoleMiddlewareNonDefaultTenantAdminRejected(t *testing.T) {
	next := func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be reached for non-default tenant_admin")
	}
	mw := ProviderConsoleMiddleware(next, nil, "test-secret")

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rr := httptest.NewRecorder()
		mw(rr, consoleRequest(t, method, "/api/providers", "tenant_admin", "hansi"))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s: expected 403, got %d", method, rr.Code)
		}
	}
}

func TestProviderConsoleMiddlewareAuthFailures(t *testing.T) {
	next := func(w http.ResponseWriter, r *http.Request) { t.Fatal("handler must not be reached") }
	mw := ProviderConsoleMiddleware(next, nil, "test-secret")

	// No token → 401
	rr := httptest.NewRecorder()
	mw(rr, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}

	// Garbage token → 401
	req := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	req.Header.Set("Authorization", "Bearer not-a-token")
	rr = httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad token, got %d", rr.Code)
	}

	// Unknown role → 403 with the SuperAdminMiddleware message
	rr = httptest.NewRecorder()
	mw(rr, consoleRequest(t, http.MethodGet, "/api/providers", "intern", "default"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unknown role, got %d", rr.Code)
	}
}

func TestProviderConsoleMiddlewareMustChangePasswordGate(t *testing.T) {
	next := func(w http.ResponseWriter, r *http.Request) { t.Fatal("handler must not be reached") }
	mw := ProviderConsoleMiddleware(next, nil, "test-secret")

	tok, _, err := SignToken(42, "default", "op", "tenant_admin", "test-secret", true)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for must_change_password, got %d", rr.Code)
	}
}

// TestProviderConsoleMiddlewareTenantScopesProviderRows covers the
// provider-row tenant scope on /api/providers/{id}/… targets: a
// default-tenant tenant_admin may read and rotate credentials of
// default-tenant providers only; providers on other tenants 403.
func TestProviderConsoleMiddlewareTenantScopesProviderRows(t *testing.T) {
	db := setupTestDB(t)

	code := fmt.Sprintf("pam-test-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var defaultID, otherID int
	err := db.QueryRow(ctx, `
		INSERT INTO providers (tenant_id, code, display_name, protocol, base_url)
		VALUES ('default', $1, 'provider access test', 'openai', 'https://example.com')
		RETURNING id`, code).Scan(&defaultID)
	if err != nil {
		t.Fatalf("seed default provider: %v", err)
	}
	defer db.Exec(context.Background(), `DELETE FROM providers WHERE id = $1`, defaultID)

	err = db.QueryRow(ctx, `
		INSERT INTO providers (tenant_id, code, display_name, protocol, base_url)
		VALUES ('hansi', $1, 'provider access test', 'openai', 'https://example.com')
		RETURNING id`, code+"-x").Scan(&otherID)
	if err != nil {
		t.Fatalf("seed other-tenant provider: %v", err)
	}
	defer db.Exec(context.Background(), `DELETE FROM providers WHERE id = $1`, otherID)

	reached := false
	next := func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(http.StatusOK) }
	mw := ProviderConsoleMiddleware(next, db, "test-secret")

	tests := []struct {
		name   string
		method string
		path   string
		want   int
		pass   bool
	}{
		{name: "read default-tenant provider", method: http.MethodGet, path: fmt.Sprintf("/api/providers/%d", defaultID), want: http.StatusOK, pass: true},
		{name: "read other-tenant provider denied", method: http.MethodGet, path: fmt.Sprintf("/api/providers/%d", otherID), want: http.StatusForbidden},
		{name: "rotate on default-tenant provider passes gate", method: http.MethodPost, path: fmt.Sprintf("/api/providers/%d/credentials/3/rotate-primary-key", defaultID), want: http.StatusOK, pass: true},
		{name: "rotate on other-tenant provider denied", method: http.MethodPost, path: fmt.Sprintf("/api/providers/%d/credentials/3/rotate-primary-key", otherID), want: http.StatusForbidden},
		{name: "patch credential on default-tenant provider denied", method: http.MethodPatch, path: fmt.Sprintf("/api/providers/%d/credentials/3", defaultID), want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached = false
			rr := httptest.NewRecorder()
			mw(rr, consoleRequest(t, tt.method, tt.path, "tenant_admin", "default"))
			if rr.Code != tt.want {
				t.Fatalf("expected %d, got %d: %s", tt.want, rr.Code, rr.Body.String())
			}
			if tt.pass != reached {
				t.Fatalf("handler reached=%v, want %v", reached, tt.pass)
			}
		})
	}
}
