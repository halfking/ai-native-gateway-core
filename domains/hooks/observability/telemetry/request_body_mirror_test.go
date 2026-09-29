package telemetry

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// H3 请求侧镜像（2026-09-24 方案 §3-H3）单测：三件套换算与
// upsertRequestLogBodies 同源、空载荷跳过、门控/降级/PG 不可用下的投递行为。
// 全部走记录型 sink（互斥保护），不依赖 DB 断言执行序——用「Begin 失败镜像
// 仍投递」证明「镜像先于任何 PG 往返」这一不变量，比调用序桩更抗重构。

// bodyMirrorRecorder 记录型 sink。
type bodyMirrorRecorder struct {
	mu    sync.Mutex
	calls []bodyMirrorCall
}

type bodyMirrorCall struct {
	tenant    string
	requestID string
	direction string
	payload   string
	at        time.Time
}

func (r *bodyMirrorRecorder) record(tenantID, requestID, direction string, payload json.RawMessage, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, bodyMirrorCall{
		tenant: tenantID, requestID: requestID, direction: direction,
		payload: string(payload), at: at,
	})
}

func (r *bodyMirrorRecorder) snapshot() []bodyMirrorCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bodyMirrorCall{}, r.calls...)
}

// TestMirrorRequestBodiesMapsThreePieces：三件套方向映射与同源换算。
func TestMirrorRequestBodiesMapsThreePieces(t *testing.T) {
	requestBody := `{"model":"glm-5","messages":[]}`
	responseBody := `{"choices":[]}`
	entry := &RequestLogEntry{
		Op:              RequestLogUpdate,
		RequestID:       "req-mirror-1",
		ApplicationCode: strptr("app-alpha"),
		RequestBody:     &requestBody,
		ResponseBody:    &responseBody,
		OutboundBody:    json.RawMessage(`{"messages":[{"role":"user"}]}`),
	}
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	entry.EventAt = &at

	c := &Client{}
	var rec bodyMirrorRecorder
	c.SetBodyMirror(rec.record)
	c.mirrorRequestBodies(entry)

	calls := rec.snapshot()
	require.Len(t, calls, 3)
	require.Equal(t, "req", calls[0].direction)
	require.Equal(t, requestBody, calls[0].payload)
	require.Equal(t, "resp", calls[1].direction)
	require.Equal(t, responseBody, calls[1].payload)
	require.Equal(t, "out", calls[2].direction)
	require.Equal(t, `{"messages":[{"role":"user"}]}`, calls[2].payload)
	// 三件套同一 (tenant, requestID, at)；EventAt 盖章时优先采用。
	for _, call := range calls {
		require.Equal(t, "app-alpha", call.tenant)
		require.Equal(t, "req-mirror-1", call.requestID)
		require.Equal(t, at, call.at)
	}
}

// TestMirrorRequestBodiesSkipsEmptyAndNull：换算空值语义钉死——
// strPtrToJSON: nil→"null"、""/非法 JSON→"{}"；jsonOrNull: 空→"null"。全部跳过。
func TestMirrorRequestBodiesSkipsEmptyAndNull(t *testing.T) {
	empty := ""
	bad := "not-json{"
	entry := &RequestLogEntry{
		RequestID:       "req-mirror-2",
		ApplicationCode: strptr("app-alpha"),
		RequestBody:     &bad,   // 非法 JSON → "{}"，跳过
		ResponseBody:    &empty, // 空串 → "{}"，跳过
		OutboundBody:    nil,    // → "null"，跳过
	}

	c := &Client{}
	var rec bodyMirrorRecorder
	c.SetBodyMirror(rec.record)
	c.mirrorRequestBodies(entry)
	require.Empty(t, rec.snapshot())

	// 部分非空：只投递有内容的方向。
	ok := `{"k":"v"}`
	entry.RequestBody = &ok
	entry.OutboundBody = json.RawMessage(`[1,2]`)
	c.mirrorRequestBodies(entry)
	calls := rec.snapshot()
	require.Len(t, calls, 2)
	require.Equal(t, "req", calls[0].direction)
	require.Equal(t, "out", calls[1].direction)
}

// TestMirrorableBody：跳过矩阵锁死换算函数的空值收敛语义。
func TestMirrorableBody(t *testing.T) {
	require.False(t, mirrorableBody("null"))
	require.False(t, mirrorableBody("{}"))
	require.True(t, mirrorableBody(`{"a":1}`))
	require.True(t, mirrorableBody("[]"))
}

