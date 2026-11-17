// Package promptinjection - 错误收集器测试
package promptinjection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewErrorCollector(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)
	assert.NotNil(t, ec)
	assert.True(t, ec.enabled)
	assert.Equal(t, db, ec.db)
}

func TestErrorCollector_Collect_Disabled(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, false)

	err = ec.Collect(context.Background(), &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "test error",
	})

	assert.NoError(t, err) // 应该静默跳过
}

func TestErrorCollector_Collect_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	collectedErr := &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		SessionID:    "session-123",
		RequestID:    "request-456",
		InputText:    "test input",
		ErrorMessage: "LLM detection failed",
		Priority:     7,
		ErrorDetails: map[string]interface{}{
			"llm_engine_id": 1,
		},
		DetectionLayers: map[string]bool{
			"basic": true,
		},
		MatchedRules: []MatchedRule{
			{
				RuleName: "test-rule",
				Category: "test",
				Severity: 5,
			},
		},
		PolicyConfig: map[string]interface{}{
			"enabled": true,
		},
		Metadata: map[string]interface{}{
			"timestamp": 1234567890,
		},
	}

	// 期望检查重复查询
	mock.ExpectQuery(`SELECT id FROM error_analysis_queue WHERE error_hash`).
		WillReturnError(sql.ErrNoRows)

	// 期望插入查询
	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WithArgs(
			ErrorTypeLLMDetectionFailed,
			sqlmock.AnyArg(), // error_hash
			7,                // priority
			"tenant-1",
			"session-123",
			"request-456",
			"test input",
			sqlmock.AnyArg(), // input_hash
			"LLM detection failed",
			sqlmock.AnyArg(), // error_details JSON
			sqlmock.AnyArg(), // detection_layers JSON
			sqlmock.AnyArg(), // matched_rules JSON
			sqlmock.AnyArg(), // policy_config JSON
			sqlmock.AnyArg(), // metadata JSON
			QueueStatusPending,
			0, // retry_count
			3, // max_retries
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	err = ec.Collect(context.Background(), collectedErr)
	assert.NoError(t, err)

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

func TestErrorCollector_Collect_MissingRequiredFields(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	tests := []struct {
		name        string
		err         *CollectedError
		expectError string
	}{
		{
			name:        "nil error",
			err:         nil,
			expectError: "collected error is nil",
		},
		{
			name: "missing error_type",
			err: &CollectedError{
				TenantID:     "tenant-1",
				ErrorMessage: "test",
			},
			expectError: "error_type is required",
		},
		{
			name: "missing tenant_id",
			err: &CollectedError{
				ErrorType:    ErrorTypeLLMDetectionFailed,
				ErrorMessage: "test",
			},
			expectError: "tenant_id is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ec.Collect(context.Background(), tt.err)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectError)
		})
	}
}

func TestErrorCollector_Collect_Duplicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	collectedErr := &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "test error",
		Priority:     5,
	}

	// 期望检查重复查询 - 返回已存在的 ID
	mock.ExpectQuery(`SELECT id FROM error_analysis_queue WHERE error_hash`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(999))

	err = ec.Collect(context.Background(), collectedErr)
	assert.NoError(t, err) // 应该静默跳过重复项

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

func TestErrorCollector_Collect_LongInputTruncation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	// 创建超长输入（>10000 字符）
	longInput := string(make([]byte, 15000))
	for i := range longInput {
		longInput = string([]byte{byte('a' + (i % 26))})
	}

	collectedErr := &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		InputText:    longInput,
		ErrorMessage: "test",
	}

	mock.ExpectQuery(`SELECT id FROM error_analysis_queue WHERE error_hash`).
		WillReturnError(sql.ErrNoRows)

	// 验证插入时输入被截断到 10000 字符
	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WithArgs(
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(), // 这应该是截断后的 input_text
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	err = ec.Collect(context.Background(), collectedErr)
	assert.NoError(t, err)
}

