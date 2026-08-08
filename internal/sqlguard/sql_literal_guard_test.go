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

	type finding struct {
		file string
		line int
		text string
	}
	var findings []finding
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

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
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
			sqlLiterals++

			startLine := fset.Position(lit.Pos()).Line
			for i, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					rel, _ := filepath.Rel(root, path)
					findings = append(findings, finding{
						file: rel,
						line: startLine + i,
						text: strings.TrimSpace(line),
					})
				}
			}
			return true
		})
		return nil
	})
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