// TestMirrorRequestBodiesTenantFallback：tenant=application_code 空则回退
// entry.TenantID；双空整体跳过（镜像路径以 tenant 命名目录，空值必被
// validMirrorID 拒绝并误计失败指标——静默跳过是显式行为而非指标噪声）。
func TestMirrorRequestBodiesTenantFallback(t *testing.T) {
	entry := &RequestLogEntry{RequestID: "req-mirror-t"}
	entry.RequestBody = strptr(`{"a":1}`)

	c := &Client{}
	var rec bodyMirrorRecorder
	c.SetBodyMirror(rec.record)

	c.mirrorRequestBodies(entry) // 双空 → 跳过
	require.Empty(t, rec.snapshot())

	entry.TenantID = "tenant-beta"
	c.mirrorRequestBodies(entry) // 回退 TenantID
	calls := rec.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, "tenant-beta", calls[0].tenant)
}

// TestSetBodyMirrorNilIsNoOp：nil 注入与摘除均为 no-op（含 nil receiver 安全）。
func TestSetBodyMirrorNilIsNoOp(t *testing.T) {
	entry := &RequestLogEntry{RequestID: "req-mirror-3"}
	entry.RequestBody = strptr(`{"a":1}`)

	var nilClient *Client
	nilClient.SetBodyMirror(nil)         // nil receiver 守卫，不 panic
	nilClient.mirrorRequestBodies(entry) // bodyMirrorFn nil 守卫，不 panic

	c := &Client{}
	var rec bodyMirrorRecorder
	c.SetBodyMirror(rec.record)
	c.SetBodyMirror(nil) // 摘除
	c.mirrorRequestBodies(entry)
	require.Empty(t, rec.snapshot())
}

// TestMirrorRequestBodiesNoRequestID：缺 request_id 直接 no-op
//（镜像文件路径以 requestID 命名，validMirrorID 必拒）。
func TestMirrorRequestBodiesNoRequestID(t *testing.T) {
	entry := &RequestLogEntry{}
	entry.RequestBody = strptr(`{"a":1}`)
	c := &Client{}
	var rec bodyMirrorRecorder
	c.SetBodyMirror(rec.record)
	c.mirrorRequestBodies(entry)
	require.Empty(t, rec.snapshot())
}

// TestPersistRequestLogMirrorsBeforePGRoundtrip：H3 验收「PG 不可用时镜像仍
// 写入」——Begin 直接失败（PG down）时三件套镜像已投递，且持久化错误原样
// 返回（镜像绝不改变主链路结果）。
func TestPersistRequestLogMirrorsBeforePGRoundtrip(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()
	mockDB.ExpectBegin().WillReturnError(errors.New("pg down"))

	c := NewClientWithRequestLogDB(mockDB)
	t.Cleanup(c.Stop)
	var rec bodyMirrorRecorder
	c.SetBodyMirror(rec.record)

	entry := &RequestLogEntry{RequestID: "req-pgdown", TenantID: "tenant-gamma"}
	entry.ResponseBody = strptr(`{"choices":[]}`)
	require.Error(t, c.persistRequestLog(entry))
	calls := rec.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, "resp", calls[0].direction)
}

// TestPersistRequestLogMirrorFollowsStopWriteGate：S4 停写门控与镜像同门
//（storage.request_logs_write_enabled）——false 时镜像整体跳过；true 时照常
// 投递（PG down 即可证投递，无需完整落库编排）。
func TestPersistRequestLogMirrorFollowsStopWriteGate(t *testing.T) {
	entry := &RequestLogEntry{RequestID: "req-gate", TenantID: "tenant-gate"}
	entry.RequestBody = strptr(`{"a":1}`)

	withStopWriteGateSettings(t, false)
	c, rec := &Client{}, &bodyMirrorRecorder{}
	c.SetDegraded(true) // 无 fallback：persist 走 errNoTelemetryDB，不触 DB
	c.SetBodyMirror(rec.record)
	require.ErrorIs(t, c.persistRequestLog(entry), errNoTelemetryDB) // 门关：镜像已跳过
	require.Empty(t, rec.snapshot())

	withStopWriteGateSettings(t, true)
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()
	mockDB.ExpectBegin().WillReturnError(errors.New("pg down"))
	c2 := NewClientWithRequestLogDB(mockDB)
	t.Cleanup(c2.Stop)
	rec2 := &bodyMirrorRecorder{}
	c2.SetBodyMirror(rec2.record)
	_ = c2.persistRequestLog(entry)
	require.Len(t, rec2.snapshot(), 1)
}

// TestPersistRequestLogMirrorInDegradedMode：degraded（PG 不可用、fallback
// 接管）下镜像仍写入——镜像调用在 degraded 早退之前正是为这条 H3 验收。
func TestPersistRequestLogMirrorInDegradedMode(t *testing.T) {
	c := &Client{}
	c.SetDegraded(true) // 无 fallback writer → persist 返回 errNoTelemetryDB
	var rec bodyMirrorRecorder
	c.SetBodyMirror(rec.record)
	entry := &RequestLogEntry{RequestID: "req-degraded", TenantID: "tenant-delta"}
	entry.OutboundBody = json.RawMessage(`{"m":1}`)
	require.ErrorIs(t, c.persistRequestLog(entry), errNoTelemetryDB)
	calls := rec.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, "out", calls[0].direction)
}
