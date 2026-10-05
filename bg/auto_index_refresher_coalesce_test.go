package bg

// AutoIndexRefresher 触发合并（coalescing）行为门（2026-10-05 P2，R44 移交
// §R44/移交.3）。
//
// 4315a598c 只加了 singleflight 互斥：它消除的是并发「交错」，不是重复执行
// —— 只要 rollup 耗时 < 触发间隔，N 倍频的触发会**串行全部执行**，DELETE
// 次数不降（实测每 8.8s 一次 ⇒ 同一个 5 分钟 bucket 被反复 DELETE+INSERT
// 约 34 遍）。本文件守节流那一半：
//
//   - 同 bucket 的重复触发必须合并（N 次触发 ≤ 定数次执行）；
//   - 合并不得破坏最终一致性：bucket 滚动后（= 5 分钟 ticker 的兜底位）
//     必须放行一次执行；
//   - singleflight 忙跳过必须留下欠账并在 bucket 内补执行 —— 否则缺陷 1
//     （跳过被记成已处理 ⇒ 丢一次路由配置直到下个 bucket）会借合并节流
//     借尸还魂。
//
// 全部离线可跑：executeFn 注入假执行体替代真 rollup（真库不可用），
// nowFn 时钟缝确定性地推进 bucket（不引入真实等待）。

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// newCoalesceTestRefresher 构造离线 refresher：1 小时窗口（测试时长内
// bucket 不会自然滚动，滚动只由 advance 显式驱动）、假执行体、执行计数。
func newCoalesceTestRefresher(t *testing.T, start time.Time) (*AutoIndexRefresher, *atomic.Int64, func(time.Duration)) {
	t.Helper()
	r := &AutoIndexRefresher{
		RefreshInterval: time.Hour,
		RefreshTimeout:  time.Second,
		CoalesceWindow:  time.Hour,
	}
	cursor := start
	r.nowFn = func() time.Time { return cursor }
	r.executeFn = func(context.Context, time.Time) error { return nil }
	var execs atomic.Int64
	r.OnRollupComplete = func(time.Time, int, int) { execs.Add(1) }
	advance := func(d time.Duration) { cursor = cursor.Add(d) }
	return r, &execs, advance
}

//  1. 核心节流门：同 bucket 的 N 次触发合并为 1 次执行；触发停止后 bucket
//     滚动（ticker 的执行位）必须放行一次兜底执行 —— 最终一致性不被节流破坏。
func TestCoalescingCollapsesSameBucketTriggers(t *testing.T) {
	base := time.Date(2026, 10, 5, 12, 3, 0, 0, time.UTC)
	r, execs, advance := newCoalesceTestRefresher(t, base)
	ctx := context.Background()

	// 20 次同 bucket 触发：只有第 1 次拿到执行权，其余 19 次合并跳过。
	for i := 0; i < 20; i++ {
		skipped, err := r.RefreshOnceStatus(ctx)
		if err != nil {
			t.Fatalf("call %d: err = %v, want nil（合并跳过不是错误）", i, err)
		}
		if i == 0 && skipped {
			t.Fatal("第一次触发必须执行（该 bucket 尚无成功执行）")
		}
		if i > 0 && !skipped {
			t.Fatalf("第 %d 次同 bucket 触发未被合并（skipped=false）⇒ 34 倍重复 rollup 依旧", i+1)
		}
	}
	if got := execs.Load(); got != 1 {
		t.Fatalf("20 次同 bucket 触发产生 %d 次执行，必须合并为 1 次", got)
	}

	// 兜底：触发停止后，bucket 滚动（run 循环的 ticker 必然到来）必须放行。
	advance(time.Hour)
	skipped, err := r.RefreshOnceStatus(ctx)
	if err != nil || skipped {
		t.Fatalf("bucket 滚动后的兜底触发被跳过（skipped=%v err=%v）—— 最终一致性被破坏", skipped, err)
	}
	if got := execs.Load(); got != 2 {
		t.Fatalf("兜底执行未发生：execs = %d, want 2", got)
	}
}

