package v2

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTurnLogsWriter_WriteStage tests basic stage log writing
func TestTurnLogsWriter_WriteStage(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnLogsWriter(db)

	rec := TurnLogRecord{
		SessionID:   "test_session_logs_001",
		TurnNo:      1,
		TenantID:    "test_tenant",
		RequestID:   "req_logs_001",
		Stage:       "routing",
		StageStatus: "success",
		EventData: map[string]interface{}{
			"provider": "openai",
			"model":    "gpt-4",
		},
		ErrorMsg:    "",
		StartedAt:   time.Now().Add(-100 * time.Millisecond),
		CompletedAt: time.Now(),
	}

	ctx := context.Background()

	err := writer.WriteStage(ctx, rec)
	require.NoError(t, err)

	// Retrieve and verify
	logs, err := writer.GetStageLogs(ctx, rec.TenantID, rec.SessionID, rec.TurnNo)
	require.NoError(t, err)
	require.Len(t, logs, 1)

	log := logs[0]
	assert.Equal(t, rec.SessionID, log.SessionID)
	assert.Equal(t, rec.TurnNo, log.TurnNo)
	assert.Equal(t, rec.Stage, log.Stage)
	assert.Equal(t, rec.StageStatus, log.StageStatus)
	assert.Equal(t, "openai", log.EventData["provider"])
	assert.Equal(t, "gpt-4", log.EventData["model"])
	assert.Empty(t, log.ErrorMsg)
}

// TestTurnLogsWriter_MultipleStages tests writing multiple stages for a turn
func TestTurnLogsWriter_MultipleStages(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnLogsWriter(db)

	sessionID := "test_session_logs_multi"
	turnNo := 1
	tenantID := "test_tenant"
	requestID := "req_logs_multi"

	ctx := context.Background()

	stages := []struct {
		stage  string
		status string
		delay  time.Duration
	}{
		{"routing", "success", 10 * time.Millisecond},
		{"compression", "success", 20 * time.Millisecond},
		{"injection_check", "success", 5 * time.Millisecond},
		{"llm_call", "success", 500 * time.Millisecond},
		{"output_check", "success", 5 * time.Millisecond},
		{"response", "success", 10 * time.Millisecond},
	}

	baseTime := time.Now()

	for i, stage := range stages {
		startedAt := baseTime.Add(time.Duration(i*10) * time.Millisecond)
		completedAt := startedAt.Add(stage.delay)

		rec := TurnLogRecord{
			SessionID:   sessionID,
			TurnNo:      turnNo,
			TenantID:    tenantID,
			RequestID:   requestID,
			Stage:       stage.stage,
			StageStatus: stage.status,
			EventData: map[string]interface{}{
				"stage_index": i,
			},
			StartedAt:   startedAt,
			CompletedAt: completedAt,
		}

		err := writer.WriteStage(ctx, rec)
		require.NoError(t, err)
	}

	// Retrieve all stages
	logs, err := writer.GetStageLogs(ctx, tenantID, sessionID, turnNo)
	require.NoError(t, err)
	require.Len(t, logs, 6)

	// Verify order (should be sorted by started_at ASC)
	for i, log := range logs {
		assert.Equal(t, stages[i].stage, log.Stage)
		assert.Equal(t, stages[i].status, log.StageStatus)
	}
}

// TestTurnLogsWriter_ErrorStage tests writing a failed stage with error message
func TestTurnLogsWriter_ErrorStage(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnLogsWriter(db)

	rec := TurnLogRecord{
		SessionID:   "test_session_logs_error",
		TurnNo:      1,
		TenantID:    "test_tenant",
		RequestID:   "req_logs_error",
		Stage:       "llm_call",
		StageStatus: "failed",
		EventData: map[string]interface{}{
			"provider":   "openai",
			"model":      "gpt-4",
			"status_code": 500,
		},
		ErrorMsg:    "Connection timeout",
		StartedAt:   time.Now().Add(-1 * time.Second),
		CompletedAt: time.Now(),
	}

	ctx := context.Background()

	err := writer.WriteStage(ctx, rec)
	require.NoError(t, err)

	// Retrieve and verify error
	logs, err := writer.GetStageLogs(ctx, rec.TenantID, rec.SessionID, rec.TurnNo)
	require.NoError(t, err)
	require.Len(t, logs, 1)

	log := logs[0]
	assert.Equal(t, "failed", log.StageStatus)
	assert.Equal(t, "Connection timeout", log.ErrorMsg)
	assert.Equal(t, "openai", log.EventData["provider"])
}

