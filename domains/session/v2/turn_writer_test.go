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

	// Duplicate request_id - should return the existing turn number
	turnNo2, err := writer.AppendTurn(ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, turnNo1, turnNo2)
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
	retrievedRec, err := writer.GetTurn(ctx, originalRec.TenantID, "req_get_test")
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

// TestTurnWriter_ListTurns_WithAttachments tests that ListTurns correctly returns attachment fields
func TestTurnWriter_ListTurns_WithAttachments(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnWriter(db)

	sessionID := "test_session_attachments"
	tenantID := "test_tenant"

	ctx := context.Background()

	// Insert turns with attachment metadata
	for i := 1; i <= 3; i++ {
		rec := TurnRecord{
			SessionID:            sessionID,
			TenantID:             tenantID,
			RequestID:            fmt.Sprintf("req_attach_%d", i),
			Ts:                   time.Now(),
			SubmitMode:           "full",
			Model:                "gpt-4",
			Provider:             "openai",
			PromptTokens:         100,
			SourceKind:           "live",
			Quality:              "verified",
			AttachmentCount:      i,                 // 1, 2, 3 attachments
			AttachmentTotalBytes: int64(i * 1024),   // 1KB, 2KB, 3KB
			MultimodalTypes:      []string{"image"}, // All have images
		}

		turnNo, err := writer.AppendTurn(ctx, rec)
		require.NoError(t, err)
		assert.Equal(t, i, turnNo)
	}

	// List turns and verify attachment fields are returned
	turns, err := writer.ListTurns(ctx, tenantID, sessionID, 10)
	require.NoError(t, err)
	require.Len(t, turns, 3)

	// Verify attachment fields for each turn
	for i, turn := range turns {
		expectedCount := i + 1
		assert.Equal(t, expectedCount, turn.AttachmentCount, "Turn %d attachment count mismatch", i+1)
		assert.Equal(t, int64(expectedCount*1024), turn.AttachmentTotalBytes, "Turn %d attachment bytes mismatch", i+1)
		assert.Equal(t, []string{"image"}, turn.MultimodalTypes, "Turn %d multimodal types mismatch", i+1)
	}
}

// ─── deriveTurnQuality (P1-2, 2026-09) ─────────────────────────────────────

func TestDeriveTurnQuality(t *testing.T) {
	cases := []struct {
		name string
		req  *ProcessedRequest
		want string
	}{
		{"nil request defaults to verified", nil, "verified"},
		{"explicit override wins", &ProcessedRequest{Quality: "inferred", Success: true, ResponseBody: []Message{{Role: "assistant"}}}, "inferred"},
		{"failure rejected", &ProcessedRequest{Success: false, ErrorKind: "upstream_5xx"}, "rejected"},
		{"error kind alone rejected", &ProcessedRequest{Success: true, ErrorKind: "rate_limited"}, "rejected"},
		{"success without response partial", &ProcessedRequest{Success: true}, "partial"},
		{"success with response verified", &ProcessedRequest{Success: true, ResponseBody: []Message{{Role: "assistant", Content: "ok"}}}, "verified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveTurnQuality(tc.req); got != tc.want {
				t.Fatalf("deriveTurnQuality() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── extractRequestDelta empty-diff fallback (P2-5, 2026-09) ───────────────

// A "full"-mode client that re-sends an identical history (bare retry, or a
// turn with no new user message) yields an EMPTY set difference. The delta
// MUST fall back to the full body: every reader accumulates request_delta
// across turns (turn_reader.LoadChain, outbound_builder.BuildFromDeltas), so
// persisting nil here would drop the turn's user input from reconstruction.
func TestExtractRequestDelta_IdenticalHistoryFallsBackToFullBody(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
	}
	req := &ProcessedRequest{
		RequestBody:     history,
		LastOutboundBody: history, // identical → zero new messages
	}
	delta := extractRequestDelta(req, "full")
	if len(delta) != len(history) {
		t.Fatalf("expected full-body fallback (%d messages), got %d", len(history), len(delta))
	}
	for i, msg := range delta {
		if msg.Content != history[i].Content || msg.Role != history[i].Role {
			t.Fatalf("delta[%d] = %+v, want %+v", i, msg, history[i])
		}
	}
}

// Sanity: when the history genuinely extends, only the new tail is persisted.
func TestExtractRequestDelta_NewMessagesStillExtracted(t *testing.T) {
	prev := []Message{{Role: "user", Content: "q1"}, {Role: "assistant", Content: "a1"}}
	req := &ProcessedRequest{
		RequestBody:      append(append([]Message{}, prev...), Message{Role: "user", Content: "q2"}),
		LastOutboundBody: prev,
	}
	delta := extractRequestDelta(req, "full")
	if len(delta) != 1 || delta[0].Content != "q2" {
		t.Fatalf("expected only the new message, got %+v", delta)
	}
}
