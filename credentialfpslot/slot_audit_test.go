package credentialfpslot

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"
)

// acquireFailureValue reads the current value of the
// llmgw_fpslot_acquire_failures_total counter for a given reason. Counters are
// process-global and accumulate across tests in a binary, so callers compare
// before/after deltas rather than absolute values.
func acquireFailureValue(t *testing.T, reason string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := slotAcquireFailures.WithLabelValues(reason).Write(m); err != nil {
		t.Fatalf("read acquire_failures{%s}: %v", reason, err)
	}
	return m.GetCounter().GetValue()
}

// preemptValue reads the current value of the
// llmgw_fpslot_preempt_events_total counter.
func preemptValue(t *testing.T) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := slotPreemptEvents.Write(m); err != nil {
		t.Fatalf("read preempt events: %v", err)
	}
	return m.GetCounter().GetValue()
}

// TestAcquire_ClassifiesRedisErrorNotSaturation is the regression for
// AUDIT_CROSSCUTTING_CONCURRENCY_20260813.md §3-S2. When the Redis backing
// store fails (here: a closed miniredis), Acquire must classify the outcome as
// a Redis error, not as "all slots active". Before the tri-state fix both cases
// incremented the saturated counter, so a Redis outage looked like credential
// exhaustion on the dashboard.
func TestAcquire_ClassifiesRedisErrorNotSaturation(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	m := New(Config{DefaultLimit: 2, Enabled: true}, client)

	limit := 2
	ctx := context.Background()
	// Warm-up: prove the pool works while Redis is up.
	if _, ok := m.Acquire(ctx, 1, &limit, "h1", "default"); !ok {
		t.Fatal("warm-up Acquire should succeed while Redis is up")
	}

	beforeSaturated := acquireFailureValue(t, "saturated")
	beforeRedisErr := acquireFailureValue(t, "redis_error")

	// Tear down the Redis backing store. Every subsequent LRU EVAL errors.
	mr.Close()

	if _, ok := m.Acquire(ctx, 1, &limit, "h2", "default"); ok {
		t.Fatal("Acquire must fail when Redis is down")
	}

	afterSaturated := acquireFailureValue(t, "saturated")
	afterRedisErr := acquireFailureValue(t, "redis_error")

	if afterSaturated != beforeSaturated {
		t.Errorf("Redis-down Acquire miscounted as saturated: before=%v after=%v (redis_error must absorb it)",
			beforeSaturated, afterSaturated)
	}
	if afterRedisErr <= beforeRedisErr {
		t.Errorf("Redis-down Acquire did not increment redis_error: before=%v after=%v", beforeRedisErr, afterRedisErr)
	}
}

// TestAcquire_EmitsPreemptMetricOnLRUPreempt is the regression for
// AUDIT_CROSSCUTTING_CONCURRENCY_20260813.md §3-S3. slotPreemptEvents was
// registered but never incremented (recordPreempt had zero live callers).
// An LRU preemption must now increment it so preemption rate is observable.
func TestAcquire_EmitsPreemptMetricOnLRUPreempt(t *testing.T) {
	// Use the miniredis instance so we can FastForward its clock past the
	// active gate, making h1's slot deterministically preemptable.
	m, mr := newTestManager(t, Config{DefaultLimit: 1, Enabled: true, ActiveGateSeconds: 1})
	limit := 1
	ctx := context.Background()

	before := preemptValue(t)

	// h1 takes the single slot (TTL = slotTTLSeconds = 1800).
	acquireSuccess(t, m, ctx, 1, &limit, "h1", "default")
	// Advance miniredis' clock so the slot is idle past the 1s gate:
	// idle = slotTTL - remaining = 1800 - (1800-2) = 2 >= gate(1) → preemptable.
	mr.FastForward(2 * time.Second)

	// h2 is a different holder — must LRU-preempt h1's now-idle slot.
	if _, ok := m.Acquire(ctx, 1, &limit, "h2", "default"); !ok {
		t.Fatal("h2 should preempt h1's idle slot past the active gate")
	}

	after := preemptValue(t)
	if after <= before {
		t.Fatalf("LRU preempt did not increment slotPreemptEvents: before=%v after=%v", before, after)
	}
}
