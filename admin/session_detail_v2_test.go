// Package admin - session_detail_v2_test.go
//
// 2026-08-26: 为 session_detail_v2.go 加上 pgxmock 覆盖，断言 LEFT JOIN LATERAL
// session_analysis_metadata 的列顺序、Scan 顺序和 payload 解码语义。
// 这层之前没有任何测试（覆盖盲点），后续如果 querySession 的 SELECT 改动
// 静默回归到不带 SessionAnalysis 的旧形状，本测试会立即失败。

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
)

// makeSessionDetailMockRow 构造 querySession 期望的 pgxmock 行。
// 列顺序必须与 admin/session_detail_v2.go:querySession 的 Scan 一一对应。
//
// pgxmock 适配：可空列（*int / *string / *time.Time）必须以指针值传入；
// 不可空列（int / string / time.Time）以裸值传入。这是 pgx Scan 的
// `database/sql` 兼容规则 —— pgxmock 严格按 dest 类型做 NULL 检查。
func makeSessionDetailMockRow(payload []byte) *pgxmock.Rows {
	return makeSessionDetailMockRowForTenant(payload, "tenant-a")
}

func makeSessionDetailMockRowForTenant(payload []byte, tenant string) *pgxmock.Rows {
	now := time.Now().UTC()
	saUpdatedAt := now.Add(-30 * time.Second)
	lastTurn := 3
	lastReq := "last req"
	lastResp := "last resp"
	lastModel := "claude-sonnet-4-5"
	lastProvider := "anthropic"
	taskType := "chat"
	clientType := "openai"
	topic := "topic-x"
	intent := "intent-y"
	primaryReq := "req-xyz"
	sourceTask := "task-99"
	return pgxmock.NewRows([]string{
		"id", "session_id", "tenant_id", "created_at", "updated_at", "closed_at", "status",
		"total_turns", "total_tokens", "total_cost_usd",
		"last_turn_no", "last_request_summary", "last_response_summary",
		"last_model", "last_provider",
		"task_type", "client_type", "topic", "intent",
		"primary_request_id", "turn_logs_summary",
		// session_analysis_metadata LATERAL 列（顺序固定，见 session_meta_view.go）
		// 注意: sa_status/sa_schema_version/sa_input_hash 在生产代码里以 *string
		// 接收 (LEFT JOIN miss 时为 SQL NULL), 因此 pgxmock 必须以 *string
		// 指针值传入, 否则报 destination kind 'ptr' not supported。
		"sa_status", "sa_schema_version", "sa_input_hash",
		"sa_source_task_id", "sa_updated_at", "sa_payload",
	}).AddRow(
		int64(42), "gw_abc", tenant,
		now.Add(-2*time.Hour), now, nil, "active",
		3, 1024, 0.0123,
		&lastTurn, &lastReq, &lastResp,
		&lastModel, &lastProvider,
		&taskType, &clientType, &topic, &intent,
		&primaryReq, []byte(`{"hint":"ok"}`),
		ptrStr("final"), ptrStr("session-analysis/v1"), ptrStr("hash-001"),
		&sourceTask, &saUpdatedAt, payload,
	)
}

func ptrBool(v bool) *bool { return &v }

func makeSessionTurnMockRows() *pgxmock.Rows {
	now := time.Now().UTC()
	return pgxmock.NewRows([]string{
		"id", "session_id", "turn_no", "tenant_id", "request_id", "ts",
		"submit_mode", "compression_applied", "compression_strategy", "compression_meta", "compression_tokens_saved",
		"injection_verdict", "output_verdict", "model", "provider", "credential_id",
		"prompt_tokens", "completion_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd", "latency_ms", "status_code", "success", "error_kind",
		"source_kind", "quality", "request_delta", "response_delta", "outbound_body", "request_attachments", "response_attachments",
	}).AddRow(
		int64(1), "gw_abc", 3, "tenant-a", "req-1", now,
		"chat", false, nil, []byte(`{}`), nil,
		"pass", "pass", ptrStr("model"), ptrStr("provider"), ptrStr("cred"),
		nil, nil, nil, nil, nil, nil, nil, ptrBool(true), nil,
		"gateway", "good", []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`[]`), []byte(`[]`),
	)
}

func newSessionDetailAuthRequest(t *testing.T, target, role, tenant string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	return SetAuthContext(r, &AuthContext{TenantID: tenant, Role: role, IsJWT: true})
}

