package admin

// PI 分析「一键应用修复 / 回滚」的钉测（2026-11-18 PI 分析管理面优化轮）。
//
// 覆盖三层：
//  1. NormalizeRuleAdjustment —— LLM 自由形状 JSON → 白名单动作的归一化
//     （这是安全面的第一道门：白名单外的任何形状都必须被忽略并计数）；
//  2. ApplyFix 的门控与事务（置信度门 / 已应用门 / 幻觉规则名 / 成功路径
//     + 规则热刷新被触发）；
//  3. RevertFix 的门控与旧值恢复。

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// ── NormalizeRuleAdjustment ────────────────────────────────────────────────

func TestNormalizeRuleAdjustment_CanonicalShape(t *testing.T) {
	raw := json.RawMessage(`{
		"action": "disable_rule",
		"rule_name": "dan_mode",
		"reason": "hits benign 'dan' substring"
	}`)
	actions, note := NormalizeRuleAdjustment(raw)
	if len(actions) != 1 {
		t.Fatalf("want 1 action, got %d (note=%q)", len(actions), note)
	}
	if actions[0].Action != "disable_rule" || actions[0].RuleName != "dan_mode" {
		t.Fatalf("bad action: %+v", actions[0])
	}
	if actions[0].Reason == "" {
		t.Fatal("reason not carried through")
	}
	if note != "" {
		t.Fatalf("unexpected note: %q", note)
	}
}

func TestNormalizeRuleAdjustment_DisableListShape(t *testing.T) {
	raw := json.RawMessage(`{"disable": ["rule_a", "rule_b"], "reason": "over-broad"}`)
	actions, _ := NormalizeRuleAdjustment(raw)
	if len(actions) != 2 {
		t.Fatalf("want 2 actions, got %d", len(actions))
	}
	if actions[0].Action != "disable_rule" || actions[1].RuleName != "rule_b" {
		t.Fatalf("bad actions: %+v", actions)
	}
}

func TestNormalizeRuleAdjustment_RulesArrayShape(t *testing.T) {
	raw := json.RawMessage(`{"rules": [
		{"name": "privilege_escalation", "action": "severity", "new_severity": 5},
		{"rule": "command_injection", "type": "modify_pattern", "new_pattern": "(?i)\\bexec\\s*\\("}
	]}`)
	actions, _ := NormalizeRuleAdjustment(raw)
	if len(actions) != 2 {
		t.Fatalf("want 2 actions, got %d", len(actions))
	}
	if actions[0].Action != "adjust_severity" || actions[0].Severity != 5 {
		t.Fatalf("bad severity action: %+v", actions[0])
	}
	if actions[1].Action != "update_pattern" || actions[1].Pattern == "" {
		t.Fatalf("bad pattern action: %+v", actions[1])
	}
}

func TestNormalizeRuleAdjustment_TopLevelArrayShape(t *testing.T) {
	raw := json.RawMessage(`[{"rule_name": "x", "action": "disable_rule"}]`)
	actions, _ := NormalizeRuleAdjustment(raw)
	if len(actions) != 1 || actions[0].RuleName != "x" {
		t.Fatalf("bad actions: %+v", actions)
	}
}

func TestNormalizeRuleAdjustment_EmptyAndNull(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, []byte("null"), []byte("{}")} {
		actions, _ := NormalizeRuleAdjustment(raw)
		if len(actions) != 0 {
			t.Fatalf("want 0 actions for %q, got %d", string(raw), len(actions))
		}
	}
}

func TestNormalizeRuleAdjustment_InvalidJSON(t *testing.T) {
	actions, note := NormalizeRuleAdjustment(json.RawMessage(`{not json`))
	if len(actions) != 0 {
		t.Fatal("invalid JSON must yield no actions")
	}
	if note == "" {
		t.Fatal("invalid JSON must produce a note")
	}
}

