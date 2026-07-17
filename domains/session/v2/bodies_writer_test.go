package v2

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionBodiesWriter_WriteBodies tests basic body writing
func TestSessionBodiesWriter_WriteBodies(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewSessionBodiesWriter(db)

	rec := BodiesRecord{
		SessionID: "test_session_bodies_001",
		TurnNo:    1,
		TenantID:  "test_tenant",
		RequestID: "req_bodies_001",
		Ts:        time.Now(),
		RequestDelta: []Message{
			{Role: "user", Content: "Hello"},
		},
		ResponseDelta: []Message{
			{Role: "assistant", Content: "Hi there!"},
		},
		OutboundBody: []Message{
			{Role: "user", Content: "Hello"},
		},
		RequestAttachments:  []AttachmentRef{},
		ResponseAttachments: []AttachmentRef{},
	}

	ctx := context.Background()

	// Write bodies
	err := writer.WriteBodies(ctx, rec)
	require.NoError(t, err)

	// Retrieve bodies
	retrieved, err := writer.GetBodies(ctx, rec.TenantID, rec.SessionID, rec.TurnNo)
	require.NoError(t, err)
	require.NotNil(t, retrieved)

	// Verify content
	assert.Equal(t, rec.SessionID, retrieved.SessionID)
	assert.Equal(t, rec.TurnNo, retrieved.TurnNo)
	assert.Len(t, retrieved.RequestDelta, 1)
	assert.Equal(t, "Hello", retrieved.RequestDelta[0].Content)
	assert.Len(t, retrieved.ResponseDelta, 1)
	assert.Equal(t, "Hi there!", retrieved.ResponseDelta[0].Content)
}

// TestSessionBodiesWriter_IncrementalStorage tests incremental storage pattern
func TestSessionBodiesWriter_IncrementalStorage(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewSessionBodiesWriter(db)

	sessionID := "test_session_incremental"
	tenantID := "test_tenant"

	ctx := context.Background()

	// Turn 1: User says hello
	rec1 := BodiesRecord{
		SessionID: sessionID,
		TurnNo:    1,
		TenantID:  tenantID,
		RequestID: "req_inc_001",
		Ts:        time.Now(),
		RequestDelta: []Message{
			{Role: "user", Content: "Hello"},
		},
		ResponseDelta: []Message{
			{Role: "assistant", Content: "Hi! How can I help?"},
		},
	}
	err := writer.WriteBodies(ctx, rec1)
	require.NoError(t, err)

	// Turn 2: User asks a question (only new message in delta)
	rec2 := BodiesRecord{
		SessionID: sessionID,
		TurnNo:    2,
		TenantID:  tenantID,
		RequestID: "req_inc_002",
		Ts:        time.Now(),
		RequestDelta: []Message{
			{Role: "user", Content: "What's the weather?"},
		},
		ResponseDelta: []Message{
			{Role: "assistant", Content: "It's sunny today."},
		},
	}
	err = writer.WriteBodies(ctx, rec2)
	require.NoError(t, err)

	// Turn 3: User follows up (only new message in delta)
	rec3 := BodiesRecord{
		SessionID: sessionID,
		TurnNo:    3,
		TenantID:  tenantID,
		RequestID: "req_inc_003",
		Ts:        time.Now(),
		RequestDelta: []Message{
			{Role: "user", Content: "Thanks!"},
		},
		ResponseDelta: []Message{
			{Role: "assistant", Content: "You're welcome!"},
		},
	}
	err = writer.WriteBodies(ctx, rec3)
	require.NoError(t, err)

	// Reconstruct full history
	history, err := writer.ReconstructFullHistory(ctx, tenantID, sessionID)
	require.NoError(t, err)
	require.Len(t, history, 3)

	// After turn 1: 2 messages
	assert.Len(t, history[0], 2)
	assert.Equal(t, "Hello", history[0][0].Content)
	assert.Equal(t, "Hi! How can I help?", history[0][1].Content)

	// After turn 2: 4 messages
	assert.Len(t, history[1], 4)
	assert.Equal(t, "What's the weather?", history[1][2].Content)
	assert.Equal(t, "It's sunny today.", history[1][3].Content)

	// After turn 3: 6 messages
	assert.Len(t, history[2], 6)
	assert.Equal(t, "Thanks!", history[2][4].Content)
	assert.Equal(t, "You're welcome!", history[2][5].Content)
}

