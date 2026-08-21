package liveactions

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newMiniredis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within timeout")
}

// TestEmitWritesBoundedRedisQueue verifies the LPUSH+LTRIM contract of 24 号
// §4: newest at head, bounded at RedisMaxLen.
func TestEmitWritesBoundedRedisQueue(t *testing.T) {
	ResetSeqForTest()
	_, rdb := newMiniredis(t)
	// Shrink the runtime LTRIM bound BEFORE the worker starts (writing it
	// while the worker runs is a data race).
	old := redisMaxLen
	redisMaxLen = 5
	defer func() { redisMaxLen = old }()
	e := NewEmitter(rdb, 0)
	defer e.Close()

	ctx := context.Background()
	e.Emit(ctx, ActionEvent{RequestID: "req-1", Action: ActionArrive})
	e.Emit(ctx, ActionEvent{RequestID: "req-1", Action: ActionRouteResolved, Model: "gpt-4o"})

	waitFor(t, func() bool {
		n, _ := rdb.LLen(ctx, RedisKey).Result()
		return n == 2
	})

	// Newest (route_resolved, seq 2) at the head.
	head, err := rdb.LIndex(ctx, RedisKey, 0).Result()
	if err != nil {
		t.Fatalf("LIndex: %v", err)
	}
	var got ActionEvent
	if err := json.Unmarshal([]byte(head), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Action != ActionRouteResolved || got.Seq != 2 || got.Model != "gpt-4o" {
		t.Fatalf("head event mismatch: %+v", got)
	}
	if got.RequestID != "req-1" || got.Ts.IsZero() {
		t.Fatalf("head event missing request_id/ts: %+v", got)
	}

	// Bounded: with the runtime LTRIM bound at 5, pushing 20 events must
	// keep the list at exactly 5 (LPUSH+LTRIM contract, 24 号 §4).
	for i := 0; i < 20; i++ {
		e.Emit(ctx, ActionEvent{RequestID: fmt.Sprintf("flood-%d", i), Action: ActionUpstreamRequest})
	}
	waitFor(t, func() bool {
		n, _ := rdb.LLen(ctx, RedisKey).Result()
		return n == 5
	})
}

// TestEmitAllThirteenActionsRoundTrip: 13 种动作各发射一条并断言按原样落到
// Redis 队列（枚举与序列化完整性）。
func TestEmitAllThirteenActionsRoundTrip(t *testing.T) {
	ResetSeqForTest()
	_, rdb := newMiniredis(t)
	e := NewEmitter(rdb, 0)
	defer e.Close()

	ctx := context.Background()
	for i, a := range Actions {
		e.Emit(ctx, ActionEvent{
			RequestID:    "req-all",
			Action:       a,
			Model:        "m",
			CredentialID: i + 1,
			ErrorKind:    "",
			RetrySeq:     i,
			Retry:        a == ActionNodeSwitch && i%2 == 0,
			Detail:       map[string]string{"k": "v"},
		})
	}
	waitFor(t, func() bool {
		n, _ := rdb.LLen(ctx, RedisKey).Result()
		return n == int64(len(Actions))
	})

	if len(Actions) != 13 {
		t.Fatalf("Actions enum must have exactly 13 entries (24 号 §2), got %d", len(Actions))
	}
	vals, err := rdb.LRange(ctx, RedisKey, 0, -1).Result()
	if err != nil {
		t.Fatalf("LRange: %v", err)
	}
	seen := map[Action]bool{}
	for _, v := range vals {
		var ev ActionEvent
		if err := json.Unmarshal([]byte(v), &ev); err != nil {
			t.Fatalf("unmarshal %s: %v", v, err)
		}
		seen[ev.Action] = true
		if ev.RequestID != "req-all" || ev.Seq == 0 {
			t.Fatalf("event missing request/seq: %+v", ev)
		}
	}
	for _, a := range Actions {
		if !seen[a] {
			t.Errorf("action %q not found in redis queue", a)
		}
	}
}

// TestSeqMonotonicPerRequest: seq 在 request_id 内单调递增，request 间独立。
func TestSeqMonotonicPerRequest(t *testing.T) {
	ResetSeqForTest()
	e := NewEmitter(nil, 0)
	defer e.Close()

	if got := NextSeq("a"); got != 1 {
		t.Fatalf("first seq = %d, want 1", got)
	}
	if got := NextSeq("a"); got != 2 {
		t.Fatalf("second seq = %d, want 2", got)
	}
	if got := NextSeq("b"); got != 1 {
		t.Fatalf("other request seq = %d, want 1", got)
	}
	_ = e
}

// TestSeqPrunedOnTerminalAction: reply/no_route 之后计数器被回收
// （同 id 再出现视为新请求，seq 从 1 重新开始）。
func TestSeqPrunedOnTerminalAction(t *testing.T) {
	ResetSeqForTest()
	e := NewEmitter(nil, 0)
	defer e.Close()
	ctx := context.Background()

	e.Emit(ctx, ActionEvent{RequestID: "t", Action: ActionArrive})
	e.Emit(ctx, ActionEvent{RequestID: "t", Action: ActionReply})
	if got := NextSeq("t"); got != 1 {
		t.Fatalf("seq after terminal = %d, want 1 (counter should be pruned)", got)
	}
}

// TestEmitChannelFullDropsImmediately: channel 满时 Emit 立即返回不阻塞，
// 丢弃计数递增。
func TestEmitChannelFullDropsImmediately(t *testing.T) {
	ResetSeqForTest()
	// No worker draining (redis nil worker still drains... use a closed
	// emitter's buffer instead): construct with buffer 2 then Close the
	// worker so nothing drains, then keep emitting through a raw send path.
	e := NewEmitter(nil, 2)
	e.Close() // worker gone; ch remains, fills up after 2 sends

	ctx := context.Background()
	// Drain racing: worker closed synchronously in Close (wg.Wait), so the
	// buffer holds 0 events now.
	before := e.DroppedTotal()
	const N = 5000
	start := time.Now()
	for i := 0; i < N; i++ {
		e.Emit(ctx, ActionEvent{RequestID: fmt.Sprintf("r-%d", i), Action: ActionArrive})
	}
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("Emit blocked: %d emits took %v", N, elapsed)
	}
	after := e.DroppedTotal()
	if after < before+N-2 {
		t.Fatalf("dropped count = %d, want >= %d", after-before, N-2)
	}
}

