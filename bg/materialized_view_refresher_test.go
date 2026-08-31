package bg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/admin/distlock"
)

// TestMaterializedViewRefresherConstants pins the timing contract the rest
// of the system relies on: cycles must not overlap (timeout < interval),
// and consumers (admin.mvFreshnessBudget = 15min) tolerate one missed
// cycle, which requires interval < freshness budget.
func TestMaterializedViewRefresherConstants(t *testing.T) {
	require.Greater(t, RefreshInterval, time.Duration(0))
	require.Greater(t, InitialDelay, time.Duration(0))
	require.Less(t, RefreshTimeout, RefreshInterval,
		"refresh timeout must stay below the interval so cycles cannot pile up")
	require.LessOrEqual(t, RefreshInterval, 15*time.Minute,
		"interval above 15min would exceed the admin freshness budget and force constant fallbacks")
}

// TestMaterializedViewRefresher_StopDuringInitialDelay verifies Stop()
// returns promptly while the loop is still in its initial delay. The
// original implementation used time.Sleep for the delay, hanging Stop()
// (and therefore graceful shutdown) for up to InitialDelay.
func TestMaterializedViewRefresher_StopDuringInitialDelay(t *testing.T) {
	refresher := NewMaterializedViewRefresher(nil)
	refresher.Start()

	done := make(chan struct{})
	go func() {
		refresher.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success: cancelled during the initial delay.
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not complete within 5s during initial delay — initial delay is not interruptible")
	}
}

// TestMaterializedViewRefresher_TriggerRefreshNilDB ensures the manual
// trigger is safe without a database (wiring tests, disabled deployments).
func TestMaterializedViewRefresher_TriggerRefreshNilDB(t *testing.T) {
	refresher := NewMaterializedViewRefresher(nil)
	require.NoError(t, refresher.TriggerRefresh(context.Background()))
}

// TestMaterializedViewRefresher is the integration variant: it exercises a
// real refresh cycle when TEST_DATABASE_URL is provisioned, otherwise (and
// in -short mode) it skips.
func TestMaterializedViewRefresher(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pool := getTestDBPool(t)
	if pool == nil {
		t.Skip("test database not available")
	}

	ctx := context.Background()

	t.Run("refresh_views_exist", func(t *testing.T) {
		refresher := NewMaterializedViewRefresher(pool)

		// Trigger manual refresh
		err := refresher.TriggerRefresh(ctx)
		require.NoError(t, err, "manual refresh should succeed")

		// Verify views were refreshed by checking refreshed_at timestamp
		var refreshedAt time.Time
		err = pool.QueryRow(ctx, `
			SELECT MAX(refreshed_at) FROM routing_analytics_7d
		`).Scan(&refreshedAt)

		if err == nil {
			// View exists and has data
			age := time.Since(refreshedAt)
			require.Less(t, age, 2*time.Minute,
				"routing_analytics_7d should have been refreshed recently")
		}
	})

	t.Run("start_stop", func(t *testing.T) {
		refresher := NewMaterializedViewRefresher(pool)

		// Start the refresher
		refresher.Start()

		// Let it run briefly
		time.Sleep(100 * time.Millisecond)

		// Stop should complete quickly
		done := make(chan struct{})
		go func() {
			refresher.Stop()
			close(done)
		}()

		select {
		case <-done:
			// Success
		case <-time.After(5 * time.Second):
			t.Fatal("Stop() did not complete within 5 seconds")
		}
	})
}

// getTestDBPool returns a pool over TEST_DATABASE_URL (the repo's standard
// integration-test DSN, see cmd/license-authority tests), or nil so callers
// skip when no test database is provisioned.
func getTestDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		return nil
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New(TEST_DATABASE_URL): %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newDistLockForTest spins up an in-memory miniredis-backed distlock.Manager
// so the token-bucket coordination path can be exercised without a real
// Redis or Postgres — mirrors admin/distlock's own test helper.
func newDistLockForTest(t *testing.T) distlock.Manager {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return distlock.NewRedisManager(rdb)
}

// TestMaterializedViewRefresher_DistLockNilFallsBackToAdvisory verifies that
// a refresher with no distLock wired takes the Postgres advisory lock path
// (useAdvisoryLock stays true) — i.e. behaviour is unchanged for anyone who
// never calls SetDistLock, preserving the pre-2026-09-01 contract.
func TestMaterializedViewRefresher_DistLockNilFallsBackToAdvisory(t *testing.T) {
	refresher := NewMaterializedViewRefresher(nil)
	require.Nil(t, refresher.acquireDistLock(context.Background()),
		"no distLock wired must yield a nil handle so callers fall back to advisory lock")
}

