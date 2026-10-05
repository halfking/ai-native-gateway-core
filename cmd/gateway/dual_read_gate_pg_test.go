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

	"github.com/jackc/pgx/v5"
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

	// ⚠ 两次 Summarize 必须在**同一个快照**里取（§9.235）。它们分别在切门控
	// 前后各读一次库；用 pool 读就是两个瞬间，而本机库有活跃写入方
	// —— 实测这个门在干净基线上**8 次里挂 1 次**，报的是
	// 「门控改变了观测数字：v1 4773→4776」。断言的意图（门控只影响判定、
	// 不影响度量）是对的，错在把它实现成了「两个时刻的读数必须逐字相等」。
	//
	// ⇒ REPEATABLE READ 把两次读钉在同一个快照上，断言强度一分不打折。
	// 门控读数是 Go 层的 seam（withV1WritesDisabled），不是库里的设置，
	// 所以同事务内切换它不会与快照冲突。
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatalf("begin repeatable-read tx: %v", err)
	}
	defer tx.Rollback(ctx)
	v := NewDualReadValidatorOn(tx)

	// ⚠ 窗口起点也必须钉住（§9.235）。`Summarize` 内部用 Go 的 `time.Now()`
	// 推 24h 窗口起点，两次调用相差几毫秒、边界就差几毫秒，会有一行从窗口
	// 里掉出去 —— 实测「同事务、不同窗口」仍然约 3 次里挂 1 次。
	// 快照相同、答案照样是错的，这是本节最反直觉的一处。
	obsAt := time.Now()

	// 基线：写入中。先确认它此刻**会**给出一个基于真实证据的答案，否则后面的
	// 「翻转」就没有对照物（若基线本来就是 void，后面无从证明什么）。
	on, err := v.SummarizeFrom(ctx, "", obsAt, 24)
	if err != nil {
		t.Fatalf("baseline Summarize: %v", err)
	}
	t.Logf("基线(写入中): v1=%d noTurns=%d genuine=%d v1覆盖率=%.2f%% ready=%v void=%v reason=%q",
		on.V1Rows, on.V1RowsWithoutTurns, on.GenuineLossRows, on.V1CoveragePP,
		on.S4Ready, on.S4GateVoid, on.S4GateVoidReason)

	// 覆盖率必须**真的被测出来**，而不是停在初值 -1（§9.235）。一个从未
	// 测过的字段会让 rule 3 恒不触发，而 rule 3 恒不触发看起来和「覆盖率
	// 一直达标」完全一样。
	if on.V1CoveragePP < 0 {
		t.Fatalf("v1 覆盖率从未被测量（V1CoveragePP=%v）—— rule 3 因此永远不会触发",
			on.V1CoveragePP)
	}
	if !on.V1WritesEnabled {
		t.Fatalf("基线就报 v1_writes_enabled=false：本机 settings 已把 S4 键设为 false，" +
			"下面的对照失去意义（请复位该键或改用别的库）")
	}
	if on.V1Rows == 0 {
		t.Skipf("24h 窗口内零 V1 行（v1 可能是测试库/静默期），无法构造对照")
	}

	// 关停态：同一个库、同一段 SQL、同一窗口，只把门控读数换成 false。
	withV1WritesDisabled(t)
	off, err := v.SummarizeFrom(ctx, "", obsAt, 24)
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

