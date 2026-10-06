package ursm_test

// pg17-duplicate-index-check.sh 的行为门（2026-10-06，审计 §10.55）
//
// 这道门要钉住的核心是一处**语义差异**：
// 同目录的 ursm-snapshot-payload-bloat.sh 里「0 行 ⇒ exit 3（没有结论）」，
// 因为它需要参照系，0 行意味着量具坏了；
// 而本脚本「0 行 ⇒ exit 0（健康）」，因为 0 对重复索引**就是**结论。
//
// 两种语义一旦混起来，就会出现本项目已经踩过的形态：
// 「查不到 ⇒ 记成跳过 ⇒ exit 0 报成功」。
// 所以下面专门有一条用例把这两个方向都钉住。
//
// 注入方式是脚本的 PSQL_CMD 覆盖点，不改动生产形态。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const dupIndexScript = "252-monitor/pg17-duplicate-index-check.sh"

func dupIndexPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", dupIndexScript)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("脚本不存在: %v", err)
	}
	return p
}

// runDupIndex 用一个假的 psql 顶替，按行返回预设输出。
// 第 2 个返回值是 psql 的退出码，用来模拟「量具不可用」。
func runDupIndex(t *testing.T, psqlRC int, stub string, env ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-psql")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"+stub+"\nexit "+itoa(psqlRC)+"\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	e := append([]string{"PSQL_CMD=" + fake}, env...)
	cmd := exec.Command("bash", dupIndexPath(t))
	cmd.Env = append(os.Environ(), e...)
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// 0 对重复索引 = 健康结论。这是与 bloat 脚本方向相反的关键语义。
func TestDupIndexZeroRowsIsHealthyNotNoConclusion(t *testing.T) {
	code, out := runDupIndex(t, 0, "echo ''")
	if code != 0 {
		t.Fatalf("0 对重复索引应判健康 exit 0，实得 %d\n%s", code, out)
	}
	// 必须明说「0 对是健康结论」，否则读者无法区分它与「查不到」。
	if !strings.Contains(out, "健康结论") {
		t.Errorf("输出应明说 0 对是健康结论，否则与「没有结论」不可区分：\n%s", out)
	}
}

// 反向：psql 失败必须 exit 3，绝不能退化成 exit 0 报健康。
func TestDupIndexPsqlFailureIsNoConclusionNotHealthy(t *testing.T) {
	code, out := runDupIndex(t, 1, "echo 'FATAL: connection refused' >&2")
	if code != 3 {
		t.Fatalf("psql 失败应 exit 3（没有结论），实得 %d —— 若为 0 就是「查不到却报健康」\n%s", code, out)
	}
	if !strings.Contains(out, "不报健康") {
		t.Errorf("失败输出应明说「不报健康」：\n%s", out)
	}
}

func TestDupIndexFindsPairExitsOne(t *testing.T) {
	stub := "echo 'session_summaries|summaries_pkey|summaries_session_key_uidx|59801600|56184448|116162560|162491|1671095|1|0'"
	code, out := runDupIndex(t, 0, stub)
	if code != 1 {
		t.Fatalf("检出重复索引应 exit 1，实得 %d\n%s", code, out)
	}
	for _, want := range []string{"session_summaries", "summaries_pkey", "summaries_session_key_uidx"} {
		if !strings.Contains(out, want) {
			t.Errorf("诊断输出缺少 %q：\n%s", want, out)
		}
	}
}

// ★ 字段数不合契约时必须 exit 3。
// psql 有可能在 rc=0 的情况下吐出非预期行（例如 psql 自己的提示行），
// 那种情况不能当成「没有重复索引」。
func TestDupIndexWrongFieldCountAborts(t *testing.T) {
	code, out := runDupIndex(t, 0, "echo 'only|three|fields'")
	if code != 3 {
		t.Fatalf("字段数不合契约应 exit 3，实得 %d\n%s", code, out)
	}
	if !strings.Contains(out, "10 个字段") {
		t.Errorf("应明说期望 10 个字段：\n%s", out)
	}
}

// 阈值必须真的进到 SQL 里：把 MIN_PAIR_BYTES 调到极大值，0 对才成立。
// 若阈值没有生效（写死或没传参），下面这条会拿到 exit 1。
func TestDupIndexThresholdIsActuallyApplied(t *testing.T) {
	stub := "echo 'session_summaries|a|b|59801600|56184448|116162560|162491|1671095|1|0'"
	if code, _ := runDupIndex(t, 0, stub); code != 1 {
		t.Fatalf("基线：应检出并 exit 1，实得 %d", code)
	}
	// 阈值高于该组合的可回收字节 ⇒ SQL 侧应当不返回这一行。
	code, out := runDupIndex(t, 0, "true", "MIN_PAIR_BYTES=999999999999")
	if code != 0 {
		t.Fatalf("阈值调高后应无命中并 exit 0，实得 %d\n%s", code, out)
	}
}

// 脚本必须声明自己「只报告不 DROP」。
// 一个会自动删索引的巡检脚本是危险品，判据里要有钉子。
func TestDupIndexScriptNeverDrops(t *testing.T) {
	b, err := os.ReadFile(dupIndexPath(t))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	// 只看**可执行行**：整行注释（shell 的 -- / #，以及 heredoc 里的 SQL --）
	// 不参与判定。注释里解释「为什么不能删」是必要的文档，
	// 把它也算成「脚本会删索引」会让这道门变成必须删掉注释的钝门。
	// 注意这是**失败方向偏保守**的近似：行中注释（`SELECT 1; -- DROP INDEX`）
	// 仍会被扫到 ⇒ 宁可误报，不会漏报。
	var code []string
	for _, ln := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "--") || strings.HasPrefix(t, "#") {
			continue
		}
		code = append(code, ln)
	}
	s := strings.Join(code, "\n")
	for _, bad := range []string{"DROP INDEX", "drop index", "REINDEX"} {
		if strings.Contains(s, bad) {
			t.Errorf("巡检脚本的可执行行里出现 %q —— 本脚本只报告，删索引必须人工确认无查询依赖", bad)
		}
	}
	if !strings.Contains(s, "只报告") {
		t.Error("脚本应明写「只报告」")
	}
}

