package v2

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionAggregator_UpdateSession tests basic session update
func TestSessionAggregator_UpdateSession(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	aggregator := NewSessionAggregator(db)

	sessionID := "test_session_agg_001"
	tenantID := "test_tenant"
	ctx := context.Background()

	// First update: creates the session
	update1 := SessionUpdate{
		SessionID:           sessionID,
		TenantID:            tenantID,
		LastTurnNo:          1,
		LastRequestSummary:  "Hello",
		LastResponseSummary: "Hi there",
		LastModel:           "gpt-4",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     150,
		CostIncrement:       0.003,
		UpdatedAt:           time.Now(),
	}

	err := aggregator.UpdateSession(ctx, update1)
	require.NoError(t, err)

	// Retrieve and verify
	snap, err := aggregator.GetSession(ctx, tenantID, sessionID)
	require.NoError(t, err)
	require.NotNil(t, snap)

	assert.Equal(t, sessionID, snap.SessionID)
	assert.Equal(t, tenantID, snap.TenantID)
	assert.Equal(t, 1, snap.TotalTurns)
	assert.Equal(t, 150, snap.TotalTokens)
	assert.InDelta(t, 0.003, snap.TotalCostUSD, 0.0001)
	assert.Equal(t, 1, snap.LastTurnNo)
	assert.Equal(t, "Hello", snap.LastRequestSummary)
	assert.Equal(t, "Hi there", snap.LastResponseSummary)
	assert.Equal(t, "gpt-4", snap.LastModel)
	assert.Equal(t, "openai", snap.LastProvider)
}

// TestSessionAggregator_IncrementalUpdate tests incremental counters
func TestSessionAggregator_IncrementalUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	aggregator := NewSessionAggregator(db)

	sessionID := "test_session_agg_incremental"
	tenantID := "test_tenant"
	ctx := context.Background()

	// Turn 1
	update1 := SessionUpdate{
		SessionID:           sessionID,
		TenantID:            tenantID,
		LastTurnNo:          1,
		LastRequestSummary:  "Message 1",
		LastResponseSummary: "Response 1",
		LastModel:           "gpt-4",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     100,
		CostIncrement:       0.002,
		UpdatedAt:           time.Now(),
	}
	err := aggregator.UpdateSession(ctx, update1)
	require.NoError(t, err)

	// Turn 2
	update2 := SessionUpdate{
		SessionID:           sessionID,
		TenantID:            tenantID,
		LastTurnNo:          2,
		LastRequestSummary:  "Message 2",
		LastResponseSummary: "Response 2",
		LastModel:           "gpt-4",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     200,
		CostIncrement:       0.004,
		UpdatedAt:           time.Now(),
	}
	err = aggregator.UpdateSession(ctx, update2)
	require.NoError(t, err)

	// Turn 3
	update3 := SessionUpdate{
		SessionID:           sessionID,
		TenantID:            tenantID,
		LastTurnNo:          3,
		LastRequestSummary:  "Message 3",
		LastResponseSummary: "Response 3",
		LastModel:           "gpt-3.5-turbo",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     150,
		CostIncrement:       0.001,
		UpdatedAt:           time.Now(),
	}
	err = aggregator.UpdateSession(ctx, update3)
	require.NoError(t, err)

	// Verify accumulated values
	snap, err := aggregator.GetSession(ctx, tenantID, sessionID)
	require.NoError(t, err)

	assert.Equal(t, 3, snap.TotalTurns)                 // 1 + 1 + 1
	assert.Equal(t, 450, snap.TotalTokens)              // 100 + 200 + 150
	assert.InDelta(t, 0.007, snap.TotalCostUSD, 0.0001) // 0.002 + 0.004 + 0.001

	// Last turn values should be from turn 3
	assert.Equal(t, 3, snap.LastTurnNo)
	assert.Equal(t, "Message 3", snap.LastRequestSummary)
	assert.Equal(t, "Response 3", snap.LastResponseSummary)
	assert.Equal(t, "gpt-3.5-turbo", snap.LastModel)
}

