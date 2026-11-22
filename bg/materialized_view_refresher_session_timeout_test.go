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
//   ② 单视图预算必须**大于**分族上限（否则抬钉子被 ctx 掐断 ⇒ 白抬）
//      ——2026-11-22 起预算从「整轮共享 ctx」改成「每视图一份」
//      （252 实测 981 族把整轮 20min 吃光、尾部视图被饿死），
//      钉的对象跟着换，判据形状不变
//   ③ 视图顺序：快的先刷（按 252 实测 mean 成本，2026-11-22 重排）
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
	// ★ 2026-10-29（R175）：refreshView 的会话画像扩成三轴（statement_timeout /
	//   work_mem / jit）⇒ 调用点的实参个数变了 ⇒ 判据从「整串带右括号」
	//   放宽为「实参序列以 sessionViewsStatementTimeout 开头」。
	//   意图不变（意图是「session 族传自己的超时」），且仍然排除了
	//   `r.refreshView(ctx, view, true)` 这种不带任何分族值的裸调用。
	if !strings.Contains(sessionBody, "r.refreshView(ctx, view, true, sessionViewsStatementTimeout,") {
		t.Fatal("refreshSessionViews 没有给 session 族传 sessionViewsStatementTimeout")
	}
	// 反向判据：不能把 routing 族的超时传给 session 族。
	if strings.Contains(sessionBody, "mvRefreshStatementTimeout") {
		t.Fatal("refreshSessionViews 把 routing 族的语句上限传给了 session 族")
	}
}

// TestSessionViewRefreshBudget_ExceedsTheFamilyStatementTimeout：单视图预算必须
// **大于**分族语句上限。
//
// ★ 这是最承重的一条：语句上限是 600s，而单视图预算若只有 600s 或更小，
//
//	ctx 会先掐断 ⇒ 抬钉子等于白抬（而且失败形态从「被杀」变成「预算死亡」，
//	该视图本轮没刷新 —— 比原来更难发现）。
//
// ★ 2026-11-22 从「轮内 ctx」改钉「单视图预算」：整轮共享 ctx 已废除
//
//	（252 实测 981 族把 20min 吃光、尾部视图被饿死，见
//	 SessionViewRefreshBudget 的注释）。钉的对象跟着换，判据不变。
func TestSessionViewRefreshBudget_ExceedsTheFamilyStatementTimeout(t *testing.T) {
	pin, err := time.ParseDuration(sessionViewsStatementTimeout)
	if err != nil {
		t.Fatalf("解析 sessionViewsStatementTimeout：%v", err)
	}
	if SessionViewRefreshBudget <= pin {
		t.Fatalf("单视图预算 %s 必须大于 session 族语句上限 %s —— ctx 先掐断的话，抬钉子没有意义",
			SessionViewRefreshBudget, pin)
	}
	// 仍必须小于周期（既有契约）：预算死亡 = 该视图本轮失败、下一轮重来，
	// 单视图预算超过周期没有意义。
	if SessionViewRefreshBudget >= SessionViewsInterval {
		t.Fatalf("单视图预算 %s 必须小于周期 %s", SessionViewRefreshBudget, SessionViewsInterval)
	}
}

