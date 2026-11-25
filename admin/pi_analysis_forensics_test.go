package admin

// pi_analysis_forensics_test.go — 取证轮端点的钉测（2026-11-25）。
//
// 覆盖七件事：
//  1. 修正方案视图：能不能自动修正由后端单点判定（四道闸的顺序与 ApplyFix 一致）；
//  2. 租户校验：带出请求明细的取证端点只放行超级管理员或同租户；
//  3. 写动作只收超级管理员（2026-11-26）：tenant_admin 在派发点被挡下，且不读队列行；
//  4. 非超级管理员的服务日志收窄到本请求（2026-11-26）：日志缓冲是全实例共用的；
//  5. 按需取证：每一路独立降级、一路失败不影响其余、JSON 形状稳定（空列表是 [] 不是 null）；
//  6. 复测聚合：结论由可验证的检查项聚合——一项都定不了时是 inconclusive，不是 pass；
//  7. 重解析：单条的错误映射；批量只动「解析失败」的行且读不出的一条不改。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// piQueueCols 是 GetQueueItem 的投影列（与 promptinjection.queuedErrorColumns 同序）。
func piQueueCols() []string {
	return []string{
		"id", "error_type", "error_hash", "priority",
		"tenant_id", "session_id", "request_id",
		"input_text", "input_hash", "error_message",
		"error_details", "detection_layers", "matched_rules", "policy_config",
		"status", "retry_count", "max_retries",
		"created_at", "started_at", "completed_at", "metadata",
	}
}

type piQueueRowOpts struct {
	tenant, status string
	input          string
	matchedRules   string
	errorDetails   string
	metadata       string
	startedAt      any
}

func piExpectQueueItem(mock sqlmock.Sqlmock, id int64, o piQueueRowOpts) {
	started := o.startedAt
	if started == nil && o.status == promptinjection.QueueStatusProcessing {
		started = time.Now()
	}
	// 可空 jsonb 列在 SQL 里已 COALESCE 成 'null'::jsonb ⇒ 模拟行也按这个形态给。
	details := any([]byte(`null`))
	if o.errorDetails != "" {
		details = []byte(o.errorDetails)
	}
	meta := any([]byte(`null`))
	if o.metadata != "" {
		meta = []byte(o.metadata)
	}
	matched := any([]byte(`null`))
	if o.matchedRules != "" {
		matched = []byte(o.matchedRules)
	}
	mock.ExpectQuery("FROM error_analysis_queue WHERE id = ").WithArgs(id).
		WillReturnRows(sqlmock.NewRows(piQueueCols()).AddRow(
			id, promptinjection.ErrorTypeRuleFalsePositive, "hash", 6, o.tenant, "sess-1", "req-1",
			o.input, nil, "Rule matched but likely false positive",
			details, []byte(`null`), matched, []byte(`null`),
			o.status, 0, 3,
			time.Now(), started, nil, meta,
		))
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not a JSON object: %v\n%s", err, rec.Body.String())
	}
	return out
}

func piAdminRequest(method, path string, role, tenant string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	if role != "" {
		r = SetAuthContext(r, &AuthContext{Role: role, TenantID: tenant, Username: "tester", IsJWT: true})
	}
	return r
}

// ── 1. 修正方案视图 ────────────────────────────────────────────────────────

func TestFixPlanView_BlockersFollowApplyGates(t *testing.T) {
	h := &PIAnalysisHandler{}
	adjustment := map[string]any{"action": "disable_rule", "rule_name": "dan_mode"}
	cases := []struct {
		name    string
		in      map[string]any
		fixable bool
		want    []string
	}{
		{"可自动修正", map[string]any{"confidence_score": 0.9, "error_type": promptinjection.ErrorTypeRuleFalsePositive,
			"root_cause": "x", "rule_adjustment": adjustment, "auto_applied": false}, true, []string{}},
		{"输出守卫类型恒只读", map[string]any{"confidence_score": 0.9, "error_type": promptinjection.ErrorTypeOutputGuardBlocked,
			"root_cause": "x", "rule_adjustment": adjustment, "auto_applied": false}, false, []string{"view_only_type"}},
		{"解析失败的结果不可执行", map[string]any{"confidence_score": 0.95, "error_type": promptinjection.ErrorTypeRuleFalsePositive,
			"root_cause": "llm_parse_failed", "rule_adjustment": adjustment, "auto_applied": false}, false, []string{"analysis_unparsable"}},
		{"没有可执行动作", map[string]any{"confidence_score": 0.9, "error_type": promptinjection.ErrorTypeRuleFalsePositive,
			"root_cause": "x", "rule_adjustment": nil, "auto_applied": false}, false, []string{"no_actions"}},
		{"置信度不足", map[string]any{"confidence_score": 0.79, "error_type": promptinjection.ErrorTypeRuleFalsePositive,
			"root_cause": "x", "rule_adjustment": adjustment, "auto_applied": false}, false, []string{"low_confidence"}},
		{"已应用", map[string]any{"confidence_score": 0.9, "error_type": promptinjection.ErrorTypeRuleFalsePositive,
			"root_cause": "x", "rule_adjustment": adjustment, "auto_applied": true}, false, []string{"already_applied"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			view := h.fixPlanView(c.in)
			if view["auto_fixable"] != c.fixable {
				t.Errorf("auto_fixable = %v, want %v", view["auto_fixable"], c.fixable)
			}
			got, _ := json.Marshal(view["blockers"])
			want, _ := json.Marshal(c.want)
			if string(got) != string(want) {
				t.Errorf("blockers = %s, want %s", got, want)
			}
		})
	}
}

// 按钮状态必须与 ApplyFix 同源：视图说可执行 ⇔ ApplyFix 过得了类型闸与动作闸。
func TestFixPlanView_AgreesWithApplyFix(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	// ApplyFix 会拒绝（没有可执行动作）的同一条结果，视图也必须说不可执行。
	rows := sqlmock.NewRows([]string{"rule_adjustment", "confidence_score", "is_false_positive", "auto_applied"}).
		AddRow(nil, 0.95, true, false)
	mock.ExpectQuery("SELECT rule_adjustment, confidence_score").WithArgs(int64(1)).WillReturnRows(rows)
	mock.ExpectQuery("SELECT error_type FROM error_analysis_queue").WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"error_type"}).AddRow(promptinjection.ErrorTypeRuleFalsePositive))
	rec := httptest.NewRecorder()
	h.ApplyFix(rec, piAdminRequest(http.MethodPost, "/x", "super_admin", "default"), 1)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("前提：ApplyFix 应拒绝（422），得 %d", rec.Code)
	}
	view := h.fixPlanView(map[string]any{"rule_adjustment": nil, "confidence_score": 0.95,
		"error_type": promptinjection.ErrorTypeRuleFalsePositive, "root_cause": "x", "auto_applied": false})
	if view["auto_fixable"] != false {
		t.Errorf("ApplyFix 会拒绝，视图却说可自动修正: %v", view)
	}
}

