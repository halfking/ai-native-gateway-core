// bg/routing_metrics_aggregator_test.go — lifecycle hardening for the
// routing metrics aggregator. The aggregation SQL itself is verified against
// a live database (migration 670 schema); a nil pool must simply no-op.

package bg

import (
	"context"
	"testing"
	"time"
)

func TestRoutingMetricsAggregator_NilPoolStopDoesNotBlock(t *testing.T) {
	w := NewRoutingMetricsAggregator(nil)
	w.Start(context.Background())
	// Stop must return even though run() never launched a sweep: it is
	// parked on the first-delay timer and exits via ctx cancellation.
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked on a never-swept worker")
	}
}

func TestRoutingMetricsAggregator_StopIdempotent(t *testing.T) {
	w := NewRoutingMetricsAggregator(nil)
	w.Start(context.Background())
	w.Start(context.Background()) // second Start must be a no-op, not a second goroutine
	w.Stop()
	w.Stop() // double Stop must be safe
}

func TestRoutingMetricsAggregator_NilPoolSweepNoop(t *testing.T) {
	w := NewRoutingMetricsAggregator(nil)
	// Must not panic and must not touch any DB.
	w.sweep(context.Background())
}
