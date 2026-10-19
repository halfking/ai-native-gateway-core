package ursm_test

// pg17-vacuum-bloat.sh 的「确认闸」行为门（2026-10-19，§117）
//
// # 这道门要拦的事故
//
// 该脚本每周日 03:15 跑，对 `dead_pct >= 30% 且 size > 256MB` 的表执行
// **VACUUM FULL** —— 全程拿 **ACCESS EXCLUSIVE 锁**（本脚本 TABLE_TIMEOUT_MIN=30）。
//
// 而它的 `dead_pct` 是**从 pg_stat 的估计值算的**：
//
//	100.0 * n_dead_tup / (n_live_tup + n_dead_tup)   ← pg_stat_user_tables
//
// pg_stat 会被 PG 重启清零、只在 ANALYZE/VACUUM 之后才回来 ⇒
// **一张其实很健康的表，可能因为估计值失真而被锁 30 分钟。**
// 代价落在 252 上所有在线业务（154 网关经它出网）。
//
// ★ 与 §116 同族：**估计量只能筛可疑项，不能驱动不可逆动作。**
//   同文件头部本来就写着「用 pgstattuple 扩展（如果没有就用 dead_tup_pct 估算）」，
//   但**估算那条路径是唯一的实现** —— 承诺没兑现。
//
// # 闸的设计（fail-safe 方向）
//
// ① 实测（pgstattuple_approx，真读页）< MIN_DEAD_PCT ⇒ SKIP
// ② 实测**量不出来**（扩展缺失 / 读失败）⇒ SKIP 并留痕 ⇒ **宁可不做，不可错做**
// ③ 实测达标 ⇒ 才 VACUUM FULL
//
// # 为什么不用子串断言
//
// 与 emergency_cleanup_l1_drop_guard_test.go 同理：本门注入假 docker，
// 让脚本真的跑到判定分支，再断言**它对哪些表发出了 VACUUM FULL**。

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const vacuumBloatScript = "../252-monitor/pg17-vacuum-bloat.sh"

// fakeVacuumDocker 记录每次调用，并按 SQL 内容应答：
//   - pg_total_relation_size(…)      → 300MB（让目标过 256MB 门槛）
//   - pgstattuple_approx(…)           → $FAKE_REAL_PCT（量不出来时为空串）
//   - BLOAT_SQL（dead_tuple_percent 出现在别处 → 用 n_dead_tup 判） → $TARGETS
func fakeVacuumDocker(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/sh
all="$*"
{
  echo "--- call ---"
  for a in "$@"; do echo "$a"; done
} >> "$TRACE_FILE"

case "$all" in
  *"pgstattuple_approx"*)
      printf '%s' "${FAKE_REAL_PCT:-}"
      ;;
  *"n_dead_tup"*)
      printf '%s' "$TARGETS"
      ;;
  *pg_total_relation_size*)
      echo "314572800"
      ;;
  *VACUUM\ FULL*)
      :
      ;;
  *)
      :
      ;;
