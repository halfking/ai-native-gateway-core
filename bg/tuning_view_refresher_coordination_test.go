package bg

// tuning_view_refresher_coordination_test.go — 2026-10-20（R51 96h 轮）
// 给 tuning refresher 的跨实例协调补判据门。
//
// 本轮的核心命题：「tuning 刷新 2× 放大 = 两个实例各跑一份无协调的
// 5 分钟 ticker」。判据要能抓住它，而且**每一腿都要能红**。
//
// ⚠ 判据设计上的两个坑（AGENTS.md 判据纪律）：
//  1. 「协调坏了」与「协调正常」的读数必须**不同**。所以用 refreshViewFn
//     这个缝来观测「数据库被碰过吗」，而不是只看返回值 —— 真实
//     refreshView 在 pool==nil 时会安静返回，于是「协调坏了」和
//     「协调正常」读数完全一样 ⇒ 恒绿门。
//  2. 「锁键独立性」是**值级契约**，只能断言常量不等。用源码文本搜索
//     会把注释里引用的字样也算成违规（AGENTS.md 判据纪律：模式扫描要
//     扫代码位，不是原始文本）。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/admin/distlock"
)

// tuningRefreshRecorder 记录 refreshView 被调用了哪些视图。
type tuningRefreshRecorder struct {
	mu      sync.Mutex
	views   []string
	calls   int
	failFor string
}

func (rec *tuningRefreshRecorder) fn(_ context.Context, view string) (tuningRefreshOutcome, time.Duration, error) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.calls++
	rec.views = append(rec.views, view)
	if rec.failFor != "" && view == rec.failFor {
		return tuningSkippedMissingView, 0, context.DeadlineExceeded
	}
	return tuningRefreshed, 7 * time.Millisecond, nil
}

func (rec *tuningRefreshRecorder) snapshot() (int, []string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]string, len(rec.views))
	copy(out, rec.views)
	return rec.calls, out
}

// TestTuningViewRefresher_CycleIsWallClockAligned 钉住「周期必须对齐墙钟」。
//
// 根因的一半在这里：令牌桶/advisory 锁只在两台实例**同一瞬间**醒来时才
// 收得住 2× 放大（materialized_view_refresher.go 文件头写明：锁只做
// "重叠窗口内互斥"，相位随机时去重不掉）。time.NewTicker 是进程启动
// 相位 ⇒ 两个实例永远错开 ⇒ 锁形同虚设。
//
// 变异：nextCycleWait 改成返回 r.tick（普通 ticker 周期）⇒ 落点是
// "now + 5min"，分秒仍是 33 ⇒ Unix%300 != 0 ⇒ 本门变红。
func TestTuningViewRefresher_CycleIsWallClockAligned(t *testing.T) {
	r := NewTuningViewRefresher(nil)
	// 故意取一个"分秒不整"的时间点：墙钟对齐的落点必须落在 5 分钟边界上。
	now := time.Date(2026, 10, 20, 2, 7, 33, 500_000_000, time.UTC)

	wait := r.nextCycleWait(now)
	next := now.Add(wait)

	sec := int64(TuningViewRefreshInterval / time.Second)
	require.Zero(t, next.Unix()%sec,
		"下一轮必须落在 %s 墙钟边界（Unix 纪元倍数）上，实际落点 %s",
		TuningViewRefreshInterval, next.UTC())
}

