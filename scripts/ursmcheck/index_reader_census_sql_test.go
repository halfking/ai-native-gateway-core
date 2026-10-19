package ursm_test

// index-reader-census.sh 的「**实际下发的 SQL 必须是可执行的真 SQL**」门（2026-10-19，§143.13）
//
// # 这道门补的是什么
//
// 仓里已有 `index_reader_census_test.go`：它用**假 psql 桩**驱动普查器，断言
// 「普查不做列名过滤」「窗口与容量必须进输出」「0 条不等于没有读方」等**性质**。
//
// ⇒ 但那个桩**把 SQL 吞掉了**（`cat >/dev/null`）⇒ 它**结构上不可能**发现
// 「SQL 本身写错了」这一类缺陷。而这恰恰是 2026-10-19 实际发生的：
//
//	SQL=${SQL/__IDX__/'$IDX'}      ← 替换值写在**单引号**里
//	⇒ shell 不展开 $IDX ⇒ SQL 里留下字面量 `$IDX`
//	⇒ 真库报 `syntax error at or near "$"`
//	⇒ 工具**从来没跑起来过**（exit 3「量具不可用」）
//
// ★ 这与该脚本自己注释里记的 `c.relkind::text` 事故是**同族第三次**
//   （假 psql 桩照单全收 ⇒ 只有真库才看得见）。
// ⇒ 既然桩按设计看不见这一类，**就得再加一道能看见的门**。
//
// # 本门怎么做
//
// 用一个**记录 + 转发**的 psql 包装脚本：
//   ① 把工具**实际下发**的 SQL 记到文件里；
//   ② 把它**真的执行**到本机 PG 沙箱（`llm-gateway-pg`，PG 17.10）上。
// ⇒ 工具下发的 SQL 若有语法错，**真库**会报错 ⇒ 门红。
// ⇒ 沙箱不可用时**跳过真库那一段**（并在输出里明说），但**文本断言照跑**：
//   下发的 SQL 必须含**展开后的**索引名，且不得残留 `$IDX` / `__IDX__` 占位符。
//
// # 变异验证
//
//	M1 把 `${SQL/__IDX__/'$IDX'}` 改回单引号   ⇒ ①真库执行红 + ②文本断言红
//	M2 把展开改成漏引号（`=$IDX`）              ⇒ ①真库执行红
//	M3 把占位符换名但忘了展开                    ⇒ ①真库执行红 + ②文本断言红

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// censusSQLSep 是记录文件里的**分隔行**：一次下发的 SQL 前后各夹一个，
// 这样多行 SQL 不会被按行切碎。
const censusSQLSep = "--__M__CENSUS_SQL__M__--"

// censusSQLRecorder 是「记录 + 转发」的 psql 包装脚本。
//
// ★ 它转发的目标是**本机 PG 沙箱**：普查器的第一条 SQL 只读 pg_catalog
//   （pg_index / pg_class / pg_inherits / pg_stat_user_indexes），
//   在任何 PG 上都能执行 ⇒ **不需要造任何夹具**。
//   而「能不能执行」正是我们要断言的性质。
//
// ★ 转发用的 psql 参数与工具自己的 PSQL_CMD **刻意不同**（多了 ON_ERROR_STOP）：
//   工具自己的参数是「-v ON_ERROR_STOP=1 -X -q -A -t -F'|'」，
//   而这里要的是「**语法错必须以非零退出码失败**」——
//   少了 ON_ERROR_STOP 时 psql 对单条 -c 的语法错**仍返回 0**（只打印 ERROR），
//   那这道门就成了空气门。
const censusSQLRecorder = `#!/bin/sh
# 记录工具实际下发的完整 SQL（$1 可能有多行 ⇒ 用分隔行隔开，别按行切）
printf '%s\n%s\n' "$CENSUS_SQL_SEP" "$1" >> "$CENSUS_SQL_LOG"
exec docker exec -i llm-gateway-pg psql -U postgres -d postgres -tAX -v ON_ERROR_STOP=1 -c "$1"
`

// censusSandboxAvailable 报告本机 PG 沙箱是否可用（不可用时真库那一段跳过）。
func censusSandboxAvailable() bool {
	cmd := exec.Command("docker", "exec", "-i", "llm-gateway-pg",
		"psql", "-U", "postgres", "-d", "postgres", "-tAX", "-c", "select 1;")
	out, err := cmd.CombinedOutput()
	return err == nil && strings.Contains(string(out), "1")
}

