// replay_fallback_hooks_test.go — R44（storage ledger E6）回归钉桩。
//
// ReplayFallback（DB 降级恢复后的回放路径）此前直调
// insertRequestLog/updateRequestLog，绕过 persistRequestLog 的 onPersisted
// hooks：同事务的 final-success claim 置位 is_final_success 后，
// sessionv2mirror hook 收不到终态信号，镜像行结构性缺失且重放器无法归零
// （storage-observation-ledger 09-15 f403405b / 09-18 44728e27 各 1 行实锤）。
// 本测试钉死：回放写库成功后 hooks 必须被触发；进 fallback 的 entry 此前
// 从未成功落库，hooks 从未对它触发，补发即 exactly-once。
//
// DB mock 用 pgxmock（现有 telemetry 测试基建，见 final_success_claim_test.go /
// body_summary_test.go 的同构期望链）。
package telemetry

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
)

func TestReplayFallbackFiresOnPersistedHooks(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	status := "success"
	sessionID := "gw_e6_replay_session"
	entry := RequestLogEntry{
		RequestID:     "req-replay-e6",
		Op:            RequestLogInsert,
		Success:       true,
		RequestStatus: &status,
		GwSessionID:   &sessionID, // 命中 final-success claim 分支
	}
	payload, err := json.Marshal(entry)
	require.NoError(t, err)

	// insertRequestLog 事务链（无 bodies → 无 bodies INSERT；APIKeyID nil
	// → 无 api_keys UPDATE；outboxWriter nil → 无 session opener）。
	mockDB.ExpectBegin()
	usageArgs := make([]interface{}, 18)
	for i := range usageArgs {
		usageArgs[i] = pgxmock.AnyArg()
	}
	mockDB.ExpectExec(`INSERT INTO usage_ledger_hot`).
		WithArgs(usageArgs...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	requestArgs := make([]interface{}, 102) // 608: +request_class/due_at ($101/$102)
	for i := range requestArgs {
		requestArgs[i] = pgxmock.AnyArg()
	}
	mockDB.ExpectExec(`INSERT INTO\s+request_logs_hot\s*\(`).
		WithArgs(requestArgs...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// bodies 行无条件写入（全 nil 时 NULLIF 置空，仍占 4 参）。
	mockDB.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	// final-success claim（同事务 SAVEPOINT/UPDATE/RELEASE，
	// 模式见 final_success_claim_test.go）。
	mockDB.ExpectExec(`SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
	mockDB.ExpectExec(`UPDATE request_logs_hot\s+SET is_final_success = TRUE`).
		WithArgs("req-replay-e6").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`RELEASE SAVEPOINT gw_final_success_claim`).
		WillReturnResult(pgxmock.NewResult("RELEASE", 0))
	mockDB.ExpectCommit()

	hookCalled := make(chan *RequestLogEntry, 1)
	client := NewClientWithRequestLogDB(mockDB)
	client.AddOnRequestLogPersisted(func(e *RequestLogEntry) {
		hookCalled <- e
	})

	err = client.ReplayFallback(context.Background(), dbdegradation.BackupRecord{
		Type:    "request_log",
		Payload: payload,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())

	select {
	case got := <-hookCalled:
		require.Equal(t, "req-replay-e6", got.RequestID)
		require.NotNil(t, got.GwSessionID)
	default:
		t.Fatal("ReplayFallback 成功后 onPersisted hooks 未触发（E6 回归：镜像 hook 收不到终态信号）")
	}
}