//  2. 缺陷 1 × 缺陷 2 的组合门：singleflight 忙跳过必须留下欠账，同 bucket
//     内的下一次触发必须放行补执行；欠账消费后恢复合并。
//     若忙跳过也并入同 bucket 合并，缺陷 1 的数据丢失窗口拉满到下个 bucket。
func TestBusyDeferredTriggerRetriesWithinBucket(t *testing.T) {
	base := time.Date(2026, 10, 5, 12, 3, 0, 0, time.UTC)
	r, execs, advance := newCoalesceTestRefresher(t, base)
	ctx := context.Background()

	// 先让本 bucket 有一次成功执行（lastBucket 记账）—— 这样后续重试的
	// 放行只能来自 busyDeferred 消费通道，而不是 lastBucket 零值旁路。
	if skipped, err := r.RefreshOnceStatus(ctx); skipped || err != nil {
		t.Fatalf("首次触发应执行（skipped=%v err=%v）", skipped, err)
	}

	// 用原语占住槽，模拟一次长执行正在进行。
	if !r.beginRefresh() {
		t.Fatal("占槽失败")
	}
	skipped, err := r.RefreshOnceStatus(ctx)
	if err != nil {
		t.Fatalf("忙跳过返回 err = %v, want nil（跳过不是错误）", err)
	}
	if !skipped {
		t.Fatal("singleflight 忙时必须跳过（skipped=true）—— 返回 false 即两个 rollup 并发跑")
	}
	if got := execs.Load(); got != 1 {
		t.Fatalf("跳过不应执行执行体，execs = %d, want 1", got)
	}
	r.endRefresh()

	// 关键：忙跳过留下欠账 —— 同 bucket 内的下一次触发必须放行补执行。
	skipped, err = r.RefreshOnceStatus(ctx)
	if err != nil || skipped {
		t.Fatalf("忙跳过后的同 bucket 重试必须放行（skipped=%v err=%v）—— 缺陷 1 的补执行被合并吞掉", skipped, err)
	}
	if got := execs.Load(); got != 2 {
		t.Fatalf("补执行未发生：execs = %d, want 2", got)
	}

	// 欠账已消费：随后的同 bucket 触发恢复合并。
	skipped, err = r.RefreshOnceStatus(ctx)
	if err != nil || !skipped {
		t.Fatalf("欠账消费后的同 bucket 触发应恢复合并（skipped=%v err=%v）", skipped, err)
	}
	if got := execs.Load(); got != 2 {
		t.Fatalf("恢复合并后执行数不应增长：execs = %d, want 2", got)
	}

	// 欠账不会跨 bucket 泄漏：滚动后照常放行。
	advance(time.Hour)
	if skipped, _ := r.RefreshOnceStatus(ctx); skipped {
		t.Fatal("bucket 滚动后必须放行兜底执行")
	}
}

//  3. 失败不记账：执行失败后 lastBucket 必须停在**上一个**成功 bucket，
//     不能把失败记成当前 bucket 已执行 —— 否则失败后的同 bucket 重试
//     （既有失败重试语义）会被合并节流挡住。
func TestCoalescingDoesNotBlockRetryAfterFailure(t *testing.T) {
	base := time.Date(2026, 10, 5, 11, 3, 0, 0, time.UTC)
	r, execs, advance := newCoalesceTestRefresher(t, base)
	ctx := context.Background()

	var fail atomic.Bool
	r.executeFn = func(context.Context, time.Time) error {
		if fail.Load() {
			return context.DeadlineExceeded
		}
		return nil
	}

	// 上一 bucket（11:00）成功一次，lastBucket = 11:00。
	if skipped, err := r.RefreshOnceStatus(ctx); skipped || err != nil {
		t.Fatalf("首次触发应执行（skipped=%v err=%v）", skipped, err)
	}
	if got := execs.Load(); got != 1 {
		t.Fatalf("execs = %d, want 1", got)
	}

	// bucket 滚动后（12:00）触发一次执行且失败。
	advance(time.Hour) // 12:03
	fail.Store(true)
	if _, err := r.RefreshOnceStatus(ctx); err == nil {
		t.Fatal("预置失败执行体应当返回错误")
	}
	if got := execs.Load(); got != 1 {
		t.Fatalf("失败不应计入成功执行：execs = %d, want 1", got)
	}

	// 同 bucket（12:00）内的下一次触发必须重试。若失败被记成已执行
	// （lastBucket = 12:00），这里会被合并挡住 —— 那是回归。
	advance(2 * time.Minute) // 12:05，仍在 12:00 bucket 内
	fail.Store(false)
	skipped, err := r.RefreshOnceStatus(ctx)
	if err != nil || skipped {
		t.Fatalf("失败后的同 bucket 触发必须重试（skipped=%v err=%v）—— 合并节流挡住了重试", skipped, err)
	}
	if got := execs.Load(); got != 2 {
		t.Fatalf("重试执行未发生：execs = %d, want 2", got)
	}
}

// 4. skipped 语义的根：nil 接收者与包装器。
func TestRefreshOnceStatusNilReceiverAndWrapper(t *testing.T) {
	var r *AutoIndexRefresher
	skipped, err := r.RefreshOnceStatus(context.Background())
	if skipped || err != nil {
		t.Fatalf("nil 接收者应返回 (false, nil)，得到 (%v, %v)", skipped, err)
	}
	if err := r.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("nil 接收者的 RefreshOnce 应返回 nil，得到 %v", err)
	}
}
