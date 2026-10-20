package admin

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestSourceSQLLocalLiteralProof(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		wantDangling bool
	}{
		{"local immutable source", "package admin; func localSourceSQL() string { return `SELECT ts FROM _request_logs` }; var q = localSourceSQL()", false},
		{"comment is not definition", "package admin; // func localSourceSQL() string { return `SELECT ts FROM _request_logs` }\nvar q = localSourceSQL()", true},
		{"parenthesized local call", "package admin; func localSourceSQL() string { return `SELECT ts FROM _request_logs` }; var q = (localSourceSQL)()", false},
		{"parenthesized selector collision", "package admin; func ExportedSourceSQL() string { return `SELECT ts FROM _request_logs` }; var a = ExportedSourceSQL(); var q = (other.ExportedSourceSQL)()", true},
		{"parenthesized shadow after real call", "package admin; func localSourceSQL() string { return `SELECT ts FROM _request_logs` }; var a = localSourceSQL(); func use() { localSourceSQL := func() string { return table }; _ = (localSourceSQL)() }", true},
		{"same-name external selector", "package admin; func ExportedSourceSQL() string { return `SELECT ts FROM _request_logs` }; var q = other.ExportedSourceSQL()", true},
		{"shadowed dynamic closure", "package admin; func localSourceSQL() string { return `SELECT ts FROM _request_logs` }; func use() { localSourceSQL := func() string { return table }; _ = localSourceSQL() }", true},
		{"external source remains unknown", "package admin; var q = other.localSourceSQL()", true},
		{"dynamic source remains unknown", "package admin; func localSourceSQL() string { return table }; var q = localSourceSQL()", true},
		{"local with branch remains unknown", "package admin; func localSourceSQL() string { if enabled { return `SELECT ts FROM _request_logs` }; return `SELECT ts FROM session_turns_hot` }; var q = localSourceSQL()", true},
		{"literal mention is not reader", "package admin; func localSourceSQL() string { return `-- FROM _request_logs` }; var q = localSourceSQL()", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := danglingSourceSQLCalls(t, tc.source)
			if (len(got) > 0) != tc.wantDangling {
				t.Fatalf("dangling = %v, want dangling %v", got, tc.wantDangling)
			}
		})
	}
}

func TestSourceFamilyLocalUnionProof(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		want      string
	}{
		{"real mixed output", "SELECT ts FROM session_turns_hot UNION ALL SELECT ts FROM _request_logs", familySessionBase},
		{"v1 anti-join does not supply session rows", "SELECT ts FROM _request_logs_hot WHERE NOT EXISTS (SELECT 1 FROM session_turns_hot) UNION ALL SELECT ts FROM _request_logs", familyBase},
		{"SQL comment is not session output", "SELECT ts FROM _request_logs_hot -- FROM session_turns_hot\n UNION ALL SELECT ts FROM _request_logs", familyBase},
		{"nested UNION is not output fallback", "SELECT ts FROM _request_logs_hot WHERE EXISTS (SELECT ts FROM session_turns_hot UNION ALL SELECT ts FROM _request_logs)", familyBase},
		{"scalar subquery is not session output", "SELECT (SELECT count(*) FROM session_turns_hot) FROM _request_logs_hot UNION ALL SELECT ts FROM _request_logs", familyBase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code := "package admin; func localSourceSQL() string { return " + strconv.Quote(tc.sql) + " }"
			if got := sourceFamilyOf(code, false); got != tc.want {
				t.Fatalf("family=%s want=%s", got, tc.want)
			}
		})
	}
	for _, code := range []string{
		"package admin; func source() string { return prefix + `SELECT ts FROM session_turns_hot UNION ALL SELECT ts FROM _request_logs` + suffix }",
		"package admin; func source() string { return `SELECT ts FROM session_turns_hot ` + dynamic + ` UNION ALL SELECT ts FROM _request_logs` }",
	} {
		if localSessionBaseUnion(code) {
			t.Fatal("unknown SQL fragment fabricated mixed output proof")
		}
	}
	raw, err := os.ReadFile("tenant_stats_logs_table.go")
	if err != nil {
		t.Fatal(err)
	}
	// ★ 2026-10-20（迁移 890，v1 族真删）：tenant stats 的四臂收敛成两臂
	// （会话 hot ∪ 母表）⇒ 它的族从「混合（会话 + 基表）」变成
	// **「只读会话族」**（familySessionOnly）。
	//
	// ⇒ 判据从「必须是 mixed」**反转**为「必须是 session-only」。
	if got := sourceFamilyForFile(string(raw), "admin/tenant_stats_logs_table.go", false); got != familySessionOnly {
		t.Fatalf("real tenant source family=%s, want session-family-only", got)
	}
	// ★ 分类器只读文本；这里再补一条**编译产物级**的证明：两个输入态
	// （writeEnabled 真/假）都必须编译出「会话族两腿、无任何 v1 腿」的
	// UNION —— 这才是「它真的只读会话族」的硬证据，而不是「文本里出现过
	// session_turns」。
	for _, enabled := range []bool{false, true} {
		query := tenantStatsLogsTableSQL(enabled)
		literal := "package admin; func source() string { return " + strconv.Quote(query) + " }"
		if !localSessionOnlyUnion(literal) {
			t.Errorf("tenant stats SQL（writeEnabled=%v）不是「只有会话族两腿」的 UNION：\n%s", enabled, query)
		}
		if hits := retiredV1RelationsInSQL(query); len(hits) > 0 {
			t.Errorf("tenant stats SQL（writeEnabled=%v）仍引用已退役 v1 族关系名 %v —— v1 族已随 890 被 DROP：\n%s",
				enabled, hits, query)
		}
	}
	// 就地阳性对照：同一把尺子必须能认出 v1 形态（否则上面那条恒绿）。
	if hits := retiredV1RelationsInSQL("FROM _request_logs p"); len(hits) == 0 {
		t.Error("retiredV1RelationsInSQL 认不出 v1 形态 —— 负向断言已恒绿，判据坏了")
	}
}

