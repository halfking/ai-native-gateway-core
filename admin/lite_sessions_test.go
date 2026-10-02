package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/storage"
)
// R31 tests: reader wired → list passthrough; reader absent (full mode) →
// explicit 503 with the capability hint.
func TestLiteSessionsEndpoint(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/lite/sessions?tenant_id=t1&limit=5", nil)
	rec := httptest.NewRecorder()
	h.handleLiteSessions(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("full mode = %d, want 503 with hint (body=%s)", rec.Code, rec.Body.String())
	}

	h.SetLiteSessionsReader(func(_ context.Context, tenantID string, opts *storage.ListOptions) ([]*storage.Session, error) {
		if tenantID != "t1" || opts.Limit != 5 {
			t.Fatalf("reader args = %q/%d", tenantID, opts.Limit)
		}
		return []*storage.Session{{ID: "s1", TenantID: tenantID}}, nil
	})
	rec = httptest.NewRecorder()
	h.handleLiteSessions(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"s1"`) || !strings.Contains(rec.Body.String(), `"tenant_id":"t1"`) {
		t.Fatalf("lite list = %d %s", rec.Code, rec.Body.String())
	}
}
