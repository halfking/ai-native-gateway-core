package sessionarchive

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestArchiver_Archive tests archival of old inactive summaries.
func TestArchiver_Archive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	dsn := os.Getenv("TEST_DB_URL")
	if dsn == "" {
		t.Skip("TEST_DB_URL not set")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	archiver := NewArchiver(pool)
	archiver.SetInactivityThreshold(7 * 24 * time.Hour) // 7 days
	archiver.SetSessionEndThreshold(7 * 24 * time.Hour) // 7 days

	ctx := context.Background()

	// Setup: create test summaries
	now := time.Now()
	oldTime := now.Add(-10 * 24 * time.Hour)   // 10 days ago
	recentTime := now.Add(-3 * 24 * time.Hour) // 3 days ago

	testSessions := []struct {
		key              string
		lastRequestAt    time.Time
		lastAccessedAt   *time.Time
		shouldBeArchived bool
	}{
		{"test-archive-1", oldTime, &oldTime, true},        // old session, old access → archive
		{"test-archive-2", oldTime, nil, true},             // old session, never accessed → archive
		{"test-archive-3", oldTime, &recentTime, false},    // old session, recent access → keep
		{"test-archive-4", recentTime, &recentTime, false}, // recent session → keep
	}

	for _, ts := range testSessions {
		_, err := pool.Exec(ctx, `
			INSERT INTO session_summaries (
				session_key, tenant_id, first_request_at, last_request_at,
				last_accessed_at, request_count
			) VALUES ($1, $2, $3, $4, $5, 1)
			ON CONFLICT (session_key) DO UPDATE SET
				last_request_at = EXCLUDED.last_request_at,
				last_accessed_at = EXCLUDED.last_accessed_at
		`, ts.key, "test-tenant", ts.lastRequestAt, ts.lastRequestAt, ts.lastAccessedAt)
		require.NoError(t, err)
	}

	defer func() {
		for _, ts := range testSessions {
			pool.Exec(ctx, "DELETE FROM session_summaries WHERE session_key = $1", ts.key)
		}
	}()

	// Run archival
	result, err := archiver.Archive(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, result.ArchivedCount) // test-archive-1 and test-archive-2

	// Verify archived status
	for _, ts := range testSessions {
		var archivedAt *time.Time
		err := pool.QueryRow(ctx, `
			SELECT archived_at FROM session_summaries WHERE session_key = $1
		`, ts.key).Scan(&archivedAt)
		require.NoError(t, err)

		if ts.shouldBeArchived {
			assert.NotNil(t, archivedAt, "session %s should be archived", ts.key)
		} else {
			assert.Nil(t, archivedAt, "session %s should not be archived", ts.key)
		}
	}

	// Test stats
	stats, err := archiver.GetStats(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, stats.ArchivedSummaries, int64(2))
	assert.Greater(t, stats.TotalSummaries, int64(0))
}

// TestArchiver_NilDB tests that Archiver with nil db is graceful no-op.
func TestArchiver_NilDB(t *testing.T) {
	archiver := NewArchiver(nil)
	ctx := context.Background()

	result, err := archiver.Archive(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, result.ArchivedCount)

	stats, err := archiver.GetStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), stats.TotalSummaries)
}
