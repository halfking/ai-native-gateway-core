package dispatch

// Stage F residual — queue-depth hot-reload (T2).
//
// A live credForwarder captured its queue depth once at construction. The
// publisher's catch-up query already fires a policy revision when
// max_queue_depth changes; these tests pin the missing consumer side:
// ApplyPolicy must resize the live forwarder via replaceDepth.

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestCredForwarderReplaceDepth exercises replaceDepth directly, without a
// running loop goroutine, so the shrink/grow buffer and in-flight preservation
// logic can be asserted deterministically.
func TestCredForwarderReplaceDepth(t *testing.T) {
	q := make(chan *QueuedRequest, 5)
	cf := &credForwarder{
		cred:   CredentialRef{CredentialID: 1, ConcurrencyMode: ModeConcurrency},
		wakeCh: make(chan struct{}),
	}
	cf.queue.Store(&q)
	cf.limit.Store(5)

	// Shrink: the channel buffer is left in place; only the admission limit
	// moves. An oversized buffer is harmless because reservations are capped
	// at cf.limit.
	cf.replaceDepth(3)
	if got := cf.Limit(); got != 3 {
		t.Fatalf("limit after shrink = %d, want 3", got)
	}
	if got := cap(*cf.queue.Load()); got != 5 {
		t.Fatalf("buffer after shrink = %d, want 5 (unchanged)", got)
	}

	// Seed 3 in-flight buffered requests, then grow to 10.
	for i := 0; i < 3; i++ {
		*cf.queue.Load() <- &QueuedRequest{ID: fmt.Sprintf("r%d", i)}
	}
	cf.replaceDepth(10)
	if got := cf.Limit(); got != 10 {
		t.Fatalf("limit after grow = %d, want 10", got)
	}
	if got := cap(*cf.queue.Load()); got != 10 {
		t.Fatalf("buffer after grow = %d, want 10", got)
	}
	// All 3 in-flight requests must be preserved in the new (larger) channel.
	got := 0
	for {
		select {
		case <-*cf.queue.Load():
			got++
		default:
			goto done
		}
	}
done:
	if got != 3 {
		t.Fatalf("preserved requests = %d, want 3", got)
	}
}

// TestApplyPolicyHotReloadsQueueDepth simulates a max_queue_depth UPDATE by
// publishing two policies with different MaxQueueDepth against a live
// forwarder, and asserts the forwarder tracks both a grow and a shrink.
func TestApplyPolicyHotReloadsQueueDepth(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	ref := CredentialRef{
		CredentialID:    7,
		ProviderID:      1,
		ConcurrencyMode: ModeConcurrency,
		ConcurrencyLimit: 2,
		MaxQueueDepth:    5,
	}
	cf := p.getOrCreateForwarder(ref)
	if cf == nil {
		t.Fatal("getOrCreateForwarder returned nil")
	}
	if got := cf.Limit(); got != 5 {
		t.Fatalf("initial forwarder limit = %d, want 5", got)
	}
	if got := cap(*cf.queue.Load()); got != 5 {
		t.Fatalf("initial forwarder buffer = %d, want 5", got)
	}

	// Grow to 12 via a published policy (the effect of a max_queue_depth UPDATE).
	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    1,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 7, Mode: ModeConcurrency, Limit: 2, MaxQueueDepth: 12},
		},
	}); err != nil {
		t.Fatalf("ApplyPolicy (grow): %v", err)
	}
	if got := cf.Limit(); got != 12 {
		t.Fatalf("forwarder limit after grow = %d, want 12", got)
	}
	if got := cap(*cf.queue.Load()); got != 12 {
		t.Fatalf("forwarder buffer after grow = %d, want 12", got)
	}

	// Shrink to 3: admission limit moves, channel buffer stays put.
	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    2,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 7, Mode: ModeConcurrency, Limit: 2, MaxQueueDepth: 3},
		},
	}); err != nil {
		t.Fatalf("ApplyPolicy (shrink): %v", err)
	}
	if got := cf.Limit(); got != 3 {
		t.Fatalf("forwarder limit after shrink = %d, want 3", got)
	}
	if got := cap(*cf.queue.Load()); got != 12 {
		t.Fatalf("forwarder buffer after shrink = %d, want 12 (unchanged)", got)
	}
}
