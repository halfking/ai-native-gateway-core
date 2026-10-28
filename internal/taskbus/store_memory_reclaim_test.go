package taskbus

// r1041 轮（2026-10-28）：MemoryStore 的**回收**与**扫描成本**门。
//
// ★ 起因：独立审计（verifier）实测 ——
//
//	enqueued=200000 sent=200000 rows_still_retained=200000
//	单次 ClaimBatch 扫描 6.1 ms，而 dispatcher **每 100 ms** 轮询一次
//	保留载荷 781 MiB
//
// ★ 两个独立缺陷：
//
//	① **回收缺失**：`MarkSent` 只把 Status 改成 sent，**不删条目**
//	   ⇒ byID / byKey / claimed 三个 map 随流量**单调增长**
//	   ⇒ 生产挂载点 `cmd/gateway/main_pipeline.go` 的 `cache_save` 任务
//	   载荷是**完整上游响应体** ⇒ 内存无界增长。
//	② **扫描成本**：`ClaimBatch` 每轮**全表扫** `byID`
//	   ⇒ 已完成的任务仍在表里 ⇒ 每次轮询的成本随累计流量**线性劣化**
//	   ⇒ 100ms 轮询 ⇒ 每秒 10 次全扫。
//
// ★ 修法：★ 给 MemoryStore 加**保留窗口 + 回收**：
//   · `MarkSent` 后进入「已完成」状态，由 sweeper 按保留窗口清理；
//   · `ClaimBatch` **只遍历未完成的条目**（维护一个 pending 索引），
//     不再全表扫。
// ⇒ ⇒ ★ 两项都要有**可验证的断言**（本文件），且**不许**只断言「状态变了」。

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestMemoryStoreReleasesCompletedTasks：★ 已完成的任务必须被回收。
//
// ★ 断言用【条目数】而不是【状态】—— ★ 只断言状态会漏掉「状态对但条目还在」
// ★ ⇒ 那正是缺陷本身（内存照样涨）。
func TestMemoryStoreReleasesCompletedTasks(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now()

	const n = 500
	for i := 0; i < n; i++ {
		if _, err := s.Enqueue(ctx, "cache.save.v1", fmt.Sprintf("key-%d", i), 1, []byte("payload")); err != nil {
			t.Fatalf("Enqueue #%d 失败：%v", i, err)
		}
	}
	batch, err := s.ClaimBatch(ctx, n, now)
	if err != nil {
		t.Fatalf("ClaimBatch 失败：%v", err)
	}
	if len(batch) != n {
		t.Fatalf("ClaimBatch 取到 %d 条，期望 %d", len(batch), n)
	}
	for _, task := range batch {
		if err := s.MarkSent(ctx, task.ID); err != nil {
			t.Fatalf("MarkSent(%d) 失败：%v", task.ID, err)
		}
	}

	// ★ 回收是【按保留窗口】的 ⇒ 立刻查应当还留着（窗口内可查，用于排障）。
	if got := s.retainedCount(); got != n {
		t.Errorf("保留窗口内的已完成任务数 = %d，期望 %d ⇒ 保留窗口语义被破坏", got, n)
	}

	// ★ 窗口过期后必须被清掉。
	s.sweep(now.Add(2 * time.Hour))
	if got := s.retainedCount(); got != 0 {
		t.Errorf("★ 保留窗口过后仍保留 %d 条已完成任务 ⇒ 内存会随流量单调增长（★ 这就是 P0 缺陷）", got)
	}
}

// TestMemoryStoreClaimBatchDoesNotScanCompletedTasks：★ ClaimBatch 不得扫已完成任务。
//
// ★ 判据不是「跑得快」（★ 那是性能断言、机器相关），
// ★ 而是【结构性】：★ 已完成的任务**不得出现在被遍历的集合里**。
func TestMemoryStoreClaimBatchDoesNotScanCompletedTasks(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now()

	// 先灌 1000 条已完成任务。
	for i := 0; i < 1000; i++ {
		if _, err := s.Enqueue(ctx, "cache.save.v1", fmt.Sprintf("k-%d", i), 1, []byte("p")); err != nil {
			t.Fatalf("Enqueue 失败：%v", err)
		}
	}
	batch, err := s.ClaimBatch(ctx, 1000, now)
	if err != nil {
		t.Fatalf("ClaimBatch 失败：%v", err)
	}
	for _, task := range batch {
		if err := s.MarkSent(ctx, task.ID); err != nil {
			t.Fatalf("MarkSent 失败：%v", err)
		}
	}

	// ★ 核心断言：★ **在全部任务完成之后**再调一次 ClaimBatch，
	//   它 **遍历到的候选数**必须为 0
	//   —— ★ 若实现仍是全表扫 byID，则它会遍历到 1000 条（哪怕都因状态不符被跳过）。
	//   ★ ⚠️ 注意判读顺序：★ 上面的那次 ClaimBatch 是**灌完立刻调用**的
	//   ★（此时任务尚未完成 ⇒ 它们当然都是候选）⇒ ★ 那次的读数**不能**用来判这条。
	if _, err := s.ClaimBatch(ctx, 10, now); err != nil {
		t.Fatalf("ClaimBatch 失败：%v", err)
	}
	if got := s.candidateScanCount(); got != 0 {
		t.Errorf("★ 全部任务完成后，ClaimBatch 仍遍历了 %d 条候选，期望 0 条\n"+
			"  ⇒ 实现仍在全表扫 byID ⇒ 每 100ms 一次的轮询成本随累计流量线性劣化\n"+
			"  ⇒ ★ 修法：维护「未完成」索引，ClaimBatch 只遍历它",
			got)
	}
}

