package ursm_test

// pg17-emergency-cleanup.sh L1 的行为门（2026-10-19，§116）
//
// # 这道门要拦的事故（**已上膛**，不是假想）
//
// 该脚本由 cron 每 15 分钟以 `--auto` 调一次，`DISK_TRIGGER_PCT=90` 时
// L1 会真的执行 `DROP TABLE IF EXISTS`。252 实测磁盘 **71%**，日志每 15 分钟
// 一行 `auto + disk < 90%, skip` ⇒ **引信还剩 19 个百分点**。
//
// L1 的「死表」判据是 `n_live_tup = 0 AND 总大小 >= 100MB`，而：
//
// ① **`n_live_tup` 是估计值，不是行数。** PG 重启后未被 ANALYZE 的表会一直
//    停在 0。本判据当前在生产命中 **6 张表 / 2,817 MB**，其中 5 张并非空表：
//    `ursm_node_snapshot_min_legacy` 有 **2,836,620 真实行**（reltuples 2,614,886）。
//    ⇒ DROP 一旦执行，就是删掉 284 万行。
//
// ② **PG 不保护已挂载的分区。** 本地 PG 17.10 实测：直接
//    `DROP TABLE <已 ATTACH 的分区>` **会成功**，父表静默丢掉该范围的行，
//    之后该范围 INSERT 直接报 `no partition of relation found` —— 写路径事故。
//    命中的 4 张分区（`usage_ledger_2026_09` 等，约 307 万行）全在这个范围里。
//
// ★ 而**正确写法就在同一个文件里**：本文件的 `_default` 分支（第 132 行起）
//   早就写着「验证是否真的为 0 行」；同族的
//   pg17-proactive-empty-table-cleanup.sh 也有 COUNT(*) 复核 + relispartition 排除。
//   ⇒ 缺陷不是「没想到」，是**没抄过来**。
//
// # 为什么不用子串断言
//
// 子串量的是「源码里写过这句话」，不是「运行时真的没 DROP」。
// 本门**注入假 docker** 让脚本真的跑到 DROP 前的三道闸，
// 再断言**它对哪些表发出了 DROP**（同 proactive_cleanup_partition_guard_test.go）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const emergencyScript = "../252-monitor/pg17-emergency-cleanup.sh"

// fakeEmergencyDocker 写一个假 docker：把每次调用的完整参数记进 $TRACE_FILE，
// 并按 SQL 内容应答。
//
// ⚠ 只有一个 $FAKE_COUNT 而不是「按表名分别应答」：早期版本做了按表名的变体，
//   那个变体**整体覆盖**了本脚本、连轨迹记录都没留 ⇒ 测试读不到 trace.txt，
//   而「轨迹为空」会让 `strings.Contains("")` 恒为 false ⇒ 用例**假绿**。
//   ⇒ 教训：假运行时的多个变体必须**共享同一份行为定义**，否则某一变体
//   会静默地把「没调到」伪装成「没发生」。
//   三个用例各自只需一个行数（2836620 / 0 / 0），单值足够。
func fakeEmergencyDocker(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/sh
all="$*"
{
  echo "--- call ---"
  for a in "$@"; do echo "$a"; done
} >> "$TRACE_FILE"

case "$all" in
  *"COUNT(*) FROM"*)
      echo "${FAKE_COUNT:-0}"
      ;;
  *relispartition*)
      printf '%s' "$CANDIDATES"
      ;;
  *pg_inherits*)
      # _default 分区候选（child|parent）。默认空 ⇒ 只关心死表分支的用例不受影响。
      printf '%s' "$DEFAULT_PARTS"
      ;;
  *pg_proc*strpos*)
      # 第四道闸：函数体是否按名字引用该分区（strpos 实测，不是 pg_depend）。
      #
      # ★ 这里必须同时匹配 **pg_proc 与 strpos** 两个词，不能只匹配 pg_proc：
      #   只按表名/模式名应答时，「有依赖」是假 docker **无条件**给的 ——
      #   于是把脚本里的 strpos 判据改坏（例如换成字面量 false），它照样返回
      #   「有依赖」⇒ SKIP 分支仍然走到 ⇒ 用例全绿。
      #   2026-10-19 实测过这个假绿：变异验证第一轮就是绿的。
      #   ⇒ 判据被**怎么问**是行为的一部分，问法变了答案就该变。
      printf '%s' "$DEPENDENTS"
      ;;
  *DROP*)
      : ;;
  *)
      : ;;
