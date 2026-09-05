package storage_test

// consistency_repair_test.go — 2026-09-05 round2 审计 G-#9 / B-#6：Repair
// 删除路径安全语义的单元测试（fake store 可控序列，不依赖真实 SQLite/文件
// 系统；真实 store 的端到端场景见 consistency_lite_test.go）。
//
// 覆盖：
//  1. G-#9 复检（double-confirm）：第一次快照后 meta 提交的竞态——可控序列
//     fake 让 GetTurnsMeta 第一次返回空（Reconcile 判孤儿）、第二次返回该
//     turn（Repair 复检），断言不删合法 body；
//  2. G-#9 宽限降级：bodies 实现 TurnFileDeleter 但未实现 TurnFileStater 时
//     无 mtime 可查，宽限护栏不生效（删除仅受复检一道保险），文档化该语义；
//  3. B-#6：action=RepairDeleteOrphanBodies 且有待删孤儿、但 bodies 未实现
//     TurnFileDeleter → 返回 error（不再静默 no-op）；report-only、无孤儿、
//     nil turns 等边界一并钉死。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRepairTurns 是可控的 TurnsStore fake：metas 为当前 meta 集合，
// WriteTurnMeta 直接改写（序列化竞态用 sequenceTurns）。
type fakeRepairTurns struct {
	metas map[string][]int // "tenant/session" → 已提交 turn 集合
}

func newFakeRepairTurns() *fakeRepairTurns {
	return &fakeRepairTurns{metas: make(map[string][]int)}
}

func (f *fakeRepairTurns) GetTurnsMeta(_ context.Context, tenantID, sessionID string) ([]*storage.TurnMeta, error) {
	out := make([]*storage.TurnMeta, 0, len(f.metas[tenantID+"/"+sessionID]))
	for _, n := range f.metas[tenantID+"/"+sessionID] {
		out = append(out, &storage.TurnMeta{TenantID: tenantID, SessionID: sessionID, TurnNo: n})
	}
	return out, nil
}

func (f *fakeRepairTurns) WriteTurnMeta(_ context.Context, meta *storage.TurnMeta) error {
	key := meta.TenantID + "/" + meta.SessionID
	for _, n := range f.metas[key] {
		if n == meta.TurnNo {
			return nil
		}
	}
	f.metas[key] = append(f.metas[key], meta.TurnNo)
	return nil
}

// fakeRepairBodies 只实现 BodiesStore + BodiesLister（故意不实现
// TurnFileDeleter / TurnFileStater，用于 B-#6 缺删除器场景）。
type fakeRepairBodies struct {
	turns []int // ListTurns 结果
}

func (f *fakeRepairBodies) Write(_ context.Context, _ *storage.SessionBody) error { return nil }
func (f *fakeRepairBodies) Read(_ context.Context, _, _ string, _ int) (*storage.SessionBody, error) {
	return nil, storage.ErrNotFound
}
func (f *fakeRepairBodies) ReadRange(_ context.Context, _, _ string, _, _ int) ([]*storage.SessionBody, error) {
	return nil, nil
}
func (f *fakeRepairBodies) Delete(_ context.Context, _, _ string) error { return nil }
func (f *fakeRepairBodies) ListTurns(_ context.Context, _, _ string) ([]int, error) {
	return f.turns, nil
}

// fakeDeleteOnlyBodies 在 fakeRepairBodies 之上补齐 TurnFileDeleter，但不实现
// TurnFileStater——模拟「能删、无 mtime 查询能力」的 bodies store。
type fakeDeleteOnlyBodies struct {
	fakeRepairBodies
	deleted []int
}

func (f *fakeDeleteOnlyBodies) DeleteTurnFile(_ context.Context, _, _ string, turnNo int) error {
	f.deleted = append(f.deleted, turnNo)
	return nil
}

// fakeStatableBodies 再补 TurnFileStater（mtime 可控），用于宽限判定单测。
type fakeStatableBodies struct {
	fakeDeleteOnlyBodies
	mtimes map[int]time.Time
}

func (f *fakeStatableBodies) TurnFileModTime(_ context.Context, _, _ string, turnNo int) (time.Time, error) {
	mt, ok := f.mtimes[turnNo]
	if !ok {
		return time.Time{}, storage.ErrNotFound
	}
	return mt, nil
}

// sequenceTurns 让 GetTurnsMeta 按调用次序依次返回预设集合——精确模拟
// 「Reconcile 第一次读 meta（无该 turn）→ Repair 复检第二次读（已提交）」
// 的在途竞态（G-#9 读-读窗口）。最后一次返回会被后续调用重复使用。
type sequenceTurns struct {
	returns [][]*storage.TurnMeta
	calls   int
}

