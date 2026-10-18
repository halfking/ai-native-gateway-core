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

// runDupIndexRaw 与 runDupIndex 的区别：**不**替桩补 exit。
// 两段判据用同一个 PSQL_CMD 调用两次，而「哪一段该成功、哪一段该失败」
// 正是要测的东西 —— 桩必须自己决定退出码。
func runDupIndexRaw(t *testing.T, stub string, env ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-psql")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"+stub+"\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	e := append([]string{"PSQL_CMD=" + fake}, env...)
	cmd := exec.Command("bash", dupIndexPath(t))
	cmd.Env = append(os.Environ(), e...)
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

// dupIndexSQLSegment 取出某个 heredoc 变量的 SQL 段。
// 变量名不同（SQL / SQL_PREFIX），但两个段都必须过同一批检查。
func dupIndexSQLSegment(t *testing.T, varName string) string {
	t.Helper()
	b, err := os.ReadFile(dupIndexPath(t))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	s := string(b)
	marker := varName + "=$(cat <<'SQL'\n"
	start := strings.Index(s, marker)
	if start < 0 {
		t.Fatalf("找不到 %s 的 SQL heredoc 段，脚本形态变了，请同步本判据", varName)
	}
	start += len(marker)
	end := strings.Index(s[start:], "\nSQL\n)")
	if end < 0 {
		t.Fatalf("找不到 %s 的 heredoc 结束标记，脚本形态变了，请同步本判据", varName)
	}
	return s[start : start+end]
}

// dupIndexSQLAll 返回全部判据段的变量名。
// 加段时忘记同步这条 ⇒ 新段不被检查，所以它本身也要有钉子。
func dupIndexSQLAll(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(dupIndexPath(t))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	var got []string
	for _, ln := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "--") || strings.HasPrefix(t, "#") {
			continue
		}
		if name, ok := strings.CutSuffix(t, "=$(cat <<'SQL'"); ok {
			got = append(got, name)
		}
	}
	return got
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
//
// ★ 这一版第一稿是假绿：它只让桩「什么都不打印」，于是无论阈值有没有
//   被替换进 SQL，第二次都会拿到 exit 0 —— 用例的名字承诺了「阈值被应用」，
//   实测的却只是「桩不说话」。
//   现在改成让桩**自己检查 SQL 文本里有没有那个数字**：脚本是内联占位符的，
//   所以这是可验的。
func TestDupIndexThresholdIsActuallyApplied(t *testing.T) {
	const row = `echo 'session_summaries|a|b|59801600|56184448|116162560|162491|1671095|1|0'`
	stub := `
case "$*" in
  *">= 999999999999::bigint"*) exit 0 ;;
  *) ` + row + `
esac`
	if code, out := runDupIndexRaw(t, stub); code != 1 {
		t.Fatalf("基线：应检出并 exit 1，实得 %d\n%s", code, out)
	}
	// 阈值高于该组合的可回收字节 ⇒ SQL 侧应当不返回这一行。
	code, out := runDupIndexRaw(t, stub, "MIN_PAIR_BYTES=999999999999")
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
	// ★ 必须逐段检查，不能只看第一段。
	//   脚本已扩成两段判据（段1 逐字相同 / 段2 前缀覆盖），
	//   而只查第一段的话，第二段写 $1 是全绿的 —— 这与本判据当初
	//   想防的「默认值从没被执行过」是同一个盲区。
	segs := dupIndexSQLAll(t)
	// 三段：逐字相同 / 前缀覆盖 / 父表前缀覆盖（§79 新增第三段）。
	// 少于 3 段说明形态变了；而下面的检查是**逐段**的，
	// 所以新段自动被覆盖 —— 漏登记新段这件事本身就是本门要防的。
	if len(segs) < 3 {
		t.Fatalf("应有 3 段判据（逐字相同 + 前缀覆盖 + 父表前缀覆盖），实得 %d 段 %v —— "+
			"脚本形态变了，请同步本判据", len(segs), segs)
	}
	for _, name := range segs {
		body := dupIndexSQLSegment(t, name)
		for _, bad := range []string{"$1", "$2", "$3"} {
			if strings.Contains(body, bad) {
				t.Errorf("[%s] SQL 段里出现 %s —— psql -c 不绑定位置参数，真机必然报 "+
					"`there is no parameter %s`。阈值请用 __占位符__ 内联。", name, bad, bad)
			}
		}
		// 阈值必须真的被这一段内联进去，否则调 MIN_PAIR_BYTES 对该段无效。
		if !strings.Contains(body, "__MIN_PAIR_BYTES__") {
			t.Errorf("[%s] SQL 段没有 __MIN_PAIR_BYTES__ 占位符 —— 该段的阈值不可调", name)
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

// ===========================================================================
// 段2「前缀覆盖」（2026-10-18 §78）
//
// 背景：段1 只抓定义逐字相同的两个索引，抓不到「较窄索引是较宽索引的前缀」。
// 生产上最贵的冗余恰恰是不等长的 —— usage_ledger_2026_09/2026_10 的
// request_id_idx（169 MB / 50 MB）两段都看不见。
//
// 本组用例里有两条是**量具钉子**，不是行为用例：
//   · TestPrefixSegmentRebasesOidvectorArrays
//   · TestPrefixSegmentComparesOpclassAndSortOrder
// 理由见各自注释。
// ===========================================================================

// segAwareStub 按段返回不同输出。段靠 SQL 里的 `-- seg: prefix-coverage` 区分。
func segAwareStub(seg1, seg2 string) string {
	return `
case "$*" in
  *"-- seg: prefix-coverage"*) ` + seg2 + " ;;\n  *) " + seg1 + " ;;\nesac\nexit 0"
}

const seg1Row = `echo 'session_summaries|a_pkey|b_uidx|71049216|70418432|141467648|142266|3175406|1|0'`
const seg2Row = `echo 'usage_ledger_2026_09|narrow_idx|wide_idx|176807936|196165632|372973568|49|0|0|1'`

// 只有段2 有命中时也必须 exit 1 —— 这条证明段2 真的被调用了，
// 而不是「写了但没接上」的死代码。
func TestPrefixSegmentIsActuallyWired(t *testing.T) {
	code, out := runDupIndexRaw(t, segAwareStub("true", seg2Row))
	if code != 1 {
		t.Fatalf("段2 单独命中应 exit 1，实得 %d —— 段2 可能根本没被调用：\n%s", code, out)
	}
	for _, want := range []string{"narrow_idx", "wide_idx", "前缀覆盖"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q：\n%s", want, out)
		}
	}
}

// 反向：只有段1 有命中时也要 exit 1（防止段2 的失败把段1 的结果吞掉）。
func TestIdenticalSegmentStillReportedAlongsidePrefix(t *testing.T) {
	code, out := runDupIndexRaw(t, segAwareStub(seg1Row, "true"))
	if code != 1 {
		t.Fatalf("仅段1 命中应 exit 1，实得 %d：\n%s", code, out)
	}
	if !strings.Contains(out, "a_pkey") {
		t.Errorf("段1 的命中丢了：\n%s", out)
	}
}

// 两段各自命中时要分段报数。
//
// ★ 桩必须用 segAwareStub3（不是旧的两段版）：脚本已是三段，
//   两段桩里未匹配的段会落进 `*)` 分支，于是「段1 命中」被重复计入，
//   断言的合计数自然对不上。这不是判据变严，是**桩没跟上形态**。
func TestBothSegmentCountsAreReportedSeparately(t *testing.T) {
	code, out := runDupIndexRaw(t, segAwareStub3(seg1Row, seg2Row, "true"))
	if code != 1 {
		t.Fatalf("两段各一对应 exit 1，实得 %d：\n%s", code, out)
	}
	for _, want := range []string{"段1/逐字相同 1 对", "段2/前缀覆盖 1 对", "检出 2 对"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q —— 读者无法判断哪一类命中了几对：\n%s", want, out)
		}
	}
}

// ★★ 量具钉子：段2 的失败必须真的中止整个脚本。
//
// 这条钉的是**调用点**的 `|| exit 3`，不是函数里的 return 还是 exit。
// 我写脚本时先认为「$(...) 里 exit 只结束子 shell、return 才传得出去」，
// 变异验证把它推翻：实测两者在命令替换里完全等价，退出码都传得回父脚本。
// 真正的风险是调用点漏掉 `|| exit 3` —— 那时 3 没人看，
// 段1 那一对的命中还会照样出报告 ⇒ exit 1，看起来完全正常。
func TestSegmentCallSiteMustCheckReturnCode(t *testing.T) {
	b, err := os.ReadFile(dupIndexPath(t))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`out1=$(run_segment "段1/逐字相同" "$SQL") || exit 3`,
		`out2=$(run_segment "段2/前缀覆盖" "$SQL_PREFIX") || exit 3`,
		`out3=$(run_segment "段3/父表前缀覆盖" "$SQL_PARENT") || exit 3`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("段调用点缺少 %q —— 不检查 run_segment 的返回码，"+
				"该段量具不可用时会被吞成「没查到」，而脚本照样拿另一段的结果当结论", want)
		}
	}
}