// TestTurnLogsWriter_GetAllSessionLogs tests retrieving all logs for a session
func TestTurnLogsWriter_GetAllSessionLogs(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnLogsWriter(db)

	sessionID := "test_session_logs_all"
	tenantID := "test_tenant"
	ctx := context.Background()

	// Create logs for 3 turns, 2 stages each
	for turnNo := 1; turnNo <= 3; turnNo++ {
		for stageIdx, stage := range []string{"routing", "llm_call"} {
			rec := TurnLogRecord{
				SessionID:   sessionID,
				TurnNo:      turnNo,
				TenantID:    tenantID,
				RequestID:   fmt.Sprintf("req_all_%d", turnNo),
				Stage:       stage,
				StageStatus: "success",
				EventData: map[string]interface{}{
					"turn":  turnNo,
					"stage": stage,
				},
				StartedAt:   time.Now().Add(time.Duration(turnNo*10+stageIdx) * time.Millisecond),
				CompletedAt: time.Now().Add(time.Duration(turnNo*10+stageIdx+5) * time.Millisecond),
			}

			err := writer.WriteStage(ctx, rec)
			require.NoError(t, err)
		}
	}

	// Retrieve all session logs
	logs, err := writer.GetAllSessionLogs(ctx, tenantID, sessionID)
	require.NoError(t, err)
	require.Len(t, logs, 6) // 3 turns * 2 stages

	// Verify order: turn_no ASC, started_at ASC
	expectedTurns := []int{1, 1, 2, 2, 3, 3}
	expectedStages := []string{"routing", "llm_call", "routing", "llm_call", "routing", "llm_call"}

	for i, log := range logs {
		assert.Equal(t, expectedTurns[i], log.TurnNo)
		assert.Equal(t, expectedStages[i], log.Stage)
	}
}

// TestTurnLogsWriter_AggregateSessionLogs tests log aggregation
func TestTurnLogsWriter_AggregateSessionLogs(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnLogsWriter(db)

	sessionID := "test_session_logs_aggregate"
	tenantID := "test_tenant"
	ctx := context.Background()

	// Create logs for 2 turns
	stages := []struct {
		turnNo int
		stage  string
	}{
		{1, "routing"},
		{1, "llm_call"},
		{2, "routing"},
		{2, "compression"},
		{2, "llm_call"},
	}

	for i, s := range stages {
		rec := TurnLogRecord{
			SessionID:   sessionID,
			TurnNo:      s.turnNo,
			TenantID:    tenantID,
			RequestID:   fmt.Sprintf("req_agg_%d", s.turnNo),
			Stage:       s.stage,
			StageStatus: "success",
			EventData: map[string]interface{}{
				"index": i,
			},
			StartedAt:   time.Now().Add(time.Duration(i*10) * time.Millisecond),
			CompletedAt: time.Now().Add(time.Duration(i*10+5) * time.Millisecond),
		}

		err := writer.WriteStage(ctx, rec)
		require.NoError(t, err)
	}

	// Aggregate logs
	summary, err := writer.AggregateSessionLogs(ctx, tenantID, sessionID)
	require.NoError(t, err)
	require.NotNil(t, summary)

	// Verify summary structure
	assert.Equal(t, 2, summary["total_turns"])
	assert.NotNil(t, summary["turns"])
	assert.NotNil(t, summary["generated_at"])

	// Verify turn logs
	turns := summary["turns"].(map[int][]map[string]interface{})
	
	// Turn 1 should have 2 stages
	assert.Len(t, turns[1], 2)
	assert.Equal(t, "routing", turns[1][0]["stage"])
	assert.Equal(t, "llm_call", turns[1][1]["stage"])

	// Turn 2 should have 3 stages
	assert.Len(t, turns[2], 3)
	assert.Equal(t, "routing", turns[2][0]["stage"])
	assert.Equal(t, "compression", turns[2][1]["stage"])
	assert.Equal(t, "llm_call", turns[2][2]["stage"])
}

// TestTurnLogsWriter_AggregateWithError tests aggregation with failed stages
func TestTurnLogsWriter_AggregateWithError(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnLogsWriter(db)

	sessionID := "test_session_logs_agg_error"
	tenantID := "test_tenant"
	ctx := context.Background()

	// Write a successful stage
	rec1 := TurnLogRecord{
		SessionID:   sessionID,
		TurnNo:      1,
		TenantID:    tenantID,
		RequestID:   "req_agg_error",
		Stage:       "routing",
		StageStatus: "success",
		EventData:   map[string]interface{}{},
		StartedAt:   time.Now(),
		CompletedAt: time.Now().Add(10 * time.Millisecond),
	}
	err := writer.WriteStage(ctx, rec1)
	require.NoError(t, err)

	// Write a failed stage with error
	rec2 := TurnLogRecord{
		SessionID:   sessionID,
		TurnNo:      1,
		TenantID:    tenantID,
		RequestID:   "req_agg_error",
		Stage:       "llm_call",
		StageStatus: "failed",
		EventData: map[string]interface{}{
			"status_code": 503,
		},
		ErrorMsg:    "Service unavailable",
		StartedAt:   time.Now().Add(10 * time.Millisecond),
		CompletedAt: time.Now().Add(20 * time.Millisecond),
	}
	err = writer.WriteStage(ctx, rec2)
	require.NoError(t, err)

	// Aggregate
	summary, err := writer.AggregateSessionLogs(ctx, tenantID, sessionID)
	require.NoError(t, err)

	turns := summary["turns"].(map[int][]map[string]interface{})
	require.Len(t, turns[1], 2)

	// First stage: success, no error
	assert.Equal(t, "success", turns[1][0]["status"])
	assert.Nil(t, turns[1][0]["error"])

	// Second stage: failed, with error
	assert.Equal(t, "failed", turns[1][1]["status"])
	assert.Equal(t, "Service unavailable", turns[1][1]["error"])
}

