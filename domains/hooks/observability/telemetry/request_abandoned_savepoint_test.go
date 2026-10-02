package telemetry

// request_abandoned_savepoint_test.go — R35 审计 P1 根修的行为级钉测。
//
// 54a63d0e5 落地时声明 fail-open（「表不存在只 warn，主写不受影响」，见
// 819 迁移头/down 指引/yml runbook 三处），但实现里 marker 语句与主写在同
// 一显式事务且排在主写**之前**、无 SAVEPOINT——语句级错误把事务置为
// aborted，后续 usage_ledger_hot 等全部 25P02，request_logs 主写整体失败。
// 修复=mark/clear 各包 SAVEPOINT（照抄 gw_final_success_claim 先例），实现
// 与三处文档声明对齐。
//
// 本文件钉行为而非文本：mock 出「表不存在（42P01）」后，同事务的后续主写
// 必须照常成功——这正是落地时缺失、导致 P1 被审计抓住的那类测试。

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func abandonedTestEntry(requestID string) *RequestLogEntry {
	status := RequestStatusInProgress
	return &RequestLogEntry{
		RequestID:     requestID,
		RequestStatus: &status,
		EventAt:       func() *time.Time { t := time.Now(); return &t }(),
	}
}

// 表缺失（42P01）时：savepoint 隔离生效，后续主写（同事务）照常成功，
// mark_failed 计数器记账。P1 的精确症状钉测。
func TestMarkRequestAbandoned_TableMissingDoesNotPoisonTx(t *testing.T) {
	mock, err := pgxmock.NewConn()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mock.Close(context.Background()) })
	mock.ExpectBegin()
	tx, err := mock.Begin(context.Background())
	require.NoError(t, err)

	failedBefore := testutil.ToFloat64(requestAbandonedMarkerOps.WithLabelValues("mark_failed"))
	markBefore := testutil.ToFloat64(requestAbandonedMarkerOps.WithLabelValues("mark"))

	mock.ExpectExec(`SAVEPOINT gw_req_abandoned_mark`).WillReturnResult(pgconn.NewCommandTag("SAVEPOINT"))
	mock.ExpectExec(`INSERT INTO public.request_abandoned`).
		WithArgs(anyArgs(8)...).
		WillReturnError(&pgconn.PgError{Code: "42P01", Message: "relation \"request_abandoned\" does not exist"})
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT gw_req_abandoned_mark`).WillReturnResult(pgconn.NewCommandTag("ROLLBACK"))
	mock.ExpectExec(`RELEASE SAVEPOINT gw_req_abandoned_mark`).WillReturnResult(pgconn.NewCommandTag("RELEASE"))
	// 主写在同事务继续并成功——修复前这里不可能到达（事务已 aborted）。
	mock.ExpectExec(`INSERT INTO request_logs_hot`).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	markRequestAbandonedPending(context.Background(), tx, abandonedTestEntry("req-poison-pin"))

	_, mainErr := tx.Exec(context.Background(), `INSERT INTO request_logs_hot (request_id) VALUES ('req-poison-pin')`)
	require.NoError(t, mainErr, "main write must survive a failed marker (declared fail-open)")
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, float64(1), testutil.ToFloat64(requestAbandonedMarkerOps.WithLabelValues("mark_failed"))-failedBefore)
	require.Equal(t, float64(0), testutil.ToFloat64(requestAbandonedMarkerOps.WithLabelValues("mark"))-markBefore)
}

// 正常路径：SAVEPOINT → INSERT → RELEASE，mark 计数。
func TestMarkRequestAbandoned_HappyPathReleasesSavepoint(t *testing.T) {
	mock, err := pgxmock.NewConn()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mock.Close(context.Background()) })
	mock.ExpectBegin()
	tx, err := mock.Begin(context.Background())
	require.NoError(t, err)

	markBefore := testutil.ToFloat64(requestAbandonedMarkerOps.WithLabelValues("mark"))

	mock.ExpectExec(`SAVEPOINT gw_req_abandoned_mark`).WillReturnResult(pgconn.NewCommandTag("SAVEPOINT"))
	mock.ExpectExec(`INSERT INTO public.request_abandoned`).WithArgs(anyArgs(8)...).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`RELEASE SAVEPOINT gw_req_abandoned_mark`).WillReturnResult(pgconn.NewCommandTag("RELEASE"))

	markRequestAbandonedPending(context.Background(), tx, abandonedTestEntry("req-happy-pin"))

	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, float64(1), testutil.ToFloat64(requestAbandonedMarkerOps.WithLabelValues("mark"))-markBefore)
}

// clear 半边同款：表缺失时 savepoint 隔离，主写（终态 UPDATE）照常。
func TestClearRequestAbandoned_TableMissingDoesNotPoisonTx(t *testing.T) {
	mock, err := pgxmock.NewConn()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mock.Close(context.Background()) })
	mock.ExpectBegin()
	tx, err := mock.Begin(context.Background())
	require.NoError(t, err)

	failedBefore := testutil.ToFloat64(requestAbandonedMarkerOps.WithLabelValues("clear_failed"))

	mock.ExpectExec(`SAVEPOINT gw_req_abandoned_clear`).WillReturnResult(pgconn.NewCommandTag("SAVEPOINT"))
	mock.ExpectExec(`DELETE FROM public.request_abandoned`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(&pgconn.PgError{Code: "42P01", Message: "relation \"request_abandoned\" does not exist"})
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT gw_req_abandoned_clear`).WillReturnResult(pgconn.NewCommandTag("ROLLBACK"))
	mock.ExpectExec(`RELEASE SAVEPOINT gw_req_abandoned_clear`).WillReturnResult(pgconn.NewCommandTag("RELEASE"))
	mock.ExpectExec(`UPDATE usage_ledger_hot`).WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))

	clearRequestAbandonedPending(context.Background(), tx, "req-clear-pin")

	_, mainErr := tx.Exec(context.Background(), `UPDATE usage_ledger_hot SET prompt_tokens = 1 WHERE request_id = 'req-clear-pin'`)
	require.NoError(t, mainErr, "terminal write must survive a failed clear (declared fail-open)")
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, float64(1), testutil.ToFloat64(requestAbandonedMarkerOps.WithLabelValues("clear_failed"))-failedBefore)
}

