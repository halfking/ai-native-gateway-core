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
      # ★ 2026-10-19：原桩在这里无条件 printf 后走末尾的 exit 0，
      #   于是「psql 返回非 0」这个真实形态在测试里**根本不存在** ——
      #   而 set -euo pipefail 下正是它会让整轮任务静默终止（见 §142）。
      #   「量不出来 ⇒ SKIP」那道门验的是**空串**，不是**失败**。
      #   ⇒ 这里让桩能按表名、按退出码、按报错文本失败。
      #   ⚠ 本段在 Go raw string 里，写注释时不要用反引号（会提前终止字面量）。
      if [ -n "${FAKE_MEASURE_EXIT:-}" ] && case "$all" in *${FAKE_MEASURE_FAIL_FOR:-__none__}*) true;; *) false;; esac; then
          [ -n "${FAKE_MEASURE_ERR:-}" ] && printf '%s\n' "${FAKE_MEASURE_ERR}" >&2
          exit "${FAKE_MEASURE_EXIT}"
      fi
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

// runVacuumBloat 跑一轮脚本。
// extra 用于追加桩行为的环境变量（§142 让桩能真的失败）。
// 返回（调用轨迹, 脚本日志, 脚本退出码）—— 退出码是本轮的核心断言之一：
// 「跳过一张表」与「整轮死在这里」在日志上都可能只留一行，只有退出码与
// 后续候选是否被处理能区分。
func runVacuumBloat3(t *testing.T, targets, realPct string, extra ...string) (string, string, int) {
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
	cmd.Env = append(cmd.Env, extra...)
	err := cmd.Run()
	exitCode := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("启动脚本失败: %v", err)
	}

	traceBody, rerr := os.ReadFile(trace)
	if rerr != nil {
		t.Fatalf("读调用轨迹失败（脚本可能没跑到候选查询）: %v", rerr)
	}
	logBody, _ := os.ReadFile(logPath)
	return string(traceBody), string(logBody), exitCode
}

