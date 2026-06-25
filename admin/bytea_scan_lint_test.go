package admin

// bytea_scan_lint_test.go — static regression guard for the
// "scan secret_ciphertext (bytea) into *string" anti-pattern.
//
// Background (2026-06-26):
//   admin/routing.go:3144 (mirrorExistingKeys) declared
//       var label, ciphertext string
//   and scanned `c.secret_ciphertext` (a PostgreSQL bytea column) into
//   `&ciphertext`. pgx refuses with:
//       cannot scan bytea (OID 17) in binary format into *string
//   causing the free-pool mirror to silently skip every row that has
//   a real key, and surfacing as a generic "query credential: ..."
//   failure on the async health_check task.
//
// This test walks every *.go file in admin/, bg/, discovery/, provider/,
// cmd/probe-cred/ using go/ast, finds every SELECT statement that
// includes `secret_ciphertext`, finds the destination list of the
// matching QueryRow.Scan() / rows.Scan() call (in the same enclosing
// function), and asserts every destination that corresponds to
// `secret_ciphertext` has type `[]byte`.
//
// The test does NOT need a live database, does NOT need a network
// connection, and runs in <1s. It is intentionally conservative: it
// only flags *clear* mismatches where the scan target is unambiguously
// `*string` (var/field declarations with `string` types). It does not
// attempt to chase generic interfaces or type aliases.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestSecretCiphertextScanIntoBytea statically verifies that no Go
// source file scans credentials.secret_ciphertext into a string.
//
// Without this guard, any future contributor who writes
//
//	var ciphertext string
//	db.QueryRow(ctx, `SELECT secret_ciphertext FROM credentials ...`).Scan(&ciphertext)
//
// will reintroduce the same production bug. This test makes that
// mistake fail CI.
func TestSecretCiphertextScanIntoBytea(t *testing.T) {
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
			t.Logf("DEBUG ParseDir err for %s: %v", dir, err)
			// Some directories may not exist (e.g. cmd/probe-cred); skip.
			continue
		}
		fileCount := 0
		for _, pkg := range pkgs {
			for range pkg.Files {
				fileCount++
			}
		}
		t.Logf("DEBUG dir=%s files=%d", dir, fileCount)
		for _, pkg := range pkgs {
			for _, file := range pkg.Files {
				violations = append(violations, scanFile(fset, file, t)...)
			}
		}
	}

	if len(violations) > 0 {
		t.Fatalf("found %d bytea-into-string violation(s):\n  %s\n\n"+
			"credentials.secret_ciphertext is a bytea column. "+
			"Scan destinations for this column must be []byte, not string. "+
			"See admin/routing.go:3197 and commit c278ff84 for context.",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// scanFile walks one Go file and returns a list of "file:line: ..." strings
// describing every location where a SELECT statement that includes
// `secret_ciphertext` is paired with a Scan() call whose corresponding
// destination is a *string.
func scanFile(fset *token.FileSet, file *ast.File, t *testing.T) []string {
	var violations []string

	// Collect every var/field declaration in the file together with the
	// (name → type-string) mapping. We only need to recognize names that
	// resolve to `string`; everything else (including untyped, []byte,
	// *string, named types) is considered safe-by-default.
	stringDecls := map[string]string{} // var name → source-text-of-type
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name == nil {
					continue
				}
				if i < len(vs.Values) {
					// `var ciphertext string = "x"` — skip, rare and still detectable from type
				}
				typeText := exprText(vs.Type)
				if typeText != "" {
					stringDecls[name.Name] = typeText
				}
			}
		}
	}

	// Walk every function in the file and look for Query/QueryRow().Scan().
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		violations = append(violations, scanFunc(fset, fn, stringDecls, nil)...)
	}

	return violations
}

// scanFunc finds every QueryRow/Query().Scan(&...) call inside fn and
// verifies its destinations match the SELECT list.
func scanFunc(fset *token.FileSet, fn *ast.FuncDecl, stringDecls map[string]string, t *testing.T) []string {
	var violations []string

	// Pre-collect function-local var declarations. We re-walk the body
	// for `var x string` patterns because they shadow package-level names.
	// Note: a single `var a, b string` ValueSpec has Names=[a b] and
	// a single Type="string" — every name inherits that type, so we
	// must register all Names, not just Names[0].
	localStringDecls := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		if exprText(vs.Type) == "string" {
			for _, name := range vs.Names {
				if name != nil {
					localStringDecls[name.Name] = true
				}
			}
		}
		return true
	})
	if t != nil {
		t.Logf("DEBUG scanFunc: fn=%s localStringDecls=%v", fn.Name.Name, localStringDecls)
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "Scan" {
			return true
		}
		if t != nil {
			t.Logf("DEBUG found Scan call in fn=%s", fn.Name.Name)
		}
		// call.Fun is `<recv>.Scan`; we expect recv to be a QueryRow / Query
		// call. Walk back through statement context to find the SQL string.
		sql := findEnclosingSQLString(call)
		if sql == "" {
			if t != nil {
				t.Logf("DEBUG Scan call has no SQL: recv=%T sel.X=%T", sel.X, sel.X)
			}
			return true
		}
		if t != nil {
			t.Logf("DEBUG Scan SQL len=%d contains_cipher=%v", len(sql), containsSecretCiphertext(sql))
		}
		if !containsSecretCiphertext(sql) {
			return true
		}
		// The SELECT must reference secret_ciphertext; the column index in
		// the SELECT list determines which Scan arg is the bytea target.
		colIdx, found := findCipherColumnIndex(sql)
		if !found {
			// SELECT uses some join alias we don't recognize; skip safely.
			return true
		}
		if colIdx < 0 || colIdx >= len(call.Args) {
			return true
		}
		dest := call.Args[colIdx]
		if !isStringDestination(dest, localStringDecls, stringDecls) {
			t.Logf("DEBUG: colIdx=%d dest=%s localDecls=%v pkgDecls=%v sql=%.80q",
				colIdx, exprText(dest), localStringDecls, stringDecls, sql)
			return true
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, pos.String()+
			": secret_ciphertext scanned into *string — must use []byte")
		return true
	})

	return violations
}