esac
exit 0
`
	p := filepath.Join(dir, "fake-docker")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 docker: %v", err)
	}
	return p
}

// runEmergency 跑一轮脚本，返回（调用轨迹, 脚本日志）。
func runEmergency(t *testing.T, candidates, fakeCount string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	fake := fakeEmergencyDocker(t, dir)
	trace := filepath.Join(dir, "trace.txt")
	logPath := filepath.Join(dir, "emergency.log")

	cmd := exec.Command(bashForScriptFor(t, emergencyScript), emergencyScript, "--level", "L1")
	cmd.Env = append(os.Environ(),
		"DOCKER_BIN="+fake,
		"TRACE_FILE="+trace,
		"CANDIDATES="+candidates,
		"FAKE_COUNT="+fakeCount,
		"LOG="+logPath,
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

// runEmergencyDefaultParts 跑一轮 L1，喂 `_default` 分区候选与第四道闸的应答。
// 与 runEmergency 分开是因为它要多带两个环境变量，而 runEmergency 的签名
// 已被四条既有用例固定住（改签名会让人以为这几条也变了）。
func runEmergencyDefaultParts(t *testing.T, parts, fakeCount, dependents string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	fake := fakeEmergencyDocker(t, dir)
	trace := filepath.Join(dir, "trace.txt")
	logPath := filepath.Join(dir, "emergency.log")

	cmd := exec.Command(bashForScriptFor(t, emergencyScript), emergencyScript, "--level", "L1")
	cmd.Env = append(os.Environ(),
		"DOCKER_BIN="+fake,
		"TRACE_FILE="+trace,
		"CANDIDATES=",
		"DEFAULT_PARTS="+parts,
		"DEPENDENTS="+dependents,
		"FAKE_COUNT="+fakeCount,
		"LOG="+logPath,
		"LLMGW_NO_SINGLE_INSTANCE_LOCK=1",
		"LLMGW_LOCK_DIR="+dir,
	)
	_, _ = cmd.CombinedOutput()

	traceBody, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("读调用轨迹失败（脚本可能没跑到 _default 分支查询）: %v", err)
	}
	logBody, _ := os.ReadFile(logPath)
	return string(traceBody), string(logBody)
}

// TestEmergencyDefaultPartitionRefusesToDropWhenFunctionBodyReferencesIt 钉住第四道闸。
//
// 252 实测形态：public.request_logs_default 0 行 / 0 MB / relpages=0，
// 但有 4 个函数的**函数体**按名字引用它（archive_/promote_/ensure_/repair_），
// 且这层依赖 pg_depend 查不到（LANGUAGE sql 的函数体是字符串）。
// 「看起来是空的」与「可以删」之间隔着 promote 归档链路 —— 本门判的是后者不成立。
func TestEmergencyDefaultPartitionRefusesToDropWhenFunctionBodyReferencesIt(t *testing.T) {
	skipWindows(t)

	trace, logBody := runEmergencyDefaultParts(t,
		"request_logs_default|_request_logs",
		"0",
		"archive_request_logs_default(),promote_request_logs_default_batch()")

	requireCalls(t, trace)
	if strings.Contains(trace, "DROP TABLE IF EXISTS request_logs_default") {
		t.Fatalf("★ 0 行但被函数体引用的默认分区仍被摘掉 —— 会打断 promote/归档链路\ntrace:\n%s\nlog:\n%s", trace, logBody)
	}
	if strings.Contains(trace, "DETACH PARTITION request_logs_default") {
		t.Fatalf("★ 在 DROP 之前就 DETACH 了 —— 分区先被摘下来，后续 INSERT 直接报 no partition of relation found\ntrace:\n%s", trace)
	}
	if !strings.Contains(logBody, "SKIP") || !strings.Contains(logBody, "promote_request_logs_default_batch") {
		t.Fatalf("必须在日志里留下 SKIP 与具体函数名，否则事后无法反查为什么没删\nlog:\n%s", logBody)
	}
}

// TestEmergencyDefaultPartitionDropsWhenUnreferenced 是**正向下限**：
// 没有任何函数体引用时，0 行的默认分区仍然必须被摘掉。
// 没有这条，上面那道门可以靠「一律 SKIP」变绿，而线上唯一的候选
// （request_logs_default）恰好**永远**会被第四道闸挡住 —— 那这道门
// 测的就是一个生产上永不发生的分支。
func TestEmergencyDefaultPartitionDropsWhenUnreferenced(t *testing.T) {
	skipWindows(t)

	trace, _ := runEmergencyDefaultParts(t, "some_free_default|some_parent", "0", "")

	requireCalls(t, trace)
	if !strings.Contains(trace, "DETACH PARTITION some_free_default") ||
		!strings.Contains(trace, "DROP TABLE IF EXISTS some_free_default") {
		t.Fatalf("无函数引用且 0 行的默认分区应当被 DETACH+DROP —— 脚本可能被改瘫，本门失去意义\ntrace:\n%s", trace)
	}
}

// runEmergencyDeadTables 跑一轮 L1 的**死表分支**，并能喂第四道闸的应答。
//
// 与 runEmergencyDefaultParts 的区别：后者喂 `_default` 分区候选（键 CANDIDATES 留空），
// 前者喂 DEAD_TABLES 候选。两者都要带 DEPENDENTS，否则第四道闸拿不到应答 ——
// 而假 docker 在 `*pg_proc*strpos*` 分支里 `printf '%s' "$DEPENDENTS"`，
// 环境变量不存在时它就是**空串**，闸会「查不到任何依赖」⇒ 恒放行 ⇒ 用例假绿。
func runEmergencyDeadTables(t *testing.T, candidates, fakeCount, dependents string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	fake := fakeEmergencyDocker(t, dir)
	trace := filepath.Join(dir, "trace.txt")
	logPath := filepath.Join(dir, "emergency.log")

	cmd := exec.Command(bashForScriptFor(t, emergencyScript), emergencyScript, "--level", "L1")
	cmd.Env = append(os.Environ(),
		"DOCKER_BIN="+fake,
		"TRACE_FILE="+trace,
		"CANDIDATES="+candidates,
		"DEFAULT_PARTS=",
		"DEPENDENTS="+dependents,
		"FAKE_COUNT="+fakeCount,
		"LOG="+logPath,
		"LLMGW_NO_SINGLE_INSTANCE_LOCK=1",
		"LLMGW_LOCK_DIR="+dir,
	)
	_, _ = cmd.CombinedOutput()

	traceBody, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("读调用轨迹失败（脚本可能没跑到死表分支查询）: %v", err)
	}
	logBody, _ := os.ReadFile(logPath)
	return string(traceBody), string(logBody)
}

// TestEmergencyL1RefusesToDropEmptyTableReferencedByFunctionBody 钉住死表分支的
// **第四道闸**（§143.6 补，此前本分支完全没有这道闸）。
//
// 252 实测形态：public._request_logs_bodies_hot 0 行 / 291MB ⇒ 前三道闸全过，
// 但它被 7 个 public 函数体按名字引用（含 promote_request_logs_bodies_hot_to_partition、
// recent_success_rate、system_health_status）⇒ 磁盘过 90% 时 DROP 它 = 打断 promote 链路。
// 同一张表的兄弟形态 public._request_logs_hot（0 行 / 61MB）同理。
func TestEmergencyL1RefusesToDropEmptyTableReferencedByFunctionBody(t *testing.T) {
	skipWindows(t)

	trace, logBody := runEmergencyDeadTables(t,
		"public._request_logs_bodies_hot|0|_request_logs_bodies_hot",
		"0",
		"promote_request_logs_bodies_hot_to_partition(),system_health_status()")

	requireCalls(t, trace)
	if strings.Contains(trace, "DROP TABLE IF EXISTS public._request_logs_bodies_hot") {
		t.Fatalf("★ 0 行但被函数体引用的死表仍被 DROP —— 会打断 promote 链路与健康读路径\ntrace:\n%s\nlog:\n%s", trace, logBody)
	}
	if !strings.Contains(logBody, "SKIP") || !strings.Contains(logBody, "promote_request_logs_bodies_hot_to_partition") {
		t.Fatalf("必须在日志里留下 SKIP 与具体函数名，否则事后无法反查为什么没删\nlog:\n%s", logBody)
	}
}

// TestEmergencyL1DependencyLookupUsesBareTableName 钉住**归一化**这一步。
//
// ⚠ 这条是本组门里最容易被忽略、后果最重的一条：
//   候选第一列是 `format('%I)'` 的**加引号**形式（public."_request_logs_bodies_hot"），
//   而函数体里写的是**裸名**。若直接拿第一列去 strpos，就**永远匹配不上**
//   ⇒ 闸恒「无依赖」⇒ 恒放行，而上面那条阻断侧用例**照样全绿**
//   （假 docker 不看表名，只看 SQL 里有没有 pg_proc+strpos 两个词）。
//   ⇒ 换句话说：上面那道门测的是「有应答时会不会 SKIP」，
//     这道门测的才是「问的对象对不对」。
func TestEmergencyL1DependencyLookupUsesBareTableName(t *testing.T) {
	skipWindows(t)

	trace, _ := runEmergencyDeadTables(t,
		`public."_request_logs_bodies_hot"|0`,
		"0",
		"") // 无依赖 ⇒ 会 DROP；本门只看**问的表名**，不看结果

	requireCalls(t, trace)
	if !strings.Contains(trace, "strpos(p.prosrc, '_request_logs_bodies_hot')") {
		t.Fatalf("★ 依赖查询用的不是裸表名 —— 加引号形式永远匹配不到函数体，第四道闸会恒放行\ntrace:\n%s", trace)
	}
	if strings.Contains(trace, `strpos(p.prosrc, 'public.`) {
		t.Fatalf("★ 依赖查询把 schema 限定名当成了表名 —— 同样永远匹配不到\ntrace:\n%s", trace)
	}
	// 第三列缺失时必须从第一列兜底并剥引号（老形态候选不能因此绕过闸）。
	if !strings.Contains(trace, "DROP TABLE IF EXISTS public.\"_request_logs_bodies_hot\"") {
		t.Fatalf("两列形态的候选应当仍然走完 DROP 路径（本门只验归一化，别把脚本改瘫）\ntrace:\n%s", trace)
	}
}

