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
//
// ★ 2026-10-20（迁移 890，v1 族退役）：本文件**只改期望链**，判据本身不变。
//
// 旧期望链（v1 族的语句集合）：
//
//	INSERT: usage_ledger_hot(19) + _request_logs_hot(103) + _request_logs_bodies_hot(4)
//	        + SAVEPOINT + claim UPDATE + RELEASE
//	UPDATE: usage_ledger_hot(5) + _request_logs_hot(100) + _request_logs_bodies_hot(4)
//	        + SAVEPOINT + claim UPDATE + RELEASE
//
// 新期望链（迁移 890 后的语句集合）：只剩 usage_ledger_hot 的一条语句 +
// commit（v1 宽表/正文族写入与终态成功认领整段退役；终态成功的仲裁迁到
// internal/sessionv2mirror 的 markTurnFinalSuccess）。
//
// ⇒ 「回放成功后 hooks 必须被触发」这条不变式**原封不动**地继续被守着。
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
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
		GwSessionID:   &sessionID, // 命中 final-success 资格判定
	}
	payload, err := json.Marshal(entry)
	require.NoError(t, err)

	// insertRequestLog 事务链（迁移 890 后只剩一条 INSERT：APIKeyID nil
	// → 无 api_keys UPDATE；outboxWriter nil → 无 session opener；
	// v1 宽表 / 正文族写入与终态成功认领整段退役）。
	mockDB.ExpectBegin()
	usageArgs := make([]interface{}, 19)
	for i := range usageArgs {
		usageArgs[i] = pgxmock.AnyArg()
	}
	mockDB.ExpectExec(`INSERT INTO usage_ledger_hot`).
		WithArgs(usageArgs...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
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

// TestReplayFallbackFiresOnPersistedHooks_UpdateOp — R45（D15+D16 复核）：
// UPDATE op 是 G2 终态信号的主通道（终态 UPDATE 才携带 final-success
// 资格），此前钉桩只盖 INSERT op。本测试钉死回放 UPDATE 成功后 hooks
// 同样触发；并钉死写库失败（err != nil）时不得补发 hooks（无漏发窗口的
// 对偶面：失败 record 会被 ring buffer requeue，下次成功才补发）。
func TestReplayFallbackFiresOnPersistedHooks_UpdateOp(t *testing.T) {
	status := "success"
	sessionID := "gw_e6_replay_update"
	entry := RequestLogEntry{
		RequestID:     "req-replay-e6-update",
		Op:            RequestLogUpdate,
		Success:       true,
		RequestStatus: &status,
		GwSessionID:   &sessionID,
	}
	payload, err := json.Marshal(entry)
	require.NoError(t, err)

	t.Run("update success fires hooks", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		// updateRequestLog 事务链：usage_ledger（无 token 走 5 参分支）→ commit。
		// v1 宽表 UPDATE / 正文族 upsert / 终态成功认领（SAVEPOINT+claim）
		// 随 890 整段退役。
		mockDB.ExpectBegin()
		mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mockDB.ExpectCommit()

		hookCalled := make(chan *RequestLogEntry, 1)
		client := NewClientWithRequestLogDB(mockDB)
		client.AddOnRequestLogPersisted(func(e *RequestLogEntry) {
			hookCalled <- e
		})

		require.NoError(t, client.ReplayFallback(context.Background(), dbdegradation.BackupRecord{
			Type: "request_log", Payload: payload,
		}))
		require.NoError(t, mockDB.ExpectationsWereMet())
		select {
		case got := <-hookCalled:
			require.Equal(t, "req-replay-e6-update", got.RequestID)
		default:
			t.Fatal("UPDATE op 回放成功后 onPersisted hooks 未触发（G2 终态主通道缺口）")
		}
	})

	t.Run("write failure must not fire hooks", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		mockDB.ExpectBegin()
		mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(fmt.Errorf("simulated degraded write failure"))

		hookCalled := false
		client := NewClientWithRequestLogDB(mockDB)
		client.AddOnRequestLogPersisted(func(e *RequestLogEntry) {
			hookCalled = true
		})

		require.Error(t, client.ReplayFallback(context.Background(), dbdegradation.BackupRecord{
			Type: "request_log", Payload: payload,
		}))
		require.False(t, hookCalled, "写库失败不得补发 hooks（失败 record 由 ring requeue，成功后才补发）")
	})
}