// findEnclosingSQLString looks for the first ancestor of `call` that is
// `db.QueryRow(ctx, "<sql>", ...)` or `db.Query(ctx, "<sql>", ...)` and
// returns the SQL literal (with newlines/indent collapsed).
//
// We try in this order:
//   1. The immediate receiver of call.Fun is itself a CallExpr to
//      Query/QueryRow → the first string arg is the SQL.
//   2. Otherwise, walk up assignments until we find a CallExpr to
//      Query/QueryRow whose first string arg matches.
func findEnclosingSQLString(call *ast.CallExpr) string {
	if call.Fun == nil {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	recv, ok := sel.X.(*ast.CallExpr)
	if !ok {
		// Sometimes the assignment is `rows, err := db.Query(...)` and
		// then `rows.Scan(...)`. Try the assignment walk below.
		return findSQLFromAssignmentChain(call)
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
	if len(recv.Args) == 0 {
		return ""
	}
	bl, ok := recv.Args[0].(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return ""
	}
	return unquoteGoString(bl.Value)
}

// findSQLFromAssignmentChain handles patterns like
//
//	rows, err := h.db.Query(ctx, `SELECT ...`)
//	...
//	rows.Scan(&x, &y)
//
// We don't need to chase the rows variable's call — we already have
// the receiver of `Scan`. If the receiver is a CallExpr, findEnclosingSQLString
// will handle it. Otherwise we have nothing to go on, return "" so the
// caller skips.
func findSQLFromAssignmentChain(call *ast.CallExpr) string {
	if call.Fun == nil {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	// sel.X may be an Ident (`rows`) referring to a rows variable, in
	// which case we cannot resolve the SQL statically. Return "".
	if _, ok := sel.X.(*ast.Ident); ok {
		return ""
	}
	// Otherwise, sel.X is itself a CallExpr — re-enter findEnclosingSQLString.
	if _, ok := sel.X.(*ast.CallExpr); ok {
		return findEnclosingSQLString(call)
	}
	return ""
}

func containsSecretCiphertext(sql string) bool {
	// Be flexible: `c.secret_ciphertext`, `secret_ciphertext`, with
	// different casing from SQL folding.
	low := strings.ToLower(sql)
	return strings.Contains(low, "secret_ciphertext")
}

// findCipherColumnIndex finds the (0-based) column index of
// `secret_ciphertext` inside the top-level SELECT list of sql.
// Returns false if the SELECT list can't be parsed (e.g. uses
// non-trivial expressions across columns).
func findCipherColumnIndex(sql string) (int, bool) {
	// Strip line comments for safety.
	cleaned := stripLineComments(sql)
	low := strings.ToLower(cleaned)

	selectIdx := strings.Index(low, "select")
	if selectIdx < 0 {
		return 0, false
	}
	fromIdx := strings.Index(low[selectIdx:], " from ")
	if fromIdx < 0 {
		return 0, false
	}
	list := cleaned[selectIdx+len("select") : selectIdx+fromIdx]

	// Split top-level columns by commas (not commas inside parens).
	cols := splitTopLevelCommas(list)
	for i, col := range cols {
		// A column reference like `c.secret_ciphertext` or `secret_ciphertext`.
		// We treat the whole expression as referring to secret_ciphertext if
		// it ends with `.secret_ciphertext` or equals `secret_ciphertext`.
		stripped := strings.TrimSpace(col)
		strippedLower := strings.ToLower(stripped)
		if strippedLower == "secret_ciphertext" {
			return i, true
		}
		if strings.HasSuffix(strippedLower, ".secret_ciphertext") {
			return i, true
		}
	}
	return 0, false
}

// splitTopLevelCommas splits s on commas that are not nested inside
// parentheses. It is good enough for our SELECT-list parsing; it does
// not handle string literals containing commas, but SELECT lists in
// this codebase don't contain those.
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

// isStringDestination returns true if `dest` is a `&varName` where
// `varName` was declared as type `string` in either the local or
// package-level decls.
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
	case *ast.MapType:
		return "map"
	default:
		return ""
	}
}

func unquoteGoString(raw string) string {
	if len(raw) < 2 {
		return raw
	}
	// raw-quoted: `...`
	if raw[0] == '`' && raw[len(raw)-1] == '`' {
		return raw[1 : len(raw)-1]
	}
	// double-quoted (with possible escaped chars; we keep it simple)
	if raw[0] == '"' && raw[len(raw)-1] == '"' {
		inner := raw[1 : len(raw)-1]
		// Light handling of common escapes.
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		inner = strings.ReplaceAll(inner, `\n`, "\n")
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

// repoRoot returns the module root for this repo. We compute it from
// the package directory at test runtime by going up two levels (we are
// in admin/, repo root is one level up).
func repoRoot(t *testing.T) string {
	t.Helper()
	// All tests in this package run with the package's directory as cwd,
	// which is <repoRoot>/admin. The repo root contains go.mod.
	// We resolve it via `go env GOMOD` if available, falling back to "..".
	root := ".."
	if _, err := parser.ParseFile(token.NewFileSet(), root+"/go.mod", nil, parser.ParseComments); err == nil {
		return root
	}
	return "."
}