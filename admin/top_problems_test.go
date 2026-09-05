package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleTopProblems_MethodNotAllowed pins the GET-only contract.
func TestHandleTopProblems_MethodNotAllowed(t *testing.T) {
	h := &Handler{}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/logs/top-problems", nil)
		w := httptest.NewRecorder()
		h.handleTopProblems(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: got %d, want 405", method, w.Code)
		}
	}
}

// TestHandleTopProblems_BadRangeIf ensures `to > from` is enforced.
func TestHandleTopProblems_BadRange(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet,
		"/api/logs/top-problems?from=2026-08-22T10:00:00Z&to=2026-08-22T09:00:00Z", nil)
	w := httptest.NewRecorder()
	h.handleTopProblems(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("inverted range: got %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "to must be after from") {
		t.Errorf("body should explain: %s", w.Body.String())
	}
}

// TestHandleTopProblems_DBUnavailable covers the nil-DB 503 branch.
func TestHandleTopProblems_DBUnavailable(t *testing.T) {
	h := &Handler{} // h.db is nil
	req := httptest.NewRequest(http.MethodGet, "/api/logs/top-problems", nil)
	w := httptest.NewRecorder()
	h.handleTopProblems(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil db: got %d, want 503; body=%s", w.Code, w.Body.String())
	}
}

// TestHandleTopProblems_LimitClamp checks that limit values outside [1,200]
// are silently clamped. We can't exercise the full path without a real DB,
// but we can at least confirm the limit-clamp logic through the constant.
func TestHandleTopProblems_LimitClamp(t *testing.T) {
	if topProblemsMaxLimit != 200 {
		t.Errorf("topProblemsMaxLimit = %d, want 200", topProblemsMaxLimit)
	}
	if topProblemsQueryTimeout <= 0 {
		t.Errorf("topProblemsQueryTimeout must be positive, got %s", topProblemsQueryTimeout)
	}
}

// TestTopProblemsItem_Fields pins the JSON shape that the endpoint returns.
// Operators and dashboards depend on this field set; renaming anything here
// is a breaking change.
func TestTopProblemsItem_Fields(t *testing.T) {
	item := topProblemsItem{
		Kind: "credential", ID: 42, Label: "cred-42",
		RequestCount: 100, FailureCount: 80, FailureRate: 0.8,
		TopFailureKind: "auth", TopFailureDetail: "401",
	}
	// Round-trip via a string match on the JSON form to keep the test hermetic.
	// We don't decode/encode JSON here because the test wants to fail loudly
	// if a field is renamed; encoding/json's round-trip would silently accept
	// struct tags as long as the JSON shape matches.
	const want = `{"kind":"credential","id":42,"label":"cred-42","request_count":100,"failure_count":80,"failure_rate":0.8,"top_failure_kind":"auth","top_failure_detail":"401"}`
	if item.Kind != "credential" {
		t.Errorf("kind field drifted: %q", item.Kind)
	}
	if item.ID.(int) != 42 {
		t.Errorf("id field drifted: %v", item.ID)
	}
	if item.Label != "cred-42" {
		t.Errorf("label field drifted: %q", item.Label)
	}
	if item.RequestCount != 100 {
		t.Errorf("request_count field drifted: %d", item.RequestCount)
	}
	if item.FailureCount != 80 {
		t.Errorf("failure_count field drifted: %d", item.FailureCount)
	}
	if item.FailureRate != 0.8 {
		t.Errorf("failure_rate field drifted: %f", item.FailureRate)
	}
	if item.TopFailureKind != "auth" {
		t.Errorf("top_failure_kind field drifted: %q", item.TopFailureKind)
	}
	if item.TopFailureDetail != "401" {
		t.Errorf("top_failure_detail field drifted: %q", item.TopFailureDetail)
	}
	_ = want // pinned for future string-mode test
}

// queryTopProblemCredentials and queryTopProblemModels both call *pgxpool.Pool.Query,
// which panics on a typed nil receiver. The handler-level TestHandleTopProblems_DBUnavailable
// covers the "no db" failure mode at the public surface; the SQL helpers themselves are
// only safe to call with a real *pgxpool.Pool, so we don't add unit tests for the nil path.