func TestSessionDetailServeHTTPPinsTenantAdminToAuthTenant(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(`LEFT JOIN LATERAL`).WithArgs("gw_abc", "tenant-a").
		WillReturnRows(makeSessionDetailMockRowForTenant(nil, "tenant-a"))
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("gw_abc", "tenant-a", 20, 0).
		WillReturnRows(makeSessionTurnMockRows())

	api := newSessionDetailV2APIWithDB(mock)
	req := newSessionDetailAuthRequest(t, "/api/admin/sessions/detail?session_id=gw_abc&tenant=tenant-b&limit=20", "tenant_admin", "tenant-a")
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionDetailServeHTTPAllowsSuperAdminTenantSelection(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(`LEFT JOIN LATERAL`).WithArgs("gw_abc", "tenant-b").
		WillReturnRows(makeSessionDetailMockRowForTenant(nil, "tenant-b"))
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("gw_abc", "tenant-b", 50, 0).
		WillReturnRows(makeSessionTurnMockRows())

	api := newSessionDetailV2APIWithDB(mock)
	req := newSessionDetailAuthRequest(t, "/api/admin/sessions/detail?session_id=gw_abc&tenant=tenant-b", "super_admin", "tenant-a")
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionDetailServeHTTPRejectsMissingAuthContext(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	api := newSessionDetailV2APIWithDB(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/detail?session_id=gw_abc&tenant=tenant-a", nil)
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQuerySession_PopulatesSessionAnalysis(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// 显式断言 SELECT 触发了 LEFT JOIN LATERAL session_analysis_metadata，
	// 防止后续重构悄悄把 join 改回普通 LEFT JOIN（会导致一对多行重复）。
	mock.ExpectQuery(`LEFT JOIN LATERAL`).
		WithArgs("gw_abc", "tenant-a").
		WillReturnRows(makeSessionDetailMockRow([]byte(`{
			"schema_version": "session-analysis/v1",
			"analysis_kind": "session_metadata",
			"status": "final",
			"title": "登录修复",
			"agent": {"name": "code-reviewer", "type": "tool"},
			"client": {"type": "openai"},
			"expert": {"type": "code", "source": "system", "confidence": 0.9},
			"work_types": [{"type": "code", "score": 0.8}],
			"project": {"label": "core", "source": "rule"},
			"input_hash": "hash-001",
			"provenance": {"extractor": "session-analysis/v1/rule"}
		}`)))

	api := newSessionDetailV2APIWithDB(mock)
	got, err := api.querySession(t.Context(), "gw_abc", "tenant-a")
	if err != nil {
		t.Fatalf("querySession: %v", err)
	}
	if got == nil {
		t.Fatal("querySession returned nil for non-empty row")
	}
	if got.SessionAnalysis == nil {
		t.Fatal("SessionAnalysis must be populated when sam.status='final' is non-empty")
	}
	if got.SessionAnalysis.Status != "final" {
		t.Errorf("Status=%q, want final", got.SessionAnalysis.Status)
	}
	if got.SessionAnalysis.InputHash != "hash-001" {
		t.Errorf("InputHash=%q, want hash-001", got.SessionAnalysis.InputHash)
	}
	if got.SessionAnalysis.Payload == nil {
		t.Fatal("Payload must decode from sam.payload jsonb")
	}
	if got.SessionAnalysis.Payload.Title != "登录修复" {
		t.Errorf("Payload.Title=%q, want 登录修复", got.SessionAnalysis.Payload.Title)
	}
	if got.SessionAnalysis.Payload.InputHash != "hash-001" {
		t.Errorf("Payload.InputHash=%q, want hash-001 (column is authoritative)", got.SessionAnalysis.Payload.InputHash)
	}
	if got.SessionAnalysis.Payload.Agent.Name != "code-reviewer" {
		t.Errorf("Payload.Agent.Name=%q, want code-reviewer", got.SessionAnalysis.Payload.Agent.Name)
	}
	if got.SessionAnalysis.Payload.Project.Label != "core" {
		t.Errorf("Payload.Project.Label=%q, want core", got.SessionAnalysis.Payload.Project.Label)
	}
	if got.TurnLogsSummary == nil {
		t.Error("TurnLogsSummary must still parse from turn_logs_summary jsonb")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB calls: %v", err)
	}
}

func TestQuerySession_LeftJoinMissLeavesAnalysisNil(t *testing.T) {
	// LEFT JOIN LATERAL 未命中：sam.status='' → SessionAnalysis 必须保持 nil。
	// 这条路径对应"会话尚未经过分析（arrival 钩子被禁用或还没跑过 final 阶段）"。
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	now := time.Now().UTC()
	mock.ExpectQuery(`LEFT JOIN LATERAL`).
		WithArgs("gw_pending", "tenant-a").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "tenant_id", "created_at", "updated_at", "closed_at", "status",
			"total_turns", "total_tokens", "total_cost_usd",
			"last_turn_no", "last_request_summary", "last_response_summary",
			"last_model", "last_provider",
			"task_type", "client_type", "topic", "intent",
			"primary_request_id", "turn_logs_summary",
			"sa_status", "sa_schema_version", "sa_input_hash",
			"sa_source_task_id", "sa_updated_at", "sa_payload",
		}).AddRow(
			int64(7), "gw_pending", "tenant-a",
			now, now, nil, "active",
			0, 0, 0,
			nil, nil, nil, nil, nil,
			nil, nil, nil, nil,
			nil, nil,
			nil, nil, nil, nil, nil, nil,
		))

	api := newSessionDetailV2APIWithDB(mock)
	got, err := api.querySession(t.Context(), "gw_pending", "tenant-a")
	if err != nil {
		t.Fatalf("querySession: %v", err)
	}
	if got == nil {
		t.Fatal("querySession returned nil for present row")
	}
	if got.SessionAnalysis != nil {
		t.Errorf("SessionAnalysis must be nil on LEFT JOIN miss, got %+v", got.SessionAnalysis)
	}
	if got.TurnLogsSummary != nil {
		t.Errorf("TurnLogsSummary must remain nil when column is NULL")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB calls: %v", err)
	}
}

func TestQuerySession_NoRowsReturnsNilSessionNilError(t *testing.T) {
	// 现有实现用 err.Error() == "no rows in result set" 把 pgx.ErrNoRows 翻译成
	// (nil, nil) —— 调用方 querySessionDetail 进一步把它当 404 处理。本测试
	// 锁住这个契约，避免后续"修复"成 errors.Is 时悄悄改变 404 的语义。
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`LEFT JOIN LATERAL`).
		WithArgs("missing", "tenant-a").
		WillReturnError(pgx.ErrNoRows)

	api := newSessionDetailV2APIWithDB(mock)
	got, err := api.querySession(t.Context(), "missing", "tenant-a")
	if err != nil {
		t.Fatalf("expected nil err (no-rows translated), got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil session on no rows, got %+v", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB calls: %v", err)
	}
}

func TestScanSessionAnalysis_AuthoritativeInputHash(t *testing.T) {
	// 即使 payload 因 omitempty 丢掉了 input_hash，scanSessionAnalysis 必须
	// 回填 column.input_hash 进去。extractor.go 的 omitempty 审计修复后这条
	// 路径不常触发，但作为防御层保留。
	payload := []byte(`{
		"schema_version": "session-analysis/v1",
		"status": "provisional",
		"title": "draft",
		"input_hash": ""
	}`)
	var view SessionAnalysisView
	scanSessionAnalysis(&view, "provisional", "session-analysis/v1", "column-hash", nil, nil, payload)
	if view.Status != "provisional" {
		t.Errorf("Status=%q", view.Status)
	}
	if view.InputHash != "column-hash" {
		t.Errorf("InputHash=%q, want column-hash", view.InputHash)
	}
	if view.Payload == nil {
		t.Fatal("Payload must decode")
	}
	if view.Payload.InputHash != "column-hash" {
		t.Errorf("Payload.InputHash=%q, want column-hash (backstop)", view.Payload.InputHash)
	}
	if view.Payload.Title != "draft" {
		t.Errorf("Payload.Title=%q", view.Payload.Title)
	}
}

func TestScanSessionAnalysis_InvalidJSONLeavesPayloadNil(t *testing.T) {
	var view SessionAnalysisView
	scanSessionAnalysis(&view, "final", "session-analysis/v1", "h", nil, nil, []byte(`{not-json`))
	if view.Status != "final" {
		t.Errorf("Status=%q, want final (column still set)", view.Status)
	}
	if view.Payload != nil {
		t.Errorf("Payload must be nil on decode failure, got %+v", view.Payload)
	}
	if view.InputHash != "h" {
		t.Errorf("InputHash=%q", view.InputHash)
	}
}

// TestSessionAnalysisView_RoundTripJSON 验证结构体序列化为 JSON 后再解析回来
// 仍保留 SessionAnalysisView 的全部字段，防止 json tag 拼错导致前端丢失。
func TestSessionAnalysisView_RoundTripJSON(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	src := SessionAnalysisView{
		Status:        "final",
		SchemaVersion: "session-analysis/v1",
		InputHash:     "h-1",
		SourceTaskID:  sessionAnalysisStrPtr("task-7"),
		UpdatedAt:     &now,
		Payload: &sessionmeta.Result{
			SchemaVersion: "session-analysis/v1",
			Status:        "final",
			Title:         "t",
			InputHash:     "h-1",
		},
	}
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var dst SessionAnalysisView
	if err := json.Unmarshal(raw, &dst); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if dst.Status != src.Status || dst.InputHash != src.InputHash || dst.SchemaVersion != src.SchemaVersion {
		t.Errorf("scalar fields lost: %+v vs %+v", src, dst)
	}
	if dst.UpdatedAt == nil || src.UpdatedAt == nil || !dst.UpdatedAt.Equal(*src.UpdatedAt) {
		t.Errorf("UpdatedAt mismatch: dst=%v src=%v", dst.UpdatedAt, src.UpdatedAt)
	}
	if dst.SourceTaskID == nil || *dst.SourceTaskID != *src.SourceTaskID {
		t.Errorf("SourceTaskID mismatch: %v vs %v", dst.SourceTaskID, src.SourceTaskID)
	}
	if dst.Payload == nil || dst.Payload.Title != src.Payload.Title {
		t.Errorf("Payload mismatch: %+v vs %+v", dst.Payload, src.Payload)
	}
}

// sessionAnalysisStrPtr returns a pointer to the given string. Use instead of a
// generic helper to avoid colliding with admin.ptrString / admin.strPtr.
func sessionAnalysisStrPtr(s string) *string { return &s }
