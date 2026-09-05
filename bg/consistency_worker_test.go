package bg

// consistency_worker_test.go — 2026-09-05 round2 审计 B-#2：一致性对账
// worker 单元测试（fake store，无真实 SQLite/文件系统依赖）。
//
// 覆盖：默认参数（report-only 恒安全）、空闲阈值与单轮上限的传递、
// report-only 不删数据、delete 模式删除（并保留复检护栏）、枚举失败报错、
// 单会话对账失败不拖垮整轮、Start 首跑延迟 + 周期 + ctx 优雅退出。

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeIdleSessionLister 记录 worker 传入的枚举参数并返回预设会话。
// mu 保护快照字段：Start 场景下 worker 协程写入、测试协程轮询。
type fakeIdleSessionLister struct {
	mu            sync.Mutex
	sessions      []*storage.Session
	gotIdleBefore time.Time
	gotLimit      int
	calls         int
}

func (f *fakeIdleSessionLister) ListIdleSessions(_ context.Context, idleBefore time.Time, limit int) ([]*storage.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotIdleBefore, f.gotLimit = idleBefore, limit
	f.calls++
	return f.sessions, nil
}

// snapshot 原子读取枚举参数快照（跨协程轮询用）。
func (f *fakeIdleSessionLister) snapshot() (idleBefore time.Time, limit, calls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotIdleBefore, f.gotLimit, f.calls
}

// fakeWorkerTurns 是可控 TurnsStore fake（key = "tenant/session"）。
type fakeWorkerTurns struct {
	metas map[string][]int
}

func (f *fakeWorkerTurns) GetTurnsMeta(_ context.Context, tenantID, sessionID string) ([]*storage.TurnMeta, error) {
	out := make([]*storage.TurnMeta, 0, len(f.metas[tenantID+"/"+sessionID]))
	for _, n := range f.metas[tenantID+"/"+sessionID] {
		out = append(out, &storage.TurnMeta{TenantID: tenantID, SessionID: sessionID, TurnNo: n})
	}
	return out, nil
}

func (f *fakeWorkerTurns) WriteTurnMeta(_ context.Context, _ *storage.TurnMeta) error { return nil }

// fakeWorkerBodies 实现 BodiesStore + BodiesLister + TurnFileDeleter 的 fake：
// turns 为各会话已落盘 turn 集合，deleted 记录删除动作（"tenant/session#n"）。
type fakeWorkerBodies struct {
	turns   map[string][]int
	deleted []string
}

func (f *fakeWorkerBodies) Write(_ context.Context, _ *storage.SessionBody) error { return nil }
func (f *fakeWorkerBodies) Read(_ context.Context, _, _ string, _ int) (*storage.SessionBody, error) {
	return nil, storage.ErrNotFound
}
func (f *fakeWorkerBodies) ReadRange(_ context.Context, _, _ string, _, _ int) ([]*storage.SessionBody, error) {
	return nil, nil
}
func (f *fakeWorkerBodies) Delete(_ context.Context, _, _ string) error { return nil }
func (f *fakeWorkerBodies) ListTurns(_ context.Context, tenantID, sessionID string) ([]int, error) {
	return f.turns[tenantID+"/"+sessionID], nil
}
func (f *fakeWorkerBodies) DeleteTurnFile(_ context.Context, tenantID, sessionID string, turnNo int) error {
	f.deleted = append(f.deleted, tenantID+"/"+sessionID+"#"+strconv.Itoa(turnNo))
	return nil
}

// fakeSequenceTurns 让 GetTurnsMeta 按调用次序返回预设集合（复检护栏接线
// 验证：Reconcile 第一次快照判孤儿、Repair 复检第二次已在 meta）。
type fakeSequenceTurns struct {
	returns [][]*storage.TurnMeta
	calls   int
}

func (f *fakeSequenceTurns) GetTurnsMeta(_ context.Context, _, _ string) ([]*storage.TurnMeta, error) {
	i := f.calls
	if i >= len(f.returns) {
		i = len(f.returns) - 1
	}
	f.calls++
	return f.returns[i], nil
}

func (f *fakeSequenceTurns) WriteTurnMeta(_ context.Context, _ *storage.TurnMeta) error { return nil }

// fakeBareBodies 只实现 BodiesStore（无 BodiesLister）：Reconcile 必然报错，
// 用于验证单会话失败不拖垮整轮。
type fakeBareBodies struct{}

