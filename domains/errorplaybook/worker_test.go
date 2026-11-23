package errorplaybook

// Worker.Start 的循环语义钉测（2026-11-23 审计轮补）。
//
// ★ 为什么单独要这条：既有 worker 测试都直接调 RunOnce，**从不经过 Start** ⇒
//   「Start 的函数体是不是循环」没有任何测试守。而 bg.SpawnLoop 的监督器对
//   runFn 的「正常返回」语义是「有意退出、不重启」（bg/base_worker.go supervise）
//   ⇒ 一个「跑一轮就返回」的函数体会让 worker 每个进程生命周期只跑一轮。
//   生产上（211）这被高频部署掩盖成「每 7–15 分钟一轮」，静默数小时时无人察觉。

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// countingSource 每次FetchWindow 都给出同一个未知错误类型，并计数。
type countingSource struct {
	calls atomic.Int64
}

func (c *countingSource) FetchWindow(ctx context.Context, since, until time.Time) ([]Event, time.Time, error) {
	c.calls.Add(1)
	return []Event{{ErrorType: "worker_loop_probe_kind", Occurrences: 1}}, until, nil
}

// Start 之后 worker 必须一轮接一轮地跑；取消 ctx 之后必须停。
func TestWorkerStartLoopsAcrossRounds(t *testing.T) {
	store := newFakeStore()
	src := &countingSource{}
	// 没有 LLM ⇒ 未知错误只计数、不分析；每轮都走完并 MarkRun。
	engine := NewEngine(store, &fakeRegistry{}, nil, nil)
	w := NewWorker(engine, src, func() Config { return cfgAuto() }, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for src.calls.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if n := src.calls.Load(); n < 5 {
		t.Fatalf("worker must keep taking rounds: want >=5 FetchWindow calls within 3s, got %d "+
			"(a one-shot body exits after the first round — bg.SpawnLoop does not restart a runFn that returns)", n)
	}
	if engine.LastRun().IsZero() {
		t.Fatal("completed rounds must advance last_run_at")
	}

	// 取消 ctx ⇒ 当前这一轮走完后循环退出，不再有新的轮次。
	cancel()
	time.Sleep(50 * time.Millisecond)
	settled := src.calls.Load()
	time.Sleep(150 * time.Millisecond)
	if got := src.calls.Load(); got != settled {
		t.Fatalf("worker must stop after ctx cancel: calls went %d -> %d", settled, got)
	}
}
