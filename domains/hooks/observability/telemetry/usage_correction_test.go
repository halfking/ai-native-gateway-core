package telemetry

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// CO-2 (2026-08-15): estimated 行回填修正。当后续真实 usage 到达
// （同 request_id 的重试成功/补写路径）时，CorrectEstimatedUsage 把
// request_logs 行从 usage_source='estimated' 修正为 'corrected' 并覆盖
// token 列。只在 estimated 行生效 —— llm/corrected 行不受影响（幂等）。
func TestCorrectEstimatedUsage_UpdatesEstimatedRow(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	prompt, completion := 120, 340
	mockDB.ExpectExec(`UPDATE request_logs_hot[\s\S]*usage_source = 'corrected'[\s\S]*WHERE request_id = \$1[\s\S]*AND usage_source = 'estimated'`).
		WithArgs("req-correct", &prompt, &completion, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	client := &Client{requestLogDB: mockDB}
	rows, err := client.CorrectEstimatedUsage(context.Background(), &RequestLogEntry{
		RequestID:        "req-correct",
		PromptTokens:     &prompt,
		CompletionTokens: &completion,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

// 非 estimated 行（0 rows affected）不算错误，调用方据此跳过 format_anomalies 回填。
func TestCorrectEstimatedUsage_NoEstimatedRowIsNoop(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	client := &Client{requestLogDB: mockDB}
	prompt := 10
	rows, err := client.CorrectEstimatedUsage(context.Background(), &RequestLogEntry{
		RequestID:    "req-llm",
		PromptTokens: &prompt,
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), rows)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

// 没有真实 token 就没有可回填的值：不发 SQL，直接返回 0。
func TestCorrectEstimatedUsage_SkipsWhenNoRealUsage(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	client := &Client{requestLogDB: mockDB}
	rows, err := client.CorrectEstimatedUsage(context.Background(), &RequestLogEntry{
		RequestID: "req-no-usage",
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), rows)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

// 回填是 estimated → corrected 状态机的终态：后续携带 usage_source='llm' 的
// 常规 UPDATE（与回填并发排队时）不得把 corrected 行降级回 llm。
func TestUpdateRequestLog_DoesNotDowngradeCorrectedUsageSource(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	status := RequestStatusSuccess
	mockDB.ExpectExec(`UPDATE request_logs_hot[\s\S]*CASE WHEN usage_source = 'corrected' THEN usage_source[\s\S]*ELSE COALESCE\(NULLIF\(\$36, ''\), usage_source\) END`).
		WithArgs(requestLogUpdateArgs(RequestLogEntry{Success: true, RequestStatus: &status})...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: mockDB}
	err = client.updateRequestLog(&RequestLogEntry{
		RequestID:     "req-guard",
		Op:            RequestLogUpdate,
		Success:       true,
		RequestStatus: &status,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}