// ★ 安全钉测：白名单外的动作词必须被忽略（LLM 幻觉「删表」之类的
// 建议绝不能变成可执行动作）。
func TestNormalizeRuleAdjustment_NonWhitelistedActionIgnored(t *testing.T) {
	raw := json.RawMessage(`{"action": "drop_table", "rule_name": "x", "table": "users"}`)
	actions, note := NormalizeRuleAdjustment(raw)
	if len(actions) != 0 {
		t.Fatalf("non-whitelisted action must be ignored, got %+v", actions)
	}
	if note == "" {
		t.Fatal("ignored item must be counted in note")
	}
}

func TestNormalizeRuleAdjustment_SeverityClamp(t *testing.T) {
	raw := json.RawMessage(`{"action": "adjust_severity", "rule_name": "x", "severity": 99}`)
	actions, _ := NormalizeRuleAdjustment(raw)
	if len(actions) != 1 || actions[0].Severity != 10 {
		t.Fatalf("severity must clamp to 10, got %+v", actions)
	}

	raw = json.RawMessage(`{"action": "adjust_severity", "rule_name": "x", "severity": -3}`)
	actions, _ = NormalizeRuleAdjustment(raw)
	if len(actions) != 1 || actions[0].Severity != 1 {
		t.Fatalf("severity must clamp to 1, got %+v", actions)
	}
}

// ★ 安全钉测：非法正则（Go regexp 编译不过）必须被忽略——
// 绝不能把编译不过的 pattern 写进库（写进去 detector 就废了）。
func TestNormalizeRuleAdjustment_InvalidRegexIgnored(t *testing.T) {
	raw := json.RawMessage(`{"action": "update_pattern", "rule_name": "x", "pattern": "(?P<n>[unclosed"}`)
	actions, _ := NormalizeRuleAdjustment(raw)
	if len(actions) != 0 {
		t.Fatalf("invalid regex must be ignored, got %+v", actions)
	}
}

// ★ 安全钉测：缺 severity / 缺 pattern 的动作不完整 ⇒ 忽略。
func TestNormalizeRuleAdjustment_IncompleteActionIgnored(t *testing.T) {
	raw := json.RawMessage(`{"action": "adjust_severity", "rule_name": "x"}`)
	actions, _ := NormalizeRuleAdjustment(raw)
	if len(actions) != 0 {
		t.Fatalf("severity action without value must be ignored, got %+v", actions)
	}
}

// ── ApplyFix / RevertFix 端点 ──────────────────────────────────────────────

func newPIFixTestHandler(t *testing.T) (*PIAnalysisHandler, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &PIAnalysisHandler{db: db}
	return h, mock
}

func piFixRequest(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	return httptest.NewRecorder()
}

func TestApplyFix_EmptyAdjustmentRejected(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	rows := sqlmock.NewRows([]string{"rule_adjustment", "confidence_score", "is_false_positive", "auto_applied"}).
		AddRow(nil, 0.95, true, false)
	mock.ExpectQuery("SELECT rule_adjustment, confidence_score").
		WithArgs(int64(1)).WillReturnRows(rows)

	rec := piFixRequest(t, http.MethodPost, "/x")
	h.ApplyFix(rec, httptest.NewRequest(http.MethodPost, "/x", nil), 1)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 for empty adjustment, got %d", rec.Code)
	}
}

func TestApplyFix_LowConfidenceRejected(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	adj := `{"action":"disable_rule","rule_name":"dan_mode"}`
	rows := sqlmock.NewRows([]string{"rule_adjustment", "confidence_score", "is_false_positive", "auto_applied"}).
		AddRow([]byte(adj), 0.42, true, false)
	mock.ExpectQuery("SELECT rule_adjustment, confidence_score").
		WithArgs(int64(1)).WillReturnRows(rows)

	rec := httptest.NewRecorder()
	h.ApplyFix(rec, httptest.NewRequest(http.MethodPost, "/x", nil), 1)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 for low confidence, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "below floor") {
		t.Fatalf("body must explain the floor, got %q", rec.Body.String())
	}
}