// 段2 的 psql 失败必须 exit 3，绝不能退化成「0 行 ⇒ 健康」。
//
// 桩的形态是**段1 成功、段2 失败**：如果失败被吞掉，
// 段1 那一对仍然会被检出 ⇒ exit 1，看起来完全正常。
func TestPrefixSegmentFailureIsNoConclusionNotSilentlyHealthy(t *testing.T) {
	stub := `
case "$*" in
  *"-- seg: prefix-coverage"*) echo 'FATAL: server closed the connection' >&2; exit 1 ;;
  *) ` + seg1Row + " ;;\nesac"
	code, out := runDupIndexRaw(t, stub)
	if code != 3 {
		t.Fatalf("段2 psql 失败应 exit 3（没有结论），实得 %d —— "+
			"若为 1/0 就是「段2 崩了却拿段1 的结果当结论」：\n%s", code, out)
	}
	if !strings.Contains(out, "不报健康") {
		t.Errorf("失败输出应明说「不报健康」：\n%s", out)
	}
	// 段1 的命中绝不能出现在报告里：出了结论就别再摆旧数据。
	if strings.Contains(out, "a_pkey") {
		t.Errorf("段2 失败时不该输出段1 的命中行 —— 混合结论比没有结论更危险：\n%s", out)
	}
}

