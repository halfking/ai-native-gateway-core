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
//   - pgstattuple_approx(…)           → $FAKE_REAL_PCT|$FAKE_FREE_BYTES（量不出来时为空串）
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
      # ★ 2026-10-19（§143.7）：确认闸一次调用取**两个**量，用 | 分隔。
      #   ⚠⚠ 桩必须**按它实际收到的 SQL 应答**，不能无条件吐 "pct|bytes"：
      #     2026-10-19 变异 M6 把脚本 SELECT 里的 approx_free_space 删掉，
      #     **所有门照样全绿** —— 因为桩不看查了什么，无条件返回两个字段。
      #     而真机上少查一个字段 ⇒ 返回值匹配不上脚本的解析正则
      #     ⇒ 每一张表都落进「量不出来」⇒ 脚本变成**永不触发的作业**，
      #     正是 §118/§142 刚花两轮拆掉的「静默永久失效」那一类。
      #   ⇒ 这里按 $all 里有没有 approx_free_space 决定返回形态，与真机对齐。
      case "$all" in
        *approx_free_space*) printf '%s|%s' "${FAKE_REAL_PCT:-}" "${FAKE_FREE_BYTES:-}" ;;
        *)                   printf '%s'  "${FAKE_REAL_PCT:-}" ;;
      esac
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

