// system_fingerprint_persist_test.go — 2026-09-12 指纹写路径贯通回归。
//
// 背景：X-System-Fingerprint 响应头此前只进 integrity detector 的 context
// JSONB，_request_logs_hot.system_fingerprint 专用列（603）全表零行，7 天
// 漂移检测器空转。本次接线：handler emitTelemetry 盖章 entry.SystemFingerprint
// → persistSystemFingerprint 落列 → 迁移 697 起随 promote 进分区。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：本文件**改指**。
//
// 被改指的对象：「指纹值被落进 v1 宽表的专用列」这条**落库级**契约。
// persistSystemFingerprint（写 _request_logs_hot.system_fingerprint）随 v1
// 族整条删除 ⇒ 断言对象已不存在（写它必然 42P01）。
//
// 改指后守的不变式（**它才是指纹漂移检测器真正的命脉**）：**进程内 arm**。
//
//	bg/integrity_fingerprint_drift.go 的两条腿：
//	  腿 1（探针）：读 v1 宽表的 system_fingerprint 列 ⇒ v1 族退役后恒空
//	  腿 2（进程内 arm）：telemetry.SystemFingerprintObservedSince()
//
// 腿 2 是**唯一还活着**的腿（而且 §9.51 早就把它与持久化解耦了：arm 记录的是
// 「本进程见过带指纹的请求」，与该请求是否落库无关）。arm 的唯一写入方是
// markSystemFingerprintObserved()，它的唯一调用点是 observeSystemFingerprint()
// ——本门钉的就是这条链在**运行时**仍然通。
//
// 跨包契约：指纹**值**本身由会话族承接（internal/sessionv2mirror 把
// entry.SystemFingerprint 搬进会话族的写入载荷）；本包侧只负责 arm。
package telemetry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fingerprintArmStamp 读出进程内 arm 的当前时间戳（没 arm 过时返回零值）。
//
// 直接读包级变量而不是走 SystemFingerprintObservedSince()：后者返回的是
// 「距今多久」，只能证明「曾经 arm 过」，**证不了**「这一次调用 arm 了」——
// 而本门要证的正是后者（有一次调用 arm 了 / 没 arm）。
func fingerprintArmStamp() time.Time {
	stamp, _ := systemFingerprintLastObserved.Load().(time.Time)
	return stamp
}

// TestUpdateRequestLog_ArmsSystemFingerprintObservation（★ 改指，原名
// TestUpdateRequestLog_PersistsSystemFingerprint）：
// 带 SystemFingerprint 的终态更新**必须**刷新进程内 arm。
//
// 正向对照（方法纪律第 2 条）：同测里再跑一次「不带指纹」的更新，断言 arm
// **不动** ⇒ 「arm 没动」与「arm 压根没被读过」不会长得一样。
func TestUpdateRequestLog_ArmsSystemFingerprintObservation(t *testing.T) {
	fp := "fp-abc123"

	// ── 正向对照：不带指纹的更新不得刷新 arm ──
	beforeNoFingerprint := fingerprintArmStamp()
	recordUpdateStatements(t, &RequestLogEntry{
		RequestID:         "req-fp-none",
		Op:                RequestLogUpdate,
		TenantID:          "default",
		Success:           true,
		RequestStatus:     strptr(RequestStatusSuccess),
		GwSessionID:       strptr("sess-fp-none"),
		SystemFingerprint: nil,
	})
	require.True(t, fingerprintArmStamp().Equal(beforeNoFingerprint),
		"不带指纹的更新不得刷新进程内 arm（否则 arm 记录的是噪声而不是观测）")

	// ── 被测：带指纹的更新必须刷新 arm ──
	before := fingerprintArmStamp()
	recordUpdateStatements(t, &RequestLogEntry{
		RequestID:         "req-fp-1",
		Op:                RequestLogUpdate,
		TenantID:          "default",
		Success:           true,
		RequestStatus:     strptr(RequestStatusSuccess),
		GwSessionID:       strptr("sess-fp-1"),
		SystemFingerprint: &fp,
	})
	after := fingerprintArmStamp()
	require.True(t, after.After(before),
		"带指纹的更新必须刷新进程内 arm —— 这是指纹漂移检测器在 v1 族退役后"+
			"唯一还活着的腿（SystemFingerprintObservedSince 读的就是它）")
}

// TestUpdateRequestLog_NoFingerprintSkipsArm（★ 改指，原名
// TestUpdateRequestLog_NoFingerprintSkipsPersist）：
// 上游未返回 X-System-Fingerprint 时，arm 必须**不**被刷新。
//
// 旧门守的是「无指纹 ⇒ 语句集合与历史完全一致」（persist 是 no-op，不得给
// 既有语句序列加行）。新写入路径下这个不变式**仍然成立且仍值得守**：指纹
// 落列语句已随 v1 退役 ⇒ 「无指纹时不得多出任何语句」这句话的对象换成了
// arm 的刷新时机。
func TestUpdateRequestLog_NoFingerprintSkipsArm(t *testing.T) {
	before := fingerprintArmStamp()

	statements := recordUpdateStatements(t, &RequestLogEntry{
		RequestID:     "req-fp-2",
		Op:            RequestLogUpdate,
		TenantID:      "default",
		Success:       true,
		RequestStatus: strptr(RequestStatusSuccess),
		GwSessionID:   strptr("sess-fp-2"),
	})

	require.Len(t, statements, 1, "updateRequestLog 应当只发一条 UPDATE（正向对照：录制器抓到了语句）")
	require.Contains(t, statements[0], "UPDATE usage_ledger_hot")
	require.True(t, fingerprintArmStamp().Equal(before),
		"无指纹的更新不得刷新进程内 arm")

	// 正向对照：同测内再跑一次带指纹的更新 ⇒ arm 必须被刷新。
	// 没有这一段，上面那句「arm 没动」就可能只是「arm 根本没被读过」。
	fp := "fp-control"
	recordUpdateStatements(t, &RequestLogEntry{
		RequestID:         "req-fp-control",
		Op:                RequestLogUpdate,
		TenantID:          "default",
		Success:           true,
		RequestStatus:     strptr(RequestStatusSuccess),
		GwSessionID:       strptr("sess-fp-control"),
		SystemFingerprint: &fp,
	})
	require.True(t, fingerprintArmStamp().After(before),
		"正向对照失效：带指纹的更新必须刷新 arm（否则上面的否定断言是假绿）")
}
