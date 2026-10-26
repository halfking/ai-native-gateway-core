// AUDIT-GUARD: 本文件的门**必须随包在 CI 上执行** ⇒ 该包已登记进 Makefile 的 GUARD_PACKAGES
package authentication

// budget_reservation_contract_test.go —— P1-3「预算预约-释放」的**现状契约门**（预算侧）。
//
// # 这道门钉的是哪块缺口
//
// 计划书 §6-P1-3 要求「**先出结论再出码**」。读完现状后的结论写在
// docs/audit/2026-10-24-magpie-v0.1.1367-改造落地审计.md §3.9，
// 一句话是：
//
//	**本仓的预算闸门是「只查不占」的 pre-flight check**（认证之后、执行之前读一次
//	当月账本，比对 `spent >= budget`）⇒ **它没有预留语义** ⇒ **并发双花**：
//	从检查到用量落库可见之间，同一 key 的 N 个并发请求**全部**通过检查
//	⇒ 实际花费可以超出 `budget_usd` 达「N × 单请求成本」。
//
// ★ 这道门**不修**这个缺口（修它需要跨节点的预留存储 + 成本预估价，属新增产品能力，
// 需要属主裁决，见审计档 §3.9 与 §五）。★ 它做的是把**现状的三条语义**钉死：
//
//	G1 ★ **只查不占**：账本没变时，同一 key 可以**反复**通过检查
//	    ⇒ 这就是双花窗口的**直接体现**（若将来引入预留，这条会红 ⇒ 强制一次显式决策）。
//	G2 ★ **边界含等号**：`spent == budget` 判为**超支**（不是「刚好用完就放过」）。
//	G3 ★ **视图缺失 ⇒ 降级为放行**（`spent=0`）且**有指标计数**
//	    ⇒ 这是一条**静默 fail-open** 的降级轴，必须被看见，不能无声无息。
//
// # 既有覆盖与本门的分工（避免重复造门）
//
// 既有 `verifier_pgx_test.go` 已覆盖：disabled / 无预算 / DB 错误 / 超支 / 未超 / 查询超时。
// ★ 本门**只补那三条没被覆盖的**，不重复。

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// G1 —— 只查不占。
//
// ★ 「反复通过」不是 bug 的同义词，而是一条**必须被看见的事实**：
// 任何读者看到「预算闸门」三个字，默认会以为它是**有界**的。
// 这条门把这个默认假设**打掉**，并让它在语义变更时**响亮地红**。
func TestBudgetCheckDoesNotReserveSpend(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	budget := 100.0
	// ★ 两次检查读到的账本**完全相同**（spent 都是 0）—— 这正是
	// 「第一次请求的用量还没落库」的那段时间。
	for i := 0; i < 2; i++ {
		mp.ExpectQuery(`SELECT budget_usd`).
			WithArgs(1).
			WillReturnRows(pgxmock.NewRows([]string{"budget_usd"}).AddRow(&budget))
		mp.ExpectQuery(`FROM usage_ledger_with_current_month`).
			WithArgs(1).
			WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(0.0))
	}

	for i := 0; i < 2; i++ {
		if err := kv.CheckBudget(context.Background(), 1); err != nil {
			t.Fatalf("第 %d 次检查应放行（账本未变）：%v", i+1, err)
		}
	}
	// ★ 两次都必须**真的查了账本**（⇒ 它每次都重读，而不是持有一份预留）。
	if err := mp.ExpectationsWereMet(); err != nil {
		t.Fatalf("两次检查都应真的查了当月账本：%v", err)
	}
	// ★ 把这条事实写进错误信息里：读数变红时，读者立刻知道「预留语义被加进来了」。
	t.Log("预算闸门是只查不占的 pre-flight check ⇒ 并发窗口内可双花；" +
		"若要引入预留（magpie 式），本门会红 ⇒ 那时必须同时改审计档 §3.9 的结论")
}

// G2 —— 边界含等号。
//
// ★ `spent == budget` 判为超支 ⇒ 「正好用完预算」的那一个请求会被 402。
// 这是个**刻意**的边界（>= 而不是 >）⇒ 必须被钉住，否则有人改成 > 就会
// 「悄悄多放一个请求」。
func TestBudgetCheckTreatsExactBudgetAsExceeded(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	budget := 100.0
	mp.ExpectQuery(`SELECT budget_usd`).
		WithArgs(1).
		WillReturnRows(pgxmock.NewRows([]string{"budget_usd"}).AddRow(&budget))
	mp.ExpectQuery(`FROM usage_ledger_with_current_month`).
		WithArgs(1).
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(100.0)) // == budget

	err := kv.CheckBudget(context.Background(), 1)
	if _, ok := err.(*BudgetExceededError); !ok {
		t.Fatalf("spent == budget 应判为超支，err = %v", err)
	}
}

// G3 —— 视图缺失 ⇒ 降级为放行（静默 fail-open），且有指标计数。
//
// ★ 这条降级轴是**故意**的（迁移窗口内不该让每个请求都 503）⇒ 但它
// **必须可见** ⇒ 否则「预算闸门其实没在工作」这件事就没人知道。
func TestBudgetCheckDegradesToAllowWhenLedgerViewMissing(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	counter := metrics.BudgetChecksTotal.WithLabelValues(metrics.BudgetOutcomeDegradedNoLedger)
	before := testutil.ToFloat64(counter)

	budget := 100.0
	mp.ExpectQuery(`SELECT budget_usd`).
		WithArgs(1).
		WillReturnRows(pgxmock.NewRows([]string{"budget_usd"}).AddRow(&budget))
	mp.ExpectQuery(`FROM usage_ledger_with_current_month`).
		WithArgs(1).
		WillReturnError(&pgconn.PgError{
			Code:    "42P01",
			Message: `relation "usage_ledger_with_current_month" does not exist`,
		})

	// ★ 降级 = 放行（spent 记 0 ⇒ 未超支 ⇒ 不报错）。
	if err := kv.CheckBudget(context.Background(), 1); err != nil {
		t.Fatalf("视图缺失时应降级为放行，err = %v", err)
	}

	// ★ 且降级必须**被计数**（否则仪表盘上看不出闸门正在放行）。
	if after := testutil.ToFloat64(counter); after != before+1 {
		t.Fatalf("degraded_no_ledger 计数 = %v，want %v（降级必须可见）", after, before+1)
	}
}
