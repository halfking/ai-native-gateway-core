package admin

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientConfigAuditContract(t *testing.T) {
	var entry clientConfigAuditRequest
	if err := json.Unmarshal([]byte(`{"action":"generate","tool":"minimax_code","os":"macos","key_id":42,"model_count":28,"model_scope":"featured"}`), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.KeyID != 42 || entry.ModelCount != 28 {
		t.Fatalf("fields lost: %+v", entry)
	}
	if message := validateClientConfigAudit(entry); message != "" {
		t.Fatal(message)
	}
	for _, tool := range []string{"zcode", "opencode", "minimax_code", "cursor", "cherry_studio", "roocode"} {
		entry.Tool = tool
		if message := validateClientConfigAudit(entry); message != "" {
			t.Fatalf("%s: %s", tool, message)
		}
	}
	for _, mutate := range []func(*clientConfigAuditRequest){func(e *clientConfigAuditRequest) { e.KeyID = 0 }, func(e *clientConfigAuditRequest) { e.ModelCount = 0 }, func(e *clientConfigAuditRequest) { e.Tool = "unknown" }, func(e *clientConfigAuditRequest) { e.ModelScope = "custom" }, func(e *clientConfigAuditRequest) { e.Action = "unknown" }, func(e *clientConfigAuditRequest) { e.OS = "unknown" }} {
		bad := entry
		mutate(&bad)
		if validateClientConfigAudit(bad) == "" {
			t.Fatalf("accepted invalid audit: %+v", bad)
		}
	}
}

func TestClientConfigHandlersFailClosed(t *testing.T) {
	h := &Handler{}
	for _, path := range []string{"/api/client-configs/audit", "/api/client-configs/featured?key_id=42"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		if strings.Contains(path, "featured") {
			h.handleClientConfigFeatured(w, r)
		} else {
			h.handleClientConfigAudit(w, r)
		}
		if w.Code != 403 {
			t.Fatalf("unauthed %s: %d", path, w.Code)
		}
	}
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/client-configs/featured?key_id=42", "", 405},
		{"GET", "/api/client-configs/featured?key_id=0", "", 400},
		{"GET", "/api/client-configs/featured?key_id=42", "", 503},
		{"POST", "/api/client-configs/audit", `{"action":"generate","tool":"minimax_code","os":"macos","key_id":42,"model_count":28,"model_scope":"featured"}`, 503},
		{"POST", "/api/client-configs/audit", `{"action":"generate","tool":"minimax_code","os":"macos","keyId":42,"modelCount":28,"modelScope":"featured"}`, 400},
	} {
		r := SetAuthContext(httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)), &AuthContext{Role: "super_admin", TenantID: "default"})
		w := httptest.NewRecorder()
		if strings.Contains(test.path, "featured") {
			h.handleClientConfigFeatured(w, r)
		} else {
			h.handleClientConfigAudit(w, r)
		}
		if w.Code != test.status {
			t.Fatalf("%s: %d want %d", test.path, w.Code, test.status)
		}
	}
}
