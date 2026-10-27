package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestMonitorSummaryCoreModeSQLShape pins the lightweight query contract
// used by the dashboard settings preload.
//
// ★ 2026-10-27（R168 轮）改判据口径：★ 从「扫源文件的反引号字面量片段」
// ⇒ 改为「判【产物 SQL】」（★ buildMonitorSummarySQL 的返回值）。
//
// # 为什么必须改（★ 旧判据的失效形态，★ 一并留档）
//
// 旧判据在源文件里找**反引号片段**，★ 要求其中某个片段同时
// ① 含 "from credentials c" 且 ② 命中 heavy 集合
// （request_logs_with_current_month / recent_success_rate / percentile_cont /
// credential_model_index_with_current_month / credential_model_call_history）。
//
// ★★ **但 detail 模式的 heavy 聚合根本不在任何「顶层片段」里** —— ★ `modelsSelect`
// （live p95 LATERAL：percentile_cont + request_logs_with_current_month）与
// `aggregatedSuccessJoin`（recent_success_rate）都是**被拼进去**的局部变量，
// 而它们的文本**不含 "from credentials c"** ⇒ ★★ **旧判据从未真正看见过 heavy**
// ⇒ ★★ 它一直是被顶层那处**字面量视图名**「顺带」满足的。
//
// ⇒ ★★ R168 把顶层那处 `total_requests` 从「字面量读视图」改成「底表两臂 helper」
// ⇒ ★★ 那个**巧合**消失 ⇒ ★★ 门红 ⇒ ⇒ ⇒ **⇒ 真因不是「门坏了」，★ 是「判据落在【源文件片段】上」**
// ⇒ ⇒ ⇒ ★★ **拼接进来的内容不在片段里** ⇒ ★★ 任何「扫源码片段」的判据都会在
// ★★ 「有人把字面量换成 helper」时**假红**（★ 本轮就撞上了）。
//
// # 新判据（★ 判据落在产物上，★ 与 R167/R168 的通用式同族）
//
// ★ 直接调用 buildMonitorSummarySQL，取 core / detail 两个模式的**最终 SQL**
// ⇒ ★ 拼接进来的内容**在产物里** ⇒ ★ 判据不再受「字面量 vs helper」影响。
//
// ★★ 并且顺手【加强】了一条：★ **core 产物不得命中任何 heavy 聚合**
// ⇒ ★ 与源码注释「Core mode deliberately avoids request-log and per-model
// ★ rate/latency aggregates」对齐 ⇒ ★★ 旧判据**根本没有这条**
// （★ 旧门只要求「coreFound 且 detailFound」，★ 两个都能在【同一种模式】里被满足）。
//
// ★★ heavy 判据盯的是【聚合/物化】这个语义（COUNT / percentile / rollup 表），
// ★ 而不是「视图名」 ⇒ ★ 将来把 heavy 聚合改走底表两臂时，★ 判据**仍然成立**
// （★ percentile_cont 与两个 rollup 表必然还在）⇒ ★ 门不会因为「优化」而红。
func TestMonitorSummaryCoreModeSQLShape(t *testing.T) {
	// ★ 产物：core 模式（Mode=core）与 detail 模式（CredentialID>0 且非 core）。
	coreSQL := buildMonitorSummarySQL(monitorSummarySQLParams{Mode: "core"})
	detailSQL := buildMonitorSummarySQL(monitorSummarySQLParams{Mode: "detail", CredentialID: 1})

	// ★ heavy = 「重聚合 / 物化」语义，★ 不是「视图名」。
	heavyMarkers := []string{
		"request_logs_with_current_month",
		"recent_success_rate",
		"credential_model_index_with_current_month",
		"credential_model_call_history",
		"percentile_cont",
	}
	hitHeavy := func(sql string) []string {
		var hits []string
		for _, m := range heavyMarkers {
			if strings.Contains(sql, m) {
				hits = append(hits, m)
			}
		}
		return hits
	}

	// ── ① core 模式：必须是「轻」的 ─────────────────────────────────
	coreLower := strings.ToLower(coreSQL)
	if !strings.Contains(coreLower, "from credentials c") {
		t.Error("core 产物必须从 credentials c 起查（找不到 \"from credentials c\"）")
	}
	if !strings.Contains(coreSQL, "model_offers") {
		t.Error("core 产物必须带 per-model 的 model_offers 连接")
	}
	if !strings.Contains(coreSQL, "model_probe_state") &&
		!strings.Contains(coreSQL, "v_node_probe_state_compat") {
		t.Error("core 产物必须读探针状态（model_probe_state / v_node_probe_state_compat）")
	}
	if !strings.Contains(coreSQL, "tenant_id") {
		t.Error("core 产物必须按 tenant_id 做归属守卫")
	}
	// ★★ 加强项（R168 轮新增）：★ core 不得触碰重聚合。
	if hits := hitHeavy(coreSQL); len(hits) > 0 {
		t.Errorf("core 产物命中重聚合 %v —— core 的契约是「不碰 request-log / "+
			"per-model rate+latency 聚合」（★ core 是 dashboard settings 预热的轻路径，"+
			"★ 重聚合会让它变成慢端点）", hits)
	}

	// ── ② detail 模式：必须真的触及 heavy 聚合 ───────────────────────
	if hits := hitHeavy(detailSQL); len(hits) == 0 {
		t.Error("detail 产物没有命中任何重聚合标记（request_logs_with_current_month / " +
			"recent_success_rate / percentile_cont / credential_model_index_with_current_month / " +
			"credential_model_call_history）—— detail 模式的存在意义就是 per-model 的" +
			"成功率与延迟聚合")
	}
}