// clear 正常路径（R36 补：此前 clear 只有失败腿，而 clear 是自称承重的
// 一半——no-op 化/漏 RELEASE/漏计数的退化在全测试族都不会红）：SAVEPOINT →
// DELETE → RELEASE，clear 计数。
func TestClearRequestAbandoned_HappyPathReleasesSavepoint(t *testing.T) {
	mock, err := pgxmock.NewConn()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mock.Close(context.Background()) })
	mock.ExpectBegin()
	tx, err := mock.Begin(context.Background())
	require.NoError(t, err)

	clearBefore := testutil.ToFloat64(requestAbandonedMarkerOps.WithLabelValues("clear"))

	mock.ExpectExec(`SAVEPOINT gw_req_abandoned_clear`).WillReturnResult(pgconn.NewCommandTag("SAVEPOINT"))
	mock.ExpectExec(`DELETE FROM public.request_abandoned`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgconn.NewCommandTag("DELETE 1"))
	mock.ExpectExec(`RELEASE SAVEPOINT gw_req_abandoned_clear`).WillReturnResult(pgconn.NewCommandTag("RELEASE"))

	clearRequestAbandonedPending(context.Background(), tx, "req-clear-happy-pin")

	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, float64(1), testutil.ToFloat64(requestAbandonedMarkerOps.WithLabelValues("clear"))-clearBefore)
}

// 谓词门（R35 P2 引入，R36 订正为正向终态判定）：updateRequestLog 的 clear
// 调用必须包在 `if requestLogEntryTerminal(entry)` 守卫内——只有携带终态
// 证据的 UPDATE 才允许删活标记，中途 enrichment UPDATE（in_progress）与
// nil/脏值方向都取「保留标记」。文本位置门，与 request_abandoned_gate_test.go
// 同族三自由度法（存在/归属/顺序）。
func TestAbandonedClearCallIsPredicateGuarded(t *testing.T) {
	src := clientSource(t)
	body := extractFuncBody(t, src, "updateRequestLog")
	callAt := indexOfSubstring(t, body, "clearRequestAbandonedPending(ctx, tx, entry.RequestID)")
	guardAt := indexOfSubstring(t, body, "if requestLogEntryTerminal(entry)")
	if guardAt < 0 || guardAt > callAt {
		t.Fatalf("clear call must be nested under `if requestLogEntryTerminal(entry)` guard (guard@%d call@%d) — a mid-flight enrichment UPDATE would delete a live marker", guardAt, callAt)
	}
}

func indexOfSubstring(t *testing.T, s, sub string) int {
	t.Helper()
	i := indexOf(s, sub)
	if i < 0 {
		t.Fatalf("substring not found: %q", sub)
	}
	return i
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
