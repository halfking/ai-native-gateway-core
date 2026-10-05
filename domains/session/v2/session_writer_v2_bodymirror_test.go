package v2

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/settings"
	filestore "github.com/kaixuan/llm-gateway-go/storage/file"
)

// errBodiesBoom 是 bodies INSERT 失败用例的注入错误。
var errBodiesBoom = errors.New("bodies insert boom")

// errCommitBoom 是 commit 失败用例的注入错误。
var errCommitBoom = errors.New("commit boom")

// H3 请求侧镜像（2026-09-24 方案 §3-H3）接线点 1 单测：SessionWriterV2 的
// BodyMirrorFunc seam 在 turn bodies / final_full 写入成功后 fire-and-forget
// 投递三件套；nil seam 全链路 no-op。复用 session_writer_tx_test.go 的
// pgxmock 编排基建（newMockedSessionWriter / expect* 助手 / sampleRequest）。

// mirrorRecorder 记录型镜像 sink（Write goroutine 内同步调用，互斥兜底）。
type mirrorRecorder struct {
	mu    sync.Mutex
	calls []mirrorCall
}

type mirrorCall struct {
	tenant    string
	requestID string
	direction string
	payload   string
	at        time.Time
}

func (r *mirrorRecorder) record(tenantID, requestID, direction string, payload json.RawMessage, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, mirrorCall{
		tenant: tenantID, requestID: requestID, direction: direction,
		payload: string(payload), at: at,
	})
}

func (r *mirrorRecorder) snapshot() []mirrorCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mirrorCall{}, r.calls...)
}

