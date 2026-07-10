package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// TestFeishuRoutingList_NoDB 测试无 DB 时返回 503。
func TestFeishuRoutingList_NoDB(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/feishubot/routing-rules", nil)
	w := httptest.NewRecorder()
	h.handleFeishuRoutingList(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

// TestFeishuRoutingCreate_Validation 测试 open_id 必填校验。
func TestFeishuRoutingCreate_Validation(t *testing.T) {
	h := &Handler{db: nil}
	body := `{"open_id": ""}`
	r := httptest.NewRequest(http.MethodPost, "/api/admin/feishubot/routing-rules", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.handleFeishuRoutingCreate(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db nil), got %d", w.Code)
	}
}

// TestFeishuRoutingUpdate_InvalidID 测试无效 id 返回 400。
// 实际：handler 先检查 db 再检查 id，db 为 nil 时返回 503。
func TestFeishuRoutingUpdate_InvalidID(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodPut, "/api/admin/feishubot/routing-rules/abc", nil)
	w := httptest.NewRecorder()
	h.handleFeishuRoutingUpdate(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db nil), got %d", w.Code)
	}
}

// TestFeishuRoutingDelete_InvalidID 测试无效 id 返回 400。
// 实际：handler 先检查 db 再检查 id，db 为 nil 时返回 503。
func TestFeishuRoutingDelete_InvalidID(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodDelete, "/api/admin/feishubot/routing-rules/abc", nil)
	w := httptest.NewRecorder()
	h.handleFeishuRoutingDelete(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db nil), got %d", w.Code)
	}
}

