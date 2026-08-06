// Copyright 2026 kaixuan.ai
// Regression tests for nil-db guards on root admin handlers.
//
// 2026-08-06 incident: when the gateway starts in no-DB mode (postgres disabled
// because ApplyMigrations timed out under statement_timeout=30s), the admin
// Handler is constructed with h.db == nil. /api/logs and /api/keys (no
// trailing slash) used to call h.listLogs / h.listKeys directly, hitting
// h.db.Query on a nil *pgxpool.Pool and panicking. The recovery middleware
// converted the panic into `{"code":"panic"}` 500s, breaking the request-logs
// UI. /api/logs/ and /api/keys/ (with trailing slash) already had the
// `h.db == nil → 503 "database not configured"` guard; this test pins the
// same behaviour on the root paths so the inconsistency cannot return.
package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleLogsRoot_DBGuard(t *testing.T) {
	h := &Handler{} // h.db == nil

	req := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	w := httptest.NewRecorder()

	h.handleLogsRoot(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /api/logs with h.db=nil: want 503, got %d body=%s",
			w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "database not configured") {
		t.Errorf("want body to contain 'database not configured', got: %s",
			w.Body.String())
	}
}

func TestHandleKeysRoot_DBGuard(t *testing.T) {
	h := &Handler{} // h.db == nil

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/keys", nil)
			w := httptest.NewRecorder()

			h.handleKeysRoot(w, req)

			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s /api/keys with h.db=nil: want 503, got %d body=%s",
					method, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "database not configured") {
				t.Errorf("want body to contain 'database not configured', got: %s",
					w.Body.String())
			}
		})
	}
}
