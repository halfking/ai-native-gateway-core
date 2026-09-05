// Package admin — session_turns_tree_test.go
//
// V3.3-OBS (2026-08-15) OBS-BE6 单元测试：
//   - 正常分页 + 子请求树组装 + request_type 映射/回退
//   - 空会话 404、跨租户 403
//   - cursor 编解码/防跨会话复用
//   - handler 层鉴权与 DB 缺失路径
//
// DB mock 用 pgxmock（现有 admin 测试基建，见 provider_cred_lifecycle_test.go）。
//
// TODO(12号口径): 归档轮次覆盖 —— session_turns 与 request_logs_hot 双源一致性
// 尚未对齐（本端点以 request_logs_with_current_month 为单一事实源），双源合并
// 一致性断言按 12 号文档"归档覆盖需补齐"口径留待后续任务，不阻塞本任务。
package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// assertNoBodyFields 用反射检查响应结构的 JSON 字段名不含正文类字段（硬约束）。
func assertNoBodyFields(t *testing.T, v any) {
	t.Helper()
	forbidden := map[string]bool{
		"body": true, "prompt": true, "messages": true, "request_body": true,
		"response_body": true, "content": true, "text": true, "inbound_body": true,
		"outbound_body": true,
	}
	typ := reflect.TypeOf(v)
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name := tag
		for _, sep := range []string{",", ";"} {
			if idx := indexAny(tag, sep); idx >= 0 {
				name = tag[:idx]
			}
		}
		if forbidden[name] {
			t.Fatalf("struct %s exposes body-like JSON field %q (hard constraint: metadata only)", typ.Name(), name)
		}
	}
}

func indexAny(s, sep string) int {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}

// ---- querySessionTurnsTree ----