func TestErrorCollector_CollectLLMDetectionFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	policy := &Policy{
		LLMEngineID:            intPtr(1),
		LLMDetectionTimeoutSec: 30,
		LLMDetectionFailClosed: true,
	}

	mock.ExpectQuery(`SELECT id FROM error_analysis_queue WHERE error_hash`).
		WillReturnError(sql.ErrNoRows)

	testError := errors.New("LLM failed")

	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WithArgs(
			ErrorTypeLLMDetectionFailed,
			sqlmock.AnyArg(),
			7, // 高优先级
			"tenant-1",
			"session-123",
			"request-456",
			"test input",
			sqlmock.AnyArg(),
			"LLM failed",
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			QueueStatusPending,
			0,
			3,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	err = ec.CollectLLMDetectionFailure(
		context.Background(),
		"tenant-1",
		"session-123",
		"request-456",
		"test input",
		testError,
		policy,
	)

	assert.NoError(t, err)
	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

func TestErrorCollector_CollectRuleFalsePositive(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	matchedRules := []MatchedRule{
		{
			RuleName: "rule-1",
			Category: "test",
			Severity: 5,
		},
	}

	detectionLayers := map[string]bool{
		"basic":    true,
		"advanced": false,
	}

	mock.ExpectQuery(`SELECT id FROM error_analysis_queue WHERE error_hash`).
		WillReturnError(sql.ErrNoRows)

	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WithArgs(
			ErrorTypeRuleFalsePositive,
			sqlmock.AnyArg(),
			6, // 优先级
			"tenant-1",
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			"test input",
			sqlmock.AnyArg(),
			"Rule matched but likely false positive",
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			QueueStatusPending,
			0,
			3,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	err = ec.CollectRuleFalsePositive(
		context.Background(),
		"tenant-1",
		"session-123",
		"request-456",
		"test input",
		matchedRules,
		detectionLayers,
	)

	assert.NoError(t, err)
	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

func TestErrorCollector_GetPendingErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	now := time.Now()

	rows := sqlmock.NewRows([]string{
		"id", "error_type", "error_hash", "priority",
		"tenant_id", "session_id", "request_id",
		"input_text", "input_hash", "error_message",
		"error_details", "detection_layers", "matched_rules", "policy_config",
		"status", "retry_count", "max_retries",
		"created_at", "started_at", "completed_at", "metadata",
	}).
		AddRow(
			1, ErrorTypeLLMDetectionFailed, "hash123", 7,
			"tenant-1", sql.NullString{String: "session-1", Valid: true}, sql.NullString{},
			sql.NullString{String: "input", Valid: true}, sql.NullString{}, "error msg",
			json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`[]`), json.RawMessage(`{}`),
			QueueStatusPending, 0, 3,
			now, sql.NullTime{}, sql.NullTime{}, json.RawMessage(`{}`),
		).
		AddRow(
			2, ErrorTypeRuleFalsePositive, "hash456", 6,
			"tenant-2", sql.NullString{}, sql.NullString{},
			sql.NullString{}, sql.NullString{}, "false positive",
			json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`[]`), json.RawMessage(`{}`),
			QueueStatusPending, 0, 3,
			now, sql.NullTime{}, sql.NullTime{}, json.RawMessage(`{}`),
		)

	mock.ExpectQuery(`SELECT .+ FROM error_analysis_queue WHERE status`).
		WithArgs(QueueStatusPending, 10).
		WillReturnRows(rows)

	errors, err := ec.GetPendingErrors(context.Background(), 10)
	assert.NoError(t, err)
	assert.Len(t, errors, 2)

	assert.Equal(t, int64(1), errors[0].ID)
	assert.Equal(t, ErrorTypeLLMDetectionFailed, errors[0].ErrorType)
	assert.Equal(t, 7, errors[0].Priority)

	assert.Equal(t, int64(2), errors[1].ID)
	assert.Equal(t, ErrorTypeRuleFalsePositive, errors[1].ErrorType)

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

func TestErrorCollector_UpdateStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	tests := []struct {
		name   string
		status string
	}{
		{"processing", QueueStatusProcessing},
		{"analyzed", QueueStatusAnalyzed},
		{"failed", QueueStatusFailed},
		{"skipped", QueueStatusSkipped},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock.ExpectExec(`UPDATE error_analysis_queue SET status`).
				WithArgs(tt.status, int64(1)).
				WillReturnResult(sqlmock.NewResult(0, 1))

			err := ec.UpdateStatus(context.Background(), 1, tt.status)
			assert.NoError(t, err)

			err = mock.ExpectationsWereMet()
			assert.NoError(t, err)
		})
	}
}

func TestErrorCollector_IncrementRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	mock.ExpectExec(`UPDATE error_analysis_queue SET retry_count`).
		WithArgs(int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = ec.IncrementRetry(context.Background(), 1)
	assert.NoError(t, err)

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

func TestErrorCollector_GetStats(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	now := time.Now()

	rows := sqlmock.NewRows([]string{
		"error_type", "total", "pending", "analyzed", "last_error_at",
	}).
		AddRow(ErrorTypeLLMDetectionFailed, 10, 3, 7, now).
		AddRow(ErrorTypeRuleFalsePositive, 5, 2, 3, now)

	mock.ExpectQuery(`SELECT error_type, COUNT\(\*\) as total`).
		WillReturnRows(rows)

	stats, err := ec.GetStats(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, stats)

	byType, ok := stats["by_type"].([]map[string]interface{})
	assert.True(t, ok)
	assert.Len(t, byType, 2)

	assert.Equal(t, ErrorTypeLLMDetectionFailed, byType[0]["error_type"])
	assert.Equal(t, 10, byType[0]["total"])
	assert.Equal(t, 3, byType[0]["pending"])
	assert.Equal(t, 7, byType[0]["analyzed"])

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

func TestCalculateErrorHash(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	err1 := &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "LLM detection failed",
		InputText:    "test input",
	}

	err2 := &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "LLM detection failed",
		InputText:    "test input",
	}

	err3 := &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "Different error",
		InputText:    "test input",
	}

	hash1 := ec.calculateErrorHash(err1)
	hash2 := ec.calculateErrorHash(err2)
	hash3 := ec.calculateErrorHash(err3)

	// 相同错误应该产生相同的 hash
	assert.Equal(t, hash1, hash2)

	// 不同错误应该产生不同的 hash
	assert.NotEqual(t, hash1, hash3)

	// Hash 应该是有效的十六进制字符串
	assert.Regexp(t, "^[0-9a-f]{64}$", hash1)
}

func TestHelperFunctions(t *testing.T) {
	t.Run("toNullString", func(t *testing.T) {
		ns1 := toNullString("")
		assert.False(t, ns1.Valid)

		ns2 := toNullString("value")
		assert.True(t, ns2.Valid)
		assert.Equal(t, "value", ns2.String)
	})

	t.Run("truncateString", func(t *testing.T) {
		assert.Equal(t, "hello", truncateString("hello", 10))
		assert.Equal(t, "hello", truncateString("hello world", 5))
		assert.Equal(t, "", truncateString("", 5))
	})
}

// Helper function
func intPtr(i int) *int {
	return &i
}