func (s *sequenceTurns) GetTurnsMeta(_ context.Context, _, _ string) ([]*storage.TurnMeta, error) {
	i := s.calls
	if i >= len(s.returns) {
		i = len(s.returns) - 1
	}
	s.calls++
	return s.returns[i], nil
}

func (s *sequenceTurns) WriteTurnMeta(_ context.Context, _ *storage.TurnMeta) error { return nil }

// TestRepairDoubleConfirmSequenceRace（G-#9 复检）：用可控序列 fake 复现
// 「第一次快照后 meta 提交」竞态——Reconcile 判 turn 2 为孤儿，Repair 复检
// 发现 meta 已提交 → 不删；两次快照都不在 meta 的 turn 3 照常删除。
func TestRepairDoubleConfirmSequenceRace(t *testing.T) {
	ctx := context.Background()
	turns := &sequenceTurns{returns: [][]*storage.TurnMeta{
		turnMetas(1),    // 第一次快照：只有 turn 1
		turnMetas(1, 2), // 第二次复检：turn 2 已提交（在途窗口闭合）
	}}
	bodies := &fakeStatableBodies{ // stater 有 mtime 但已超出宽限（回拨），不干扰复检断言
		fakeDeleteOnlyBodies: fakeDeleteOnlyBodies{fakeRepairBodies: fakeRepairBodies{turns: []int{1, 2, 3}}},
		mtimes:               map[int]time.Time{2: time.Now().Add(-2 * storage.DefaultOrphanBodyGrace), 3: time.Now().Add(-2 * storage.DefaultOrphanBodyGrace)},
	}

	report, err := storage.ReconcileTurnArtifacts(ctx, "tenant-a", "sess-seq", bodies, turns)
	require.NoError(t, err)
	require.Equal(t, []int{2, 3}, report.OrphanBodies, "snapshot flags both turns as orphans")

	res, err := storage.RepairTurnArtifacts(ctx, report, bodies, turns,
		storage.RepairDeleteOrphanBodies, nil)
	require.NoError(t, err)
	assert.Equal(t, []int{2}, res.SkippedInFlight, "turn committed between snapshots must be kept")
	assert.Equal(t, []int{3}, res.Deleted, "turn absent from both snapshots must be deleted")
	assert.Equal(t, []int{3}, bodies.deleted)
}

// TestRepairGraceAndDegradedStater（G-#9 宽限及其降级）：
//   - 有 TurnFileStater：宽限期内的新鲜孤儿跳过删除（SkippedByGrace），
//     mtime 已过宽限期的照常删除；stater 报 ErrNotFound（文件已消失）时
//     无需删除、直接跳过；
//   - 无 TurnFileStater（fakeDeleteOnlyBodies）：宽限护栏不生效，仅剩复检
//     一道保险，孤儿照常删除（文档化降级语义）。
func TestRepairGraceAndDegradedStater(t *testing.T) {
	ctx := context.Background()
	turns := newFakeRepairTurns()
	require.NoError(t, turns.WriteTurnMeta(ctx, &storage.TurnMeta{TenantID: "t", SessionID: "s", TurnNo: 1}))
	fresh := time.Now()
	aged := fresh.Add(-2 * storage.DefaultOrphanBodyGrace)

	t.Run("fresh orphan within default grace is skipped", func(t *testing.T) {
		bodies := &fakeStatableBodies{
			fakeDeleteOnlyBodies: fakeDeleteOnlyBodies{fakeRepairBodies: fakeRepairBodies{turns: []int{1, 2}}},
			mtimes:               map[int]time.Time{2: fresh},
		}
		report := &storage.TurnArtifactReport{TenantID: "t", SessionID: "s", OrphanBodies: []int{2}}
		res, err := storage.RepairTurnArtifacts(ctx, report, bodies, turns, storage.RepairDeleteOrphanBodies, nil)
		require.NoError(t, err)
		assert.Equal(t, []int{2}, res.SkippedByGrace)
		assert.Empty(t, res.Deleted)
		assert.Empty(t, bodies.deleted, "fresh orphan must not be deleted")
	})

	t.Run("aged orphan is deleted", func(t *testing.T) {
		bodies := &fakeStatableBodies{
			fakeDeleteOnlyBodies: fakeDeleteOnlyBodies{fakeRepairBodies: fakeRepairBodies{turns: []int{1, 2}}},
			mtimes:               map[int]time.Time{2: aged},
		}
		report := &storage.TurnArtifactReport{TenantID: "t", SessionID: "s", OrphanBodies: []int{2}}
		res, err := storage.RepairTurnArtifacts(ctx, report, bodies, turns, storage.RepairDeleteOrphanBodies, nil)
		require.NoError(t, err)
		assert.Equal(t, []int{2}, res.Deleted)
		assert.Equal(t, []int{2}, bodies.deleted)
	})

	t.Run("vanished orphan is a no-op", func(t *testing.T) {
		bodies := &fakeStatableBodies{
			fakeDeleteOnlyBodies: fakeDeleteOnlyBodies{fakeRepairBodies: fakeRepairBodies{turns: []int{1, 2}}},
			mtimes:               map[int]time.Time{}, // TurnFileModTime → ErrNotFound：文件已不在
		}
		report := &storage.TurnArtifactReport{TenantID: "t", SessionID: "s", OrphanBodies: []int{2}}
		res, err := storage.RepairTurnArtifacts(ctx, report, bodies, turns, storage.RepairDeleteOrphanBodies, nil)
		require.NoError(t, err)
		assert.Empty(t, res.Deleted)
		assert.Empty(t, bodies.deleted)
		// 2026-09-05 round2 复审 F3：已消失孤儿计入 Vanished 桶（审计对账
		// 可解释），不再"凭空消失"。
		assert.Equal(t, []int{2}, res.Vanished)
	})

	t.Run("deleter without stater skips grace but still deletes", func(t *testing.T) {
		bodies := &fakeDeleteOnlyBodies{fakeRepairBodies: fakeRepairBodies{turns: []int{1, 2}}}
		report := &storage.TurnArtifactReport{TenantID: "t", SessionID: "s", OrphanBodies: []int{2}}
		res, err := storage.RepairTurnArtifacts(ctx, report, bodies, turns, storage.RepairDeleteOrphanBodies, nil)
		require.NoError(t, err)
		assert.Equal(t, []int{2}, res.Deleted, "no stater: deletion relies on re-check only")
	})
}

