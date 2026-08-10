package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleCredentialKeys_InvalidKid verifies that non-numeric / non-positive
// kid_index values are rejected at the routing layer (400) before hitting the
// DB. This is the only routing-logic path testable without a live pgxpool —
// the DB-touching handlers (list/add/delete/reset) require a real pool and
// panic on nil, so they're covered by integration tests against a test DB.
func TestHandleCredentialKeys_InvalidKid(t *testing.T) {
	h := &Handler{}
	for _, tc := range []struct {
		method string
		kid    string
	}{
		{http.MethodDelete, "abc"},
		{http.MethodDelete, "0"},
		{http.MethodDelete, "-1"},
		{http.MethodPatch, "abc"},
		{http.MethodPatch, "0"},
	} {
		t.Run(tc.method+" "+tc.kid, func(t *testing.T) {
			req := httptest.NewRequest(tc.method,
				"/api/providers/1/credentials/2/keys/"+tc.kid, nil)
			w := httptest.NewRecorder()
			h.handleCredentialKeys(w, req, 1, 2)
			if w.Code != http.StatusBadRequest {
				t.Errorf("kid %q: status = %d, want 400 (body=%s)",
					tc.kid, w.Code, w.Body.String())
			}
		})
	}
}

// TestHandleCredentialKeys_MethodNotAllowed verifies that unsupported methods
// on the keys collection get 405 (doesn't touch DB).
func TestHandleCredentialKeys_MethodNotAllowed(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPut,
		"/api/providers/1/credentials/2/keys", nil)
	w := httptest.NewRecorder()
	h.handleCredentialKeys(w, req, 1, 2)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT on keys collection: status = %d, want 405", w.Code)
	}
}

// TestHandleProviderCredentials_KeyItemRouteReachable catches a regression
// where /keys/{kid} was parsed as subPath="keys/{kid}" by the parent router
// but only exact subPath="keys" was dispatched. Use an invalid kid so the test
// stays DB-free while proving the nested handler is reached (400, not 404).
func TestHandleProviderCredentials_KeyItemRouteReachable(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodDelete,
		"/api/providers/1/credentials/2/keys/abc", nil)
	w := httptest.NewRecorder()
	h.handleProviderCredentials(w, req, 1, "2/keys/abc")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("DELETE /keys/{kid}: status = %d, want 400 (body=%s)",
			w.Code, w.Body.String())
	}
}
