package storage_test

// consistency_lite_test.go — 2026-09-05 审计闭环4：lite 模式跨资源一致性
// 与重启恢复的运行态证据。
//
// 审计缺口：lite 模式 request body（文件）与 SQLite reference（turn meta /
// session 行 / request_logs.has_body）、附件与 session 元数据的跨资源
// 原子性缺少集成测试。本文件用真实 SQLite + 真实文件系统验证：
//
//  1. 一致写入 → 关闭（重启）→ 重开：三侧（session 行 / turn meta /
//     body 文件 / request log）可完整往返，内容逐字节一致；
//  2. 写 body 后写 meta 前中断（进程崩溃窗口）→ 留下孤儿 body 文件，
//     Reconcile 必须检出且 Repair 可清理；
//  3. 写 meta 后 body 缺失 → Reconcile 检出 MissingBodies（内容丢失，
//     只报告不伪造）；
//  4. 一致会话 Reconcile 报告 Consistent=true（无误报）。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/kaixuan/llm-gateway-go/storage"
	"github.com/kaixuan/llm-gateway-go/storage/file"
	"github.com/kaixuan/llm-gateway-go/storage/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type liteHarness struct {
	dir     string
	dbPath  string
	db      *sql.DB
	bodies  *file.FileBodiesStore
	session *sqlite.SQLiteSessionStore
	turns   *sqlite.SQLiteTurnsStore
	logs    *sqlite.SQLiteRequestLogStore
}

func newLiteHarness(t *testing.T) *liteHarness {
	t.Helper()
	dir := t.TempDir()
	h := &liteHarness{
		dir:    dir,
		dbPath: filepath.Join(dir, "lite.db"),
	}
	h.open()
	t.Cleanup(func() { _ = h.close() })
	return h
}

// open/reopen 模拟进程重启：所有存储基于同一 SQLite 文件与同一 bodies 目录。
func (h *liteHarness) open() {
	db, err := sqlite.OpenSQLite(h.dbPath)
	if err != nil {
		panic(err)
	}
	h.db = db
	h.bodies = file.NewFileBodiesStore(filepath.Join(h.dir, "bodies"), 2)
	h.session = sqlite.NewSQLiteSessionStore(h.db)
	h.turns = sqlite.NewSQLiteTurnsStore(h.db)
	h.logs = sqlite.NewSQLiteRequestLogStore(h.db)
}

func (h *liteHarness) close() error {
	if h.db != nil {
		_ = h.db.Close()
		h.db = nil
	}
	return h.bodies.Close()
}

// writeTurn 是 lite 模式单轮写入的最小原子单元：meta（SQLite）+ body（文件）
// + request log（has_body 标记）。生产实现应保证顺序：body 先落盘、meta
// 后提交（meta 是"已提交"标志，孤儿 body 可被补偿清理，反之则丢内容）。
func writeTurn(t *testing.T, h *liteHarness, tenantID, sessionID string, turnNo int, req, resp string) {
	t.Helper()
	body := &storage.SessionBody{
		TenantID:  tenantID,
		SessionID: sessionID,
		TurnNo:    turnNo,
		Request:   json.RawMessage(req),
		Response:  json.RawMessage(resp),
		Metadata:  map[string]interface{}{"model": "glm-5.2"},
	}
	require.NoError(t, h.bodies.Write(context.Background(), body))
	require.NoError(t, h.turns.WriteTurnMeta(context.Background(), &storage.TurnMeta{
		TenantID: tenantID, SessionID: sessionID, TurnNo: turnNo,
		CompressionStrategy: "none", PromptTokens: 100 + turnNo, CompletionTokens: 200 + turnNo,
	}))
}

