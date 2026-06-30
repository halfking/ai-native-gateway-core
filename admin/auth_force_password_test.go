package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminMiddleware_RejectsJWTWhenPasswordChangeRequired(t *testing.T) {
	token, _, err := SignToken(42, "tenant-a", "alice", "tenant_admin", "test-secret", true)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	called := false
	mw := AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}, nil, "test-secret")

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	if called {
		t.Fatal("handler should not run when password change is required")
	}
}

func TestAdminMiddleware_AllowsChangePasswordWhenPasswordChangeRequired(t *testing.T) {
	token, _, err := SignToken(42, "tenant-a", "alice", "tenant_admin", "test-secret", true)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	called := false
	mw := AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		auth := GetAuthContext(r)
		if auth == nil || !auth.MustChangePassword {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}, nil, "test-secret")

	req := httptest.NewRequest(http.MethodPut, "/api/auth/change-password", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Fatal("handler should run for change-password route")
	}
}

// PR-4 (2026-06-30): super_admin must also satisfy must_change_password
// gate. Previously only AdminMiddleware checked this; super_admin users
// with must_change_password=true could bypass via Bearer header.
func TestSuperAdminMiddleware_RejectsWhenPasswordChangeRequired(t *testing.T) {
	token, _, err := SignToken(99, "default", "root", "super_admin", "test-secret", true)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	called := false
	mw := SuperAdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}, nil, "test-secret")

	req := httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	if called {
		t.Fatal("handler should not run when super_admin must change password")
	}
}

// PR-4 (2026-06-30): super_admin can still hit the password-change whitelist
// (me / change-password / logout) even with must_change_password=true.
func TestSuperAdminMiddleware_AllowsLogoutWhenPasswordChangeRequired(t *testing.T) {
	token, _, err := SignToken(99, "default", "root", "super_admin", "test-secret", true)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	called := false
	mw := SuperAdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}, nil, "test-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (logout allowed in whitelist), got %d body=%s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Fatal("handler should run for logout even when password change is required")
	}
}

// PR-4 (2026-06-30): AdminMiddleware users can also hit logout
// (previously only me + change-password were allowed).
func TestAdminMiddleware_AllowsLogoutWhenPasswordChangeRequired(t *testing.T) {
	token, _, err := SignToken(42, "tenant-a", "alice", "tenant_admin", "test-secret", true)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	called := false
	mw := AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}, nil, "test-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (logout added to whitelist), got %d body=%s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Fatal("handler should run for logout")
	}
}

// PR-4 (2026-06-30): users without must_change_password=true are not
// affected by the new gate (regression check).
func TestSuperAdminMiddleware_AllowsNormalUsers(t *testing.T) {
	token, _, err := SignToken(99, "default", "root", "super_admin", "test-secret", false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	called := false
	mw := SuperAdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}, nil, "test-secret")

	req := httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Fatal("handler should run for super_admin without password change requirement")
	}
}
