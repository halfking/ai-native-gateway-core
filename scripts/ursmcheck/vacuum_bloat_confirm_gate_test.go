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

	cmd := exec.Command(bashForScript(t), vacuumBloatScript)
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