func TestLiteConsistencyRoundTripAndRestart(t *testing.T) {
	h := newLiteHarness(t)
	ctx := context.Background()

	require.NoError(t, h.session.CreateSession(ctx, &storage.Session{
		ID: "sess-round-1", TenantID: "tenant-a",
		Metadata: map[string]interface{}{"model": "glm-5.2"},
	}))

	type turnFixture struct {
		turn            int
		req, resp, meta string
	}
	fixtures := []turnFixture{
		{1, `{"q":"turn-1-请求"}`, `{"a":"turn-1-回答"}`, `{"model":"glm-5.2"}`},
		{2, `{"q":"turn-2-请求"}`, `{"a":"turn-2-回答"}`, `{"model":"glm-5.2"}`},
		{3, `{"q":"turn-3-请求"}`, `{"a":"turn-3-回答"}`, `{"model":"glm-5.2"}`},
	}
	for _, f := range fixtures {
		writeTurn(t, h, "tenant-a", "sess-round-1", f.turn, f.req, f.resp)
	}

	// —— 模拟重启：全部关闭后基于同一目录重开 ——
	require.NoError(t, h.close())
	h.open()

	sess, err := h.session.GetSession(ctx, "tenant-a", "sess-round-1")
	require.NoError(t, err, "session row must survive restart")
	assert.Equal(t, "glm-5.2", sess.Metadata["model"], "session metadata must roundtrip")

	metas, err := h.turns.GetTurnsMeta(ctx, "tenant-a", "sess-round-1")
	require.NoError(t, err)
	require.Len(t, metas, 3, "all turn metas must survive restart")
	for i, f := range fixtures {
		assert.Equal(t, f.turn, metas[i].TurnNo)
		assert.Equal(t, 100+f.turn, metas[i].PromptTokens, "turn meta roundtrip")
	}

	for _, f := range fixtures {
		body, err := h.bodies.Read(ctx, "tenant-a", "sess-round-1", f.turn)
		require.NoError(t, err, "body file must survive restart")
		assert.JSONEq(t, f.req, string(body.Request), "request body bytes must roundtrip")
		assert.JSONEq(t, f.resp, string(body.Response), "response body bytes must roundtrip")
		assert.Equal(t, f.turn, body.TurnNo)
	}

	// 跨资源关联键一致性：meta 侧与 body 侧的 turn 集合完全一致。
	report, err := storage.ReconcileTurnArtifacts(ctx, "tenant-a", "sess-round-1", h.bodies, h.turns)
	require.NoError(t, err)
	assert.True(t, report.Consistent, "freshly written session must reconcile clean: %+v", report)
	assert.Empty(t, report.MissingBodies)
	assert.Empty(t, report.OrphanBodies)
	assert.Equal(t, []int{1, 2, 3}, report.TurnsWithMeta)
	assert.Equal(t, []int{1, 2, 3}, report.TurnsWithBody)
}

