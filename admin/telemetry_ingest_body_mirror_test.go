package admin

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	filestore "github.com/kaixuan/llm-gateway-go/storage/file"
)

// F5（2026-10-01）：admin HTTP ingest（/api/telemetry/request-log）是
// request_logs_bodies_hot 的第三落库点，此前未接热区请求侧镜像。本文件钉桩
// 该接线的行为契约。
//
// 刻意**不**只测 mirrorRequestBodies 这个 helper：helper 存在不等于接上了。
// 主用例走真实落库路径 persistRequestLog，变异验证时把 persistRequestLog 里的
// t.mirrorRequestBodies(e) 删掉即会红——那才是「接线被摘掉」的形态。

// mirrorCapture 收集镜像投递，替代真实 RequestMirror（不依赖文件系统）。
type mirrorCapture struct{ calls []mirrorCall }

type mirrorCall struct {
	tenant    string
	requestID string
	direction string
	payload   string
}

func (m *mirrorCapture) fn() BodyMirrorFunc {
	return func(tenantID, requestID, direction string, payload json.RawMessage, at time.Time) {
		m.calls = append(m.calls, mirrorCall{
			tenant:    tenantID,
			requestID: requestID,
			direction: direction,
			payload:   string(payload),
		})
	}
}

func (m *mirrorCapture) directions() []string {
	out := make([]string, 0, len(m.calls))
	for _, c := range m.calls {
		out = append(out, c.direction)
	}
	return out
}

// newMirroringIngester 构造一个只关心镜像投递的 ingester：db 的 Begin 立即失败，
// 因此「镜像仍投递」同时证明了入口投递语义（PG 停机期间正文仍有灾备副本）。
//
// 显式把 S4 停写门钉在「开」：F5 镜像受 requestLogsWriteEnabled() 同键同门
// 约束（见 TestF5AdminIngestMirrorHonorsStopWriteGate），若本组用例依赖
// settings.Global 的环境默认，就会因同包其他用例的设置残留而时绿时红。
func newMirroringIngester(t *testing.T, cap *mirrorCapture) *telemetryIngester {
	t.Helper()
	withRequestLogsWriteSetting(t, true)
	ing := &telemetryIngester{db: failingBeginDB{}}
	fn := cap.fn()
	ing.bodyMirror.Store(&fn)
	return ing
}

// TestF5AdminIngestMirrorHonorsStopWriteGate 钉桩 S4 停写门同键同门。
//
// 这是 2026-10-01 审计抓到的真实缺陷：F5 首版把镜像投递放在 persistRequestLog
// 入口且**不接** storage.request_logs_write_enabled，而 telemetry client 侧
// 明确 `if requestLogsWriteEnabled() { c.mirrorRequestBodies(entry) }`。
// 后果：运维关停该键是为了止血 request_logs 家族的磁盘占用（F3 重复写、
// 4.3GB/24k 行量级），而 admin ingest 镜像绕过该门继续把**同一批正文**写进
// 热区 requests/ 子树——「停写却仍落盘」，两侧行为分裂。
//
// 判据打「门关时零投递」，而非打「门开时有投递」：后者在缺陷存在时也成立，
// 钉不住。
func TestF5AdminIngestMirrorHonorsStopWriteGate(t *testing.T) {
	t.Run("gate off 零镜像投递", func(t *testing.T) {
		withRequestLogsWriteSetting(t, false)
		cap := &mirrorCapture{}
		ing := &telemetryIngester{db: failingBeginDB{}}
		fn := cap.fn()
		ing.bodyMirror.Store(&fn)

		ing.persistRequestLog(context.Background(), &requestLogInput{
			RequestID:    "req-f5-gate-off",
			TenantID:     "acme",
			RequestBody:  strPtr(`{"messages":[{"role":"user","content":"hi"}]}`),
			ResponseBody: strPtr(`{"content":"hello"}`),
		})
		require.Empty(t, cap.calls, "S4 停写时镜像必须同步停：镜像写的是 request_logs_bodies_hot 的同一批正文")
	})

	t.Run("gate on 照常投递", func(t *testing.T) {
		withRequestLogsWriteSetting(t, true)
		cap := &mirrorCapture{}
		ing := &telemetryIngester{db: failingBeginDB{}}
		fn := cap.fn()
		ing.bodyMirror.Store(&fn)

		ing.persistRequestLog(context.Background(), &requestLogInput{
			RequestID:    "req-f5-gate-on",
			TenantID:     "acme",
			RequestBody:  strPtr(`{"messages":[{"role":"user","content":"hi"}]}`),
			ResponseBody: strPtr(`{"content":"hello"}`),
		})
		require.Len(t, cap.calls, 2)
	})
}

