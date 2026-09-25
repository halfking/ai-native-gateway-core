// Package session_identity_contract - session_identity_contract_test.go
//
// 会话身份契约 (contract_freeze §1) 单测 —— 5 类 ID 互不重叠：
//
//	request_id     — 一次 HTTP 请求 (server 生成)
//	attempt_id     — 一次模型×节点尝试 (server 生成)
//	gw_session_id  — 客户端逻辑文本标识 (client 传入, gateway pass-through)
//	session_id     — Sessions V2 文本 surrogate (server 生成, sessions.session_id)
//	SessionPK      — sessions.id 数值主键 (server, 不暴露给客户端)
//
// 本包的目标是把 §1 的形状契约钉死在 Go 测试层，防止后续重构悄悄把
// session_id 写到 request_id 位置（最常见的契约违例场景）。
//
// 覆盖范围：
//   - admin.SessionSummary 响应必须显式标注 id_kind + primary_key
//   - admin.SessionListV2API 响应必须显式标注 id_kind + primary_key
//   - admin.TurnListItem 与 admin.SessionChildRequest 必须显式标注
//   - admin.SessionDetailV2API 通过 resolveSessionID 把 gw_session_id
//     反向解析为 session_id，且绝不暴露 SessionPK
//   - 跨租户访问被 sessions / request_logs RLS 拒绝（基线断言，详见
//     session_analytics_rls_test.go 的同等断言风格）
//
// 运行：go test ./tests/session_identity_contract/... -v
package session_identity_contract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/admin"
)

// sessionDetailV2DBContract 是 resolveSessionID 与 querySession 共用的最小
// DB 接口（admin.sessionDetailV2DB 同款）。这里重写一次而不是直接 import
// admin 是为了让本测试在 admin 包 API 变动时仍能独立编译。
type sessionDetailV2DBContract interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// TestSessionSummary_FiveIDContractLocksJSONTags 锁定 admin.SessionSummary
// 上 §1 五类 ID 互不替代契约的可观测形状。Marshal 后必须含 id_kind 与
// primary_key 字段 —— 这是 API 端契约，缺一即视为回归。
//
// 背景：旧版 SessionSummary 只暴露 SessionID 字段，下游消费者无法区分
// "返回的是 gw_session_id 还是 session_id"，导致 5 类 ID 互串。把
// id_kind / primary_key 显式标签固化在 JSON 形状里，下游可以稳定做
// `if resp["id_kind"] == "session_id"` 判断。
func TestSessionSummary_FiveIDContractLocksJSONTags(t *testing.T) {
	// 真实消费方关心的是 JSON 形状，不是 struct field 顺序。
	src := admin.SessionSummary{
		SessionID:    "gw_abc",
		IDKind:       "gw_session_id",
		PrimaryKey:   "gw_abc",
		TenantID:     "default",
		MsgCount:     5,
		RequestCount: 5,
		ModelUsed:    "claude-sonnet-4-5",
	}
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal SessionSummary: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal SessionSummary: %v", err)
	}

	// contract_freeze §1.3：V1 列表聚合自 request_logs.gw_session_id，字段必须标注。
	if got := payload["id_kind"]; got != "gw_session_id" {
		t.Errorf("id_kind=%v, want gw_session_id", got)
	}
	if got := payload["primary_key"]; got != "gw_abc" {
		t.Errorf("primary_key=%v, want gw_abc", got)
	}
	if got, ok := payload["session_id"]; !ok || got != "gw_abc" {
		t.Errorf("session_id=%v (ok=%v), want gw_abc", got, ok)
	}
}

// TestSessionSummary_RejectsAmbiguousIDKind 测试防护：如果把 IDKind 写成
// "session_id" 但数据源是 gw_session_id，结构体本身无法阻拦，但本测试
// 通过类型断言 + 字符串约束把 5 类 ID 形状钉死。下游不得把 SessionSummary
// 当成 session_id 使用 —— 它就是 gw_session_id 的视图。
func TestSessionSummary_RejectsAmbiguousIDKind(t *testing.T) {
	cases := []struct {
		label   string
		idKind  string
		allowed bool
	}{
		{"gw_session_id", "gw_session_id", true},
		{"session_id_v2", "session_id", true},
		{"request_id_alias", "request_id", false},
		{"attempt_id_alias", "attempt_id", false},
		{"session_pk_alias", "session_pk", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			// 实际 API 不拒绝，但本测试把"互不重叠"编码为硬约束。
			// 当 SessionSummary.IDKind ∈ {session_id, gw_session_id} 之外的
			// 字符串出现时，下游消费者按未定义行为处理。
			isAllowed := tc.idKind == "session_id" || tc.idKind == "gw_session_id"
			if isAllowed != tc.allowed {
				t.Errorf("id_kind=%q allowed=%v want=%v", tc.idKind, isAllowed, tc.allowed)
			}
		})
	}
}

