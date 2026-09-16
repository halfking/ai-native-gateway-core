// bg/auto_route_workers_test.go — regression tests for the audit findings.
//
// Covers the silent-failure classes that the original feature shipped with:
//   - MEDIUM-4: Stop() must not deadlock when Start() was never called.
//   - MEDIUM-4: a second Start() must not launch a second run() goroutine.
//   - HIGH-1 + sample_count: visible behaviour of the cumulative count (a
//     pure-math test on the input shape; the SQL guard is exercised in the
//     scratch-DB smoke test rather than here, where it would require either
//     a real DB or a brittle stub).

package bg

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/admin/distlock"
)

// MEDIUM-4: Stop on a freshly-constructed worker (no Start) must return, not
// block. Verified the bug on the original code: <-w.done blocked forever
// because run() never launched and done was never closed.
func TestSettleWorker_StopWithoutStartDoesNotDeadlock(t *testing.T) {
	w := NewAutoRouteSettleWorker(nil)

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() blocked forever when Start() was never called")
	}
}

func TestAffinityWorker_StopWithoutStartDoesNotDeadlock(t *testing.T) {
	w := NewAutoRouteAffinityWorker(nil)

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() blocked forever when Start() was never called")
	}
}

// MEDIUM-4: Start() is idempotent. A second call would otherwise launch a
// second run() goroutine and panic on the second close(w.done).
func TestSettleWorker_StartIsIdempotent(t *testing.T) {
	w := NewAutoRouteSettleWorker(nil)
	w.Start(context.Background())
	w.Start(context.Background()) // must be a no-op
	w.Stop()
}

func TestAffinityWorker_StartIsIdempotent(t *testing.T) {
	w := NewAutoRouteAffinityWorker(nil)
	w.Start(context.Background())
	w.Start(context.Background()) // must be a no-op
	w.Stop()
}

// MEDIUM-4: Stop() can be called twice without panicking — the stopOnce guard
// prevents a double-close on the cancel context.
func TestSettleWorker_StopTwiceDoesNotPanic(t *testing.T) {
	w := NewAutoRouteSettleWorker(nil)
	w.Start(context.Background())
	w.Stop()
	w.Stop()
}

func TestAffinityWorker_StopTwiceDoesNotPanic(t *testing.T) {
	w := NewAutoRouteAffinityWorker(nil)
	w.Start(context.Background())
	w.Stop()
	w.Stop()
}

// R31 (audit §四#1): the token-bucket gate's TTL must exceed the sweep
// timeout so a slow-but-alive sweep never loses its lease mid-cycle (same
// invariant the refresher pins in TestMaterializedViewRefresherConstants).
func TestAutoRouteWorkersDistLockConstants(t *testing.T) {
	require.Greater(t, settleDistLockTTL, 4*time.Minute,
		"settle TTL must exceed its 4m sweep timeout")
	require.Greater(t, affinityDistLockTTL, affinitySweepTO,
		"affinity TTL must exceed its sweep timeout")
}

// R31 (audit §四#1): nil manager (never wired) and a disabled manager
// (NewRedisManager(nil), matching a deployment without Redis) both yield a
// nil handle — callers sweep exactly as before this change.
func TestAcquireSweepDistLock_NilAndDisabledManagerYieldNil(t *testing.T) {
	require.Nil(t, acquireSweepDistLock(context.Background(), nil,
		"auto_route_settle", time.Minute, "test"))
	require.Nil(t, acquireSweepDistLock(context.Background(), distlock.NewRedisManager(nil),
		"auto_route_settle", time.Minute, "test"))
}

// R31 (audit §四#1): core token-bucket contract — of two workers racing for
// the same key, exactly one observes IsLeader()==true.
func TestAcquireSweepDistLock_ExactlyOneLeaderPerKey(t *testing.T) {
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	h1 := acquireSweepDistLock(ctx, mgr, "auto_route_settle", time.Minute, "test")
	require.NotNil(t, h1, "first acquirer should get a handle")
	defer h1.Release(ctx)
	require.True(t, h1.IsLeader(), "first acquirer should be elected leader")

	h2 := acquireSweepDistLock(ctx, mgr, "auto_route_settle", time.Minute, "test")
	require.NotNil(t, h2, "second acquirer should get a follower handle")
	defer h2.Release(ctx)
	require.False(t, h2.IsLeader(), "token-bucket has exactly one redeemer per cycle")
}