// TestRequestLogsWriteEnabledSingleKeyLit 钉桩 admin 侧的 S4 判定只有一个
// 来源（requestLogsWriteEnabled），且该来源**不含键字面量**。
//
// 背景：F5 缺陷的成因正是「同一语义键在两个调用点各自内联字面量」，日后单独
// 改动其一即产生分裂。
//
// 2026-10-02 收紧：原判据是「键字面量在 admin/telemetry.go 中恰好出现 1 次」——
// 它只约束了 admin 这一个包，而同样的字面量当时还散落在另外三个包
// （cmd/gateway、domains/hooks/observability/telemetry、internal/trace）。
// 现在四处全部改为调用 settings.RequestLogsWriteEnabled()，本文件里的计数
// 合法地变成 **0**，而「整个仓库只有一个拼法」这条性质由
// settings.TestRequestLogsWriteEnabledKeyIsSpelledOnce 承担。
//
// 两道门并存是刻意的：这道管「admin 确实走共享来源」，那道管「全仓没有第二个
// 拼法」。只留任何一道都会漏掉另一半——只留本道，生产代码里再多一个字面量它
// 照样绿；只留那道，admin 自己退回内联也不会被本包察觉。
func TestRequestLogsWriteEnabledSingleKeyLit(t *testing.T) {
	src, err := os.ReadFile("telemetry.go")
	require.NoError(t, err)
	require.Zero(t, strings.Count(string(src), `"storage.request_logs_write_enabled"`),
		"admin/telemetry.go 不得再内联 S4 键字面量，必须经 settings.RequestLogsWriteEnabled()")
	require.Contains(t, string(src), "settings.RequestLogsWriteEnabled()",
		"admin 的 S4 判定必须走共享来源，而不是自己读设置")
}

// failingBeginDB 是 Begin 即失败的 ingesterDB stub。
type failingBeginDB struct{}

func (failingBeginDB) Begin(context.Context) (pgx.Tx, error) { return nil, context.DeadlineExceeded }

