package telemetry

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// withStopWriteGateSettings 把 settings.Global 换成携带真实 storage specs 的
// registry，并把 storage.request_logs_write_enabled 钉在给定值上（fake 平台
// 后端），恢复交由 t.Cleanup。复用 body_summary_test.go 的 fakeSettingsBackend
// 与 strconvFormatBool。
func withStopWriteGateSettings(t *testing.T, writeEnabled bool) {
	t.Helper()
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{
		"storage.request_logs_write_enabled": []byte(strconvFormatBool(writeEnabled)),
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	for _, spec := range settings.StorageSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

// TestRequestLogsWriteEnabled_DefaultsFalse：settings.Global 为 nil（单测未
// 初始化 settings）时门控必须回退为 false（不双写），与 Lite sink 同语义。
//
// 2026-10-20（审计 D0）：本断言的**期望值**由 true 改为 false —— 这是契约变更，
// 不是断言变弱。理由：代码默认必须是「不双写」，否则未显式配置的环境
// （新装/克隆/灾备）会继续写已退役的宽族，使「双写已下线」只是某台机器的
// 配置事实而不是代码事实。旧值 true 曾经承担的那层担心（false 会被 dual-read
// 门读成「v1 已冻结」而作废 S4 判据）没有消失：cmd/gateway 的 S4 门仍然以
// v1_writes_disabled 给出 void 理由，只是现在那是**有意为之**的状态。
func TestRequestLogsWriteEnabled_DefaultsFalse(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	settings.Global = nil
	require.False(t, requestLogsWriteEnabled())

	registry := settings.NewRegistry()
	for _, spec := range settings.StorageSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry // 已注册 spec 但无后端值 → 落到声明默认
	require.False(t, requestLogsWriteEnabled())
}

// ★ 2026-10-20（迁移 890，v1 族退役）：本文件的两条门控用例**方向反转**。
//
// 旧门（TestInsertRequestLogStopWriteGate / TestUpdateRequestLogStopWriteGate）
// 守的是：「S4 停写键关掉后，_request_logs_hot 主行与 _request_logs_bodies_hot
// 正文写入被整体跳过，usage_ledger 计费行照常提交」。
//
// 反转的原因：v1 宽表与正文族随 890 整族删除 ⇒ 停写键在本包里**不再门控任何
// PG 写入**。现在键只门控**文件系统正文镜像**（persistRequestLog 里的
// mirrorRequestBodies，见 TestPersistRequestLogMirrorFollowsStopWriteGate）。
//
// 反转后的不变式（更重要、也更有牙）：
//
//	★ S4 停写键**不得**门控计费写入（usage_ledger_hot）。
//	  键的语义是「停掉宽表」，不是「停掉计费」；把两者绑在一起 ⇒ 一次运维
//	  停写会连带把计费行也停掉，而 v1 族退役后**没有任何东西**能补它。
//
// 判据强度：pgxmock 严格模式——「门关时少写的那条语句」不存在 ⇒ 只要生产
// 代码还把计费写在门里，本用例的 gate-off 分支立刻红（期望的 INSERT/UPDATE
// 不会发生，mock 先报 Commit unexpected）。
//
// 正向对照：两个分支都**必须**真的写出计费行，且两条分支发出的语句集合
// **逐条相同**（门开关不得改变 PG 语句集合）。

// TestInsertRequestLogStopWriteGate（★ 方向反转）：INSERT 路径的计费写入
// **恒写**，与 S4 停写键无关。
func TestInsertRequestLogStopWriteGate(t *testing.T) {
	recorded := map[bool][]string{}
	for _, writeOn := range []bool{true, false} {
		t.Run(stopWriteCaseName(writeOn), func(t *testing.T) {
			withStopWriteGateSettings(t, writeOn)

			mock, recording, recorder := newRecordingDB(t)
			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO usage_ledger_hot").
				WithArgs(anyArgs(19)...).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectCommit()

			c := &Client{requestLogDB: recording}
			c.AddOnRequestLogPersisted(func(entry *RequestLogEntry) {
				raw, err := json.Marshal(entry)
				require.NoError(t, err)
				var persisted map[string]any
				require.NoError(t, json.Unmarshal(raw, &persisted))
				require.Equal(t, true, persisted["session_v2_write_required"] == true,
					"committed request must carry the selected canonical writer")
			})
			err := c.persistRequestLog(&RequestLogEntry{
				Op:        RequestLogInsert,
				RequestID: "req-stop-write-insert",
				TenantID:  "tenant-1",
				Success:   true,
			})
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())

			statements := recorder.recorded()
			require.Len(t, statements, 1,
				"insertRequestLog 应当只发一条 INSERT（正向对照：录制器抓到了语句）")
			require.Contains(t, statements[0], "INSERT INTO usage_ledger_hot")
			recorded[writeOn] = statements
		})
	}
	require.Equal(t, recorded[true], recorded[false],
		"S4 停写键不得改变 INSERT 路径的 PG 语句集合（计费写入恒写）")
}

// TestUpdateRequestLogStopWriteGate（★ 方向反转）：UPDATE 路径的计费刷新
// **恒写**，与 S4 停写键无关。
func TestUpdateRequestLogStopWriteGate(t *testing.T) {
	recorded := map[bool][]string{}
	for _, writeOn := range []bool{true, false} {
		t.Run(stopWriteCaseName(writeOn), func(t *testing.T) {
			withStopWriteGateSettings(t, writeOn)

			mock, recording, recorder := newRecordingDB(t)
			mock.ExpectBegin()
			mock.ExpectExec("UPDATE usage_ledger_hot").
				WithArgs(anyArgs(5)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectCommit()

			c := &Client{requestLogDB: recording}
			c.AddOnRequestLogPersisted(func(entry *RequestLogEntry) {
				raw, err := json.Marshal(entry)
				require.NoError(t, err)
				var persisted map[string]any
				require.NoError(t, json.Unmarshal(raw, &persisted))
				require.Equal(t, true, persisted["session_v2_write_required"] == true,
					"committed request must carry the selected canonical writer")
			})
			err := c.persistRequestLog(&RequestLogEntry{
				Op:        RequestLogUpdate,
				RequestID: "req-stop-write-update",
				TenantID:  "tenant-1",
				Success:   true,
			})
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())

			statements := recorder.recorded()
			require.Len(t, statements, 1,
				"updateRequestLog 应当只发一条 UPDATE（正向对照：录制器抓到了语句）")
			require.Contains(t, statements[0], "UPDATE usage_ledger_hot")
			recorded[writeOn] = statements
		})
	}
	require.Equal(t, recorded[true], recorded[false],
		"S4 停写键不得改变 UPDATE 路径的 PG 语句集合（计费刷新恒写）")
}

// stopWriteCaseName 给停写门的两个分支起名（用例名要能自解释）。
func stopWriteCaseName(writeOn bool) string {
	if writeOn {
		return "gate on still writes the canonical ledger"
	}
	return "gate off still writes the canonical ledger"
}

// A terminal UPDATE may find no row and fall back to INSERT after a
// live configuration switch. The committed copy must pass its selected V2
// source back to the original entry consumed by persisted hooks.
type reviewSwitchBackend struct {
	fakeSettingsBackend
	reads int
}

func (b *reviewSwitchBackend) Get(scope settings.Scope, key string) ([]byte, error) {
	if key == settings.KeyRequestLogsWriteEnabled {
		b.reads++
		if b.reads <= 2 {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	}
	return b.fakeSettingsBackend.Get(scope, key)
}

// TestReviewV2LatchSurvivesUpdatePath（★ 改指，原名
// TestReviewV2LatchSurvivesUpdateFallback）：
//
// 被改指的对象：「终态 UPDATE 命中 0 行 ⇒ 回落到 INSERT」这条**回落分支**。
// 它随 v1 族整段删除（新写入路径的 UPDATE 只改已有行，行不存在时不回落），
// 连同它依赖的「运行时切开关导致前后两次读键不一致」的时序一起消失。
//
// 留下来的是它真正想守的那条不变式：**V2 事实源的选择必须传回给持久化
// hook 消费的那个 entry**。旧用例的断言原文是
// 「committed fallback skipped V1, original entry must preserve selected V2
// source」——前半句（fallback / V1）没了，后半句（original entry must
// preserve selected V2 source）还在，且**更重要**：写路径无条件把
// entry.SessionV2WriteRequired 置真，会话族（sessionv2mirror）据此决定
// 要不要落这一行；这个标记漏了 ⇒ 事实行缺失。
//
// 另加一条**新**的钉子：命中 0 行**不得**回落 INSERT（回落分支的复活会
// 让「一条 UPDATE」变成「一条 UPDATE + 一条 INSERT」，且 INSERT 与 UPDATE
// 落在不同事务里 ⇒ 计费行会重复）。
func TestReviewV2LatchSurvivesUpdatePath(t *testing.T) {
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })
	b := &reviewSwitchBackend{fakeSettingsBackend: fakeSettingsBackend{store: map[string][]byte{"sessions_v2.enabled": []byte("true"), "sessions_v2.shadow_write": []byte("true")}}}
	reg := settings.NewRegistry()
	reg.RegisterBackend(settings.ScopePlatform, b)
	for _, sp := range append(settings.StorageSpecs(), settings.SessionsV2Specs()...) {
		reg.MustRegisterSpec(sp)
	}
	settings.Global = reg

	mock, recording, recorder := newRecordingDB(t)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE usage_ledger_hot").WithArgs(anyArgs(5)...).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	c := &Client{requestLogDB: recording}
	observed := false
	c.AddOnRequestLogPersisted(func(e *RequestLogEntry) {
		observed = true
		require.True(t, e.SessionV2WriteRequired,
			"committed row must carry the selected V2 source back to the persisted hook")
	})
	e := &RequestLogEntry{Op: RequestLogUpdate, RequestID: "review-fallback", TenantID: "tenant-1", Success: true}
	require.NoError(t, c.persistRequestLog(e))
	require.NoError(t, mock.ExpectationsWereMet())
	require.True(t, observed)
	require.True(t, e.SessionV2WriteRequired,
		"original entry must preserve the selected V2 source")

	// 命中 0 行不得回落 INSERT（回落分支随 v1 族退役）。
	statements := recorder.recorded()
	require.Len(t, statements, 1, "updateRequestLog 应当只发一条 UPDATE（正向对照：录制器抓到了语句）")
	require.Contains(t, statements[0], "UPDATE usage_ledger_hot")
	for _, statement := range statements {
		require.NotContains(t, statement, "INSERT INTO usage_ledger_hot",
			"命中 0 行不得回落 INSERT（回落分支随迁移 890 退役）")
	}
}
