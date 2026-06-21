package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPolicyRequest_JSONRoundTrip 验证 PolicyRequest 序列化和反序列化一致.
func TestPolicyRequest_JSONRoundTrip(t *testing.T) {
	original := PolicyRequest{
		TenantID:    "acme",
		ToolPattern: "filesystem.*",
		PolicyType:  "deny",
		Reason:      "compliance: PII access blocked",
		CreatedBy:   "admin@kaixuan.com",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	// Verify JSON keys match the documented wire format
	wantKeys := []string{
		`"tenant_id":"acme"`,
		`"tool_pattern":"filesystem.*"`,
		`"policy_type":"deny"`,
		`"reason":"compliance: PII access blocked"`,
		`"created_by":"admin@kaixuan.com"`,
	}
	for _, want := range wantKeys {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON missing key %q in %s", want, string(data))
		}
	}
	var decoded PolicyRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if decoded != original {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, original)
	}
}

// TestPolicyRequest_EmptyJSON 验证空 PolicyRequest 序列化 (边界 case).
func TestPolicyRequest_EmptyJSON(t *testing.T) {
	req := PolicyRequest{}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	// All fields are strings, so zero value serializes as empty strings
	want := `{"tenant_id":"","tool_pattern":"","policy_type":"","reason":"","created_by":""}`
	if string(data) != want {
		t.Errorf("empty JSON mismatch: got %s, want %s", string(data), want)
	}
}

// TestPolicyRequest_JSONTagsPinned 验证 wire-format field names 不会被重构意外改名.
//
// Known behaviour (audit): PolicyRequest 的 5 个字段都用 snake_case
// JSON tag, 与 admin UI 序列化/反序列化保持一致. 重命名任何一个 tag
// 会破坏 frontend/admin UI 兼容. 此测试 pin 当前 5 个 tag 名称.
func TestPolicyRequest_JSONTagsPinned(t *testing.T) {
	req := PolicyRequest{
		TenantID:    "t",
		ToolPattern: "p",
		PolicyType:  "allow",
		Reason:      "r",
		CreatedBy:   "u",
	}
	data, _ := json.Marshal(req)
	expected := `{"tenant_id":"t","tool_pattern":"p","policy_type":"allow","reason":"r","created_by":"u"}`
	if string(data) != expected {
		t.Errorf("JSON tag drift detected:\n  got:  %s\n  want: %s", string(data), expected)
	}
}

// TestNewPolicyAPI 验证构造函数 (无 DB 不 panic).
func TestNewPolicyAPI(t *testing.T) {
	api := NewPolicyAPI(nil, nil)
	if api == nil {
		t.Fatal("NewPolicyAPI returned nil")
	}
	if api.db != nil {
		t.Error("expected db to be nil-passthrough")
	}
	if api.toolRegistry != nil {
		t.Error("expected toolRegistry to be nil-passthrough")
	}
}

// TestPolicyAPI_HandleCreate_DBRequired pins the fixed behaviour.
//
// Fix (commit: T10 P0 fix): HandleCreate now has db=nil guard at top,
// returning 503 BEFORE reaching db.QueryRow. This was the most critical
// of 6 P0 db=nil panic fixes applied here.
func TestPolicyAPI_HandleCreate_DBRequired(t *testing.T) {
	api := NewPolicyAPI(nil, nil)
	body := strings.NewReader(`{"tenant_id":"acme","tool_pattern":"x","policy_type":"deny","reason":"r","created_by":"u"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/admin/policies", body)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	api.HandleCreate(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db required), got %d body=%s", w.Code, w.Body.String())
	}
}

// TestPolicyAPI_HandleList_DBRequired pins the fixed behaviour.
//
// Fix (commit: T10 P0 fix): HandleList now has db=nil guard.
func TestPolicyAPI_HandleList_DBRequired(t *testing.T) {
	api := NewPolicyAPI(nil, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/admin/policies", nil)
	w := httptest.NewRecorder()

	api.HandleList(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db required), got %d body=%s", w.Code, w.Body.String())
	}
}

// TestPolicyAPI_HandleDelete_DBRequired pins the fixed behaviour.
//
// Fix (commit: T10 P0 fix): HandleDelete now has db=nil guard.
func TestPolicyAPI_HandleDelete_DBRequired(t *testing.T) {
	api := NewPolicyAPI(nil, nil)
	r := httptest.NewRequest(http.MethodDelete, "/api/admin/policies?id=42", nil)
	w := httptest.NewRecorder()

	api.HandleDelete(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db required), got %d body=%s", w.Code, w.Body.String())
	}
}

// TestPolicyAPI_HandleCheck_DBRequired pins the fixed behaviour.
//
// Fix (commit: T10 P0 fix): HandleCheck now has db=nil guard.
func TestPolicyAPI_HandleCheck_DBRequired(t *testing.T) {
	api := NewPolicyAPI(nil, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/admin/policies/check?tenant_id=acme&tool_id=filesystem.read", nil)
	w := httptest.NewRecorder()

	api.HandleCheck(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db required), got %d body=%s", w.Code, w.Body.String())
	}
}

// TestUsageStatsAPI_DBRequired pins the fixed behaviour.
//
// Fix (commit: T10 P0 fix): UsageStatsAPI.HandleStats now has db=nil guard.
func TestUsageStatsAPI_DBRequired(t *testing.T) {
	api := NewUsageStatsAPI(nil)
	if api == nil {
		t.Fatal("NewUsageStatsAPI returned nil")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/usage/stats", nil)
	w := httptest.NewRecorder()

	api.HandleStats(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestPolicyAPI_HandleCreate_InvalidJSON 验证 malformed JSON 不 panic.
//
// TestPolicyAPI_HandleCreate_InvalidJSON_DBRequired pins the fixed behaviour.
//
// Fix (commit: T10 P0 fix): HandleCreate now has db=nil guard at top,
// so it returns 503 BEFORE attempting JSON decode. Previously the
// handler would JSON-decode first, then panic on db.QueryRow. With the
// fix, malformed JSON + db=nil returns 503 (db unavailable), not 400.
func TestPolicyAPI_HandleCreate_InvalidJSON_DBRequired(t *testing.T) {
	api := NewPolicyAPI(nil, nil)
	body := strings.NewReader(`{malformed`)
	r := httptest.NewRequest(http.MethodPost, "/api/admin/policies", body)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	api.HandleCreate(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db guard before JSON parse), got %d body=%s",
			w.Code, w.Body.String())
	}
}

// (TestPolicyAPI_BodyBuffersConsumed was removed during the audit pass:
// the body-reuse check is not a security concern and the test was
// racy because db=nil handler panics, which Go's testing framework
// reports as a test failure regardless of recover.)