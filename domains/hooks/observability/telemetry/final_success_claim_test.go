// final_success_claim_test.go — v4 T7 (migration 532) 会话级唯一成功 claim 单测。
//
// 覆盖测试方案 G10：
//
//	UT-FS-01  final-success claim：SQL 形状（同事务 + NOT EXISTS 双检查 +
//	          savepoint）、并发冲突（23505）降级为普通成功；
//	UT-FS-02  5 次重发回归（单元侧）：只有成功终态 + 非空 gw_session_id 的
//	          entry 触发 claim，失败/取消/空会话路径完全不触碰 claim
//	          （库级唯一性由部分唯一索引保证，集成测试覆盖）。
//
// DB mock 用 pgxmock（现有 telemetry 测试基建，见 request_logger_test.go）。
package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func newClaimMock(t *testing.T) (pgxmock.PgxConnIface, pgx.Tx) {
	t.Helper()
	mock, err := pgxmock.NewConn()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mock.Close(context.Background()) })

	mock.ExpectBegin()
	tx, err := mock.Begin(context.Background())
	require.NoError(t, err)
	return mock, tx
}

// anyArgs 生成 n 个 AnyArg 匹配器（大参数量语句用）。
func anyArgs(n int) []interface{} {
	args := make([]interface{}, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

// claimSQLPattern 钉死 claim 语句的语义要素：自守卫（成功终态 + 非空会话）、
// hot 侧 NOT EXISTS（排除自身）。2026-08-25: promoted 分区守卫 (跨 7 天窗口)
// 改为动态拼装 (列存安全), 默认 nil 分支 (单测无 Client 注册) 不含该子句.
// 集成测试覆盖 promoted guard 拼接: TestClaimSessionFinalSuccess_HeapPartitionsGuard。
const claimSQLPattern = `UPDATE request_logs_hot\s+SET is_final_success = TRUE\s+WHERE request_id = \$1\s+AND success = TRUE\s+AND request_status = 'success'\s+AND COALESCE\(gw_session_id, ''\) <> ''\s+AND NOT EXISTS[\s\S]*FROM request_logs_hot other[\s\S]*other\.request_id <> request_logs_hot\.request_id`

func TestClaimSessionFinalSuccess_GrantPathRunsInSameTxWithSavepoint(t *testing.T) {
	mock, tx := newClaimMock(t)

	mock.ExpectExec(`SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
	mock.ExpectExec(claimSQLPattern).
		WithArgs("req-claim-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`RELEASE SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("RELEASE", 0))

	claimSessionFinalSuccess(context.Background(), tx, "req-claim-1")

	require.NoError(t, mock.ExpectationsWereMet())
}

// UT-FS-01（竞争失败方）：唯一索引冲突（23505）必须降级为普通成功——
// 回滚到 savepoint，绝不让错误冒泡导致业务行丢失。
func TestClaimSessionFinalSuccess_UniqueViolationDegradesToNormalSuccess(t *testing.T) {
	mock, tx := newClaimMock(t)

	mock.ExpectExec(`SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
	mock.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs("req-claim-loser").
		WillReturnError(&pgconn.PgError{
			Code:    "23505",
			Message: "duplicate key value violates unique constraint \"uq_request_logs_hot_final_success_session\"",
		})
	// 降级路径：回滚 savepoint，且不再 RELEASE。
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("ROLLBACK", 0))

	claimSessionFinalSuccess(context.Background(), tx, "req-claim-loser")

	require.NoError(t, mock.ExpectationsWereMet())
}

// 滚动发布窗口（migration 532 未应用，列不存在 42703）等其它错误同样降级，
// 不阻塞 request_logs 主写路径。
func TestClaimSessionFinalSuccess_OtherErrorsDegrade(t *testing.T) {
	mock, tx := newClaimMock(t)

	mock.ExpectExec(`SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
	mock.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs("req-claim-42703").
		WillReturnError(&pgconn.PgError{
			Code:    "42703",
			Message: "column \"is_final_success\" of relation \"request_logs_hot\" does not exist",
		})
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("ROLLBACK", 0))

	claimSessionFinalSuccess(context.Background(), tx, "req-claim-42703")

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimSessionFinalSuccess_Guards(t *testing.T) {
	// 空 requestID / nil tx：不产生任何语句。
	claimSessionFinalSuccess(context.Background(), nil, "req-x")
	claimSessionFinalSuccess(context.Background(), nil, "")
}

// UT-FS-02（单元侧）：只有「成功终态 + 非空 gw_session_id」才尝试 claim。
// 失败、取消、客户端断开（success=FALSE）与空会话行完全不触碰 claim。
func TestShouldClaimFinalSuccess(t *testing.T) {
	cases := []struct {
		name  string
		entry *RequestLogEntry
		want  bool
	}{
		{"success with session", &RequestLogEntry{RequestID: "r1", Success: true, GwSessionID: strptr("sess-1")}, true},
		{"failure never claims", &RequestLogEntry{RequestID: "r2", Success: false, GwSessionID: strptr("sess-1")}, false},
		{"disconnect (success=false) never claims", &RequestLogEntry{RequestID: "r3", Success: false, GwSessionID: strptr("sess-1"), ErrorKind: strptr("client_disconnect")}, false},
		{"nil session skips", &RequestLogEntry{RequestID: "r4", Success: true}, false},
		{"empty session skips", &RequestLogEntry{RequestID: "r5", Success: true, GwSessionID: strptr("")}, false},
		{"nil entry", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldClaimFinalSuccess(tc.entry))
		})
	}
}

// UT-FS-02（写路径回归）：updateRequestLog 在成功终态事务内恰好追加一次
// claim（SAVEPOINT → claim UPDATE → RELEASE），且不改变既有语句集合
// （usage_ledger → request_logs_hot 终态 UPDATE → bodies upsert）。
func TestUpdateRequestLog_AppendsFinalSuccessClaimOnTerminalSuccess(t *testing.T) {
	mock, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer func() { _ = mock.Close(context.Background()) }()

	sessionID := "sess-dup-054"
	entry := &RequestLogEntry{
		RequestID:     "req-054-3",
		Op:            RequestLogUpdate,
		TenantID:      "default",
		Success:       true,
		RequestStatus: strptr(RequestStatusSuccess),
		GwSessionID:   strptr(sessionID),
	}

	mock.ExpectBegin()
	// usage_ledger_hot（无 token 字段的分支）
	mock.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// request_logs_hot 终态 UPDATE（RETURNING ts，97 个绑定参数；含 t0..t9）
	mock.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs(anyArgs(96)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// bodies 侧表 upsert
	mock.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(anyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// v4 T7 claim（同事务）
	mock.ExpectExec(`SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
	mock.ExpectExec(claimSQLPattern).
		WithArgs("req-054-3").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`RELEASE SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("RELEASE", 0))
	mock.ExpectCommit()

	c := NewClient()
	c.SetDB(nil) // requestLogDatabase() falls back to dbPool via interface below
	// 直接注入 mock（SetDB 只收 pgxpool，测试走接口字段）。
	c.requestLogDB = mock

	require.NoError(t, c.updateRequestLog(entry))
	require.NoError(t, mock.ExpectationsWereMet())
}

// 失败路径不 claim：除了主语句外不应出现任何 SAVEPOINT/claim 语句。
func TestUpdateRequestLog_FailurePathSkipsClaim(t *testing.T) {
	mock, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer func() { _ = mock.Close(context.Background()) }()

	entry := &RequestLogEntry{
		RequestID:     "req-fail-1",
		Op:            RequestLogUpdate,
		TenantID:      "default",
		Success:       false,
		RequestStatus: strptr(RequestStatusFailure),
		GwSessionID:   strptr("sess-1"),
		ErrorKind:     strptr("upstream_error"),
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs(anyArgs(96)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(anyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	c := NewClient()
	c.requestLogDB = mock

	require.NoError(t, c.updateRequestLog(entry))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestClaimSessionFinalSuccess_HeapPartitionsGuard 验证 2026-08-25 columnar 安全修复:
// 当 Client 注册了非空 heap 月度分区列表, claim 语句必须拼装 promoted guard
// (UNION ALL 多个 heap 月度分区, 排除 columnar 月份). 这覆盖生产路径, 避免
// `FROM request_logs promoted` 触发 SQLSTATE 0A000.
func TestClaimSessionFinalSuccess_HeapPartitionsGuard(t *testing.T) {
	mock, tx := newClaimMock(t)

	// SetClaimClient 把当前实例注入 holder, claim 路径会取其 heap 月度分区.
	prev := loadClientForClaim()
	t.Cleanup(func() { SetClaimClient(prev) })

	c := NewClient()
	c.heapPartitions = []string{
		"request_logs_2026_07", // 列存 (白名单外) — 不在 heap 列表
		"request_logs_2026_08", // 已转 heap (P1-5 修复)
		"request_logs_2026_09", // heap
	}
	c.heapPartitionsCachedAt = time.Now()
	SetClaimClient(c)

	mock.ExpectExec(`SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
	// 期望 claim SQL 包含 UNION ALL heap 分区名 (白名单过滤) + 接受 1 个参数 (req id)
	expectedGuard := `UNION ALL[\s\S]*request_logs_2026_08[\s\S]*request_logs_2026_09[\s\S]*promoted\.gw_session_id`
	mock.ExpectExec(expectedGuard).
		WithArgs("req-claim-guard").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`RELEASE SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("RELEASE", 0))

	claimSessionFinalSuccess(context.Background(), tx, "req-claim-guard")
	require.NoError(t, mock.ExpectationsWereMet())
}