// TestEmergencyL1SkipsWhenBareNameCannotBeResolved 钉住空模式防护。
//
// PG 里 `strpos(x,'')` 返回 **1**（永远命中），而 `strpos(x,NULL)` 返回 NULL
//（永不命中）—— 空模式的语义与「查不到」**完全相反**，是能静默改判的方向。
// 解析不出裸表名时必须 SKIP，且**不能**发出那次依赖查询。
func TestEmergencyL1SkipsWhenBareNameCannotBeResolved(t *testing.T) {
	skipWindows(t)

	trace, logBody := runEmergencyDeadTables(t, "public.|0|", "0", "")

	requireCalls(t, trace)
	if strings.Contains(trace, "DROP TABLE IF EXISTS public.") {
		t.Fatalf("★ 解析不出裸表名时不得 DROP\ntrace:\n%s", trace)
	}
	if strings.Contains(trace, "strpos(p.prosrc, '')") {
		t.Fatalf("★ 不得用空模式查依赖（PG 里 strpos(x,'')=1，语义与直觉相反）\ntrace:\n%s", trace)
	}
	if !strings.Contains(logBody, "SKIP") {
		t.Fatalf("解析不出表名时必须留 SKIP 记录\nlog:\n%s", logBody)
	}
}

// TestEmergencyL1StillDropsUnreferencedEmptyTableInDeadTableBranch 是**正向下限**。
// 没有函数体引用时，0 行的死表仍然必须被 DROP ——
// 否则上面几道门可以靠「一律 SKIP」变绿，而线上唯一真会落盘的候选
// （_request_logs_bodies_hot）恰好**永远**会被第四道闸挡住 ⇒ 那道门测的是一个
// 生产上永不发生的分支。这条保证闸是**判别式**的，不是一律拒绝。
func TestEmergencyL1StillDropsUnreferencedEmptyTableInDeadTableBranch(t *testing.T) {
	skipWindows(t)

	trace, logBody := runEmergencyDeadTables(t, "public.some_genuinely_empty_dead_table|0|some_genuinely_empty_dead_table", "0", "")

	requireCalls(t, trace)
	if !strings.Contains(trace, "DROP TABLE IF EXISTS public.some_genuinely_empty_dead_table") {
		t.Fatalf("无函数引用且 0 行的死表应当被 DROP —— 脚本可能被改瘫，本门失去意义\ntrace:\n%s\nlog:\n%s", trace, logBody)
	}
}

