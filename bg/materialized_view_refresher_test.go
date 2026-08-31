package bg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
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