// TestSessionViewRefreshBudget_CoversLockBudgetPlusStatement
//
// ★★ R107 轮（12h 审计）补的**承重**门，2026-11-22 随预算制改造换对象。
//
// 锁等待是**加在语句之前**的，不包含在语句上限里。单视图的最坏耗时：
//
//	SessionViewLockRetryBudget（锁等待）+ sessionViewsStatementTimeout（语句）
//
// ⇒ 预算必须**完整装下**这两段，否则最坏路径上 ctx 先掐断，
//
//	  抬语句钉子等于白抬：
//
//		2026-10-22 旧账：240s + 600s = 840s > 15min(900s)？否 —— 840s ≤ 900s ✔
//		（当时钉的是「整轮 20min ≥ 240 + 600 + 其余两视图 247.7s」= 1087.7s，
//		 那个「其余视图」项随 981 族入列失去意义：7 张视图的最坏和 43min
//		 任何整轮 ctx 都装不下 ⇒ 结构性修法是每视图一份预算，而不是继续抬轮。）
//
// ⇒ 这道门把**新的**完整不变量钉成机器可复核的判据：
//
//	SessionViewRefreshBudget ≥ SessionViewLockRetryBudget
//	                         + sessionViewsStatementTimeout
func TestSessionViewRefreshBudget_CoversLockBudgetPlusStatement(t *testing.T) {
	pin, err := time.ParseDuration(sessionViewsStatementTimeout)
	if err != nil {
		t.Fatalf("解析 sessionViewsStatementTimeout：%v", err)
	}
	required := SessionViewLockRetryBudget + pin
	if SessionViewRefreshBudget < required {
		t.Fatalf("单视图预算 %s 装不下「锁等待预算 %s + 语句上限 %s」= %s —— "+
			"最坏路径上 ctx 会先掐断 ⇒ 抬语句钉子等于白抬",
			SessionViewRefreshBudget, SessionViewLockRetryBudget, pin, required)
	}
	// 留余量：至少 5%，避免「刚好卡死线上」这种临界值。
	minMargin := required / 20
	if SessionViewRefreshBudget-required < minMargin {
		t.Fatalf("单视图预算 %s 相对必需的 %s 余量 %s 小于 5%%（%s）—— 临界值没有工程意义",
			SessionViewRefreshBudget, required, SessionViewRefreshBudget-required, minMargin)
	}
	// ★ 族间饥饿护栏（换算成单视图形态）：session 轮错开到 :05，routing 轮在
	//   :00/:30 ⇒ **单张视图**的预算必须小于「:05 到 :30」的窗口，否则一张
	//   慢视图就能把 routing tick 推迟。（整轮可以更长——那是病态场景下的
	//   可恢复延迟，见 SessionViewRefreshBudget 注释里的取舍；单视图不行。）
	available := RefreshInterval - sessionViewsPhaseOffset
	if SessionViewRefreshBudget >= available {
		t.Fatalf("单视图预算 %s 必须小于「%s 到下一个 routing tick」的窗口 %s —— "+
			"否则一张慢视图就会推迟 routing 轮（相位错开的收益被抵消）",
			SessionViewRefreshBudget, RefreshInterval, available)
	}
	// 仍必须小于周期（既有契约，重复钉一次以防有人只改上面几处）。
	if SessionViewRefreshBudget >= SessionViewsInterval {
		t.Fatalf("单视图预算 %s 必须小于周期 %s", SessionViewRefreshBudget, SessionViewsInterval)
	}
}