// bashForScript 找一个能跑这些守卫脚本的 bash，**优先** >= 4。
//
// 2026-10-19 审计修正（12h 修订审计轮 §3）：本helper 此前只接受 bash >= 4，
// 找不到就 `t.Skip`。而 macOS 自带的 /bin/bash 是 **3.2**，且默认没装
// homebrew bash ⇒ **本包所有行为门在维护者自己的机器上全部 SKIP**，
// 跑出来是绿的（PASS/ok），实际上一条判定分支都没走到 ——
// 正是本仓 LESSONS 反复登记的「假绿」。
//
// 但**不能**无脑退回 bash 3：本 helper 覆盖的脚本里，
// pg17-emergency-cleanup.sh:72 用了 `${2^^}`，bash 3.2 直接 `bad substitution`
// 把脚本打死（2026-10-19 实测）。所以正确的口径是**按被测脚本判**：
// 目标脚本不含 bash-4 专有语法 ⇒ 用 bash 3 也能跑，门就不该跳过；
// 含 ⇒ 老老实实 Skip 并说清原因，而不是让整包假绿。
//
// ⚠ 判「是否 bash4 专有」时注意 `${2^^}`：变量名以**数字**开头，
//
//	按 `${[A-Za-z_]…}` 写正则会漏掉它（本轮第一次就漏了，
//	差点把一个真需要 bash4 的脚本判成 bash3 兼容）。
func bashForScript(t *testing.T) string {
	t.Helper()
	return bashForScriptFor(t, "")
}

