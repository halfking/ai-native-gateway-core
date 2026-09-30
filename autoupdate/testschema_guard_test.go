// Round 44: guard against the bug class that produced the worst finding this
// round — test cleanup that silently targets a table which does not exist.
//
// The shape, from autoupdate/autoupdate_integration_test.go:
//
//	_, _ = pool.Exec(ctx, "DELETE FROM autoupdate_upgrade_logs WHERE ...")
//	_, _ = pool.Exec(ctx, "DELETE FROM autoupdate_gray_rules   WHERE ...")
//	_, _ = pool.Exec(ctx, "DELETE FROM autoupdate_releases     WHERE ...")
//
// `autoupdate_upgrade_logs` / `autoupdate_gray_rules` / `autoupdate_releases` are
// not tables in this schema, and no product code has ever referenced them — the
// store is a *PgxStore and writes `releases` / `upgrade_logs` /
// `gray_release_rules` / `instance_release_status`. Every one of those DELETEs
// raised "relation does not exist" and the error was discarded by `_, _ =`.
//
// The damage was not local. The rows those subtests actually wrote stayed in
// `releases` on ChannelStable with a high build_seq, so a DIFFERENT test file
// (store_pgx_test.go) picked them up and three of its subtests failed. One test
// file's broken cleanup silently broke another test file, and because no CI run
// ever supplies a database URL, none of it was visible.
//
// This test has no build tag and needs no database: it is pure source analysis.
// It is a guard, not a fix — the fixes are in the cleanup statements themselves.
package autoupdate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// stripGoComments removes // line comments and /* */ block comments.
//
// This is load-bearing, not decoration. The fix for the cleanup above explains
// in prose exactly which non-existent tables it used to reference, and a guard
// that matched raw source text would match its own explanation and stay green —
// the same false-green that round 43 hit when a guard was satisfied by a script
// name appearing in a workflow comment.
func stripGoComments(src string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		if inBlock {
			if i := strings.Index(line, "*/"); i >= 0 {
				line, inBlock = line[i+2:], false
			} else {
				continue
			}
		}
		if i := strings.Index(line, "/*"); i >= 0 {
			if j := strings.Index(line[i:], "*/"); j >= 0 {
				line = line[:i] + line[i+j+2:]
			} else {
				line, inBlock = line[:i], true
			}
		}
		if i := strings.Index(line, "//"); i >= 0 {
			// Leave string literals alone: a "//" inside a URL or regex is not a
			// comment. Good enough for a test file whose tables are all in
			// backticks or double quotes on one line.
			if !strings.Contains(line[:i], `"`) {
				line = line[:i]
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// sqlLiterals returns every string literal in a Go file that is a SQL
// statement, i.e. one that BEGINS with a statement keyword.
//
// The predicate took three attempts, and the failures are the useful part:
//
//  1. case-insensitive `FROM|JOIN|INTO|UPDATE` over raw source → matched English
//     prose ("rows into later tests", "This update must be applied").
//  2. "the string CONTAINS SELECT/DELETE/INSERT/UPDATE" → same three, because
//     the word "update" alone satisfies such a filter.
//  3. "the string is an argument to pool.Exec/Query" → semantically closest, but
//     it found only 1 reference: the cleanup statements this guard exists for
//     live in a `[]string{...}` slice and reach Exec as a variable, so no
//     argument-position rule can see them. The "0 references" self-check is what
//     surfaced that as a failure instead of a silent pass.
//
// What survives all three is the syntactic property of SQL itself: a statement
// starts with SELECT / DELETE / INSERT / UPDATE / WITH / CREATE / ALTER / DROP
// / TRUNCATE. Anchoring at the START excludes prose, and scanning every literal
// (not just call arguments) reaches slice elements. This is the same lesson
// round 43 and round 44 recorded twice — a guard must be pinned to the property
// that is actually necessary, not to a word that merely appears nearby.
var sqlLeadingKeyword = regexp.MustCompile(`(?is)^\s*(select|delete|insert|update|with|create|alter|drop|truncate)\b`)

func sqlLiterals(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(bl.Value)
		if err != nil || !sqlLeadingKeyword.MatchString(s) {
			return true
		}
		out = append(out, s)
		return true
	})
	return out
}

// tableRefs finds relation names inside a SQL string.
var tableRefs = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|INTO|UPDATE)\s+([a-z_][a-z0-9_]*)`)

// schemaTables returns the relations the installer actually creates.
func schemaTables(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../sql/schema/01-schema.sql")
	if err != nil {
		t.Fatalf("读不到基线 schema：%v", err)
	}
	src := stripGoComments(string(raw))
	tables := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?(?:TABLE|VIEW|MATERIALIZED\s+VIEW|SEQUENCE)\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?([a-z_][a-z0-9_]*)`).FindAllStringSubmatch(src, -1) {
		tables[m[1]] = true
	}
	// A schema list that came back nearly empty means the extraction broke, and
	// every assertion below would then be vacuously satisfied.
	if len(tables) < 100 {
		t.Fatalf("只从基线解析出 %d 张表，解析多半是坏的；下面的断言会全部空转", len(tables))
	}
	return tables
}

func TestTestCleanupTargetsRealTables(t *testing.T) {
	src, err := os.ReadFile("autoupdate_integration_test.go")
	if err != nil {
		t.Fatalf("读不到被测文件：%v", err)
	}
	_ = src
	tables := schemaTables(t)

	var checked int
	for _, stmt := range sqlLiterals(t, "autoupdate_integration_test.go") {
		for _, m := range tableRefs.FindAllStringSubmatch(stmt, -1) {
			name := strings.ToLower(m[1])
			// SQL keywords that can follow FROM/INTO/UPDATE, and inline CTE names.
			switch name {
			case "select", "delete", "values", "set", "where", "only":
				continue
			}
			checked++
			if !tables[name] {
				t.Errorf("测试 SQL 引用了 schema 里不存在的表 %q（语句：%s）；"+
					"这类语句执行时报 42P01，若错误被 `_, _ =` 吞掉就会静默变成 no-op，"+
					"测试数据留在库里并污染同包其它测试文件", name, strings.TrimSpace(stmt))
			}
		}
	}
	if checked == 0 {
		t.Fatal("一条表引用都没解析出来；判据空转等于没有守卫")
	}
	t.Logf("checked %d table references against %d schema relations", checked, len(tables))
}

// TestGuardIsNotSatisfiedByItsOwnComment is the meta-guard.
//
// Round 44 shipped a guard that was satisfied by the very text explaining the
// bug it was written for. This test fails if someone re-introduces that, by
// checking the table names that do not exist in the schema are absent from the
// STRIPPED source. It also documents the two names on purpose: they appear in
// the prose, and must appear nowhere in code.
func TestGuardIsNotSatisfiedByItsOwnComment(t *testing.T) {
	src, err := os.ReadFile("autoupdate_integration_test.go")
	if err != nil {
		t.Fatalf("读不到被测文件：%v", err)
	}
	code := stripGoComments(string(src))
	for _, ghost := range []string{"autoupdate_releases", "autoupdate_gray_rules", "autoupdate_upgrade_logs"} {
		if strings.Contains(code, ghost) {
			t.Errorf("剥掉注释后的源码里仍出现不存在的表 %q；"+
				"若它只该出现在说明文字里，说明注释剥离失效，守卫会被自己的注释满足", ghost)
		}
	}
}