// TestSessionAnalyticsViews_FastestFirst：快的排前面、慢的排最后。
//
// ★ 2026-11-22 依据 252 生产 pg_stat_statements 的 mean 成本重排
//
//	  （338~64 次完成样本；max 列供参考，被杀的运行不进 pgss ⇒ 尾部被低估）：
//
//		session_client_stats        mean  3.4s（357）
//		session_task_stats          mean  9.3s（357）
//		session_client_task_matrix  mean 102.6s（357，串行实测 117.6s）
//		agent_usage_aggregates      mean 134.1s（981，串行）
//		project_task_aggregates     mean 164.8s（981，串行）
//		model_usage_aggregates      mean 167.2s（981，串行）
//		user_session_aggregates     mean 265.9s（981，串行）
//
// ★ 2026-10-22 版的排序（981 排最前）依据的「981 族 0.6~3.5s」来自 211 真库，
//
//	在 252 的 1955MB 源表上不成立 ⇒ 那份排序实际把最慢的四个排在了最前。
//	每视图预算制下顺序影响新鲜度年龄分布（末项年龄 = 间隔 + 整轮总时长，
//	与顺序无关；SJF 让更多视图拿到更小年龄）⇒ 按 mean 升序钉死。
func TestSessionAnalyticsViews_FastestFirst(t *testing.T) {
	if len(SessionAnalyticsViews) < 2 {
		t.Fatalf("清单为空：%v", SessionAnalyticsViews)
	}
	// 首项必须是实测最快的 session_client_stats（252 mean 3.4s）。
	if SessionAnalyticsViews[0] != "session_client_stats" {
		t.Fatalf("实测最快的 session_client_stats（252 mean 3.4s）必须排在最前，当前清单首项是 %q：%v",
			SessionAnalyticsViews[0], SessionAnalyticsViews)
	}
	// 末项必须是实测最慢的 user_session_aggregates（252 mean 265.9s / max 577.8s）。
	if SessionAnalyticsViews[len(SessionAnalyticsViews)-1] != "user_session_aggregates" {
		t.Fatalf("user_session_aggregates（252 mean 265.9s）必须排在最后，当前清单末项是 %q：%v",
			SessionAnalyticsViews[len(SessionAnalyticsViews)-1], SessionAnalyticsViews)
	}
	// matrix 必须在前三（owner 报障视图：排在前面让它即便在慢轮里也能尽早落戳）。
	matrixAt := -1
	for i, v := range SessionAnalyticsViews {
		if v == "session_client_task_matrix" {
			matrixAt = i
		}
	}
	if matrixAt < 0 || matrixAt > 2 {
		t.Fatalf("session_client_task_matrix 应排在前三位（当前位次 %d）—— 它是 2026-11-22 报障链路的视图，"+
			"排前面让它在慢轮里也尽早拿到新戳：%v", matrixAt, SessionAnalyticsViews)
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
//
//	明确否掉过：那会让 routing 与 session 的 REFRESH 并发跑，而 session 一轮
//	已经占掉集群 62%~75% 的 WAL。⇒ 相位错开是零并发的解法，必须保持唯一解。
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

// TestRefreshSessionViews_PerViewBudgetInsideLoop：每视图预算的**接线**钉测。
//
// ★ 2026-11-22 预算制改造的承重形态有两条，缺一即回到饿死时代：
//
//	① ctx 必须在**视图循环体内**按 SessionViewRefreshBudget 构造
//	  （循环外构造 = 回到整轮共享 = 尾部视图被饿死）；
//	② 旧常量 SessionViewsTimeout 不得复活（它的语义就是整轮共享，
//	  只要它还存在就有人会把它接回去）。
func TestRefreshSessionViews_PerViewBudgetInsideLoop(t *testing.T) {
	src := mvRefresherSourceForTest(t)
	body := mvFuncBodyForSession(t, src, "func (r *MaterializedViewRefresher) refreshSessionViews(")

	deadlineAt := strings.Index(body, "retryDeadline := time.Now().Add(")
	loopAt := strings.Index(body, "for _, view := range SessionAnalyticsViews")
	if loopAt < 0 {
		t.Fatal("refreshSessionViews 里没有视图循环")
	}
	loop := body[loopAt:]
	if !strings.Contains(loop, "context.WithTimeout(parentCtx, SessionViewRefreshBudget)") {
		t.Fatal("视图循环体内没有按 SessionViewRefreshBudget 构造每视图 ctx —— " +
			"慢视图会重新侵占其他视图的预算（252 饿死形态回归）")
	}
	if deadlineAt > loopAt {
		t.Fatal("retryDeadline 在循环内计算 —— 与既有共享重试预算的钉测冲突")
	}
	// 反向判据：整轮共享预算的旧形态不得回归。
	if strings.Contains(body, "context.WithTimeout(parentCtx, SessionViewsTimeout)") ||
		strings.Contains(src, "SessionViewsTimeout =") {
		t.Fatal("整轮共享 ctx / SessionViewsTimeout 形态复活 —— 尾部视图会被重新饿死")
	}
}