// TestS4CoverageIsCrossValidated measures the same quantity a second, different
// way and demands the two agree.
//
// Why this exists: mutation M6 divided the coverage ratio by the covered-hour
// count instead of the traffic-bearing count, which makes the ratio a constant
// 100%. Every other gate stayed green. The control pair could not catch it —
// it feeds numbers into the verdict function and never computes one — and the
// real-database assertion only checked that coverage was measured at all.
//
// That is a real hole, not a contrived one: anything that makes the reported
// coverage *too high* is invisible, because the floor only rejects low values.
// So the number itself needs a witness.
//
// The reference is written deliberately differently: it derives hour buckets
// from the rows (`date_trunc`) instead of enumerating the window with
// `generate_series` + EXISTS. The two disagree slightly on partial hours at
// the window edges — measured 20.31% vs 19.69% on this window — hence the
// tolerance. Agreeing to 2pp is still nowhere near the 100% that M6 produces.
//
// The window is historical and pinned via SummarizeFrom, for two reasons at
// once: it is the one window whose true coverage is nowhere near 100% (every
// live window measures 100%), and a past window cannot move under a
// concurrent writer, so the reference and the gate see the same data.
func TestS4CoverageIsCrossValidated(t *testing.T) {
	dsn := resolveDualReadDSN()
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set — offline mode")
	}
	pool := openDualReadPool(t, dsn)
	probeDualReadTables(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The local v1 write outage: v1 writes stop 09-06 22:00 and resume
	// 09-11 21:00 while session_turns keeps recording.
	const winStart = "2026-09-06 00:00:00+08"
	// RFC3339 (with the T) so time.Parse can read it; the +08 offset matches the
	// database's own timestamps, which are what the hour buckets are cut from.
	const winEnd = "2026-09-12T00:00:00+08:00"
	const hours = 144

	// Reference: hour buckets derived from the rows, both storage faces.
	var refCovered, refBearing int64
	if err := pool.QueryRow(ctx, `
		WITH v AS (
		  SELECT date_trunc('hour', ts) h FROM request_logs_hot WHERE ts >= $1 AND ts < $2
		  UNION ALL
		  SELECT date_trunc('hour', ts) FROM request_logs      WHERE ts >= $1 AND ts < $2),
		s AS (
		  SELECT date_trunc('hour', ts) h FROM session_turns_hot WHERE ts >= $1 AND ts < $2
		  UNION ALL
		  SELECT date_trunc('hour', ts) FROM session_turns      WHERE ts >= $1 AND ts < $2)
		SELECT (SELECT count(DISTINCT h) FROM v),
		       (SELECT count(*) FROM (SELECT h FROM v UNION SELECT h FROM s) u)`,
		winStart, "2026-09-12 00:00:00+08").Scan(&refCovered, &refBearing); err != nil {
		t.Fatalf("reference coverage: %v", err)
	}
	if refBearing == 0 {
		t.Skip("window has no traffic — nothing to cross-validate against")
	}
	want := 100 * float64(refCovered) / float64(refBearing)
	t.Logf("参考口径（date_trunc 推小时桶）: %d/%d = %.2f%%", refCovered, refBearing, want)

	now, err := time.Parse(time.RFC3339, winEnd)
	if err != nil {
		t.Fatalf("parse window end: %v", err)
	}
	sum, err := NewDualReadValidator(pool).SummarizeFrom(ctx, "", now, hours)
	if err != nil {
		t.Fatalf("SummarizeFrom: %v", err)
	}
	t.Logf("门内口径（generate_series 枚举小时）: %.2f%%  reason=%q", sum.V1CoveragePP, sum.S4GateVoidReason)

	if diff := sum.V1CoveragePP - want; diff > 2 || diff < -2 {
		t.Errorf("覆盖率两条口径不一致: 门内 %.2f%% vs 参考 %.2f%%（差 %.2fpp）。"+
			"只核对「是否被测量过」是不够的 —— 让覆盖率**偏高**的变异（如 M6 那种"+
			"除数写错）在任何门里都不会显形", sum.V1CoveragePP, want, diff)
	}
	// And the direction that matters: this window must be one rule 3 rejects.
	// If the gate reported 100% here it would be permitting a cutover on the
	// strength of a fifth of the window.
	if sum.S4GateVoidReason != s4GateReasonInsufficientV1Coverage {
		t.Errorf("跨 5 天写入中断的窗口应判 %q，实得 %q（s4_ready=%v）—— "+
			"这条路径不成立，上面那个交叉校验也就没有意义了",
			s4GateReasonInsufficientV1Coverage, sum.S4GateVoidReason, sum.S4Ready)
	}
}
