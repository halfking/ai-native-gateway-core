package casdoor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSessionManager(t *testing.T) {
	t.Run("WithDefaults", func(t *testing.T) {
		manager := NewSessionManager(nil)
		assert.NotNil(t, manager)
		assert.Equal(t, 24*time.Hour, manager.sessionTTL)
		assert.Equal(t, 7*24*time.Hour, manager.refreshTTL)
		assert.Equal(t, "session:", manager.sessionPrefix)
	})

	t.Run("WithCustomConfig", func(t *testing.T) {
		config := &SessionConfig{
			SessionTTL:    1 * time.Hour,
			RefreshTTL:    24 * time.Hour,
			SessionPrefix: "custom:",
		}
		manager := NewSessionManager(config)
		assert.NotNil(t, manager)
		assert.Equal(t, 1*time.Hour, manager.sessionTTL)
		assert.Equal(t, 24*time.Hour, manager.refreshTTL)
		assert.Equal(t, "custom:", manager.sessionPrefix)
	})
}

func TestCreateSession(t *testing.T) {
	manager := NewSessionManager(nil)
	ctx := context.Background()

	t.Run("CreateSuccess", func(t *testing.T) {
		metadata := map[string]interface{}{
			"device": "mobile",
			"app":    "ios",
		}

		session, err := manager.CreateSession(ctx, 123, "testuser", "tenant1", metadata)
		require.NoError(t, err)
		assert.NotNil(t, session)
		assert.NotEmpty(t, session.ID)
		assert.Equal(t, 123, session.UserID)
		assert.Equal(t, "testuser", session.Username)
		assert.Equal(t, "tenant1", session.TenantID)
		assert.False(t, session.CreatedAt.IsZero())
		assert.False(t, session.ExpiresAt.IsZero())
		assert.Equal(t, "mobile", session.Metadata["device"])
	})

	t.Run("UniqueSessionIDs", func(t *testing.T) {
		session1, err := manager.CreateSession(ctx, 1, "user1", "tenant1", nil)
		require.NoError(t, err)

		session2, err := manager.CreateSession(ctx, 2, "user2", "tenant1", nil)
		require.NoError(t, err)

		assert.NotEqual(t, session1.ID, session2.ID)
	})
}

func TestGetSession(t *testing.T) {
	manager := NewSessionManager(nil)
	ctx := context.Background()

	t.Run("GetExistingSession", func(t *testing.T) {
		// Create session
		created, err := manager.CreateSession(ctx, 123, "testuser", "tenant1", nil)
		require.NoError(t, err)

		// Get session
		retrieved, err := manager.GetSession(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, created.ID, retrieved.ID)
		assert.Equal(t, created.UserID, retrieved.UserID)
	})

	t.Run("GetNonExistentSession", func(t *testing.T) {
		_, err := manager.GetSession(ctx, "nonexistent")
		assert.Error(t, err)
	})

	t.Run("GetExpiredSession", func(t *testing.T) {
		// Create session with short TTL
		shortTTLManager := NewSessionManager(&SessionConfig{
			SessionTTL: 1 * time.Millisecond,
		})

		session, err := shortTTLManager.CreateSession(ctx, 123, "testuser", "tenant1", nil)
		require.NoError(t, err)

		// Wait for expiration
		time.Sleep(10 * time.Millisecond)

		// Try to get expired session
		_, err = shortTTLManager.GetSession(ctx, session.ID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "expired")
	})
}

func TestUpdateSession(t *testing.T) {
	manager := NewSessionManager(nil)
	ctx := context.Background()

	t.Run("UpdateSuccess", func(t *testing.T) {
		// Create session
		session, err := manager.CreateSession(ctx, 123, "testuser", "tenant1", nil)
		require.NoError(t, err)

		// Wait a bit
		time.Sleep(10 * time.Millisecond)

		// Update session
		newMetadata := map[string]interface{}{
			"updated": true,
		}
		err = manager.UpdateSession(ctx, session.ID, newMetadata)
		require.NoError(t, err)

		// Get updated session
		updated, err := manager.GetSession(ctx, session.ID)
		require.NoError(t, err)
		assert.True(t, updated.LastActivity.After(session.LastActivity))
		assert.Equal(t, true, updated.Metadata["updated"])
	})
}

func TestRefreshSession(t *testing.T) {
	manager := NewSessionManager(nil)
	ctx := context.Background()

	t.Run("RefreshSuccess", func(t *testing.T) {
		// Create session
		session, err := manager.CreateSession(ctx, 123, "testuser", "tenant1", nil)
		require.NoError(t, err)
		originalExpiry := session.ExpiresAt

		// Wait a bit
		time.Sleep(10 * time.Millisecond)

		// Refresh session
		refreshed, err := manager.RefreshSession(ctx, session.ID)
		require.NoError(t, err)
		assert.True(t, refreshed.ExpiresAt.After(originalExpiry))
		assert.True(t, refreshed.LastActivity.After(session.LastActivity))
	})

	t.Run("RefreshNonExistent", func(t *testing.T) {
		_, err := manager.RefreshSession(ctx, "nonexistent")
		assert.Error(t, err)
	})
}