// TestTuningViewRefresher_FollowerSkipsWithoutTouchingDatabase 钉住
// 「follower 不碰数据库」。
//
// 这条是本轮修复的核心判据：2× 放大之所以存在，就是因为**两个实例都刷**。
// 协调修好之后，抢不到令牌的那台必须**一个视图都不碰**。
//
// ⚠ 判据不能只看返回值：refreshView 在 pool==nil 时会安静返回，
// 「协调坏了」和「协调正常」读数完全一样 ⇒ 恒绿门。所以用
// refreshViewFn 缝来观测「有没有真的去刷」。
//
// 变异：把 refreshCoordinated 里的 follower 早退删掉 ⇒ 被测件会继续
// 调 refreshViewFn ⇒ touched 非空 ⇒ 本门变红。
func TestTuningViewRefresher_FollowerSkipsWithoutTouchingDatabase(t *testing.T) {
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	// 外部占住令牌 ⇒ 被测 refresher 必然是 follower。
	holder := NewTuningViewRefresher(nil)
	holder.SetDistLock(mgr)
	h := holder.acquireDistLock(ctx, tuningViewDistLockLogical)
	require.NotNil(t, h, "外部占位者必须拿到令牌")
	require.True(t, h.IsLeader())
	defer h.Release(ctx)

	rec := &tuningRefreshRecorder{}
	follower := NewTuningViewRefresher(nil)
	follower.SetDistLock(mgr)
	follower.refreshViewFn = rec.fn

	before := testutil.ToFloat64(mvRefreshTotal.WithLabelValues("tuning_signals_5m", "skipped_follower"))

	require.NoError(t, follower.refreshCoordinated(ctx))

	calls, views := rec.snapshot()
	require.Zero(t, calls, "follower 绝不能去刷视图（否则 2× 放大照旧），实际刷了 %v", views)
	require.Empty(t, views)

	after := testutil.ToFloat64(mvRefreshTotal.WithLabelValues("tuning_signals_5m", "skipped_follower"))
	require.Greater(t, after, before,
		"follower 跳过必须落到 gateway_mv_refresh_total{view=tuning_signals_5m,outcome=skipped_follower} 上 —— "+
			"否则协调效果没有尺子（这正是 R51 发现 2× 却无从量化的原因）")
}

// TestTuningViewRefresher_LeaderRefreshesBothViews 钉住「leader 刷全部视图」
// 并顺带证明 tuning 视图真的进了指标族闭集。
//
// 变异：把 refreshCoordinated 的 leader 分支改成只刷第一个视图 ⇒ 本门红。
func TestTuningViewRefresher_LeaderRefreshesBothViews(t *testing.T) {
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	rec := &tuningRefreshRecorder{}
	leader := NewTuningViewRefresher(nil)
	leader.SetDistLock(mgr)
	leader.refreshViewFn = rec.fn

	before := testutil.ToFloat64(mvRefreshTotal.WithLabelValues("tuning_signals_5m", "success"))
	beforeDaily := testutil.ToFloat64(mvRefreshTotal.WithLabelValues("tuning_signals_daily", "success"))

	require.NoError(t, leader.refreshCoordinated(ctx))

	calls, views := rec.snapshot()
	require.Equal(t, len(TuningViews), calls, "leader 必须把两个视图都刷一遍，实际 %v", views)
	require.ElementsMatch(t, TuningViews, views)

	require.Greater(t, testutil.ToFloat64(mvRefreshTotal.WithLabelValues("tuning_signals_5m", "success")), before,
		"tuning_signals_5m 必须出现在 gateway_mv_refresh_total 的 view 标签闭集里")
	require.Greater(t, testutil.ToFloat64(mvRefreshTotal.WithLabelValues("tuning_signals_daily", "success")), beforeDaily,
		"tuning_signals_daily 必须出现在 gateway_mv_refresh_total 的 view 标签闭集里")
}

