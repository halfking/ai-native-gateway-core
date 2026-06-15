package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminMiddlewareRejectsMissingBearer(t *testing.T) {
	h := &Handler{db: nil, secret: "test-secret"}
	called := false
	handler := h.admin(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for no auth header, got %d", rec.Code)
	}
	if called {
		t.Fatal("handler should not run without auth/db")
	}
}
