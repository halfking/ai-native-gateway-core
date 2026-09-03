// Package admin - turn_digest_integration_test.go
//
// 2026-09-02 turn-digest 集成测试（替代此前的 skeleton）。
//
// 覆盖 session_turns_v2 读路径的 HTTP 层行为（serveSessionTurnDetailDB /
// serveSessionTurnsListDB 接口缝版本），DB 层以 pgxmock 注入：
//
//	1. HappyPath            — 完整 prompt/response → fallback 重建 digest
//	2. UniqueKeyCollision   — 5 元组重复行被 LIMIT 1 折叠
//	3. TenantIsolation      — tenant_admin 不能用 ?tenant= 越租户；super_admin 可以
//	4. NullBodiesDowngrade  — request/response 为 null → digest 仍产出且无 "<nil>" 泄漏
//	5. QueryErrorReturns500 — DB 错误 → 500 + 结构化错误
//	6. PersistedHit         — digest 列有合法 envelope → 直接返回，不触发 fallback
//	7. FallbackRebuild      — digest 列为 NULL → fallback 重建 + counter +1
//	8. ListEndpoint         — 列表端点列序 + persisted digest 透传
//
// 列序契约：detail 端点 29 列 / list 端点 24 列，必须与
// serveSessionTurnDetailDB / serveSessionTurnsListDB 的 Scan 一一对应。

package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domains/sessiondigest"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixtures
// ─────────────────────────────────────────────────────────────────────────────

// digestDetailColumns 是 serveSessionTurnDetailDB SELECT 投影的列序（29 列）。
var digestDetailColumns = []string{
	"turn_no", "request_id", "ts",
	"submit_mode", "compression_applied", "compression_strategy",
	"compression_meta", "compression_tokens_saved",
	"injection_verdict", "output_verdict",
	"model", "provider",
	"prompt_tokens", "completion_tokens", "cache_read_tokens", "cache_write_tokens",
	"cost_usd", "latency_ms", "status_code", "success", "error_kind",
	"source_kind", "quality", "digest",
	"request_delta", "response_delta", "outbound_body",
	"request_attachments", "response_attachments",
}

// seedTurnDetail 构造 serveSessionTurnDetailDB 期望的单行。可空列以 nil /
// 指针值传入（pgx Scan 的 database/sql 兼容规则），与既有
// makeSessionTurnMockRows（session_detail_v2_test.go）同款写法。
func seedTurnDetail(prompt, response string, persistedDigest []byte) *pgxmock.Rows {
	now := time.Now().UTC()
	promptTokens, completionTokens := 100, 50
	cacheRead := 0
	costUSD := 0.01
	latencyMs := 1500
	statusCode := 200
	success := true
	model, provider := "test-model", "test-provider"
	return pgxmock.NewRows(digestDetailColumns).AddRow(
		1, "req-1", now,
		"chat", false, nil,
		nil, nil,
		"pass", "pass",
		&model, &provider,
		&promptTokens, &completionTokens, &cacheRead, nil,
		&costUSD, &latencyMs, &statusCode, &success, nil,
		"gateway", "good", persistedDigest,
		[]byte(`{"messages":[{"role":"user","content":"`+prompt+`"}]}`),
		[]byte(`{"choices":[{"message":{"role":"assistant","content":"`+response+`"}}]}`),
		nil, []byte(`[]`), []byte(`[]`),
	)
}

// seedTurnDetailNullBodies 与 seedTurnDetail 相同但 request/response_delta 为
// JSON null（LEFT JOIN miss 的常见形态）。
func seedTurnDetailNullBodies(persistedDigest []byte) *pgxmock.Rows {
	now := time.Now().UTC()
	promptTokens := 100
	latencyMs := 1500
	model, provider := "test-model", "test-provider"
	return pgxmock.NewRows(digestDetailColumns).AddRow(
		1, "req-1", now,
		"chat", false, nil,
		nil, nil,
		"pass", "pass",
		&model, &provider,
		&promptTokens, nil, nil, nil,
		nil, &latencyMs, nil, nil, nil,
		"gateway", "good", persistedDigest,
		[]byte("null"), []byte("null"),
		nil, []byte(`[]`), []byte(`[]`),
	)
}