// TestDupIndexSQLHasNoPsqlPositionalParams 防的是一类**默认路径专属**缺陷。
//
// 本脚本第一版把阈值写成 SQL 里的 `$1`。psql -c **不绑定位置参数**
// （那是 \set / -v 的东西），真机报 `there is no parameter $1`。
//
// ★ 为什么仓库里的门没能提前抓住：假 psql 桩对 SQL 内容**照单全收**，
// 而 cron 接线门只断言「脚本存在且有 cron 行」—— 两者都不看 SQL 是否合法。
// 这与 scripts/252-monitor/ursm-snapshot-payload-bloat.sh 注释里记的
// 「默认值从没被执行过」是同一族缺陷，只是这次发生在我自己新写的脚本上。
func TestDupIndexSQLHasNoPsqlPositionalParams(t *testing.T) {
	b, err := os.ReadFile(dupIndexPath(t))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	s := string(b)
	// 只看 heredoc 里的 SQL 段（`$1` 出现在 shell 位置参数里是合法的）。
	sqlStart := strings.Index(s, "SQL=$(cat <<'SQL'")
	sqlEnd := strings.Index(s, "SQL\n)")
	if sqlStart < 0 || sqlEnd < 0 || sqlEnd < sqlStart {
		t.Fatalf("找不到 SQL heredoc 段，脚本形态变了，请同步本判据")
	}
	body := s[sqlStart:sqlEnd]
	for _, bad := range []string{"$1", "$2", "$3"} {
		if strings.Contains(body, bad) {
			t.Errorf("SQL 段里出现 %s —— psql -c 不绑定位置参数，真机必然报 "+
				"`there is no parameter %s`。阈值请用 __占位符__ 内联。", bad, bad)
		}
	}
	// 阈值必须经过非负整数校验后才允许进 SQL。
	if !strings.Contains(s, "MIN_PAIR_BYTES 必须是非负整数") {
		t.Error("缺少 MIN_PAIR_BYTES 的非负整数校验 —— 内联进 SQL 前必须挡住注入与非数字")
	}
}