// 段2 输出不合 10 字段契约时同样必须 exit 3。
func TestPrefixSegmentWrongFieldCountAborts(t *testing.T) {
	code, out := runDupIndexRaw(t, segAwareStub("true", "echo 'only|three|fields'"))
	if code != 3 {
		t.Fatalf("段2 字段数不合契约应 exit 3，实得 %d：\n%s", code, out)
	}
	if !strings.Contains(out, "10 个字段") {
		t.Errorf("应明说期望 10 个字段：\n%s", out)
	}
}

// ★★ 量具钉子：oidvector 的下界陷阱。
//
// pg_index.indkey/indoption/indclass/indcollation 是 int2vector/oidvector，
// 下界为 0；`::int2[]` 只换元素类型、**不换下界**。于是
// (indkey::int2[])[1:n] 取到的是第 **2..n** 个元素（实测）。
//
// 后果不是报错，是**静默取错列**：前缀比较恒为假 ⇒ 段2 永远 0 行，
// 而 0 行在本脚本是健康结论 ⇒ 量具坏了反而报健康。
// 连带第二个后果：INCLUDE 的下标区间少截了最后一个列（实测 5 个只剩 4 个）。
//
// 所以判据钉的是**那对重新定基函数**，不是「结果对不对」——
// 后者在假 psql 桩下永远绿。
func TestPrefixSegmentRebasesOidvectorArrays(t *testing.T) {
	body := dupIndexSQLSegment(t, "SQL_PREFIX")
	for _, vec := range []string{"indkey", "indoption", "indclass", "indcollation"} {
		rewritten := "string_to_array(array_to_string(i." + vec
		if !strings.Contains(body, rewritten) {
			t.Errorf("段2 的 %s 没有用 string_to_array(array_to_array(...)) 重新定基 —— "+
				"%s 是 vector 类型、下界为 0，`::[]` 不换下界，切片会**静默取错列**，"+
				"结果是段2 恒 0 行而被当成健康结论。", vec, vec)
		}
	}
	// 反向钉子：禁止出现裸切片形式。
	for _, bad := range []string{"(i.indkey::int2[])[", "(i.indcollation::oid[])["} {
		if strings.Contains(body, bad) {
			t.Errorf("段2 出现裸 vector 切片 %q —— 它取到的是第 2..n 个元素，不是第 1..n 个", bad)
		}
	}
}