func TestGetResultIncludesFixPlanView(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	mock.ExpectQuery("SELECT").WithArgs(int64(1)).
		WillReturnRows(piResultRowsForShape())
	rec := httptest.NewRecorder()
	h.GetResult(rec, httptest.NewRequest(http.MethodGet, "/x", nil), 1)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"fix_plan_view"`) || !strings.Contains(body, `"auto_fixable":false`) {
		t.Errorf("结果详情缺修正方案视图: %.300s", body)
	}
	// 既有字段一个不少（视图是**追加**，不是替换）。
	for _, key := range []string{`"analysis_id"`, `"root_cause"`, `"queued_at"`, `"analyzer_version":null`} {
		if !strings.Contains(body, key) {
			t.Errorf("追加视图后既有字段 %s 丢了", key)
		}
	}
}

// ── 2. 租户校验 ────────────────────────────────────────────────────────────

func TestPIAuthorizeQueueTenant(t *testing.T) {
	otherTenantRow := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery("SELECT tenant_id FROM error_analysis_queue WHERE id").WithArgs(int64(7)).
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-other"))
	}

	// 非超级管理员、不同租户 ⇒ 403。
	h, mock := newPIFixTestHandler(t)
	otherTenantRow(mock)
	rec := httptest.NewRecorder()
	if h.piAuthorizeQueueTenant(rec, piAdminRequest(http.MethodGet, "/x", "tenant_admin", "default"), 7) || rec.Code != http.StatusForbidden {
		t.Errorf("跨租户应 403，得 %d", rec.Code)
	}

	// 同租户 ⇒ 放行。
	h, mock = newPIFixTestHandler(t)
	otherTenantRow(mock)
	rec = httptest.NewRecorder()
	if !h.piAuthorizeQueueTenant(rec, piAdminRequest(http.MethodGet, "/x", "tenant_admin", "tenant-other"), 7) {
		t.Errorf("同租户应放行，得 %d %s", rec.Code, rec.Body.String())
	}

	// 超级管理员 ⇒ 不查库直接放行。
	h, mock = newPIFixTestHandler(t)
	rec = httptest.NewRecorder()
	// 超级管理员不查库：这里没登记任何期望，一旦发生查询，处理器的返回就不是放行。
	if !h.piAuthorizeQueueTenant(rec, piAdminRequest(http.MethodGet, "/x", "super_admin", "default"), 7) || rec.Code != http.StatusOK {
		t.Errorf("超级管理员应放行: ok=%v code=%d %s", rec.Code == 200, rec.Code, rec.Body.String())
	}

	// 行不存在 ⇒ 404（不是 403，也不是 500）。
	h, mock = newPIFixTestHandler(t)
	mock.ExpectQuery("SELECT tenant_id FROM error_analysis_queue WHERE id").WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}))
	rec = httptest.NewRecorder()
	if h.piAuthorizeQueueTenant(rec, piAdminRequest(http.MethodGet, "/x", "tenant_admin", "default"), 7) || rec.Code != http.StatusNotFound {
		t.Errorf("行不存在应 404，得 %d", rec.Code)
	}
}

func TestQueueItemRoutes_Dispatch(t *testing.T) {
	h, _ := newPIFixTestHandler(t)
	h.prefix = "/api/admin/pi-analysis"
	cases := []struct {
		path string
		code int
	}{
		{"/api/admin/pi-analysis/queue/abc", http.StatusBadRequest},
		{"/api/admin/pi-analysis/queue/1/unknown", http.StatusNotFound},
		{"/api/admin/pi-analysis/queue/1/evidence", http.StatusServiceUnavailable}, // 取证面未注入
		// 方法检查在配置检查之前：GET 打到只收 POST 的子动作 ⇒ 405（不是 503）。
		{"/api/admin/pi-analysis/queue/1/analyze", http.StatusMethodNotAllowed},
		{"/api/admin/pi-analysis/queue/1/verify", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.handleQueueItem(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s ⇒ %d, want %d (%s)", c.path, rec.Code, c.code, rec.Body.String())
		}
	}
	// 分析器未配置 ⇒ 503（POST 打到 analyze）。
	rec := httptest.NewRecorder()
	h.handleQueueItem(rec, httptest.NewRequest(http.MethodPost, "/api/admin/pi-analysis/queue/1/analyze", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("POST analyze（分析器未配置）⇒ %d, want 503", rec.Code)
	}
}

// ── 3. 按需取证 ────────────────────────────────────────────────────────────

func piRequestCols() []string {
	return []string{
		"ts", "request_status", "success", "error_kind", "failure_stage", "failure_detail_code",
		"upstream_status_code", "client_model", "outbound_model", "canonical_model", "provider_id", "credential_id",
		"client_protocol", "client_endpoint", "agent_name", "latency_ms", "prompt_tokens", "completion_tokens",
		"total_tokens", "stream_interrupted", "routing_attempts", "is_auto_request", "request_preview",
	}
}

// piEvidenceSelectItems 从请求账本投影的 SQL 文本里数出选择项、并取出每项的输出名。
//
//	★ 为什么要从文本数：sqlmock 的模拟行是手写的 ⇒ 列数会**跟着 Scan 走**而不是跟着 SQL 走
//	  ⇒ 2026-11-26 在 211 上实测：SQL 选了 23 列、Scan 只有 22 个目标 ⇒ database/sql 报
//	  「expected 23 destination arguments in Scan, not 22」⇒ 请求账本整路降级，而单测全绿。
//	  括号内的逗号（LEFT(COALESCE(…), 300)、CASE 表达式）不算分隔符。
func piEvidenceSelectItems(t *testing.T) []string {
	t.Helper()
	text := strings.ReplaceAll(piEvidenceRequestCols, "`", "")
	items, depth, start := []string{}, 0, 0
	for i, r := range text {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, strings.TrimSpace(text[start:i]))
				start = i + 1
			}
		}
	}
	items = append(items, strings.TrimSpace(text[start:]))
	names := make([]string, 0, len(items))
	for _, it := range items {
		name := it
		if k := strings.LastIndex(strings.ToUpper(it), " AS "); k >= 0 {
			name = it[k+4:]
		} else if k := strings.LastIndex(it, "."); k >= 0 {
			name = it[k+1:]
		}
		names = append(names, strings.TrimSpace(name))
	}
	return names
}

