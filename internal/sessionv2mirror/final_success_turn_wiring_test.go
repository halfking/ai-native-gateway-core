//go:build !integration

package sessionv2mirror

// final_success_turn_wiring_test.go — 2026-10-05（审计 §9.203）。
//
// # 为什么静态门之外还要这一道
//
// final_success_turn_gate_test.go 断言的是**代码里有没有那几个字**。它挡得住
// 「有人重构时把调用整段删掉」，挡不住「调用还在、但永远走不到」——
// 比如条件写反、守卫挂在另一个变量上、early return 提前跳出。
//
// 这一道是**行为级**的：真的调 runShadowWrite，真的观察它有没有去调标记函数。
//
// # 怎么在没有数据库的情况下观察「它去调了」
//
// markTurnFinalSuccess 收 *pgxpool.Pool，而单测里没有池。**但这恰恰是可观测的**：
// pool==nil 时它会记 `mark_no_pool` 并打一条 Warn。这个标签是这条包唯一的
// 「我被调用了」的信号，所以：
//
//	FinalSuccessClaimed=true  → mark_no_pool +1
//	FinalSuccessClaimed=false → mark_no_pool 不变
//
// ⇒ 不需要桩 DB，也不需要真的写库，就能判定调用发生没发生。
//
// # 零样本必须指名 Skip
//
// 本门断言的是**计数器增量**，不是数据行。真库那层由
// final_success_turn_realdb_test.go 负责；两者职责不重叠。

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// countingWriter records how many times the shadow write was attempted and
// whether it succeeded, so the test can tell "the mark was skipped because
// the write failed" apart from "the mark was never wired up".
type countingWriter struct {
	calls int
	err   error
}

func (c *countingWriter) Write(context.Context, *v2.ProcessedRequest) error {
	c.calls++
	return c.err
}

func runShadowWriteForTest(t *testing.T, entry *telemetry.RequestLogEntry, w V2Writer) {
	t.Helper()
	req := &v2.ProcessedRequest{
		SessionID: "wiring-test-session",
		TenantID:  "wiring-test-tenant",
		RequestID: entry.RequestID,
		Timestamp: time.Now(),
	}
	// shadowWriteDispatchAsync is forced inline by the caller (the tests in
	// this package do the same): with it true the write jumps to a goroutine
	// and the assertion below would race the goroutine, i.e. the gate would
	// pass or fail by scheduling luck.
	prev := shadowWriteDispatchAsync
	shadowWriteDispatchAsync = false
	t.Cleanup(func() { shadowWriteDispatchAsync = prev })

	runShadowWrite(w, req, entry, nil, 5*time.Second, false)
}

func TestRunShadowWriteAppliesTheFinalSuccessMarkOnlyWhenClaimed(t *testing.T) {
	// 前提自证：单测里没有初始化过池，所以 mark_no_pool 就是「被调用了」的信号。
	// 若将来某个 TestMain 真的初始化了池，这条前提就失效了——那时本门会恒绿
	// 而不是变红，所以必须在这里把它钉死并说清楚。
	if p := mirrorOutbox.Load(); p != nil {
		t.Fatalf("本门的前提不成立：mirrorOutbox 已被初始化（%T），"+
			"markTurnFinalSuccess 会走真库路径而不再记 mark_no_pool，本门会恒绿。"+
			"请改用可注入的 DB 桩，不要靠「恰好没初始化」", p)
	}

	t.Run("claimed → 被调用", func(t *testing.T) {
		before := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_no_pool"))
		w := &countingWriter{}
		runShadowWriteForTest(t, &telemetry.RequestLogEntry{
			RequestID:           "wiring-claimed",
			FinalSuccessClaimed: true,
		}, w)
		if w.calls != 1 {
			t.Fatalf("影子写没有被执行（calls=%d）—— 本门测的不是标记", w.calls)
		}
		if got := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_no_pool")) - before; got != 1 {
			t.Fatalf("认领成功时 runShadowWrite 没有调用标记函数（mark_no_pool +%v）—— "+
				"标志到了 entry 却没人在 w.Write 之后用它", got)
		}
	})

	t.Run("未认领 → 不被调用", func(t *testing.T) {
		before := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_no_pool"))
		w := &countingWriter{}
		runShadowWriteForTest(t, &telemetry.RequestLogEntry{
			RequestID:           "wiring-unclaimed",
			FinalSuccessClaimed: false,
		}, w)
		if w.calls != 1 {
			t.Fatalf("影子写没有被执行（calls=%d）", w.calls)
		}
		if got := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_no_pool")) - before; got != 0 {
			t.Fatalf("未认领的请求也去打了标记（mark_no_pool +%v）—— "+
				"守卫条件写错了，每一个 turn 都会被标成最终成功", got)
		}
	})

	t.Run("写失败 → 不得调用（否则必然 mark_no_row）", func(t *testing.T) {
		before := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_no_pool"))
		w := &countingWriter{err: context.DeadlineExceeded}
		runShadowWriteForTest(t, &telemetry.RequestLogEntry{
			RequestID:           "wiring-writefailed",
			FinalSuccessClaimed: true,
		}, w)
		if got := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_no_pool")) - before; got != 0 {
			t.Fatalf("影子写失败后仍然去打了标记（mark_no_pool +%v）—— "+
				"此时 turn 行根本不存在，调用只会稳定命中 0 行并打出假警", got)
		}
	})
}
