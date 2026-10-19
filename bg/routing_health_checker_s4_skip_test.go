package bg

// routing_health_checker_s4_skip_test.go —— 2026-10-20（R51 续轮）
// 给「recorded_cost_is_negative 挂 S4 停写门」补判据门。
//
// 背景：属主在 R51 续轮拍板走 (b)「框架级『源未推进 ⇒ 跳过』」。
// 这条检查读的是已停写的 `public._request_logs` ⇒ 停写期间
// 「有没有**新出现的**负成本」不可判定；而它每 ~7.5 分钟（两个实例 × 15m
// ticker）重扫一次一个**永远不会再变**的源，且恒返回 0 行 ——
// 0 行在 findings 计数上与「扫了没发现问题」**完全不可区分**。
//
// ⇒ 判据要钉住三件不同的事，缺一条就会出现「看起来门都绿了，其实没挡住」：
//  1. 守卫**先于** SQL 短路（没发 SQL，而不是发完再丢结果）；
//  2. 守卫是**逐检查**的，不是把整个 worker 停掉；
//  3. 「跳过了」**可观测**（gauge=1），不被读成「没有问题」。
//
// ⚠ 判据设计说明：本文件的所有腿都**不依赖真库**（TEST_DATABASE_URL 未设置时
// 真库腿会静默 skip ⇒ 恒绿）。核心腿的量具是「一个**连不上**的池」：
// 池非 nil ⇒ runner 不会 panic，而是把连接错误**带在错误串里**；
// 错误串的开头就区分了「谁发的这条 SQL」——
//   · 守卫生效 ⇒ 错误来自 runner **尾部**的 auto-fix（检查本身没发过 SQL）；
//   · 守卫失效 ⇒ 错误来自 "query recorded_cost_is_negative: ..."。
// ⇒ 正反两腿用**同一个池、同一条检查**，只切换停写门，互为对照（非空断言）。
//
// ★ 本轮这条门当场抓到一个真缺陷：recordedCostComparability 回答的是
// 「还能不能判定」，SkipWhen 回答的是「要不要跳过」——两个问题**极性相反**。
// 直连会写成「不可判定 ⇒ 不跳过」，守卫于是永远不生效，而**判定函数本身的
// 判据仍然全绿**。⇒ 这条「错误串里有没有这条检查的查询」的腿是必需的。

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// s4TestRequestLogsGate 把 S4 停写门切到指定状态，并在测试结束时还原。
func s4TestRequestLogsGate(t *testing.T, writeEnabled bool) {
	t.Helper()
	orig := requestLogsWriteEnabled
	requestLogsWriteEnabled = func() bool { return writeEnabled }
	t.Cleanup(func() { requestLogsWriteEnabled = orig })
}

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

// TestRecordedCostComparability —— 判定函数的正反两腿。
//
// 变异一：永远返回 comparable=true ⇒ 第二腿红。
// 变异二：reason 换个未登记的键 ⇒ 第二腿红（且会被 s4 闭集门抓住）。
func TestRecordedCostComparability(t *testing.T) {
	comparable, reason := recordedCostComparability(true)
	require.True(t, comparable, "写门开着时这条检查必须照常执行")
	require.Empty(t, reason, "可判定时不应带跳过原因")

	comparable, reason = recordedCostComparability(false)
	require.False(t, comparable, "停写期间这条检查不可判定，必须跳过")
	require.Equal(t, s4ScanSkipReasonStopWrite, reason,
		"跳过原因必须用已登记的键，否则 recordS4ScanSkipState 会拒写（宁可吵闹不可谎报）")
}

// TestRecordedCostCheckCarriesTheS4Guard —— 检查定义真的挂了守卫，
// 且守卫的行为**完全等于** recordedCostComparability。
//
// 变异：把守卫换成「恒 false」⇒ 第二腿红（停写时仍会发 SQL）。
// 变异：把守卫换成「恒 true」⇒ 第一腿红（写门开着时也不查了）。
func TestRecordedCostCheckCarriesTheS4Guard(t *testing.T) {
	def := s4TestCheckDefByID(t, "recorded_cost_is_negative")
	require.NotNil(t, def.SkipWhen, "这条检查必须挂前提守卫（SkipWhen）")

	s4TestRequestLogsGate(t, false)
	skip, reason := def.SkipWhen()
	require.True(t, skip, "停写生效时这条检查必须被守卫挡下")
	require.Equal(t, s4ScanSkipReasonStopWrite, reason)

	s4TestRequestLogsGate(t, true)
	skip, reason = def.SkipWhen()
	require.False(t, skip, "写门开着时这条检查必须照常执行（守卫不能恒跳过）")
	require.Empty(t, reason)
}

// TestOnlyTheFrozenSourceCheckIsGuarded —— 粒度腿：守卫是**逐检查**的。
//
// 停写期间只有读冻结源的那一条不再可判定；其余检查（canonical_id_null /
// probe_missing / billing_mismatch …）读的是活表，必须照跑。
// 把整个 worker 停掉是错的（会连带停掉十几条仍然可判定的检查）。
func TestOnlyTheFrozenSourceCheckIsGuarded(t *testing.T) {
	for _, id := range []string{"canonical_id_null", "probe_missing", "billing_mismatch"} {
		def := s4TestCheckDefByID(t, id)
		require.Nil(t, def.SkipWhen,
			"检查 %q 读的是活表，停写期间仍然可判定，不该被守卫挡掉", id)
	}
}

// TestHealthCheckSkipWhenShortCircuitsBeforeQuery —— 核心腿：
// 守卫**先于** SQL 短路。
//
// 同一个池、同一条检查，只切换停写门：
//   · 写门开着 ⇒ 错误来自这条检查自己的查询；
//   · 停写生效 ⇒ 错误**不**来自这条检查的查询（它根本没发 SQL）。
//
// 变异：把守卫判断挪到 db.Query 之后 ⇒ 第二腿红。
// 变异：把极性接反（不可判定 ⇒ 不跳过）⇒ 第二腿红（本轮真踩到过）。
func TestHealthCheckSkipWhenShortCircuitsBeforeQuery(t *testing.T) {
	pool := s4TestUnreachablePool(t)
	def := s4TestCheckDefByID(t, "recorded_cost_is_negative")

	// 正腿：写门开着 ⇒ 这条检查确实发了 SQL。
	s4TestRequestLogsGate(t, true)
	_, _, err := runChecks(context.Background(), pool, []HealthCheckDef{def})
	require.Error(t, err, "连不上的池必然报错")
	require.Contains(t, err.Error(), "query recorded_cost_is_negative",
		"写门开着时这条检查必须照常发 SQL —— 否则正腿失去对照意义")

	// 反腿：停写生效 ⇒ 这条检查没发过 SQL。
	s4TestRequestLogsGate(t, false)
	_, _, err = runChecks(context.Background(), pool, []HealthCheckDef{def})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "query recorded_cost_is_negative",
		"守卫必须让这条检查**根本不发出 SQL** —— 错误只能来自 runner 尾部的 auto-fix")
}

// TestSkippedCheckIsRecordedAndPublished —— 可观测腿（端到端）。
//
// 三步：守卫挡下 ⇒ recorder 收到 reason ⇒ gauge 变成 1。
// 最后一步是关键：gauge=0 会让告警侧读成「跑过了、没问题」。
func TestSkippedCheckIsRecordedAndPublished(t *testing.T) {
	s4TestRequestLogsGate(t, false)
	pool := s4TestUnreachablePool(t)

	w := NewRoutingHealthChecker(nil)
	w.resetSkipped()

	def := s4TestCheckDefByID(t, "recorded_cost_is_negative")
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
}
