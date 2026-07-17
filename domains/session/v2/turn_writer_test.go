package v2

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTurnWriter_AppendTurn tests basic turn appending
func TestTurnWriter_AppendTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnWriter(db)

	// Test data
	rec := TurnRecord{
		SessionID:           "test_session_001",
		TenantID:            "test_tenant",
		RequestID:           "req_001",
		Ts:                  time.Now(),
		SubmitMode:          "full",
		CompressionApplied:  false,
		CompressionStrategy: "",
		CompressionMeta:     map[string]interface{}{},
		InjectionVerdict:    "pass",
		OutputVerdict:       "pass",
		Model:               "gpt-4",
		Provider:            "openai",
		CredentialID:        "cred_001",
		PromptTokens:        100,
		CompletionTokens:    50,
		CostUSD:             0.005,
		LatencyMs:           1000,
		StatusCode:          200,
		Success:             true,
		SourceKind:          "live",
		Quality:             "verified",
	}

	ctx := context.Background()

	// First turn should be 1
	turnNo1, err := writer.AppendTurn(ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, 1, turnNo1)

	// Second turn should be 2
	rec.RequestID = "req_002"
	turnNo2, err := writer.AppendTurn(ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, 2, turnNo2)

	// Third turn should be 3
	rec.RequestID = "req_003"
	turnNo3, err := writer.AppendTurn(ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, 3, turnNo3)
}

// TestTurnWriter_Idempotency tests that duplicate request_id is handled
func TestTurnWriter_Idempotency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnWriter(db)

	rec := TurnRecord{
		SessionID:    "test_session_002",
		TenantID:     "test_tenant",
		RequestID:    "req_duplicate",
		Ts:           time.Now(),
		SubmitMode:   "full",
		Model:        "gpt-4",
		Provider:     "openai",
		PromptTokens: 100,
		SourceKind:   "live",
		Quality:      "verified",
	}

	ctx := context.Background()

	// First insert
	turnNo1, err := writer.AppendTurn(ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, 1, turnNo1)

	// Duplicate request_id - should not error (ON CONFLICT DO NOTHING)
	// But turn_no should still increment
	turnNo2, err := writer.AppendTurn(ctx, rec)
	require.NoError(t, err)
	// Because of ON CONFLICT DO NOTHING, it still returns the next turn_no
	assert.Equal(t, 2, turnNo2)
}

// TestTurnWriter_ConcurrentWrites tests concurrent turn appending
func TestTurnWriter_ConcurrentWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnWriter(db)

	sessionID := "test_session_concurrent"
	tenantID := "test_tenant"
	numGoroutines := 10

	ctx := context.Background()

	// Launch multiple goroutines to append turns concurrently
	type result struct {
		turnNo int
		err    error
	}
	results := make(chan result, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(index int) {
			rec := TurnRecord{
				SessionID:    sessionID,
				TenantID:     tenantID,
				RequestID:    fmt.Sprintf("req_concurrent_%d", index),
				Ts:           time.Now(),
				SubmitMode:   "full",
				Model:        "gpt-4",
				Provider:     "openai",
				PromptTokens: 100,
				SourceKind:   "live",
				Quality:      "verified",
			}

			turnNo, err := writer.AppendTurn(ctx, rec)
			results <- result{turnNo: turnNo, err: err}
		}(i)
	}

	// Collect results
	turnNos := make(map[int]bool)
	for i := 0; i < numGoroutines; i++ {
		res := <-results
		require.NoError(t, res.err)
		
		// Verify turn_no is unique
		assert.False(t, turnNos[res.turnNo], "Duplicate turn_no detected: %d", res.turnNo)
		turnNos[res.turnNo] = true
	}

	// Verify all turn_nos from 1 to numGoroutines exist
	for i := 1; i <= numGoroutines; i++ {
		assert.True(t, turnNos[i], "Missing turn_no: %d", i)
	}
}

// TestTurnWriter_GetTurn tests retrieving a turn by request_id
func TestTurnWriter_GetTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnWriter(db)

	// Insert a turn
	originalRec := TurnRecord{
		SessionID:           "test_session_003",
		TenantID:            "test_tenant",
		RequestID:           "req_get_test",
		Ts:                  time.Now(),
		SubmitMode:          "full",
		CompressionApplied:  true,
		CompressionStrategy: "delta_append",
		CompressionMeta:     map[string]interface{}{"tokens_saved": 500},
		InjectionVerdict:    "pass",
		OutputVerdict:       "pass",
		Model:               "gpt-4",
		Provider:            "openai",
		PromptTokens:        100,
		CompletionTokens:    50,
		CostUSD:             0.005,
		StatusCode:          200,
		Success:             true,
		SourceKind:          "live",
		Quality:             "verified",
	}

	ctx := context.Background()

	turnNo, err := writer.AppendTurn(ctx, originalRec)
	require.NoError(t, err)

	// Retrieve the turn
	retrievedRec, err := writer.GetTurn(ctx, "req_get_test")
	require.NoError(t, err)
	require.NotNil(t, retrievedRec)

	// Verify fields
	assert.Equal(t, originalRec.SessionID, retrievedRec.SessionID)
	assert.Equal(t, originalRec.TenantID, retrievedRec.TenantID)
	assert.Equal(t, originalRec.RequestID, retrievedRec.RequestID)
	assert.Equal(t, originalRec.SubmitMode, retrievedRec.SubmitMode)
	assert.Equal(t, originalRec.CompressionApplied, retrievedRec.CompressionApplied)
	assert.Equal(t, originalRec.CompressionStrategy, retrievedRec.CompressionStrategy)
	assert.Equal(t, originalRec.Model, retrievedRec.Model)
	assert.Equal(t, originalRec.Provider, retrievedRec.Provider)
	assert.Equal(t, originalRec.PromptTokens, retrievedRec.PromptTokens)
	assert.Equal(t, originalRec.CompletionTokens, retrievedRec.CompletionTokens)

	// Note: turnNo is not returned by GetTurn, but we saved it from AppendTurn
	_ = turnNo
}

// TestTurnWriter_ListTurns tests listing all turns for a session
func TestTurnWriter_ListTurns(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnWriter(db)

	sessionID := "test_session_list"
	tenantID := "test_tenant"

	ctx := context.Background()

	// Insert 5 turns
	for i := 1; i <= 5; i++ {
		rec := TurnRecord{
			SessionID:    sessionID,
			TenantID:     tenantID,
			RequestID:    fmt.Sprintf("req_list_%d", i),
			Ts:           time.Now(),
			SubmitMode:   "full",
			Model:        fmt.Sprintf("model_%d", i),
			Provider:     "openai",
			PromptTokens: i * 100,
			SourceKind:   "live",
			Quality:      "verified",
		}

		turnNo, err := writer.AppendTurn(ctx, rec)
		require.NoError(t, err)
		assert.Equal(t, i, turnNo)
	}

	// List turns
	turns, err := writer.ListTurns(ctx, tenantID, sessionID, 10)
	require.NoError(t, err)
	require.Len(t, turns, 5)

	// Verify order (should be ascending by turn_no)
	for i, turn := range turns {
		assert.Equal(t, fmt.Sprintf("req_list_%d", i+1), turn.RequestID)
		assert.Equal(t, fmt.Sprintf("model_%d", i+1), turn.Model)
		assert.Equal(t, (i+1)*100, turn.PromptTokens)
	}
}
