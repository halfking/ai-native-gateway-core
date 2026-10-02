package main

// dual_read_gate_pg_test.go — 2026-10-02：S4 前置门「停写后真空为绿」的真库门。
//
// 这道门与 dual_read_validator_pg_test.go 的分工：那边证明**当前态**下 Summarize
// 的跨查询不变式成立；这边证明**把 v1 写入关掉之后，端点的答案会翻转**。
//
// 为什么必须打真库：缺陷的完整形态是「真实 SQL 返回 0 行 + 真实门控读数为
// false ⇒ 端点说 ready」。纯函数门证明不了 SQL 在零行窗口下真的返回零行，
// 真库门也证明不了「门控读数会变」——两者缺一，缺陷就能从缺口子钻过去。
// 所以这里把两件事接在一起跑，并且跑的是 Summarize 本体，不是复刻的 SQL。
//
// 覆盖两种停写形态，因为它们的失败方式不同：
//   - 窗口内零 V1 行：真实发生于「关停已超过一个窗口」之后；
//   - 窗口内仍有 V1 行：真实发生于关停当天的过渡期。
//     只测前者会漏掉后者——过渡期里 GenuineLossRows 未必为 0，但只要 v1 已冻结，
//     结论就不再可信（而这正是本缺陷的形状：恒真，不是偶发）。

import (
	"context"
	"testing"
	"time"
)

// withV1WritesDisabled 让 currentV1WritesEnabled 返回 false，跑完后恢复。
//
// 换的是**门控读数**而不是数据库状态：真的去停写会污染共享库，而缺陷的成因
// 恰恰是「门控为 false 时端点仍报绿」，所以换读数就是换到缺陷所在的分支。
func withV1WritesDisabled(t *testing.T) {
	t.Helper()
	saved := currentV1WritesEnabled
	currentV1WritesEnabled = func() bool { return false }
	t.Cleanup(func() { currentV1WritesEnabled = saved })
}

// TestS4GateStopsClaimingReadyAfterStopWrite 是本轮缺陷的直接回归门。
func TestS4GateStopsClaimingReadyAfterStopWrite(t *testing.T) {
	dsn := resolveDualReadDSN()
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set — offline mode")
	}
	// 必须走 openDualReadPool：RLS 的 FORCE 侧在受限 DSN 用户上会静默返回
	// 0 行，而「全零」恰好能通过本门的大部分断言 —— 那正是这道门自己文件头
	// 里写的「最危险的失败形态」。本机 superuser 看不出差别，CI 看得出。
	pool := openDualReadPool(t, dsn)
	probeDualReadTables(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	v := NewDualReadValidator(pool)

	// 基线：写入中。先确认它此刻**会**给出一个基于真实证据的答案，否则后面的
	// 「翻转」就没有对照物（若基线本来就是 void，后面无从证明什么）。
	on, err := v.Summarize(ctx, "", 24)
	if err != nil {
		t.Fatalf("baseline Summarize: %v", err)
	}
	t.Logf("基线(写入中): v1=%d noTurns=%d genuine=%d ready=%v void=%v reason=%q",
		on.V1Rows, on.V1RowsWithoutTurns, on.GenuineLossRows,
		on.S4Ready, on.S4GateVoid, on.S4GateVoidReason)
	if !on.V1WritesEnabled {
		t.Fatalf("基线就报 v1_writes_enabled=false：本机 settings 已把 S4 键设为 false，" +
			"下面的对照失去意义（请复位该键或改用别的库）")
	}
	if on.V1Rows == 0 {
		t.Skipf("24h 窗口内零 V1 行（v1 可能是测试库/静默期），无法构造对照")
	}

	// 关停态：同一个库、同一段 SQL、同一窗口，只把门控读数换成 false。
	withV1WritesDisabled(t)
	off, err := v.Summarize(ctx, "", 24)
	if err != nil {
		t.Fatalf("post-stop Summarize: %v", err)
	}
	t.Logf("关停后: v1=%d noTurns=%d genuine=%d ready=%v void=%v reason=%q",
		off.V1Rows, off.V1RowsWithoutTurns, off.GenuineLossRows,
		off.S4Ready, off.S4GateVoid, off.S4GateVoidReason)

	// 核心断言。
	if off.S4Ready {
		t.Fatalf("停写后 Summarize 仍报 s4_ready=true（v1=%d genuine=%d）——"+
			"这正是 2026-10-02 修掉的真空为绿：停写让「无行可缺」恒成立",
			off.V1Rows, off.GenuineLossRows)
	}
	if !off.S4GateVoid {
		t.Error("停写后必须标记 s4_gate_void=true：v1 已冻结，本次运行没有验证任何漂移")
	}
	if off.S4GateVoidReason != s4GateReasonV1WritesDisabled {
		t.Errorf("void reason = %q, want %q", off.S4GateVoidReason, s4GateReasonV1WritesDisabled)
	}
	if off.V1WritesEnabled {
		t.Error("v1_writes_enabled 必须如实反映门控读数（不得为让 S4Ready 成立而谎报 true）")
	}

	// 漂移数字必须**不因门控而改变**：门控只影响「能不能这么声称」，不影响
	// 「实际扫到了什么」。若这里也变了，说明有人为了让门翻转去动了 SQL。
	if off.V1Rows != on.V1Rows || off.V1RowsWithoutTurns != on.V1RowsWithoutTurns ||
		off.GenuineLossRows != on.GenuineLossRows {
		t.Errorf("门控改变了观测数字：v1 %d→%d, noTurns %d→%d, genuine %d→%d ——"+
			"门控只该影响判定，不该影响度量",
			on.V1Rows, off.V1Rows, on.V1RowsWithoutTurns, off.V1RowsWithoutTurns,
			on.GenuineLossRows, off.GenuineLossRows)
	}
}

