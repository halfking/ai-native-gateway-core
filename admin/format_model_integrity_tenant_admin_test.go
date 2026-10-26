package admin

// 批 157 (2026-10-16) — dual-role middleware coverage for
// /api/admin/format-anomalies* and /api/admin/model-integrity*.
// The new middlewares accept super_admin / admin_key (full access via
// bypass) AND tenant_admin (read-only via RLS policy isolation).
// This file verifies the JWT-dispatch layer; the SQL/helper layer is
// covered by the existing format_anomalies / model_integrity tests
// running through the same package.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFormatAnomalyTenantAdminMiddleware_AllowsSuperAdmin(t *testing.T) {
	token, _, err := SignToken(1, "default", "admin", "super_admin", "test-secret-fa-1", false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	var nextCalled bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})
	mw := FormatAnomalyTenantAdminMiddleware(next, nil, "test-secret-fa-1")
	req := httptest.NewRequest("GET", "/api/admin/format-anomalies", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("super_admin: got %d, want 200 body=%s", rr.Code, rr.Body.String())
	}
	if !nextCalled {
		t.Error("next handler should have been called")
	}
}

func TestFormatAnomalyTenantAdminMiddleware_AllowsTenantAdmin(t *testing.T) {
	// tenant_admin with a real tenantID — the middleware refuses
	// tenant_admin without a tenant binding (defense in depth).
	token, _, err := SignToken(7, "acme", "ops", "tenant_admin", "test-secret-fa-2", false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	var nextCalled bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})
	mw := FormatAnomalyTenantAdminMiddleware(next, nil, "test-secret-fa-2")
	req := httptest.NewRequest("GET", "/api/admin/format-anomalies", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("tenant_admin with tenant: got %d, want 200 body=%s", rr.Code, rr.Body.String())
	}
	if !nextCalled {
		t.Error("next handler should have been called")
	}
}

func TestFormatAnomalyTenantAdminMiddleware_RejectsUserRole(t *testing.T) {
	// user role is neither super_admin nor tenant_admin → 403.
	token, _, err := SignToken(99, "default", "joe", "user", "test-secret-fa-3", false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	var nextCalled bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})
	mw := FormatAnomalyTenantAdminMiddleware(next, nil, "test-secret-fa-3")
	req := httptest.NewRequest("GET", "/api/admin/format-anomalies", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("user: got %d, want 403", rr.Code)
	}
	if nextCalled {
		t.Error("next handler should NOT have been called")
	}
}

func TestFormatAnomalyTenantAdminMiddleware_RejectsTenantAdminWithoutTenant(t *testing.T) {
	// tenant_admin JWT with empty tenantID must be refused — bypass
	// branch would be reached if EffectiveTenantIDAll returned "",
	// violating the withTenantOrAllReadOnlyTx authorization premise.
	token, _, err := SignToken(8, "", "ops-no-tenant", "tenant_admin", "test-secret-fa-4", false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	mw := FormatAnomalyTenantAdminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil, "test-secret-fa-4")
	req := httptest.NewRequest("GET", "/api/admin/format-anomalies", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("tenant_admin without tenant: got %d, want 403", rr.Code)
	}
}

func TestFormatAnomalyTenantAdminMiddleware_AllowsAdminKey(t *testing.T) {
	// admin_key inherits super_admin's full-access path.
	token, _, err := SignToken(2, "default", "key", "admin_key", "test-secret-fa-5", false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	mw := FormatAnomalyTenantAdminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil, "test-secret-fa-5")
	req := httptest.NewRequest("GET", "/api/admin/format-anomalies", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("admin_key: got %d, want 200", rr.Code)
	}
}

func TestModelIntegrityTenantAdminMiddleware_AllowsSuperAdmin(t *testing.T) {
	token, _, err := SignToken(1, "default", "admin", "super_admin", "test-secret-mi-1", false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	mw := ModelIntegrityTenantAdminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil, "test-secret-mi-1")
	req := httptest.NewRequest("GET", "/api/admin/model-integrity", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("super_admin: got %d, want 200", rr.Code)
	}
}

