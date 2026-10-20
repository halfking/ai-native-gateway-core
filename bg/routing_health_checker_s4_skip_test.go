package bg

// routing_health_checker_s4_skip_test.go —— 2026-10-20（R51 续轮建；
// ★ 2026-10-20 迁移 890 + 并行审计轮 D0 轮**退役换题**）
//
// # 退役了什么
//
// 本文件原先给「recorded_cost_is_negative 挂 S4 停写门」补判据门，守的是三件事：
//
//	 1. 守卫**先于** SQL 短路（没发 SQL，而不是发完再丢结果）；
//	 2. 守卫是**逐检查**的，不是把整个 worker 停掉；
//	 3. 「跳过了」**可观测**（gauge=1），不被读成「没有问题」。
//
// 退役的是 `recordedCostComparability`（纯函数）与 `requestLogsWriteEnabled`
// （变量），以及挂在 recorded_cost_is_negative 上的那道**具体守卫**。
//
// # 为什么退役
//
// ① **前提没了**：890 把 v1 族整体 DROP，这条检查的源随之迁到**会话族**
//    （db.SessionFamilyTurnsSourceSQL()，写入路径无条件）⇒「源不再前进 ⇒
//    『有没有新出现的负成本』不可判定」这个前提**不再成立**。
// ② **门恒闭**：守卫读的是 settings.RequestLogsWriteEnabled()，而该键的
//    **代码默认**已被并行审计轮改成 false（origin/main 的 D0）⇒ 在任何未显式
//    配置它的环境上，这条检查**永久不发 SQL** ⇒「不可判定」被上报成「没问题」。
//
// ⇒ 门关着的理由已过期、后果却不可逆 ⇒ 门退役（检查**恒执行**）。
//
// # 现在由什么承担
//
// ① `TestRecordedCostCheckCarriesNoS4Guard`（新门）：这条检查**不得**再挂
//    SkipWhen —— 有人把守卫加回来时立刻变红。
// ② `TestSkipWhenMechanismStillShortCircuitsBeforeQuery` 与
//    `TestSkippedCheckIsRecordedAndPublished`：**机制**侧的两道腿保留，
//    但改用**合成检查**（带 SkipWhen 的 HealthCheckDef）驱动 ——
//    SkipWhen 这个机制本身仍然活着，仍然必须「先于 SQL 短路」且「跳过可观测」。
//    为什么用合成检查：真实世界里现在**没有**任何检查挂 SkipWhen 了，
//    若只靠真实检查覆盖机制 ⇒ 机制会**静默腐烂**（改了没人发现）。
// ③ `TestRoutingHealthCheckerIsRegisteredInS4SkipClosedSet`：闭集腿保留。
//
// # 量具说明（不变）
//
// 核心腿的量具是「一个**连不上**的池」：池非 nil ⇒ runner 不会 panic，
// 而是把连接错误**带在错误串里**；错误串的开头就区分了「谁发的这条 SQL」：
//   · SkipWhen 生效 ⇒ 错误来自 runner **尾部**的 auto-fix（检查本身没发过 SQL）；
//   · SkipWhen 失效 ⇒ 错误来自 "query <check_id>: ..."。
// ⇒ 正反两腿用**同一个池、同一条（合成）检查**，只切换 SkipWhen 的返回值。

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// s4TestUnreachablePool 返回一个**非 nil 但连不上**的连接池。
//
// 为什么不用 nil 池：nil 池在 runner **尾部**的 auto-fix 处就会 panic，
// 于是「panic 发生在哪」变成了唯一的判别依据（要读栈帧，脆）。
// 连不上的池则把错误**带在错误串里**，判据可以按串判定。
func s4TestUnreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://nobody:nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// s4TestCheckDefByID 在 AllHealthChecks() 里找出某条检查定义。
func s4TestCheckDefByID(t *testing.T, id string) HealthCheckDef {
	t.Helper()
	for _, c := range AllHealthChecks() {
		if c.CheckID == id {
			return c
		}
	}
	t.Fatalf("检查 %q 不在 AllHealthChecks() 里", id)
	return HealthCheckDef{}
}