// TestMaterializedViewRefresher_DistLockDisabledManagerFallsBack verifies
// that a distlock.Manager whose Enabled() reports false (e.g. constructed
// with a nil *redis.Client, matching a deployment with no Redis configured)
// is treated the same as no manager at all.
func TestMaterializedViewRefresher_DistLockDisabledManagerFallsBack(t *testing.T) {
	refresher := NewMaterializedViewRefresher(nil)
	refresher.SetDistLock(distlock.NewRedisManager(nil))
	require.Nil(t, refresher.acquireDistLock(context.Background()),
		"a disabled distlock.Manager must yield a nil handle so callers fall back to advisory lock")
}

// TestMaterializedViewRefresher_DistLockLeaderElection verifies the core
// token-bucket contract: of two refreshers racing for the same Redis lock,
// exactly one observes IsLeader()==true and the other observes false —
// mirroring what refreshAll uses to decide whether to run REFRESH at all.
func TestMaterializedViewRefresher_DistLockLeaderElection(t *testing.T) {
	mgr := newDistLockForTest(t)

	r1 := NewMaterializedViewRefresher(nil)
	r1.SetDistLock(mgr)
	r2 := NewMaterializedViewRefresher(nil)
	r2.SetDistLock(mgr)

	ctx := context.Background()
	h1 := r1.acquireDistLock(ctx)
	require.NotNil(t, h1, "first acquirer should get a handle")
	defer h1.Release(ctx)
	require.True(t, h1.IsLeader(), "first acquirer should be elected leader")

	h2 := r2.acquireDistLock(ctx)
	require.NotNil(t, h2, "second acquirer should get a handle (follower)")
	defer h2.Release(ctx)
	require.False(t, h2.IsLeader(), "second acquirer must not also be leader — token-bucket has exactly one redeemer per cycle")
}

// TestMaterializedViewRefresher_DistLockReleaseFreesToken verifies that
// releasing the leader's handle frees the Redis key so the *next* refresh
// cycle (a fresh Acquire call, as refreshAll performs on every tick) can
// elect a new leader — this is what keeps the token bucket flowing every
// RefreshInterval instead of wedging on the first winner forever.
func TestMaterializedViewRefresher_DistLockReleaseFreesToken(t *testing.T) {
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	r1 := NewMaterializedViewRefresher(nil)
	r1.SetDistLock(mgr)
	h1 := r1.acquireDistLock(ctx)
	require.NotNil(t, h1)
	require.True(t, h1.IsLeader())
	h1.Release(ctx)

	r2 := NewMaterializedViewRefresher(nil)
	r2.SetDistLock(mgr)
	h2 := r2.acquireDistLock(ctx)
	require.NotNil(t, h2)
	defer h2.Release(ctx)
	require.True(t, h2.IsLeader(), "next cycle's Acquire must be able to win the token once the previous leader released it")
}

// TestMaterializedViewRefresher_RefreshAllSkipsWhenFollower is an
// integration-flavoured check that refreshAll actually consults
// acquireDistLock and returns early for a follower without touching the
// database (pool stays nil — a real REFRESH would panic/nil-deref if
// reached). Requires a real database for the leader path, so it only runs
// the follower short-circuit, which is DB-independent by construction.
func TestMaterializedViewRefresher_RefreshAllSkipsWhenFollower(t *testing.T) {
	mgr := newDistLockForTest(t)
	ctx := context.Background()

	// Pin the token with an external holder so our refresher under test is
	// guaranteed to observe a follower handle.
	holder := NewMaterializedViewRefresher(nil)
	holder.SetDistLock(mgr)
	holderHandle := holder.acquireDistLock(ctx)
	require.NotNil(t, holderHandle)
	require.True(t, holderHandle.IsLeader())
	defer holderHandle.Release(ctx)

	follower := NewMaterializedViewRefresher(nil) // nil db: would fail loudly if refreshView were reached
	follower.SetDistLock(mgr)

	done := make(chan struct{})
	go func() {
		follower.refreshAll(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Success: refreshAll returned without touching the nil db pool.
	case <-time.After(5 * time.Second):
		t.Fatal("refreshAll did not return promptly for a follower — expected an immediate skip")
	}
}