// TestTurnLogsWriter_CleanupExpiredLogs tests log cleanup
func TestTurnLogsWriter_CleanupExpiredLogs(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnLogsWriter(db)
	ctx := context.Background()

	// Write a log with expired timestamp (manually)
	// Note: This requires directly inserting with a past expires_at
	_, err := db.Exec(ctx, `
		INSERT INTO gateway.session_turn_logs (
			session_id, turn_no, tenant_id, request_id,
			stage, stage_status, event_data,
			started_at, completed_at, latency_ms,
			expires_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			$8, $9, $10,
			$11
		)
	`,
		"test_session_expired", 1, "test_tenant", "req_expired",
		"routing", "success", []byte("{}"),
		time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour), 10,
		time.Now().Add(-1*time.Hour), // Expired 1 hour ago
	)
	require.NoError(t, err)

	// Write a non-expired log
	rec := TurnLogRecord{
		SessionID:   "test_session_active",
		TurnNo:      1,
		TenantID:    "test_tenant",
		RequestID:   "req_active",
		Stage:       "routing",
		StageStatus: "success",
		EventData:   map[string]interface{}{},
		StartedAt:   time.Now(),
		CompletedAt: time.Now().Add(10 * time.Millisecond),
	}
	err = writer.WriteStage(ctx, rec)
	require.NoError(t, err)

	// Cleanup expired logs
	deleted, err := writer.CleanupExpiredLogs(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted) // Should delete 1 expired log

	// Verify expired log is gone
	logs, err := writer.GetAllSessionLogs(ctx, "test_tenant", "test_session_expired")
	require.NoError(t, err)
	assert.Len(t, logs, 0)

	// Verify active log is still there
	logs, err = writer.GetAllSessionLogs(ctx, "test_tenant", "test_session_active")
	require.NoError(t, err)
	assert.Len(t, logs, 1)
}

// TestTurnLogsWriter_EmptyEventData tests writing stage with empty event data
func TestTurnLogsWriter_EmptyEventData(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnLogsWriter(db)

	rec := TurnLogRecord{
		SessionID:   "test_session_empty_data",
		TurnNo:      1,
		TenantID:    "test_tenant",
		RequestID:   "req_empty",
		Stage:       "cache_update",
		StageStatus: "skipped",
		EventData:   map[string]interface{}{}, // Empty
		StartedAt:   time.Now(),
		CompletedAt: time.Now().Add(1 * time.Millisecond),
	}

	ctx := context.Background()

	err := writer.WriteStage(ctx, rec)
	require.NoError(t, err)

	logs, err := writer.GetStageLogs(ctx, rec.TenantID, rec.SessionID, rec.TurnNo)
	require.NoError(t, err)
	require.Len(t, logs, 1)

	assert.Equal(t, "skipped", logs[0].StageStatus)
	assert.NotNil(t, logs[0].EventData)
	assert.Len(t, logs[0].EventData, 0)
}

// TestTurnLogsWriter_MultipleTurnsIsolation tests turn isolation
func TestTurnLogsWriter_MultipleTurnsIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	writer := NewTurnLogsWriter(db)

	sessionID := "test_session_turn_isolation"
	tenantID := "test_tenant"
	ctx := context.Background()

	// Write logs for 3 different turns
	for turnNo := 1; turnNo <= 3; turnNo++ {
		rec := TurnLogRecord{
			SessionID:   sessionID,
			TurnNo:      turnNo,
			TenantID:    tenantID,
			RequestID:   fmt.Sprintf("req_isolation_%d", turnNo),
			Stage:       "routing",
			StageStatus: "success",
			EventData: map[string]interface{}{
				"turn": turnNo,
			},
			StartedAt:   time.Now(),
			CompletedAt: time.Now().Add(10 * time.Millisecond),
		}

		err := writer.WriteStage(ctx, rec)
		require.NoError(t, err)
	}

	// Verify each turn has its own isolated log
	for turnNo := 1; turnNo <= 3; turnNo++ {
		logs, err := writer.GetStageLogs(ctx, tenantID, sessionID, turnNo)
		require.NoError(t, err)
		require.Len(t, logs, 1)

		assert.Equal(t, turnNo, logs[0].TurnNo)
		assert.Equal(t, float64(turnNo), logs[0].EventData["turn"])
	}
}
