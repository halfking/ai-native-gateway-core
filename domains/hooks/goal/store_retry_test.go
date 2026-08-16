package goal

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) *sql.DB {
	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		t.Skip("TEST_DB_URL not set, skipping database integration test")
	}

	db, err := sql.Open("postgres", dbURL)
	require.NoError(t, err)
	require.NoError(t, db.Ping())

	t.Cleanup(func() {
		// Clean up test data
		_, _ = db.Exec("DELETE FROM goal_sessions WHERE tenant_id = 'test_tenant'")
		db.Close()
	})

	return db
}

func TestPGStore_AddRetryCount(t *testing.T) {
	db := setupTestDB(t)
	store := NewPGStore(db)
	ctx := context.Background()

	// Create a test session
	session := &Session{
		SessionID:    "test_session_retry_001",
		TenantID:     "test_tenant",
		State:        StateActive,
		OriginalGoal: "test goal",
		RetryCount:   0,
	}
	err := store.CreateSession(ctx, session)
	require.NoError(t, err)

	t.Run("increment by positive delta", func(t *testing.T) {
		err := store.AddRetryCount(ctx, session.TenantID, session.SessionID, 2)
		require.NoError(t, err)

		// Verify
		retrieved, err := store.GetSession(ctx, session.TenantID, session.SessionID)
		require.NoError(t, err)
		require.Equal(t, 2, retrieved.RetryCount)
	})

	t.Run("increment again accumulates", func(t *testing.T) {
		err := store.AddRetryCount(ctx, session.TenantID, session.SessionID, 3)
		require.NoError(t, err)

		// Verify
		retrieved, err := store.GetSession(ctx, session.TenantID, session.SessionID)
		require.NoError(t, err)
		require.Equal(t, 5, retrieved.RetryCount, "should accumulate: 2 + 3")
	})

	t.Run("empty session ID is no-op", func(t *testing.T) {
		err := store.AddRetryCount(ctx, "", "", 5)
		require.NoError(t, err, "should not error on empty ID")
	})

	t.Run("zero delta is no-op", func(t *testing.T) {
		before, err := store.GetSession(ctx, session.TenantID, session.SessionID)
		require.NoError(t, err)

		err = store.AddRetryCount(ctx, session.TenantID, session.SessionID, 0)
		require.NoError(t, err)

		after, err := store.GetSession(ctx, session.TenantID, session.SessionID)
		require.NoError(t, err)
		require.Equal(t, before.RetryCount, after.RetryCount, "count should not change")
	})

	t.Run("negative delta is no-op", func(t *testing.T) {
		before, err := store.GetSession(ctx, session.TenantID, session.SessionID)
		require.NoError(t, err)

		err = store.AddRetryCount(ctx, session.TenantID, session.SessionID, -1)
		require.NoError(t, err)

		after, err := store.GetSession(ctx, session.TenantID, session.SessionID)
		require.NoError(t, err)
		require.Equal(t, before.RetryCount, after.RetryCount, "count should not change")
	})

	t.Run("non-existent session does not error", func(t *testing.T) {
		err := store.AddRetryCount(ctx, "test_tenant", "nonexistent_session", 1)
		require.NoError(t, err, "fail-open: should not error on missing session")
	})
}
