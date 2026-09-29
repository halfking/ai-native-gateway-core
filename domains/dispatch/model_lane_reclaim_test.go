package dispatch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// R28-Q-2 (round 31): the per-model lane map must be bounded and idle lanes
// must be reclaimed, without ever stranding a request in an undrained
// channel.

func newLaneTestPipeline(cfg Config) *Pipeline {
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)
	return NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{cred(1, ModeConcurrency, 1)}, nil
		},
		ModelResolveFunc: func(ctx context.Context, requested string, tried []string) (string, []string, error) {
			return "m", nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		HotCfg: hotCfg,
	})
}

func TestModelLaneCapRejectsAdmission(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxModelLanes = 2
	cfg.ModelLaneIdleSeconds = 0 // disable reclaim so the cap is what fires
	p := newLaneTestPipeline(cfg)
	p.Start()
	defer p.Stop()

	if p.getOrCreateModelQueue("m1") == nil || p.getOrCreateModelQueue("m2") == nil {
		t.Fatal("first two lanes must be created")
	}
	if p.getOrCreateModelQueue("m3") != nil {
		t.Fatal("third lane must be rejected at the cap")
	}
	// An EXISTING lane keeps working (the cap bounds distinct lanes, not use).
	if p.getOrCreateModelQueue("m1") == nil {
		t.Fatal("existing lane must still be usable")
	}
}

func TestModelLaneIdleReclaimAndFreshLane(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelLaneIdleSeconds = 1
	p := newLaneTestPipeline(cfg)
	p.Start()
	defer p.Stop()

	first := p.getOrCreateModelQueue("m-idle")
	if first == nil {
		t.Fatal("lane creation failed")
	}
	// Wait past the idle TTL: the drainer must deregister the empty lane.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p.modelMu.Lock()
		_, ok := p.models["m-idle"]
		p.modelMu.Unlock()
		if !ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.modelMu.Lock()
	_, stillThere := p.models["m-idle"]
	p.modelMu.Unlock()
	if stillThere {
		t.Fatal("idle lane was not reclaimed")
	}
	if !first.closed {
		t.Fatal("reclaimed lane must be marked closed")
	}
	// Next request lazily creates a FRESH lane (not the closed one).
	second := p.getOrCreateModelQueue("m-idle")
	if second == nil || second == first || second.closed {
		t.Fatal("fresh lane expected after reclaim")
	}
}

// A straggler that already holds a reclaimed lane's reference must be
// refused (not silently enqueued into an undrained channel).
func TestEnqueueModelClosedLaneRefused(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelLaneIdleSeconds = 0
	p := newLaneTestPipeline(cfg)
	p.Start()
	defer p.Stop()

	mq := p.getOrCreateModelQueue("m-closed")
	if mq == nil {
		t.Fatal("lane creation failed")
	}
	// Simulate the idle-reclaimed state directly.
	mq.mu.Lock()
	mq.closed = true
	mq.mu.Unlock()

	qr := NewQueuedRequest("straggler", "t", "m-closed", context.Background(), nil)
	// enqueueModel must retry on a fresh lane and succeed there.
	if !p.enqueueModel("m-closed", qr) {
		t.Fatal("enqueue must succeed via a fresh lane after reclaim")
	}
	p.modelMu.Lock()
	live := p.models["m-closed"]
	p.modelMu.Unlock()
	if live == nil || live == mq || live.closed {
		t.Fatal("retry must have created a fresh live lane")
	}
}