func TestModelIntegrityTenantAdminMiddleware_AllowsTenantAdmin(t *testing.T) {
	token, _, err := SignToken(7, "acme", "ops", "tenant_admin", "test-secret-mi-2", false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	mw := ModelIntegrityTenantAdminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil, "test-secret-mi-2")
	req := httptest.NewRequest("GET", "/api/admin/model-integrity", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("tenant_admin with tenant: got %d, want 200", rr.Code)
	}
}

func TestModelIntegrityTenantAdminMiddleware_RejectsUserRole(t *testing.T) {
	token, _, err := SignToken(99, "default", "joe", "user", "test-secret-mi-3", false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	mw := ModelIntegrityTenantAdminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil, "test-secret-mi-3")
	req := httptest.NewRequest("GET", "/api/admin/model-integrity", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("user: got %d, want 403", rr.Code)
	}
}

// TestHandleFormatAnomalySubrouter_TenantAdminResolve_403 — the resolve
// write path stays super_admin-only; tenant_admin must NOT be able to
// mark an anomaly as resolved. Auth context is set up by hand to
// simulate the middleware-allowed tenant_admin reaching the subrouter.
func TestHandleFormatAnomalySubrouter_TenantAdminResolve_403(t *testing.T) {
	h := &Handler{db: nil}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/format-anomalies/123/resolve", nil)
	req.Header.Set("Content-Type", "application/json")
	// tenant_admin role reaches the subrouter (middleware allowed),
	// but the subrouter-level gate must refuse.
	authReq := SetAuthContext(req, &AuthContext{
		UserID:   7,
		TenantID: "acme",
		Username: "ops",
		Role:     "tenant_admin",
		IsJWT:    true,
	})
	rec := httptest.NewRecorder()
	h.handleFormatAnomalySubrouter(rec, authReq)
	if rec.Code != http.StatusForbidden {
		t.Errorf("tenant_admin resolve: got %d, want 403 body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleModelIntegrity_TenantAdminResolve_403 — same shape for
// model-integrity's resolve path. The dispatcher gates resolve on
// super_admin before the sub-handler runs.
func TestHandleModelIntegrity_TenantAdminResolve_403(t *testing.T) {
	h := &Handler{db: nil}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/model-integrity/events/123/resolve", nil)
	req.Header.Set("Content-Type", "application/json")
	authReq := SetAuthContext(req, &AuthContext{
		UserID:   7,
		TenantID: "acme",
		Username: "ops",
		Role:     "tenant_admin",
		IsJWT:    true,
	})
	rec := httptest.NewRecorder()
	h.handleModelIntegrity(rec, authReq)
	if rec.Code != http.StatusForbidden {
		t.Errorf("tenant_admin resolve: got %d, want 403 body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleModelIntegrity_TenantAdminResolve_AllowsSuperAdmin —
// counter-positive: the resolve path still works for super_admin,
// proving the gate is role-conditional and not blanket-closed.
func TestHandleModelIntegrity_TenantAdminResolve_AllowsSuperAdmin(t *testing.T) {
	h := &Handler{db: nil}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/model-integrity/events/123/resolve", nil)
	req.Header.Set("Content-Type", "application/json")
	authReq := SetAuthContext(req, &AuthContext{
		UserID:   1,
		TenantID: "default",
		Username: "admin",
		Role:     "super_admin",
		IsJWT:    true,
	})
	rec := httptest.NewRecorder()
	h.handleModelIntegrity(rec, authReq)
	// super_admin passes the gate; the sub-handler then sees db=nil
	// and returns 503 (no DB), not 403. This proves the gate accepts
	// super_admin and only blocks tenant_admin.
	if rec.Code == http.StatusForbidden {
		t.Errorf("super_admin resolve: got 403, want non-403 body=%s", rec.Body.String())
	}
}