// ★★ 量具钉子：只比列名会造假阳性（实测命中过一对真非冗余的索引）。
//
//   request_logs_2026_10_client_model_idx      — (client_model)
//   request_logs_2026_10_client_model_idx2     — (client_model text_pattern_ops)
//
// text_pattern_ops 服务 LIKE 'x%'，默认 opclass 服务 =/</排序，
// 是**两种索引**，删一个就会让一类查询走全表扫。
func TestPrefixSegmentComparesOpclassAndSortOrder(t *testing.T) {
	body := dupIndexSQLSegment(t, "SQL_PREFIX")
	for _, need := range []string{
		"(b.keys)[1:a.indnkeyatts] = a.keys",
		"(b.opts)[1:a.indnkeyatts] = a.opts",
		"(b.opl)[1:a.indnkeyatts] = a.opl",
		"(b.colls)[1:a.indnkeyatts] = a.colls",
	} {
		if !strings.Contains(body, need) {
			t.Errorf("段2 缺少前缀判据 %q —— 少比 opclass 会把 text_pattern_ops 误判成冗余，"+
				"少比 opts 会把 `ts` 与 `ts DESC` 误判成覆盖", need)
		}
	}
	// INCLUDE 列不进索引主体，必须单独比一次，否则
	// 「a 的 INCLUDE 被 b 覆盖」判不出来。
	if !strings.Contains(body, "<@ (b.keys || COALESCE(b.incs") {
		t.Error("段2 没有比对 a 的 INCLUDE 列 —— btree 的 INCLUDE 列不参与前缀判定，须单独比")
	}
	// 唯一性：a 唯一而 b 不唯一时 b 兜不住，删 a 会丢约束语义。
	if !strings.Contains(body, "NOT (a.indisunique AND NOT b.indisunique)") {
		t.Error("段2 缺少唯一性方向约束 —— 唯一索引不能被非唯一索引覆盖")
	}
}

// 两段必须**结构上互斥**，否则同一对会报两次，把「14 对」说成「18 对」。
// 段1 覆盖「键数相等」，所以段2 必须要求严格小于。
func TestPrefixSegmentRequiresStrictlyFewerKeys(t *testing.T) {
	body := dupIndexSQLSegment(t, "SQL_PREFIX")
	if !strings.Contains(body, "a.indnkeyatts < b.indnkeyatts") {
		t.Error("段2 必须要求 a.indnkeyatts < b.indnkeyatts（严格小于）—— " +
			"写成 <= 会与段1 双报同一对")
	}
}

// 段2 的阈值必须真的生效。让桩自己检查 SQL 文本里有没有那个数字 ——
// 见 TestDupIndexThresholdIsActuallyApplied 里「第一稿是假绿」的说明。
func TestPrefixSegmentThresholdIsActuallyApplied(t *testing.T) {
	stub := `
case "$*" in
  *"-- seg: prefix-coverage"*)
    case "$*" in
      *">= 999999999999::bigint"*) exit 0 ;;
    esac
    ` + seg2Row + `
    ;;
  *) exit 0 ;;
esac`
	if code, out := runDupIndexRaw(t, stub); code != 1 {
		t.Fatalf("基线：段2 应检出并 exit 1，实得 %d\n%s", code, out)
	}
	code, out := runDupIndexRaw(t, stub, "MIN_PAIR_BYTES=999999999999")
	if code != 0 {
		t.Fatalf("阈值调高后应无命中并 exit 0，实得 %d：\n%s", code, out)
	}
}

// 报告必须明说「被覆盖 ≠ 该删」：planner 按字节偏好更小的索引，
// 删掉窄索引会让点查改走更大的宽索引。§78 已把这个表述从
// 「可证明冗余」降级过一次，这里钉住降级后的措辞。
func TestPrefixReportWarnsCoveredIsNotSameAsDroppable(t *testing.T) {
	_, out := runDupIndexRaw(t, segAwareStub("true", seg2Row))
	for _, want := range []string{"不等于", "planner"} {
		if !strings.Contains(out, want) {
			t.Errorf("段2 的报告应保留「被覆盖不等于该删 / planner 按字节偏好更小索引」的提醒，"+
				"缺 %q：\n%s", want, out)
		}
	}
}