// runVacuumBloat 是既有用例的薄封装（只要轨迹与日志）。
//
// ★ 2026-10-19（§143.7）：签名加了 freeBytes。确认闸现在一次取两个量
//   （dead_tuple_percent | approx_free_space），桩也复刻了这个形态；
//   **每个调用点都必须显式给出 freeBytes** —— 留空会落进「量不出来」分支，
//   而那个分支的结论恰好也是 SKIP ⇒ 断言 SKIP 的门会**因为错的原因变绿**。
func runVacuumBloat(t *testing.T, targets, realPct, freeBytes string) (string, string) {
	t.Helper()
	trace, logBody, _ := runVacuumBloat3(t, targets, realPct, "FAKE_FREE_BYTES="+freeBytes)
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

// TestVacuumBloatDecidesOnReclaimableBytesNotDeadRatio 是 §143.7 的正身，
// 并**取代**原来的 TestVacuumBloatSkipsWhenEstimateLies。
//
// ★ 为什么必须取代而不是保留：旧判据下「估算 52.3% / 实测 3.1% ⇒ SKIP」
//   验的是「别信估算」。新判据**根本不看比例** ⇒ 同一条断言在新代码里
//   会因为「free 字节数不够」而 SKIP —— **结论对、原因错**，
//   而「因为错的原因变绿」的门比红的门更危险：它会在比例腿被改坏时继续绿。
//
// 本例反过来钉：实测 dead 高达 60%（远超旧门槛 30%）、估算也 52.3%，
// 但 993MB 的表只可回收 1MB ⇒ **必须 SKIP**。
// 旧代码在这个输入下会执行 VACUUM FULL（60 > 30）⇒ 本门对它转红。
func TestVacuumBloatDecidesOnReclaimableBytesNotDeadRatio(t *testing.T) {
	skipVacuumWindows(t)

	// size=993697792（≈948MB）≤ MAX_RECLAIM_MB(1024MB) ⇒ 比例腿**可能**满足，
	// 唯一让它落空的是那 1MB 的 free ⇒ 这条门专门钉「比例腿没被顺手删掉」。
	trace, logBody := runVacuumBloat(t, "public.session_summaries|993697792|52.3", "60.0", "1048576")

	requireVacuumCalls(t, trace)
	if strings.Contains(trace, "VACUUM FULL public.session_summaries") {
		t.Fatalf("★ 实测 dead=60%%（远超旧门槛 30%%）但只可回收 1MB 仍执行了 VACUUM FULL —— "+
			"判据还在按死元组比例决策，这就是 §119 说的「对 948MB 的表锁 30 分钟换 1MB」的负收益\n"+
			"trace:\n%s\nlog:\n%s", trace, logBody)
	}
	if !strings.Contains(logBody, "SKIP") {
		t.Fatalf("SKIP 时必须留痕\nlog:\n%s", logBody)
	}
}

// TestVacuumBloatIgnoresDeadRatioWhenBytesAreWorthIt 是上一条的反面：
// 实测 dead 只有 0.4%（旧判据下会 SKIP），但 500MB 的表可回收 200MB
// ⇒ **必须 VACUUM FULL**。旧代码在这个输入下会 SKIP（0.4 < 30）⇒ 本门对它转红。
//
// ⚠ 夹具是**刻意**挑的：free=200,000,000B < 256MB(=268,435,456B)
//   ⇒ 绝对腿**不满足**；ratio=200/500=40.00% ≥ 30% 且 500MB ≤ 1GB
//   ⇒ **只有比例腿能让它通过**。
//   ⇒ 比例腿若被删或被改阈值，这条就红。
//   ⚠ 第一版这里写的是 free=300MB，而 300,000,000 > 268,435,456 ——
//     绝对腿照样满足，这条门会因为「错的原因」变绿（它压根没验到比例腿）。
//     这正是「边界值要取在两侧、不要取一个自以为在阈值内侧的数」。
func TestVacuumBloatIgnoresDeadRatioWhenBytesAreWorthIt(t *testing.T) {
	skipVacuumWindows(t)

	trace, logBody := runVacuumBloat(t, "public.session_summaries|500000000|52.3", "0.4", "200000000")

	requireVacuumCalls(t, trace)
	if !strings.Contains(trace, "VACUUM FULL public.session_summaries") {
		t.Fatalf("实测可回收 200MB/500MB=40%%（绝对腿不满足、只有比例腿）时应当 VACUUM FULL —— "+
			"比例腿坏了这条就红\ntrace:\n%s\nlog:\n%s", trace, logBody)
	}
	if !strings.Contains(logBody, "比例") {
		t.Fatalf("必须记下是比例腿触发（200MB < 绝对腿 256MB）\nlog:\n%s", logBody)
	}
	if strings.Contains(logBody, "触发腿=绝对") {
		t.Fatalf("这条夹具本意是只走比例腿；若日志说绝对腿触发，说明 256MB 的常量被改小到 ≤200MB\nlog:\n%s", logBody)
	}
}

// TestVacuumBloatSkipsWhenUnmeasurable 覆盖 fail-safe 方向：
// 实测量不出来（扩展缺失 / 读失败）⇒ 同样 SKIP，而不是「量不了就当达标」。
func TestVacuumBloatSkipsWhenUnmeasurable(t *testing.T) {
	skipVacuumWindows(t)

	// ★ freeBytes 留空是有意的：桩会输出 "|"（两个字段都空），
	//   脚本的解析正则要求 `^[0-9]+(\.[0-9]+)?\|[0-9]+$` ⇒ 匹配不上 ⇒ 落进「量不出来」。
	//   这正是「psql 回了但没有可用数值」的形态。
	trace, logBody := runVacuumBloat(t, "public.session_summaries|993697792|52.3", "", "")

	requireVacuumCalls(t, trace)
	if strings.Contains(trace, "VACUUM FULL public.session_summaries") {
		t.Fatalf("★ 实测量不出来仍然执行了 VACUUM FULL —— 应当 fail-safe 跳过\ntrace:\n%s", trace)
	}
	if !strings.Contains(logBody, "量不出来") {
		t.Fatalf("必须显式区分「量不出来」与「量出来不够脏」\nlog:\n%s", logBody)
	}
}

// TestVacuumBloatStillRunsWhenConfirmed 钉住「别把脚本改瘫」：
// 确认闸量到了、且**确实值得做**时，必须执行 VACUUM FULL。
//
// ★ 2026-10-19（§143.7）：夹具从「dead=55%」换成「free=300MB」，因为
//   决策量换了。**并且刻意只用绝对腿**：size=4GB > MAX_RECLAIM_MB(1024MB)
//   ⇒ 比例腿被体积上限挡住；free=300MB ≥ 256MB ⇒ 只有绝对腿能让它通过。
//   ⇒ 这条门因此专门钉「绝对腿」，与比例腿的门各钉一条，删任一条都会被抓到。
func TestVacuumBloatStillRunsWhenConfirmed(t *testing.T) {
	skipVacuumWindows(t)

	trace, logBody := runVacuumBloat(t, "public.session_summaries|4000000000|52.3", "12.0", "300000000")

	requireVacuumCalls(t, trace)
	if !strings.Contains(trace, "VACUUM FULL public.session_summaries") {
		t.Fatalf("实测可回收 300MB（≥绝对腿 256MB）时应当执行 VACUUM FULL，但轨迹里没有 —— "+
			"脚本可能被改瘫，判据失去意义\ntrace:\n%s\nlog:\n%s", trace, logBody)
	}
	if !strings.Contains(logBody, "绝对") {
		t.Fatalf("必须记下是绝对腿触发（size 4GB 已超比例腿的体积上限）\nlog:\n%s", logBody)
	}
}

// TestVacuumBloatRatioLegRespectsSizeCap 钉住比例腿的**体积上限**。
//
// ★★ 这道门是**变异验证逼出来的**：第一版夹具用「2.9GB 表可回收 40%」
//   去验上限，而把上限删掉（变异 M3）时它**照样全绿**。
//   算下来原因很硬：比例腿要求 free ≥ MIN_FREE_PCT×size = 30%×size，
//   而上限针对的是 size > 1GB ⇒ free > 300MB > MIN_RECLAIM_BYTES(256MB)
//   ⇒ **绝对腿必然先满足** ⇒ 在当前默认值下，这个上限**永远不可能生效**。
//   ⇒ 第一版夹具验的其实是「free 太小所以比例腿也不满足」，与上限无关。
//
// ⇒ 所以本门改成显式注入 MIN_RECLAIM_MB=512（>300MB）**把绝对腿挪开**，
//   再对同一组数字做 A/B：
//     900MB / free 270MB(30.00%)  ⇒ 比例腿满足、体积 ≤1GB ⇒ VACUUM FULL
//     1500MB / free 450MB(30.00%) ⇒ 比例腿满足、体积 >1GB ⇒ 上限挡住 ⇒ SKIP
//   两条的 free 都 < 512MB ⇒ 绝对腿在两例中都不满足 ⇒ 差异只可能来自上限。
//   ⚠ 结论同样要写进脚本注释：默认 256/30/1024 下这个上限是**不可达的**，
//     它是「旋钮被调小之后的事故护栏」，不是当前在起作用的判据。
func TestVacuumBloatRatioLegRespectsSizeCap(t *testing.T) {
	skipVacuumWindows(t)

	raiseAbs := "MIN_RECLAIM_MB=512" // 把绝对腿挪到 450MB 之上，让它不参与

	under, logUnder, _ := runVacuumBloat3(t, "public.under_cap|900000000|1.0", "0.5", raiseAbs, "FAKE_FREE_BYTES=270000000")
	requireVacuumCalls(t, under)
	if !strings.Contains(under, "VACUUM FULL public.under_cap") {
		t.Fatalf("900MB / free 270MB=30%% 应当 VACUUM FULL（比例腿满足、体积未超上限）—— "+
			"体积上限若被误设成 <900MB，这条就红\ntrace:\n%s\nlog:\n%s", under, logUnder)
	}

	over, logOver, _ := runVacuumBloat3(t, "public.over_cap|1500000000|1.0", "0.5", raiseAbs, "FAKE_FREE_BYTES=450000000")
	requireVacuumCalls(t, over)
	if strings.Contains(over, "VACUUM FULL public.over_cap") {
		t.Fatalf("★ 1.5GB / free 450MB=30%% 仍执行了 VACUUM FULL —— 体积上限失效；"+
			"整表重写会撞 TABLE_TIMEOUT_MIN=30min，即「拿了 ACCESS EXCLUSIVE 又做不完」，比不做更糟\n"+
			"trace:\n%s\nlog:\n%s", over, logOver)
	}
	if !strings.Contains(logOver, "SKIP") {
		t.Fatalf("被上限挡下时必须留 SKIP 痕迹\nlog:\n%s", logOver)
	}
}

// TestVacuumBloatRatioLegDefaultBoundary 钉住 MIN_FREE_PCT 的**默认值 30**
// 在边界两侧各取一个样本。
//
// ⚠ 为什么取两侧而不是「正好 30」：free/size 是浮点除法，
//   「正好 30%」要求 free = 0.30*size 取整后仍精确等于 30.00% ——
//   这类样本会随分母大小漂，做出来的是不存在的形态（§907 同族坑）。
//   ⇒ 29.90%（差 0.1 点，必须 SKIP）与 30.40%（过 0.4 点，必须做）才是真边界。
// 体积取 800MB，使两侧的 free（239.2MB / 243.2MB）**都小于**绝对腿 256MB
// ⇒ 绝对腿在这两例中都不满足，测到的确实是比例腿的默认值。
func TestVacuumBloatRatioLegDefaultBoundary(t *testing.T) {
	skipVacuumWindows(t)

	below, logBelow := runVacuumBloat(t, "public.below|800000000|1.0", "0.3", "239200000")
	requireVacuumCalls(t, below)
	if strings.Contains(below, "VACUUM FULL public.below") {
		t.Fatalf("★ 比例 29.90%%（默认门槛 30%% 之下）仍执行了 VACUUM FULL —— 默认值被改小了\ntrace:\n%s\nlog:\n%s", below, logBelow)
	}

	above, _ := runVacuumBloat(t, "public.above|800000000|1.0", "0.3", "243200000")
	requireVacuumCalls(t, above)
	if !strings.Contains(above, "VACUUM FULL public.above") {
		t.Fatalf("比例 30.40%%（默认门槛 30%% 之上）应当 VACUUM FULL，但没做 —— 默认值被改大了\ntrace:\n%s", above)
	}
}

// TestVacuumBloatReclaimThresholdDefaults 钉住三个可注入旋钮的**默认值**。
//
// ⚠ 为什么这三条需要单独一道门：MIN_RECLAIM_MB / MIN_FREE_PCT / MAX_RECLAIM_MB
//   是为了让行为门能驱动边界分支而做成注入点的（同 emergency 分支的 DOCKER_BIN）。
//   **注入点本身就是放松生产约束的后门** —— 有人把 256 改成 25，
//   判据全绿而脚本开始对着小表疯狂 VACUUM FULL。
//   ⇒ 必须有一条门钉住「不传时就是这些值」，且这条门自己也要能被变异打到
//   （把 256 改成 25，下面第 ① 条即红）。
func TestVacuumBloatReclaimThresholdDefaults(t *testing.T) {
	b, err := os.ReadFile(vacuumBloatScript)
	if err != nil {
		t.Fatalf("读脚本: %v", err)
	}
	s := string(b)
	for _, needle := range []string{
		"MIN_RECLAIM_MB=${MIN_RECLAIM_MB:-256}",
		"MIN_FREE_PCT=${MIN_FREE_PCT:-30}",
		"MAX_RECLAIM_MB=${MAX_RECLAIM_MB:-1024}",
	} {
		if !strings.Contains(s, needle) {
			t.Fatalf("缺少默认值声明 %q —— 这三个是**可注入**旋钮，"+
				"默认值被改小会让脚本开始做负收益的 VACUUM FULL，而所有行为门仍然全绿（它们都传了自己的值）", needle)
		}
	}
}

// TestVacuumBloatCandidateSQLHasNoDeadRatioFilter 钉住候选层**不再按死元组比例筛选**。
//
// ★ 这是一道**判据空洞的补丁**，不是可有可无的加固：
//   上面的行为门全部通过「注入 TARGETS」驱动，而 TARGETS 是**桩给的**，
//   它不经过候选 SQL。⇒ 有人把 `dead_pct >= 30` 加回 BLOAT_SQL 的 WHERE，
//   脚本会退回「本库恒不命中」的状态（§119 实测：44 张大表估算最高只有 8.51%），
//   **而所有行为门依然全绿** —— 因为它们压根不看候选 SQL 长什么样。
//   ⇒ 候选层是唯一一处「只能靠源码断言钉住」的地方，必须单独立一道门。
func TestVacuumBloatCandidateSQLHasNoDeadRatioFilter(t *testing.T) {
	b, err := os.ReadFile(vacuumBloatScript)
	if err != nil {
		t.Fatalf("读脚本: %v", err)
	}
	s := string(b)

	// ⚠ 从**开引号之后**开始找收尾引号：BLOAT_SQL="…" 的第一个 `"` 就是赋值
	//   语句自己的收尾引号，从它开始截会得到字符串 "BLOAT_SQL=" —— 空壳。
	//   （第一版就这么写的，失败输出是 `BLOAT_SQL=`，当场暴露。）
	marker := `BLOAT_SQL="`
	i := strings.Index(s, marker)
	if i < 0 {
		t.Fatal("找不到 BLOAT_SQL —— 门禁空转（脚本结构变了？）")
	}
	rest := s[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("BLOAT_SQL 串未闭合 —— 门禁空转")
	}
	seg := rest[:j]

	if strings.Contains(seg, "dead_pct >=") {
		t.Fatalf("★ 候选 SQL 又按死元组比例筛选了 —— 判据退回到 §119 证明与可回收字节脱钩的那个量，"+
			"且在本库恒不命中（估算最高 8.51%% vs 门槛 30%%），脚本会再次变成永不触发的作业\n%s", seg)
	}
	if !strings.Contains(seg, "size > 100*1024*1024") {
		t.Fatalf("候选 SQL 必须保留体积门槛（100MB）—— 否则会对小表反复 VACUUM FULL\n%s", seg)
	}
}

// TestVacuumBloatUsesConfirmGate 钉住闸本身存在（文本断言，可枚举属性）。
func TestVacuumBloatUsesConfirmGate(t *testing.T) {
	b, err := os.ReadFile(vacuumBloatScript)
	if err != nil {
		t.Fatalf("读脚本: %v", err)
	}
	s := string(b)
	// ★ 2026-10-19（§143.7）：MIN_DEAD_PCT 已**退役为决策量**（死元组比例与
	//   可回收字节几乎脱钩，§119 的 6 个校准样本），这里换成两条腿的变量名。
	//   ⚠ 这不是「把判据放松」：pgstattuple_approx（真读页的量）与 SKIP（fail-safe
	//   的方向）两个 needle 原样保留，被替换掉的只是**决策变量名**。
	//   决策量换掉之后，仍有两道门钉住它（…DecidesOnReclaimableBytesNotDeadRatio
	//   与 …IgnoresDeadRatioWhenBytesAreWorthIt）。
	for _, needle := range []string{"pgstattuple_approx", "MIN_RECLAIM_MB", "MIN_FREE_PCT", "SKIP"} {
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

	trace, _ := runVacuumBloat(t, "", "", "")

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
// session_summaries（857MB）§143.7 实测
//
//	dead_tuple_percent = 0.427%（估算报 7.29%，高估约 17 倍）
//	approx_free_space  = 16,256,444 B = 15.5MB，占 total 1.81%
//
// ⇒ 锁它 30 分钟只换回 1.81%，是 §119 说的**负收益** ⇒ 必须 SKIP。
// 旧判据下这条也 SKIP，但**原因是 dead 0.427% < 30%** —— 那是巧合：
//   一张「dead 低但 free 高」的表（§143.7 实测 request_stage_events 正是这个形态）
//   会被旧判据放过。所以本门现在断言的是**可回收字节**出现在日志里。
func TestVacuumBloatSkipsAtProductionCalibration(t *testing.T) {
	skipVacuumWindows(t)

	// size=898584576(857MB) ≤ 1GB ⇒ 比例腿可用；估算用一个远超旧门槛的 30.0，
	// 让「估算达标、实测不值得」成为**唯一**的触发条件。
	trace, logBody := runVacuumBloat(t, "public.session_summaries|898584576|30.0", "0.427", "16256444")

	requireVacuumCalls(t, trace)
	if strings.Contains(trace, "VACUUM FULL public.session_summaries") {
		t.Fatalf("★ 估算过线（30.0%%）但实测只可回收 16MB/857MB=1.81%% 仍执行了 VACUUM FULL —— "+
			"确认闸失效，白拿 30 分钟 ACCESS EXCLUSIVE\ntrace:\n%s\nlog:\n%s", trace, logBody)
	}
	// 断言精确百分比（1.81）而不是整数 MB：日志里的 MB 列是整除截断
	//（16,256,444B → 15MB），截断值会随样本大小漂，百分比不会。
	if !strings.Contains(logBody, "1.81%") {
		t.Fatalf("SKIP 时必须留下实测可回收比例，否则无法区分「闸起作用」与「作业没跑」\nlog:\n%s", logBody)
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
		"FAKE_FREE_BYTES=300000000",
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
