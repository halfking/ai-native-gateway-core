package dispatch

import (
	"context"
	"testing"
)

// A capacity retry that ends in a successful enqueue must clear the capacity
// flag: onRetryDue treats CapacityRetryCount>0 as "re-dispatch across all
// credentials", so a stale counter turns every later same-credential error
// retry into a full re-route (failover.go onRetryDue). Regression: 2026-08-24
// minimax-m3 concurrency audit — the counter was never reset after the
// capacity wait succeeded.
func TestDispatchClearsCapacityRetryCountOnSuccessfulEnqueue(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": {credWithProvider(1, 10, 4)}},
		forwardFn: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("cap-reset-1", "tenant", "m", context.Background(), nil)
	qr.GatewayInstanceID = "gw-test"
	qr.CapacityRetryCount = 3 // as if the request already waited on saturated queues

	p.dispatch(qr)

	if qr.CapacityRetryCount != 0 {
		t.Fatalf("CapacityRetryCount = %d after successful enqueue, want 0", qr.CapacityRetryCount)
	}
}