func TestDeleteSession(t *testing.T) {
	manager := NewSessionManager(nil)
	ctx := context.Background()

	t.Run("DeleteSuccess", func(t *testing.T) {
		// Create session
		session, err := manager.CreateSession(ctx, 123, "testuser", "tenant1", nil)
		require.NoError(t, err)

		// Delete session
		err = manager.DeleteSession(ctx, session.ID)
		require.NoError(t, err)

		// Try to get deleted session
		_, err = manager.GetSession(ctx, session.ID)
		assert.Error(t, err)
	})

	t.Run("DeleteNonExistent", func(t *testing.T) {
		err := manager.DeleteSession(ctx, "nonexistent")
		assert.NoError(t, err) // Should not error
	})
}

func TestDeleteUserSessions(t *testing.T) {
	manager := NewSessionManager(nil)
	ctx := context.Background()

	t.Run("DeleteAllUserSessions", func(t *testing.T) {
		userID := 123

		// Create multiple sessions for the user
		_, err := manager.CreateSession(ctx, userID, "testuser", "tenant1", nil)
		require.NoError(t, err)
		_, err = manager.CreateSession(ctx, userID, "testuser", "tenant1", nil)
		require.NoError(t, err)

		// Create session for different user
		otherSession, err := manager.CreateSession(ctx, 456, "otheruser", "tenant1", nil)
		require.NoError(t, err)

		// Delete all sessions for userID
		err = manager.DeleteUserSessions(ctx, userID)
		require.NoError(t, err)

		// Other user's session should still exist
		_, err = manager.GetSession(ctx, otherSession.ID)
		assert.NoError(t, err)
	})
}

func TestListUserSessions(t *testing.T) {
	manager := NewSessionManager(nil)
	ctx := context.Background()

	t.Run("ListMultipleSessions", func(t *testing.T) {
		userID := 123

		// Create multiple sessions
		_, err := manager.CreateSession(ctx, userID, "testuser", "tenant1", nil)
		require.NoError(t, err)
		_, err = manager.CreateSession(ctx, userID, "testuser", "tenant1", nil)
		require.NoError(t, err)

		// Create session for different user
		_, err = manager.CreateSession(ctx, 456, "otheruser", "tenant1", nil)
		require.NoError(t, err)

		// List user sessions
		sessions, err := manager.ListUserSessions(ctx, userID)
		require.NoError(t, err)
		assert.Len(t, sessions, 2)
	})

	t.Run("ListNoSessions", func(t *testing.T) {
		sessions, err := manager.ListUserSessions(ctx, 999)
		require.NoError(t, err)
		assert.Empty(t, sessions)
	})
}

func TestCleanupExpiredSessions(t *testing.T) {
	t.Run("CleanupSuccess", func(t *testing.T) {
		// Create manager with short TTL
		manager := NewSessionManager(&SessionConfig{
			SessionTTL: 10 * time.Millisecond,
		})
		ctx := context.Background()

		// Create sessions
		_, err := manager.CreateSession(ctx, 1, "user1", "tenant1", nil)
		require.NoError(t, err)
		_, err = manager.CreateSession(ctx, 2, "user2", "tenant1", nil)
		require.NoError(t, err)

		// Wait for expiration
		time.Sleep(20 * time.Millisecond)

		// Cleanup
		count, err := manager.CleanupExpiredSessions(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})
}

func TestGetStats(t *testing.T) {
	manager := NewSessionManager(nil)
	ctx := context.Background()

	t.Run("EmptyStats", func(t *testing.T) {
		stats, err := manager.GetStats(ctx)
		require.NoError(t, err)
		assert.Equal(t, 0, stats.TotalSessions)
		assert.Equal(t, 0, stats.ActiveSessions)
	})

	t.Run("WithActiveSessions", func(t *testing.T) {
		// Create sessions
		_, err := manager.CreateSession(ctx, 1, "user1", "tenant1", nil)
		require.NoError(t, err)
		_, err = manager.CreateSession(ctx, 2, "user2", "tenant1", nil)
		require.NoError(t, err)

		stats, err := manager.GetStats(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, stats.TotalSessions)
		assert.Equal(t, 2, stats.ActiveSessions)
	})
}

func TestGenerateSessionID(t *testing.T) {
	t.Run("GenerateUnique", func(t *testing.T) {
		id1, err := generateSessionID()
		require.NoError(t, err)
		assert.NotEmpty(t, id1)

		id2, err := generateSessionID()
		require.NoError(t, err)
		assert.NotEmpty(t, id2)

		assert.NotEqual(t, id1, id2)
	})
}

// BenchmarkCreateSession benchmarks session creation.
func BenchmarkCreateSession(b *testing.B) {
	manager := NewSessionManager(nil)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := manager.CreateSession(ctx, i, "user", "tenant", nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetSession benchmarks session retrieval.
func BenchmarkGetSession(b *testing.B) {
	manager := NewSessionManager(nil)
	ctx := context.Background()

	// Setup: create a session
	session, err := manager.CreateSession(ctx, 1, "user", "tenant", nil)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := manager.GetSession(ctx, session.ID)
		if err != nil {
			b.Fatal(err)
		}
	}
}