// runVacuumBloat 是既有五条用例的薄封装（只要轨迹与日志）。
func runVacuumBloat(t *testing.T, targets, realPct string) (string, string) {
	t.Helper()
	trace, logBody, _ := runVacuumBloat3(t, targets, realPct)
	return trace, logBody
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

// ---------------------------------------------------------------------------
// §142（2026-10-19）：确认闸的 fail-safe 在**真实失败路径上从未发生过**
//
// # 缺陷本体
//
// §117 写下的闸是「量不出来 ⇒ SKIP ⇒ 宁可不做，不可错做」，
// 并为此配了 TestVacuumBloatSkipsWhenUnmeasurable。
// 那道门**是绿的，但验的不是那件事**：
//
//   ① 原桩对 pgstattuple_approx 无条件 printf 后走末尾的 exit 0
//      ⇒ 「psql 返回非 0」这个形态在测试里被**排除掉了**，
//         「量不出来」在那道门里等于「返回空串」。
//   ② 真机上 psql 失败时返回**非 0**，而本脚本开头是 set -euo pipefail：
//         x=$(docker … psql … | tr …)
//      管道整体失败 ⇒ 赋值语句退出码非 0 ⇒ **set -e 直接终止整个脚本**。
//      （本机实测：set -euo pipefail 下 x=$(false | tr …) 之后那行不执行、退出码 1；
//        去掉 pipefail 才会带着空值继续。）
//
// ⇒ 真实形态不是「跳过这张表」，而是「**整轮任务在这里静默死掉**」：
//   后续候选一个不跑、收尾行没有、cron 又把输出丢进 /dev/null。
//   而 §118 已实测列存表会让 pgstattuple_approx **必然**失败
//   （only heap AM is supported）⇒ 这条路径一旦被触发就是必然发生。
//
// ⇒ 本节的三道门：失败不得终止、后续候选必须继续、失败原因必须分类留痕。
// ---------------------------------------------------------------------------

// TestVacuumBloatConfirmFailureDoesNotAbortRun 是本组门的正身。
// 候选里放两张表：第一张的确认闸**真的返回非 0**，第二张正常。
// 期望：t_a 被 SKIP 并写明原因，**t_b 仍然走到 VACUUM FULL**，且脚本退出码为 0。
func TestVacuumBloatConfirmFailureDoesNotAbortRun(t *testing.T) {
	skipVacuumWindows(t)

	targets := "public.t_a|993697792|52.3\npublic.t_b|993697792|52.3"
	trace, logBody, exitCode := runVacuumBloat3(t, targets, "55.0",
		"FAKE_MEASURE_EXIT=1",
		"FAKE_MEASURE_FAIL_FOR=public.t_a",
		"FAKE_MEASURE_ERR=ERROR:  relation \"x\" does not exist")

	requireVacuumCalls(t, trace)
	// ★ 断言顺序刻意：先查「后续候选有没有被处理」。
	//   「t_a 没留下 SKIP」与「整轮终止」是同一个缺陷的两个症状，
	//   而后者才是本门要拦的那句话；先报它，失败输出才说的是**结论**而不是副作用。
	if !strings.Contains(trace, "VACUUM FULL public.t_b") {
		t.Fatalf("★ 确认闸失败导致**整轮终止**：后面的候选 t_b 根本没被处理 —— "+
			"这正是 set -euo pipefail 吃掉失败的真实形态（跳过一张 ≠ 整轮死掉）\n"+
			"trace:\n%s\nlog:\n%s", trace, logBody)
	}
	if strings.Contains(trace, "VACUUM FULL public.t_a") {
		t.Fatalf("★ 确认闸读失败的表被执行了 VACUUM FULL\ntrace:\n%s\nlog:\n%s", trace, logBody)
	}
	if !strings.Contains(logBody, "SKIP public.t_a") {
		t.Fatalf("失败的那张表必须留下 SKIP 记录\nlog:\n%s", logBody)
	}
	if exitCode != 0 {
		t.Fatalf("确认闸读失败属 fail-safe（宁可不做），脚本不应以非 0 退出；got %d\nlog:\n%s", exitCode, logBody)
	}
}

// TestVacuumBloatClassifiesConfirmFailureReasons 钉住「失败原因必须分类」：
// 超时、非堆表、扩展缺失，三种的**修法完全不同**，
// 混成一句「扩展缺失或读失败」等于把判据缺陷报成环境问题。
func TestVacuumBloatClassifiesConfirmFailureReasons(t *testing.T) {
	skipVacuumWindows(t)

	cases := []struct {
		name string
		err  string
		want string
	}{
		{"timeout", "ERROR:  canceling statement due to statement timeout", "读超时"},
		{"columnar", "ERROR:  only heap AM is supported", "非堆表"},
		{"noext", `ERROR:  function pgstattuple_approx(unknown) does not exist`, "不可用"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, logBody, _ := runVacuumBloat3(t, "public.t_a|993697792|52.3", "",
				"FAKE_MEASURE_EXIT=1",
				"FAKE_MEASURE_FAIL_FOR=public.t_a",
				"FAKE_MEASURE_ERR="+c.err)
			if !strings.Contains(logBody, c.want) {
				t.Fatalf("失败原因 %q 应被归类为含 %q 的措辞，实际日志：\n%s", c.err, c.want, logBody)
			}
		})
	}
}

// TestVacuumBloatConfirmGateHasExplicitTimeout 钉住显式超时存在：
// 不设超时的话，确认闸继承库默认（252 实测 30s），
// 而实测最坏一次已用到 17s ⇒ 只剩 43% 余量，静默超时会与「扩展缺失」写成同一句话。
func TestVacuumBloatConfirmGateHasExplicitTimeout(t *testing.T) {
	b, err := os.ReadFile(vacuumBloatScript)
	if err != nil {
		t.Fatalf("读脚本: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "CONFIRM_TIMEOUT_SEC") {
		t.Fatalf("确认闸必须显式设超时，否则继承库默认（本库 30s，实测最坏 17s）")
	}
	// PGOPTIONS 必须真的挂到那一次 psql 调用上，而不是只定义一个变量。
	// 光有变量定义不算数：它必须出现在**确认闸那一次 exec** 的参数里。
	// ⚠ 用字面量而不是「就近搜索」：确认闸上方那段注释很长（本轮就把它挤出了
	//   800 字节窗口），窗口式断言会因为**注释变长**而误红——那是判据量的东西不对。
	//   判据该钉的是「这次调用带上了超时参数」这个事实，与注释长短无关。
	needle := `PGOPTIONS=-c statement_timeout=${CONFIRM_TIMEOUT_SEC}`
	idx := strings.Index(s, needle)
	if idx < 0 {
		t.Fatalf("确认闸那次 exec 没有带上 statement_timeout=${CONFIRM_TIMEOUT_SEC}（只定义变量不算数）")
	}
	rest := s[idx:]
	if !strings.Contains(rest, "pgstattuple_approx") {
		t.Fatalf("超时参数出现的位置在确认闸调用之前或之外 —— 它没有作用到那一次 psql\n%s", rest[:400])
	}
}
