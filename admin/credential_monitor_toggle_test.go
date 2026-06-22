package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// newReq builds a minimal *http.Request. body may be nil.
func newReq(method, path string, body *strings.Reader) *http.Request {
	if body == nil {
		return httptest.NewRequest(method, path, nil)
	}
	return httptest.NewRequest(method, path, body)
}

// rec returns a fresh *httptest.ResponseRecorder.
func rec() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// strReader wraps a string for use as a request body.
func strReader(s string) *strings.Reader { return strings.NewReader(s) }

// repeat returns s repeated n times.
func repeat(s string, n int) string { return strings.Repeat(s, n) }

// errStr renders an error for assertion messages (returns "" for nil).
func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// identityWrap is the no-op middleware used to bypass auth checks in tests.
func identityWrap(h http.HandlerFunc) http.HandlerFunc { return h }

// TestHandleModelToggle_MethodNotAllowed checks the pre-DB method guard.
func TestHandleModelToggle_MethodNotAllowed(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	req := newReq(http.MethodGet, "/api/credentials/model-toggle", nil)
	rr := rec()
	m.handleModelToggle(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestHandleModelToggle_NilDB returns 503 before any validation runs.
func TestHandleModelToggle_NilDB(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	req := newReq(http.MethodPost, "/api/credentials/model-toggle",
		strReader(`{"credential_id":1,"raw_model_name":"m","action":"offline","reason":"r"}`))
	rr := rec()
	m.handleModelToggle(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestHandleModelHistory_MethodNotAllowed checks the pre-DB method guard.
func TestHandleModelHistory_MethodNotAllowed(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	req := newReq(http.MethodPost, "/api/credentials/model-history", nil)
	rr := rec()
	m.handleModelHistory(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestHandleModelHistory_NilDB returns 503 before any validation runs.
func TestHandleModelHistory_NilDB(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	req := newReq(http.MethodGet,
		"/api/credentials/model-history?credential_id=1&raw_model_name=m", nil)
	rr := rec()
	m.handleModelHistory(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestValidateModelToggleRequest covers the pure pre-DB validation path.
func TestValidateModelToggleRequest(t *testing.T) {
	cases := []struct {
		name    string
		req     modelToggleRequest
		wantErr string
	}{
		{"missing credential_id", modelToggleRequest{RawModel: "m", Action: "offline", Reason: "r"}, "credential_id and raw_model_name required"},
		{"missing raw_model_name", modelToggleRequest{CredentialID: 1, Action: "offline", Reason: "r"}, "credential_id and raw_model_name required"},
		{"invalid action", modelToggleRequest{CredentialID: 1, RawModel: "m", Action: "banana", Reason: "r"}, `action must be "online" or "offline"`},
		{"empty action", modelToggleRequest{CredentialID: 1, RawModel: "m", Reason: "r"}, `action must be "online" or "offline"`},
		{"missing reason (empty)", modelToggleRequest{CredentialID: 1, RawModel: "m", Action: "offline", Reason: ""}, "reason is required"},
		{"missing reason (ws)", modelToggleRequest{CredentialID: 1, RawModel: "m", Action: "offline", Reason: "   \t\n"}, "reason is required"},
		{"overlong reason", modelToggleRequest{CredentialID: 1, RawModel: "m", Action: "offline", Reason: repeat("x", 501)}, "reason must be"},
		{"happy offline", modelToggleRequest{CredentialID: 1, RawModel: "m", Action: "offline", Reason: "test"}, ""},
		{"happy online", modelToggleRequest{CredentialID: 1, RawModel: "m", Action: "online", Reason: "test"}, ""},
		{"exactly 500 chars", modelToggleRequest{CredentialID: 1, RawModel: "m", Action: "offline", Reason: repeat("x", 500)}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateModelToggleRequest(&tc.req)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if !strings.Contains(errStr(err), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, errStr(err))
			}
		})
	}
}

// TestModelToggleRequest_JSONShape guards against accidental tag drift.
func TestModelToggleRequest_JSONShape(t *testing.T) {
	in := `{"credential_id":42,"raw_model_name":"gpt-4o","action":"offline","reason":"smoke"}`
	var req modelToggleRequest
	if err := json.Unmarshal([]byte(in), &req); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if req.CredentialID != 42 || req.RawModel != "gpt-4o" || req.Action != "offline" || req.Reason != "smoke" {
		t.Fatalf("field binding drifted: %+v", req)
	}
	b, err := json.Marshal(modelToggleRequest{CredentialID: 7, RawModel: "m", Action: "online", Reason: "x"})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, key := range []string{`"credential_id":7`, `"raw_model_name":"m"`, `"action":"online"`, `"reason":"x"`} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("expected key %s in JSON, got %s", key, string(b))
		}
	}
}

// TestModelHistoryEvent_JSONShape guards the history response's wire format.
func TestModelHistoryEvent_JSONShape(t *testing.T) {
	ev := ModelHistoryEvent{TS: "2026-06-23T10:00:00Z", Source: "auto", Event: "broke"}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, key := range []string{`"ts"`, `"source"`, `"event"`} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("expected key %s in JSON, got %s", key, string(b))
		}
	}
	for _, key := range []string{`"triggered_by"`, `"probe_status"`, `"http_status"`,
		`"error_code"`, `"error_message"`, `"actor"`, `"reason"`} {
		if !strings.Contains(string(b), key+":null") {
			t.Fatalf("expected %s:null in JSON, got %s", key, string(b))
		}
	}
}

// TestModelToggleResponse_JSONShape pins the response wire format.
func TestModelToggleResponse_JSONShape(t *testing.T) {
	resp := ModelToggleResponse{Success: true, Available: false, PrevAvailable: true, Action: "offline"}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, key := range []string{`"success":true`, `"available":false`, `"prev_available":true`, `"action":"offline"`} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("expected key %s in JSON, got %s", key, string(b))
		}
	}
}

