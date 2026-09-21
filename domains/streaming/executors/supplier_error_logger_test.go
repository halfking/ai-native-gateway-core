package executors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// 2026-09-05 审计闭环1/2：supplier_errors_hot 统一写入契约 + 脱敏验收。
//
// 事实源链路：CandidateFailureWriter.logFailure 是唯一写入函数；
// supplier_errors_hot 行携带完整诊断维度（request_id / attempt_seq /
// supplier / model / http_status / error_type / retryable / latency /
// stage）；所有自由文本（error_message、request_metadata 字符串值）必须
// 经 errorsx.SanitizeErrorText 脱敏后入库。

// capturingDB 按 SQL 片段捕获 Exec 参数（测试专用 candidateFailureDB）。
type capturingDB struct {
	mu           sync.Mutex
	calls        []capturedCall
	failSupplier bool
}

type capturedCall struct {
	SQL  string
	Args []any
}

func (c *capturingDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, capturedCall{SQL: sql, Args: args})
	if c.failSupplier && strings.Contains(sql, "supplier_errors_hot") {
		return pgconn.CommandTag{}, errors.New("partition missing")
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (c *capturingDB) callsFor(substr string) []capturedCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []capturedCall
	for _, call := range c.calls {
		if strings.Contains(call.SQL, substr) {
			out = append(out, call)
		}
	}
	return out
}

// supplierErrorArgs 是 persistSupplierError 的 INSERT 位置参数投影，
// 顺序对应 supplierErrorInsertSQL 的 $1..$19（$1 occurred_at 起，
// $19 request_metadata；列见 V371 迁移）。
type supplierErrorArgs struct {
	OccurredAt   any
	RequestID    string
	TraceID      string
	TenantID     string
	SessionID    string
	ProviderID   int
	Supplier     string
	CredentialID int64
	Model        string
	AttemptSeq   int
	ErrorType    string
	ErrorCode    string
	HTTPStatus   any
	ErrorMessage string
	Retryable    *bool
	Stage        string
	LatencyMs    any
	MetadataJSON string
}

func parseSupplierErrorArgs(args []any) (supplierErrorArgs, error) {
	// supplierErrorInsertSQL 有 18 个占位符（affected_users 是字面量 1）。
	if len(args) != 18 {
		return supplierErrorArgs{}, fmt.Errorf("supplier insert expects 18 args, got %d", len(args))
	}
	out := supplierErrorArgs{
		OccurredAt:   args[0],
		RequestID:    asString(args[1]),
		TraceID:      asString(args[2]),
		TenantID:     asString(args[3]),
		SessionID:    asString(args[4]),
		ProviderID:   asInt(args[5]),
		Supplier:     asString(args[6]),
		CredentialID: asInt64(args[7]),
		Model:        asString(args[8]),
		AttemptSeq:   asInt(args[9]),
		ErrorType:    asString(args[10]),
		ErrorCode:    asString(args[11]),
		HTTPStatus:   args[12],
		ErrorMessage: asString(args[13]),
		Stage:        asString(args[15]),
		LatencyMs:    args[16],
	}
	if b, ok := args[14].(*bool); ok && b != nil {
		v := *b
		out.Retryable = &v
	}
	if s, ok := args[17].(string); ok {
		out.MetadataJSON = s
	}
	return out, nil
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int32:
		return int(t)
	case int64:
		return int(t)
	case *int:
		if t == nil {
			return 0
		}
		return *t
	default:
		return 0
	}
}

func asInt64(v any) int64 {
	switch t := v.(type) {
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case int64:
		return t
	default:
		return 0
	}
}

// TestSupplierErrorWriteContract 锁定统一写入的行形状：维度齐全、
// 低基数列来自 context 注入、retryable 来自 recovery projection。
func TestSupplierErrorWriteContract(t *testing.T) {
	db := &capturingDB{}
	w := &CandidateFailureWriter{pool: db}
	sc := 429
	w.LogFailureWithKind("req-sup-1", "tenant-1", "sess-1", 42, 9, "glm-5.2", 3,
		&upstreampkg.Error{Kind: upstreampkg.KindRateLimit, Message: "too many requests", StatusCode: sc},
		errorsx.KindRateLimit, nil, intPtrSupplier(812),
		map[string]any{
			"supplier":      "zhipu",
			"failure_stage": "upstream",
			"error_code":    "1210",
			"trace_id":      "trace-xyz",
		})

	supplierCalls := db.callsFor("supplier_errors_hot")
	require.Len(t, supplierCalls, 1, "exactly one supplier_errors_hot insert per failure")
	captured, err := parseSupplierErrorArgs(supplierCalls[0].Args)
	require.NoError(t, err)

	require.NotNil(t, captured.Retryable, "retryable must be populated from recovery projection")
	assert.Equal(t, "req-sup-1", captured.RequestID)
	assert.Equal(t, "tenant-1", captured.TenantID)
	assert.Equal(t, "sess-1", captured.SessionID)
	assert.Equal(t, int64(42), captured.CredentialID)
	assert.Equal(t, 9, captured.ProviderID)
	assert.Equal(t, "zhipu", captured.Supplier, "supplier dimension from catalog code")
	assert.Equal(t, "glm-5.2", captured.Model)
	assert.Equal(t, 3, captured.AttemptSeq, "attempt_seq from AttemptNo")
	assert.Equal(t, "rate_limit", captured.ErrorType)
	assert.Equal(t, "1210", captured.ErrorCode)
	assert.Equal(t, 429, asInt(captured.HTTPStatus))
	// rate_limit 走 wait-recovery 而非通用透明重试：中央策略钉死
	// IsRetryable(rate_limit)=false，事实源列必须忠实投影该语义。
	assert.False(t, *captured.Retryable, "rate_limit is wait-recovery, not generic-retryable")
	assert.Equal(t, "upstream", captured.Stage)
	assert.Equal(t, 812, asInt(captured.LatencyMs))
	assert.Equal(t, "trace-xyz", captured.TraceID)
	assert.NotEmpty(t, captured.MetadataJSON)

	// 双写契约：旧读端表同请求仍有一行。
	assert.Len(t, db.callsFor("candidate_failure_logs_hot"), 1)
}

