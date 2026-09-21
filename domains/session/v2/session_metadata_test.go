package v2

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionMetadata_SetAndGet tests M2/M3 metadata read/write (title + user_tags).
func TestSessionMetadata_SetAndGet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pool := setupTestDB(t)
	agg := NewSessionAggregator(pool)
	ctx := context.Background()
	var err error
	tenantID := "test-tenant"
	sessionID := "test-session-m2m3"

	// Setup: create a test session
	_, err = pool.Exec(ctx, `
		INSERT INTO public.sessions (session_id, tenant_id, partition_date)
		VALUES ($1, $2, CURRENT_DATE)
		ON CONFLICT (session_id, partition_date) DO NOTHING
	`, sessionID, tenantID)
	require.NoError(t, err)

	defer func() {
		pool.Exec(ctx, "DELETE FROM public.sessions WHERE session_id = $1", sessionID)
		pool.Exec(ctx, "DELETE FROM gateway.session_tags WHERE session_id = $1", sessionID)
	}()

	// M2: Set metadata (title + user_tags)
	err = agg.SetSessionMetadata(ctx, tenantID, sessionID, SessionMetadata{
		TaskType:   "coding",
		ClientType: "vscode",
		Topic:      "golang",
		Intent:     "debug",
		Title:      "Debugging Go concurrency",
		UserTags:   []string{"urgent", "backend"},
	})
	require.NoError(t, err)

	// M2: Get metadata (user tags only, no auto tags yet)
	meta, err := agg.GetSessionMetadata(ctx, tenantID, sessionID, false)
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, "coding", meta.TaskType)
	assert.Equal(t, "Debugging Go concurrency", meta.Title)
	assert.ElementsMatch(t, []string{"urgent", "backend"}, meta.UserTags)

	// M3: Insert auto tags (simulating SessionTagger)
	_, err = pool.Exec(ctx, `
		INSERT INTO gateway.session_tags (tenant_id, session_id, tag_key, tag_value, tag_source, created_at)
		VALUES 
			($1, $2, 'language', 'go', 'auto', NOW()),
			($1, $2, 'category', 'backend', 'auto', NOW())
	`, tenantID, sessionID)
	require.NoError(t, err)

	// M3: Get metadata with auto tags merged
	metaMerged, err := agg.GetSessionMetadata(ctx, tenantID, sessionID, true)
	require.NoError(t, err)
	require.NotNil(t, metaMerged)

	// Should contain user tags + auto tags, deduplicated
	// user: [urgent, backend], auto: [go, backend] → merged: [urgent, backend, go]
	assert.Contains(t, metaMerged.UserTags, "urgent")
	assert.Contains(t, metaMerged.UserTags, "backend")
	assert.Contains(t, metaMerged.UserTags, "go")
	assert.Len(t, metaMerged.UserTags, 3) // backend not duplicated
}

// TestSessionMetadata_GetNonExistent tests that GetSessionMetadata returns nil for missing sessions.
func TestSessionMetadata_GetNonExistent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pool := setupTestDB(t)
	agg := NewSessionAggregator(pool)
	ctx := context.Background()

	meta, err := agg.GetSessionMetadata(ctx, "fake-tenant", "fake-session", false)
	require.NoError(t, err)
	assert.Nil(t, meta)
}

// TestSessionMetadata_PartialUpdate tests COALESCE behavior (empty strings don't overwrite).
func TestSessionMetadata_PartialUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pool := setupTestDB(t)
	agg := NewSessionAggregator(pool)
	ctx := context.Background()
	var err error
	tenantID := "test-tenant"
	sessionID := "test-session-partial"

	// Setup
	_, err = pool.Exec(ctx, `
		INSERT INTO public.sessions (session_id, tenant_id, partition_date)
		VALUES ($1, $2, CURRENT_DATE)
		ON CONFLICT (session_id, partition_date) DO NOTHING
	`, sessionID, tenantID)
	require.NoError(t, err)

	defer pool.Exec(ctx, "DELETE FROM public.sessions WHERE session_id = $1", sessionID)

	// Set initial metadata
	err = agg.SetSessionMetadata(ctx, tenantID, sessionID, SessionMetadata{
		Title: "Initial Title",
		Topic: "golang",
	})
	require.NoError(t, err)

	// Partial update: only update TaskType, leave Title/Topic unchanged
	err = agg.SetSessionMetadata(ctx, tenantID, sessionID, SessionMetadata{
		TaskType: "testing",
		// Title: "", // empty → should not overwrite
	})
	require.NoError(t, err)

	// Verify Title is preserved
	meta, err := agg.GetSessionMetadata(ctx, tenantID, sessionID, false)
	require.NoError(t, err)
	assert.Equal(t, "testing", meta.TaskType)
	assert.Equal(t, "Initial Title", meta.Title)
	assert.Equal(t, "golang", meta.Topic)
}