// TestTuningViewRefresher_RefreshFailureIsVisible 钉住「刷新失败要能被看见」。
//
// 变异：把 err 分支的 recordMVRefreshFailure 删掉 ⇒ 本门红。
func TestTuningViewRefresher_RefreshFailureIsVisible(t *testing.T) {
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	rec := &tuningRefreshRecorder{failFor: "tuning_signals_5m"}
	r := NewTuningViewRefresher(nil)
	r.SetDistLock(mgr)
	r.refreshViewFn = rec.fn

	before := testutil.ToFloat64(mvRefreshTotal.WithLabelValues("tuning_signals_5m", "failed"))

	err := r.refreshCoordinated(ctx)
	require.Error(t, err, "单视图失败必须让整轮返回错误，不能被静默吞掉")

	require.Greater(t, testutil.ToFloat64(mvRefreshTotal.WithLabelValues("tuning_signals_5m", "failed")), before,
		"刷新失败必须落到 gateway_mv_refresh_total{outcome=failed} 上")

	// 失败不应阻断另一个视图（两个视图用途独立）。
	_, views := rec.snapshot()
	require.Contains(t, views, "tuning_signals_daily",
		"一个视图失败不该作废另一个视图的刷新")
}

// TestTuningViewRefresher_LockKeysIndependentFromMaterializedRefresher
// 钉住「tuning 的锁键必须与 routing/session 的锁键独立」。
//
// 这是**值级契约**，不是行为级：tuning 周期 5min 与 routing 周期 30min、
// session 周期 60min 互为整数倍，且三者都对齐墙钟 ⇒ 每个 30min/60min
// 边界必然撞在同一把锁上。2026-10-12 已实测过这个坑：session 那一轮
// 整轮 skipped_no_lock，MV 数据陈旧 2 小时。
//
// 变异：把 tuningViewAdvisoryLockKey 改成 mvRefreshLockKey ⇒ 本门红。
func TestTuningViewRefresher_LockKeysIndependentFromMaterializedRefresher(t *testing.T) {
	require.NotEqual(t, mvRefreshLockKey, tuningViewAdvisoryLockKey,
		"tuning 的 advisory 锁键必须与 routing/session 的 mvRefreshLockKey 独立（5min/30min/60min 三个周期都是墙钟对齐的整数倍，撞锁必然发生）")
	require.NotEqual(t, mvRefreshDistLockLogical, tuningViewDistLockLogical,
		"Redis 令牌桶的 logical 名必须与 routing 周期不同（不同节奏的周期共用一个 token 会互相抢票）")
	require.NotEqual(t, sessionViewsDistLockLogical, tuningViewDistLockLogical,
		"Redis 令牌桶的 logical 名必须与 session 周期不同（同上）")
}

// TestTuningViewRefresher_TokenTTLDoesNotSpanWindows 钉住「令牌 TTL 不得跨窗口」。
//
// 变异：把 tuningViewDistLockTTL 改成 10min（> 5min 窗口）⇒ 本门红。
func TestTuningViewRefresher_TokenTTLDoesNotSpanWindows(t *testing.T) {
	require.Less(t, tuningViewDistLockTTL, TuningViewRefreshInterval,
		"令牌 TTL 跨窗口会把下一窗口的刷新也顶掉 ⇒ 视图陈旧度翻倍，且从指标上看不出来")
	require.GreaterOrEqual(t, tuningViewDistLockTTL, 30*time.Second,
		"TTL 必须够覆盖两台实例在同一瞬间的抢票窗口（Redis SETNX 是原子操作，毫秒级）")
}

// TestTuningViewRefresher_ReleaseFreesTokenForNextWindow 钉住「上一轮释放后，
// 下一窗口必须能重新选主」。
//
// 变异：把 Release 去掉 / 或者 acquire 路径改成常驻 ⇒ 本门红。
func TestTuningViewRefresher_ReleaseFreesTokenForNextWindow(t *testing.T) {
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	r1 := NewTuningViewRefresher(nil)
	r1.SetDistLock(mgr)
	h1 := r1.acquireDistLock(ctx, tuningViewDistLockLogical)
	require.NotNil(t, h1)
	require.True(t, h1.IsLeader())
	h1.Release(ctx)

	r2 := NewTuningViewRefresher(nil)
	r2.SetDistLock(mgr)
	h2 := r2.acquireDistLock(ctx, tuningViewDistLockLogical)
	require.NotNil(t, h2)
	defer h2.Release(ctx)
	require.True(t, h2.IsLeader(),
		"上一轮释放后，下一窗口必须能重新选主 —— 否则第一个赢家会把令牌一直占着，视图永远不再刷新")
}

