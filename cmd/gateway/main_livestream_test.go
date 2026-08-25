package main

// 2026-08-25 (实时流解耦): liveStreamEmittedForwarder 的定向测试。
//
// forwarder 是 telemetry onEmitted (请求热路径, 同步触发) 与 hub.Publish
// (含 ≤200ms 的 provider DB 查找) 之间的解耦层:
//
//   - TestLiveStreamEmittedForwarder_EmitDeliversShallowCopy — emit 投递的是
//     entry 的浅拷贝: telemetry worker 后续原地改写原 entry 不会污染已投递
//     的 in_progress 投影。
//   - TestLiveStreamEmittedForwarder_EmitNonBlockingAndDrops — 队列满时
//     emit 立即返回 (select-default), 丢弃计数递增。
//   - TestLiveStreamEmittedForwarder_RunForwardsToHubAndStops — run 消费
//     entries 并 hub.Publish (经 hub 公开的 Stats().broadcast_count 观察),
//     stop() 幂等且让 run 退出。
//   - TestLiveStreamEmittedForwarder_NilHubIsNoOp — hub 未接线时 emit/stop
//     均为安全 no-op。

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func TestLiveStreamEmittedForwarder_EmitDeliversShallowCopy(t *testing.T) {
	hub := admin.NewLiveStreamSSEHub(nil, admin.LiveStreamConfig{BroadcastQueueSize: 8})
	f := newLiveStreamEmittedForwarder(hub)

	orig := &telemetry.RequestLogEntry{RequestID: "req-copy", TenantID: "tenant-a"}
	f.emit(orig)

	select {
	case got := <-f.entries:
		if got == orig {
			t.Fatal("emit must enqueue a copy, not the caller's pointer")
		}
		if got.RequestID != "req-copy" || got.TenantID != "tenant-a" {
			t.Fatalf("copy lost fields: %+v", got)
		}
		// Mutating the caller's entry after emit must not bleed into the
		// queued copy (the telemetry worker rewrites entries in place after
		// the DB queue dequeue).
		orig.RequestID = "mutated"
		if got.RequestID != "req-copy" {
			t.Fatalf("queued copy was mutated through the caller's entry: %q", got.RequestID)
		}
	default:
		t.Fatal("emit must deliver the entry to the queue")
	}
}

func TestLiveStreamEmittedForwarder_EmitNonBlockingAndDrops(t *testing.T) {
	hub := admin.NewLiveStreamSSEHub(nil, admin.LiveStreamConfig{BroadcastQueueSize: 8})
	f := newLiveStreamEmittedForwarder(hub)

	// Fill the queue with no consumer running.
	for i := 0; i < liveStreamEmittedForwarderCapacity; i++ {
		f.entries <- &telemetry.RequestLogEntry{RequestID: "fill"}
	}

	start := time.Now()
	f.emit(&telemetry.RequestLogEntry{RequestID: "overflow"})
	if elapsed := time.Since(start); elapsed >= 50*time.Millisecond {
		t.Fatalf("emit must not block on a full queue, took %v", elapsed)
	}
	if got := f.dropped.Load(); got != 1 {
		t.Fatalf("dropped = %d, want 1 (overflow entry must be counted)", got)
	}
	if got := len(f.entries); got != liveStreamEmittedForwarderCapacity {
		t.Fatalf("queue length = %d, want %d (dropped entry must not be enqueued)", got, liveStreamEmittedForwarderCapacity)
	}
}

