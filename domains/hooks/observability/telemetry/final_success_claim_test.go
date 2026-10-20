// final_success_claim_test.go — 会话级唯一成功 claim 的单测。
//
// 覆盖测试方案 G10：
//
//	UT-FS-01  final-success claim：SQL 形状（同事务 + NOT EXISTS 双检查 +
//	          savepoint）、并发冲突（23505）降级为普通成功；
//	UT-FS-02  5 次重发回归（单元侧）：只有成功终态 + 非空 gw_session_id 的
//	          entry 触发 claim，失败/取消/空会话路径完全不触碰 claim
//	          （库级唯一性由部分唯一索引保证，集成测试覆盖）。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：v1 族（_request_logs / _request_logs_hot
// / _request_logs_bodies*）连同它的终态成功认领入口 claimSessionFinalSuccess
// 一起落库删除 ⇒ 本文件**整体重写**。
//
// ── 退役登记表（本轮退役的判据 → 退役原因 → 现在由谁承载）────────────────
//
//  1. TestClaimSessionFinalSuccess_GrantPathRunsInSameTxWithSavepoint
//     退役对象：v1 的 claim UPDATE（`UPDATE _request_logs_hot SET
//     is_final_success = TRUE ... NOT EXISTS(other)`）+ SAVEPOINT/RELEASE 序列 +
//     D12 的 session_mirror_outbox 补偿登记。
//     原因：claim 的目标表随 890 删除；且**仲裁职责已迁出本包**——会话族用
//     partial 唯一索引 uq_session_turns_hot_final_success + markTurnFinalSuccess
//     做仲裁（internal/sessionv2mirror/final_success_turn.go）。
//     现在由谁承载：internal/sessionv2mirror 自己的判据
//     （final_success_turn_gate_test.go / final_success_turn_wiring_test.go）；
//     本包侧只负责「**资格**判定」与「把资格挂到 entry 上」⇒ 见本文件下方的
//     TestUpdateRequestLog_TerminalSuccessMarksFinalSuccessClaimed。
//
//  2. TestClaimSessionFinalSuccess_NoGrantNoOutboxRegistration
//     退役对象：claim 未置位（RowsAffected=0）时不登记补偿行。
//     原因：v1 的补偿登记（session_mirror_outbox）随 claim 一起退役。
//     现在由谁承载：**本包侧没有任何判据**守这条（补偿登记已不在本包）；
//     该不变式在会话族侧另有人守。
//
//  3. TestClaimSessionFinalSuccess_UniqueViolationDegradesToNormalSuccess
//     退役对象：唯一索引冲突（23505）降级为普通成功（回滚到 savepoint）。
//     原因：v1 的 claim 语句不存在，冲突发生在会话族
//     （uq_session_turns_hot_final_success），降级逻辑随之迁到
//     internal/sessionv2mirror。
//     现在由谁承载：internal/sessionv2mirror 的判据；本包侧无。
//
//  4. TestClaimSessionFinalSuccess_OtherErrorsDegrade
//     退役对象：滚动发布窗口的 42703（列不存在）降级。
//     原因：同上——v1 族与其列都不存在了，42703 不再是本包的失败模式。
//     现在由谁承载：internal/sessionv2mirror；本包侧无。
//
//  5. TestClaimSessionFinalSuccess_Guards
//     退役对象：nil entry / nil tx / nil Client 不产生任何语句。
//     原因：claim 函数本身退役，没有「不产生语句」的主体了。
//     现在由谁承载：nil 安全的**资格判定**仍在本包 ⇒ 由
//     TestShouldClaimFinalSuccess 的 "nil entry" 用例承载。
//
//  6. TestClaimSessionFinalSuccess_HeapPartitionsGuard
//     退役对象：v1 月度分区名缓存（heapPartitions / heapPartitionsCachedAt）
//     驱动的 promoted guard（UNION ALL 多个 heap 月度分区，排除 columnar 月份）。
//     原因：v1 宽表及其月度分区随 890 删除，promoted/columnar 晋级机制也一并
//     删除（890 同时删掉了 ensure_request_logs_partition / promote_* 系列函数）。
//     现在由谁承载：**本包侧无**（会话族没有 promoted/columnar 分区晋级这条线）。
//
//  7. TestUpdateRequestLog_AppendsFinalSuccessClaimOnTerminalSuccess
//     **改指**（不是退役）：claim 语句那一半退役；「事务内恰好追加一次 claim」
//     变成「事务内只把认领资格挂到 entry.FinalSuccessClaimed」⇒ 见下方同名的
//     改指用例。
//
//  8. TestUpdateRequestLog_FailurePathSkipsClaim
//     **改指**（不是退役）：失败路径不再发 claim 语句，但仍然必须**不**置位
//     entry.FinalSuccessClaimed（否则会话族会把一次失败当成终态成功去仲裁）。
//
// DB mock 用 pgxmock（现有 telemetry 测试基建，见 request_logger_test.go）。
package telemetry

