package ursm_test

// index-reader-census.sh 的性质门（2026-10-18，§82）
//
// 这道工具是为了消灭「手写 SQL 做读方普查」这件事本身：
// §79/§81 两轮的普查都是手写的，**两次都写错了** ——
//   ① 筛选条件 `AND query ~* 'request_id'` 把整族读者挡在外面（§81）；
//   ② `ILIKE '%sessions%'` 把 goal_sessions 的读算成 sessions 的（§81）。
// 而 §82 又发现第三条更根本的限制：**普查窗口与 pg_stat_statements 容量**
// 决定了这份普查**能证明什么**，而前两轮谁都没查、也没写。
//
// 所以本门钉的不是「脚本存在」，而是三件被前两轮漏掉的事：
//   1. 普查**不做列名过滤**（只列碰了这张表的全部形态）；
//   2. 普查窗口与容量**无条件出现在输出里**；
//   3. 「0 条」与「被 TOP_N 截断」都**明说不是「没有读方」**。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const censusScript = "252-monitor/index-reader-census.sh"

func censusPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", censusScript)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("脚本不存在: %v", err)
	}
	return p
}

// runCensus 用假 psql 桩跑普查。stub 完全掌控退出码与每段输出。
func runCensus(t *testing.T, rc int, stub string, args ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-psql")
	body := "#!/bin/sh\ncat >/dev/null <<'__EOFSQL__'\n__EOFSQL__\n"
	if err := os.WriteFile(fake, []byte(body+stub+"\nexit "+itoa(rc)+"\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	argv := append([]string{censusPath(t)}, args...)
	cmd := exec.Command("bash", argv...)
	cmd.Env = append(os.Environ(), "PSQL_CMD="+fake, "LLMGW_NO_SINGLE_INSTANCE_LOCK=1")
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

// censusSource 读脚本原文（做源码级断言用）。
func censusSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(censusPath(t))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	return string(b)
}

// censusExecutableSource 读脚本原文，但把**整行注释**换成空行（行号保持不变）。
//
// ★★ 为什么必须剥注释（2026-10-19，§143.13 实测）：
//   下面那道「裸 $VAR 紧跟全角字符」的门是**源码文本**判据。
//   而「源码文本断言先剥注释」是本仓的老教训（§917 同款：JSDoc 里的示例代码
//   把登记表变成空气门）——
//   本轮给普查器补了一段解释**单引号展开 bug**的注释，
//   注释里写了 `$IDX` 后面紧跟中文 ⇒ 那道门立刻红了。
//   ⇒ 红的是**注释**，不是代码 ⇒ 判据必须只管**可执行部分**。
//   ⇒ 保留行号（注释行替换成空行）⇒ 报错仍指向原来的行。
func censusExecutableSource(t *testing.T) string {
	t.Helper()
	lines := strings.Split(censusSource(t), "\n")
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// TestCensusMustNotFilterByColumnName —— 普查**不允许**按列名筛选语句。
//
// 这条钉的是 §81 的死因：筛选条件 `AND query ~* 'request_id'` 与被删索引的
// 列同名，于是「不用 request_id、但依赖同一张表的另一条索引」的查询被整族漏掉。
// 普查器只允许按**表名**匹配 FROM/JOIN 之后的完整表名。
func TestCensusMustNotFilterByColumnName(t *testing.T) {
	s := censusSource(t)
	for _, bad := range []string{"ILIKE '%request_id%'", "ILIKE '%session_id%'"} {
		if strings.Contains(s, bad) {
			t.Errorf("普查器出现按列名的筛选 %q —— 筛选条件是随手写的，"+
				"而与被删索引同名的列会把另一族读者整族挡掉（§81 实测）。"+
				"只允许按表名匹配。", bad)
		}
	}
	// 必须锚定 FROM/JOIN/UPDATE/INTO 之后的完整表名，而不是子串。
	for _, need := range []string{"(FROM|JOIN|UPDATE|INTO)", "__TBL__"} {
		if !strings.Contains(s, need) {
			t.Errorf("普查器缺少 %q —— 必须锚定 FROM 之后的完整表名；"+
				"`ILIKE '%%sessions%%'` 会把 goal_sessions/chat_sessions 一并算进来（§81 实测）", need)
		}
	}
}

// TestCensusMustReportWindowAndCapacity —— 窗口与容量必须**无条件**进输出。
//
// §82 实测本库窗口只有 8 天 4 小时、track=top 且 4886/5000。
// 前两轮普查（§79/§81）谁都没查这两项，于是「只有 1 条读查询」被当成了
// 「没有读方」——而它只能证明「在这 8 天、进 top-N 的语句里没有」。
func TestCensusMustReportWindowAndCapacity(t *testing.T) {
	s := censusSource(t)
	for _, need := range []string{
		"pg_stat_statements_info",
		"stats_reset",
		"pg_stat_statements.max",
		"pg_stat_statements.track",
	} {
		if !strings.Contains(s, need) {
			t.Errorf("普查输出缺少 %q —— 窗口长度与采集容量决定这份普查**能证明什么**，"+
				"不打印它们等于把「没查到」说成「不存在」（§82）", need)
		}
	}
	stub := `case "$*" in
  *pg_index\ i*) echo 'p|usage_ledger|65126400|327|0|1|ts' ;;
  *pg_stat_statements_info*) echo '2026-10-10 16:11:47|8 days 04:49:00|4886|5000|top' ;;
  *count\(*pg_stat_statements*) echo '14' ;;
  *) echo '91|127431.5|11596.3|WITH deduped ...' ;;
esac`
	_, out := runCensus(t, 0, stub, "idx_usage_ledger_part_ts")
	for _, want := range []string{"2026-10-10 16:11:47", "8 days 04:49:00", "4886", "5000", "track=top"} {
		if !strings.Contains(out, want) {
			t.Errorf("普查输出里看不到 %q —— 读者无法判断这份普查的边界：\n%s", want, out)
		}
	}
}

// TestCensusZeroRowsIsNotNoReaders —— 「0 条」不许被写成「没有读方」。
//
// 这与 pg17-duplicate-index-check.sh 的语义分工是对照着的：
// 那道门里「0 行 ⇒ 健康」是对的（0 对重复索引就是结论）；
// 而**普查**里「0 条 ⇒ 没有读方」是错的（0 条只说明窗口+容量内没查到）。
// 两种语义混起来，就会出现本项目已经踩过的「查不到 ⇒ 记成跳过 ⇒ 报成功」。
func TestCensusZeroRowsIsNotNoReaders(t *testing.T) {
	stub := `case "$*" in
  *pg_index\ i*) echo 'r|routing_health_checks|335872|0|0|1|check_id' ;;
  *pg_stat_statements_info*) echo '2026-10-10 16:11:47|8 days 04:49:00|4886|5000|top' ;;
  *count\(*pg_stat_statements*) echo '0' ;;
  *) true ;;
esac`
	code, out := runCensus(t, 0, stub, "idx_rhc_check_id")
	if code != 0 {
		t.Fatalf("普查成功应 exit 0，实得 %d：\n%s", code, out)
	}
	if !strings.Contains(out, "不是") || !strings.Contains(out, "没有读方") {
		t.Errorf("0 条时必须明说「不是没有读方」：\n%s", out)
	}
}

// TestCensusMustReportTotalNotJustPrinted —— 必须报**总数**，不能只报打印条数。
//
// 只报「打印了 N 条」会被读成「只有 N 种读法」——
// 而「没打印出来」与「不存在」是两件事（TOP_N 截断）。
func TestCensusMustReportTotalNotJustPrinted(t *testing.T) {
	stub := `case "$*" in
  *pg_index\ i*) echo 'p|usage_ledger|65126400|327|0|1|ts' ;;
  *pg_stat_statements_info*) echo '2026-10-10 16:11:47|8 days 04:49:00|4886|5000|top' ;;
  *count\(*pg_stat_statements*) echo '37' ;;
  *) echo '91|127431.5|11596.3|WITH deduped ...' ;;
esac`
	_, out := runCensus(t, 0, stub, "idx_usage_ledger_part_ts", "1")
	if !strings.Contains(out, "共 37 种") {
		t.Errorf("输出必须报语句形态的**总数**（共 37 种），只报打印条数会把截断读成全量：\n%s", out)
	}
	if !strings.Contains(out, "不是全部") {
		t.Errorf("输出必须明说打印的是前 N 种而非全部：\n%s", out)
	}
}

// TestCensusParentIndexUsesCascadeAggregate —— 父表索引必须报级联占用。
//
// 父表索引自身恒为 0 字节（分区父表的正常渲染），直接报父表大小会得到
// 「0 bytes」，读起来像「量具坏了」。idx_scan 同理：父表上恒为空。
func TestCensusParentIndexUsesCascadeAggregate(t *testing.T) {
	s := censusSource(t)
	if !strings.Contains(s, "FROM pg_inherits ch WHERE ch.inhparent = i.indexrelid") {
		t.Error("父表索引的占用必须按 pg_inherits 跨分区聚合 —— 直接报父表大小恒为 0")
	}
	if !strings.Contains(s, "sum(COALESCE(cs.idx_scan, 0))") {
		t.Error("父表索引的 idx_scan 必须跨分区聚合 —— 父表上的 idx_scan 恒为空")
	}
}

// TestCensusMustNotClaimCronMembership —— 本工具**不得**出现在 cron 正典里。
//
// 本目录的命名约定 `pg17-*` / `ursm-*` 等价于「这是一个定时巡检」，
// cron 登记门按这个前缀划候选集。本工具是按需诊断：
// 挂 cron 会每小时打一份语句形态清单，既没有可告警的判据，
// 又会把真正有用的巡检日志轮转掉。
// ⇒ 它的名字必须**不占前缀**（与 browser-*-test.py 同 precedent）。
func TestCensusMustNotClaimCronMembership(t *testing.T) {
	base := filepath.Base(censusScript)
	if strings.HasPrefix(base, "pg17-") || strings.HasPrefix(base, "ursm-") {
		t.Errorf("%q 占了 cron 候选前缀（pg17-/ursm-）却不是定时巡检 —— "+
			"cron 登记门会要求它进 etc.cron.d.pg17，而按需诊断挂 cron 只会轮转掉有用的日志。", base)
	}
	cron, err := os.ReadFile(filepath.Join("..", "252-monitor", "etc.cron.d.pg17"))
	if err != nil {
		t.Fatalf("read cron: %v", err)
	}
	if strings.Contains(string(cron), base) {
		t.Errorf("cron 正典里不该有 %q —— 它是按需诊断，不是巡检", base)
	}
}

// TestCensusNeverWrites —— 本工具纯只读。
func TestCensusNeverWrites(t *testing.T) {
	var code []string
	for _, ln := range strings.Split(censusSource(t), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "--") || strings.HasPrefix(t, "#") {
			continue
		}
		code = append(code, ln)
	}
	s := strings.Join(code, "\n")
	for _, bad := range []string{"DROP INDEX", "drop index", "REINDEX", "VACUUM", "ALTER TABLE"} {
		if strings.Contains(strings.ToUpper(s), strings.ToUpper(bad)) {
			t.Errorf("普查工具的可执行行里出现 %q —— 它是纯只读诊断", bad)
		}
	}
}

// TestCensusSQLIsRealNotJustStubs —— 出货 SQL 必须在真库跑过一次。
//
// ★ 这条门记的是 §82 本轮的真实事故：meta SQL 里
//   `c.relkind || '|' || ...`
// 真库报 `operator is not unique: "char" || unknown`（relkind 是 "char" 类型），
// 而**假 psql 桩对 SQL 照单全收，永远绿**。
// ⇒ 与本仓 `$1` 那条注释是同一个坑，第二次。
//
// 这里没法连真库，所以断言的是**那些真跑出来才暴露过的形状已被修正**：
// relkind 显式 ::text，且四条 SQL 段都在脚本里被真正执行。
func TestCensusSQLIsRealNotJustStubs(t *testing.T) {
	s := censusSource(t)
	if !strings.Contains(s, "c.relkind::text") {
		t.Error("relkind 必须 ::text —— 它的类型是 \"char\"，" +
			"`\"char\" || unknown` 算子不唯一，真库直接报错，而假桩永远绿（§82 实测）")
	}
	if strings.Contains(s, `SELECT c.relkind || `) {
		t.Error("出现未加 ::text 的 relkind 拼接")
	}
	// 四段 SQL：meta / window / list / count
	if n := strings.Count(s, "<<'SQL'"); n != 4 {
		t.Errorf("期望 4 段 SQL（meta/window/list/count），实得 %d 段", n)
	}
	// 四段都要真的被 psql_17 调用（而不是定义了不用）。
	for _, seg := range []string{"SQL=", "SQL_WIN=", "SQL_Q=", "SQL_CNT="} {
		if !strings.Contains(s, seg+"$(") {
			t.Errorf("%s 没有被赋值", seg)
		}
	}
}

// TestCensusBraceQuotesVarsBeforeCJK —— 裸 $VAR 紧跟全角字符会被 bash 吞名。
//
// 实测：`echo "…（relkind=$RELKIND）"` 在 bash 3.2 下报
// `RELKIND?: 未绑定的变量` —— 多字节的 `）` 被并进了变量名。
// 本脚本的输出全是中文，必须一律写成 ${VAR}。
func TestCensusBraceQuotesVarsBeforeCJK(t *testing.T) {
	// ★ 只扫**可执行部分**（整行注释已剥）：注释里写 `$IDX` 后面跟中文
	//   是文档，不是 bash 语法（§143.13 实测：补了一段解释展开 bug 的注释
	//   就把这道门打红了 ⇒ 红的是注释，不是代码）。
	for i, ln := range strings.Split(censusExecutableSource(t), "\n") {
		for j := 0; j+1 < len(ln); j++ {
			if ln[j] != '$' || !isIdentStart(ln[j+1]) {
				continue
			}
			k := j + 1
			for k < len(ln) && isIdentPart(ln[k]) {
				k++
			}
			if k < len(ln) && ln[k] >= 0x80 {
				t.Errorf("第 %d 行：裸 `$%s` 紧跟非 ASCII 字符 %q —— "+
					"bash 会把多字节并进变量名（实测报「未绑定的变量」），必须写 ${%s}",
					i+1, ln[j+1:k], string(ln[k]), ln[j+1:k])
			}
			j = k
		}
	}
}

func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentPart(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}