// TestPrefixReportWarnsCoverageIsNotCostEquivalence —— §84 的口径必须留在报告里。
//
// 「功能覆盖」≠「成本等价」：(snapshot_ts) 是 (snapshot_ts, tenant_id, …)
// 的严格前缀，段2 会把它报成「可删」；但它**窄 4–5×**，
// 只按前缀做范围扫描的读方（如健康巡检主判据）恰恰靠它省下 4/5 的索引字节。
//
// ⇒ 报告若只说「被覆盖不等于该删（planner 偏好小索引）」，
// 读者会得出**相反**的结论（既然 planner 偏好小的，那就该留小的、删大的）。
// 必须明写「窄侧明显更窄时它恰恰不该被删」。
func TestPrefixReportWarnsCoverageIsNotCostEquivalence(t *testing.T) {
	_, out := runDupIndexRaw(t, segAwareStub("true", seg2Row))
	for _, want := range []string{"成本等价", "更窄", "A < B/2"} {
		if !strings.Contains(out, want) {
			t.Errorf("报告应写明「功能覆盖 ≠ 成本等价」并给出可判定的门槛（A < B/2），"+
				"缺 %q —— 否则读者会以为「planner 偏好小索引」等于「该删小索引」，"+
				"与 §84 实测相反：\n%s", want, out)
		}
	}
}

// ===========================================================================
// 段3「父表前缀覆盖」（2026-10-18 §79）
//
// 段2 报的「分区上有 168 MiB 的窄索引」有个致命附加条件：
// CREATE TABLE … PARTITION OF 会克隆父表声明的每个索引。
// 段1/段2 都写死 relkind 'r'/'m' + 索引 'i' ⇒ **看不到父表**，
// 于是「删了会不会长回来」它答不出来。
// §79 实测：只删分区实例是无效的，真实代价从一次性 345 MiB 变成
// 每月 ≈243 MiB ≈ 2.9 GB/年。段3 就是让工具自己答这个问题。
// ===========================================================================

const seg3Marker = "-- seg: parent-prefix-coverage"

// segAwareStub3 三段感知的桩。段靠 SQL 里的 seg 注释区分。
func segAwareStub3(seg1, seg2, seg3 string) string {
	return `
case "$*" in
  *"` + seg3Marker + `"*) ` + seg3 + " ;;\n" +
		`  *"-- seg: prefix-coverage"*) ` + seg2 + " ;;\n" +
		`  *) ` + seg1 + " ;;\nesac\nexit 0"
}

const seg3Row = `echo 'usage_ledger|idx_usage_ledger_part_request_id|usage_ledger_partitioned_request_id_ts_key|229564416|255205376|484769792|100|0|0|1'`

// 只有段3 有命中时也必须 exit 1 —— 证明段3 真的被调用，不是死代码。
func TestParentSegmentIsActuallyWired(t *testing.T) {
	code, out := runDupIndexRaw(t, segAwareStub3("true", "true", seg3Row))
	if code != 1 {
		t.Fatalf("段3 单独命中应 exit 1，实得 %d —— 段3 可能根本没被调用：\n%s", code, out)
	}
	for _, want := range []string{"idx_usage_ledger_part_request_id", "父表前缀覆盖"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q：\n%s", want, out)
		}
	}
}

// 三段都要算进合计，并且分段报数。
func TestAllThreeSegmentCountsAreReported(t *testing.T) {
	code, out := runDupIndexRaw(t, segAwareStub3(seg1Row, seg2Row, seg3Row))
	if code != 1 {
		t.Fatalf("三段各一对应 exit 1，实得 %d：\n%s", code, out)
	}
	for _, want := range []string{
		"段1/逐字相同 1 对", "段2/前缀覆盖 1 对", "段3/父表前缀覆盖 1 对", "检出 3 对",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q —— 读者无法判断哪一类命中了几对：\n%s", want, out)
		}
	}
}

// 段3 的 psql 失败必须 exit 3。形态是**段1/段2 成功、段3 失败**：
// 失败若被吞掉，前两段的命中仍会出报告 ⇒ exit 1，看起来完全正常。
func TestParentSegmentFailureIsNoConclusionNotSilentlyHealthy(t *testing.T) {
	stub := `
case "$*" in
  *"` + seg3Marker + `"*) echo 'FATAL: connection refused' >&2; exit 1 ;;
  *"-- seg: prefix-coverage"*) ` + seg2Row + " ;;\n" +
		`  *) ` + seg1Row + " ;;\nesac"
	code, out := runDupIndexRaw(t, stub)
	if code != 3 {
		t.Fatalf("段3 失败应 exit 3（没有结论），实得 %d —— 若为 1 就是"+
			"「段3 崩了却拿段1/段2 的结果当结论」：\n%s", code, out)
	}
	if strings.Contains(out, "summaries_pkey") {
		t.Errorf("段3 失败时不该输出段1 的命中行 —— 混合结论比没有结论更危险：\n%s", out)
	}
}

