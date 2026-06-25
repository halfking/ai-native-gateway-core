package admin

// bytea_scan_lint_test.go — static regression guard for the
// "scan secret_ciphertext (bytea) into *string" anti-pattern.
//
// Background (2026-06-26):
//   admin/routing.go:3194 (mirrorExistingKeys) declared
//       var label, ciphertext string
//   and scanned `c.secret_ciphertext` (a PostgreSQL bytea column) into
//   `&ciphertext`. pgx refuses with:
//       cannot scan bytea (OID 17) in binary format into *string
//   causing the free-pool mirror to silently skip every row that has
//   a real key.
//
// Strategy:
//   For every .go file in admin/, bg/, discovery/, provider/, and
//   cmd/probe-cred/, this test walks every function body and finds
//   every `db.Query/QueryRow(...).Scan(&x, &y, ...)` chain where the
//   SQL contains the column `secret_ciphertext`. It then verifies that
//   the destination corresponding to that column has been declared as
//   type []byte (NOT string) in either the function-local declarations
//   or the package-level declarations.
//
// The test does NOT need a live database, does NOT need a network
// connection, and runs in <1s. It is intentionally conservative: it
// only flags *clear* mismatches where the scan target is unambiguously
// `*string` (var declarations with `string` type). It does not chase
// type aliases or generic interfaces.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// byteaColumns is the set of PostgreSQL bytea columns in this schema.
// We track them here because the "scan-into-string" failure mode applies
// uniformly to any bytea column, and a future contributor who adds a
// new bytea column should add it here too.
var byteaColumns = []string{
	"secret_ciphertext", // credentials.secret_ciphertext (AES-GCM encrypted)
}

// TestScanByteaColumnsIntoBytea walks all credential-handling Go
// files and ensures that no SELECT statement projecting any bytea
// column scans into a Go `*string`.
//
// Without this guard, any future contributor who writes
//
//	var ciphertext string
//	db.QueryRow(ctx, `SELECT secret_ciphertext FROM credentials ...`).Scan(&ciphertext)
//
// will reintroduce the production bug fixed in commit c278ff84. This
// test makes that mistake fail CI before it ships.
func TestScanByteaColumnsIntoBytea(t *testing.T) {
	root := repoRoot(t)
	dirs := []string{
		"admin",
		"bg",
		"discovery",
		"provider",
		"cmd/probe-cred",
	}

	fset := token.NewFileSet()
	var violations []string

	for _, dir := range dirs {
		pkgs, err := parser.ParseDir(fset, root+"/"+dir, nil, parser.AllErrors|parser.ParseComments)
		if err != nil {
			// Directory may not exist (e.g. cmd/probe-cred); skip.
			continue
		}
		for _, pkg := range pkgs {
			for _, file := range pkg.Files {
				violations = append(violations, scanFile(fset, file)...)
			}
		}
	}

	if len(violations) > 0 {
		t.Fatalf("found %d bytea-into-string violation(s):\n  %s\n\n"+
			"PostgreSQL bytea columns MUST be scanned into []byte, not string. "+
			"pgx returns: \"cannot scan bytea (OID 17) in binary format into *string\". "+
			"See commit c278ff84 (mirrorExistingKeys) for context.",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// scanFile walks one Go file and returns a list of "file:line: ..." strings
// describing every bytea-into-string violation.
func scanFile(fset *token.FileSet, file *ast.File) []string {
	var violations []string

	// Collect every package-level var declaration that has type `string`,
	// mapping var name → "string". (Includes `var x, y string`.)
	stringDecls := collectStringDecls(file.Decls)

	// Walk every function in the file.
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		violations = append(violations, scanFunc(fset, fn, stringDecls)...)
	}

	return violations
}

// collectStringDecls walks a list of top-level decls and returns a
// map[name]"string" for every var declared with type "string" (or
// anything ending in `.string`).
func collectStringDecls(decls []ast.Decl) map[string]string {
	out := map[string]string{}
	for _, decl := range decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			typeText := exprText(vs.Type)
			if typeText == "" || typeText == "string" || strings.HasSuffix(typeText, ".string") {
				for _, name := range vs.Names {
					if name != nil {
						out[name.Name] = "string"
					}
				}
			}
		}
	}
	return out
}

// scanFunc finds every Scan(&...) call inside fn whose receiving call
// is `db.Query(...)` or `db.QueryRow(...)` whose SQL projects at least
// one bytea column, and verifies each destination type.
func scanFunc(fset *token.FileSet, fn *ast.FuncDecl, pkgStringDecls map[string]string) []string {
	var violations []string

	// Function-local string declarations shadow package-level ones.
	localStringDecls := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		typeText := exprText(vs.Type)
		if typeText == "string" || strings.HasSuffix(typeText, ".string") {
			for _, name := range vs.Names {
				if name != nil {
					localStringDecls[name.Name] = true
				}
			}
		}
		return true
	})

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "Scan" {
			return true
		}

		sql := findSQLForScanCall(call)
		if sql == "" {
			return true
		}

		// For each bytea column, find its position in the SELECT list
		// and verify the matching Scan destination is NOT a string.
		for _, col := range byteaColumns {
			colIdx, ok := findColumnIndex(sql, col)
			if !ok {
				continue
			}
			if colIdx < 0 || colIdx >= len(call.Args) {
				continue
			}
			dest := call.Args[colIdx]
			if !isStringDestination(dest, localStringDecls, pkgStringDecls) {
				continue
			}
			pos := fset.Position(call.Pos())
			violations = append(violations, pos.String()+
				": "+col+" scanned into *string — must use []byte")
		}
		return true
	})

	return violations
}