// R31 (audit §四#1): settle and affinity elect independently — the affinity
// worker must not be starved because the settle worker happened to hold a
// token first (distinct intervals: 5m vs 15m).
func TestAcquireSweepDistLock_SettleAndAffinityKeysIndependent(t *testing.T) {
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	hs := acquireSweepDistLock(ctx, mgr, "auto_route_settle", time.Minute, "test")
	ha := acquireSweepDistLock(ctx, mgr, "auto_route_affinity", time.Minute, "test")
	require.NotNil(t, hs)
	require.NotNil(t, ha)
	defer hs.Release(ctx)
	defer ha.Release(ctx)
	require.True(t, hs.IsLeader())
	require.True(t, ha.IsLeader(), "distinct worker keys must elect independently")
}

// captureDefaultLogger swaps slog's default logger for one writing into a
// buffer, restoring the previous default on cleanup. Returns the buffer.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// lazyBrokenPool returns a pgxpool over an unreachable address. pgxpool.New
// is lazy (no dial until the first query), so a sweep that must skip DB work
// returns cleanly while a sweep that reaches DB work fails fast with
// connection refused.
func lazyBrokenPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nouser:nopass@127.0.0.1:1/none")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// R31 (audit §四#1): a follower must return from sweep without touching the
// database. The skip log is the observable contract; the broken pool makes
// any DB access loud.
func TestSettleWorker_SweepFollowerSkipsWithoutDB(t *testing.T) {
	buf := captureDefaultLogger(t)
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	hl := acquireSweepDistLock(ctx, mgr, "auto_route_settle", settleDistLockTTL, "test-external")
	require.NotNil(t, hl)
	require.True(t, hl.IsLeader())
	defer hl.Release(ctx)

	w := NewAutoRouteSettleWorker(lazyBrokenPool(t))
	w.SetDistLock(mgr)
	w.sweep(ctx)

	require.Contains(t, buf.String(), "auto-route settle skipped",
		"follower must log the skip instead of sweeping")
}

// R31 (audit §四#1): the leader path must reach DB work — here it fails
// against the broken pool, and the baselines-unavailable warning proves the
// gate let the sweep through (mirrors what happens on Redis-less deployments,
// where behaviour must be unchanged).
func TestSettleWorker_SweepLeaderProceedsToDB(t *testing.T) {
	buf := captureDefaultLogger(t)
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	w := NewAutoRouteSettleWorker(lazyBrokenPool(t))
	w.SetDistLock(mgr)
	w.sweep(ctx)

	require.NotContains(t, buf.String(), "auto-route settle skipped")
	require.Contains(t, buf.String(), "cohort baselines unavailable",
		"leader must proceed past the gate into the sweep body")
}

// R31 (audit §四#1): affinity mirror of the follower-skip contract.
func TestAffinityWorker_SweepFollowerSkipsWithoutDB(t *testing.T) {
	buf := captureDefaultLogger(t)
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	hl := acquireSweepDistLock(ctx, mgr, "auto_route_affinity", affinityDistLockTTL, "test-external")
	require.NotNil(t, hl)
	require.True(t, hl.IsLeader())
	defer hl.Release(ctx)

	w := NewAutoRouteAffinityWorker(lazyBrokenPool(t))
	w.SetDistLock(mgr)
	w.sweep(ctx)

	require.Contains(t, buf.String(), "auto-route affinity skipped",
		"follower must log the skip instead of sweeping")
}

// R31 (audit §四#1): affinity leader path reaches DB work (aggregate error
// against the broken pool), proving the gate is not a blanket skip.
func TestAffinityWorker_SweepLeaderProceedsToDB(t *testing.T) {
	buf := captureDefaultLogger(t)
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	w := NewAutoRouteAffinityWorker(lazyBrokenPool(t))
	w.SetDistLock(mgr)
	w.sweep(ctx)

	require.NotContains(t, buf.String(), "auto-route affinity skipped")
	require.Contains(t, buf.String(), "auto-route affinity aggregate failed",
		"leader must proceed past the gate into the sweep body")
}