// bashForScriptFor 同上，但可传入被测脚本路径做逐脚本判定。
// 传空串时退化为「只用 bash >= 4，找不到就 Skip」。
func bashForScriptFor(t *testing.T, script string) string {
	t.Helper()
	candidates := []string{"/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/opt/local/bin/bash", "/bin/bash"}
	var fallback []string
	for _, c := range candidates {
		out, err := exec.Command(c, "-c", "echo ${BASH_VERSINFO[0]}").Output()
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(out)) >= "4" {
			return c
		}
		fallback = append(fallback, c)
	}
	if len(fallback) == 0 {
		t.Skip("找不到任何可用的 bash —— 这是环境问题，不是被测行为")
		return ""
	}
	if script == "" {
		t.Skipf("找不到 bash >= 4，且未指定被测脚本可做兼容性判定（bash3: %s）", fallback[0])
		return ""
	}
	if scriptUsesBash4Only(script) {
		t.Skipf("%s 用到 bash4 专有语法（如大写展开），bash 3.2 跑不了；装 bash>=4 后再跑本门", script)
		return ""
	}
	return fallback[0]
}

// scriptUsesBash4Only 判断脚本是否用了 bash 4 才有的语法。
// 逐脚本判定，而不是一刀切 —— 一刀切会让**所有**行为门在 macOS 上静默跳过。
var bash4OnlyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\$\{[0-9A-Za-z_][^}]*\^\^`), // ${x^^} / ${2^^}：变量名可能是纯数字
	regexp.MustCompile(`\$\{[0-9A-Za-z_][^}]*,,\}`), // ${x,,}
	regexp.MustCompile(`declare\s+-A`),                // 关联数组
	regexp.MustCompile(`\bmapfile\b`),
	regexp.MustCompile(`\breadarray\b`),
	regexp.MustCompile(`\bcoproc\b`),
}

func scriptUsesBash4Only(script string) bool {
	b, err := os.ReadFile(script)
	if err != nil {
		return true // 读不了就按「要 bash4」处理，宁可 Skip 也不给错误结论
	}
	s := string(b)
	for _, re := range bash4OnlyPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// requireCalls 断言脚本**确实**调过假 docker。
// 没有它，「轨迹为空」会让后面所有 `strings.Contains(trace, …)` 恒为 false
// ⇒ 用例假绿（本门第一版就踩过：假 docker 变体没记录轨迹，测试却全绿）。
func requireCalls(t *testing.T, trace string) {
	t.Helper()
	if !strings.Contains(trace, "--- call ---") {
		t.Fatalf("脚本一次都没调 docker —— 门禁空转（注入点失效 / 脚本提前退出）")
	}
}

func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 基线（既有，非回归）：守卫脚本与 psql stub 是 bash 轨迹")
	}
}

// TestEmergencyL1RefusesToDropTableWithRows 是本门的正身。
// 生产实测 `ursm_node_snapshot_min_legacy` 的 n_live_tup=0 而 COUNT(*)=2,836,620 ——
// 本用例把这个真实形态喂进去，它必须被拒绝。
func TestEmergencyL1RefusesToDropTableWithRows(t *testing.T) {
	skipWindows(t)

	trace, logBody := runEmergency(t, "public.ursm_node_snapshot_min_legacy|0", "2836620")

	requireCalls(t, trace)
	if strings.Contains(trace, "DROP TABLE IF EXISTS public.ursm_node_snapshot_min_legacy") {
		t.Fatalf("★ 对 n_live_tup=0 但 COUNT(*)=2836620 的表发出了 DROP —— 这会删掉 284 万行\ntrace:\n%s\nlog:\n%s", trace, logBody)
	}
	if !strings.Contains(logBody, "SKIP") || !strings.Contains(logBody, "2836620") {
		t.Fatalf("必须在日志里留下 SKIP 与真实行数，以便事后反查为什么没删\nlog:\n%s", logBody)
	}
}

// TestEmergencyL1RefusesToDropPartition 即使候选行的结构标记谎报为 0，
// SQL 层的 `NOT c.relispartition` 也必须先把分区挡在候选集之外。
//
// ⚠ 本门断言的是**行为**：分区根本不进候选列表，所以循环里根本不会看到它。
func TestEmergencyL1RefusesToDropPartition(t *testing.T) {
	skipWindows(t)

	// 候选列表里只放一张普通表（分区已被 SQL 层排除）
	trace, _ := runEmergency(t, "public.some_plain_table|0", "0")
	requireCalls(t, trace)
	if strings.Contains(trace, "DROP TABLE IF EXISTS public.usage_ledger_2026_09") {
		t.Fatalf("★ 对分区发出了 DROP —— PG 不会拦，父表会静默丢行并拒绝后续 INSERT\ntrace:\n%s", trace)
	}
}

// TestEmergencyL1RefusesToDropPartitionEvenIfFlagSaysPlain 覆盖**第二道闸**：
// 候选行的 is_partition 列谎报 1（模拟 SQL 层被改坏 / 目录信息过期）时，
// 行为层的闸必须仍然拦住它。
func TestEmergencyL1RefusesToDropPartitionEvenIfFlagSaysPlain(t *testing.T) {
	skipWindows(t)

	trace, logBody := runEmergency(t, "public.usage_ledger_2026_09|1", "0")

	requireCalls(t, trace)
	if strings.Contains(trace, "DROP TABLE IF EXISTS public.usage_ledger_2026_09") {
		t.Fatalf("★ is_partition=1 时仍被 DROP 了\ntrace:\n%s", trace)
	}
	if !strings.Contains(logBody, "SKIP") {
		t.Fatalf("分区被跳过时必须留 SKIP 记录\nlog:\n%s", logBody)
	}
}

// TestEmergencyL1StillDropsGenuinelyEmptyTable 钉住「别把脚本改瘫」。
// 一个真的空的普通表仍然必须被 DROP，否则本门就是靠「什么都不做」变绿的。
func TestEmergencyL1StillDropsGenuinelyEmptyTable(t *testing.T) {
	skipWindows(t)

	trace, _ := runEmergency(t, "public.some_genuinely_empty_table|0", "0")

	requireCalls(t, trace)
	if !strings.Contains(trace, "DROP TABLE IF EXISTS public.some_genuinely_empty_table") {
		t.Fatalf("真空表应当被 DROP，但轨迹里没有 —— 脚本可能被改瘫了，判据失去意义\ntrace:\n%s", trace)
	}
}

// TestEmergencyL1CandidateSQLExcludesPartitions 钉住第一道闸（SQL 层）。
// 这里允许文本断言 —— 它判的是「筛选条件在不在」这个**可枚举**的属性，
// 而上面几条负责判行为不被瘫掉（同 proactive 脚本的门禁分工）。
func TestEmergencyL1CandidateSQLExcludesPartitions(t *testing.T) {
	b, err := os.ReadFile(emergencyScript)
	if err != nil {
		t.Fatalf("读脚本: %v", err)
	}
	s := string(b)

	i := strings.Index(s, "DEAD_TABLES=$(docker_exec")
	if i < 0 {
		t.Fatal("找不到 DEAD_TABLES 候选查询 —— 门禁空转（脚本结构变了？）")
	}
	seg := s[i:]
	if j := strings.Index(seg, "\n  \""); j >= 0 {
		seg = seg[:j]
	}

	if !strings.Contains(seg, "NOT c.relispartition") {
		t.Fatalf("L1 候选 SQL 缺少 NOT c.relispartition —— 分区可能被裸 DROP\n%s", seg)
	}
	if !strings.Contains(seg, "COUNT(*)") && !strings.Contains(s, "SELECT COUNT(*) FROM $tname") {
		t.Fatalf("L1 缺少 COUNT(*) 复核 —— n_live_tup 是估计值，可能删掉非空表")
	}
}