func (f fakeBareBodies) Write(_ context.Context, _ *storage.SessionBody) error { return nil }
func (f fakeBareBodies) Read(_ context.Context, _, _ string, _ int) (*storage.SessionBody, error) {
	return nil, storage.ErrNotFound
}
func (f fakeBareBodies) ReadRange(_ context.Context, _, _ string, _, _ int) ([]*storage.SessionBody, error) {
	return nil, nil
}
func (f fakeBareBodies) Delete(_ context.Context, _, _ string) error { return nil }

func idleSession(tenant, id string) *storage.Session {
	return &storage.Session{TenantID: tenant, ID: id}
}

func TestConsistencyWorkerDefaults(t *testing.T) {
	w := NewConsistencyWorker(&fakeIdleSessionLister{}, &fakeWorkerTurns{}, &fakeWorkerBodies{})
	if w.action != storage.RepairReportOnly {
		t.Errorf("默认 action = %v, want report_only（恒安全）", w.action)
	}
	if w.interval != 24*time.Hour || w.idleThreshold != 10*time.Minute ||
		w.maxSessions != 500 || w.initialDelay != 10*time.Minute {
		t.Errorf("默认参数 = %v/%v/%d/%v, want 24h/10m/500/10m",
			w.interval, w.idleThreshold, w.maxSessions, w.initialDelay)
	}
	// With* 覆盖与零值防御。
	w.WithInterval(time.Minute).WithIdleThreshold(time.Second).WithMaxSessions(3).WithInitialDelay(time.Millisecond)
	w.WithInterval(0).WithIdleThreshold(0).WithMaxSessions(0).WithInitialDelay(0)
	if w.interval != time.Minute || w.idleThreshold != time.Second || w.maxSessions != 3 || w.initialDelay != time.Millisecond {
		t.Errorf("With* 覆盖失败或零值穿透: %v/%v/%d/%v", w.interval, w.idleThreshold, w.maxSessions, w.initialDelay)
	}
}

func TestConsistencyWorker_RunOnce_ReportOnly(t *testing.T) {
	lister := &fakeIdleSessionLister{sessions: []*storage.Session{
		idleSession("tenant-a", "sess-1"), idleSession("tenant-b", "sess-2"),
	}}
	turns := &fakeWorkerTurns{metas: map[string][]int{
		"tenant-a/sess-1": {1},    // turn 2 有 body 无 meta → 孤儿
		"tenant-b/sess-2": {1, 2}, // turn 2 有 meta 无 body → missing
	}}
	bodies := &fakeWorkerBodies{turns: map[string][]int{
		"tenant-a/sess-1": {1, 2},
	}}

	w := NewConsistencyWorker(lister, turns, bodies)
	require.NoError(t, w.RunOnce(context.Background()))

	// report-only：不动任何数据。
	assert.Empty(t, bodies.deleted, "report-only must not delete anything")
	assert.Equal(t, 2, w.lastSessionsChecked)
	assert.Equal(t, 2, w.lastInconsistent)
	assert.Equal(t, 1, w.lastOrphans)
	assert.Equal(t, 2, w.lastMissing, "sess-2 has meta 1,2 but no body files at all")
	assert.Equal(t, 0, w.lastDeleted)
}

func TestConsistencyWorker_RunOnce_DeleteMode(t *testing.T) {
	lister := &fakeIdleSessionLister{sessions: []*storage.Session{idleSession("tenant-a", "sess-1")}}
	turns := &fakeWorkerTurns{metas: map[string][]int{"tenant-a/sess-1": {1}}}
	bodies := &fakeWorkerBodies{turns: map[string][]int{"tenant-a/sess-1": {1, 2, 3}}}

	w := NewConsistencyWorker(lister, turns, bodies).WithAction(storage.RepairDeleteOrphanBodies)
	require.NoError(t, w.RunOnce(context.Background()))

	// delete 模式：复检确认后的真孤儿被删（fake meta 中无 turn 2/3）。
	assert.ElementsMatch(t, []string{"tenant-a/sess-1#2", "tenant-a/sess-1#3"}, bodies.deleted)
	assert.Equal(t, 2, w.lastDeleted)
}