// TestEmitConcurrentNeverBlocks: 并发发射 + 满缓冲下无死锁。
func TestEmitConcurrentNeverBlocks(t *testing.T) {
	ResetSeqForTest()
	e := NewEmitter(nil, 8)
	defer e.Close()

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			ctx := context.Background()
			for i := 0; i < 1000; i++ {
				e.Emit(ctx, ActionEvent{
					RequestID: fmt.Sprintf("c-%d", g),
					Action:    ActionUpstreamRequest,
				})
			}
		}(g)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Emit deadlocked")
	}
}

func TestNormalizeStageCategory(t *testing.T) {
	ev := ActionEvent{Action: ActionModelEnqueued}
	normalizeStage(&ev)
	if ev.Stage != "model_queue" || ev.StageCategory != "routing" {
		t.Fatalf("normalized stage = %q/%q, want model_queue/routing", ev.Stage, ev.StageCategory)
	}

	ev = ActionEvent{Action: ActionFirstByte}
	normalizeStage(&ev)
	if ev.Stage != "streaming" || ev.StageCategory != "llm" {
		t.Fatalf("normalized stage = %q/%q, want streaming/llm", ev.Stage, ev.StageCategory)
	}
}

// TestNilEmitterIsNoOp: nil emitter 上 Emit 必须 no-op 不 panic。
func TestNilEmitterIsNoOp(t *testing.T) {
	var e *Emitter
	e.Emit(context.Background(), ActionEvent{RequestID: "x", Action: ActionArrive})
	if e.DroppedTotal() != 0 || e.RedisFailuresTotal() != 0 {
		t.Fatal("nil emitter must expose zero counters")
	}
	e.Close()
}

// TestRedisUnavailableSilentDegrade: Redis 挂掉时计数递增、Emit 不阻塞。
func TestRedisUnavailableSilentDegrade(t *testing.T) {
	ResetSeqForTest()
	mr := miniredis.RunT(t)
	// No client-side retries so the failure path is fast.
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	e := NewEmitter(rdb, 0)
	defer e.Close()

	mr.Close() // redis goes away after emitter construction

	ctx := context.Background()
	for i := 0; i < 10; i++ {
		e.Emit(ctx, ActionEvent{RequestID: fmt.Sprintf("d-%d", i), Action: ActionUpstreamRequest})
	}
	waitFor(t, func() bool { return e.RedisFailuresTotal() > 0 })
}

// TestEmptyRequestIDDroppedExceptStateChange: 非节点维度事件没有 request_id
// 时直接丢弃；state_change（节点维度）允许空 request_id。
func TestEmptyRequestIDDroppedExceptStateChange(t *testing.T) {
	ResetSeqForTest()
	_, rdb := newMiniredis(t)
	e := NewEmitter(rdb, 0)
	defer e.Close()

	ctx := context.Background()
	e.Emit(ctx, ActionEvent{Action: ActionArrive})      // dropped, no request_id
	e.Emit(ctx, ActionEvent{Action: ActionStateChange}) // kept (node dimension)
	waitFor(t, func() bool {
		n, _ := rdb.LLen(ctx, RedisKey).Result()
		return n == 1
	})
	v, err := rdb.LIndex(ctx, RedisKey, 0).Result()
	if err != nil {
		t.Fatalf("LIndex: %v", err)
	}
	var ev ActionEvent
	if err := json.Unmarshal([]byte(v), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Action != ActionStateChange {
		t.Fatalf("expected state_change, got %s", ev.Action)
	}
	if ev.Seq != 0 {
		t.Fatalf("state_change should carry seq 0 (node-dimension), got %d", ev.Seq)
	}
}

// TestCloseDrainsBuffer: Close 排空缓冲再退出，不丢已入队事件。
func TestCloseDrainsBuffer(t *testing.T) {
	ResetSeqForTest()
	_, rdb := newMiniredis(t)
	e := NewEmitter(rdb, 0)

	ctx := context.Background()
	const N = 50
	for i := 0; i < N; i++ {
		e.Emit(ctx, ActionEvent{RequestID: fmt.Sprintf("q-%d", i), Action: ActionUpstreamRequest})
	}
	e.Close() // drains remaining buffered events before returning

	n, err := rdb.LLen(ctx, RedisKey).Result()
	if err != nil {
		t.Fatalf("LLen: %v", err)
	}
	if n != N {
		t.Fatalf("queue len = %d, want %d (Close must drain the buffer)", n, N)
	}
}