// TestSessionBodiesWriter_Attachments tests attachment reference storage
func TestSessionBodiesWriter_Attachments(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewSessionBodiesWriter(db)

	rec := BodiesRecord{
		SessionID: "test_session_attachments",
		TurnNo:    1,
		TenantID:  "test_tenant",
		RequestID: "req_attach_001",
		Ts:        time.Now(),
		RequestDelta: []Message{
			{Role: "user", Content: "Here's an image"},
		},
		ResponseDelta: []Message{
			{Role: "assistant", Content: "I can see it"},
		},
		RequestAttachments: []AttachmentRef{
			{
				Name:        "image.png",
				ObjectKey:   "attachments/abc123.png",
				ContentType: "image/png",
				Size:        1024,
				SHA256:      "abc123def456",
			},
		},
		ResponseAttachments: []AttachmentRef{},
	}

	ctx := context.Background()

	err := writer.WriteBodies(ctx, rec)
	require.NoError(t, err)

	// Retrieve and verify attachments
	retrieved, err := writer.GetBodies(ctx, rec.TenantID, rec.SessionID, rec.TurnNo)
	require.NoError(t, err)

	assert.Len(t, retrieved.RequestAttachments, 1)
	assert.Equal(t, "image.png", retrieved.RequestAttachments[0].Name)
	assert.Equal(t, "attachments/abc123.png", retrieved.RequestAttachments[0].ObjectKey)
	assert.Equal(t, "image/png", retrieved.RequestAttachments[0].ContentType)
	assert.Equal(t, int64(1024), retrieved.RequestAttachments[0].Size)
}

// TestSessionBodiesWriter_ListAllBodies tests listing all bodies for a session
func TestSessionBodiesWriter_ListAllBodies(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewSessionBodiesWriter(db)

	sessionID := "test_session_list_bodies"
	tenantID := "test_tenant"

	ctx := context.Background()

	// Insert 3 turns
	for i := 1; i <= 3; i++ {
		rec := BodiesRecord{
			SessionID: sessionID,
			TurnNo:    i,
			TenantID:  tenantID,
			RequestID: fmt.Sprintf("req_list_bodies_%d", i),
			Ts:        time.Now(),
			RequestDelta: []Message{
				{Role: "user", Content: fmt.Sprintf("Message %d", i)},
			},
			ResponseDelta: []Message{
				{Role: "assistant", Content: fmt.Sprintf("Response %d", i)},
			},
		}

		err := writer.WriteBodies(ctx, rec)
		require.NoError(t, err)
	}

	// List all bodies
	bodies, err := writer.ListAllBodies(ctx, tenantID, sessionID)
	require.NoError(t, err)
	require.Len(t, bodies, 3)

	// Verify order
	for i, body := range bodies {
		assert.Equal(t, i+1, body.TurnNo)
		assert.Equal(t, fmt.Sprintf("Message %d", i+1), body.RequestDelta[0].Content)
		assert.Equal(t, fmt.Sprintf("Response %d", i+1), body.ResponseDelta[0].Content)
	}
}

// TestSessionBodiesWriter_UpdateConflict tests ON CONFLICT behavior
func TestSessionBodiesWriter_UpdateConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewSessionBodiesWriter(db)

	sessionID := "test_session_conflict"
	tenantID := "test_tenant"
	turnNo := 1

	ctx := context.Background()

	// First write
	rec1 := BodiesRecord{
		SessionID: sessionID,
		TurnNo:    turnNo,
		TenantID:  tenantID,
		RequestID: "req_conflict_001",
		Ts:        time.Now(),
		RequestDelta: []Message{
			{Role: "user", Content: "Original request"},
		},
		ResponseDelta: []Message{
			{Role: "assistant", Content: "Original response"},
		},
	}

	err := writer.WriteBodies(ctx, rec1)
	require.NoError(t, err)

	// Second write with same session_id and turn_no (should UPDATE response_delta)
	rec2 := BodiesRecord{
		SessionID: sessionID,
		TurnNo:    turnNo,
		TenantID:  tenantID,
		RequestID: "req_conflict_001", // Same request_id
		Ts:        time.Now(),
		RequestDelta: []Message{
			{Role: "user", Content: "Original request"}, // Same
		},
		ResponseDelta: []Message{
			{Role: "assistant", Content: "Updated response"}, // Different
		},
	}

	err = writer.WriteBodies(ctx, rec2)
	require.NoError(t, err)

	// Retrieve and verify it was updated
	retrieved, err := writer.GetBodies(ctx, tenantID, sessionID, turnNo)
	require.NoError(t, err)

	assert.Equal(t, "Updated response", retrieved.ResponseDelta[0].Content)
}
