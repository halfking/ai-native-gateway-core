// bg/routing_metrics_aggregator_test.go — lifecycle hardening for the
// routing metrics aggregator. The aggregation SQL itself is verified against
// a live database (migration 670 schema); a nil pool must simply no-op.

package bg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

// R31 pattern alignment (audit §四#1): the aggregator's distlock TTL must
// exceed its 4-minute sweep timeout so a slow-but-alive sweep never loses
// its lease mid-cycle (same invariant as settleDistLockTTL).
func TestRoutingMetricsAggregator_DistLockTTLExceedsSweepTimeout(t *testing.T) {
	require.Greater(t, routingMetricsDistLockTTL, 4*time.Minute,
		"aggregator TTL must exceed its 4m sweep timeout")
}

// R31 pattern alignment: a follower must return from sweep without touching
// the database. The skip log is the observable contract; the broken pool
// makes any DB access loud.
func TestRoutingMetricsAggregator_SweepFollowerSkipsWithoutDB(t *testing.T) {
	buf := captureDefaultLogger(t)
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	hl := acquireSweepDistLock(ctx, mgr, "routing_metrics_aggregate", routingMetricsDistLockTTL, "test-external")
	require.NotNil(t, hl)
	require.True(t, hl.IsLeader())
	defer hl.Release(ctx)

	w := NewRoutingMetricsAggregator(lazyBrokenPool(t))
	w.SetDistLock(mgr)
	w.sweep(ctx)

	require.Contains(t, buf.String(), "routing metrics aggregation skipped",
		"follower must log the skip instead of sweeping")
}

// R31 pattern alignment: the leader path must reach DB work — here it fails
// against the broken pool, and the sweep-failed warning proves the gate let
// the sweep through (mirrors Redis-less deployments, where behaviour must be
// unchanged).
func TestRoutingMetricsAggregator_SweepLeaderProceedsToDB(t *testing.T) {
	buf := captureDefaultLogger(t)
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	w := NewRoutingMetricsAggregator(lazyBrokenPool(t))
	w.SetDistLock(mgr)
	w.sweep(ctx)

	require.NotContains(t, buf.String(), "routing metrics aggregation skipped")
	require.Contains(t, buf.String(), "routing metrics aggregation sweep failed",
		"leader must proceed past the gate into the sweep body")
}

// R31 pattern alignment: the aggregator elects on its own key — a settle
// worker holding its token must not starve the aggregator (distinct cadence
// expectations per worker).
func TestRoutingMetricsAggregator_DistLockKeyIndependentFromSettle(t *testing.T) {
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	hs := acquireSweepDistLock(ctx, mgr, "auto_route_settle", time.Minute, "test")
	require.NotNil(t, hs)
	defer hs.Release(ctx)
	require.True(t, hs.IsLeader())

	ha := acquireSweepDistLock(ctx, mgr, "routing_metrics_aggregate", time.Minute, "test")
	require.NotNil(t, ha)
	defer ha.Release(ctx)
	require.True(t, ha.IsLeader(), "distinct worker keys must elect independently")
}