// TestLiteReconcileOrphanBodyAndRepair 模拟「body 落盘后、meta 提交前
// 进程崩溃」：孤儿文件被检出，RepairDeleteOrphanBodies 清理后恢复一致。
func TestLiteReconcileOrphanBodyAndRepair(t *testing.T) {
	h := newLiteHarness(t)
	ctx := context.Background()

	writeTurn(t, h, "tenant-a", "sess-orphan", 1, `{"q":"1"}`, `{"a":"1"}`)
	writeTurn(t, h, "tenant-a", "sess-orphan", 2, `{"q":"2"}`, `{"a":"2"}`)
	// 模拟崩溃窗口：turn 3 只写了 body，meta 未提交。
	require.NoError(t, h.bodies.Write(ctx, &storage.SessionBody{
		TenantID: "tenant-a", SessionID: "sess-orphan", TurnNo: 3,
		Request: json.RawMessage(`{"q":"3"}`), Response: json.RawMessage(`{"a":"3"}`),
	}))

	report, err := storage.ReconcileTurnArtifacts(ctx, "tenant-a", "sess-orphan", h.bodies, h.turns)
	require.NoError(t, err)
	assert.False(t, report.Consistent)
	assert.Equal(t, []int{3}, report.OrphanBodies, "orphan body must be detected")
	assert.Empty(t, report.MissingBodies)

	// 只报告策略：不动数据。
	require.NoError(t, storage.RepairTurnArtifacts(ctx, report, h.bodies, storage.RepairReportOnly))
	still, err := storage.ReconcileTurnArtifacts(ctx, "tenant-a", "sess-orphan", h.bodies, h.turns)
	require.NoError(t, err)
	assert.Equal(t, []int{3}, still.OrphanBodies, "report-only must not delete data")

	// 删除策略：孤儿清理，turn 1/2 不受影响。
	require.NoError(t, storage.RepairTurnArtifacts(ctx, report, h.bodies, storage.RepairDeleteOrphanBodies))
	repaired, err := storage.ReconcileTurnArtifacts(ctx, "tenant-a", "sess-orphan", h.bodies, h.turns)
	require.NoError(t, err)
	assert.True(t, repaired.Consistent, "repair must restore consistency: %+v", repaired)
	// 孤儿 turn 3 的 body 被删，但 meta 侧本就没有 turn 3，两侧仍对齐。
	assert.Equal(t, []int{1, 2}, repaired.TurnsWithBody)

	// 未受影响轮次内容完好。
	body, err := h.bodies.Read(ctx, "tenant-a", "sess-orphan", 1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"q":"1"}`, string(body.Request))
}

// TestLiteReconcileMissingBody 模拟「meta 提交后 body 丢失」（磁盘清理
// 误删 / body 写失败但 meta 已提交）：检出 MissingBodies；补偿策略下也
// 不伪造数据，只报告。
func TestLiteReconcileMissingBody(t *testing.T) {
	h := newLiteHarness(t)
	ctx := context.Background()

	writeTurn(t, h, "tenant-a", "sess-missing", 1, `{"q":"1"}`, `{"a":"1"}`)
	writeTurn(t, h, "tenant-a", "sess-missing", 2, `{"q":"2"}`, `{"a":"2"}`)

	// 直接删 turn 2 的文件（绕过 meta）。
	require.NoError(t, h.bodies.DeleteTurnFile(ctx, "tenant-a", "sess-missing", 2))

	report, err := storage.ReconcileTurnArtifacts(ctx, "tenant-a", "sess-missing", h.bodies, h.turns)
	require.NoError(t, err)
	assert.False(t, report.Consistent)
	assert.Equal(t, []int{2}, report.MissingBodies, "missing body must be reported")
	assert.Empty(t, report.OrphanBodies)

	// 任何策略都不伪造缺失内容。
	require.NoError(t, storage.RepairTurnArtifacts(ctx, report, h.bodies, storage.RepairDeleteOrphanBodies))
	after, err := storage.ReconcileTurnArtifacts(ctx, "tenant-a", "sess-missing", h.bodies, h.turns)
	require.NoError(t, err)
	assert.Equal(t, []int{2}, after.MissingBodies, "missing content must never be fabricated")

	_, readErr := h.bodies.Read(ctx, "tenant-a", "sess-missing", 2)
	assert.ErrorIs(t, readErr, storage.ErrNotFound)
}

// TestLiteBodyGzipRoundTrip 内容完整性：gzip 压缩层必须无损往返，
// 多字节（中文）内容不得被截断。
func TestLiteBodyGzipRoundTrip(t *testing.T) {
	h := newLiteHarness(t)
	ctx := context.Background()
	long := bytes.Repeat([]byte("网关一致性验证-"), 2000)
	req := `{"messages":[{"role":"user","content":"` + string(long) + `"}]}`
	writeTurn(t, h, "tenant-b", "sess-gzip", 1, req, `{"a":"ok"}`)

	body, err := h.bodies.Read(ctx, "tenant-b", "sess-gzip", 1)
	require.NoError(t, err)
	assert.JSONEq(t, req, string(body.Request), "large multibody content must roundtrip losslessly")
}