// 请求账本投影的列数、列名、顺序必须与测试夹具（也就是 Scan 的目标顺序）逐项一致。
func TestEvidenceRequestSelectListMatchesScan(t *testing.T) {
	got := piEvidenceSelectItems(t)
	want := piRequestCols()
	if len(got) != len(want) {
		t.Fatalf("投影选了 %d 列，Scan 夹具只有 %d 列:\n got %v\nwant %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 列不一致：SQL 选出 %q，Scan 按 %q 接", i+1, got[i], want[i])
		}
	}
}

func TestQueueEvidence_ReturnsAllSourcesAndDegradesPerSource(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	h.collector = promptinjection.NewErrorCollector(h.db, true)
	g := NewPIEvidenceGatherer(h.db)
	g.searchLogs = func(q logging.RecentQuery) logging.RecentResult {
		if q.Limit == 1 {
			// 覆盖窗口：缓冲起点在错误发生之后（队列行的 created_at 是「现在」）⇒ 如实说「不覆盖」。
			oldest := time.Now().Add(2 * time.Hour)
			return logging.RecentResult{Lines: []logging.RecentLine{}, OldestAt: &oldest, NewestAt: &oldest}
		}
		return logging.RecentResult{Lines: []logging.RecentLine{}, Scanned: 12}
	}
	h.SetEvidenceGatherer(g)

	piExpectQueueItem(mock, 7, piQueueRowOpts{tenant: "default", status: "analyzed",
		errorDetails: `{"rule":"builtin.guard.tool_json_invalid","error_kind":"output_policy_blocked"}`})
	// ① 请求账本（生产真实形态的那一行：15.8 万 prompt tokens 的请求被守卫拦断）。
	mock.ExpectQuery("rl.request_id = .1 AND rl.tenant_id = .2").WithArgs("req-1", "default").
		WillReturnRows(sqlmock.NewRows(piRequestCols()).AddRow(
			time.Date(2026, 11, 25, 0, 56, 28, 0, time.Now().Location()), "failure", false, "output_policy_blocked",
			"upstream", "output_policy_blocked", nil,
			"glm-5.3-flash", "glm-5.3-flash", nil, 32, 62,
			"openai-responses", nil, "minimax-code", 7063, 158787, 0, 158787, false, nil, true, `{"model":"glm-5.3-flash"}`))
	// ② 上游尝试：这一路查询失败 ⇒ 只留一句说明。
	mock.ExpectQuery("FROM candidate_failure_logs_hot").WithArgs("req-1", "default", 10).
		WillReturnError(errors.New("pq: canceling statement due to statement timeout"))
	// ③ 会话上下文：空。
	mock.ExpectQuery("rl.gw_session_id = .1").WillReturnRows(
		sqlmock.NewRows([]string{"ts", "request_status", "client_model", "error_kind", "upstream_status_code", "latency_ms", "total_tokens", "is_target"}))
	// ④ 同型错误分布：表不在场 ⇒ 静默。
	mock.ExpectQuery("GROUP BY provider_id, raw_model_name, error_kind").WillReturnError(
		errors.New("pq: relation \"candidate_failure_logs_hot\" does not exist"))
	// ⑥ 手册：两种错误类型各查一次。
	mock.ExpectQuery("FROM error_playbook_entries").WithArgs(promptinjection.ErrorTypeRuleFalsePositive, 5).
		WillReturnRows(sqlmock.NewRows([]string{"id", "title", "root_cause", "plan_kind", "enabled", "auto_execute", "hit_count", "success_count", "failure_count", "last_status"}))
	mock.ExpectQuery("FROM error_playbook_entries").WithArgs("output_policy_blocked", 5).
		WillReturnRows(sqlmock.NewRows([]string{"id", "title", "root_cause", "plan_kind", "enabled", "auto_execute", "hit_count", "success_count", "failure_count", "last_status"}).
			// last_status 给 NULL：它是可空列（从未执行过的条目就是空的），按 string 接会整路报错。
			AddRow(3, "输出守卫格式类拦截", "上游断流", "prompt", true, false, 12, 0, 0, nil))

	rec := httptest.NewRecorder()
	h.handleQueueEvidence(rec, piAdminRequest(http.MethodGet, "/x", "super_admin", "default"), 7)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"client_model":"glm-5.3-flash"`, `"prompt_tokens":158787`, `"latency_ms":7063`,
		`"provider_id":32`, `"error_kind":"output_policy_blocked"`,
		`"session_context":[]`, `"upstream_attempts":[]`, `"error_window":[]`, `"service_logs":[]`, `"verifications":[]`,
		`"covers_window":false`, `"title":"输出守卫格式类拦截"`, `"degraded":[`,
		`"is_auto_request":true`, "上游断流",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("取证响应缺 %s\n%s", want, body)
		}
	}
	if !strings.Contains(body, "upstream attempts unavailable") {
		t.Errorf("上游尝试一路失败没有如实留说明: %s", body)
	}
	// 请求账本与手册这两路必须是**算出来的**，不能出现在降级清单里
	//（2026-11-26 的 211 实测：前者因 Scan 列数不匹配、后者因可空列按 string 接而整路降级）。
	for _, bad := range []string{"request ledger unavailable", "playbook: read failed"} {
		if strings.Contains(body, bad) {
			t.Errorf("这一路不该降级，却出现了 %q: %s", bad, body)
		}
	}
	// 空值字段不出现（不是 null 占位）。
	if strings.Contains(body, `"upstream_status_code":null`) || strings.Contains(body, `"credential_id":null`) {
		t.Errorf("空值不应以 null 出现: %s", body)
	}
	// 同型错误分布那一路表不在场 ⇒ 不该在说明里报「不可用」（可选表缺席是静默跳过）。
	if strings.Contains(body, "same-kind error distribution unavailable") || strings.Contains(body, "same-kind error distribution: query timed out") {
		t.Errorf("可选表不在场不该报不可用: %s", body)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestQueueEvidence_TenantScopeAndNotConfigured(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	h.collector = promptinjection.NewErrorCollector(h.db, true)
	h.SetEvidenceGatherer(NewPIEvidenceGatherer(h.db))
	// 先注册日志桩，避免 serviceLogs 读真实缓冲（测试里缓冲可能非空）。
	h.evidence.searchLogs = func(q logging.RecentQuery) logging.RecentResult {
		return logging.RecentResult{Lines: []logging.RecentLine{}}
	}

	// 跨租户 ⇒ 403，且不读队列行（越权的行连错误内容都不该带出）。
	mock.ExpectQuery("SELECT tenant_id FROM error_analysis_queue WHERE id").WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-other"))
	rec := httptest.NewRecorder()
	h.handleQueueEvidence(rec, piAdminRequest(http.MethodGet, "/x", "tenant_admin", "default"), 7)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨租户 ⇒ %d, want 403", rec.Code)
	}

	// 行不存在 ⇒ 404。
	mock.ExpectQuery("SELECT tenant_id FROM error_analysis_queue WHERE id").WithArgs(int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}))
	rec = httptest.NewRecorder()
	h.handleQueueEvidence(rec, piAdminRequest(http.MethodGet, "/x", "tenant_admin", "default"), 8)
	if rec.Code != http.StatusNotFound {
		t.Errorf("行不存在 ⇒ %d, want 404", rec.Code)
	}
	// 方法不对 ⇒ 405。
	rec = httptest.NewRecorder()
	h.handleQueueEvidence(rec, piAdminRequest(http.MethodPost, "/x", "super_admin", "default"), 7)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST evidence ⇒ %d, want 405", rec.Code)
	}
}

// 取证采集器：一路失败只降级；没有请求号时两路无从查起且如实说明。
func TestEvidenceGatherer_DegradesWithoutFailing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g := NewPIEvidenceGatherer(db)
	g.searchLogs = func(q logging.RecentQuery) logging.RecentResult {
		return logging.RecentResult{Lines: []logging.RecentLine{}}
	}

	mock.ExpectQuery("rl.request_id = .1 AND rl.tenant_id = .2").WithArgs("req-9", "default").
		WillReturnError(errors.New("pq: canceling statement due to user request"))
	mock.ExpectQuery("FROM candidate_failure_logs_hot").WithArgs("req-9", "default", 10).
		WillReturnRows(sqlmock.NewRows([]string{"ts", "provider_id", "credential_id", "model", "attempt_index", "error_kind", "error_message", "upstream_status_code", "retryable", "latency_ms"}).
			AddRow(time.Now(), "32", "62", "glm-5.3-flash", 1, "timeout", "upstream request timed out", 504, true, 30000))
	mock.ExpectQuery("GROUP BY provider_id, raw_model_name, error_kind").
		WillReturnRows(sqlmock.NewRows([]string{"provider_id", "model", "error_kind", "count", "last_ts"}).
			AddRow("32", "glm-5.3-flash", "timeout", 479, time.Now()))
	mock.ExpectQuery("FROM error_playbook_entries").WillReturnError(errors.New(`pq: relation "error_playbook_entries" does not exist`))

	b, err := g.Gather(context.Background(), promptinjection.EvidenceTarget{
		QueueID: 9, ErrorType: promptinjection.ErrorTypeRequestFailed, TenantID: "default", RequestID: "req-9",
		ErrorAt: time.Now(), ErrorDetails: map[string]any{"error_kind": "timeout"},
	})
	if err != nil || b == nil {
		t.Fatalf("一路失败不应让 Gather 返回错误: %v", err)
	}
	if len(b.Attempts) != 1 || b.Attempts[0]["error_kind"] != "timeout" || b.Attempts[0]["upstream_status_code"] != 504 {
		t.Errorf("上游尝试读错: %v", b.Attempts)
	}
	if len(b.ErrorWindow) != 1 || b.ErrorWindow[0]["count"] != int64(479) {
		t.Errorf("同型错误分布读错: %v", b.ErrorWindow)
	}
	// 两路降级各留一条说明：请求账本（查询被取消）+ 手册（表不在场——不再静默）。
	if len(b.Notes) != 2 || !strings.Contains(b.Notes[0], "request ledger unavailable") ||
		!strings.Contains(b.Notes[1], "playbook: table is not provisioned") {
		t.Errorf("请求账本一路失败应留一条说明、手册缺席应留一条: %v", b.Notes)
	}
	// 机器可读的降级标记与说明同源：调用方据此区分「空」与「算不出来」。
	if len(b.Degraded) != 2 || b.Degraded[0].Part != "request ledger" || b.Degraded[1].Part != "playbook" {
		t.Errorf("降级标记没跟上: %+v", b.Degraded)
	}
	// 摘要里能看到「479 次同型」——分析「偶发还是一直在发生」靠的就是这个数。
	digest := promptinjection.EvidenceDigest(b)
	if !strings.Contains(digest, "count=479") || !strings.Contains(digest, "upstream request timed out") {
		t.Errorf("摘要缺关键事实: %s", digest)
	}

	// 没有请求号：两路无从查起 ⇒ 说明里点名，且不发那两条查询。
	mock.ExpectQuery("FROM error_playbook_entries").WillReturnError(errors.New(`pq: relation "error_playbook_entries" does not exist`))
	b, err = g.Gather(context.Background(), promptinjection.EvidenceTarget{
		QueueID: 10, ErrorType: promptinjection.ErrorTypePolicyLoadFailed, ErrorAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if b.Request != nil || len(b.Attempts) != 0 {
		t.Errorf("没有请求号时不应有请求账本/上游尝试: %+v", b)
	}
	if len(b.Notes) == 0 || !strings.Contains(b.Notes[0], "queue item has no request id") {
		t.Errorf("没有请求号应如实说明: %v", b.Notes)
	}
	// 空租户落 default（采集器的缺省口径与采集器一致）。
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// 服务日志：缓冲起点晚于要查的窗口 ⇒ covers_window=false，且不把「没搜到」说成「没发生」。
func TestEvidenceGatherer_ServiceLogCoverageIsHonest(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g := NewPIEvidenceGatherer(db)
	oldest := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	g.searchLogs = func(q logging.RecentQuery) logging.RecentResult {
		if q.Limit == 1 {
			return logging.RecentResult{Lines: []logging.RecentLine{}, OldestAt: &oldest, NewestAt: &oldest}
		}
		return logging.RecentResult{Lines: []logging.RecentLine{}, Scanned: 5}
	}
	errorAt := time.Date(2026, 11, 25, 1, 0, 0, 0, time.UTC)
	_, meta := g.serviceLogs(promptinjection.EvidenceTarget{ErrorAt: errorAt, RequestID: "req-1"})
	if meta["covers_window"] != false || meta["oldest_at"] != oldest.Format(time.RFC3339) {
		t.Errorf("覆盖窗口读数不对: %v", meta)
	}
	if note, _ := meta["note"].(string); !strings.Contains(note, "no longer in the buffer") {
		t.Errorf("缓冲不覆盖时应明说: %v", meta)
	}

	// 缓冲覆盖窗口但一行都没有 ⇒ covers_window=true + 「窗内没有」的说明。
	early := errorAt.Add(-time.Hour)
	g.searchLogs = func(q logging.RecentQuery) logging.RecentResult {
		if q.Limit == 1 {
			return logging.RecentResult{Lines: []logging.RecentLine{}, OldestAt: &early, NewestAt: &oldest}
		}
		return logging.RecentResult{Lines: []logging.RecentLine{}, Scanned: 5}
	}
	_, meta = g.serviceLogs(promptinjection.EvidenceTarget{ErrorAt: errorAt, RequestID: "req-1"})
	if meta["covers_window"] != true {
		t.Errorf("缓冲覆盖窗口应为 true: %v", meta)
	}
	// 缓冲为空 ⇒ available=false。
	g.searchLogs = func(q logging.RecentQuery) logging.RecentResult {
		return logging.RecentResult{Lines: []logging.RecentLine{}}
	}
	_, meta = g.serviceLogs(promptinjection.EvidenceTarget{ErrorAt: errorAt})
	if meta["available"] != false {
		t.Errorf("空缓冲应报 available=false: %v", meta)
	}
}

// ── 4. 复测聚合 ────────────────────────────────────────────────────────────

type piVerifyFixture struct {
	replay  *promptinjection.RulesReplayResult
	recheck *promptinjection.LayerRecheckResult
	// queueRecurrence 是同型错误在参照时点之后的新增队列行数。
	queueRecurrence int64
	reference       string
}

func runVerify(t *testing.T, o piQueueRowOpts, f *piVerifyFixture, includeLLM bool) (int, map[string]any) {
	t.Helper()
	h, mock := newPIFixTestHandler(t)
	h.collector = promptinjection.NewErrorCollector(h.db, true)
	h.replayRulesFn = func(ctx context.Context, db *sql.DB, tenantID, input string) (*promptinjection.RulesReplayResult, error) {
		if f == nil || f.replay == nil {
			return &promptinjection.RulesReplayResult{Available: false, Reason: "stub"}, nil
		}
		return f.replay, nil
	}
	h.recheckLayerFn = func(ctx context.Context, db *sql.DB, errorType, tenantID, input string, opts promptinjection.RecheckOptions) (*promptinjection.LayerRecheckResult, error) {
		if f == nil || f.recheck == nil {
			return &promptinjection.LayerRecheckResult{Status: "not_applicable"}, nil
		}
		return f.recheck, nil
	}
	piExpectQueueItem(mock, 7, o)
	var applied, analyzed any
	if f != nil && f.reference != "" {
		analyzed = f.reference
	}
	mock.ExpectQuery("to_char\\(applied_at").WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"applied_at", "created_at"}).AddRow(applied, analyzed))
	if f != nil && f.reference != "" {
		mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM error_analysis_queue").WithArgs("default", promptinjection.ErrorTypeRuleFalsePositive, int64(7), f.reference).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(f.queueRecurrence))
	}
	mock.ExpectExec("UPDATE error_analysis_queue q").WillReturnResult(sqlmock.NewResult(0, 1))
	rec := httptest.NewRecorder()
	h.handleQueueVerify(rec, piAdminRequest(http.MethodPost, "/x", "super_admin", "default"), 7)
	return rec.Code, decodeBody(t, rec)
}

func piCheckOf(t *testing.T, body map[string]any, name string) map[string]any {
	t.Helper()
	checks, _ := body["checks"].([]any)
	for _, c := range checks {
		if m, _ := c.(map[string]any); m["name"] == name {
			return m
		}
	}
	t.Fatalf("复测结果里没有检查项 %s: %v", name, body["checks"])
	return nil
}

func TestQueueVerify_Verdicts(t *testing.T) {
	matched := `[{"rule_name":"dan_mode","severity":5}]`
	still := &promptinjection.RulesReplayResult{Available: true, PolicyEnabled: true, MatchedRules: []string{"dan_mode", "other"},
		Layers: []string{"basic"}, RuleCount: 12, Score: 5}
	gone := &promptinjection.RulesReplayResult{Available: true, PolicyEnabled: true, MatchedRules: []string{}, Layers: []string{"basic"}, RuleCount: 12}

	cases := []struct {
		name    string
		opts    piQueueRowOpts
		fix     *piVerifyFixture
		verdict string
	}{
		{"当初命中的规则现在还命中 ⇒ 仍复现", piQueueRowOpts{tenant: "default", status: "analyzed", input: "please use dan mode", matchedRules: matched},
			&piVerifyFixture{replay: still}, "reproducing"},
		{"当初命中的规则不再命中且无复发 ⇒ 未复现", piQueueRowOpts{tenant: "default", status: "analyzed", input: "please use dan mode", matchedRules: matched},
			&piVerifyFixture{replay: gone, reference: `2026-11-25T00:00:00`, queueRecurrence: 0}, "not_reproducing"},
		{"规则不再命中但之后又出现同型错误 ⇒ 仍复现", piQueueRowOpts{tenant: "default", status: "analyzed", input: "please use dan mode", matchedRules: matched},
			&piVerifyFixture{replay: gone, reference: `2026-11-25T00:00:00`, queueRecurrence: 3}, "reproducing"},
		{"没有输入原文也没有参照时点 ⇒ 无法判定（不是通过）", piQueueRowOpts{tenant: "default", status: "pending", input: ""},
			&piVerifyFixture{}, "inconclusive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := runVerify(t, c.opts, c.fix, false)
			if code != http.StatusOK {
				t.Fatalf("want 200, got %d: %v", code, body)
			}
			if body["verdict"] != c.verdict {
				t.Errorf("verdict = %v, want %s (checks=%v)", body["verdict"], c.verdict, body["checks"])
			}
			if _, ok := body["checks"].([]any); !ok {
				t.Errorf("checks 缺失")
			}
			if body["by"] != "manual:tester" {
				t.Errorf("复测留痕必须答得出是谁按的: by=%v", body["by"])
			}
		})
	}
}

func TestQueueVerify_LayerRecheckIsDecisiveForLayerErrors(t *testing.T) {
	ok := &promptinjection.LayerRecheckResult{Layer: "canary", Status: "ok", Detail: "canary token query ok (active tokens: 3)"}
	failing := &promptinjection.LayerRecheckResult{Layer: "canary", Status: "failed", Detail: "relation is gone"}

	// 出错的那一层现在通了、且之后没再出现 ⇒ 未复现。
	code, body := runVerify(t, piQueueRowOpts{tenant: "default", status: "analyzed", input: "x"},
		&piVerifyFixture{recheck: ok, reference: `2026-11-25T00:00:00`}, false)
	if code != 200 || body["verdict"] != "not_reproducing" {
		t.Errorf("层通 + 无复发 ⇒ %v %v", code, body["verdict"])
	}
	if c := piCheckOf(t, body, "layer_recheck"); c["status"] != "pass" || c["decisive"] != true {
		t.Errorf("层复查通过应是决定性检查: %v", c)
	}

	// 那一层仍然失败 ⇒ 仍复现（即使复发统计是 0）。
	_, body = runVerify(t, piQueueRowOpts{tenant: "default", status: "analyzed", input: "x"},
		&piVerifyFixture{recheck: failing, reference: `2026-11-25T00:00:00`}, false)
	if body["verdict"] != "reproducing" {
		t.Errorf("层仍失败 ⇒ %v, want reproducing", body["verdict"])
	}
}

// 写动作只收超级管理员：tenant_admin（本仓约定的只读角色）在**派发点**就被挡下，
// 且不读队列行——越权的行连错误内容都不该带出（这里没登记任何查询期望）。
func TestWriteSubActions_RequireSuperAdmin(t *testing.T) {
	h, _ := newPIFixTestHandler(t)
	h.prefix = "/api/admin/pi-analysis"
	const tenantAdmin = "tenant_admin"
	for _, p := range []string{
		"/api/admin/pi-analysis/queue/1/analyze",
		"/api/admin/pi-analysis/queue/1/verify",
		"/api/admin/pi-analysis/results/1/apply",
		"/api/admin/pi-analysis/results/1/revert",
		"/api/admin/pi-analysis/results/1/reparse",
	} {
		rec := httptest.NewRecorder()
		if strings.HasPrefix(p, h.prefix+"/queue") {
			h.handleQueueItem(rec, piAdminRequest(http.MethodPost, p, tenantAdmin, "default"))
		} else {
			h.handleResults(rec, piAdminRequest(http.MethodPost, p, tenantAdmin, "default"))
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("tenant_admin POST %s ⇒ %d, want 403 (%s)", p, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	h.handleReparseFailed(rec, piAdminRequest(http.MethodPost, h.prefix+"/results/reparse-failed", tenantAdmin, "default"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("tenant_admin 批量重解析 ⇒ %d, want 403", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.TriggerAnalysis(rec, piAdminRequest(http.MethodPost, h.prefix+"/trigger", tenantAdmin, "default"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("tenant_admin 批量分析 ⇒ %d, want 403", rec.Code)
	}

	// 超级管理员不被这道闸拦（后续各端点自己的校验照常：这里的库没有登记期望，
	// 读结果行会失败 ⇒ 5xx；判据只是「不是 403」）。
	for _, p := range []string{
		"/api/admin/pi-analysis/queue/1/analyze",
		"/api/admin/pi-analysis/results/1/apply",
	} {
		rec := httptest.NewRecorder()
		if strings.HasPrefix(p, h.prefix+"/queue") {
			h.handleQueueItem(rec, piAdminRequest(http.MethodPost, p, "super_admin", "default"))
		} else {
			h.handleResults(rec, piAdminRequest(http.MethodPost, p, "super_admin", "default"))
		}
		if rec.Code == http.StatusForbidden {
			t.Errorf("super_admin POST %s 不该被权限闸拦: %s", p, rec.Body.String())
		}
	}
	// 只读方法不过这道闸：GET 打到写子动作仍是各端点自己的 405。
	rec = httptest.NewRecorder()
	h.handleResults(rec, piAdminRequest(http.MethodGet, h.prefix+"/results/1/apply", tenantAdmin, "default"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET apply ⇒ %d, want 405", rec.Code)
	}
}

// piScopeServiceLogsToRequest：进程内日志缓冲是全实例共用的 ⇒ 非超级管理员只能
// 看到提到本请求号 / 会话号的行；收窄掉的行数如实留痕。
func TestScopeServiceLogsToRequest(t *testing.T) {
	mkItem := func(requestID, sessionID string) *promptinjection.QueuedError {
		return &promptinjection.QueuedError{
			RequestID: sql.NullString{String: requestID, Valid: requestID != ""},
			SessionID: sql.NullString{String: sessionID, Valid: sessionID != ""},
		}
	}
	lines := func(b *promptinjection.EvidenceBundle) []string {
		out := []string{}
		for _, l := range b.ServiceLogs {
			out = append(out, l.Line)
		}
		return out
	}
	fresh := func() *promptinjection.EvidenceBundle {
		return &promptinjection.EvidenceBundle{ServiceLogs: []promptinjection.ServiceLogLine{
			{Line: `{"msg":"dispatch","request_id":"req-1"}`},
			{Line: `{"msg":"upstream 429","tenant":"other"}`},
			{Line: `{"msg":"stream closed","session_id":"sess-9"}`},
			{Line: `{"msg":"circuit open"}`},
		}}
	}

	b := fresh()
	piScopeServiceLogsToRequest(b, mkItem("req-1", "sess-9"))
	if got, want := lines(b), []string{`{"msg":"dispatch","request_id":"req-1"}`, `{"msg":"stream closed","session_id":"sess-9"}`}; !slices.Equal(got, want) {
		t.Errorf("保留的行不对:\n got %v\nwant %v", got, want)
	}
	if b.ServiceLogMeta["scoped_to_request"] != true || b.ServiceLogMeta["hidden_unrelated"] != 2 {
		t.Errorf("收窄元数据不对: %v", b.ServiceLogMeta)
	}
	if len(b.Notes) != 1 || !strings.Contains(b.Notes[0], "2 line(s)") || !strings.Contains(b.Notes[0], "super admins only") {
		t.Errorf("收窄说明要如实写明收窄了几行: %v", b.Notes)
	}

	// 队列行没有请求号 / 会话号 ⇒ 没有一行能确定属于本租户 ⇒ 全部收窄。
	b = fresh()
	piScopeServiceLogsToRequest(b, mkItem("", ""))
	if len(b.ServiceLogs) != 0 || b.ServiceLogMeta["hidden_unrelated"] != 4 {
		t.Errorf("无锚点时应全部收窄: %d 行, meta=%v", len(b.ServiceLogs), b.ServiceLogMeta)
	}

	// 本来就没有无关行 ⇒ 不留「收窄过」的说明（没发生的事不写）。
	b = &promptinjection.EvidenceBundle{ServiceLogs: []promptinjection.ServiceLogLine{{Line: "req-1 done"}}}
	piScopeServiceLogsToRequest(b, mkItem("req-1", ""))
	if len(b.Notes) != 0 || b.ServiceLogMeta["hidden_unrelated"] != nil {
		t.Errorf("没有收窄掉任何行时不应留说明: %v %v", b.Notes, b.ServiceLogMeta)
	}
}

// 取证端点的接线：同租户的租户管理员拿到的服务日志已收窄；超级管理员看全量。
func TestQueueEvidence_ServiceLogsScopedForTenantAdmin(t *testing.T) {
	expectLedger := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery("rl.request_id = .1 AND rl.tenant_id = .2").WillReturnError(errors.New("ledger unavailable"))
		mock.ExpectQuery("FROM candidate_failure_logs_hot").WillReturnError(errors.New("attempts unavailable"))
		mock.ExpectQuery("rl.gw_session_id = .1").WillReturnError(errors.New("session unavailable"))
		mock.ExpectQuery("GROUP BY provider_id, raw_model_name, error_kind").WillReturnError(errors.New("window unavailable"))
		mock.ExpectQuery("FROM error_playbook_entries").WillReturnError(errors.New("playbook unavailable"))
	}
	stubLogs := func(g *PIEvidenceGatherer) {
		g.searchLogs = func(q logging.RecentQuery) logging.RecentResult {
			now := time.Now()
			switch {
			case q.Limit == 1:
				return logging.RecentResult{OldestAt: &now, NewestAt: &now}
			case len(q.MustContain) > 0:
				return logging.RecentResult{Lines: []logging.RecentLine{{Time: now, Level: "INFO", Line: `{"msg":"dispatch","request_id":"req-1"}`}}}
			default:
				return logging.RecentResult{Lines: []logging.RecentLine{
					{Time: now, Level: "ERROR", Line: `{"msg":"upstream 429","tenant":"other"}`},
					{Time: now, Level: "WARN", Line: `{"msg":"stream closed","gw_session_id":"sess-1"}`},
				}}
			}
		}
	}

	run := func(role string) (string, []string, map[string]any, []any) {
		t.Helper()
		h, mock := newPIFixTestHandler(t)
		h.collector = promptinjection.NewErrorCollector(h.db, true)
		g := NewPIEvidenceGatherer(h.db)
		stubLogs(g)
		h.SetEvidenceGatherer(g)
		if role != "super_admin" {
			mock.ExpectQuery("SELECT tenant_id FROM error_analysis_queue WHERE id").WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("default"))
		}
		piExpectQueueItem(mock, 7, piQueueRowOpts{tenant: "default", status: "analyzed"})
		expectLedger(mock)
		rec := httptest.NewRecorder()
		h.handleQueueEvidence(rec, piAdminRequest(http.MethodGet, "/x", role, "default"), 7)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s ⇒ %d: %s", role, rec.Code, rec.Body.String())
		}
		body := decodeBody(t, rec)
		logs, _ := body["service_logs"].([]any)
		lines := []string{}
		for _, l := range logs {
			lines = append(lines, l.(map[string]any)["line"].(string))
		}
		meta, _ := body["service_log_meta"].(map[string]any)
		notes, _ := body["notes"].([]any)
		return rec.Body.String(), lines, meta, notes
	}

	// 保留两行：提到请求号的（请求号那一遍检索）＋ 提到会话号的（时间窗那一遍里
	// 恰好属于本会话的）；别租户的那一行被收窄。
	_, lines, meta, notes := run("tenant_admin")
	if len(lines) != 2 {
		t.Errorf("租户管理员只应看到提到本请求 / 本会话的行，得 %v", lines)
	}
	for _, l := range lines {
		if strings.Contains(l, "tenant\":\"other") {
			t.Errorf("别租户的行不该出现: %s", l)
		}
	}
	if meta["scoped_to_request"] != true || meta["hidden_unrelated"] != float64(1) {
		t.Errorf("收窄元数据不对: %v", meta)
	}
	if len(notes) == 0 || !strings.Contains(fmt.Sprint(notes), "1 line(s)") {
		t.Errorf("收窄说明应随材料返回: %v", notes)
	}

	body, lines, meta, _ := run("super_admin")
	if len(lines) != 3 {
		t.Errorf("超级管理员应看到全部 3 行，得 %v", lines)
	}
	if _, ok := meta["scoped_to_request"]; ok || strings.Contains(body, "super admins only") {
		t.Errorf("超级管理员不该出现收窄标记: %s", body)
	}
}

// ── 5. 重解析 ──────────────────────────────────────────────────────────────

const piStoredCols = "id, llm_response_raw, root_cause, error_category, analyzer_model, analyzer_version, analysis_details"

func TestResultReparse_ErrorMapping(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	r := piAdminRequest(http.MethodPost, "/x", "super_admin", "default")

	mock.ExpectQuery("FROM error_analysis_results").WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows(strings.Split(piStoredCols, ", ")))
	rec := httptest.NewRecorder()
	h.handleResultReparse(rec, r, 5)
	if rec.Code != http.StatusNotFound {
		t.Errorf("没有结果行 ⇒ %d, want 404", rec.Code)
	}

	mock.ExpectQuery("FROM error_analysis_results").WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows(strings.Split(piStoredCols, ", ")).
			AddRow(9, "{invalid json", "llm_parse_failed", "parse_failure", "m", "1.0.0", []byte(`{}`)))
	rec = httptest.NewRecorder()
	h.handleResultReparse(rec, r, 5)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("仍读不出 ⇒ %d, want 422", rec.Code)
	}

	mock.ExpectQuery("FROM error_analysis_results").WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows(strings.Split(piStoredCols, ", ")).
			AddRow(9, `{"analysis_report":{"summary":"s","root_cause_analysis":{"primary_cause":"p"},"remediation":{"immediate":["a"]}}}`,
				"llm_parse_failed", "parse_failure", "m", "1.0.0", []byte(`{"parse_error":"root_cause is required"}`)))
	mock.ExpectExec("UPDATE error_analysis_results").WillReturnResult(sqlmock.NewResult(0, 1))
	rec = httptest.NewRecorder()
	h.handleResultReparse(rec, r, 5)
	if rec.Code != http.StatusOK {
		t.Fatalf("重解析 ⇒ %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["root_cause"] != "p" || body["mode"] != "nested" {
		t.Errorf("重解析读错: %v", body)
	}

	rec = httptest.NewRecorder()
	h.handleResultReparse(rec, piAdminRequest(http.MethodGet, "/x", "super_admin", "default"), 5)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET reparse ⇒ %d, want 405", rec.Code)
	}
}

func TestReparseFailed_OnlyTouchesParseFailedRows(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	// 批量只取 root_cause = 'llm_parse_failed' 的行（其它行一律不动）。
	mock.ExpectQuery("root_cause = 'llm_parse_failed'").WithArgs(piReparseFailedBatch).
		WillReturnRows(sqlmock.NewRows([]string{"queue_id"}).AddRow(int64(5)).AddRow(int64(6)))

	// 5：读得出 ⇒ 更新。
	mock.ExpectQuery("FROM error_analysis_results").WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows(strings.Split(piStoredCols, ", ")).
			AddRow(50, `{"analysis":{"root_cause":"real cause"},"suggested_fix":"do x"}`, "llm_parse_failed", "parse_failure", "m", "1.0.0", []byte(`{}`)))
	mock.ExpectExec("UPDATE error_analysis_results").WillReturnResult(sqlmock.NewResult(0, 1))
	// 6：仍读不出 ⇒ **不动库**（没有 UPDATE 的期望）。
	mock.ExpectQuery("FROM error_analysis_results").WithArgs(int64(6)).
		WillReturnRows(sqlmock.NewRows(strings.Split(piStoredCols, ", ")).
			AddRow(60, "just some prose", "llm_parse_failed", "parse_failure", "m", "1.0.0", []byte(`{}`)))

	rec := httptest.NewRecorder()
	h.handleReparseFailed(rec, piAdminRequest(http.MethodPost, "/x", "super_admin", "default"))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["scanned"] != float64(2) || body["reparsed"] != float64(1) || body["still_unparsable"] != float64(1) || body["failed"] != float64(0) {
		t.Errorf("批量读数不对: %v", body)
	}
	if ids, _ := body["reparsed_queue_ids"].([]any); len(ids) != 1 || ids[0] != float64(5) {
		t.Errorf("应点名哪些行被重解析: %v", body["reparsed_queue_ids"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// ── 定向分析端点 ────────────────────────────────────────────────────────────

func TestQueueAnalyze_Guards(t *testing.T) {
	// 分析器未配置 ⇒ 503。
	h, mock := newPIFixTestHandler(t)
	rec := httptest.NewRecorder()
	h.handleQueueAnalyze(rec, piAdminRequest(http.MethodPost, "/x", "super_admin", "default"), 7)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("未配置分析器 ⇒ %d, want 503", rec.Code)
	}

	// 行不存在 ⇒ 404。
	h, mock = newPIFixTestHandler(t)
	h.collector = promptinjection.NewErrorCollector(h.db, true)
	h.analyzer = promptinjection.NewErrorAnalyzer(h.db, h.collector, &piStubLLM{}, "m", true)
	mock.ExpectQuery("FROM error_analysis_queue WHERE id = ").WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows(piQueueCols()))
	rec = httptest.NewRecorder()
	h.handleQueueAnalyze(rec, piAdminRequest(http.MethodPost, "/x", "super_admin", "default"), 7)
	if rec.Code != http.StatusNotFound {
		t.Errorf("行不存在 ⇒ %d, want 404", rec.Code)
	}

	// 正在被别的流程分析 ⇒ 409（不重复花一次模型）。
	h, mock = newPIFixTestHandler(t)
	h.collector = promptinjection.NewErrorCollector(h.db, true)
	h.analyzer = promptinjection.NewErrorAnalyzer(h.db, h.collector, &piStubLLM{}, "m", true)
	piExpectQueueItem(mock, 7, piQueueRowOpts{tenant: "default", status: promptinjection.QueueStatusProcessing})
	rec = httptest.NewRecorder()
	h.handleQueueAnalyze(rec, piAdminRequest(http.MethodPost, "/x", "super_admin", "default"), 7)
	if rec.Code != http.StatusConflict {
		t.Errorf("分析中 ⇒ %d, want 409", rec.Code)
	}

	// 正常 ⇒ 202，后台流程里那一步发现「刚被领取」而让位（不调模型）。
	h, mock = newPIFixTestHandler(t)
	h.collector = promptinjection.NewErrorCollector(h.db, true)
	stub := &piStubLLM{}
	h.analyzer = promptinjection.NewErrorAnalyzer(h.db, h.collector, stub, "m", true)
	piExpectQueueItem(mock, 7, piQueueRowOpts{tenant: "default", status: "pending"})
	piExpectQueueItem(mock, 7, piQueueRowOpts{tenant: "default", status: "pending"})
	mock.ExpectExec("UPDATE error_analysis_queue").WithArgs(int64(7), true, "3600 seconds").
		WillReturnResult(sqlmock.NewResult(0, 0)) // 被并发领取 ⇒ 让位
	rec = httptest.NewRecorder()
	h.handleQueueAnalyze(rec, piAdminRequest(http.MethodPost, "/x", "super_admin", "default"), 7)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if body := decodeBody(t, rec); body["status"] != "queued" {
		t.Errorf("响应应为 queued: %v", body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mock.ExpectationsWereMet() == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("后台定向分析没有跑起来: %v", err)
	}
	if stub.calls.Load() != 0 {
		t.Errorf("并发领取被挡下时不应调用模型")
	}

	// GET ⇒ 405。
	rec = httptest.NewRecorder()
	h.handleQueueAnalyze(rec, piAdminRequest(http.MethodGet, "/x", "super_admin", "default"), 7)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET analyze ⇒ %d, want 405", rec.Code)
	}
}

type piStubLLM struct {
	calls atomic.Int64
}

func (s *piStubLLM) Analyze(ctx context.Context, prompt string) (string, int, error) {
	s.calls.Add(1)
	return `{"root_cause":"stub","suggested_fix":"stub"}`, 1, nil
}

// piResultRowsForShape 是一条「规范形态 + 两个可空列取 NULL」的结果详情行
// （与 pi_analysis_result_shape_test.go 同形）。
func piResultRowsForShape() *sqlmock.Rows {
	cols := []string{
		"id", "analysis_id", "queue_id",
		"root_cause", "error_category", "is_false_positive",
		"confidence_score", "suggested_fix",
		"rule_adjustment", "code_changes",
		"severity_level", "impact_scope", "affected_users_estimate",
		"similar_cases", "pattern_frequency",
		"analysis_duration_ms", "llm_tokens_used",
		"created_at",
		"analyzer_model", "analyzer_version",
		"analysis_details", "llm_response_raw",
		"auto_applied", "applied_at", "applied_by", "applied_actions",
		"error_type", "error_hash", "priority",
		"tenant_id", "session_id", "request_id",
		"input_text", "input_hash", "error_message", "error_details",
		"detection_layers", "matched_rules", "policy_config",
		"status", "retry_count",
		"queued_at", "started_at", "completed_at",
	}
	return sqlmock.NewRows(cols).AddRow(
		int64(1), "analysis-1", int64(1),
		"root", "category", false,
		0.9, "fix",
		nil, nil,
		"high", "global", 3,
		nil, 1,
		10, 20,
		"2026-11-19T00:00:00Z",
		"model-x", nil,
		nil, nil,
		false, nil, nil, nil,
		promptinjection.ErrorTypeRuleFalsePositive, "hash", 5,
		"default", nil, nil,
		"input", nil, "msg", nil,
		nil, nil, nil,
		"analyzed", 0,
		"2026-11-19T00:00:00Z", nil, nil,
	)
}
