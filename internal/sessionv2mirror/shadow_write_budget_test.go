package sessionv2mirror

// shadow_write_budget_test.go — 2026-10-20。
//
// 钉住「维度写与轮次写各用各的预算」这条语义。
//
// 为什么这条语义要单独钉（一次静默失效的根因）：
//
//	runShadowWrite 里 session_dim 维度写与轮次写原本**共用一个 ctx**
//	（也就共用一个 deadline）。维度写是
//	`INSERT ... ON CONFLICT (gw_session_id) DO UPDATE`，其触发器还要去
//	UPDATE session_summaries；这一步慢或多等几把锁，就会把**轮次写**的
//	预算吃光 ⇒ session_turn_details 超时 ⇒
//	  ① 正文（session_bodies）一行不落；
//	  ② 整笔写失败 ⇒ 走 outbox 异步重放 ⇒ turn_no 与请求顺序错位；
//	  ③ trace 找不到父行 ⇒ trace_events 丢失。
//
// ⇒ 这正是 LESSONS「同一次判定里的不同判据可以有不同的时间语义，
// 不要因为共用一个函数就让它们共用同一个 gate」的形态：
// **维度写是 best-effort 的旁路，轮次写才是这次判定的主体。**
// 让旁路吃主体的预算 ⇒ 主体失败、旁路却「成功」了。
//
// 本门用**行为**钉：让维度写把预算耗光，轮次写仍必须拿到**完整**的预算。
// 变异：把 dim 的 ctx 改回共享的 ctx ⇒ 轮次写拿到的剩余预算 ≈0 ⇒ 门红。

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// budgetBurnDimWriter 一直阻塞到自己的 ctx 结束 —— 模拟「维度写慢到把预算
// 耗光」这个真实形态（触发器里的 session_summaries 更新被锁住等）。
type budgetBurnDimWriter struct {
	blocked bool
}

func (d *budgetBurnDimWriter) UpsertSessionDim(ctx context.Context, _ *telemetry.RequestLogEntry) error {
	d.blocked = true
	<-ctx.Done()
	return ctx.Err()
}

// budgetRecordingWriter 记录 Write 拿到的 ctx 的剩余 deadline。
type budgetRecordingWriter struct {
	remaining time.Duration
	called    bool
}

func (w *budgetRecordingWriter) Write(ctx context.Context, _ *v2.ProcessedRequest) error {
	w.called = true
	if dl, ok := ctx.Deadline(); ok {
		w.remaining = time.Until(dl)
	}
	return nil
}

func TestShadowWriteBudgetIsNotSharedBetweenDimAndTurn(t *testing.T) {
	const budget = 400 * time.Millisecond

	dim := &budgetBurnDimWriter{}
	turn := &budgetRecordingWriter{}
	entry := &telemetry.RequestLogEntry{RequestID: "req-budget-probe"}

	start := time.Now()
	runShadowWrite(turn, &v2.ProcessedRequest{}, entry, []DimWriter{dim}, budget, false)
	elapsed := time.Since(start)

	if !dim.blocked {
		t.Fatal("维度写没被调用 —— 判据没跑到被测语义上")
	}
	if !turn.called {
		t.Fatal("轮次写没被调用 —— 判据没跑到被测语义上")
	}
	if elapsed < budget {
		t.Fatalf("总耗时 %v < 预算 %v —— 维度写没有真的耗光预算，判据没跑到被测语义上",
			elapsed, budget)
	}
	// 关键断言：轮次写拿到的必须是**完整**预算，而不是被维度写扣掉过的残余。
	// 共用 ctx 时，维度写耗光预算后轮次写看到的是已过期的 deadline（剩余 ≤ 0）。
	if turn.remaining <= budget/2 {
		t.Fatalf("轮次写拿到的剩余预算 = %v（预算 %v）—— 维度写把主体的 deadline 吃光了。"+
			"维度写是 best-effort 旁路、轮次写才是判定主体，两者必须各用各的预算 "+
			"（否则正文整段不落、turn_no 错位、trace 丢失）",
			turn.remaining, budget)
	}
}