// TestConsistencyWorker_RunOnce_KeepsDoubleConfirm 验证 worker 的删除路径
// 保留 storage.RepairTurnArtifacts 的复检护栏：Reconcile 快照判孤儿、Repair
// 复检时 meta 已提交的在途轮不被删除。
func TestConsistencyWorker_RunOnce_KeepsDoubleConfirm(t *testing.T) {
	lister := &fakeIdleSessionLister{sessions: []*storage.Session{idleSession("tenant-a", "sess-1")}}
	turns := &fakeSequenceTurns{returns: [][]*storage.TurnMeta{
		turnMetasFor(1),    // Reconcile 快照：只有 turn 1 → turn 2 被判孤儿
		turnMetasFor(1, 2), // Repair 复检：turn 2 已提交（在途窗口闭合）
	}}
	bodies := &fakeWorkerBodies{turns: map[string][]int{"tenant-a/sess-1": {1, 2}}}

	w := NewConsistencyWorker(lister, turns, bodies).WithAction(storage.RepairDeleteOrphanBodies)
	require.NoError(t, w.RunOnce(context.Background()))

	assert.Empty(t, bodies.deleted, "in-flight turn must survive worker-driven repair")
	assert.Equal(t, 0, w.lastDeleted)
	assert.Equal(t, 1, w.lastOrphans, "snapshot still reports the (resolved) orphan")
}

func TestConsistencyWorker_RunOnce_BoundedAndIdleCutoff(t *testing.T) {
	lister := &fakeIdleSessionLister{sessions: []*storage.Session{idleSession("t", "s")}}
	w := NewConsistencyWorker(lister, &fakeWorkerTurns{}, &fakeWorkerBodies{}).
		WithIdleThreshold(5 * time.Minute).
		WithMaxSessions(3)

	before := time.Now()
	require.NoError(t, w.RunOnce(context.Background()))
	after := time.Now()

	gotIdleBefore, gotLimit, _ := lister.snapshot()
	assert.Equal(t, 3, gotLimit, "max sessions bound must be passed to the lister")
	if gotIdleBefore.Before(before.Add(-5*time.Minute)) || gotIdleBefore.After(after.Add(-5*time.Minute)) {
		t.Errorf("idleBefore = %v, want ~%v", gotIdleBefore, before.Add(-5*time.Minute))
	}
}

func TestConsistencyWorker_RunOnce_EnumerationError(t *testing.T) {
	lister := &failingIdleLister{err: errors.New("db down")}
	w := NewConsistencyWorker(lister, &fakeWorkerTurns{}, &fakeWorkerBodies{})
	err := w.RunOnce(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "枚举空闲会话失败")
}

type failingIdleLister struct{ err error }

func (f *failingIdleLister) ListIdleSessions(context.Context, time.Time, int) ([]*storage.Session, error) {
	return nil, f.err
}

// TestConsistencyWorker_RunOnce_PerSessionErrorContinues：bodies 不支持
// ListTurns 时单会话对账失败只跳过（告警），整轮不报错、其余统计为零。
func TestConsistencyWorker_RunOnce_PerSessionErrorContinues(t *testing.T) {
	lister := &fakeIdleSessionLister{sessions: []*storage.Session{idleSession("t", "s")}}
	w := NewConsistencyWorker(lister, &fakeWorkerTurns{metas: map[string][]int{}}, fakeBareBodies{})
	require.NoError(t, w.RunOnce(context.Background()))
	assert.Equal(t, 1, w.lastSessionsChecked)
	assert.Equal(t, 0, w.lastInconsistent)
	assert.Equal(t, 0, w.lastDeleted)
}

func TestConsistencyWorker_Start_LoopAndGracefulExit(t *testing.T) {
	lister := &fakeIdleSessionLister{}
	w := NewConsistencyWorker(lister, &fakeWorkerTurns{}, &fakeWorkerBodies{}).
		WithInitialDelay(5 * time.Millisecond).
		WithInterval(15 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Start(ctx)
		close(done)
	}()

	// 首跑延迟 + 至少一个周期：等待第二次枚举。
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, calls := lister.snapshot()
		if calls >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Start 未在期限内完成首跑与首个周期")
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
		// ctx 取消后优雅退出。
	case <-time.After(2 * time.Second):
		t.Fatal("Start 未在 ctx 取消后返回")
	}
}

func turnMetasFor(turnNos ...int) []*storage.TurnMeta {
	out := make([]*storage.TurnMeta, 0, len(turnNos))
	for _, n := range turnNos {
		out = append(out, &storage.TurnMeta{TenantID: "tenant-a", SessionID: "sess-1", TurnNo: n})
	}
	return out
}