// marshalPersistedDigest 构造一条合法的 sessiondigest envelope。
func marshalPersistedDigest(t *testing.T, userInput, assistantOutput string, latencyMs int) []byte {
	t.Helper()
	raw, err := sessiondigest.Marshal(&sessiondigest.Envelope{
		SchemaVersion:    sessiondigest.SchemaVersion,
		AlgorithmVersion: sessiondigest.AlgorithmVersion,
		GeneratedAt:      time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Source:           sessiondigest.Source,
		Payload: sessiondigest.Digest{
			UserInput:       userInput,
			AssistantOutput: assistantOutput,
			Metrics:         sessiondigest.Metrics{TokensUsed: 100, Cost: 0.01, LatencyMs: latencyMs},
			Events:          []sessiondigest.Event{{Type: "info", Category: "test", Message: "persisted"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal persisted digest: %v", err)
	}
	return raw
}

// newDigestMockPool 返回已关闭清理的 pgxmock 池。
func newDigestMockPool(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock
}

// newDigestAuthRequest 构造带 AuthContext 的请求（绕过 JWT 签名，与
// session_detail_v2_test.go 的 newSessionDetailAuthRequest 同款）。
func newDigestAuthRequest(method, target, role, tenant string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	return SetAuthContext(r, &AuthContext{TenantID: tenant, Role: role, IsJWT: true})
}

// digestDetailBody 是 turnDetailV2Response 的断言投影。
type digestDetailBody struct {
	Digest *TurnDigest `json:"digest"`
	Error  struct {
		Detail string `json:"detail"`
	} `json:"error"`
}

// callTurnDetail 以 mock db 调用 serveSessionTurnDetailDB 并解析响应。
func callTurnDetail(t *testing.T, mock pgxmock.PgxPoolIface, req *http.Request) (int, digestDetailBody) {
	t.Helper()
	rr := httptest.NewRecorder()
	serveSessionTurnDetailDB(mock, rr, req, "gw_abc", "1")
	var body digestDetailBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body %q: %v", rr.Body.String(), err)
	}
	return rr.Code, body
}

// assertNoNilLeak 断言 digest 序列化后不含 "<nil>" 字面量（fmt %v 拼接
// 遗漏 nil 检查时的典型回归形态）。
func assertNoNilLeak(t *testing.T, d *TurnDigest) {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal digest: %v", err)
	}
	if strings.Contains(string(b), "<nil>") {
		t.Fatalf("digest json leaked <nil>: %s", b)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. Happy path
// ─────────────────────────────────────────────────────────────────────────────

func TestTurnDigest_HappyPath(t *testing.T) {
	mock := newDigestMockPool(t)
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("gw_abc", "tenant-a", 1).
		WillReturnRows(seedTurnDetail("hi there", "hello back", nil))

	code, body := callTurnDetail(t, mock, newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns/1", "tenant_admin", "tenant-a"))

	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %+v", code, body)
	}
	if body.Digest == nil {
		t.Fatal("digest is nil")
	}
	if body.Digest.UserInput != "hi there" {
		t.Errorf("user_input = %q, want %q", body.Digest.UserInput, "hi there")
	}
	if body.Digest.AssistantOutput != "hello back" {
		t.Errorf("assistant_output = %q, want %q", body.Digest.AssistantOutput, "hello back")
	}
	if body.Digest.Metrics.LatencyMs != 1500 {
		t.Errorf("latency_ms = %d, want 1500", body.Digest.Metrics.LatencyMs)
	}
	if body.Digest.Metrics.TokensUsed != 150 {
		t.Errorf("tokens_used = %d, want 150 (prompt 100 + completion 50)", body.Digest.Metrics.TokensUsed)
	}
	assertNoNilLeak(t, body.Digest)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. Unique key collision — LIMIT 1 折叠语义
// ─────────────────────────────────────────────────────────────────────────────

// TestTurnDigest_UniqueKeyCollision 验证 5 元组 (tenant_id, session_id,
// turn_no, request_id, partition_date) 唯一键在 DB 层（DDL 见
// deploy/sql/schemas/baseline/01-schema.sql:21096）阻止重复行；即便 mock 层
// 模拟出重复行，detail 端点也必须经 LIMIT 1 折叠为单行、单 digest。
func TestTurnDigest_UniqueKeyCollision(t *testing.T) {
	mock := newDigestMockPool(t)
	now := time.Now().UTC()
	model, provider := "m", "p"
	row := func() []any {
		return []any{
			1, "req-1", now, "chat", false, nil, nil, nil, "pass", "pass",
			&model, &provider, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			"gateway", "good", nil,
			[]byte(`{"messages":[{"role":"user","content":"row"}]}`),
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"row"}}]}`),
			nil, []byte(`[]`), []byte(`[]`),
		}
	}
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("gw_abc", "tenant-a", 1).
		WillReturnRows(pgxmock.NewRows(digestDetailColumns).AddRow(row()...).AddRow(row()...))

	code, body := callTurnDetail(t, mock, newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns/1", "tenant_admin", "tenant-a"))

	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %+v", code, body)
	}
	if body.Digest == nil || body.Digest.UserInput != "row" {
		t.Fatalf("duplicate rows not folded to a single digest: %+v", body.Digest)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. Tenant isolation
// ─────────────────────────────────────────────────────────────────────────────

// TestTurnDigest_TenantIsolation_PinsTenantAdminToAuthTenant 验证
// tenant_admin 不能通过 ?tenant= 越租户（tenantFromQueryOrContext,
// session_turns_v2.go:764 只有 super_admin/admin_key 可显式选租户）。
// 断言机制：pgxmock WithArgs 精确匹配 ("gw_abc", "tenant-a", 1)，若 handler
// 越权用了 tenant-b，期望不匹配 → 查询报错 → 500 → 测试失败。
func TestTurnDigest_TenantIsolation_PinsTenantAdminToAuthTenant(t *testing.T) {
	mock := newDigestMockPool(t)
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("gw_abc", "tenant-a", 1).
		WillReturnRows(seedTurnDetail("tenant-a secret", "reply", nil))

	code, body := callTurnDetail(t, mock,
		newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns/1?tenant=tenant-b", "tenant_admin", "tenant-a"))

	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %+v", code, body)
	}
	if body.Digest == nil || body.Digest.UserInput != "tenant-a secret" {
		t.Fatalf("cross-tenant leak or wrong digest: %+v", body.Digest)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestTurnDigest_TenantIsolation_SuperAdminMaySelectTenant 验证 super_admin
// 的显式租户选择仍被允许（运维跨租户排障路径）。
func TestTurnDigest_TenantIsolation_SuperAdminMaySelectTenant(t *testing.T) {
	mock := newDigestMockPool(t)
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("gw_abc", "tenant-b", 1).
		WillReturnRows(seedTurnDetail("tenant-b secret", "reply", nil))

	code, body := callTurnDetail(t, mock,
		newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns/1?tenant=tenant-b", "super_admin", "tenant-a"))

	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %+v", code, body)
	}
	if body.Digest == nil || body.Digest.UserInput != "tenant-b secret" {
		t.Fatalf("super_admin tenant selection broken: %+v", body.Digest)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. NULL bodies downgrade
// ─────────────────────────────────────────────────────────────────────────────

// TestTurnDigest_NullBodiesDowngrade 验证 request/response_delta 为 null 时
// digest 仍产出（metrics 兜底触发 hasMetricData）、文本为空、无 "<nil>" 泄漏。
func TestTurnDigest_NullBodiesDowngrade(t *testing.T) {
	mock := newDigestMockPool(t)
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("gw_abc", "tenant-a", 1).
		WillReturnRows(seedTurnDetailNullBodies(nil))

	code, body := callTurnDetail(t, mock, newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns/1", "tenant_admin", "tenant-a"))

	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %+v", code, body)
	}
	if body.Digest == nil {
		t.Fatal("digest is nil; metrics should keep it alive via hasMetricData")
	}
	if body.Digest.UserInput != "" || body.Digest.AssistantOutput != "" {
		t.Errorf("null bodies should downgrade to empty text: user=%q assistant=%q",
			body.Digest.UserInput, body.Digest.AssistantOutput)
	}
	assertNoNilLeak(t, body.Digest)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. DB error → 500
// ─────────────────────────────────────────────────────────────────────────────

func TestTurnDigest_QueryErrorReturns500(t *testing.T) {
	mock := newDigestMockPool(t)
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("gw_abc", "tenant-a", 1).
		WillReturnError(errors.New("conn closed"))

	code, body := callTurnDetail(t, mock, newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns/1", "tenant_admin", "tenant-a"))

	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}
	if body.Error.Detail != "query turn failed" {
		t.Errorf("error.detail = %q, want %q", body.Error.Detail, "query turn failed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. Persisted digest hit（"缓存命中"路径：digest 列存在合法 envelope）
// ─────────────────────────────────────────────────────────────────────────────

// TestTurnDigest_PersistedHit 验证 digest 列的持久化 envelope 优先返回，
// 不触发 on-the-fly 重建，且 fallback counter 不增长。
func TestTurnDigest_PersistedHit(t *testing.T) {
	mock := newDigestMockPool(t)
	persisted := marshalPersistedDigest(t, "persisted user", "persisted assistant", 42)
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("gw_abc", "tenant-a", 1).
		WillReturnRows(seedTurnDetail("hi there", "hello back", persisted))

	beforeMissing := readFallbackCount(t, digestFallbackMissing)
	beforeMalformed := readFallbackCount(t, digestFallbackMalformed)

	code, body := callTurnDetail(t, mock, newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns/1", "tenant_admin", "tenant-a"))

	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %+v", code, body)
	}
	if body.Digest == nil {
		t.Fatal("digest is nil")
	}
	if body.Digest.UserInput != "persisted user" || body.Digest.AssistantOutput != "persisted assistant" {
		t.Errorf("persisted payload not returned: %+v", body.Digest)
	}
	if body.Digest.Metrics.LatencyMs != 42 {
		t.Errorf("latency_ms = %d, want 42", body.Digest.Metrics.LatencyMs)
	}
	if len(body.Digest.Events) == 0 || body.Digest.Events[0].Message != "persisted" {
		t.Errorf("persisted events not returned: %+v", body.Digest.Events)
	}
	if got := readFallbackCount(t, digestFallbackMissing); got != beforeMissing {
		t.Errorf("missing-fallback counter incremented on persisted hit: %d -> %d", beforeMissing, got)
	}
	if got := readFallbackCount(t, digestFallbackMalformed); got != beforeMalformed {
		t.Errorf("malformed-fallback counter incremented on persisted hit: %d -> %d", beforeMalformed, got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 7. Fallback rebuild（"缓存未命中"路径：digest 列为 NULL）
// ─────────────────────────────────────────────────────────────────────────────

// TestTurnDigest_FallbackRebuild 验证 digest 列为 NULL（migration 456 之前的
// 旧行）时走 on-the-fly 重建，reason="missing" counter +1。
func TestTurnDigest_FallbackRebuild(t *testing.T) {
	mock := newDigestMockPool(t)
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("gw_abc", "tenant-a", 1).
		WillReturnRows(seedTurnDetail("rebuilt user", "rebuilt assistant", nil))

	before := readFallbackCount(t, digestFallbackMissing)

	code, body := callTurnDetail(t, mock, newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns/1", "tenant_admin", "tenant-a"))

	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %+v", code, body)
	}
	if body.Digest == nil || body.Digest.UserInput != "rebuilt user" {
		t.Fatalf("fallback rebuild broken: %+v", body.Digest)
	}
	if after := readFallbackCount(t, digestFallbackMissing); after != before+1 {
		t.Fatalf("missing-fallback counter: before=%d after=%d (expected +1)", before, after)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 8. List endpoint — 列序 + persisted digest 透传
// ─────────────────────────────────────────────────────────────────────────────

// TestTurnDigest_ListEndpoint 验证 serveSessionTurnsListDB 的 24 列投影、
// persisted digest 在列表项中透传、分页字段形状。
func TestTurnDigest_ListEndpoint(t *testing.T) {
	mock := newDigestMockPool(t)
	now := time.Now().UTC()
	persisted := marshalPersistedDigest(t, "persisted user", "persisted assistant", 42)
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("tenant-a", "gw_abc", int(^uint(0)>>1), 51).
		WillReturnRows(pgxmock.NewRows([]string{
			"turn_no", "ts", "title", "summary",
			"prompt_tokens", "completion_tokens", "cost_usd",
			"model", "provider", "status_code",
			"submit_mode", "injection_verdict", "output_verdict", "attachment_count",
			"request_id", "cache_read_tokens", "latency_ms", "success",
			"error_kind", "compression_applied", "compression_tokens_saved", "digest",
			"request_delta", "response_delta",
		}).AddRow(
			3, now, "", "", 100, 50, 0.01,
			"test-model", "test-provider", 200,
			"chat", "pass", "pass", 0,
			"req-1", 0, 1500, true,
			nil, false, nil, persisted,
			[]byte(`{"messages":[{"role":"user","content":"ignored"}]}`),
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"ignored"}}]}`),
		))

	req := newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns", "tenant_admin", "tenant-a")
	rr := httptest.NewRecorder()
	serveSessionTurnsListDB(mock, "test-secret", rr, req, "gw_abc")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var listBody struct {
		Turns     []TurnListItem `json:"turns"`
		HasMore   bool           `json:"has_more"`
		NextCur   string         `json:"next_cursor"`
		SessionID string         `json:"session_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list body: %v", err)
	}
	if len(listBody.Turns) != 1 {
		t.Fatalf("turns len = %d, want 1", len(listBody.Turns))
	}
	it := listBody.Turns[0]
	if it.Digest == nil || it.Digest.UserInput != "persisted user" {
		t.Fatalf("list item persisted digest missing: %+v", it.Digest)
	}
	if it.RequestTokens != 100 || it.ResponseTokens != 50 {
		t.Errorf("token columns mis-ordered: request=%d response=%d", it.RequestTokens, it.ResponseTokens)
	}
	if listBody.HasMore {
		t.Error("has_more should be false for 1 row < limit")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
