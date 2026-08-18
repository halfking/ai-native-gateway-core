package bg

import (
	"context"
	"testing"
	"time"
)

// TestStartFastProbeConsumerDrainsQueue reproduces the 2026-08-18 154
// production failure mode at unit scale: with LLM_GATEWAY_USE_NEW_PROBE_MODE
// =true the legacy Start() is skipped, and nothing consumed
// fastReprobeQueue. PeriodicQuotaProbe/BalanceQuotaProbe kept submitting,
// the 64-slot queue filled, and every quota-recovery probe was dropped
// ("fast probe queue full") — quota-exhausted credentials never recovered
// even after the upstream window reset.
//
// The probe delay is set long so the drained goroutines park on their delay
// and never touch the (nil in this test) database.
func TestStartFastProbeConsumerDrainsQueue(t *testing.T) {
	t.Setenv("LLM_GATEWAY_CRED_PROBE_V2_FAST_REPROBE_DELAY", "1h")
	t.Setenv("LLM_GATEWAY_CRED_PROBE_V2_INTERVAL", "1h")

	c := NewCredentialProbeV2(nil, nil)

	// Without a consumer the queue caps at 64 and further submissions drop.
	for i := 0; i < 128; i++ {
		c.SubmitFastProbe(i + 1)
	}
	if got := c.fastProbeQueueLen(); got != 64 {
		t.Fatalf("queue should cap at 64 without a consumer, got %d", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartFastProbeConsumer(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for c.fastProbeQueueLen() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("StartFastProbeConsumer did not drain the queue: %d items left", c.fastProbeQueueLen())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// After draining, new submissions are consumed instead of piling up.
	c.SubmitFastProbe(999)
	deadline = time.Now().Add(5 * time.Second)
	for c.fastProbeQueueLen() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("post-drain submission was not consumed: %d items left", c.fastProbeQueueLen())
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	c.Stop()
}

// TestStartFastProbeConsumerSkipsCycleAll verifies the consumer-only mode
// does not run the legacy hourly cycleAll scan (that is what
// USE_NEW_PROBE_MODE=true intends to skip) while still accepting queue
// submissions. cycleAll would query the DB; with a nil pool it would panic,
// so reaching the drain without a panic is itself the assertion.
func TestStartFastProbeConsumerSkipsCycleAll(t *testing.T) {
	t.Setenv("LLM_GATEWAY_CRED_PROBE_V2_FAST_REPROBE_DELAY", "1h")
	t.Setenv("LLM_GATEWAY_CRED_PROBE_V2_INTERVAL", "1h")

	c := NewCredentialProbeV2(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartFastProbeConsumer(ctx)
	defer c.Stop()

	c.SubmitFastProbe(42)

	deadline := time.Now().Add(5 * time.Second)
	for c.fastProbeQueueLen() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("submission not consumed: %d items left", c.fastProbeQueueLen())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !c.started || c.stopped {
		t.Fatalf("consumer-only mode must mark the worker started (ProbeNowAsync gate), started=%v stopped=%v", c.started, c.stopped)
	}
}

// TestStartFastProbeConsumerLifecycle guards the double-start / stop
// contract shared with Start().
func TestStartFastProbeConsumerLifecycle(t *testing.T) {
	t.Setenv("LLM_GATEWAY_CRED_PROBE_V2_FAST_REPROBE_DELAY", "1h")
	t.Setenv("LLM_GATEWAY_CRED_PROBE_V2_INTERVAL", "1h")

	c := NewCredentialProbeV2(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	c.StartFastProbeConsumer(ctx)
	c.StartFastProbeConsumer(ctx) // second start must be a no-op, not a panic
	cancel()
	c.Stop()
	c.Stop() // idempotent
}
