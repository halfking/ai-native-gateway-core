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
