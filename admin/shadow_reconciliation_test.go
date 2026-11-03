// shadow_reconciliation_test.go — P1-4 转发面的契约（未配 503 / path 改写 /
// 状态透传 / GET-only）。
package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShadowReconciliationNotConfigured503(t *testing.T) {
	t.Setenv("ASM_INTERNAL_ENDPOINT", "")
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/shadow-reconciliation", nil)
	rec := httptest.NewRecorder()
	h.handleShadowReconciliation(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d want 503", rec.Code)
	}
}

func TestShadowReconciliationForwardsAndRewrites(t *testing.T) {
	var gotPath, gotQuery, gotRID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotRID = r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Request-Id")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"verdict":"clean"}`))
	}))
	defer upstream.Close()
	t.Setenv("ASM_INTERNAL_ENDPOINT", upstream.URL)

	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/shadow-reconciliation?window=7d", nil)
	req.Header.Set("X-Request-Id", "req-1")
	rec := httptest.NewRecorder()
	h.handleShadowReconciliation(rec, req)

	if gotPath != "/internal/diagnostics/shadow-reconciliation" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotQuery != "window=7d" {
		t.Fatalf("query not forwarded: %q", gotQuery)
	}
	if gotRID != "req-1" {
		t.Fatalf("X-Request-Id not forwarded: %q", gotRID)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != `{"verdict":"clean"}` {
		t.Fatalf("status/body = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Gw-Upstream") != "asm-shadow-reconciliation" {
		t.Fatal("upstream marker header missing")
	}
}

func TestShadowReconciliationUpstreamErrorPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"asm db down"}`))
	}))
	defer upstream.Close()
	t.Setenv("ASM_INTERNAL_ENDPOINT", upstream.URL)

	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleShadowReconciliation(rec, httptest.NewRequest(http.MethodGet, "/api/admin/shadow-reconciliation", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream 502 must pass through, got %d", rec.Code)
	}
}

func TestShadowReconciliationGETOnly(t *testing.T) {
	t.Setenv("ASM_INTERNAL_ENDPOINT", "http://127.0.0.1:1")
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleShadowReconciliation(rec, httptest.NewRequest(http.MethodPost, "/api/admin/shadow-reconciliation", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d want 405", rec.Code)
	}
}