// s4TestSyntheticSkippedCheck 造一条**带 SkipWhen** 的合成检查，用来驱动
// SkipWhen 这套**机制**。
//
// ⚠ 为什么必须用合成检查：890 之后真实世界里**没有**任何检查挂 SkipWhen
// （唯一挂的那个随 v1 族退役）⇒ 若机制的覆盖只依赖真实检查，
// 机制就会静默腐烂（改了 SkipWhen 的短路语义也没人发现）。
//
// skip 开关可切换 ⇒ 正反两腿用**同一条**检查、**同一个池**，只切换它。
func s4TestSyntheticSkippedCheck(skip bool) HealthCheckDef {
	return HealthCheckDef{
		CheckID:  "s4_synthetic_skipped_probe",
		Severity: "warning",
		SkipWhen: func() (bool, string) { return skip, s4ScanSkipReasonStopWrite },
		Query:    "SELECT 1",
	}
}

// TestRecordedCostCheckCarriesNoS4Guard（★ 新门，退役换题后的不变式）：
// recorded_cost_is_negative **不得**再挂 SkipWhen。
//
// 变异：给这条检查加回 SkipWhen ⇒ 红。
func TestRecordedCostCheckCarriesNoS4Guard(t *testing.T) {
	def := s4TestCheckDefByID(t, "recorded_cost_is_negative")
	require.Nil(t, def.SkipWhen,
		"这条检查不得再挂前提守卫 —— 它的源已随 890 迁到会话族（写入路径无条件），"+
			"『源不再前进 ⇒ 问题不可判定』的前提已不存在；而守卫读的键（"+
			"storage.request_logs_write_enabled）的代码默认已被并行审计轮改成 false "+
			"⇒ 挂上去等于让这条检查在未显式配置该键的环境上永久不发 SQL，"+
			"把「不可判定」上报成「没问题」")
}

// TestOnlyTheFrozenSourceCheckIsGuarded —— 粒度腿（★ 反转后的方向）：
// 守卫是**逐检查**的机制 ⇒ 当前**没有任何**检查挂它。
//
// 旧门断言的是「只有读冻结源的那一条被守卫挡着，其余照跑」；
// 反转后：唯一挂过守卫的那条已退役 ⇒ 现在**一条都不挂**。
//
// ⚠ 这条断言的方向必须能区分「没有守卫」与「守卫挂错地方」：
//
//	正向对照用下面的合成检查证明 SkipWhen **确实**会被 runner 读到。
func TestOnlyTheFrozenSourceCheckIsGuarded(t *testing.T) {
	for _, id := range []string{
		"recorded_cost_is_negative",
		"canonical_id_null", "probe_missing", "billing_mismatch",
	} {
		def := s4TestCheckDefByID(t, id)
		require.Nil(t, def.SkipWhen,
			"检查 %q 不该挂前提守卫 —— 当前健康检查器里没有任何一条的源是冻结的", id)
	}
}

// TestSkipWhenMechanismStillShortCircuitsBeforeQuery —— 机制的核心腿：
// SkipWhen **先于** SQL 短路。
//
// 同一个池、同一条（合成）检查，只切换 SkipWhen 的返回值：
//
//	· 不跳过 ⇒ 错误来自这条检查自己的查询；
//	· 跳过 ⇒ 错误**不**来自这条检查的查询（它根本没发 SQL）。
//
// 变异：把守卫判断挪到 db.Query 之后 ⇒ 反腿红。
// 变异：把极性接反（跳过 ⇒ 不跳过）⇒ 反腿红。
func TestSkipWhenMechanismStillShortCircuitsBeforeQuery(t *testing.T) {
	pool := s4TestUnreachablePool(t)

	// 正腿：不跳过 ⇒ 这条检查确实发了 SQL。
	def := s4TestSyntheticSkippedCheck(false)
	_, _, err := runChecks(context.Background(), pool, []HealthCheckDef{def})
	require.Error(t, err, "连不上的池必然报错")
	require.Contains(t, err.Error(), "query "+def.CheckID,
		"不跳过时这条检查必须照常发 SQL —— 否则正腿失去对照意义")

	// 反腿：跳过 ⇒ 这条检查没发过 SQL。
	def = s4TestSyntheticSkippedCheck(true)
	_, _, err = runChecks(context.Background(), pool, []HealthCheckDef{def})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "query "+def.CheckID,
		"SkipWhen 必须让这条检查**根本不发出 SQL** —— 错误只能来自 runner 尾部的 auto-fix")
}