// TestMemoryStoreSweepKeepsPendingTasks：★ 回收不得误伤未完成任务。
func TestMemoryStoreSweepKeepsPendingTasks(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now()

	if _, err := s.Enqueue(ctx, "cache.save.v1", "pending-key", 1, []byte("p")); err != nil {
		t.Fatalf("Enqueue 失败：%v", err)
	}
	task, err := s.Enqueue(ctx, "cache.save.v1", "sent-key", 1, []byte("p"))
	if err != nil {
		t.Fatalf("Enqueue 失败：%v", err)
	}
	s.MarkSent(ctx, task.ID)

	s.sweep(now.Add(2 * time.Hour))

	// ★ pending 的那条必须还在。
	if _, err := s.ClaimBatch(ctx, 10, now); err != nil {
		t.Fatalf("ClaimBatch 失败：%v", err)
	} else if got := s.pendingCount(); got != 1 {
		t.Errorf("回收后未完成任务数 = %d，期望 1 ⇒ 回收误伤了未完成任务", got)
	}
}

// TestMemoryStoreSweepReleasesIdempotencyKeys：★ 回收必须同时释放 byKey
// （★ 否则幂等键会一直占着内存 ⇒ 而幂等键要保留多久是另一个问题）。
func TestMemoryStoreSweepReleasesIdempotencyKeys(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now()

	task, err := s.Enqueue(ctx, "cache.save.v1", "idem-key", 1, []byte("p"))
	if err != nil {
		t.Fatalf("Enqueue 失败：%v", err)
	}
	s.MarkSent(ctx, task.ID)
	s.sweep(now.Add(2 * time.Hour))

	if s.hasIdempotencyKey("idem-key") {
		t.Errorf("★ 回收后幂等键仍在 ⇒ byKey 随流量单调增长（★ 同一个内存泄漏的另一半）")
	}
}

// TestDispatcherTriggersSweep：★ 生产路径上回收器**真的会被调用**。
//
// ★ 为什么要有这条：★ 上一版只测了 `Sweep` 这个函数本身 ⇒
// ★ 「函数对，但没人调它」是完全可能的（★ 这正是本轮缺陷的形态：
// ★ 旧实现里回收逻辑**根本不存在**，而它的替代品是「永不删」）。
// ⇒ ⇒ ★ 必须证明【Dispatcher 的轮询循环】会触发它。
func TestDispatcherTriggersSweep(t *testing.T) {
	store := NewMemoryStore()
	// ★ 保留窗口设为极短 ⇒ 便于断言「已被回收」。
	store.Retention = time.Millisecond

	reg := NewRegistry()
	reg.Register("cache.save.v1", HandlerFunc(func(ctx context.Context, task Task) error { return nil }))

	d := NewDispatcher(DispatcherConfig{
		Store:       store,
		Registry:    reg,
		PollInterval: time.Millisecond,
		BatchSize:   64,
		SweepEvery:  1, // ★ 每轮都回收（便于断言）
	})

	// 先灌一批并投递完（全部成功 ⇒ 进入 sent 终态）。
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if _, err := store.Enqueue(ctx, "cache.save.v1", fmt.Sprintf("d-key-%d", i), 1, []byte("p")); err != nil {
			t.Fatalf("Enqueue 失败：%v", err)
		}
	}
	if _, err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce 失败：%v", err)
	}

	// ★ 等保留窗口过期，然后跑一轮 ⇒ 回收应当发生。
	time.Sleep(5 * time.Millisecond)
	if _, err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce（第二轮）失败：%v", err)
	}
	if got := store.retainedCount(); got != 0 {
		t.Errorf("★ 两轮 DispatchOnce 后仍保留 %d 条已完成任务 ⇒ "+
			"★ dispatcher 的轮询循环【没有触发回收】⇒ 生产路径上内存照样无界增长", got)
	}
}
