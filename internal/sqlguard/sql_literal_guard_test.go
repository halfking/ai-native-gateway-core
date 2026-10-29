// Package sqlguard holds a repo-wide guard against a defect class that no
// existing tool catches: a Go-style `//` comment written inside a raw-string
// SQL literal.
//
// Why this guard exists (2026-08-08 production incident):
//
// A code-review note was added to the SQL of loadCandidatesByModalityDB using
// Go comment syntax, inside the backtick literal:
//
//	AND COALESCE(c_sibling.manual_disabled, FALSE) = FALSE
//	// 2026-08-08 audit note: c_sibling.quota_state predicate
//	AND COALESCE(c_sibling.quota_state, 'ok') NOT IN (...)
//
// PostgreSQL has no `//` comment form, so the statement stopped parsing:
//
//	ERROR:  syntax error at or near "audit"
//
// The candidate query then failed for every request, the router received zero
// candidates, and the streaming handler returned HTTP 500
// `routing_database_error` — the whole failover machine was unreachable.
//
// `go build` and `go vet` both passed the entire time, because to Go this is
// just a string. That is the gap this guard closes.
package sqlguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stmtStart matches a literal whose first meaningful line begins a SQL
// statement. Requiring a statement keyword at the start (rather than anywhere
// in the text) keeps ordinary Go strings that merely mention SELECT out of
// scope.
var stmtStart = regexp.MustCompile(
	`(?i)^\s*(SELECT|INSERT\s+INTO|UPDATE\s+\w|DELETE\s+FROM|WITH\s+\w|` +
		`CREATE|ALTER|DROP|TRUNCATE|PREPARE|REFRESH|COMMENT\s+ON|GRANT|REVOKE)\b`,
)

// skipDirs are trees we do not own or that intentionally hold SQL fragments
// in non-executable form.
var skipDirs = map[string]bool{
	"vendor":       true,
	"testdata":     true,
	"node_modules": true,
	".git":         true,
}

// TestNoGoCommentsInSQLLiterals walks every .go file in the repository, finds
// raw-string literals that hold SQL, and fails if any line inside one starts
// with `//`.
//
// The check uses the Go AST rather than a regex over the file text: brace and
// backtick pairing done by eye (or by regex) misattributes ordinary doc
// comments to nearby literals, which is exactly how the original defect
// survived review.
func TestNoGoCommentsInSQLLiterals(t *testing.T) {
	root := repoRoot(t)

	findings, sqlLiterals, err := findGoCommentsInSQLLiterals(root)
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}

	if sqlLiterals == 0 {
		t.Fatal("scanned 0 SQL literals — the detector is broken, not the code")
	}
	t.Logf("scanned %d SQL literals", sqlLiterals)

	for _, f := range findings {
		t.Errorf("%s:%d: Go // comment inside SQL literal (PostgreSQL cannot "+
			"parse it; use -- or /* */ instead):\n\t%s", f.file, f.line, f.text)
	}
}

// sqlLiteralFinding 是一条「SQL 字面量里出现了 // 开头行」的发现。
type sqlLiteralFinding struct {
	file string
	line int
	text string
}

