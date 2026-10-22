package bg

import (
	"os"
	"strings"
	"testing"
	"time"
)

// ── session 族 REFRESH 被 180s 语句上限杀掉（2026-10-22）────────────────────
//
// 缺陷本体（生产实测，不是推断）：
//   252 共享库上 `REFRESH MATERIALIZED VIEW CONCURRENTLY session_client_stats`
//   **零锁等待**跑了 180.31s，被 `mvRefreshStatementTimeout = "180s"` 杀掉，
//   报错原文 `ERROR: canceling statement due to statement timeout (SQLSTATE 57014)`。
//   ⇒ 视图的 refreshed_at 不前进 ⇒ 超过 90min 新鲜度预算 ⇒
//     `/api/admin/session-analytics/clients` 持续返回 503 `analytics_view_stale`。
//
// 这道门钉住四处修复各自的**承重判据**，缺一处就红：
//   ① 分族语句上限（session 族有自己的值，且**真的**被 refreshView 用上）
//   ② 轮内 ctx 必须**大于**分族上限（否则抬钉子被 ctx 掐断 ⇒ 白抬）
//   ③ 视图顺序：快的先刷（限制慢视图的爆炸半径）
//   ④ 相位错开：session 周期不再与 routing 周期同刻抢同一把 advisory lock
//
// ★ 这道门**不**钉「另开一把 advisory 锁键」——本文件历史上明确否掉过
//   （另开锁会让 routing 与 session 的 REFRESH 并发跑，而 session 一轮已经占掉
//   集群 62%~75% 的 WAL）⇒ 门里有一条**反向**判据守住「没有第二把锁」。

const mvRefresherSrcPath = "materialized_view_refresher.go"

func mvRefresherSourceForTest(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(mvRefresherSrcPath)
	if err != nil {
		t.Fatalf("读源文件 %s：%v", mvRefresherSrcPath, err)
	}
	return string(b)
}

// mvFuncBodyForSession extracts a function body by its signature prefix,
// stopping at the first line that is exactly "}" at column 0.
func mvFuncBodyForSession(t *testing.T, src, sig string) string {
	t.Helper()
	at := strings.Index(src, sig)
	if at < 0 {
		t.Fatalf("没找到 %s", sig)
	}
	end := strings.Index(src[at:], "\n}\n")
	if end < 0 {
		t.Fatalf("%s 抽取失败", sig)
	}
	return src[at : at+end]
}

// TestSessionViewsStatementTimeout_ExceedsTheObservedKillLine：分族上限必须
// 明显高于实测的杀线。
//
// 实测杀线是 180.31s（245 03:00 轮，零锁等待）。取值 600s（与仓内 §14330
// 巡检脚本的 180s→600s 同一处置路径）。
func TestSessionViewsStatementTimeout_ExceedsTheObservedKillLine(t *testing.T) {
	got, err := time.ParseDuration(sessionViewsStatementTimeout)
	if err != nil {
		t.Fatalf("sessionViewsStatementTimeout=%q 解析失败：%v", sessionViewsStatementTimeout, err)
	}
	const observedKillLine = 180 * time.Second
	if got <= observedKillLine {
		t.Fatalf("session 族语句上限 %s 必须大于实测杀线 %s —— 否则 REFRESH 还是会被自己的上限砍掉",
			got, observedKillLine)
	}
	// 余量至少 1.5×：实测尾部不可测（被杀的运行不进 pg_stat_statements），
	// 余量太小等于把「刚好够」当「够」。
	if got < observedKillLine*3/2 {
		t.Fatalf("session 族语句上限 %s 对实测杀线 %s 的余量不足 1.5× —— 实测尾部不可测，不能贴着杀线取值",
			got, observedKillLine)
	}
}

// TestSessionViewsStatementTimeout_DiffersFromRoutingFamily：分族必须真的分族。
//
// 若两者相等，说明「分族」是摆设 —— 慢视图仍会被共用值砍掉。
func TestSessionViewsStatementTimeout_DiffersFromRoutingFamily(t *testing.T) {
	if sessionViewsStatementTimeout == mvRefreshStatementTimeout {
		t.Fatalf("session 族与 routing 族共用同一个语句上限 %q —— 分族修复没有生效",
			mvRefreshStatementTimeout)
	}
}

