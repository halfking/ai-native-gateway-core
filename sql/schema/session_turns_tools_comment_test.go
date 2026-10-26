package schema

// session_turns_tools_comment_test.go —— **基线里 tools 列注释的同步门**
// （2026-10-20 会话族审计 G-6 / 迁移 889）。
//
// # 这道门防什么
//
// 迁移 889 订正了 `public.session_turns.tools` 的列注释（说明它从 889 起
// 由写路径填充、值来自 session_tools_hot、历史行不回填）。
//
// 但**新装实际用的是第三份基线副本**
// （installer/cmd/llm-gw-installer/embeddata/01-schema.sql）——
// 只改迁移不改基线 ⇒ 一次全新安装出来的库里，这一列仍然挂着 456 的
// 旧注释「Tools invoked during this turn (JSON array of tool_use records).」，
// 而**旧注释描述的是一列从来没人写过的列**（真实形状是摘要数组，
// 不是 tool_use 记录）。
//
// ⇒ 三条路径（存量库迁移 889 / 新装库基线）必须落成**同一句话**。
//
// # 为什么比「字面量逐字相等」而不是「包含关键词」
//
// 判据纪律：只断言「没坏」的判据，在「干脆没做」面前永远是绿的。
// 包含 "session_tools_hot" 三个词的注释可以一边说填充来源一边说
// 「恒为空数组」。这里比的是**整条注释语句的字面量逐字相等**，
// 并额外要求它明确写出「填充」与「历史行不回填」两件事。
//
// ★ 复用同包既有的 baselineCopies / readRepoFile（不重复定义）——
//   复制一份路径常量等于复制一把尺子，两边一起漂移时对照察觉不到。

import (
	"regexp"
	"strings"
	"testing"
)

const migration889File = "../../sql/migrations/startup/889_session_turns_tools_population.sql"

// sessionTurnsToolsCommentRe 抓 `COMMENT ON COLUMN public.session_turns.tools IS '...'`。
var sessionTurnsToolsCommentRe = regexp.MustCompile(`(?is)COMMENT\s+ON\s+COLUMN\s+public\.session_turns\.tools\s+IS\s+('(?:[^']|'')*')\s*;`)

// extractSessionTurnsToolsCommentLiteral 返回该注释语句里的**字面量**
// （含引号，含 SQL 里的 ” 转义）。
func extractSessionTurnsToolsCommentLiteral(t *testing.T, path string) string {
	t.Helper()
	body := readRepoFile(t, path)
	m := sessionTurnsToolsCommentRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("%s 里找不到 `COMMENT ON COLUMN public.session_turns.tools IS '...'` —— "+
			"要么列注释被删了，要么写法漂移（判据的尺子要跟着改）", path)
	}
	return m[1]
}

// TestSessionTurnsToolsBaselineCommentMatchesMigration889 是正题：
// 三份基线副本的 tools 列注释必须与迁移 889 **逐字相同**。
func TestSessionTurnsToolsBaselineCommentMatchesMigration889(t *testing.T) {
	mig := extractSessionTurnsToolsCommentLiteral(t, migration889File)

	for _, path := range baselineCopies {
		got := extractSessionTurnsToolsCommentLiteral(t, path)
		if got != mig {
			t.Errorf("%s 的 tools 列注释与迁移 889 **不逐字相同**：\n--- 迁移 ---\n%s\n--- 本文件 ---\n%s\n"+
				"存量库（新装基线）与新装库（基线副本）必须说同一句话，"+
				"否则同一列在两种环境里对外契约不同",
				path, mig, got)
		}
	}
}

// TestSessionTurnsToolsBaselineCommentStatesTheFillSource 是**正向**断言：
// 注释必须写出「从 889 起被填充」与「填充来源是 session_tools_hot」。
//
// ★ 为什么必须要有这条：只断言「与迁移逐字相同」的话，迁移里那句注释
//
//	被改成「本列恒为空数组」三份基线也会跟着变、门照样全绿 ——
//	门测的是「三份一致」，不是「一致且正确」。这里补上正确性。
func TestSessionTurnsToolsBaselineCommentStatesTheFillSource(t *testing.T) {
	for _, path := range baselineCopies {
		literal := extractSessionTurnsToolsCommentLiteral(t, path)
		if !strings.Contains(literal, "起由写路径填充") {
			t.Errorf("%s 的 tools 列注释没有说明「从 889 起由写路径填充」：\n%s", path, literal)
		}
		if !strings.Contains(literal, "session_tools_hot") {
			t.Errorf("%s 的 tools 列注释没有点明填充来源 session_tools_hot：\n%s", path, literal)
		}
		if !strings.Contains(literal, "不回填") {
			t.Errorf("%s 的 tools 列注释没有说明「历史行不回填」—— "+
				"读注释的人会以为老行也有值：\n%s", path, literal)
		}
	}
}

// TestMigration889ToolsCommentIsNotAppliedToHotOnlyTables 钉住一件事：
// 基线里**没有** session_turns_hot / session_tools / session_tools_hot
// 这三张表（它们由 526/706 迁移建，不在基线快照里）⇒ 889 对它们的列注释
// 订正**只**存在于迁移里，是正常的。
//
// ★ 这条门的作用是「别把三张不在基线里的表误加进基线」——
//
//	新装时基线先建表、迁移后建 hot 表，顺序反了会出「基线里的表缺列」。
//	断言的是**不存在**，比断言「存在」更早发现顺序错误。
func TestMigration889ToolsCommentIsNotAppliedToHotOnlyTables(t *testing.T) {
	for _, path := range baselineCopies {
		body := readRepoFile(t, path)
		for _, table := range []string{"public.session_turns_hot", "public.session_tools", "public.session_tools_hot"} {
			if strings.Contains(body, "CREATE TABLE "+table+" (") {
				t.Errorf("%s 里出现了 `CREATE TABLE %s` —— 这三张表由 526/706 迁移建，"+
					"不该出现在基线快照里（基线先建、迁移后建，顺序反了会缺列）",
					path, table)
			}
		}
	}
}
