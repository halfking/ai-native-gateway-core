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
	if got := sourceFamilyForFile(string(raw), "admin/tenant_stats_logs_table.go", false); got != familySessionBase {
		t.Fatalf("real tenant source family=%s, want mixed source", got)
	}
}

func TestSourceFamilyProofIsFileScoped(t *testing.T) {
	raw, err := os.ReadFile("tenant_stats_logs_table.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := sourceFamilyForFile(string(raw), "admin/other.go", false); got != familyBase {
		t.Fatalf("file proof leaked: %s", got)
	}
	for _, code := range []string{
		"package admin; func other() string { return `SELECT ts FROM _request_logs` }",
		"package admin; // func tenantStatsLogsTableSQL(bool) string {}\nvar q = `SELECT ts FROM _request_logs`",
		"package admin; func (*Handler) tenantStatsLogsTableSQL(bool) string { return `SELECT ts FROM _request_logs` }",
		"package admin; func tenantStatsLogsTableSQL(any) string { return `SELECT ts FROM _request_logs` }",
	} {
		if got := sourceFamilyForFile(code, "admin/tenant_stats_logs_table.go", false); got != familyBase {
			t.Fatalf("proof accepted different source declaration: %s", got)
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