// TestRefreshView_UsesThePassedTimeoutNotTheGlobalConstant：refreshView 必须
// **用参数**去 SET statement_timeout。
//
// ★ 这是最容易出现的「假修复」：加了参数、却仍然写死常量 ⇒ 调用点传什么都没用。
func TestRefreshView_UsesThePassedTimeoutNotTheGlobalConstant(t *testing.T) {
	src := mvRefresherSourceForTest(t)
	body := mvFuncBodyForSession(t, src, "func (r *MaterializedViewRefresher) refreshView(")

	if !strings.Contains(body, "SET statement_timeout = '\"+statementTimeout+\"'") {
		t.Fatal("refreshView 没有用传入的 statementTimeout 去 SET —— 参数是摆设")
	}
	if strings.Contains(body, "SET statement_timeout = '\"+mvRefreshStatementTimeout+\"'") {
		t.Fatal("refreshView 仍然写死 routing 族的常量 —— session 族会被共用值砍掉")
	}
	if !strings.Contains(body, "RESET statement_timeout") {
		t.Fatal("refreshView 没有 RESET statement_timeout —— 归还连接池后会污染其他语句")
	}
}

// TestRefreshCallSites_PassTheRightFamilyTimeout：两个族的调用点必须各传各的。
func TestRefreshCallSites_PassTheRightFamilyTimeout(t *testing.T) {
	src := mvRefresherSourceForTest(t)

	routingBody := mvFuncBodyForSession(t, src, "func (r *MaterializedViewRefresher) refreshAll(")
	if !strings.Contains(routingBody, `"routing_analytics_7d", useAdvisoryLock, mvRefreshStatementTimeout`) {
		t.Fatal("refreshAll 没有给 routing_analytics_7d 传 mvRefreshStatementTimeout")
	}
	if !strings.Contains(routingBody, `"routing_audit_summary_7d", useAdvisoryLock, mvRefreshStatementTimeout`) {
		t.Fatal("refreshAll 没有给 routing_audit_summary_7d 传 mvRefreshStatementTimeout")
	}

	sessionBody := mvFuncBodyForSession(t, src, "func (r *MaterializedViewRefresher) refreshSessionViews(")
	if !strings.Contains(sessionBody, "r.refreshView(ctx, view, true, sessionViewsStatementTimeout)") {
		t.Fatal("refreshSessionViews 没有给 session 族传 sessionViewsStatementTimeout")
	}
}

// TestSessionViewsTimeout_ExceedsTheFamilyStatementTimeout：轮内 ctx 必须**大于**
// 分族语句上限。
//
// ★ 这是最承重的一条：语句上限是 600s，而轮内 ctx 若只有 600s 或更小，
//   ctx 会先掐断 ⇒ 抬钉子等于白抬（而且失败形态从「被杀」变成「整轮被砍」，
//   后两个视图根本没刷新 —— 比原来更难发现）。
func TestSessionViewsTimeout_ExceedsTheFamilyStatementTimeout(t *testing.T) {
	pin, err := time.ParseDuration(sessionViewsStatementTimeout)
	if err != nil {
		t.Fatalf("解析 sessionViewsStatementTimeout：%v", err)
	}
	if SessionViewsTimeout <= pin {
		t.Fatalf("轮内 ctx %s 必须大于 session 族语句上限 %s —— ctx 先掐断的话，抬钉子没有意义",
			SessionViewsTimeout, pin)
	}
	// 仍必须小于周期，否则刷新周期会堆积（既有契约）。
	if SessionViewsTimeout >= SessionViewsInterval {
		t.Fatalf("轮内 ctx %s 必须小于周期 %s，否则刷新周期会堆积",
			SessionViewsTimeout, SessionViewsInterval)
	}
}