func TestSourceFamilyProofIsFileScoped(t *testing.T) {
	raw, err := os.ReadFile("tenant_stats_logs_table.go")
	if err != nil {
		t.Fatal(err)
	}
	// ★ 2026-10-20（迁移 890）：**按路径的证明已退役**（见
	// sourceFamilyForFile 的头注）⇒ 这道门的形态从「路径相关的证明不许
	// 泄漏」换成「**分类不得依赖路径**」。
	//
	// 原来守的是：tenant stats 的「混合」证明只对这个文件生效，同样内容
	// 放到别的路径下不得拿到那一档。现在没有按路径的证明了 ⇒ 等价且仍
	// 成立的不变式是：**同一段源码在不同路径下必须得到同一个族**。
	//
	// ⚠ 这条断言只有在「存在路径相关的暗箱」时才会红 ⇒ 必须配阳性对照
	// （下面的 v1 形态片段：它在**任何**路径下都必须被判成基表族）。
	base := sourceFamilyOf(string(raw), false)
	for _, path := range []string{"admin/other.go", "admin/tenant_stats_logs_table.go", "bg/x.go"} {
		if got := sourceFamilyForFile(string(raw), path, false); got != base {
			t.Fatalf("分类依赖路径：%s 得到 %s，纯文本分类是 %s —— 分类器必须与路径无关", path, got, base)
		}
	}
	// 阳性对照：v1 形态的片段在任何路径下都必须被判成基表族
	//（⇒ 上面那条断言不是恒绿：分类器确实会区分）。
	for _, code := range []string{
		"package admin; func other() string { return `SELECT ts FROM _request_logs` }",
		"package admin; // func tenantStatsLogsTableSQL(bool) string {}\nvar q = `SELECT ts FROM _request_logs`",
		"package admin; func (*Handler) tenantStatsLogsTableSQL(bool) string { return `SELECT ts FROM _request_logs` }",
		"package admin; func tenantStatsLogsTableSQL(any) string { return `SELECT ts FROM _request_logs` }",
	} {
		for _, path := range []string{"admin/tenant_stats_logs_table.go", "admin/other.go"} {
			if got := sourceFamilyForFile(code, path, false); got != familyBase {
				t.Fatalf("v1 形态片段在 %s 下被判成 %s，期望基表族", path, got)
			}
		}
	}
}

// stripSQLComments removes `-- ...` to end of line and `/* ... */` from a SQL
// body. It is deliberately conservative about quoted regions: an apostrophe
// inside a string literal is common in these queries, and mangling one would
// change a classification silently. A `--` inside a quoted literal is left in
// place, which can only leave a name un-stripped, never invent a relation.
func stripSQLComments(raw string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(raw, "\n") {
		if inBlock {
			if i := strings.Index(line, "*/"); i >= 0 {
				line, inBlock = line[i+2:], false
			} else {
				continue
			}
		}
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		for {
			i := strings.Index(line, "/*")
			j := strings.Index(line, "*/")
			if i < 0 {
				break
			}
			if j > i {
				line = line[:i] + line[j+2:]
				continue
			}
			line, inBlock = line[:i], true
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
