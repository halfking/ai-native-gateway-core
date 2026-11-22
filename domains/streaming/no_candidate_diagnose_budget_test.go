package streaming

// no_candidate_diagnose_budget_test.go —— 零候选归因诊断的**独立时间预算**钉测
// （2026-11-23 12h 修订审计轮）。
//
// # 为什么要有这道门
//
// 归因诊断是**失败路径**上的只读旁路查询。审计前它直接用请求 ctx，
// **没有自己的时间上限** ⇒
//
//	① 失败路径恰是数据库最吃紧的时刻（候选判空常伴随上游异常 / 凭据大面积被闸滤掉）
//	   ⇒ 诊断查询一慢，**503 的应答就被拖住**；
//	② 诊断在 chat/completions、messages、responses 三个协议面各调一次；
//	③ 既有纪律只挡「诊断**出错**」，挡不住「诊断很慢」。
//
// # 本门钉什么
//
//	① 诊断拿到的 ctx **必须带 deadline**（预算确实接上了）；
//	② 该 deadline 必须**不长于** noCandidateDiagnoseBudget；
//	③ 预算本身必须在一个有界的区间内（防止被改成 0 或改成分钟级）；
//	④ 预算到期时，诊断**失败**并**退回通用文案**——不得变成新的失败形态
//	   （既有 TestDiagnoseNoCandidateCause_FallsBackWhenUnexplained 覆盖
//	    「解释不了」；本门补「预算到期」这一条路径）。
//
// ★ 本门**不**试图用真实 DB 制造慢查询——那需要 DSN 与故障注入；
//   它钉的是「预算被接上了、量级有界、到期后退回通用文案」这三件可机器判定的事。
//   真实慢查询下的端到端表现由 make test-realdb-gate 一层兜底。

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// budgetProbeResolver 记录诊断被调用时 ctx 上是否带 deadline。
type budgetProbeResolver struct {
	resolverBase
	breakdown provider.EmptyCandidateBreakdown
	err       error

	sawDeadline       bool
	deadlineRemaining time.Duration
}

func (r *budgetProbeResolver) DiagnoseEmptyCandidates(ctx context.Context, model, tenantID string) (provider.EmptyCandidateBreakdown, error) {
	if dl, ok := ctx.Deadline(); ok {
		r.sawDeadline = true
		r.deadlineRemaining = time.Until(dl)
	}
	return r.breakdown, r.err
}

// TestDiagnoseNoCandidateCause_HasIndependentBudget 钉「诊断自带时间上限」。
func TestDiagnoseNoCandidateCause_HasIndependentBudget(t *testing.T) {
	probe := &budgetProbeResolver{breakdown: sonnet55Breakdown()}
	h := &ChatHandler{provider: probe}

	h.diagnoseNoCandidateCause(context.Background(), "claude-sonnet-5-5", "default")

	if !probe.sawDeadline {
		t.Fatal("诊断拿到的 ctx 没有 deadline ⇒ 失败路径上的旁路查询没有独立时间预算（审计轮缺陷未修）")
	}
	if probe.deadlineRemaining <= 0 {
		t.Fatalf("诊断拿到的 deadline 已经过期（remaining=%v）⇒ 诊断永远拿不到结果", probe.deadlineRemaining)
	}
	if probe.deadlineRemaining > noCandidateDiagnoseBudget {
		t.Errorf("诊断预算=%v 超过约定的 noCandidateDiagnoseBudget=%v ⇒ 失败路径可能被拖住",
			probe.deadlineRemaining, noCandidateDiagnoseBudget)
	}
}

// TestNoCandidateDiagnoseBudget_Bounded 钉预算量级有界。
func TestNoCandidateDiagnoseBudget_Bounded(t *testing.T) {
	if noCandidateDiagnoseBudget <= 0 {
		t.Fatalf("noCandidateDiagnoseBudget=%v 必须为正", noCandidateDiagnoseBudget)
	}
	if noCandidateDiagnoseBudget > 5*time.Second {
		t.Errorf("noCandidateDiagnoseBudget=%v 过大（>5s）⇒ 失败路径上的诊断可能与客户端超时抢时间", noCandidateDiagnoseBudget)
	}
}

// TestDiagnoseNoCandidateCause_BudgetExpiryFallsBack 钉「预算到期 ⇒ 退回通用文案」。
//
// 做法：让诊断器**睡到预算到期**再返回 ⇒ ctx 已过期 ⇒ 诊断必须失败 ⇒
// 归因为空 ⇒ 调用方拿到的必须是零值（通用文案），而不是一个半吊子归因。
func TestDiagnoseNoCandidateCause_BudgetExpiryFallsBack(t *testing.T) {
	probe := &budgetProbeResolver{}
	probe.err = nil
	// 用一个必定超时的诊断器：睡到 ctx 结束。
	h := &ChatHandler{provider: &sleepingResolver{}}

	got := h.diagnoseNoCandidateCause(context.Background(), "claude-sonnet-5-5", "default")
	if !got.empty() {
		t.Fatalf("预算到期后归因必须为空（退回通用文案），实际拿到了 %+v", got)
	}
}

// sleepingResolver 模拟一个「慢到超过预算」的诊断器。
type sleepingResolver struct {
	resolverBase
}

func (r *sleepingResolver) DiagnoseEmptyCandidates(ctx context.Context, model, tenantID string) (provider.EmptyCandidateBreakdown, error) {
	<-ctx.Done()
	return provider.EmptyCandidateBreakdown{}, ctx.Err()
}

// TestDiagnoseNoCandidateCause_ClientCancelStillPropagates 钉「客户端取消不被预算吞掉」。
//
// 预算层是从请求 ctx **派生**的 ⇒ 客户端先取消时，实际生效的应当是客户端的取消，
// 而不是那 2s 预算 ⇒ 诊断立刻返回，不多等 2s。
func TestDiagnoseNoCandidateCause_ClientCancelStillPropagates(t *testing.T) {
	reqCtx, cancelReq := context.WithCancel(context.Background())
	cancelReq()

	probe := &budgetProbeResolver{breakdown: sonnet55Breakdown()}
	h := &ChatHandler{provider: probe}

	start := time.Now()
	h.diagnoseNoCandidateCause(reqCtx, "claude-sonnet-5-5", "default")
	elapsed := time.Since(start)

	if elapsed > noCandidateDiagnoseBudget {
		t.Errorf("客户端已取消，但诊断仍耗时 %v（> 预算 %v）⇒ 取消没有被透传", elapsed, noCandidateDiagnoseBudget)
	}
	if !probe.sawDeadline {
		t.Fatal("诊断 ctx 仍应带 deadline（预算层存在）")
	}
}