// findGoCommentsInSQLLiterals 是本门的**唯一一份**检测实现（门与自证共用）。
//
// ★ 2026-10-29：检测逻辑从门里抽出来，因为要加**自证**（量具自己也要被量）：
//
//	· 自证要证明门对「拼接形态」有牙；
//	· 自证要**登记覆盖边界**（本门只认 SQL **语句**形态的字面量 ⇒
//	  「SQL 表达式片段」（例如 2026-10-29 抽出来的模态接受臂唯一片段）
//	  **不在本门的覆盖范围内** ⇒ 它的覆盖由 provider 侧那道**词法**门
//	  （扫整个包的所有 raw string）承担 ⇒ 两道门必须同时存在。
func findGoCommentsInSQLLiterals(root string) ([]sqlLiteralFinding, int, error) {
	var findings []sqlLiteralFinding
	sqlLiterals := 0

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// A file that does not parse is not this guard's problem; the
			// compiler will report it.
			return nil
		}

		// covered 记下「已经被当作拼接常量的一部分扫过」的 raw string 字面量，
		// 避免同一个字面量被扫两次。
		covered := map[*ast.BasicLit]bool{}

		scanLiteral := func(lit *ast.BasicLit) {
			body := strings.Trim(lit.Value, "`")
			sqlLiterals++
			startLine := fset.Position(lit.Pos()).Line
			for i, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					rel, _ := filepath.Rel(root, path)
					findings = append(findings, sqlLiteralFinding{
						file: rel,
						line: startLine + i,
						text: strings.TrimSpace(line),
					})
				}
			}
		}

		ast.Inspect(file, func(n ast.Node) bool {
			// ★★ 拼接常量（`...` + `...`）：整个表达式的常量值才是「这段 SQL」
			//   ⇒ 判 SQL 形态要按**拼接后的整体**判 ⇒ 否则被拆开的后半段
			//   （自己的第一个有效行不是语句关键字）会被当成「不是 SQL」⇒
			//   ★★ 后半段里的 `//` 注释对门完全隐形（2026-10-29 实测）。
			//   ⇒ 整体像 SQL ⇒ 逐个字面量都按 SQL 扫（行号取各字面量自己的位置）。
			if bin, ok := n.(*ast.BinaryExpr); ok && bin.Op == token.ADD {
				var parts []*ast.BasicLit
				alreadyCovered := false
				ast.Inspect(bin, func(m ast.Node) bool {
					lit, ok := m.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING || !strings.HasPrefix(lit.Value, "`") {
						return true
					}
					parts = append(parts, lit)
					if covered[lit] {
						alreadyCovered = true
					}
					return true
				})
				if alreadyCovered || len(parts) == 0 {
					// 外层拼接已经扫过这几片 ⇒ 不重复扫。
					return true
				}
				// ★ 判定口径：**这一组拼接的【第一片】像 SQL，整组都按 SQL 扫。**
				//   ⇒ 为什么不用「拼接后的整体」判定：拼接里常夹着**非常量**
				//   的片段（仓内实例：provider/client.go 的
				//   `+ brokenPairExcludeSQL(...) +`、domains/streaming 的
				//   `+ db.SessionFamilyTurnsSourceSQL() +`）⇒ AST 求不出
				//   整体的值 ⇒ 只能按「片段自己像不像 SQL」判。
				//   ⇒ 为什么是「**第一片**」而不是「任意一片」：拼接组里可能
				//   整段都不是 SQL（仓内实例：internal/dbx 的测试夹具把**一整个
				//   Go 源文件**当字符串塞进 raw string，那里面嵌着 SQL 片段）
				//   ⇒ 用「任意一片」会把夹具里的 Go 注释误判成 SQL 注释
				//   （★ 实测过一次假阳性）。
				//   ⇒ 而 SQL 语句的文本**从第一片开始** ⇒ 第一片不像 SQL ⇒
				//   这一组就不是一条 SQL ⇒ 不扫（其中的 SQL 片段仍由单片扫描
				//   单独覆盖）。
				if !isSQL(strings.Trim(parts[0].Value, "`")) {
					return true
				}
				for _, lit := range parts {
					covered[lit] = true
					scanLiteral(lit)
				}
				return true
			}

			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if covered[lit] {
				return true
			}
			// Raw strings only — SQL is never written with escaped newlines.
			if !strings.HasPrefix(lit.Value, "`") {
				return true
			}
			body := strings.Trim(lit.Value, "`")
			if !isSQL(body) {
				return true
			}
			scanLiteral(lit)
			return true
		})
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return findings, sqlLiterals, nil
}

// isSQL reports whether a raw-string literal body looks like a SQL statement,
// judged by its first line that is neither blank nor already a SQL comment.
func isSQL(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "--") || strings.HasPrefix(s, "/*") {
			continue
		}
		return stmtStart.MatchString(s)
	}
	return false
}

// repoRoot walks up from the test's working directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from working directory")
		}
		dir = parent
	}
}