// TestMonitorSummaryRowsIterationFailure guards the audit fix: when the
// driver reports rows.Err() (network drop, server-side cancellation, etc.)
// the handler must surface 5xx instead of returning an incomplete payload.
//
// 2026-08-19: the iteration now lives in pgxQueryer-parameterized helpers
// (runMonitorSummary / runModelHistory / runCredentialDecisions) so a
// pgxmock pool can drive them. The audit guarantee — that the SQL
// execution path checks rows.Err() before returning — must hold inside the
// helpers, which the HTTP handler then maps to 5xx via the returned error.
func TestMonitorSummaryRowsIterationFailurePropagates(t *testing.T) {
	src, err := os.ReadFile("credential_monitor.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "rows iteration failed") {
		t.Fatal("credential monitor SQL helpers must surface a wrapped rows.Err() (the file no longer logs a rows iteration failure)")
	}
	// Each helper owns its own rows.Next() loop and must check rows.Err()
	// before returning. Anchor on the helper signature so the snippet search
	// window is scoped to *that* helper rather than to a sibling handler or
	// free-floating doc-comment that mentions the same name.
	requiredHelpers := []string{"runMonitorSummary", "runModelHistory", "runCredentialDecisions"}
	requiredSnippet := "if rows.Err() != nil"
	for _, h := range requiredHelpers {
		sig := "func " + h + "("
		idx := strings.Index(body, sig)
		if idx < 0 {
			t.Errorf("helper %s not found in credential_monitor.go", h)
			continue
		}
		tail := strings.Index(body[idx+len(sig):], "\nfunc ")
		end := len(body)
		if tail >= 0 {
			end = idx + len(sig) + tail
		}
		if !strings.Contains(body[idx:end], requiredSnippet) {
			t.Errorf("%s must call rows.Err() before returning results", h)
		}
	}
}

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
//
// 2026-10-19: model-toggle 现在有写侧角色门（superadmin 才可写，
// 见 credential_monitor.go 的写侧角色门注释），未授权者会先得 403。
// 本用例要验的是**过了角色门之后**的 nil-db 分支，故用 super_admin 身份。
func TestHandleModelToggle_NilDB(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	req := newReq(http.MethodPost, "/api/credentials/model-toggle",
		strReader(`{"credential_id":1,"raw_model_name":"m","action":"offline","reason":"r"}`))
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", IsJWT: true})
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
	// 2026-10-16 audit #154: pre-empt the role gate so this test still
	// exercises the nil-DB 503. Production always reaches the handler with
	// an admin role set by AdminMiddleware.
	req = SetAuthContext(req, &AuthContext{Role: "super_admin"})
	rr := rec()
	m.handleModelHistory(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// 2026-10-16 audit #154: role gate on /api/credentials/model-history. Route
// is registered under AdminMiddleware (any authenticated user), but the SQL
// filter is `($4 = ” OR tenant_id = $4)` and tenantID only gets populated
// for tenant_admin (handleModelHistory:1531). A role=user caller passes ""
// through and reads every tenant's history. Restrict to super_admin /
// admin_key / tenant_admin.
func TestHandleModelHistory_UserRoleForbidden(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}} // db intentionally nil
	req := newReq(http.MethodGet,
		"/api/credentials/model-history?credential_id=1&raw_model_name=m", nil)
	req = SetAuthContext(req, &AuthContext{Role: "user", TenantID: "acme"})
	rr := rec()
	m.handleModelHistory(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "administrator role required") {
		t.Fatalf("expected administrator role required, got %s", rr.Body.String())
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
