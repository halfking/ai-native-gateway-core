package sessioncontext

// parallel_panic_test.go —— worker 协程里 panic 的隔离（2026-11-23，
// 12h 变更审计 A/C）。
//
// 背景：归约的 work 是外部注入的守卫（能否并发由注入方声明）。一次空指针
// （实测形态：类型化 nil 的 Redis 客户端穿过两道 `== nil` 闸 ⇒ client.Get(...)
// 解引用空指针）就足以让 work panic。
//
// ★ worker 协程里的 panic **不会**沿调用栈回到请求协程 ⇒ 没有 recover 时
// 它直接升级为**进程级崩溃**（整个网关挂掉，而不是这条请求失败）。
// 本文件钉住三条契约：
//   ① panic 被捕获 ⇒ runIndexed 正常返回；
//   ② panic 不静默吞掉 ⇒ 折成错误返回 + 结构化错误日志；
//   ③ panic 不影响其它 worker ⇒ 它们照常处理自己的条目。

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// captureLogs 把 slog 默认 logger 换成写进 buffer 的那个（测试期间有效）。
//
// ★ 本包没有 t.Parallel() 的用例 ⇒ 换全局 logger 不会与其它用例打架。
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestRunIndexedRecoversWorkerPanic —— panic 被捕获、折成错误、留下日志。
func TestRunIndexedRecoversWorkerPanic(t *testing.T) {
	logs := captureLogs(t)
	ctx := context.Background()

	var processed atomic.Int64
	var panicked atomic.Bool

	idx, err := runIndexed(ctx, 4, 64,
		func(i int) (any, error) { return i, nil },
		func(i int, _ any) error {
			if i == 7 {
				panicked.Store(true)
				panic("boom from worker")
			}
			processed.Add(1)
			return nil
		})

	// ① 函数正常返回（进程没死）且 ② panic 没有被静默吞掉。
	if err == nil {
		t.Fatal("worker panic 必须折成错误返回，不能静默吞掉")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("错误里应说明是 panic: %v", err)
	}
	if !panicked.Load() {
		t.Fatal("测试前提不成立：work 应当真的 panic 过")
	}
	// 「返回下标最小的错误」这一既有契约保持不变。
	if idx != 7 {
		t.Fatalf("idx = %d, want 7（应返回下标最小的错误）", idx)
	}
	// panic 之前的条目都处理完了（0..6 共 7 条）。
	if got := processed.Load(); got < 7 {
		t.Fatalf("panic 之前的条目应已处理, processed = %d, want >= 7", got)
	}
	// ② 不得静默吞掉：必须有结构化错误日志（含下标与栈）。
	out := logs.String()
	if !strings.Contains(out, "reduce worker panicked") {
		t.Fatalf("缺少 panic 的结构化日志: %s", out)
	}
	if !strings.Contains(out, "index=7") {
		t.Fatalf("日志里应带 panic 的下标: %s", out)
	}
	if !strings.Contains(out, "stack=") {
		t.Fatalf("日志里应带 panic 的栈: %s", out)
	}
}

// TestRunIndexedWorkerPanicKeepsOtherWorkersAlive —— 一个 worker panic 不
// 影响其它 worker（它们照常处理自己的条目）。
func TestRunIndexedWorkerPanicKeepsOtherWorkersAlive(t *testing.T) {
	captureLogs(t)
	ctx := context.Background()

	var mu sync.Mutex
	done := map[int]bool{}
	mark := func(i int) { mu.Lock(); done[i] = true; mu.Unlock() }

	idx, err := runIndexed(ctx, 4, 64,
		func(i int) (any, error) { return i, nil },
		func(i int, _ any) error {
			if i == 0 {
				// 给其它 worker 留出时间把手上那条做完（确定性，不靠运气）。
				time.Sleep(30 * time.Millisecond)
				panic("boom from worker")
			}
			mark(i)
			return nil
		})
	if err == nil {
		t.Fatal("worker panic 必须折成错误返回")
	}
	if idx != 0 {
		t.Fatalf("idx = %d, want 0", idx)
	}

	mu.Lock()
	others := 0
	for i := 1; i < 64; i++ {
		if done[i] {
			others++
		}
	}
	mu.Unlock()
	if others == 0 {
		t.Fatal("其它 worker 应当照常处理自己的条目（一个 worker 的 panic 不得拖垮它们）")
	}
}
