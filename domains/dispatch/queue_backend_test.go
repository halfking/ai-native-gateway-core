package dispatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// V6-W1.7 U2 (docs/架构优化v6/10-dual-backend-queue.md §2.1/§2.4): the redis
// queue backend accounts cluster admission in hash pairs (counts + hb) per
// capacity family, self-heals dead instances inside the 30s stale window and
// fail-opens on Redis outages. All invariants below map to §2.4.

func newTestRedisBackend(t *testing.T, instance string) (QueueBackend, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	b := NewRedisQueueBackend(client, instance)
	if err := b.Open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, mr, client
}

func TestRedisQueueBackendAdmitUpToCapThenReject(t *testing.T) {
	b, mr, _ := newTestRedisBackend(t, "i1")
	qr := planQR("q1")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		adm, ok := b.TryAdmitTotal(ctx, qr, 3)
		if !ok || adm.Kind != LaneTotal {
			t.Fatalf("admit %d = (%+v, %v), want admitted total token", i, adm, ok)
		}
	}
	if _, ok := b.TryAdmitTotal(ctx, qr, 3); ok {
		t.Fatalf("admit beyond cluster cap succeeded")
	}

	// Cluster occupancy is visible to a sibling instance (fresh backend,
	// same miniredis) whose admissions share the budget.
	sib := NewRedisQueueBackend(redisForMini(t, mr), "i2")
	if err := sib.Open(context.Background()); err != nil {
		t.Fatalf("sibling open: %v", err)
	}
	defer sib.Close()
	if _, ok := sib.TryAdmitTotal(ctx, planQR("q2"), 3); ok {
		t.Fatalf("sibling admitted into a full cluster waiting room")
	}

	// Release on the first instance frees the slot for the sibling
	// (admit/release symmetry, invariant 2).
	b.Release(Admission{Kind: LaneTotal, Key: queueTotalSlot})
	if _, ok := sib.TryAdmitTotal(ctx, planQR("q3"), 3); !ok {
		t.Fatalf("sibling still rejected after a release")
	}
}

func TestRedisQueueBackendReleaseFloorsAtZero(t *testing.T) {
	b, _, _ := newTestRedisBackend(t, "i1")
	// Double release of one token must not drive the family negative.
	tok, ok := b.TryReserveLane(context.Background(), LaneModel, "m1", 2)
	if !ok {
		t.Fatalf("reserve failed")
	}
	b.Release(tok)
	b.Release(tok) // idempotent no-op server-side
	tok2, ok := b.TryReserveLane(context.Background(), LaneModel, "m1", 1)
	if !ok {
		t.Fatalf("release overshot: family still counted as occupied")
	}
	b.Release(tok2)
}

// Invariant 3 (crash self-heal): a dead instance's counts are swept from the
// cluster sum inside the stale window — simulated by planting a heartbeat
// older than queueStaleWindowMS.
func TestRedisQueueBackendCrashSelfHeal(t *testing.T) {
	b, mr, client := newTestRedisBackend(t, "live")
	ctx := context.Background()

	// "Dead" instance i-dead holds 2 admissions with a stale heartbeat.
	counts, hb := queueFamilyKeys(queueTotalSlot)
	if err := client.HSet(ctx, counts, "i-dead", 2).Err(); err != nil {
		t.Fatalf("plant counts: %v", err)
	}
	staleMS := time.Now().UnixMilli() - 2*queueStaleWindowMS
	if err := client.HSet(ctx, hb, "i-dead", staleMS).Err(); err != nil {
		t.Fatalf("plant hb: %v", err)
	}

	// The live instance's admit sweeps the dead entry and succeeds against
	// the healed sum (2 phantom admissions must not consume the budget).
	if _, ok := b.TryAdmitTotal(ctx, planQR("q1"), 2); !ok {
		t.Fatalf("dead instance's phantom counts still gate admission")
	}
	if got, err := client.HGet(ctx, counts, "i-dead").Result(); err == nil && got != "" {
		t.Fatalf("dead instance counts not swept: %q", got)
	}
	_ = mr
}