// ★★ 量具钉子：段3 的 relkind 大小写。
//
// 分区索引的 pg_class.relkind 是 **'I'（大写）**，普通索引是 'i'。
// 写错成 'i' ⇒ 段3 恒 0 行，而 0 行在本脚本是**健康结论**
// ⇒ 量具坏了反而报健康。§79 实测踩过一次（我先归因成 pg_am 匹配不上）。
func TestParentSegmentUsesUppercaseRelkind(t *testing.T) {
	body := dupIndexSQLSegment(t, "SQL_PARENT")
	if !strings.Contains(body, "c.relkind = 'p' AND ic.relkind = 'I'") {
		t.Error("段3 必须扫 relkind='p' 的父表 + relkind='I' 的**分区索引**（大写 I）—— " +
			"写成 'i' 会让段3 恒 0 行，而 0 行在本脚本被判成健康")
	}
	// 段1/段2 看的仍是普通表与普通索引，不能被一起改掉。
	for _, seg := range []string{"SQL", "SQL_PREFIX"} {
		b := dupIndexSQLSegment(t, seg)
		if !strings.Contains(b, "c.relkind IN ('r','m') AND ic.relkind = 'i'") {
			t.Errorf("[%s] 应保持扫普通表 + 普通索引（relkind 'r'/'m' 与 'i'）", seg)
		}
	}
}

// ★★ 量具钉子：段3 必须报**跨分区级联占用**，不是父表索引的 0 字节。
//
// 父表上的索引恒为 0 bytes —— 那是分区父表的正常渲染。
// 若直接用 pg_relation_size(父表索引)，段3 只能报「有个 0 字节的冗余」，
// 没有任何处置价值；必须按 pg_inherits 聚合各分区实例。
// 实测锚点：聚合计数与手工求和逐位吻合（219 / 62 / 24 MB）。
func TestParentSegmentAggregatesCascadedBytes(t *testing.T) {
	body := dupIndexSQLSegment(t, "SQL_PARENT")
	if !strings.Contains(body, "FROM pg_inherits ch") {
		t.Error("段3 必须用 pg_inherits 聚合各分区实例 —— 父表索引本身恒为 0 字节")
	}
	if !strings.Contains(body, "ch.inhparent = i.indexrelid") {
		t.Error("段3 的级联聚合必须按 inhparent = 父索引 oid 关联（索引在 pg_inherits 里也有继承关系）")
	}
	if strings.Contains(body, "pg_relation_size(i.indexrelid) AS bytes") {
		t.Error("段3 不能直接报父表索引的大小 —— 那恒为 0，报出来没有处置价值")
	}
	// idx_scan 同样要聚合成跨分区总和：父表上的 idx_scan 恒为空。
	if !strings.Contains(body, "sum(COALESCE(cs.idx_scan, 0))") {
		t.Error("段3 的 idx_scan 必须跨分区聚合 —— 父表上的 idx_scan 恒为空")
	}
}

// 段3 命中的处置方式与段1/段2 不同：必须在**父表**上删。
// 报告若不给这句，操作者会去逐分区 DROP，然后下个月看着它长回来。
func TestParentReportSaysDropOnParent(t *testing.T) {
	_, out := runDupIndexRaw(t, segAwareStub3("true", "true", seg3Row))
	for _, want := range []string{"分区父表", "级联占用", "父表"} {
		if !strings.Contains(out, want) {
			t.Errorf("段3 命中时报告应说明「字节是跨分区级联占用」「要在父表上删」，缺 %q：\n%s", want, out)
		}
	}
}

// 段3 的阈值必须真的进到它自己的 SQL 里。
func TestParentSegmentThresholdIsActuallyApplied(t *testing.T) {
	stub := `
case "$*" in
  *"` + seg3Marker + `"*)
    case "$*" in
      *">= 999999999999::bigint"*) exit 0 ;;
    esac
    ` + seg3Row + `
    ;;
  *) exit 0 ;;
esac`
	if code, out := runDupIndexRaw(t, stub); code != 1 {
		t.Fatalf("基线：段3 应检出并 exit 1，实得 %d\n%s", code, out)
	}
	code, out := runDupIndexRaw(t, stub, "MIN_PAIR_BYTES=999999999999")
	if code != 0 {
		t.Fatalf("阈值调高后段3 应无命中并 exit 0，实得 %d：\n%s", code, out)
	}
}