// TestDupIndexReportCarriesDropDecisionEvidence 钉住报告里**必须有**的两项。
//
// 为什么必须：只报「有两个一样的索引」不足以让人决定删哪个。
// 实测踩过（§10.56）：session_summaries 那一对里，
//
//	session_summaries_pkey            —— 是 PRIMARY KEY，被约束背着，DROP INDEX 会被拒
//	session_summaries_session_key_uidx —— 裸唯一索引，可删，且 idx_scan 高达 167 万
//
// 没有「受约束」列就会去删错的那个（正好是我第一次报的方向）；
// 没有「idx_scan」列就无法区分「无人用的重复」与「正在被高频使用的重复」。
func TestDupIndexReportCarriesDropDecisionEvidence(t *testing.T) {
	b, err := os.ReadFile(dupIndexPath(t))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	s := string(b)
	for _, need := range []string{"idx_scan", "constrained", "pg_constraint", "pg_stat_user_indexes"} {
		if !strings.Contains(s, need) {
			t.Errorf("报告缺少 %q —— 没有它就无法判断哪个索引可裸删、哪个正被使用", need)
		}
	}
	// 受约束计数必须是按 indexrelid 关联的（conindid），不是按表名或按名字。
	if !strings.Contains(s, "c.conindid = i.indexrelid") {
		t.Error("受约束判定必须用 pg_constraint.conindid 关联 indexrelid —— " +
			"按名字/表名关联会判错")
	}

	// ★ 上一版判据只查「全文出现过 idx_scan」，于是删掉 SELECT 列表里的那两列
	//   而 JOIN 与日志文本里还留着词根 ⇒ 门照样绿（M49 实测）。
	//   所以这里必须钉在**SQL 的 SELECT 列表**上，而不是全文。
	sqlStart := strings.Index(s, "SQL=$(cat <<'SQL'")
	sqlEnd := strings.Index(s, "SQL\n)")
	if sqlStart < 0 || sqlEnd < 0 || sqlEnd < sqlStart {
		t.Fatal("找不到 SQL heredoc 段，脚本形态变了，请同步本判据")
	}
	body := s[sqlStart:sqlEnd]
	// 取最后一个顶层 SELECT（输出列在 CTE 之后）。
	sel := body[strings.LastIndex(body, "SELECT c.relname,"):]
	for _, col := range []string{"a.idx_scan", "b.idx_scan", "a.constrained", "b.constrained"} {
		if !strings.Contains(sel, col) {
			t.Errorf("输出列清单缺少 %s —— 它必须真的被 SELECT 出来，"+
				"只出现在 JOIN 或日志文字里不算（这正是 M49 漏掉的那一层）", col)
		}
	}
}

// TestDupIndexReportShowsScanAndConstraintValues 行为级判据：
// 桩里给的 idx_scan 与受约束值必须**真的出现在 stdout**。
//
// 为什么不用「数格式串里的 %s 个数」：那数的是格式串，
// 把参数删掉它也不变 ⇒ 门照样绿（第一版 M50 就这么被骗过去的）。
// 判「值有没有被呈现」只能看值本身。
func TestDupIndexReportShowsScanAndConstraintValues(t *testing.T) {
	stub := "echo 'session_summaries|a_idx|b_idx|59801600|56184448|116162560|162491|1671095|1|0'"
	_, out := runDupIndex(t, 0, stub)
	// 这两个是决策依据：a_idx 受约束=1（不能裸删）、b_idx 扫描 1671095（在被用）。
	for _, want := range []string{"162491", "1671095", "a_idx", "b_idx"} {
		if !strings.Contains(out, want) {
			t.Errorf("报告输出里看不到 %q —— 决策依据没有真正呈现给操作者：\n%s", want, out)
		}
	}
	// 表头也要有这两项的名字，否则操作者看不懂列的含义。
	for _, want := range []string{"扫描", "受约束"} {
		if !strings.Contains(out, want) {
			t.Errorf("表头缺少 %q：\n%s", want, out)
		}
	}
}
