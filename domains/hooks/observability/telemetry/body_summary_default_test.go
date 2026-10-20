package telemetry

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ★ 2026-10-20（迁移 890，v1 族退役）：本文件整体**改指**。
//
// 被改指的对象：「sessions_v2 打开/关闭决定正文写 full 还是摘要」这条
// **写路径级**契约。它成立的前提是本包有一个正文写入点（v1 正文族）——
// 该写入点随 890 整条删除 ⇒ 断言对象已不存在。
//
// 改指后本文件守的不变式（仍成立、仍有牙）：**sessions_v2 的开关不得改变
// 本包的写路径**。具体说：
//
//	① sessions_v2.enabled=true 与 false 产出的语句集合必须完全一致；
//	② 两者都不得发出正文语句（正文落点归会话族）。
//
// 这条不变式不是空话：它是「v1 正文写没有被接回来」的运行时证据——
// 有人若把摘要/全文的正文写重新挂回本包，开关组合一变语句集合就变 ⇒ 变红。
//
// 「开关组合 → 摘要标志」的解析逻辑仍由 body_summary_test.go 的
// TestRequestBodiesSummaryEnabled_FlagCombination /
// _ApplicationCanary 守着（那两条判据仍绿、仍有效）。

// recordUpdateStatementsWithSessions 在给定 sessions_v2 开关组合下跑一次
// updateRequestLog，返回实际发出的 SQL 原文。
func recordUpdateStatementsWithSessions(t *testing.T, overrides map[string]bool) []string {
	t.Helper()
	withSessionsV2Settings(t, overrides)
	return recordUpdateStatements(t, &RequestLogEntry{
		RequestID:     "req-sessions-v2",
		Op:            RequestLogUpdate,
		Success:       true,
		RequestStatus: strptr(RequestStatusSuccess),
	})
}

// TestUpdateRequestLog_SessionsV2DoesNotChangeWritePath（★ 改指，原名
// TestUpdateRequestLog_SessionsV2DefaultsToFullBodies）：sessions_v2 打开时，
// 写路径的语句集合与关闭时**逐条相同**，且不含正文语句。
func TestUpdateRequestLog_SessionsV2DoesNotChangeWritePath(t *testing.T) {
	baseline := recordUpdateStatementsWithSessions(t, map[string]bool{"sessions_v2.enabled": false})
	enabled := recordUpdateStatementsWithSessions(t, map[string]bool{"sessions_v2.enabled": true})

	require.NotEmpty(t, baseline, "正向对照：录制器必须抓到语句（否则下面的相等断言是假绿）")
	require.Equal(t, baseline, enabled,
		"sessions_v2 打开不得改变本包的写路径（正文落点已迁到会话族）")
	requireNoBodyStatement(t, enabled)
}

// TestUpdateRequestLog_LegacyFalseOverrideDoesNotChangeWritePath（★ 改指，
// 原名 TestUpdateRequestLog_LegacyFalseOverrideStillKeepsFullBodies）：
// 历史遗留的 sessions_v2.request_bodies_full=false 显式覆盖，同样不得改变
// 写路径——旧门守的是「显式 false 仍写 full 正文」，那个落点已不存在。
func TestUpdateRequestLog_LegacyFalseOverrideDoesNotChangeWritePath(t *testing.T) {
	baseline := recordUpdateStatementsWithSessions(t, map[string]bool{"sessions_v2.enabled": false})
	legacy := recordUpdateStatementsWithSessions(t, map[string]bool{
		"sessions_v2.enabled":             true,
		"sessions_v2.request_bodies_full": false,
	})

	require.NotEmpty(t, baseline, "正向对照：录制器必须抓到语句（否则下面的相等断言是假绿）")
	require.Equal(t, baseline, legacy,
		"历史的 request_bodies_full=false 覆盖不得改变本包的写路径")
	requireNoBodyStatement(t, legacy)
}