// findSQLForScanCall returns the SQL string of the Query/QueryRow call
// that `.Scan(&...)` is invoked on. Returns "" if the structure is too
// complex to resolve statically (in which case we conservatively skip).
func findSQLForScanCall(call *ast.CallExpr) string {
	if call.Fun == nil {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	recv, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return ""
	}
	if recv.Fun == nil {
		return ""
	}
	rsel, ok := recv.Fun.(*ast.SelectorExpr)
	if !ok || rsel.Sel == nil {
		return ""
	}
	if rsel.Sel.Name != "Query" && rsel.Sel.Name != "QueryRow" {
		return ""
	}
	// The SQL is the first STRING-typed argument. Query/QueryRow take
	// (ctx, sql, ...args) — so SQL is usually Args[1], not Args[0].
	for _, arg := range recv.Args {
		bl, ok := arg.(*ast.BasicLit)
		if !ok {
			continue
		}
		if bl.Kind == token.STRING {
			return unquoteGoString(bl.Value)
		}
	}
	return ""
}

// findColumnIndex returns the 0-based column index of `col` in the
// top-level SELECT list of `sql`, and whether it was found.
//
// We recognize the column by its suffix (e.g. `c.secret_ciphertext`,
// `secret_ciphertext`) or exact match.
func findColumnIndex(sql string, col string) (int, bool) {
	cleaned := stripLineComments(sql)
	low := strings.ToLower(cleaned)

	selectIdx := strings.Index(low, "select")
	if selectIdx < 0 {
		return 0, false
	}
	// Find `from` keyword at the top level (not inside parens).
	// The keyword may be preceded by any whitespace, including \n and tabs.
	fromIdx := findTopLevelKeyword(low[selectIdx:], "from")
	if fromIdx < 0 {
		return 0, false
	}
	list := cleaned[selectIdx+len("select") : selectIdx+fromIdx]

	cols := splitTopLevelCommas(list)
	colLower := strings.ToLower(col)
	for i, c := range cols {
		stripped := strings.TrimSpace(c)
		strippedLower := strings.ToLower(stripped)
		if strippedLower == colLower {
			return i, true
		}
		if strings.HasSuffix(strippedLower, "."+colLower) {
			return i, true
		}
	}
	return 0, false
}

// findTopLevelKeyword returns the byte offset of `kw` in `s` such that
// `kw` is at the top level (not inside parens) and bounded by
// whitespace. Returns -1 if not found.
func findTopLevelKeyword(s string, kw string) int {
	depth := 0
	for i := 0; i+len(kw) <= len(s); i++ {
		c := s[i]
		if c == '(' {
			depth++
			continue
		}
		if c == ')' {
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth != 0 {
			continue
		}
		// Must be preceded by whitespace (or start of string).
		if i > 0 && !isWhitespace(s[i-1]) {
			continue
		}
		if s[i:i+len(kw)] != kw {
			continue
		}
		// Must be followed by whitespace (or end of string).
		end := i + len(kw)
		if end < len(s) && !isWhitespace(s[end]) {
			continue
		}
		return i
	}
	return -1
}

func isWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// splitTopLevelCommas splits s on commas not nested inside parens.
func splitTopLevelCommas(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// isStringDestination returns true if `dest` is `&varName` where
// varName was declared as type string.
func isStringDestination(dest ast.Expr, localStringDecls map[string]bool, pkgStringDecls map[string]string) bool {
	u, ok := dest.(*ast.UnaryExpr)
	if !ok || u.Op != token.AND {
		return false
	}
	id, ok := u.X.(*ast.Ident)
	if !ok {
		return false
	}
	if localStringDecls[id.Name] {
		return true
	}
	if t, ok := pkgStringDecls[id.Name]; ok && t == "string" {
		return true
	}
	return false
}

// exprText returns a short text representation of a type expression
// good enough to compare with "string". Examples:
//
//	Ident "string"         → "string"
//	Ident "CipherText"     → "CipherText"
//	ArrayType Elt=Ident "byte" → "byte"
//	StarExpr X=Ident "Foo" → "Foo"
//	SelectorExpr X="pkg" Sel="Bar" → "pkg.Bar"
func exprText(e ast.Expr) string {
	if e == nil {
		return ""
	}
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.ArrayType:
		return exprText(v.Elt)
	case *ast.StarExpr:
		return exprText(v.X)
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	}
	return ""
}

// unquoteGoString handles both raw-quoted (``...``) and
// double-quoted ("...") Go string literals.
func unquoteGoString(raw string) string {
	if len(raw) < 2 {
		return raw
	}
	if raw[0] == '`' && raw[len(raw)-1] == '`' {
		return raw[1 : len(raw)-1]
	}
	if raw[0] == '"' && raw[len(raw)-1] == '"' {
		inner := raw[1 : len(raw)-1]
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		inner = strings.ReplaceAll(inner, `\n`, "\n")
		inner = strings.ReplaceAll(inner, `\t`, "\t")
		return inner
	}
	return raw
}

func stripLineComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// repoRoot returns the module root for this repo. We are running from
// the admin/ package directory (cwd = <root>/admin). The repo root
// contains go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	candidates := []string{"..", "../..", "."}
	for _, c := range candidates {
		if _, err := os.Stat(c + "/go.mod"); err == nil {
			return c
		}
	}
	t.Fatalf("could not find repo root (no go.mod above cwd=%q)", strconv.Quote(mustGetwd()))
	return ""
}

func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
}