// runCensusRecording 用记录型 psql 跑一次普查，返回（工具输出, 实际下发的 SQL 列表）。
func runCensusRecording(t *testing.T, idx string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	rec := filepath.Join(dir, "recorder.sh")
	logFile := filepath.Join(dir, "sent.sql")
	if err := os.WriteFile(rec, []byte(censusSQLRecorder), 0o755); err != nil {
		t.Fatalf("写记录脚本: %v", err)
	}
	cmd := exec.Command("bash", censusPath(t), idx)
	cmd.Env = append(os.Environ(),
		"PSQL_CMD="+rec,
		"CENSUS_SQL_LOG="+logFile,
		"CENSUS_SQL_SEP="+censusSQLSep,
		"LLMGW_NO_SINGLE_INSTANCE_LOCK=1",
	)
	out, _ := cmd.CombinedOutput()

	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("读下发 SQL 记录: %v", err)
	}
	// ★ 按分隔行切，**不是按行切**：SQL 本身是多行的。
	var sents []string
	for _, chunk := range strings.Split(string(b), censusSQLSep+"\n") {
		if strings.TrimSpace(chunk) != "" {
			sents = append(sents, strings.TrimRight(chunk, "\n"))
		}
	}
	return string(out), sents
}

// TestCensusSendsExecutableSQL —— 工具**实际下发**的 SQL 必须在真 PG 上能执行。
//
// ★ 这是本门的承重断言：2026-10-19 那次「工具从来没跑起来过」就是它能抓的。
func TestCensusSendsExecutableSQL(t *testing.T) {
	if !censusSandboxAvailable() {
		t.Skip("本机 PG 沙箱（llm-gateway-pg）不可用 ⇒ 跳过真库执行断言；" +
			"文本断言仍由 TestCensusSendsExpandedIndexName 覆盖")
	}
	_, sents := runCensusRecording(t, "session_turns")
	if len(sents) == 0 {
		t.Fatalf("工具一条 SQL 都没下发（记录文件为空）⇒ 判据前提不成立")
	}
	// 第一条是元数据查询（后面才是窗口查询与语句形态查询）。
	first := sents[0]
	if !strings.Contains(first, "relname") {
		t.Fatalf("第一条下发 SQL 不像元数据查询（没有 relname）：\n%s", first)
	}
	cmd := exec.Command("docker", "exec", "-i", "llm-gateway-pg",
		"psql", "-U", "postgres", "-d", "postgres", "-tAX", "-v", "ON_ERROR_STOP=1", "-c", first)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("★ 工具下发的 SQL 在**真库**上执行失败 ⇒ 这正是 §143.13 修的那类缺陷"+
			"（假 psql 桩永远看不见）：\n%s\n--- 工具输出 ---\n%s", out, out)
	}
}

// TestCensusSendsExpandedIndexName —— 下发的 SQL 必须含**展开后**的索引名，
// 且不得残留任何未展开的占位符。
//
// ★ 假 psql 桩按设计看不见这一类（它把 SQL 吞了）⇒ 这条断言是它的替代品。
func TestCensusSendsExpandedIndexName(t *testing.T) {
	const idx = "idx_some_parent_index"
	_, sents := runCensusRecording(t, idx)
	if len(sents) == 0 {
		t.Fatalf("工具一条 SQL 都没下发 ⇒ 判据前提不成立")
	}
	first := sents[0]
	if !strings.Contains(first, "'"+idx+"'") {
		t.Errorf("下发的 SQL 里没有展开后的索引名字面量 '%s' ⇒ 索引名没被正确内联：\n%s", idx, first)
	}
	for _, bad := range []string{"$IDX", "__IDX__", "$TBL", "__TBL__", "$TOPN", "__TOPN__"} {
		if strings.Contains(first, bad) {
			t.Errorf("下发的 SQL 里残留未展开的占位符 %q ⇒ 展开这一环坏了"+
				"（§143.13 实测：写成单引号替换值时，shell 不展开，真库直接语法错）：\n%s", bad, first)
		}
	}
}