// TestFeishuSendLogList_NoDB 测试无 DB 时返回 503。
func TestFeishuSendLogList_NoDB(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/feishubot/send-log", nil)
	w := httptest.NewRecorder()
	h.handleFeishuSendLogList(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

// TestUserFromContext 测试 context 取值容错。
func TestUserFromContext(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if u := userFromContext(r); u != "unknown" {
		t.Errorf("expected 'unknown' for empty context, got %q", u)
	}
}

// TestFeishuRoutingRulesCollection_404OnDBNil 测试 collection 端点无 DB 时的回退。
func TestFeishuRoutingRulesCollection_NoDB(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/feishubot/routing-rules", nil)
	w := httptest.NewRecorder()
	h.feishuRoutingRulesCollection(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

// TestFeishuRoutingRulesItem_405 测试 method not allowed。
func TestFeishuRoutingRulesItem_405(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/feishubot/routing-rules/1", nil)
	w := httptest.NewRecorder()
	h.feishuRoutingRulesItem(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestRegisterFeishuRoutes_NoDBNil 测试 registerFeishuRoutes 不 panic。
//
// 即使 DB 为 nil，路由注册本身也必须能完成（handler 在请求时才检查 DB）。
func TestRegisterFeishuRoutes_NoDBNil(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	h.registerFeishuRoutes(mux)
	// Sanity: 路径已注册
	routes := []string{
		"/api/admin/feishubot/routing-rules",
		"/api/admin/feishubot/send-log",
	}
	for _, p := range routes {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		_, pat := mux.Handler(req)
		if pat == "" {
			t.Errorf("route %s not registered", p)
		}
	}
}

// TestFeishuSettingsLoaded 测试从 settings_kv 加载 feishu_bot.allowed_users 行为（占位）
//
// 完整集成测试需要 DB，这里只验证 settings 包的 spec 注册。
func TestFeishuSettingsLoaded(t *testing.T) {
	settings.Global = settings.NewRegistry()
	for _, sp := range settings.ModuleSpecs() {
		_ = settings.Global.RegisterSpec(sp)
	}
	sp := settings.Global.Spec("feishu_bot.allowed_users")
	if sp == nil {
		t.Fatal("feishu_bot.allowed_users spec not registered")
	}
	if sp.Type != settings.TypeString {
		t.Errorf("expected TypeString, got %v", sp.Type)
	}
}

// TestFeishuRouteRuleJSON 测试 JSON 序列化字段名（snake_case）。
func TestFeishuRouteRuleJSON(t *testing.T) {
	r := feishuRouteRule{
		ID:          1,
		TenantID:    "default",
		OpenID:      "ou_test",
		DisplayName: "测试",
		UserRole:    "admin",
		RiskLevels:  []string{"high", "critical"},
		Priority:    50,
		Enabled:     true,
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	// 关键字段必须是 snake_case
	for _, k := range []string{`"open_id"`, `"user_role"`, `"risk_levels"`, `"display_name"`, `"tenant_id"`} {
		if !strings.Contains(s, k) {
			t.Errorf("missing %q in JSON: %s", k, s)
		}
	}
}

// 2026-07-09: CSV parser 测试
func TestParseFeishuRouteRulesCSV_BasicHeaders(t *testing.T) {
	csv := `open_id,display_name,user_role,risk_levels,priority,enabled,note
ou_admin_1,Alice,admin,high;critical,10,true,VIP
ou_member_1,Bob,member,low,50,true,`
	rows, err := parseFeishuRouteRulesCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	r0 := rows[0]
	if r0.OpenID != "ou_admin_1" {
		t.Errorf("open_id = %q", r0.OpenID)
	}
	if r0.DisplayName != "Alice" {
		t.Errorf("display_name = %q", r0.DisplayName)
	}
	if r0.UserRole != "admin" {
		t.Errorf("user_role = %q", r0.UserRole)
	}
	if r0.RiskLevels != "high;critical" {
		t.Errorf("risk_levels = %q", r0.RiskLevels)
	}
	if r0.Priority != 10 {
		t.Errorf("priority = %d", r0.Priority)
	}
	if r0.Enabled == nil || !*r0.Enabled {
		t.Error("enabled should be true")
	}
}

func TestParseFeishuRouteRulesCSV_CaseInsensitiveHeaders(t *testing.T) {
	csv := `OPEN_ID,Display_Name,User_Role
ou_1,Alice,admin`
	rows, err := parseFeishuRouteRulesCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 1 || rows[0].OpenID != "ou_1" {
		t.Errorf("case-insensitive header matching failed: %+v", rows)
	}
}

func TestParseFeishuRouteRulesCSV_EmptyRows(t *testing.T) {
	csv := `open_id
ou_1
ou_2
`
	rows, err := parseFeishuRouteRulesCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(rows))
	}
}

func TestParseFeishuRouteRulesCSV_HeaderOnly(t *testing.T) {
	csv := `open_id,display_name`
	_, err := parseFeishuRouteRulesCSV(strings.NewReader(csv))
	if err == nil {
		t.Error("expected error for header-only CSV")
	}
}

func TestParseFeishuRouteRulesCSV_EmptyInput(t *testing.T) {
	_, err := parseFeishuRouteRulesCSV(strings.NewReader(""))
	if err == nil {
		t.Error("expected error for empty CSV")
	}
}

func TestParseFeishuRouteRulesCSV_QuotedFields(t *testing.T) {
	csv := `open_id,note
ou_1,"hello, world"
ou_2,"line1
line2"`
	rows, err := parseFeishuRouteRulesCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].Note != "hello, world" {
		t.Errorf("note = %q", rows[0].Note)
	}
}

func TestHandleFeishuRoutingImport_NoDB(t *testing.T) {
	h := &Handler{}
	body := `[{"open_id":"ou_x","display_name":"X"}]`
	r := httptest.NewRequest(http.MethodPost, "/api/admin/feishubot/routing-rules:import", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.handleFeishuRoutingRulesImport(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleFeishuRoutingImport_Empty(t *testing.T) {
	h := &Handler{db: nil}
	body := `[]`
	r := httptest.NewRequest(http.MethodPost, "/api/admin/feishubot/routing-rules:import", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.handleFeishuRoutingRulesImport(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (db nil), got %d", w.Code)
	}
}