esac
exit 0
`
	p := filepath.Join(dir, "fake-docker")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 docker: %v", err)
	}
	return p
}

func runVacuumBloat(t *testing.T, targets, realPct string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	fake := fakeVacuumDocker(t, dir)
	trace := filepath.Join(dir, "trace.txt")
	logPath := filepath.Join(dir, "vacuum.log")

	cmd := exec.Command(bashForScriptFor(t, vacuumBloatScript), vacuumBloatScript)
	cmd.Env = append(os.Environ(),
		"DOCKER_BIN="+fake,
		"TRACE_FILE="+trace,
		"TARGETS="+targets,
		"FAKE_REAL_PCT="+realPct,
		"LOG="+logPath,
		"CONTAINER=fake-pg", "PG_USER=u", "PG_DB=d",
		"LLMGW_NO_SINGLE_INSTANCE_LOCK=1",
		"LLMGW_LOCK_DIR="+dir,
	)
	_, _ = cmd.CombinedOutput()

	traceBody, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("读调用轨迹失败（脚本可能没跑到候选查询）: %v", err)
	}
	logBody, _ := os.ReadFile(logPath)
	return string(traceBody), string(logBody)
}

func skipVacuumWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 基线（既有，非回归）：守卫脚本与 psql stub 是 bash 轨迹")
	}
}

func requireVacuumCalls(t *testing.T, trace string) {
	t.Helper()
	if !strings.Contains(trace, "--- call ---") {
		t.Fatalf("脚本一次都没调 docker —— 门禁空转（注入点失效 / 脚本提前退出）")
	}
}

// TestVacuumBloatSkipsWhenEstimateLies 是本门的正身：
// 估算说 52.3% 很脏，**实测只有 3.1%** ⇒ 必须 SKIP，绝不能拿独占锁去 VACUUM FULL。
func TestVacuumBloatSkipsWhenEstimateLies(t *testing.T) {
	skipVacuumWindows(t)

	trace, logBody := runVacuumBloat(t, "public.session_summaries|993697792|52.3", "3.1")

	requireVacuumCalls(t, trace)
	if strings.Contains(trace, "VACUUM FULL public.session_summaries") {
		t.Fatalf("★ 估算 52.3%% 但实测 3.1%% 仍执行了 VACUUM FULL —— 白拿 30 分钟 ACCESS EXCLUSIVE\ntrace:\n%s\nlog:\n%s", trace, logBody)
	}
	if !strings.Contains(logBody, "SKIP") || !strings.Contains(logBody, "3.1") {
		t.Fatalf("SKIP 时必须在日志里写明实测值，便于事后反查\nlog:\n%s", logBody)
	}
}

// TestVacuumBloatSkipsWhenUnmeasurable 覆盖 fail-safe 方向：
// 实测量不出来（扩展缺失 / 读失败）⇒ 同样 SKIP，而不是「量不了就当达标」。
func TestVacuumBloatSkipsWhenUnmeasurable(t *testing.T) {
	skipVacuumWindows(t)

	trace, logBody := runVacuumBloat(t, "public.session_summaries|993697792|52.3", "")

	requireVacuumCalls(t, trace)
	if strings.Contains(trace, "VACUUM FULL public.session_summaries") {
		t.Fatalf("★ 实测量不出来仍然执行了 VACUUM FULL —— 应当 fail-safe 跳过\ntrace:\n%s", trace)
	}
	if !strings.Contains(logBody, "量不出来") {
		t.Fatalf("必须显式区分「量不出来」与「量出来不够脏」\nlog:\n%s", logBody)
	}
}

// TestVacuumBloatStillRunsWhenConfirmed 钉住「别把脚本改瘫」：
// 实测确实达标（55%）时**必须**执行 VACUUM FULL。
func TestVacuumBloatStillRunsWhenConfirmed(t *testing.T) {
	skipVacuumWindows(t)

	trace, _ := runVacuumBloat(t, "public.session_summaries|993697792|52.3", "55.0")

	requireVacuumCalls(t, trace)
	if !strings.Contains(trace, "VACUUM FULL public.session_summaries") {
		t.Fatalf("实测 55%% 达标时应当执行 VACUUM FULL，但轨迹里没有 —— 脚本可能被改瘫，判据失去意义\ntrace:\n%s", trace)
	}
}

// TestVacuumBloatUsesConfirmGate 钉住闸本身存在（文本断言，可枚举属性）。
func TestVacuumBloatUsesConfirmGate(t *testing.T) {
	b, err := os.ReadFile(vacuumBloatScript)
	if err != nil {
		t.Fatalf("读脚本: %v", err)
	}
	s := string(b)
	for _, needle := range []string{"pgstattuple_approx", "MIN_DEAD_PCT", "SKIP"} {
		if !strings.Contains(s, needle) {
			t.Fatalf("确认闸缺少 %q —— 估计量又在直接驱动 VACUUM FULL", needle)
		}
	}
}

// ---------------------------------------------------------------------------
// §118（2026-10-19）：把生产实测的校准数据与「列存表」盲区钉成判据
// ---------------------------------------------------------------------------

// TestVacuumBloatCandidateExcludesColumnarTables 钉住 §118 的实测盲区：
//
//	SELECT ... FROM pgstattuple_approx('routing_decision_log_2026_09');
//	ERROR:  only heap AM is supported
//
// 该表的 amname 是 `columnar`（不是 columnar_internal 里的元数据表，
// 而是 **public schema 下的列存表**）⇒ 旧 WHERE 只排 `schemaname NOT IN
// ('columnar_internal')`，**排除不掉它** ⇒ 它会进候选 ⇒ 确认闸必然失败
// ⇒ 被 fail-safe 跳过。行为上安全，但那是**静默的永久失效**：
// 日志只会写「量不出来」，读起来与「扩展没装」一模一样。
//
// ⇒ 现在在候选层用 `pg_am` JOIN + `amname='heap'` 显式排除，
// 让「列存不在本脚本范围内」成为看得见的口径，而不是靠运行时失败。
func TestVacuumBloatCandidateExcludesColumnarTables(t *testing.T) {
	skipVacuumWindows(t)

	trace, _ := runVacuumBloat(t, "", "")

	requireVacuumCalls(t, trace)
	for _, needle := range []string{"pg_am", "amname = 'heap'"} {
		if !strings.Contains(trace, needle) {
			t.Fatalf("候选 SQL 缺少 %q —— 列存表（amname='columnar'）会进候选，"+
				"而 pgstattuple_approx 对非堆表必然报 `only heap AM is supported`，"+
				"于是列存表被静默地永久跳过（fail-safe 但看不出原因）\ntrace:\n%s", needle, trace)
		}
	}
}

// TestVacuumBloatSkipsAtProductionCalibration 用**生产真值**驱动闸：
// session_summaries（857MB）实测
//
//	估算 dead_pct = 62,877/(800,561+62,877) = 7.29%
//	实测 pgstattuple_approx dead_tuple_percent = 0.346%
//	⇒ n_dead_tup 在本库高估约 21 倍
//
// 换句话说：估算说「脏了」，真读页说「几乎不脏」。
// 本例两者都低于 30% 门槛、结果一致，但**方向与量级都是真的** ——
// 换一张更大的表或更低的门槛，估算就会把健康表推进 VACUUM FULL。
//
// ⇒ 因此钉住这个量级本身：估算远超阈值而实测极低时，必须 SKIP。
func TestVacuumBloatSkipsAtProductionCalibration(t *testing.T) {
	skipVacuumWindows(t)

	// 估算用一个远超门槛的值（真实估算是 7.29%，这里取 30.0 恰好压线之上，
	// 目的是让「估算达标、实测极低」这个组合成为**唯一**的触发条件）
	trace, logBody := runVacuumBloat(t, "public.session_summaries|898584576|30.0", "0.3")

	requireVacuumCalls(t, trace)
	if strings.Contains(trace, "VACUUM FULL public.session_summaries") {
		t.Fatalf("★ 估算过线（30.0%%）但实测仅 0.3%%（生产实测高估约 21 倍）仍执行了 VACUUM FULL —— "+
			"确认闸失效，白拿 30 分钟 ACCESS EXCLUSIVE\ntrace:\n%s\nlog:\n%s", trace, logBody)
	}
	if !strings.Contains(logBody, "0.3") {
		t.Fatalf("SKIP 时必须留下实测值，否则事后无法区分「闸起作用」与「作业没跑」\nlog:\n%s", logBody)
	}
}