// TestSessionViewsTimeout_CoversLockBudgetPlusStatements
//
// ★★ R107 轮（12h 审计）补的一道**承重**门。
//
// 上一条门（TestSessionViewsTimeout_ExceedsTheFamilyStatementTimeout）只比了
// 「轮内 ctx vs 分族语句上限」——**漏掉了锁等待预算**。而锁等待是**加在语句之前**
// 的，不包含在语句上限里：
//
//	一轮的最坏耗时 = SessionViewLockRetryBudget（整轮共享，≤4min）
//	               + 各视图的语句耗时
//
// ⇒ 「ctx(15min=900s) > 语句上限(600s)」成立，**并不**代表 ctx 装得下一轮：
//	240s(锁) + 600s(client_stats) + 182.33s(task_stats) + 65.40s(matrix)
//	= 1087.7s > 900s
// ⇒ 也就是说，**抬到 600s 之后，ctx 仍可能是先掐断的那道闸**——
// 而 ctx 掐断的表现（后几个视图根本没刷新 + 半轮状态）正是本文件头部
// 注释里说的「比整体失败更难发现」的那种形态。
//
// ⇒ 这道门把**完整**的不变量钉成机器可复核的判据：
//	SessionViewsTimeout ≥ SessionViewLockRetryBudget
//	                     + sessionViewsStatementTimeout
//	                     + sessionViewsOtherViewsBudget
func TestSessionViewsTimeout_CoversLockBudgetPlusStatements(t *testing.T) {
	pin, err := time.ParseDuration(sessionViewsStatementTimeout)
	if err != nil {
		t.Fatalf("解析 sessionViewsStatementTimeout：%v", err)
	}
	required := SessionViewLockRetryBudget + pin + sessionViewsOtherViewsBudget
	if SessionViewsTimeout < required {
		t.Fatalf("轮内 ctx %s 装不下「锁等待预算 %s + 语句上限 %s + 其余视图预算 %s」= %s —— "+
			"ctx 会先掐断 ⇒ 抬语句钉子等于白抬（且失败形态是「后几个视图根本没刷新」的半轮状态，"+
			"比整体失败更难发现）",
			SessionViewsTimeout, SessionViewLockRetryBudget, pin,
			sessionViewsOtherViewsBudget, required)
	}
	// 留余量：至少 5%，避免「刚好卡死线上」这种临界值。
	minMargin := required / 20
	if SessionViewsTimeout-required < minMargin {
		t.Fatalf("轮内 ctx %s 相对必需的 %s 余量 %s 小于 5%%（%s）—— 临界值没有工程意义",
			SessionViewsTimeout, required, SessionViewsTimeout-required, minMargin)
	}
	// 仍必须小于周期（既有契约，重复钉一次以防有人只改上面两处）。
	if SessionViewsTimeout >= SessionViewsInterval {
		t.Fatalf("轮内 ctx %s 必须小于周期 %s，否则刷新周期会堆积",
			SessionViewsTimeout, SessionViewsInterval)
	}
	// ★ 族间饥饿护栏：session 轮错开到 :05，routing 轮在 :00/:30。
	//   ⇒ ctx 必须让 session 轮在 :30 之前跑完，否则 routing 轮被推迟。
	//   ⇒ 可用窗口 = 30min - sessionViewsPhaseOffset。
	available := RefreshInterval - sessionViewsPhaseOffset
	if SessionViewsTimeout >= available {
		t.Fatalf("轮内 ctx %s 必须小于「%s 到下一个 routing tick」的窗口 %s —— "+
			"否则 session 轮会把 routing 轮推迟（相位错开的收益被抵消）",
			SessionViewsTimeout, RefreshInterval, available)
	}
}

// TestSessionAnalyticsViews_FastestFirst：慢视图必须排在最后。
//
// 实测成本（252 生产 2026-10-22）：matrix 7.45~65.4s、task_stats 6.19~151.5s、
// client_stats 34.28s 且**≥180.31s（被杀）**。一轮是串行的 ⇒ 慢视图排第一
// 会把整轮推迟它自己的耗时，后面的视图因此被推到离新鲜度预算只剩 28 分钟。
func TestSessionAnalyticsViews_FastestFirst(t *testing.T) {
	if len(SessionAnalyticsViews) != 3 {
		t.Fatalf("视图数应为 3，实际 %d：%v", len(SessionAnalyticsViews), SessionAnalyticsViews)
	}
	if SessionAnalyticsViews[0] != "session_client_task_matrix" {
		t.Fatalf("最慢的视图必须排在最后，当前清单首项是 %q：%v",
			SessionAnalyticsViews[0], SessionAnalyticsViews)
	}
	if SessionAnalyticsViews[len(SessionAnalyticsViews)-1] != "session_client_stats" {
		t.Fatalf("session_client_stats（实测 ≥180.31s）必须排在最后，当前清单末项是 %q：%v",
			SessionAnalyticsViews[len(SessionAnalyticsViews)-1], SessionAnalyticsViews)
	}
}

// TestSessionViewsPhaseOffset_IsNonZeroAndInsideTheInterval：相位错开量必须
// 落在 (0, 周期) 内 —— 0 等于没错开，≥周期 等于没定义。
func TestSessionViewsPhaseOffset_IsNonZeroAndInsideTheInterval(t *testing.T) {
	if sessionViewsPhaseOffset <= 0 {
		t.Fatalf("sessionViewsPhaseOffset=%s 必须为正 —— 0 等于没有错开", sessionViewsPhaseOffset)
	}
	if sessionViewsPhaseOffset >= SessionViewsInterval {
		t.Fatalf("sessionViewsPhaseOffset=%s 必须小于周期 %s", sessionViewsPhaseOffset, SessionViewsInterval)
	}
}

// TestRefreshLoop_UsesTheOffsetPhase：refreshLoop 必须用带相位的等待函数。
//
// 只定义常量而没接上，等于没错开 —— 这条判据把「常量」与「接线」分开钉。
func TestRefreshLoop_UsesTheOffsetPhase(t *testing.T) {
	src := mvRefresherSourceForTest(t)
	body := mvFuncBodyForSession(t, src, "func (r *MaterializedViewRefresher) refreshLoop(")
	if !strings.Contains(body, "sessionViewsTickWait(") {
		t.Fatal("refreshLoop 没有用 sessionViewsTickWait —— 相位错开没有接线")
	}
	if strings.Contains(body, "nextAlignedWait(time.Now(), SessionViewsInterval)") {
		t.Fatal("refreshLoop 仍直接用整点对齐的 SessionViewsInterval —— 与 routing 周期同刻撞锁")
	}
}