// TestModelToggleHandler_RoutesRegistered confirms the new routes are wired
// through RegisterMonitorRoutes. We use mux.Handler(req) instead of ServeHTTP
// because handleMonitorSummary dereferences h.db before any nil-check.
func TestModelToggleHandler_RoutesRegistered(t *testing.T) {
	mux := http.NewServeMux()
	m := &CredentialMonitorHandlers{h: &Handler{}}
	m.RegisterMonitorRoutes(mux, identityWrap)

	for _, path := range []string{
		"/api/credentials/monitor-summary",
		"/api/credentials/sliding-window",
		"/api/credentials/promote",
		"/api/credentials/demote",
		"/api/credentials/set-concurrency-auto",
		"/api/credentials/model-toggle",
		"/api/credentials/model-history",
	} {
		req, _ := http.NewRequest(http.MethodGet, path, nil)
		_, pattern := mux.Handler(req)
		if pattern == "" {
			t.Errorf("route %s is not registered (mux returned empty pattern)", path)
		}
	}
}

// TestHandleModelToggle_OfflineNextRetryNotNull guards the 2026-06-23 P0 fix:
// the offline branch in handleModelToggle must NOT write NULL into
// model_probe_state.next_retry_at (column is NOT NULL). It uses
// NOW() + INTERVAL '100 years' so the probe runner never re-picks a
// manually-offline binding until the operator toggles it back online.
func TestHandleModelToggle_OfflineNextRetryNotNull(t *testing.T) {
	src, err := os.ReadFile("credential_monitor.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	// Both the INSERT and the ON CONFLICT UPDATE branches must use
	// NOW() + INTERVAL '100 years' — never NULL.
	if !strings.Contains(body, "VALUES ($1, $2, 'unknown', 0, 0, 0, NOW(), NOW() + INTERVAL '100 years', 'manual_offline')") {
		t.Errorf("offline INSERT VALUES clause must use NOW() + INTERVAL '100 years' for next_retry_at (not NULL); see 2026-06-23 P0 bug fix")
	}
	offlineIdx := strings.Index(body, "INSERT INTO model_probe_state")
	if offlineIdx < 0 {
		t.Fatalf("could not locate model_probe_state INSERT in source")
	}
	// Find the next-retry-at assignment within the offline ON CONFLICT block.
	conflictIdx := strings.Index(body[offlineIdx:], "ON CONFLICT")
	if conflictIdx < 0 {
		t.Fatalf("could not locate ON CONFLICT clause after offline INSERT")
	}
	endIdx := offlineIdx + conflictIdx + 240
	if endIdx > len(body) {
		endIdx = len(body)
	}
	if !strings.Contains(body[offlineIdx:endIdx], "next_retry_at = NOW() + INTERVAL '100 years'") {
		t.Errorf("offline ON CONFLICT UPDATE must set next_retry_at = NOW() + INTERVAL '100 years' (not NULL); see 2026-06-23 P0 bug fix")
	}
	// And the online branch already used NOW() — make sure we didn't regress it.
	if !strings.Contains(body, "VALUES ($1, $2, 'recovering', 0, 0, 0, NOW(), NOW(), 'manual_online')") {
		t.Errorf("online VALUES clause must keep NOW() for next_retry_at")
	}
}