func TestLiveStreamEmittedForwarder_RunForwardsToHubAndStops(t *testing.T) {
	hub := admin.NewLiveStreamSSEHub(nil, admin.LiveStreamConfig{BroadcastQueueSize: 8})
	f := newLiveStreamEmittedForwarder(hub)
	go hub.Run()
	defer hub.Stop()

	exited := make(chan struct{})
	go func() {
		f.run()
		close(exited)
	}()

	// Entry without credential/provider/canonical ids keeps the hub's DB
	// lookups on their nil-fast paths; the point here is the plumbing, not
	// the projection details.
	f.emit(&telemetry.RequestLogEntry{RequestID: "req-forward", TenantID: "tenant-a"})

	// run() must consume the entry and hub.Publish it. The broadcast counter
	// is observable through the hub's public Stats() (nil-Redis hub: Publish
	// only enqueues the local broadcast, no Redis writes).
	deadline := time.Now().Add(3 * time.Second)
	for {
		if n, ok := hub.Stats()["broadcast_count"].(int64); ok && n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("forwarder never published the emitted entry, stats=%v", hub.Stats())
		}
		time.Sleep(2 * time.Millisecond)
	}

	// stop is idempotent and terminates the consumer goroutine.
	f.stop()
	f.stop()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not exit after stop()")
	}
}

func TestLiveStreamEmittedForwarder_NilHubIsNoOp(t *testing.T) {
	f := newLiveStreamEmittedForwarder(nil)
	f.emit(&telemetry.RequestLogEntry{RequestID: "req-nil"}) // must not panic
	f.stop()
	f.stop()
	if got := f.dropped.Load(); got != 0 {
		t.Fatalf("dropped = %d, want 0 for a nil-hub forwarder", got)
	}
	if got := len(f.entries); got != 0 {
		t.Fatalf("queue length = %d, want 0 for a nil-hub forwarder", got)
	}
}

// 2026-08-25 (审计修正回归): emitted 与 persisted 必须经同一条 FIFO —— 同一
// request 的 in_progress 投影先入队(请求热路径 t0)、终态补偿后入队(telemetry
// worker 落库后 t1>t0), 单消费者按序 Publish, 前端 last-write-wins 不会被
// 乱序回退。此处验证机制核心: 两路投递进同一 chan 且顺序保持。
func TestLiveStreamEmittedForwarder_PersistSharesFIFOWithEmit(t *testing.T) {
	hub := admin.NewLiveStreamSSEHub(nil, admin.LiveStreamConfig{BroadcastQueueSize: 8})
	f := newLiveStreamEmittedForwarder(hub)

	inProgress := telemetry.RequestLogEntry{RequestID: "req-ord", TenantID: "tenant-a"}
	terminal := telemetry.RequestLogEntry{RequestID: "req-ord", TenantID: "tenant-a", Success: true}

	f.emit(&inProgress)
	f.persist(&terminal)
	// 另一 request 的 emitted 排在其后, 不得插队到 req-ord 的终态之前。
	f.emit(&telemetry.RequestLogEntry{RequestID: "req-ord-2", TenantID: "tenant-a"})

	var order []string
	for i := 0; i < 3; i++ {
		select {
		case got := <-f.entries:
			stage := "in_progress"
			if got.Success {
				stage = "terminal"
			}
			order = append(order, got.RequestID+":"+stage)
		case <-time.After(2 * time.Second):
			t.Fatalf("entry %d missing from FIFO, got %v", i, order)
		}
	}
	want := []string{"req-ord:in_progress", "req-ord:terminal", "req-ord-2:in_progress"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("FIFO order = %v, want %v (terminal must not precede its in_progress)", order, want)
		}
	}

	// persist 的条目同样被消费者 Publish(经 broadcast_count 观察, nil-Redis
	// hub 的 Publish 只入本地广播队列)。顺序验证已消费掉队列内容, 这里重新
	// 投递同样的三条让 run() 走完整链路。
	f.emit(&inProgress)
	f.persist(&terminal)
	f.emit(&telemetry.RequestLogEntry{RequestID: "req-ord-2", TenantID: "tenant-a"})
	go hub.Run()
	defer hub.Stop()
	exited := make(chan struct{})
	go func() { f.run(); close(exited) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if n, ok := hub.Stats()["broadcast_count"].(int64); ok && n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("forwarder never published persisted entries, stats=%v", hub.Stats())
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.stop()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not exit after stop()")
	}
}
