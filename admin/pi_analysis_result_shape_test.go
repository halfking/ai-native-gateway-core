package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestGetResultJSONShapeIsSnakeCase 钉死 GET /results/{id} 的序列化形状。
//
// 审计轮 D12：本轮为修「前端拿到 [object Object]」造了 PiNullString，
// 但 GetResult 的 AnalyzerVersion / LLMResponseRaw 两个字段**漏改**，
// 仍用 sql.NullString ⇒ 输出 {"String":"..","Valid":..} 对象。
// 既有 TestDetailJSONShapeIsSnakeCase 只覆盖 GetQueueItem ⇒ 漏网。
//
// 本测试覆盖 GetResult，并**特意让两个可空列取 NULL**——这正是
// 漏改时输出 {"String":"","Valid":false} 的那个分支。
func TestGetResultJSONShapeIsSnakeCase(t *testing.T) {
	h, mock := newPIFixTestHandler(t)

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
	rows := sqlmock.NewRows(cols).AddRow(
		int64(1), "analysis-1", int64(1),                    // id, analysis_id, queue_id
		"root", "category", false,                           // root_cause, error_category, is_false_positive
		0.9, "fix",                                          // confidence_score, suggested_fix
		nil, nil,                                            // rule_adjustment, code_changes
		"high", "global", 3,                                 // severity_level, impact_scope, affected_users_estimate
		nil, 1,                                              // similar_cases, pattern_frequency
		10, 20,                                              // analysis_duration_ms, llm_tokens_used
		"2026-11-19T00:00:00Z",                              // created_at
		"model-x", nil,                                      // analyzer_model, analyzer_version (NULL!)
		nil, nil,                                            // analysis_details, llm_response_raw (NULL!)
		false, nil, nil, nil,                                // auto_applied, applied_at, applied_by, applied_actions
		"rule_fp", "hash", 5,                                // error_type, error_hash, priority
		"default", nil, nil,                                 // tenant_id, session_id, request_id
		"input", nil, "msg", nil,                            // input_text, input_hash, error_message, error_details
		nil, nil, nil,                                       // detection_layers, matched_rules, policy_config
		"pending", 0,                                        // status, retry_count
		"2026-11-19T00:00:00Z", nil, nil,                    // queued_at, started_at, completed_at
	)
	mock.ExpectQuery("SELECT").WithArgs(int64(1)).WillReturnRows(rows)

	rec := httptest.NewRecorder()
	h.GetResult(rec, httptest.NewRequest(http.MethodGet, "/x", nil), 1)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// 必须用 snake_case
	for _, key := range []string{
		`"analysis_id"`, `"rule_adjustment"`, `"analyzer_version"`,
		`"llm_response_raw"`, `"created_at"`, `"queued_at"`, `"error_type"`,
	} {
		if !strings.Contains(body, key) {
			t.Errorf("result JSON must use snake_case, missing %s in %s", key, body)
		}
	}
	// 不得出现 Go 导出名
	for _, bad := range []string{
		`"AnalysisID"`, `"AnalyzerVersion"`, `"LLMResponseRaw"`,
		`"CreatedAt"`, `"ErrorType"`, `"MatchedRules"`,
	} {
		if strings.Contains(body, bad) {
			t.Errorf("result JSON must not contain Go field name %s in %s", bad, body)
		}
	}
	// ★ D12 核心断言：可空字符串列必须是 JSON null，不是 {"String":..,"Valid":..}
	for _, bad := range []string{`"String"`, `"Valid"`} {
		if strings.Contains(body, bad) {
			t.Errorf("result JSON must not leak sql.NullString internals (%s) in %s", bad, body)
		}
	}
	if !strings.Contains(body, `"analyzer_version":null`) {
		t.Errorf("analyzer_version must serialize as null, got %s", body)
	}
	if !strings.Contains(body, `"llm_response_raw":null`) {
		t.Errorf("llm_response_raw must serialize as null, got %s", body)
	}
}
