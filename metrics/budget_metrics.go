package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Budget enforcement observability (第三十轮, 2026-10-02).
//
// 执行面预算检查（CheckBudget）的错误结局此前被两调用点类型断言静默吞掉。
// 并行会话 f215e22d0（12h 第三十轮）已把两调用点切为 fail-closed 503
// budget_unavailable；本指标是执行面的统一观测轴——ok/exceeded/error/
// skipped_snapshot/degraded_no_ledger 五结局闭集（GW-00）。
var (
	// BudgetChecksTotal counts every CheckBudget invocation by outcome.
	// outcome is the closed enum:
	//   ok               — check ran, under budget
	//   exceeded         — check ran, budget exceeded (402 path)
	//   error            — DB/查询错误（调用方 fail-closed 503 budget_unavailable）
	//   skipped_snapshot — snapshot-only verifier，无台账可查（设计跳过）
	//   degraded_no_ledger — usage_ledger_with_current_month 视图缺失，
	//     预算执行整体降级为放行（迁移 344 未应用）。注意：这是降级**事件**
	//     标记轴，与 ok/exceeded/error 终态轴会同一次调用双计（降级后检查
	//     仍会以 spent=0 继续走出终态）——按 series 分别作图，不做求和。
	BudgetChecksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_budget_checks_total",
		Help: "Budget enforcement checks by outcome (ok/exceeded/error/skipped_snapshot/degraded_no_ledger).",
	}, []string{"outcome"})
)

// Budget check outcome label values (closed enum).
const (
	BudgetOutcomeOK              = "ok"
	BudgetOutcomeExceeded        = "exceeded"
	BudgetOutcomeError           = "error"
	BudgetOutcomeSkippedSnapshot = "skipped_snapshot"
	BudgetOutcomeDegradedNoLedger = "degraded_no_ledger"
)

func init() {
	// 预热五个 outcome series：指标在首次 Inc 前不产出样本，仪表盘会呈现
	// "series 不存在"而非 0（R81 empty_response 白名单漏族的同型教训）。
	for _, outcome := range []string{
		BudgetOutcomeOK, BudgetOutcomeExceeded, BudgetOutcomeError,
		BudgetOutcomeSkippedSnapshot, BudgetOutcomeDegradedNoLedger,
	} {
		BudgetChecksTotal.WithLabelValues(outcome).Add(0)
	}
}