// TestZeroDriftStopsClaimingDriftAfterStopWrite 覆盖同一根因的另一面：
// 停写后 v1 冻结、v2 继续增长，OnlyInV2 必然单调增长，所以 ZeroDrift 若仍按
// 「false = 有漂移」上报，这道门会在停写之后**永远无法宣告完成**——spec 的
// 「7 天零漂移」退出条件因此不可满足。正确形态是报告「不可评估」。
func TestZeroDriftStopsClaimingDriftAfterStopWrite(t *testing.T) {
	dsn := resolveDualReadDSN()
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set — offline mode")
	}
	// 必须走 openDualReadPool：RLS 的 FORCE 侧在受限 DSN 用户上会静默返回
	// 0 行，而「全零」恰好能通过本门的大部分断言 —— 那正是这道门自己文件头
	// 里写的「最危险的失败形态」。本机 superuser 看不出差别，CI 看得出。
	pool := openDualReadPool(t, dsn)
	probeDualReadTables(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	v := NewDualReadValidator(pool)

	// 取一个真有轮次的会话，否则 SetDifference 两侧皆空，断言无从落地。
	var session, tenant string
	err := pool.QueryRow(ctx, `
		SELECT session_id::text, tenant_id::text
		FROM session_turns
		WHERE session_id IS NOT NULL AND session_id <> ''
		LIMIT 1`).Scan(&session, &tenant)
	if err != nil {
		t.Skipf("no session rows available: %v", err)
	}

	on, err := v.CompareDetail(ctx, tenant, session, 10)
	if err != nil {
		t.Fatalf("baseline CompareDetail: %v", err)
	}
	if !on.V1WritesEnabled {
		t.Fatalf("基线就报 v1_writes_enabled=false，库上的 S4 键已被关闭")
	}

	withV1WritesDisabled(t)
	off, err := v.CompareDetail(ctx, tenant, session, 10)
	if err != nil {
		t.Fatalf("post-stop CompareDetail: %v", err)
	}
	t.Logf("会话 %s: 写入中 evaluable=%v zeroDrift=%v | 停写后 evaluable=%v zeroDrift=%v onlyInV2=%d",
		session, on.ZeroDriftEvaluable, on.ZeroDrift,
		off.ZeroDriftEvaluable, off.ZeroDrift, off.OnlyInV2Count)

	if off.ZeroDriftEvaluable {
		t.Error("停写后 zero_drift_evaluable 必须为 false：v1 是冻结快照，行集差量测的是时间而不是分歧")
	}
	if off.ZeroDrift {
		t.Error("不可评估时 zero_drift 不得报 true（那会声称「无漂移」而其实没测）")
	}
}
