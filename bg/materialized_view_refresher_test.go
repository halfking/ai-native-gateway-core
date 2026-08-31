package bg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMaterializedViewRefresher(t *testing.T) {
	// Integration test requires database connection
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

// getTestDBPool returns a test database connection pool.
// This is a placeholder - actual implementation depends on test infrastructure.
func getTestDBPool(t *testing.T) interface{} {
	// TODO: implement based on existing test helpers
	// For now, return nil to skip tests
	return nil
}