// expectTurnBodiesHappyPath 复刻 tx 测试的 happy path 编排（不带 digest 位置
// 钉死）：Begin → tenant set_config → 双锁 → 前置探针 → turn_no → turn INSERT
// → bodies INSERT → outbox → Commit。
func expectTurnBodiesHappyPath(mock pgxmock.PgxPoolIface, req *ProcessedRequest) {
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app\\.current_tenant', \\$1, true\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	expectSessionLock(mock)
	expectListAllBodiesEmpty(mock)
	expectRequestLock(mock)
	mock.ExpectQuery("COALESCE\\(MAX\\(turn_no\\), 0\\) \\+ 1").
		WithArgs(req.TenantID, req.SessionID).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(1))
	mock.ExpectExec("INSERT INTO public.session_turns_hot").
		WithArgs(anyArgs(99)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO public.session_bodies").
		WithArgs(anyArgs(12)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectOutboxEnqueue(mock)
	mock.ExpectCommit()
}

// TestSessionWriterMirror_TurnBodies：turn bodies 写入成功后镜像 req/resp 两
// 方向；outbound 为空（sampleRequest 未设 OutboundBody）→ safeJSONMarshal(nil)
// 产 "null" → mirrorableTurnPayload 跳过，不得投递 "out"。
func TestSessionWriterMirror_TurnBodies(t *testing.T) {
	w, mock := newMockedSessionWriter(t)
	req := sampleRequest()
	rec := &mirrorRecorder{}
	w.SetBodyMirror(rec.record)

	expectTurnBodiesHappyPath(mock, req)
	require.NoError(t, w.Write(context.Background(), req))
	require.NoError(t, w.Stop(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())

	calls := rec.snapshot()
	require.Len(t, calls, 2, "req/resp 两方向；out 载荷为 null 跳过")
	require.Equal(t, "req", calls[0].direction)
	require.Contains(t, calls[0].payload, "hi", "requestDelta 与落库同源")
	require.Equal(t, "resp", calls[1].direction)
	require.Contains(t, calls[1].payload, "hello")
	for _, c := range calls {
		require.Equal(t, req.TenantID, c.tenant)
		require.Equal(t, req.RequestID, c.requestID)
		require.Equal(t, req.Timestamp, c.at)
	}
}

// TestSessionWriterMirror_NilSeamNoOp：未注入 seam（离线 backfill 工具 /
// lite / 热区关闭形态）时主链路照常成功、零镜像行为——nil 检查零开销路径。
func TestSessionWriterMirror_NilSeamNoOp(t *testing.T) {
	w, mock := newMockedSessionWriter(t)
	req := sampleRequest()

	expectTurnBodiesHappyPath(mock, req)
	require.NoError(t, w.Write(context.Background(), req))
	require.NoError(t, w.Stop(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSessionWriterMirror_WriteFailsNoMirror：turn bodies 写失败（bodies INSERT
// 报错）时主链路返回错误且**零镜像投递**——镜像只在事务提交成功后激发。
func TestSessionWriterMirror_WriteFailsNoMirror(t *testing.T) {
	w, mock := newMockedSessionWriter(t)
	req := sampleRequest()
	rec := &mirrorRecorder{}
	w.SetBodyMirror(rec.record)

	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app\\.current_tenant', \\$1, true\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	expectSessionLock(mock)
	expectListAllBodiesEmpty(mock)
	expectRequestLock(mock)
	mock.ExpectQuery("COALESCE\\(MAX\\(turn_no\\), 0\\) \\+ 1").
		WithArgs(req.TenantID, req.SessionID).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(1))
	mock.ExpectExec("INSERT INTO public.session_turns_hot").
		WithArgs(anyArgs(99)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO public.session_bodies").
		WithArgs(anyArgs(12)...).
		WillReturnError(errBodiesBoom)
	mock.ExpectRollback()

	require.Error(t, w.Write(context.Background(), req))
	require.NoError(t, w.Stop(context.Background()))
	require.Empty(t, rec.snapshot(), "主链路失败不得投递镜像")
}

// TestSessionWriterMirror_FinalFull：S1b 灰度开启时 final_full 行写入成功后
// 投递 "out" 镜像，requestID='final_full:<session>' 与 PG 行口径一致；
// per-turn "out" 因停写门（bodiesRec.OutboundBody 置 nil）自然缺席。
func TestSessionWriterMirror_FinalFull(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &finalFullFakeKV{on: true})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	for _, spec := range settings.StorageSpecs() {
		require.NoError(t, registry.RegisterSpec(spec))
	}
	settings.Global = registry

	w, mock := newMockedSessionWriter(t)
	req := sampleRequest()
	req.OutboundBody = []Message{{Role: "assistant", Content: "hello"}}
	rec := &mirrorRecorder{}
	w.SetBodyMirror(rec.record)

	// pgxmock 严格按注册顺序消费期望：final_full INSERT 在代码里位于 per-turn
	// bodies INSERT 之后、outbox enqueue 之前，故期望顺序为 turn → bodies →
	// final_full → outbox → commit（不能复用先注册 outbox 的 happy-path 助手）。
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app\\.current_tenant', \\$1, true\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	expectSessionLock(mock)
	expectListAllBodiesEmpty(mock)
	expectRequestLock(mock)
	mock.ExpectQuery("COALESCE\\(MAX\\(turn_no\\), 0\\) \\+ 1").
		WithArgs(req.TenantID, req.SessionID).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(1))
	mock.ExpectExec("INSERT INTO public.session_turns_hot").
		WithArgs(anyArgs(99)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO public.session_bodies").
		WithArgs(anyArgs(12)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// final_full INSERT（7 参：session_id/$2 tenant/$3 final_full:id/ts/outbound/kind/partition_date）。
	mock.ExpectExec("INSERT INTO public\\.session_bodies_hot").
		WithArgs(anyArgs(7)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectOutboxEnqueue(mock)
	mock.ExpectCommit()

	require.NoError(t, w.Write(context.Background(), req))
	require.NoError(t, w.Stop(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())

	calls := rec.snapshot()
	require.Len(t, calls, 3, "req/resp（per-turn）+ out（final_full）")
	require.Equal(t, "out", calls[2].direction)
	require.Equal(t, "final_full:"+req.SessionID, calls[2].requestID)
	require.Contains(t, calls[2].payload, "hello")
	require.Equal(t, req.TenantID, calls[2].tenant)
	require.Equal(t, req.Timestamp, calls[2].at)
}

// TestSessionWriterMirror_CommitFailsNoMirror（2026-09-30 审计 F-A 钉死）：
// turn/bodies/outbox 全部写入成功但 tx.Commit 失败回滚 → PG 无 bodies 行，
// 镜像必须零投递（镜像调用在 commit 之后，孤儿镜像根修）。
func TestSessionWriterMirror_CommitFailsNoMirror(t *testing.T) {
	w, mock := newMockedSessionWriter(t)
	req := sampleRequest()
	rec := &mirrorRecorder{}
	w.SetBodyMirror(rec.record)

	// 内联编排（不能复用 happy-path 助手——其末尾的 ExpectCommit 会先被消费，
	// 导致注入的 commit 失败期望落在第二个、永远不被触达）。
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app\\.current_tenant', \\$1, true\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	expectSessionLock(mock)
	expectListAllBodiesEmpty(mock)
	expectRequestLock(mock)
	mock.ExpectQuery("COALESCE\\(MAX\\(turn_no\\), 0\\) \\+ 1").
		WithArgs(req.TenantID, req.SessionID).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(1))
	mock.ExpectExec("INSERT INTO public.session_turns_hot").
		WithArgs(anyArgs(99)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO public.session_bodies").
		WithArgs(anyArgs(12)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectOutboxEnqueue(mock)
	mock.ExpectCommit().WillReturnError(errCommitBoom)

	require.Error(t, w.Write(context.Background(), req))
	require.NoError(t, w.Stop(context.Background()))
	require.Empty(t, rec.snapshot(), "commit 失败回滚不得投递镜像（孤儿镜像根修）")
}

// TestSessionWriterMirror_DiskPathEndToEnd（2026-09-30 审计 F-B 补齐）：
// 真 RequestMirror 注入（装配闭包同款适配），断言三件套**磁盘路径**与 gunzip
// 内容——requests/{tenant}/{date}/{requestID}.{req|resp|out}.json.gz，
// 与 PG 行内容同源。Close() 排空异步队列后断言，无竞态。
func TestSessionWriterMirror_DiskPathEndToEnd(t *testing.T) {
	w, mock := newMockedSessionWriter(t)
	req := sampleRequest()
	req.OutboundBody = []Message{{Role: "assistant", Content: "hello"}}
	mirror := filestore.NewRequestMirror(t.TempDir(), 2)
	w.SetBodyMirror(func(tenantID, requestID, direction string, payload json.RawMessage, at time.Time) {
		mirror.MirrorAsync(tenantID, requestID, filestore.RequestDirection(direction), payload, at)
	})

	expectTurnBodiesHappyPath(mock, req)
	require.NoError(t, w.Write(context.Background(), req))
	require.NoError(t, w.Stop(context.Background()))
	require.NoError(t, mirror.Close()) // 排空异步写队列，之后断言无竞态
	require.NoError(t, mock.ExpectationsWereMet())

	date := req.Timestamp.UTC().Format("2006-01-02")
	base := filepath.Join(mirror.BaseDir(), req.TenantID, date)
	want := map[string]string{
		req.RequestID + ".req.json.gz":  "hi",
		req.RequestID + ".resp.json.gz": "hello",
		req.RequestID + ".out.json.gz":  "hello", // per-turn outbound（final_full 关闭时随 bodies 行）
	}
	for name, needle := range want {
		raw, err := os.ReadFile(filepath.Join(base, name))
		require.NoError(t, err, "镜像文件应存在: %s", name)
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		require.NoError(t, err, "%s 应为合法 gzip", name)
		var buf bytes.Buffer
		_, err = io.Copy(&buf, zr)
		require.NoError(t, err)
		require.Contains(t, buf.String(), needle, "%s 内容与 PG 行同源", name)
	}
	// 不该有其他文件（direction 集合闭合）。
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	require.Len(t, entries, 3, "恰好三件套")
}

// TestMirrorableTurnPayload：镜像载荷跳过矩阵（空 / "null" / 有效 JSON）。
func TestMirrorableTurnPayload(t *testing.T) {
	require.False(t, mirrorableTurnPayload(nil))
	require.False(t, mirrorableTurnPayload([]byte("null")))
	require.True(t, mirrorableTurnPayload([]byte(`[{"role":"user"}]`)))
}

// finalFullFakeKV 只读 settings_kv 桩：钉 storage.session_final_full_enabled。
type finalFullFakeKV struct{ on bool }

func (f *finalFullFakeKV) Get(scope settings.Scope, key string) ([]byte, error) {
	if scope == settings.ScopePlatform && key == "storage.session_final_full_enabled" {
		if f.on {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	}
	return nil, nil
}

func (f *finalFullFakeKV) Set(scope settings.Scope, key string, value any) ([]byte, error) {
	return nil, nil
}

func (f *finalFullFakeKV) GetTenant(tenantID, key string) ([]byte, error) { return nil, nil }

func (f *finalFullFakeKV) SetTenant(tenantID, key string, value any) ([]byte, error) {
	return nil, nil
}
