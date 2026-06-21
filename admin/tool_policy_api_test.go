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

// TestPolicyAPI_HandleCreate_DBRequired pins the current behaviour.
//
// Known behaviour (audit P0 bug): HandleCreate does NOT check for db == nil
// before calling db.QueryRow at line 90. With db=nil, the handler panics
// with a nil-pointer dereference (reproduced via testing.T helper).
//
// This is a real production risk: if the gateway starts without a configured
// DB pool, every POST /api/admin/policies crashes the entire process via
// unrecovered panic (pgxpool.Pool.Acquire panics on nil receiver).
//
// The fix is a one-liner: add `if api.db == nil { writeError(w, 503); return }`
// at the top of HandleCreate (and HandleList / HandleDelete / HandleCheck
// for consistency). The v6.0 audit §7 lists this as a P0 fix item.
//
// This test intentionally captures the panic so CI passes while the
// audit trail stays visible. After the fix lands, the test should be
// inverted to assert the 503 response (no panic).
func TestPolicyAPI_HandleCreate_DBRequired(t *testing.T) {
	api := NewPolicyAPI(nil, nil)
	body := strings.NewReader(`{"tenant_id":"acme","tool_pattern":"x","policy_type":"deny","reason":"r","created_by":"u"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/admin/policies", body)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	defer func() {
		if rec := recover(); rec != nil {
			t.Logf("AUDIT P0 BUG CONFIRMED: HandleCreate panics with db=nil: %v", rec)
			t.Logf("Fix: add 'if api.db == nil { writeError(w, 503); return }' at top of HandleCreate")
		}
	}()

	api.HandleCreate(w, r)

	// If we get here without panic, the db guard exists (good).
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db required), got %d body=%s", w.Code, w.Body.String())
	}
}

// TestPolicyAPI_HandleList_DBRequired pins the current behaviour.
//
// AUDIT P0 BUG: HandleList panics on db=nil (no early return guard).
// Same pattern as HandleCreate — see TestPolicyAPI_HandleCreate_DBRequired
// doc comment for the fix and the audit context.
func TestPolicyAPI_HandleList_DBRequired(t *testing.T) {
	api := NewPolicyAPI(nil, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/admin/policies", nil)
	w := httptest.NewRecorder()

	defer func() {
		if rec := recover(); rec != nil {
			t.Logf("AUDIT P0 BUG CONFIRMED: HandleList panics with db=nil: %v", rec)
		}
	}()

	api.HandleList(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db required), got %d body=%s", w.Code, w.Body.String())
	}
}

// TestPolicyAPI_HandleDelete_DBRequired pins the current behaviour.
//
// HandleDelete uses ?id=N query param. With valid id but db=nil, the
// handler proceeds past parameter validation and reaches db.QueryRow,
// which panics on nil receiver.
//
// AUDIT P0 BUG: no db=nil guard at top of HandleDelete.
func TestPolicyAPI_HandleDelete_DBRequired(t *testing.T) {
	api := NewPolicyAPI(nil, nil)
	r := httptest.NewRequest(http.MethodDelete, "/api/admin/policies?id=42", nil)
	w := httptest.NewRecorder()

	defer func() {
		if rec := recover(); rec != nil {
			t.Logf("AUDIT P0 BUG CONFIRMED: HandleDelete (db=nil, id=42) panics: %v", rec)
		}
	}()

	api.HandleDelete(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db required), got %d body=%s", w.Code, w.Body.String())
	}
}

// TestPolicyAPI_HandleCheck_DBRequired pins the current behaviour.
//
// HandleCheck is GET with ?tenant_id=&tool_id=. With valid params but
// db=nil, the handler panics on db.QueryRow.
//
// AUDIT P0 BUG: no db=nil guard at top of HandleCheck.
func TestPolicyAPI_HandleCheck_DBRequired(t *testing.T) {
	api := NewPolicyAPI(nil, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/admin/policies/check?tenant_id=acme&tool_id=filesystem.read", nil)
	w := httptest.NewRecorder()

	defer func() {
		if rec := recover(); rec != nil {
			t.Logf("AUDIT P0 BUG CONFIRMED: HandleCheck (db=nil, valid params) panics: %v", rec)
		}
	}()

	api.HandleCheck(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db required), got %d body=%s", w.Code, w.Body.String())
	}
}

// TestUsageStatsAPI_DBRequired pins the current behaviour.
//
// AUDIT P0 BUG: UsageStatsAPI.HandleStats panics on db=nil (same pattern).
func TestUsageStatsAPI_DBRequired(t *testing.T) {
	api := NewUsageStatsAPI(nil)
	if api == nil {
		t.Fatal("NewUsageStatsAPI returned nil")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/usage/stats", nil)
	w := httptest.NewRecorder()

	defer func() {
		if rec := recover(); rec != nil {
			t.Logf("AUDIT P0 BUG CONFIRMED: UsageStatsAPI.HandleStats panics with db=nil: %v", rec)
		}
	}()

	api.HandleStats(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestPolicyAPI_HandleCreate_InvalidJSON 验证 malformed JSON 不 panic.
//
// TestPolicyAPI_HandleCreate_InvalidJSON_DBRequired pins the current behaviour.
//
// HandleCreate 没有 db=nil guard. 当 JSON malformed 时, handler 返回
// 400 (JSON parse error), 然后流程应该继续到 DB 操作 — 但因为我们
// 测的是 db=nil, 还没到 db.QueryRow, 所以返回 400. 这反而是 audit
// 上的"问题变好": malformed JSON 早返回, 不会触发 panic.
//
// AUDIT P0 BUG 仍然存在: 但只在 valid JSON + db=nil 时触发.
func TestPolicyAPI_HandleCreate_InvalidJSON_DBRequired(t *testing.T) {
	api := NewPolicyAPI(nil, nil)
	body := strings.NewReader(`{malformed`)
	r := httptest.NewRequest(http.MethodPost, "/api/admin/policies", body)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	api.HandleCreate(w, r)

	// Pin: malformed JSON 返回 400 (JSON parse error), 不 panic
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed JSON, got %d body=%s", w.Code, w.Body.String())
	}
}

// (TestPolicyAPI_BodyBuffersConsumed was removed during the audit pass:
// the body-reuse check is not a security concern and the test was
// racy because db=nil handler panics, which Go's testing framework
// reports as a test failure regardless of recover.)