func (failingBeginDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

// TestF5AdminIngestMirrorsBodiesOnRealPersistPath 是 F5 的主用例：走真实
// persistRequestLog，断言 req/resp 两件套被投递给镜像，且 tenant 口径等于
// PG 行的 tenant_id（= nonEmptyDefault(TenantID)）。
func TestF5AdminIngestMirrorsBodiesOnRealPersistPath(t *testing.T) {
	cap := &mirrorCapture{}
	ing := newMirroringIngester(t, cap)

	ing.persistRequestLog(context.Background(), &requestLogInput{
		RequestID:    "req-f5-001",
		TenantID:     "acme",
		RequestBody:  strPtr(`{"messages":[{"role":"user","content":"hi"}]}`),
		ResponseBody: strPtr(`{"content":"hello"}`),
	})

	require.Len(t, cap.calls, 2, "req/resp 两件套都应投递（本路径无 outbound 字段）")
	require.Equal(t, []string{"req", "resp"}, cap.directions())
	require.Equal(t, "acme", cap.calls[0].tenant)
	require.Equal(t, "req-f5-001", cap.calls[0].requestID)
	require.JSONEq(t, `{"messages":[{"role":"user","content":"hi"}]}`, cap.calls[0].payload)
	require.JSONEq(t, `{"content":"hello"}`, cap.calls[1].payload)
}

// TestF5AdminIngestMirrorSkipsEmptyAndNull 钉桩 F4 口径在 admin 侧的换算：
// nil → "null" 跳过、空串/非法 JSON → "{}" 跳过（无正文可对账，落镜像只是
// 噪声文件）；空 requestID 跳过；空 tenant 归一到 default 后**照常**投递
// （见该子用例注释——归一语义钉反会写出错误的"跳过"断言）。
func TestF5AdminIngestMirrorSkipsEmptyAndNull(t *testing.T) {
	t.Run("nil bodies 无投递", func(t *testing.T) {
		cap := &mirrorCapture{}
		ing := newMirroringIngester(t, cap)
		ing.persistRequestLog(context.Background(), &requestLogInput{
			RequestID: "req-f5-002",
			TenantID:  "acme",
		})
		require.Empty(t, cap.calls)
	})

	t.Run("空串与非法 JSON 收敛为 {} 并跳过", func(t *testing.T) {
		cap := &mirrorCapture{}
		ing := newMirroringIngester(t, cap)
		ing.persistRequestLog(context.Background(), &requestLogInput{
			RequestID:    "req-f5-003",
			TenantID:     "acme",
			RequestBody:  strPtr(""),
			ResponseBody: strPtr("not-json"),
		})
		require.Empty(t, cap.calls, "空串/非法 JSON 收敛为 {} 后无可对账正文")
	})

	t.Run("空 tenant 归一到 default 且仍投递", func(t *testing.T) {
		cap := &mirrorCapture{}
		ing := newMirroringIngester(t, cap)
		ing.persistRequestLog(context.Background(), &requestLogInput{
			RequestID:    "req-f5-004",
			RequestBody:  strPtr(`{"a":1}`),
			ResponseBody: strPtr(`{"b":2}`),
		})
		// nonEmptyDefault 对空/纯空白返回 "default"（不是空串），而 "default"
		// 同时是合法镜像目录段与 PG 行的 tenant_id —— 两侧仍然对齐，所以这里
		// 必须**投递**而不是跳过。写成"跳过"会把归一语义钉反。
		require.Len(t, cap.calls, 2)
		require.Equal(t, "default", cap.calls[0].tenant)
		require.Equal(t, "default", cap.calls[1].tenant)
	})

	t.Run("纯空白 tenant 同样归一到 default", func(t *testing.T) {
		cap := &mirrorCapture{}
		ing := newMirroringIngester(t, cap)
		ing.persistRequestLog(context.Background(), &requestLogInput{
			RequestID:   "req-f5-004b",
			TenantID:    "   ",
			RequestBody: strPtr(`{"a":1}`),
		})
		require.Len(t, cap.calls, 1)
		require.Equal(t, "default", cap.calls[0].tenant)
	})

	t.Run("空 requestID 跳过", func(t *testing.T) {
		cap := &mirrorCapture{}
		ing := newMirroringIngester(t, cap)
		ing.persistRequestLog(context.Background(), &requestLogInput{
			TenantID:    "acme",
			RequestBody: strPtr(`{"a":1}`),
		})
		require.Empty(t, cap.calls)
	})
}

// TestF5AdminIngestMirrorNoOpWhenNotWired 钉桩未注入镜像时行为与 F5 之前完全
// 一致（纯落库、不 panic）——F5 是增量接线，不得改变未装配环境的语义。
func TestF5AdminIngestMirrorNoOpWhenNotWired(t *testing.T) {
	ing := &telemetryIngester{db: failingBeginDB{}}
	require.NotPanics(t, func() {
		ing.persistRequestLog(context.Background(), &requestLogInput{
			RequestID:    "req-f5-005",
			TenantID:     "acme",
			RequestBody:  strPtr(`{"a":1}`),
			ResponseBody: strPtr(`{"b":2}`),
		})
	})
	// nil ingester 与未注入闭包都不得 panic（bodyMirrorFn 自身 nil-safe）。
	var nilIng *telemetryIngester
	require.Nil(t, nilIng.bodyMirrorFn())
	require.Nil(t, ing.bodyMirrorFn())
}

// TestF5ConvertBodyPayloadIsSharedContract 钉桩镜像换算规则是单一事实源。
//
// 背景：换算规则若在镜像侧与落库侧各写一份，某一侧改动会让「镜像 gunzip
// 内容 == PG 列内容」的对账承诺静默失配（E2E 演练 F4 口径的根因面）。本用例
// 把规则钉在 storage/file 上，telemetry 落库侧的 strPtrToJSON 与 admin 侧
// 都经它转调。
func TestF5ConvertBodyPayloadIsSharedContract(t *testing.T) {
	cases := []struct {
		name string
		in   *string
		want string
	}{
		{"nil → null", nil, "null"},
		{"空串 → {}", strPtr(""), "{}"},
		{"非法 JSON → {}", strPtr("{oops"), "{}"},
		{"合法对象原样", strPtr(`{"a":1}`), `{"a":1}`},
		{"合法数组原样", strPtr(`[1,2]`), `[1,2]`},
		{"JSON null 字面量原样", strPtr("null"), "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, filestore.ConvertBodyPayload(tc.in))
		})
	}

	// 可镜像判定：null / {} 跳过，其余保留。
	require.False(t, filestore.MirrorablePayload("null"))
	require.False(t, filestore.MirrorablePayload("{}"))
	require.True(t, filestore.MirrorablePayload(`{"a":1}`))
	require.True(t, filestore.MirrorablePayload(`[]`))
}