// TestTuningViewRefresher_CycleReleasesToken 钉住「一轮结束后必须释放令牌」。
//
// 这是 token-bucket 不卡死的关键：若 refreshCoordinated 忘了 Release，
// 第一个赢家会**永远**占着令牌 ⇒ 视图此后再也不刷新（而且从指标上看
// 只会有 skipped_follower 一直涨，像是"协调在工作"）。
//
// ⚠ 上一条 TestTuningViewRefresher_ReleaseFreesTokenForNextWindow 测的是
// acquireDistLock 这个原语的手工释放，**测不到周期层有没有真的释放**。
// 这条测的正是周期层。
//
// 变异：把 refreshCoordinated 里的 `defer handle.Release(...)` 去掉
// ⇒ 第二轮被当成 follower ⇒ 刷新次数不涨 ⇒ 本门红。
func TestTuningViewRefresher_CycleReleasesToken(t *testing.T) {
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	rec := &tuningRefreshRecorder{}
	r1 := NewTuningViewRefresher(nil)
	r1.SetDistLock(mgr)
	r1.refreshViewFn = rec.fn
	require.NoError(t, r1.refreshCoordinated(ctx))

	// 第二台实例必须能在下一个窗口拿到令牌并真的刷新。
	r2 := NewTuningViewRefresher(nil)
	r2.SetDistLock(mgr)
	r2.refreshViewFn = rec.fn
	require.NoError(t, r2.refreshCoordinated(ctx))

	calls, _ := rec.snapshot()
	require.GreaterOrEqual(t, calls, 2*len(TuningViews),
		"上一轮没有释放令牌，第二轮被当成 follower 跳过了（视图会永远不再刷新）")
}

// TestTuningViewRefresher_NoDistLockFallsBackToAdvisory 钉住「没接 Redis 时
// 退到 advisory lock 而不是不刷新」。
//
// 变异：把 acquireDistLock 改成直接 return nil 之外的硬失败，或把
// follower 早退改成"没令牌就不刷新" ⇒ 本门红（此时无令牌也无 advisory
// 兜底，视图会永远不刷新）。
func TestTuningViewRefresher_NoDistLockFallsBackToAdvisory(t *testing.T) {
	r := NewTuningViewRefresher(nil)
	require.Nil(t, r.acquireDistLock(context.Background(), tuningViewDistLockLogical),
		"没接 distlock 时必须返回 nil handle，让调用方退到 Postgres advisory lock")

	// 接了但 Enabled()==false 的 manager（没有 Redis 的部署）同样退到 advisory。
	r2 := NewTuningViewRefresher(nil)
	r2.SetDistLock(distlock.NewRedisManager(nil))
	require.Nil(t, r2.acquireDistLock(context.Background(), tuningViewDistLockLogical),
		"disabled 的 distlock.Manager 必须返回 nil handle，让调用方退到 advisory lock")
}

// TestTuningViewRefresher_RefreshRejectsNonWhitelistedView 钉住白名单。
//
// REFRESH 语句必须把视图名拼进 SQL（PG 不支持参数化视图名）⇒ 白名单是
// 唯一的防线。
//
// 变异：把白名单校验删掉 ⇒ 本门红。
func TestTuningViewRefresher_RefreshRejectsNonWhitelistedView(t *testing.T) {
	r := NewTuningViewRefresher(nil)

	outcome, _, err := r.refreshView(context.Background(), "pg_class")
	require.Error(t, err, "非白名单视图名必须在拼 SQL 之前被拒")
	require.NotEqual(t, tuningRefreshed, outcome)

	require.False(t, isTuningView("pg_class"))
	require.False(t, isTuningView(""))
	require.True(t, isTuningView("tuning_signals_5m"))
	require.True(t, isTuningView("tuning_signals_daily"))
}