func intPtrSupplier(v int) *int { return &v }

// TestSupplierErrorRedaction 脱敏验收：密钥/Token/Authorization/query token
// 不得出现在 error_message 与 request_metadata 的任何字符串值里
// （审计要求：自动扫描覆盖错误消息与 URL query token）。
func TestSupplierErrorRedaction(t *testing.T) {
	db := &capturingDB{}
	w := &CandidateFailureWriter{pool: db}

	// 恶意上游：把密钥回显进错误消息与 body（真实供应商故障模式）。
	leaky := &upstreampkg.Error{
		Kind: upstreampkg.KindUpstreamDown,
		Message: `upstream 503: Authorization: Bearer sk-live-abcdef1234567890abcdef, ` +
			`api_key=super-secret-query-token, x-api-key: ak-raw-987654321`,
		Err:        errors.New("503"),
		StatusCode: 503,
		Body:       []byte(`{"error":"Service Unavailable","echo":"Authorization: Bearer sk-live-abcdef1234567890abcdef"}`),
	}

	w.LogFailureWithKind("req-redact-1", "tenant-1", "sess-1", 7, 9, "glm-5.2", 1,
		leaky, "", nil, nil,
		map[string]any{
			"supplier":         "zhipu",
			"failure_stage":    "connect",
			"stream_reason":    "Authorization: Bearer sk-live-abcdef1234567890abcdef leaked upstream",
			"preflight_reason": "api_key=super-secret-query-token rejected",
		})

	supplierCalls := db.callsFor("supplier_errors_hot")
	require.Len(t, supplierCalls, 1)
	captured, err := parseSupplierErrorArgs(supplierCalls[0].Args)
	require.NoError(t, err)

	// 消息正文里的密钥同样必须被旧表列脱敏（regression 双保险）。
	require.Len(t, db.callsFor("candidate_failure_logs_hot"), 1)

	secrets := []string{
		"sk-live-abcdef1234567890abcdef",
		"super-secret-query-token",
		"ak-raw-987654321",
	}
	for _, haystack := range []string{captured.ErrorMessage, captured.MetadataJSON} {
		for _, secret := range secrets {
			assert.NotContains(t, haystack, secret,
				"secret %q leaked into supplier_errors_hot free text", secret)
		}
	}
	// 错误类型本身不受脱敏影响（保持可聚合）。
	assert.Equal(t, "upstream_down", captured.ErrorType)
	// 结构化维度不携带自由文本。
	assert.Equal(t, "connect", captured.Stage)
}

// TestSupplierErrorMetadataIsJSON 可解析性门禁：metadata 列必须是合法 JSON
// 对象（趋势 API 直接 jsonb 取键）。
func TestSupplierErrorMetadataIsJSON(t *testing.T) {
	db := &capturingDB{}
	w := &CandidateFailureWriter{pool: db}
	w.LogFailure("req-json-1", "tenant-1", "sess-1", 1, 2, "m", 0,
		fmt.Errorf("boom"), nil, nil, map[string]any{"source": "unit", "attempt": 2})

	captured, err := parseSupplierErrorArgs(db.callsFor("supplier_errors_hot")[0].Args)
	require.NoError(t, err)
	require.NotEmpty(t, captured.MetadataJSON)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(captured.MetadataJSON), &decoded),
		"request_metadata must be valid JSONB string, got %q", captured.MetadataJSON)
	assert.Equal(t, "unit", decoded["source"])
}

// TestSupplierErrorLowCardinalityGuard 低基数维度防御：supplier/stage 列
// 拒绝含空白的自由文本（契约漂移时丢弃而不是污染维度列）。
func TestSupplierErrorLowCardinalityGuard(t *testing.T) {
	assert.Equal(t, "", lowCardinalityContextValue(map[string]any{"supplier": "not a code"}, "supplier"))
	assert.Equal(t, "zhipu", lowCardinalityContextValue(map[string]any{"supplier": "zhipu"}, "supplier"))
	assert.Equal(t, "", lowCardinalityContextValue(map[string]any{"supplier": strings.Repeat("x", 100)}, "supplier"))
	assert.Equal(t, "", lowCardinalityContextValue(nil, "supplier"))
}

// TestSupplierErrorInsertFailureIsBestEffort 写入失败不影响请求链路
// （warn 只记日志），且不会因 supplier INSERT 失败跳过旧表写入。
func TestSupplierErrorInsertFailureIsBestEffort(t *testing.T) {
	db := &capturingDB{failSupplier: true}
	w := &CandidateFailureWriter{pool: db}
	assert.NotPanics(t, func() {
		w.LogFailure("req-be-1", "tenant-1", "", 1, 2, "m", 0,
			fmt.Errorf("boom"), nil, nil, nil)
	})
	// 两条 INSERT 都尝试过：事实源失败不阻断旧表（读端迁移期双写）。
	assert.Len(t, db.callsFor("candidate_failure_logs_hot"), 1)
	assert.Len(t, db.callsFor("supplier_errors_hot"), 1)
}