func TestApplyFix_AlreadyAppliedRejected(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	rows := sqlmock.NewRows([]string{"rule_adjustment", "confidence_score", "is_false_positive", "auto_applied"}).
		AddRow([]byte(`{"action":"disable_rule","rule_name":"x"}`), 0.95, true, true)
	mock.ExpectQuery("SELECT rule_adjustment, confidence_score").
		WithArgs(int64(1)).WillReturnRows(rows)

	rec := httptest.NewRecorder()
	h.ApplyFix(rec, httptest.NewRequest(http.MethodPost, "/x", nil), 1)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 for already applied, got %d", rec.Code)
	}
}

func TestApplyFix_ResultNotFound(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	mock.ExpectQuery("SELECT rule_adjustment, confidence_score").
		WithArgs(int64(9)).WillReturnError(sql.ErrNoRows)

	rec := httptest.NewRecorder()
	h.ApplyFix(rec, httptest.NewRequest(http.MethodPost, "/x", nil), 9)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// ★ 幻觉规则名：UPDATE 前的 SELECT ... FOR UPDATE 无行 ⇒ 整个事务回滚 + 422。
func TestApplyFix_HallucinatedRuleNameRejected(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	adj := `{"action":"disable_rule","rule_name":"ghost_rule"}`
	resRows := sqlmock.NewRows([]string{"rule_adjustment", "confidence_score", "is_false_positive", "auto_applied"}).
		AddRow([]byte(adj), 0.95, true, false)
	mock.ExpectQuery("SELECT rule_adjustment, confidence_score").
		WithArgs(int64(1)).WillReturnRows(resRows)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT enabled, severity, pattern FROM prompt_injection_rules").
		WithArgs("ghost_rule").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	rec := httptest.NewRecorder()
	h.ApplyFix(rec, httptest.NewRequest(http.MethodPost, "/x", nil), 1)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 for hallucinated rule, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ghost_rule") {
		t.Fatalf("body must name the missing rule, got %q", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestApplyFix_SuccessPathRefreshesRules(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	adj := `{"action":"disable_rule","rule_name":"dan_mode","reason":"benign substring"}`
	resRows := sqlmock.NewRows([]string{"rule_adjustment", "confidence_score", "is_false_positive", "auto_applied"}).
		AddRow([]byte(adj), 0.95, true, false)
	mock.ExpectQuery("SELECT rule_adjustment, confidence_score").
		WithArgs(int64(1)).WillReturnRows(resRows)

	mock.ExpectBegin()
	ruleRows := sqlmock.NewRows([]string{"enabled", "severity", "pattern"}).
		AddRow(true, 10, `(?i)(DAN|do anything now) mode`)
	mock.ExpectQuery("SELECT enabled, severity, pattern FROM prompt_injection_rules").
		WithArgs("dan_mode").WillReturnRows(ruleRows)
	mock.ExpectExec("UPDATE prompt_injection_rules SET enabled = FALSE").
		WithArgs("dan_mode").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE error_analysis_results").
		WithArgs("manual", sqlmock.AnyArg(), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	refreshed := 0
	h.SetRuleRefresher(func() error { refreshed++; return nil })

	rec := httptest.NewRecorder()
	h.ApplyFix(rec, httptest.NewRequest(http.MethodPost, "/x", nil), 1)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if refreshed != 1 {
		t.Fatalf("rule refresher must fire exactly once, got %d", refreshed)
	}

	// 响应体必须含留痕记录（旧值快照）。
	var resp piFixResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Applied) != 1 {
		t.Fatalf("want 1 applied record, got %d", len(resp.Applied))
	}
	rec0 := resp.Applied[0]
	if rec0.RuleName != "dan_mode" || rec0.New["enabled"] != false {
		t.Fatalf("bad record: %+v", rec0)
	}
	if v, ok := rec0.Old["severity"].(float64); !ok || int(v) != 10 {
		t.Fatalf("old severity snapshot missing: %+v", rec0.Old)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRevertFix_NotAppliedRejected(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	rows := sqlmock.NewRows([]string{"auto_applied", "applied_actions"}).
		AddRow(false, nil)
	mock.ExpectQuery("SELECT auto_applied, applied_actions").
		WithArgs(int64(1)).WillReturnRows(rows)

	rec := httptest.NewRecorder()
	h.RevertFix(rec, httptest.NewRequest(http.MethodPost, "/x", nil), 1)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
}

func TestRevertFix_RestoresOldValues(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	applied := `[
		{"action":"disable_rule","rule_name":"dan_mode",
		 "old":{"enabled":true,"severity":10,"pattern":"(?i)old"},
		 "new":{"enabled":false}}
	]`
	rows := sqlmock.NewRows([]string{"auto_applied", "applied_actions"}).
		AddRow(true, []byte(applied))
	mock.ExpectQuery("SELECT auto_applied, applied_actions").
		WithArgs(int64(1)).WillReturnRows(rows)

	mock.ExpectBegin()
	// enabled=true / severity=10 / pattern 旧值都回写。
	mock.ExpectExec("UPDATE prompt_injection_rules SET updated_at = NOW\\(\\), enabled = \\$1, severity = \\$2, pattern = \\$3 WHERE rule_name = \\$4").
		WithArgs(true, 10, "(?i)old", "dan_mode").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE error_analysis_results").
		WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	refreshed := 0
	h.SetRuleRefresher(func() error { refreshed++; return nil })

	rec := httptest.NewRecorder()
	h.RevertFix(rec, httptest.NewRequest(http.MethodPost, "/x", nil), 1)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if refreshed != 1 {
		t.Fatalf("rule refresher must fire after revert, got %d", refreshed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// driver.Value 断言帮助（让 WithArgs 的 int64/bool 字面量匹配可读）。
var _ = driver.Value(nil)

// ★ 2026-11-18 活体回归（211 实测抓到）：GetQueueItem 的可空 JSONB 列
// （error_details / detection_layers / matched_rules / policy_config）
// 在真实数据里就是 NULL ⇒ 结构体字段必须是 NullableJSON——用
// json.RawMessage 会 "unsupported Scan, storing driver.Value type <nil>
// into type *jsontext.Value" ⇒ 端点 500。此钉测用 sqlmock 造全 NULL
// 行锁住这个形状（首版正是漏了它才被活体打回）。
func TestGetQueueItem_NullJSONColumnsScan(t *testing.T) {
	h, mock := newPIFixTestHandler(t)
	rows := sqlmock.NewRows([]string{
		"id", "error_type", "error_hash", "priority",
		"tenant_id", "session_id", "request_id",
		"input_text", "input_hash", "error_message", "error_details",
		"detection_layers", "matched_rules", "policy_config",
		"status", "retry_count", "max_retries",
		"created_at", "started_at", "completed_at",
	}).AddRow(
		1, "llm_detection_failed", "abc", 5,
		"default", nil, nil,
		"some input", nil, "boom", nil,
		nil, nil, nil,
		"pending", 0, 3,
		"2026-11-18T12:00:00Z", nil, nil,
	)
	mock.ExpectQuery("SELECT").WithArgs(int64(1)).WillReturnRows(rows)

	rec := httptest.NewRecorder()
	h.GetQueueItem(rec, httptest.NewRequest(http.MethodGet, "/x", nil), 1)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with all-NULL jsonb columns, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, key := range []string{`"ErrorDetails":null`, `"DetectionLayers":null`, `"MatchedRules":null`, `"PolicyConfig":null`} {
		if !strings.Contains(body, key) {
			t.Fatalf("body must serialize null jsonb as %s, body=%s", key, body)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