// §2.2 fail-open: with Redis unreachable the backend admits (local bounds
// apply) and reports degraded; recovery clears the flag (D5).
func TestRedisQueueBackendFailOpen(t *testing.T) {
	b, mr, _ := newTestRedisBackend(t, "i1")
	ctx := context.Background()
	qr := planQR("q1")

	mr.Close() // simulate Redis outage

	if adm, ok := b.TryAdmitTotal(ctx, qr, 1); !ok || adm.Kind != "" {
		t.Fatalf("outage must fail-open, got (%+v, %v)", adm, ok)
	}
	stats, err := b.Snapshot(ctx)
	if err == nil || !stats.Degraded {
		t.Fatalf("backend should report degraded after outage, got %+v err=%v", stats, err)
	}
}

func TestRedisQueueBackendDueZSET(t *testing.T) {
	b, _, client := newTestRedisBackend(t, "i1")
	ctx := context.Background()
	due := time.Now().Add(time.Minute)

	if err := b.ParkDue(ctx, "r1", due); err != nil {
		t.Fatalf("park: %v", err)
	}
	n, err := client.ZCard(ctx, queueDueKey).Result()
	if err != nil || n != 1 {
		t.Fatalf("due zset card = %d err=%v, want 1", n, err)
	}
	score, err := client.ZScore(ctx, queueDueKey, "i1|r1").Result()
	if err != nil || int64(score) != due.UnixMilli() {
		t.Fatalf("due score = %v err=%v, want %d", score, err, due.UnixMilli())
	}
	if err := b.ClearDue(ctx, "r1"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n, _ := client.ZCard(ctx, queueDueKey).Result(); n != 0 {
		t.Fatalf("due zset not cleared: %d", n)
	}
}

// Local backend: pass-through admission, no-op everything else (U1).
func TestLocalQueueBackendPassThrough(t *testing.T) {
	b := NewLocalQueueBackend()
	ctx := context.Background()
	if err := b.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	if adm, ok := b.TryAdmitTotal(ctx, planQR("l1"), 0); !ok || adm != (Admission{}) {
		t.Fatalf("local admit = (%+v, %v), want zero-token admit", adm, ok)
	}
	if adm, ok := b.TryReserveLane(ctx, LaneModel, "m", 1); !ok || adm != (Admission{}) {
		t.Fatalf("local lane reserve = (%+v, %v), want zero-token", adm, ok)
	}
	b.Release(Admission{Kind: LaneTotal})
	if err := b.Heartbeat(ctx); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	stats, err := b.Snapshot(ctx)
	if err != nil || stats.Kind != QueueBackendLocal {
		t.Fatalf("snapshot = %+v err=%v", stats, err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func redisForMini(t *testing.T, mr *miniredis.Miniredis) *redis.Client {
	t.Helper()
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

// TestPipelineClusterQueueBackendIntegration (U3): two pipelines (instances)
// share one Redis; the CLUSTER Tier-0 cap and lane caps are enforced across
// them. Layout with DispatcherWorkers=0 + MaxQueueDepth=1: q1 parks the
// model drainer on the unbuffered dispatchIn, q2 fills the model lane (and
// holds the cluster model slot), q3 must then park in the Tier-0 FIFO —
// holding the cluster's only total admission.
func TestPipelineClusterQueueBackendIntegration(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redisForMini(t, mr)

	cfg := DefaultConfig()
	cfg.DispatcherWorkers = 0
	cfg.MaxQueueDepth = 1
	cfg.TotalQueueCapacity = 1
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)

	deps := Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{cred(1, ModeConcurrency, 1)}, nil
		},
		ModelResolveFunc: func(ctx context.Context, requested string, tried []string) (string, []string, error) {
			return requested, nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		HotCfg: hotCfg,
	}
	newP := func(inst string) *Pipeline {
		p := NewPipeline(deps)
		b := NewRedisQueueBackend(client, inst)
		if err := b.Open(context.Background()); err != nil {
			t.Fatalf("open backend %s: %v", inst, err)
		}
		p.SetQueueBackend(b)
		p.Start()
		return p
	}
	p1 := newP("i1")
	p2 := newP("i2")
	t.Cleanup(func() { p1.Stop(); p2.Stop() })

	submitAsync := func(p *Pipeline, id string) (*QueuedRequest, <-chan error, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		qr := NewQueuedRequest(id, "t", "m", ctx, nil)
		ch := make(chan error, 1)
		go func() {
			_, err := p.Submit(ctx, qr)
			ch <- err
		}()
		return qr, ch, cancel
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("state never reached (%s)", what)
	}
	snapOf := func(p *Pipeline) QueueBackendStats {
		stats, _ := p.queueBackend.Snapshot(context.Background())
		return stats
	}
	// modelLaneDepth reads the lane depth under the pipeline's own locks (the
	// request objects in flight are single-owner — never poll them directly
	// from the test goroutine, that races the stage writes).
	modelLaneDepth := func(p *Pipeline, name string) int64 {
		p.modelMu.Lock()
		mq, ok := p.models[name]
		p.modelMu.Unlock()
		if !ok {
			return -1 // lane not created yet
		}
		mq.mu.Lock()
		defer mq.mu.Unlock()
		return mq.depth.Load()
	}
	// settled reports the local queues EMPTY and STABLE: five consecutive
	// reads 10ms apart show Tier-0 empty AND the model lane existing-but-
	// empty. The transit window (popped from the FIFO, not yet in the lane)
	// is nanoseconds and cannot span the whole settle window.
	settled := func(p *Pipeline) bool {
		for i := 0; i < 5; i++ {
			if p.totalQueue.depth() != 0 || modelLaneDepth(p, "m") != 0 {
				return false
			}
			time.Sleep(10 * time.Millisecond)
		}
		return true
	}

	// Serialize the setup with stable signals (never touching in-flight
	// requests): q1 parks the model drainer on the unbuffered dispatchIn;
	// q2 fills the model lane and holds the cluster model slot (ModelHeld
	// stays 1 — the drainer never pops it); q3 then meets the full model
	// lane, backpressures, and parks in the Tier-0 FIFO holding the
	// cluster's only total admission.
	_, _, c1 := submitAsync(p1, "q1")
	defer c1()
	waitFor("q1 drained into the stuck model drainer", func() bool {
		return settled(p1)
	})
	_, _, c2 := submitAsync(p1, "q2")
	defer c2()
	waitFor("q2 parked in the model lane", func() bool {
		s := snapOf(p1)
		return s.TotalHeld == 0 && s.ModelHeld == 1
	})
	_, done3, c3 := submitAsync(p1, "q3")
	defer c3()

	// Wait until the cluster budget is fully held by i1's parked request.
	waitFor("q3 holds the cluster total slot", func() bool {
		return snapOf(p1).ClusterTotal >= 1
	})

	// Cross-instance refusal: i2's local queues are all empty, but the
	// CLUSTER waiting room is full.
	ctx4, cancel4 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel4()
	_, err := p2.Submit(ctx4, NewQueuedRequest("q4", "t", "m", ctx4, nil))
	var overflow *OverflowError
	if !errors.As(err, &overflow) || overflow.Reason != "total_queue_full" {
		t.Fatalf("cross-instance cluster cap not enforced, err = %v", err)
	}

	// Releasing the holder (client cancels q3) returns the cluster slot.
	c3()
	select {
	case <-done3:
	case <-time.After(2 * time.Second):
		t.Fatalf("q3 submit never returned after cancel")
	}
	releaseDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(releaseDeadline) {
		if stats, err := p1.queueBackend.Snapshot(context.Background()); err == nil && stats.ClusterTotal == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// i2 is admitted again — it flows past admission and parks in i2's own
	// lane path (deadline, not overflow: the cluster lane slot is still
	// held by q2, but the model-lane backpressure loop is LOCAL behavior).
	ctx5, cancel5 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel5()
	_, err = p2.Submit(ctx5, NewQueuedRequest("q5", "t", "m", ctx5, nil))
	if errors.As(err, &overflow) {
		t.Fatalf("q5 refused after the cluster slot was released: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("q5 should park until its ctx deadline after admission, got %v", err)
	}
}

// TestPipelineQueueBackendFailOpen (§2.2): with Redis gone mid-flight the
// pipeline keeps serving — admission degrades to local bounds.
func TestPipelineQueueBackendFailOpen(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redisForMini(t, mr)
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": {cred(1, ModeConcurrency, 1)}},
		forwardFn: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{Result: "ok"}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	b := NewRedisQueueBackend(client, "i-failopen")
	if err := b.Open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	p.SetQueueBackend(b)
	p.Start()
	defer p.Stop()

	mr.Close() // Redis outage

	res, err := p.Submit(context.Background(), NewQueuedRequest("fo1", "t", "m", context.Background(), nil))
	if err != nil {
		t.Fatalf("fail-open submit must succeed on local bounds, got %v", err)
	}
	if res != "ok:cred1:call1" {
		t.Fatalf("unexpected result %v", res)
	}
	stats, _ := b.Snapshot(context.Background())
	if !stats.Degraded {
		t.Fatalf("backend should be marked degraded during the outage")
	}
}
