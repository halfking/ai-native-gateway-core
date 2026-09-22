// system_fingerprint_persist_test.go — 2026-09-12 指纹写路径贯通回归。
//
// 背景：X-System-Fingerprint 响应头此前只进 integrity detector 的 context
// JSONB，request_logs_hot.system_fingerprint 专用列（603）全表零行，7 天
// 漂移检测器空转。本次接线：handler emitTelemetry 盖章 entry.SystemFingerprint
// → persistSystemFingerprint 落列 → 迁移 697 起随 promote 进分区。
package telemetry

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

const fingerprintPersistPattern = `UPDATE request_logs_hot\s+SET system_fingerprint = \$2\s+WHERE request_id = \$1`

// 带 SystemFingerprint 的终态更新：指纹小 UPDATE 必须出现在同一事务
// （bodies upsert 之后、claim SAVEPOINT 之前）。
func TestUpdateRequestLog_PersistsSystemFingerprint(t *testing.T) {
	mock, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer func() { _ = mock.Close(context.Background()) }()

	fp := "fp-abc123"
	entry := &RequestLogEntry{
		RequestID:         "req-fp-1",
		Op:                RequestLogUpdate,
		TenantID:          "default",
		Success:           true,
		RequestStatus:     strptr(RequestStatusSuccess),
		GwSessionID:       strptr("sess-fp-1"),
		SystemFingerprint: &fp,
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs(anyArgs(100)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// 指纹落列（697 贯通）
	mock.ExpectExec(fingerprintPersistPattern).
		WithArgs("req-fp-1", fp).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// heap 分区目录查询（claim 前置，空结果 → promoted 守卫跳过）
	mock.ExpectQuery(`FROM pg_inherits`).
		WillReturnRows(pgxmock.NewRows([]string{"relname"}))
	mock.ExpectExec(`SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
	mock.ExpectExec(claimSQLPattern).
		WithArgs("req-fp-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`RELEASE SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("RELEASE", 0))
	mock.ExpectCommit()

	c := NewClient()
	c.requestLogDB = mock

	require.NoError(t, c.updateRequestLog(entry))
	require.NoError(t, mock.ExpectationsWereMet())
}

// 无指纹（上游未返回该头）时语句集合与历史完全一致——persist 必须 no-op，
// 不得给既有语句序列加行。
func TestUpdateRequestLog_NoFingerprintSkipsPersist(t *testing.T) {
	mock, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer func() { _ = mock.Close(context.Background()) }()

	entry := &RequestLogEntry{
		RequestID:     "req-fp-2",
		Op:            RequestLogUpdate,
		TenantID:      "default",
		Success:       true,
		RequestStatus: strptr(RequestStatusSuccess),
		GwSessionID:   strptr("sess-fp-2"),
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs(anyArgs(100)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// 无指纹 → 无指纹 UPDATE；直接 heap 分区目录 + claim
	mock.ExpectQuery(`FROM pg_inherits`).
		WillReturnRows(pgxmock.NewRows([]string{"relname"}))
	mock.ExpectExec(`SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
	mock.ExpectExec(claimSQLPattern).
		WithArgs("req-fp-2").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`RELEASE SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("RELEASE", 0))
	mock.ExpectCommit()

	c := NewClient()
	c.requestLogDB = mock

	require.NoError(t, c.updateRequestLog(entry))
	require.NoError(t, mock.ExpectationsWereMet())
}