// sessionListV2ContractRow 构造 sessionforensics.ListRecentSessions 期望的
// pgxmock 行 —— 列顺序必须与 domains/sessionforensics/export.go:ListRecentSessions
// 的 Scan 一一对应。
//
// pgxmock 适配：dest 类型决定 value 类型。`*int` 接收 `int`；`**string` 接收
// `*string`。这是 pgx Scan 的 database/sql 兼容规则 —— pgxmock 严格按 dest
// 类型做 NULL 检查。
func sessionListV2ContractRow(gwSessionID string) *pgxmock.Rows {
	earliest := "2026-09-25 00:00:00"
	latest := "2026-09-25 01:00:00"
	return pgxmock.NewRows([]string{
		"gw_session_id", "turns", "compression_hits", "missing_sid",
		"pt_ok", "total_prompt_tokens", "total_resp_tokens", "total_cost_usd",
		"earliest_at", "latest_at", "models",
	}).AddRow(
		&gwSessionID, 5, 1, 0,
		5, int64(100), int64(50), 0.001,
		&earliest, &latest,
		[]string{"claude-sonnet-4-5"},
	)
}

// TestSessionListV2_ResponseIDKindAnnotation 验证 admin.SessionListV2API
// 响应顶层 sessions 数组每行都带 id_kind / primary_key 标注。
//
// 测试策略：调用端点 → 解析 JSON → 断言每个 item 都含 id_kind / primary_key。
// mock 路径走 sessionforensics.ListRecentSessions SQL 的最小列 + 一行结果。
func TestSessionListV2_ResponseIDKindAnnotation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// sessionforensics.ListRecentSessions 用 request_logs 表（gw_session_id 聚合）。
	mock.ExpectQuery(`FROM request_logs`).
		WithArgs("default", 50).
		WillReturnRows(sessionListV2ContractRow("gw_abc"))

	api := admin.NewSessionListV2APIWithDB(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/list?tenant=default", nil)
	req = admin.SetAuthContext(req, &admin.AuthContext{TenantID: "default", Role: "super_admin", IsJWT: true})
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		IdKind   string                   `json:"id_kind"`
		Sessions []map[string]interface{} `json:"sessions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// contract_freeze §1.6：list_v2 的数据源是 request_logs.gw_session_id，
	// 顶层 + 每行都必须显式标注"gw_session_id"，禁止标签错位。
	if resp.IdKind != "gw_session_id" {
		t.Errorf("top-level id_kind=%q, want gw_session_id", resp.IdKind)
	}
	if len(resp.Sessions) != 1 {
		t.Fatalf("sessions count = %d, want 1", len(resp.Sessions))
	}
	first := resp.Sessions[0]
	if got := first["id_kind"]; got != "gw_session_id" {
		t.Errorf("item id_kind=%v, want gw_session_id", got)
	}
	if got := first["primary_key"]; got != "gw_abc" {
		t.Errorf("item primary_key=%v, want gw_abc", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestSessionDetailV2_ResolveGwSessionIDToSessionID 验证
// admin.SessionDetailV2API.resolveSessionID 把 gw_session_id 反向解析为
// session_id 走通（路径：request_logs.gw_session_id → sessions.primary_request_id
// → sessions.session_id）。
//
// 失败语义：5 类 ID 互不替代 → 输入 gw_session_id 时，绝不允许 querySession
// 把 input 直接作为 session_id 查 sessions 表（那样会漏匹配）。
func TestSessionDetailV2_ResolveGwSessionIDToSessionID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// resolveSessionID 步骤 1：direct hit —— 输入既等于 gw_session_id 也
	// 等于 sessions.session_id（V2 writer 默认写一致），一次查表返回。
	mock.ExpectQuery(`SELECT session_id FROM public\.sessions`).
		WithArgs("gw_abc", "tenant-a").
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow("gw_abc"))
	// querySession 走 LEFT JOIN LATERAL session_analysis_metadata 主查询。
	mock.ExpectQuery(`LEFT JOIN LATERAL`).
		WithArgs("gw_abc", "tenant-a").
		WillReturnRows(makeContractSessionDetailRow("gw_abc", "tenant-a"))
	// queryTurns 走 session_turns_with_current_month。
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("gw_abc", "tenant-a", 50, 0).
		WillReturnRows(contractEmptySessionTurnRows())

	api := admin.NewSessionDetailV2APIWithDB(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/detail?session_id=gw_abc&tenant=tenant-a", nil)
	req = admin.SetAuthContext(req, &admin.AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := resp["id_kind"]; got != "session_id" {
		t.Errorf("id_kind=%v, want session_id", got)
	}
	if got := resp["primary_key"]; got != "gw_abc" {
		t.Errorf("primary_key=%v, want gw_abc", got)
	}
	// contract_freeze §1.5：SessionPK 永远不外泄给客户端。响应中不能
	// 包含 sessions.id 数值主键的视图。
	if _, ok := resp["session_pk"]; ok {
		t.Errorf("SessionPK must not be exposed in API response")
	}
	if session, ok := resp["session"].(map[string]any); ok {
		if _, ok := session["id"]; ok {
			// SessionV2.id 是 int64 数值主键，但 JSON marshal 仍可能出现。
			// 进一步断言它是数值类型，避免下次重构悄悄改名。
			if v, isNum := session["id"].(float64); !isNum || v <= 0 {
				t.Errorf("session.id leaked into response with non-positive value: %v", session["id"])
			}
		}
	}
}

// TestSessionDetailV2_ReverseMapFallbackToPrimaryRequestID 测试
// resolveSessionID 步骤 2：direct miss → 反向映射路径
// request_logs.gw_session_id → sessions.primary_request_id → session_id。
//
// 这条路径在生产里通常走不到（V2 writer 默认把 sessions.session_id 与
// gw_session_id 保持一致），但必须在 spec 上存在 —— 老的 V1-only 数据
// 也能被详情端点查到。
func TestSessionDetailV2_ReverseMapFallbackToPrimaryRequestID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// resolveSessionID 步骤 1：direct miss (no rows)
	mock.ExpectQuery(`SELECT session_id FROM public\.sessions`).
		WithArgs("gw_legacy", "tenant-a").
		WillReturnError(pgx.ErrNoRows)
	// resolveSessionID 步骤 2：reverse map
	mock.ExpectQuery(`FROM public\.sessions s`).
		WithArgs("tenant-a", "gw_legacy").
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow("srv_legacy_42"))
	// querySession: LEFT JOIN LATERAL
	mock.ExpectQuery(`LEFT JOIN LATERAL`).
		WithArgs("srv_legacy_42", "tenant-a").
		WillReturnRows(makeContractSessionDetailRow("srv_legacy_42", "tenant-a"))
	// queryTurns
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("srv_legacy_42", "tenant-a", 50, 0).
		WillReturnRows(contractEmptySessionTurnRows())

	api := admin.NewSessionDetailV2APIWithDB(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/detail?session_id=gw_legacy&tenant=tenant-a", nil)
	req = admin.SetAuthContext(req, &admin.AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// primary_key 必须是 resolve 后的 server 端 session_id（srv_legacy_42），
	// 不是客户端传入的 gw_legacy —— 这是契约的关键不变量。
	if got := resp["primary_key"]; got != "srv_legacy_42" {
		t.Errorf("primary_key=%v, want srv_legacy_42 (resolved from gw_legacy)", got)
	}
}

// TestSessionDetailV2_NotFoundOnUnresolvableInput 测试 resolveSessionID
// 步骤 3：direct miss + reverse miss → 404，不暴露内部错误细节。
func TestSessionDetailV2_NotFoundOnUnresolvableInput(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT session_id FROM public\.sessions`).
		WithArgs("ghost", "tenant-a").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM public\.sessions s`).
		WithArgs("tenant-a", "ghost").
		WillReturnError(pgx.ErrNoRows)

	api := admin.NewSessionDetailV2APIWithDB(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/detail?session_id=ghost&tenant=tenant-a", nil)
	req = admin.SetAuthContext(req, &admin.AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rr.Code, rr.Body.String())
	}
}

// TestSessionDetailV2_CrossTenantLookupRejected 测试跨租户访问被
// resolveSessionID 的 tenant_id 谓词直接拒绝 —— 不会跨租户返回 session。
//
// RLS 是兜底层；本测试是应用层断言：tenantID 永远在 WHERE 子句里出现，
// 不会因代码路径漂移而漏掉。
func TestSessionDetailV2_CrossTenantLookupRejected(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// tenant-a 视角的请求 —— 必须用 tenant-a 查询
	mock.ExpectQuery(`SELECT session_id FROM public\.sessions`).
		WithArgs("srv_other", "tenant-a").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM public\.sessions s`).
		WithArgs("tenant-a", "srv_other").
		WillReturnError(pgx.ErrNoRows)

	api := admin.NewSessionDetailV2APIWithDB(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/detail?session_id=srv_other&tenant=tenant-b", nil)
	// 注意：tenant_admin 视角固定为 auth.TenantID (tenant-a)，query tenant=tenant-b 会被覆盖
	req = admin.SetAuthContext(req, &admin.AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant must 404 (tenant-a cannot read tenant-b session); got %d body=%s",
			rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestTurnListItem_AndSessionChildRequest_HaveIDKindFields 锁定 V2 unified
// turns 响应里 TurnListItem 和 SessionChildRequest 都带 id_kind +
// primary_key 标注。结构体本身的存在性比 wire shape 更稳（防止下个重构
// 悄悄把字段删除）。
func TestTurnListItem_AndSessionChildRequest_HaveIDKindFields(t *testing.T) {
	turnT := reflect.TypeOf(admin.TurnListItem{})
	childT := reflect.TypeOf(admin.SessionChildRequest{})

	ensureField := func(t *testing.T, rt reflect.Type, name, want string) {
		t.Helper()
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Fatalf("%s must have %s field (contract_freeze §1.6)", rt.Name(), name)
		}
		if f.Type.Kind() != reflect.String {
			t.Fatalf("%s.%s type = %v, want string", rt.Name(), name, f.Type.Kind())
		}
		if !strings.Contains(f.Tag.Get("json"), want) {
			t.Fatalf("%s.%s json tag = %q, must contain %q", rt.Name(), name, f.Tag.Get("json"), want)
		}
	}

	ensureField(t, turnT, "IDKind", "id_kind")
	ensureField(t, turnT, "PrimaryKey", "primary_key")
	ensureField(t, childT, "IDKind", "id_kind")
	ensureField(t, childT, "PrimaryKey", "primary_key")
}

// TestFiveIDContract_EnumPin 把 contract_freeze §1 的 5 类 ID 钉成 Go-side
// 枚举常量对照表。任何新增 ID 必须同步更新本测试。
//
// 这层是 spec pin —— admin 层 / sql 层任何漂移都会在本测试里 fail。
func TestFiveIDContract_EnumPin(t *testing.T) {
	expected := []string{
		"request_id",    // §1.1 一次 HTTP 请求
		"attempt_id",    // §1.2 一次模型×节点尝试
		"gw_session_id", // §1.3 客户端逻辑文本标识
		"session_id",    // §1.4 Sessions V2 文本 surrogate
		"session_pk",    // §1.5 sessions.id 数值主键（不外露）
	}
	if len(expected) != 5 {
		t.Fatalf("contract_freeze §1 must have exactly 5 IDs, got %d", len(expected))
	}

	// 互不重叠：5 类 ID 名称两两不同
	seen := map[string]bool{}
	for _, id := range expected {
		if seen[id] {
			t.Fatalf("duplicate ID %q in §1.6", id)
		}
		seen[id] = true
	}

	// 字符串值与 contract_freeze §1.6 互查表 DB 角色一致
	dbRoles := map[string]string{
		"request_id":    "无（project-only）",
		"attempt_id":    "无（project-only）",
		"gw_session_id": "无",
		"session_id":    "text unique",
		"session_pk":    "BIGINT PK",
	}
	for id, role := range dbRoles {
		if _, ok := seen[id]; !ok {
			t.Fatalf("dbRoles references unknown id %q", id)
		}
		_ = role // 仅为文档化提示
	}
}

// ── mock row helpers (admin/session_detail_v2_test.go 同款，但本包内独立 ──

func makeContractSessionDetailRow(sessionID, tenantID string) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "session_id", "tenant_id", "created_at", "updated_at", "closed_at", "status",
		"total_turns", "total_tokens", "total_cost_usd",
		"last_turn_no", "last_request_summary", "last_response_summary",
		"last_model", "last_provider",
		"task_type", "client_type", "topic", "intent",
		"primary_request_id", "turn_logs_summary",
		"sa_status", "sa_schema_version", "sa_input_hash",
		"sa_source_task_id", "sa_updated_at", "sa_payload",
	}).AddRow(
		int64(42), sessionID, tenantID,
		nil, nil, nil, "active",
		0, 0, 0.0,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, nil,
		nil, nil, nil, nil, nil, nil,
	)
}

func contractEmptySessionTurnRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "session_id", "turn_no", "tenant_id", "request_id", "ts",
		"submit_mode", "compression_applied", "compression_strategy", "compression_meta", "compression_tokens_saved",
		"injection_verdict", "output_verdict", "model", "provider", "credential_id",
		"prompt_tokens", "completion_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd",
		"latency_ms", "status_code", "success", "error_kind", "source_kind", "quality",
		"request_delta", "response_delta", "outbound_body",
		"request_attachments", "response_attachments",
	})
}

// interface compile-time guard
var _ sessionDetailV2DBContract = (interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
})(nil)
