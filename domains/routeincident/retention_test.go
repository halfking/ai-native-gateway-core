package routeincident

// 2026-10-18：事件留存 worker 的单元判据（不依赖真库的部分）。
//
// 这里只钉「未启用 ⇒ 什么都不做」这一条不变量。
// 涉及真库行为的部分在 retention_integration_test.go（build tag: integration）。

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRetentionDisabledDoesNothing：retention <= 0 时必须**完全不碰库**。
//
// ★ 为什么这条要在单元层钉住：worker 是「默认不启用」的组件，
// 若某次重构让它在 retention=0 时也去查库/删行，
// 那是**在未授权的情况下动生产数据** —— 必须在最便宜的一层挡住。
func TestRetentionDisabledDoesNothing(t *testing.T) {
	// 故意传 nil pool：只要它碰到库就会 panic，
	// 于是「没碰库」这件事由运行时而不是由断言来保证。
	w := NewEventRetentionWorker(nil, 0)

	res, err := w.CleanupOnce(context.Background())
	if err != nil {
		t.Fatalf("CleanupOnce on disabled worker: %v", err)
	}
	if res.EventsDeleted != 0 {
		t.Errorf("EventsDeleted = %d, want 0 on a disabled worker", res.EventsDeleted)
	}
	if res.SkippedNoIndex {
		t.Error("SkippedNoIndex = true, want false — 未启用时应先短路，" +
			"不能走到索引探测（那会要求 pool 非 nil）")
	}

	// 负值同样视为未启用。
	w2 := NewEventRetentionWorker(nil, -time.Hour)
	if res2, err := w2.CleanupOnce(context.Background()); err != nil || res2.EventsDeleted != 0 {
		t.Fatalf("negative retention should behave as disabled: res=%+v err=%v", res2, err)
	}
}

// TestRetentionBuildersOnlyOverrideOnPositive：链式配置不得被 0/负值改写。
//
// 这类 setter 是测试与运维调节的口子，一旦允许 0 写进去，
// 就会同时关掉 batch 上限（0 → 每批 0 行 → 无限循环）与周期。
func TestRetentionBuildersOnlyOverrideOnPositive(t *testing.T) {
	w := NewEventRetentionWorker(nil, time.Hour)
	beforeInterval, beforeBatch := w.interval, w.batchSize

	w.WithInterval(0).WithInterval(-time.Minute)
	if w.interval != beforeInterval {
		t.Errorf("WithInterval(0/-) changed interval to %v, want %v", w.interval, beforeInterval)
	}

	w.WithBatchSize(0).WithBatchSize(-1)
	if w.batchSize != beforeBatch {
		t.Errorf("WithBatchSize(0/-) changed batchSize to %v, want %v", w.batchSize, beforeBatch)
	}

	// 正值必须真的生效，否则链式配置形同虚设。
	w.WithInterval(time.Minute).WithBatchSize(7)
	if w.interval != time.Minute {
		t.Errorf("WithInterval(1m) did not apply, got %v", w.interval)
	}
	if w.batchSize != 7 {
		t.Errorf("WithBatchSize(7) did not apply, got %v", w.batchSize)
	}
}

// TestHeldByPeerIsNotContextError：errHeldByPeer 必须与 context 错误可区分。
//
// 调用方据此决定「记 INFO 让给对端」还是「记 WARN 失败」；
// 两者混在一起会让「双实例并行」被误报成故障，噪声淹没真信号。
func TestHeldByPeerIsNotContextError(t *testing.T) {
	if errors.Is(errHeldByPeer, context.Canceled) {
		t.Error("errHeldByPeer 不得与 context.Canceled 混淆")
	}
	if errors.Is(errHeldByPeer, context.DeadlineExceeded) {
		t.Error("errHeldByPeer 不得与 context.DeadlineExceeded 混淆")
	}
}