// TestSkippedCheckIsRecordedAndPublished —— 可观测腿（端到端）。
//
// 三步：守卫挡下 ⇒ recorder 收到 reason ⇒ gauge 变成 1。
// 最后一步是关键：gauge=0 会让告警侧读成「跑过了、没问题」。
func TestSkippedCheckIsRecordedAndPublished(t *testing.T) {
	pool := s4TestUnreachablePool(t)

	w := NewRoutingHealthChecker(nil)
	w.resetSkipped()

	def := s4TestSyntheticSkippedCheck(true)
	_, _, _ = runChecksWithRecorder(context.Background(), pool, []HealthCheckDef{def}, w)

	skipped := w.SkippedChecks()
	require.Len(t, skipped, 1, "跳过的检查必须被记录下来")
	require.Equal(t, s4ScanSkipReasonStopWrite, skipped[0])

	gauge := func() float64 {
		return testutil.ToFloat64(s4ScanSkippedLastRun.WithLabelValues(
			s4ScanWorkerRoutingHealthChecker, s4ScanSkipReasonStopWrite))
	}

	before := gauge()
	recordS4ScanSkipState(s4ScanWorkerRoutingHealthChecker, w.SkippedChecks(), time.Now())
	require.Greater(t, gauge(), before,
		"「本轮没执行」必须落到 llm_gateway_bg_s4_scan_skipped_last_run=1 上 —— "+
			"否则 findings=0 会被读成「扫了没发现问题」")

	// 下一轮真跑了 ⇒ 必须显式归零（§9.35 M3 那个「字段恒为 true」的坑）。
	w.resetSkipped()
	recordS4ScanSkipState(s4ScanWorkerRoutingHealthChecker, w.SkippedChecks(), time.Now())
	require.Zero(t, gauge(), "恢复执行后 gauge 必须显式归零")
}

// TestRoutingHealthCheckerIsRegisteredInS4SkipClosedSet —— 闭集腿。
//
// worker 必须登记进 s4ScanSkipWorkers（否则 init() 不会预初始化它的序列，
// 且 recordS4ScanSkipState 的归零遍历不会覆盖它 ⇒ 「上一轮跳过、这轮真跑」
// 会永远显示为跳过）。
//
// ⚠ 两个 reason 都要在闭集里：
//
//	· `usage_arm_empty` 是**现在**唯一会被产出���跳过原因（账务对账那条）；
//	· `s4_stop_write` 保留在闭集里是为了**指标序列连续性**（它在闭集里，
//	  gauge 恒 0）—— 闭集门的方向是「能产出的 reason 必须在闭集内」，
//	  不是「闭集里的必须被用到」，所以留着一个恒 0 的 reason 合规。
func TestRoutingHealthCheckerIsRegisteredInS4SkipClosedSet(t *testing.T) {
	found := false
	for _, w := range s4ScanSkipWorkers {
		if w == s4ScanWorkerRoutingHealthChecker {
			found = true
			break
		}
	}
	require.True(t, found,
		"routing_health_checker 必须登记进 s4ScanSkipWorkers（否则归零遍历不覆盖它）")
	require.True(t, s4ScanSkipReasonRegistered(s4ScanSkipReasonStopWrite),
		"s4_stop_write 必须在 s4ScanSkipReasons 闭集里")
	require.True(t, s4ScanSkipReasonRegistered(usageArmEmptySkipReason),
		"usage_arm_empty 必须在 s4ScanSkipReasons 闭集里 —— 它是账务对账那条"+
			"唯一会产出的跳过原因，未登记 ⇒ gauge 会把「跳过了」谎报成「跑过了」")
}