// TestSessionAggregator_OnConflictBehavior tests INSERT ON CONFLICT DO UPDATE
func TestSessionAggregator_OnConflictBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	aggregator := NewSessionAggregator(db)

	sessionID := "test_session_agg_conflict"
	tenantID := "test_tenant"
	ctx := context.Background()

	// First insert
	update1 := SessionUpdate{
		SessionID:           sessionID,
		TenantID:            tenantID,
		LastTurnNo:          1,
		LastRequestSummary:  "Original",
		LastResponseSummary: "Original Response",
		LastModel:           "gpt-4",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     100,
		CostIncrement:       0.002,
		UpdatedAt:           time.Now(),
	}
	err := aggregator.UpdateSession(ctx, update1)
	require.NoError(t, err)

	// Second update with same session_id, partition_date
	// Should increment counters and update last_* fields
	update2 := SessionUpdate{
		SessionID:           sessionID,
		TenantID:            tenantID,
		LastTurnNo:          2,
		LastRequestSummary:  "Updated",
		LastResponseSummary: "Updated Response",
		LastModel:           "gpt-3.5-turbo",
		LastProvider:        "anthropic",
		TurnIncrement:       1,
		TokensIncrement:     50,
		CostIncrement:       0.001,
		UpdatedAt:           time.Now(),
	}
	err = aggregator.UpdateSession(ctx, update2)
	require.NoError(t, err)

	snap, err := aggregator.GetSession(ctx, tenantID, sessionID)
	require.NoError(t, err)

	// Counters should be incremented
	assert.Equal(t, 2, snap.TotalTurns)
	assert.Equal(t, 150, snap.TotalTokens)
	assert.InDelta(t, 0.003, snap.TotalCostUSD, 0.0001)

	// Last values should be from update2
	assert.Equal(t, 2, snap.LastTurnNo)
	assert.Equal(t, "Updated", snap.LastRequestSummary)
	assert.Equal(t, "Updated Response", snap.LastResponseSummary)
	assert.Equal(t, "gpt-3.5-turbo", snap.LastModel)
	assert.Equal(t, "anthropic", snap.LastProvider)
}

// TestSessionAggregator_CloseSession tests closing a session
func TestSessionAggregator_CloseSession(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	aggregator := NewSessionAggregator(db)

	sessionID := "test_session_agg_close"
	tenantID := "test_tenant"
	ctx := context.Background()

	// Create session
	update := SessionUpdate{
		SessionID:           sessionID,
		TenantID:            tenantID,
		LastTurnNo:          1,
		LastRequestSummary:  "Message",
		LastResponseSummary: "Response",
		LastModel:           "gpt-4",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     100,
		CostIncrement:       0.002,
		UpdatedAt:           time.Now(),
	}
	err := aggregator.UpdateSession(ctx, update)
	require.NoError(t, err)

	// Verify status is active
	snap, err := aggregator.GetSession(ctx, tenantID, sessionID)
	require.NoError(t, err)
	assert.Equal(t, "active", snap.Status)
	assert.Nil(t, snap.ClosedAt)

	// Close session
	err = aggregator.CloseSession(ctx, tenantID, sessionID)
	require.NoError(t, err)

	// Verify status is closed
	snap, err = aggregator.GetSession(ctx, tenantID, sessionID)
	require.NoError(t, err)
	assert.Equal(t, "closed", snap.Status)
	assert.NotNil(t, snap.ClosedAt)
}

// TestSessionAggregator_SetSessionMetadata tests setting session metadata
func TestSessionAggregator_SetSessionMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	aggregator := NewSessionAggregator(db)

	sessionID := "test_session_agg_metadata"
	tenantID := "test_tenant"
	ctx := context.Background()

	// Create session
	update := SessionUpdate{
		SessionID:           sessionID,
		TenantID:            tenantID,
		LastTurnNo:          1,
		LastRequestSummary:  "Message",
		LastResponseSummary: "Response",
		LastModel:           "gpt-4",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     100,
		CostIncrement:       0.002,
		UpdatedAt:           time.Now(),
	}
	err := aggregator.UpdateSession(ctx, update)
	require.NoError(t, err)

	// Set metadata
	metadata := SessionMetadata{
		TaskType:   "code_completion",
		ClientType: "vscode",
		Topic:      "golang",
		Intent:     "debug",
	}
	err = aggregator.SetSessionMetadata(ctx, tenantID, sessionID, metadata)
	require.NoError(t, err)

	// Verify metadata was set
	snap, err := aggregator.GetSession(ctx, tenantID, sessionID)
	require.NoError(t, err)

	assert.Equal(t, "code_completion", snap.TaskType)
	assert.Equal(t, "vscode", snap.ClientType)
	assert.Equal(t, "golang", snap.Topic)
	assert.Equal(t, "debug", snap.Intent)
}