import (
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// anyArgs 生成 n 个 AnyArg 匹配器（大参数量语句用）。
func anyArgs(n int) []interface{} {
	args := make([]interface{}, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

// ── 退役登记：让「退役」这件事在测试清单里可见，而不是变成一段无人引用的注释 ──
func TestClaimSessionFinalSuccess_RetiredWithV1Family(t *testing.T) {
	// 本轮退役的判据清单见文件头的「退役登记表」。此处保留一个显式的登记用例，
	// 使「这些判据已经不在了」这件事在 go test 的输出里可见——
	// 免得下一个会话以为它们还在、或者以为漏跑了什么。
	t.Log("claimSessionFinalSuccess 与 v1 月度分区缓存（heapPartitions）" +
		"随迁移 890 整条删除；终态成功的仲裁迁到 internal/sessionv2mirror " +
		"（uq_session_turns_hot_final_success + markTurnFinalSuccess），" +
		"本包只保留『资格判定 + 把资格挂到 entry.FinalSuccessClaimed』。")
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

// R34 (2026-09-17)：网关内部回环（auto-title gt_/auto-summary gs_ 分支会话）
// 不得 claim is_final_success —— sessionv2mirror 经 IsInternalAutoEntry 把
// 它们排除在 session_turns 之外，claim 了就会留下「is_final_success=TRUE
// 但无 turns」的行，GLOBAL_G2 对账恒 >0（F1 修复让成功终态带上了
// IsAutoRequest 之后暴露）。业务 auto 轮带 TaskType，仍正常 claim。
func TestShouldClaimFinalSuccess_ExcludesInternalLoopbacks(t *testing.T) {
	cases := []struct {
		name  string
		entry *RequestLogEntry
		want  bool
	}{
		{"auto-title loopback (origin_actor list) never claims", &RequestLogEntry{
			RequestID: "gt-1", Success: true, GwSessionID: strptr("gt_gw_s1"),
			IsAutoRequest: boolptr(true), OriginActor: strptr("auto-title-generator"),
		}, false},
		{"auto-summary loopback (request_type) never claims", &RequestLogEntry{
			RequestID: "gs-1", Success: true, GwSessionID: strptr("gs_gw_s1"),
			IsAutoRequest: boolptr(true), RequestType: strptr("summary"),
		}, false},
		{"taskless auto entry (decider-nil fallback) never claims", &RequestLogEntry{
			RequestID: "ga-1", Success: true, GwSessionID: strptr("gw_s1"),
			IsAutoRequest: boolptr(true),
		}, false},
		{"business auto turn with TaskType still claims", &RequestLogEntry{
			RequestID: "ba-1", Success: true, GwSessionID: strptr("gw_s1"),
			IsAutoRequest: boolptr(true), TaskType: strptr("coding"),
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldClaimFinalSuccess(tc.entry))
			// 同一判定必须与镜像排除一致（构造保证）：claim 的行不该被镜像排除。
			if tc.want {
				require.False(t, IsInternalAutoEntry(tc.entry), "claiming entry must not be mirror-excluded")
			}
		})
	}
}

// UT-FS-02（写路径回归，★ 改指）：updateRequestLog 在成功终态事务内**不再**
// 发任何 v1 claim 语句（claimSessionFinalSuccess 随 890 整条删除），只把
// **认领资格**挂到 entry.FinalSuccessClaimed 上——真正的仲裁由
// internal/sessionv2mirror 的 markTurnFinalSuccess 承担
// （uq_session_turns_hot_final_success 部分唯一索引）。
//
// 改指后钉住的新契约：
//
//	① 成功终态 + 非空 gw_session_id ⇒ entry.FinalSuccessClaimed == true。
//	   这是**跨包契约**：internal/sessionv2mirror/hook.go 与 replay.go 都读这个
//	   标记才去仲裁 ⇒ 标记漏置位 = 会话永远不会有终态成功。
//	② 事务内**不出现**任何 is_final_success 的 UPDATE（仲裁不在本包）。
//
// 正向对照（方法纪律第 2 条）：录制器必须抓到那条 usage_ledger_hot UPDATE
// ——否则「没抓到语句」与「语句里没有 is_final_success」长得一模一样。
func TestUpdateRequestLog_TerminalSuccessMarksFinalSuccessClaimed(t *testing.T) {
	mock, recording, recorder := newRecordingDB(t)

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
	// usage_ledger_hot（无 token 字段的分支：5 个绑定参数）
	mock.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-054-3", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	c := &Client{requestLogDB: recording}
	require.NoError(t, c.updateRequestLog(entry))
	require.NoError(t, mock.ExpectationsWereMet())

	// ① 认领资格被挂上（internal/sessionv2mirror 的输入）。
	require.True(t, entry.FinalSuccessClaimed,
		"成功终态且带会话 ⇒ 必须标记认领资格，否则会话族永远不会仲裁出终态成功")

	// ② 仲裁不在本包：事务里不得出现 is_final_success 的 UPDATE。
	statements := recorder.recorded()
	require.Len(t, statements, 1,
		"updateRequestLog 应当只发一条 UPDATE（正向对照：录制器抓到了语句）")
	require.Contains(t, statements[0], "UPDATE usage_ledger_hot")
	require.NotContains(t, statements[0], "is_final_success",
		"v1 的终态成功认领随 890 退役，本包不得再发 is_final_success 的 UPDATE")
}

// 失败路径不认领：除了主语句外不应出现任何 claim 相关的语句，
// 也不得把 entry.FinalSuccessClaimed 置位（否则 sessionv2mirror 会把
// 一次失败当成会话的终态成功去仲裁）。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：本用例由
// TestUpdateRequestLog_FailurePathSkipsClaim **改指**——「跳过 claim 语句」
// 那一半随 v1 退役，但「失败不得置位认领资格」这半个跨包契约还在。
func TestUpdateRequestLog_FailurePathSkipsFinalSuccessClaim(t *testing.T) {
	mock, recording, recorder := newRecordingDB(t)

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
		WithArgs("req-fail-1", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	c := &Client{requestLogDB: recording}
	require.NoError(t, c.updateRequestLog(entry))
	require.NoError(t, mock.ExpectationsWereMet())

	require.False(t, entry.FinalSuccessClaimed,
		"失败路径不得标记认领资格（否则会话族会把失败当成终态成功仲裁）")

	statements := recorder.recorded()
	require.Len(t, statements, 1,
		"updateRequestLog 应当只发一条 UPDATE（正向对照：录制器抓到了语句）")
	require.NotContains(t, statements[0], "is_final_success")
}

// 资格判定与写路径的一致性：写路径置位认领资格的条件，必须与
// shouldClaimFinalSuccess 的判定**完全一致**——两者不同步就会出现
// 「判定说该认领、写路径却没标记」或反之的空档。
//
// 本用例是上面两条写路径判据的**同测正向对照**：同一个 entry 家族，
// 走写路径看标记、走判定函数看布尔值，两边必须同号。
func TestUpdateRequestLog_ClaimFlagAgreesWithEligibilityPredicate(t *testing.T) {
	cases := []struct {
		name    string
		entry   *RequestLogEntry
		wantSQL bool
	}{
		{
			name: "success with session => eligible and flagged",
			entry: &RequestLogEntry{
				RequestID: "req-agree-1", Success: true,
				RequestStatus: strptr(RequestStatusSuccess), GwSessionID: strptr("sess-a"),
			},
			wantSQL: true,
		},
		{
			name: "failure with session => not eligible and not flagged",
			entry: &RequestLogEntry{
				RequestID: "req-agree-2", Success: false,
				RequestStatus: strptr(RequestStatusFailure), GwSessionID: strptr("sess-b"),
			},
			wantSQL: false,
		},
		{
			name: "success without session => not eligible and not flagged",
			entry: &RequestLogEntry{
				RequestID: "req-agree-3", Success: true,
				RequestStatus: strptr(RequestStatusSuccess),
			},
			wantSQL: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE usage_ledger_hot`).
				WithArgs(anyArgs(5)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectCommit()

			c := &Client{requestLogDB: mock}
			require.NoError(t, c.updateRequestLog(tc.entry))
			require.NoError(t, mock.ExpectationsWereMet())

			require.Equal(t, tc.wantSQL, tc.entry.FinalSuccessClaimed,
				"写路径置位认领资格的条件必须与 shouldClaimFinalSuccess 一致")
			require.Equal(t, tc.wantSQL, shouldClaimFinalSuccess(tc.entry),
				"资格判定与写路径标记必须同号")
		})
	}
}