func TestQuerySessionTurnsTree_Normal(t *testing.T) {
	t.Setenv("CURSOR_HMAC_SECRET", "test-secret-key-0123456789012345")
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	latency := 1200
	l80, l30 := 80, 30
	// 主请求页：limit=2 取 3 行 → has_more=true，截断为 2
	// （pgxmock 惯例：可空列用指针值喂给 **int 目标，nil 表示 NULL）
	// 游标比较用 (turn_number, request_id) 两段, turn_number 出现在 ">" 与 "="
	// 两个分支, 故 TurnNumber 在实参中传两次 →
	// 实参 = [SessionID, TenantID, TurnNumber, TurnNumber, RequestID, Limit+1] = 6 个。
	mainRows := pgxmock.NewRows([]string{"turn_number", "request_id", "status", "model", "latency_ms"}).
		AddRow(int64(1), "req_main_1", "success", "glm-4", &l80).
		AddRow(int64(2), "req_main_2", "success", "glm-4.5", &latency).
		AddRow(int64(3), "req_main_3", "pending", "glm-4", nil)
	mock.ExpectQuery("SELECT t.turn_number").
		WithArgs("gw_s1", "acme", int64(0), int64(0), "", 3).
		WillReturnRows(mainRows)
	// 子请求：一条 510 列 title_gen，一条回退 origin_actor（sensitive_check 暂无写入方）
	childRows := pgxmock.NewRows([]string{"parent_request_id", "request_id", "request_status", "latency_ms", "request_type", "origin_actor"}).
		AddRow("req_main_1", "req_title_1", "success", &l80, "title_gen", "auto-title-generator").
		AddRow("req_main_1", "req_sens_1", "success", &l30, "main", "").
		AddRow("req_main_2", "req_sum_2", "failed", nil, "main", "auto-summary-generator")
	mock.ExpectQuery("SELECT parent_request_id").
		WithArgs(pgxmock.AnyArg(), "acme").
		WillReturnRows(childRows)

	res, err := querySessionTurnsTree(context.Background(), mock, sessionTurnsTreeParams{
		SessionID: "gw_s1", TenantID: "acme", Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.NotFound || res.Forbidden {
		t.Fatalf("unexpected not_found=%v forbidden=%v", res.NotFound, res.Forbidden)
	}
	if !res.HasMore {
		t.Fatal("expected has_more=true with limit=2 and 3 rows")
	}
	if len(res.Turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(res.Turns))
	}
	first := res.Turns[0]
	if first.TurnNumber != 1 || first.RequestID != "req_main_1" || first.Status != "success" || first.Model != "glm-4" {
		t.Fatalf("turn 1 mismatch: %+v", first)
	}
	if len(first.ChildRequests) != 2 {
		t.Fatalf("turn 1 expected 2 children, got %d", len(first.ChildRequests))
	}
	if first.ChildRequests[0].RequestType != "title" {
		t.Fatalf("child 0 request_type = %q, want title", first.ChildRequests[0].RequestType)
	}
	if first.ChildRequests[1].RequestType != "other" {
		t.Fatalf("child 1 request_type = %q, want other (fallback, no actor)", first.ChildRequests[1].RequestType)
	}
	second := res.Turns[1]
	if len(second.ChildRequests) != 1 || second.ChildRequests[0].RequestType != "summary" {
		t.Fatalf("turn 2 children mismatch: %+v", second.ChildRequests)
	}
	if second.LatencyMs == nil || *second.LatencyMs != latency {
		t.Fatalf("turn 2 latency mismatch: %v", second.LatencyMs)
	}
	// next key = 最后保留的一条（turn 2）
	if res.NextKey.TurnNumber != 2 || res.NextKey.RequestID != "req_main_2" {
		t.Fatalf("next key mismatch: %+v", res.NextKey)
	}
	// 硬约束：响应结构不含正文字段
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQuerySessionTurnsTree_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("SELECT t.turn_number").
		WithArgs("gw_none", "acme", int64(0), int64(0), "", 21).
		WillReturnRows(pgxmock.NewRows([]string{"turn_number", "request_id", "status", "model", "latency_ms"}))
	mock.ExpectQuery("SELECT tenant_id FROM request_logs_with_current_month").
		WithArgs("gw_none").
		WillReturnError(pgx.ErrNoRows)

	res, err := querySessionTurnsTree(context.Background(), mock, sessionTurnsTreeParams{
		SessionID: "gw_none", TenantID: "acme", Limit: 20,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !res.NotFound || res.Forbidden {
		t.Fatalf("expected NotFound=true, got not_found=%v forbidden=%v", res.NotFound, res.Forbidden)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQuerySessionTurnsTree_CrossTenantForbidden(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// tenant 过滤下主请求为空
	mock.ExpectQuery("SELECT t.turn_number").
		WithArgs("gw_other", "acme", int64(0), int64(0), "", 21).
		WillReturnRows(pgxmock.NewRows([]string{"turn_number", "request_id", "status", "model", "latency_ms"}))
	// 不限租户存在性检查：会话归属 other-tenant
	mock.ExpectQuery("SELECT tenant_id FROM request_logs_with_current_month").
		WithArgs("gw_other").
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id"}).AddRow("other-tenant"))

	res, err := querySessionTurnsTree(context.Background(), mock, sessionTurnsTreeParams{
		SessionID: "gw_other", TenantID: "acme", Limit: 20,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !res.Forbidden || res.NotFound {
		t.Fatalf("expected Forbidden=true, got not_found=%v forbidden=%v", res.NotFound, res.Forbidden)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ---- handler 层 ----

func newTurnsTreeAuthRequest(t *testing.T, method, target, role, tenant string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	return SetAuthContext(r, &AuthContext{TenantID: tenant, Role: role, IsJWT: true})
}

func TestHandleSessionTurnsTree_MethodAndAuth(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/admin/sessions/{id}/turns", h.handleSessionTurnsTree)

	// 非 GET → 405
	r := newTurnsTreeAuthRequest(t, http.MethodPost, "/api/admin/sessions/gw_s1/turns", "super_admin", "default")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", rr.Code, rr.Body.String())
	}

	// 未认证 → 401
	r = httptest.NewRequest(http.MethodGet, "/api/admin/sessions/gw_s1/turns", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}

	// 认证通过但 db 未配置 → 503
	r = newTurnsTreeAuthRequest(t, http.MethodGet, "/api/admin/sessions/gw_s1/turns", "tenant_admin", "acme")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSessionTurnsRouteRegistration_PreservesTreeSpecificity(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	// Keep this registration pair identical to RegisterRoutes: Go 1.22's
	// method/path pattern must continue to win over the legacy subtree.
	mux.HandleFunc("/api/admin/sessions/", h.handleSessionSubrouter)
	mux.HandleFunc("/api/admin/sessions/{id}/turns", h.handleSessionTurnsTree)

	r := newTurnsTreeAuthRequest(t, http.MethodGet, "/api/admin/sessions/gw_s1/turns?cursor=invalid", "tenant_admin", "acme")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "session.pagination_invalid_cursor") {
		t.Fatalf("exact turns route was not handled by tree endpoint: status=%d body=%s", rr.Code, rr.Body.String())
	}

	r = newTurnsTreeAuthRequest(t, http.MethodGet, "/api/admin/sessions/gw_s1/turns/not-a-number", "tenant_admin", "acme")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid turn_no") {
		t.Fatalf("turn detail did not remain on V2 subtree: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleSessionTurnsTree_InvalidCursor(t *testing.T) {
	t.Setenv("CURSOR_HMAC_SECRET", "test-secret-key-0123456789012345")
	h := &Handler{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/admin/sessions/{id}/turns", h.handleSessionTurnsTree)
	r := newTurnsTreeAuthRequest(t, http.MethodGet, "/api/admin/sessions/gw_s1/turns?cursor=not-base64!!!", "super_admin", "default")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ---- request_type 映射 ----

func TestNormalizeChildRequestType(t *testing.T) {
	cases := []struct {
		requestType, actor, want string
	}{
		{"title_gen", "", "title"},
		{"summary", "", "summary"},
		{"sensitive_check", "", "sensitive_word"},
		{"compression", "", "compression"},
		{"other", "", "other"},
		// 回退：request_type 缺失/为 main → origin_actor（X-Gw-Source-Actor 落库列）
		{"main", "auto-title-generator", "title"},
		{"main", "auto-summary-generator", "summary"},
		{"main", "session-summary", "summary"},
		{"main", "", "other"},
		{"", "auto-title-generator", "title"},
		{"", "", "other"},
	}
	for _, c := range cases {
		if got := normalizeChildRequestType(c.requestType, c.actor); got != c.want {
			t.Errorf("normalizeChildRequestType(%q,%q) = %q, want %q", c.requestType, c.actor, got, c.want)
		}
	}
}

// ---- cursor ----

func TestSessionTurnsTreeCursor_RoundTrip(t *testing.T) {
	t.Setenv("CURSOR_HMAC_SECRET", "test-secret-key-0123456789012345")
	key := sessionTurnsTreeCursor{TurnNumber: 7, RequestID: "req_main_7"}
	encoded, err := encodeSessionTurnsTreeCursor(key, "gw_s1")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := parseSessionTurnsTreeCursor(encoded, "gw_s1")
	if err != nil {
		t.Fatal(err)
	}
	if decoded.TurnNumber != 7 || decoded.RequestID != "req_main_7" {
		t.Fatalf("round-trip mismatch: %+v", decoded)
	}

	// 跨会话复用 → 拒绝
	if _, err := parseSessionTurnsTreeCursor(encoded, "gw_s2"); err == nil {
		t.Fatal("expected error for cross-session cursor reuse")
	}

	// 篡改（错误密钥重签）→ 拒绝
	t.Setenv("CURSOR_HMAC_SECRET", "another-secret-key-012345678901234")
	if _, err := parseSessionTurnsTreeCursor(encoded, "gw_s1"); err == nil {
		t.Fatal("expected signature verification failure with different secret")
	}

	// 非法载荷 → 拒绝
	if _, err := parseSessionTurnsTreeCursor("dHVybnN8YWJj", "gw_s1"); err == nil {
		t.Fatal("expected error for malformed cursor payload")
	}
}

// TestSessionTurnsTreeItem_NoBodyFields 硬约束：响应结构只含元数据字段，
// 禁止出现正文（body/prompt/messages）字段。
func TestSessionTurnsTreeItem_NoBodyFields(t *testing.T) {
	turn := SessionTurnTreeItem{
		TurnNumber:    1,
		RequestID:     "req_1",
		Status:        "success",
		Model:         "glm-4",
		ChildRequests: []*SessionChildRequest{{RequestID: "req_c", RequestType: "title", Status: "success"}},
	}
	assertNoBodyFields(t, &turn)
	assertNoBodyFields(t, turn.ChildRequests[0])
}