// TestSessionAggregator_PartialMetadata tests partial metadata updates
func TestSessionAggregator_PartialMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	aggregator := NewSessionAggregator(db)

	sessionID := "test_session_agg_partial"
	tenantID := "test_tenant"
	ctx := context.Background()

	// Create session
	update := SessionUpdate{
		SessionID:           sessionID,
		TenantID:            tenantID,
		LastTurnNo:          1,
		LastRequestSummary:  "Message",
		LastResponseSummary: "Response",
		LastModel:           "gpt-4",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     100,
		CostIncrement:       0.002,
		UpdatedAt:           time.Now(),
	}
	err := aggregator.UpdateSession(ctx, update)
	require.NoError(t, err)

	// Set initial metadata
	metadata1 := SessionMetadata{
		TaskType:   "code_completion",
		ClientType: "vscode",
		Topic:      "golang",
		Intent:     "debug",
	}
	err = aggregator.SetSessionMetadata(ctx, tenantID, sessionID, metadata1)
	require.NoError(t, err)

	// Update only task_type (others should remain)
	metadata2 := SessionMetadata{
		TaskType: "chat",
		// Other fields are empty, should not overwrite
	}
	err = aggregator.SetSessionMetadata(ctx, tenantID, sessionID, metadata2)
	require.NoError(t, err)

	// Verify only task_type changed (COALESCE behavior)
	snap, err := aggregator.GetSession(ctx, tenantID, sessionID)
	require.NoError(t, err)

	assert.Equal(t, "chat", snap.TaskType)     // Updated
	assert.Equal(t, "vscode", snap.ClientType) // Preserved
	assert.Equal(t, "golang", snap.Topic)      // Preserved
	assert.Equal(t, "debug", snap.Intent)      // Preserved
}

// TestSessionAggregator_GetNonExistentSession tests querying non-existent session
func TestSessionAggregator_GetNonExistentSession(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	aggregator := NewSessionAggregator(db)
	ctx := context.Background()

	// Try to get non-existent session
	snap, err := aggregator.GetSession(ctx, "test_tenant", "non_existent_session")
	assert.Error(t, err)
	assert.Nil(t, snap)
}

// TestSessionAggregator_MultipleSessionsIsolation tests session isolation
func TestSessionAggregator_MultipleSessionsIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	aggregator := NewSessionAggregator(db)
	ctx := context.Background()

	tenantID := "test_tenant"

	// Create multiple sessions
	for i := 1; i <= 5; i++ {
		sessionID := fmt.Sprintf("test_session_multi_%d", i)

		update := SessionUpdate{
			SessionID:           sessionID,
			TenantID:            tenantID,
			LastTurnNo:          i,
			LastRequestSummary:  fmt.Sprintf("Message %d", i),
			LastResponseSummary: fmt.Sprintf("Response %d", i),
			LastModel:           "gpt-4",
			LastProvider:        "openai",
			TurnIncrement:       i,
			TokensIncrement:     i * 100,
			CostIncrement:       float64(i) * 0.001,
			UpdatedAt:           time.Now(),
		}

		err := aggregator.UpdateSession(ctx, update)
		require.NoError(t, err)
	}

	// Verify each session is independent
	for i := 1; i <= 5; i++ {
		sessionID := fmt.Sprintf("test_session_multi_%d", i)

		snap, err := aggregator.GetSession(ctx, tenantID, sessionID)
		require.NoError(t, err)

		assert.Equal(t, sessionID, snap.SessionID)
		assert.Equal(t, i, snap.TotalTurns)
		assert.Equal(t, i*100, snap.TotalTokens)
		assert.InDelta(t, float64(i)*0.001, snap.TotalCostUSD, 0.0001)
		assert.Equal(t, i, snap.LastTurnNo)
	}
}
