package bg

import (
	"strings"
	"testing"
	"time"
)

func TestTodaySuccessProbeCadenceIsBounded(t *testing.T) {
	if todaySuccessProbeInterval != 15*time.Minute {
		t.Fatalf("interval = %v, want 15m", todaySuccessProbeInterval)
	}
	if todaySuccessProbeBatch > 50 {
		t.Fatalf("batch = %d, want ≤50 so the scan stays cheap", todaySuccessProbeBatch)
	}
	// 2026-09-20 probe-volume policy: per-credential cap replaces the old
	// 1h healthy re-probe cadence (todaySuccessHealthySkipAfter). The scan is
	// a recovery re-verification: a credential contributes at most 2 pairs
	// per tick, which together with the two-success gate bounds the pass.
	if todaySuccessPerCredentialCap != 2 {
		t.Fatalf("per-credential cap = %d, want 2", todaySuccessPerCredentialCap)
	}
}

// TestTodaySuccessProbeSQLRecoveryOnly pins the 2026-09-20 re-scope: only
// pairs that carried real (non-probe) business success in 24h AND are
// currently judged unhealthy are submitted; the healthy-hourly re-probe arm
// must stay gone.
//
// ★ 2026-10-20（迁移 890，v1 族退役）：读源从 v1 热表换成**会话族热腿**
// （v1 族已 DROP，提及即整条语句 42P01）；探针排除谓词随之从 v1 那条
// （引用 quality_flags）换成**会话族**那条（origin_stage + task_type +
// origin_actor）——会话族没有 quality_flags 列，用旧谓词会 42703。
func TestTodaySuccessProbeSQLRecoveryOnly(t *testing.T) {
	sql := todaySuccessProbeSQL()
	for _, marker := range []string{
		// ★ 890：证据源是会话族热腿（渲染后的 SQL 里是真实关系名）。
		"public.session_turns_hot",
		"now() - interval '24 hours'",
		"rl.success = TRUE",
		// ★ 890：会话族探针排除谓词（三臂）——原判据里的
		// "NOT COALESCE('probe' = ANY(rl.quality_flags), FALSE)" 引用了会话族
		// **不存在**的列，必须换成会话族那条。
		"rl.origin_stage",
		"rl.task_type",
		"rl.origin_actor",
		// current-unhealthy signal (either side of the OR)
		"COALESCE(cmb.available, TRUE) = FALSE",
		"COALESCE(nps.last_direct_ok, TRUE) = FALSE",
		// INV-4 two-consecutive-success gate
		credentialTwoProbeSuccessGateSQL("unhealthy.id"),
		// per-credential cap
		"rn <= 2",
		"LIMIT $1",
	} {
		if !strings.Contains(sql, marker) {
			t.Fatalf("today-success SQL missing %q", marker)
		}
	}
	// 负向断言：会话族的**谓词**不得再引用 quality_flags（42703）。
	// ⚠ 边界：会话族原生源的**投影**里带着 `d.quality_flags AS quality_flags`
	// 这一列（canonical 契约的 118 列之一），所以不能对整条 SQL 断言
	// "quality_flags 不出现" —— 那样会被投影喂饱、恒红。
	// ⇒ 断的是**谓词形态**：`'probe' = ANY(<alias>.quality_flags)` 是 v1 物理
	// 谓词独有的写法（会话族那条三臂谓词里没有它）。
	// ⚠ 正向对照：上面那三条 origin_stage / task_type / origin_actor 断言。
	if strings.Contains(sql, "'probe' = ANY(rl.quality_flags)") {
		t.Fatalf("today-success SQL must not use the v1 physical exclusion predicate (quality_flags arm):\n%s", sql)
	}
	for _, banned := range []string{
		// the pre-2026-09-20 healthy re-probe arms
		"interval '60 minutes'",
		"nps.last_attempt_at IS NULL",
		"nps.credential_id IS NULL",
		// the retired v1 family (migration 890 dropped it)
		"_request_logs",
	} {
		if strings.Contains(sql, banned) {
			t.Fatalf("today-success SQL must not contain %q", banned)
		}
	}
}