// TestNextAlignedWaitAt_LandsOnTheOffset：错开等待函数的行为判据。
//
// 三条都要覆盖：目标点之前 → 返回差额；目标点之后 → 返回一整个周期（不返回
// 零长等待）；offset ≥ interval → 取模归一。
func TestNextAlignedWaitAt_LandsOnTheOffset(t *testing.T) {
	const interval = time.Hour
	const offset = 5 * time.Minute

	// 取一个相位明确在 offset 之前的时刻：Unix epoch + 3min。
	before := time.Unix(0, 0).Add(3 * time.Minute)
	got := nextAlignedWaitAt(before, interval, offset)
	want := 2 * time.Minute
	if got != want {
		t.Fatalf("相位之前：期望等 %s，实际 %s", want, got)
	}

	// 相位之后：必须等一整个周期，而不是零长等待。
	after := time.Unix(0, 0).Add(7 * time.Minute)
	got = nextAlignedWaitAt(after, interval, offset)
	wantAfter := 58 * time.Minute
	if got != wantAfter {
		t.Fatalf("相位之后：期望等 %s（一整个周期），实际 %s", wantAfter, got)
	}

	// 恰好落在目标相位上：必须等一整个周期（不能返回 0 ⇒ 忙循环）。
	exact := time.Unix(0, 0).Add(5 * time.Minute)
	got = nextAlignedWaitAt(exact, interval, offset)
	if got != interval {
		t.Fatalf("恰好落在相位上：期望等 %s，实际 %s", interval, got)
	}

	// offset ≥ interval：取模归一，行为必须与 offset=5min 相同。
	got = nextAlignedWaitAt(before, interval, offset+interval)
	if got != want {
		t.Fatalf("offset 越界未归一：期望 %s，实际 %s", want, got)
	}
}

// TestSessionViewsTickWait_KeepsAGapAfterTheStartupRound：启动后的首轮刷新与
// 第一个对齐 tick 之间必须留出间隔。
//
// 错开相位带来的新形态：进程若在 :03 启动，首轮在 :05、对齐 tick 也在 :05
// ⇒ 两轮只隔 2 分钟，等于把一轮几百秒的 REFRESH 重复做一遍。
func TestSessionViewsTickWait_KeepsAGapAfterTheStartupRound(t *testing.T) {
	// Unix epoch + 3min ⇒ 对齐点在 2 分钟后，间隔不足 ⇒ 必须推到一整个周期后。
	now := time.Unix(0, 0).Add(3 * time.Minute)
	got := sessionViewsTickWait(now)
	if got < sessionViewsMinTickGap {
		t.Fatalf("对齐 tick 距上一轮只有 %s，低于最小间隔 %s ⇒ 会连做两轮重活",
			got, sessionViewsMinTickGap)
	}

	// 距上一轮已经很久时，不必推迟：间隔应等于纯相位等待。
	now = time.Unix(0, 0).Add(30 * time.Minute)
	got = sessionViewsTickWait(now)
	if got != 35*time.Minute {
		t.Fatalf("间隔充足时不应推迟：期望 %s，实际 %s", 35*time.Minute, got)
	}
}

// TestNoSecondAdvisoryLockKey：守「没有第二把 advisory 锁」。
//
// ★ 这是一条**反向**判据。历史上有人想过后给 session 族单开一把锁键，本文件
//   明确否掉过：那会让 routing 与 session 的 REFRESH 并发跑，而 session 一轮
//   已经占掉集群 62%~75% 的 WAL。⇒ 相位错开是零并发的解法，必须保持唯一解。
func TestNoSecondAdvisoryLockKey(t *testing.T) {
	src := mvRefresherSourceForTest(t)

	// advisory 锁的获取点必须只有一处，且键名仍是 mvRefreshLockKey。
	body := mvFuncBodyForSession(t, src, "func (r *MaterializedViewRefresher) refreshView(")
	n := strings.Count(body, "pg_try_advisory_lock($1)")
	if n != 1 {
		t.Fatalf("refreshView 里 pg_try_advisory_lock 出现 %d 次（应为 1）—— 出现第二处意味着有人另开了一把锁", n)
	}
	if !strings.Contains(body, "pg_try_advisory_lock($1)`, mvRefreshLockKey") {
		t.Fatal("refreshView 的 advisory 锁键不是 mvRefreshLockKey")
	}
	if strings.Contains(body, "mvRefreshLockKey, sessionViews") ||
		strings.Contains(body, "sessionViewsAdvisoryLockKey") {
		t.Fatal("session 族另开了一把 advisory 锁 ⇒ 会与 routing 族并发 REFRESH（已被否掉的方案）")
	}
}
