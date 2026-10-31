package telemetry

// ★ 2026-10-29（asm 投影缺口 reconciliation 轮）：updateRequestLog 终态路径
// 补发 opened 的守门用例。
//
// 缺口形态（211 生产实测）：opened 只在新插入路径发射 ⇒ 不走插入路径的终态写
// （早失败直写终态等）让会话永远没有 opened ⇒ asm session_projection 无行 ⇒
// 该会话所有 completed 永久 503 ⇒ outbox 背压积压（14,369 会话 / DLQ 34,183）。
//
// 守的判据：
//  1. 终态 + GwSessionID + outboxWriter ⇒ 同事务先 INSERT opened（幂等
//     ON CONFLICT）再 INSERT completed，顺序不可倒（asm 投影按序消费）；
//  2. outboxWriter 为 nil ⇒ 完全不碰 outbox_events（与既有行为一致）。

import (
	"context"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
)

// stubOutboxWriter 只为让 c.outboxWriter != nil（实际写入走 tx 直插，不经它）。
type stubOutboxWriter struct{}

func (stubOutboxWriter) Write(context.Context, outbox.EventEnvelope) error { return nil }

func TestUpdateRequestLog_TerminalPathEmitsIdempotentOpened(t *testing.T) {
	mockDB, recording, recorder := newRecordingDB(t)

	gwSession := "gw_41059725-test"
	status := RequestStatusSuccess

	mockDB.ExpectBegin()
	// 无 token 用量 ⇒ latency-only 的 usage_ledger UPDATE 分支（5 参）。
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-opened-terminal", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// opened 先于 completed：event_type 实参钉死为 session.opened.v1。
	mockDB.ExpectExec(`INSERT INTO outbox_events[\s\S]*ON CONFLICT \(event_id\) DO NOTHING`).
		WithArgs(pgxmock.AnyArg(), "session.opened.v1", pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectQuery(`session_turns_hot`).
		WithArgs(gwSession).
		WillReturnRows(pgxmock.NewRows([]string{"coalesce"}).AddRow(1))
	mockDB.ExpectExec(`INSERT INTO outbox_events`).
		WithArgs(pgxmock.AnyArg(), "request.completed.v1", pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: recording, outboxWriter: stubOutboxWriter{}}
	err := client.updateRequestLog(&RequestLogEntry{
		RequestID:     "req-opened-terminal",
		Op:            RequestLogUpdate,
		Success:       true,
		RequestStatus: &status,
		GwSessionID:   &gwSession,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())

	// 两条 outbox INSERT 的 SQL 文本完全相同（同形状参数化语句），区分只能靠
	// 顺序：pgxmock 的期望按注册顺序匹配（opened 的 event_type 实参已钉死为
	// session.opened.v1 且先注册），ExpectationsWereMet 通过即同时证明了
	// 「两条都发了」与「opened 先于 completed」。
	outboxInserts := 0
	for _, sql := range recorder.queries {
		if strings.Contains(sql, "INSERT INTO outbox_events") {
			outboxInserts++
		}
	}
	require.Equal(t, 2, outboxInserts, "终态路径必须发出 opened + completed 两条 INSERT")
}

func TestUpdateRequestLog_NonTerminalOrNoWriterDoesNotTouchOutbox(t *testing.T) {
	mockDB, recording, recorder := newRecordingDB(t)

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-no-writer", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectCommit()

	status := RequestStatusSuccess
	gwSession := "gw_41059725-test"
	client := &Client{requestLogDB: recording} // outboxWriter nil
	err := client.updateRequestLog(&RequestLogEntry{
		RequestID:     "req-no-writer",
		Op:            RequestLogUpdate,
		Success:       true,
		RequestStatus: &status,
		GwSessionID:   &gwSession,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())

	for _, sql := range recorder.queries {
		require.NotContains(t, sql, "INSERT INTO outbox_events",
			"outboxWriter 为 nil 时不得写 outbox_events")
	}
}