// TestRepairDeleteWithoutDeleterReturnsError（B-#6）：删除策略 + 待删孤儿 +
// bodies 未实现 TurnFileDeleter → 返回 error；report-only、无孤儿、nil turns
// 边界一并钉死。
func TestRepairDeleteWithoutDeleterReturnsError(t *testing.T) {
	ctx := context.Background()
	turns := newFakeRepairTurns()
	require.NoError(t, turns.WriteTurnMeta(ctx, &storage.TurnMeta{TenantID: "tenant-a", SessionID: "sess-x", TurnNo: 1}))
	bodies := &fakeRepairBodies{turns: []int{1, 2}} // 无 TurnFileDeleter

	report, err := storage.ReconcileTurnArtifacts(ctx, "tenant-a", "sess-x", bodies, turns)
	require.NoError(t, err)
	require.Equal(t, []int{2}, report.OrphanBodies)

	// 删除策略：缺删除器必须显式报错（不再静默 no-op）。
	res, err := storage.RepairTurnArtifacts(ctx, report, bodies, turns,
		storage.RepairDeleteOrphanBodies, &storage.RepairOptions{OrphanGrace: -1})
	require.Error(t, err, "delete action without TurnFileDeleter must return an error")
	assert.Nil(t, res.Deleted)
	assert.True(t, strings.Contains(err.Error(), "TurnFileDeleter"), "error should name the missing capability: %v", err)

	// report-only：同一缺删除器 store 下 no-op 成功（B-#6 只针对删除动作）。
	res, err = storage.RepairTurnArtifacts(ctx, report, bodies, turns, storage.RepairReportOnly, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Deleted)

	// 无孤儿时即便缺删除器也无须报错（无事可做）。
	clean := &storage.TurnArtifactReport{TenantID: "tenant-a", SessionID: "sess-x"}
	res, err = storage.RepairTurnArtifacts(ctx, clean, bodies, turns, storage.RepairDeleteOrphanBodies, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Deleted)

	// nil turns 时无法复检，删除动作直接拒绝。
	_, err = storage.RepairTurnArtifacts(ctx, report, bodies, nil,
		storage.RepairDeleteOrphanBodies, &storage.RepairOptions{OrphanGrace: -1})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "double-confirm"), "error should explain why: %v", err)
}

func turnMetas(turnNos ...int) []*storage.TurnMeta {
	out := make([]*storage.TurnMeta, 0, len(turnNos))
	for _, n := range turnNos {
		out = append(out, &storage.TurnMeta{TenantID: "tenant-a", SessionID: "sess-seq", TurnNo: n})
	}
	return out
}
