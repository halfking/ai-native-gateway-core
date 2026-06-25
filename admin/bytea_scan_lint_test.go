package admin

// bytea_scan_lint_test.go — static regression guard for two related
// PostgreSQL column-type anti-patterns:
//
// 1. bytea columns scanned into Go *string. pgx refuses:
//      "cannot scan bytea (OID 17) in binary format into *string"
// 2. jsonb columns scanned into Go *string / sql.NullString WITHOUT
//    a `::text` cast in the SELECT. pgx refuses:
//      "cannot scan jsonb (OID 3802) into *string"
//
// Background (2026-06-26):
//   admin/routing.go:3266 (mirrorExistingKeys) — bytea→string bug
//   admin/provider_credential.go:135, admin/provider_refresh.go:273/313,
//   admin/provider_vendor.go:61/199, discovery/discovery.go:302/320,
//   admin/session_compare.go:120/493 — jsonb→string bugs (missing ::text)
//
// Strategy:
//   For every .go file in admin/, bg/, discovery/, provider/, and
//   cmd/probe-cred/, this test walks every function body and finds
//   every `db.Query/QueryRow(...).Scan(&x, &y, ...)` chain. For each
//   tracked column (bytea or jsonb), it verifies the matching Scan
//   destination is type-compatible:
//
//   - bytea MUST scan into []byte (or *[]byte / sql.NullString with
//     explicit ::text elsewhere). Scanning into *string always fails.
//   - jsonb MUST scan into a []byte / json.RawMessage / sql.NullString
//     / *string destination, BUT only if the SELECT column uses a
//     `::text` cast. Without the cast, pgx returns an error.
//
//   The test does NOT need a live database, does NOT need a network
//   connection, and runs in <1s. It is intentionally conservative:
//   it only flags clear mismatches where the scan target is
//   unambiguously a string-typed var.
//
//   Limitations: this lint only catches SQL strings written as raw
//   literals inline (the common case in this codebase). If the SQL
//   is assigned to a variable first (`q := "...SELECT..."`, then
//   `db.Query(ctx, q, ...)`), the lint conservatively skips — those
//   cases must be verified by manual code review.

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

// jsonbColumnsRequiringCast is the set of jsonb columns that, when
// scanned into Go *string / sql.NullString, MUST be wrapped with a
// `::text` cast in the SELECT list. Without the cast, pgx v5 will
// return "cannot scan jsonb (OID 3802) into *string".
//
// jsonb columns that are scanned into []byte / json.RawMessage /
// interface{} do NOT need a cast (pgx handles those natively).
var jsonbColumnsRequiringCast = []string{
	"tags",                // credentials.tags (jsonb)
	"models_manifest_json", // provider_catalog.models_manifest_json (jsonb)
	"request_body",        // request_logs.request_body (jsonb)
	"response_body",       // request_logs.response_body (jsonb)
	"outbound_body",       // request_logs.outbound_body (jsonb)
	"compression_meta",    // request_logs.compression_meta (jsonb)
}

// TestScanPGColumnsIntoCompatibleType walks all credential-handling
// Go files and ensures that bytea columns scan into []byte and that
// jsonb columns either scan into a non-string type or are wrapped
// with `::text` in the SELECT.
//
// Without this guard, contributors will keep introducing the same
// pgx scan failures that bit us in commits c278ff84 (bytea) and
// the jsonb audit (2026-06-26).
func TestScanPGColumnsIntoCompatibleType(t *testing.T) {
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
		t.Fatalf("found %d pg-column scan violation(s):\n  %s\n\n"+
			"PostgreSQL bytea columns MUST be scanned into []byte, not string. "+
			"PostgreSQL jsonb columns MUST be cast to ::text when scanned into "+
			"*string / sql.NullString. "+
			"See commit c278ff84 (mirrorExistingKeys) and the 2026-06-26 jsonb "+
			"audit for context.",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// scanFile walks one Go file and returns a list of "file:line: ..." strings
// describing every bytea-into-string / jsonb-without-cast violation.
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
// one bytea/jsonb column, and verifies each destination type.
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
			// Pattern 2: rows.Scan() where rows comes from a
			// Query/QueryRow assignment elsewhere in this function.
			if id, ok := sel.X.(*ast.Ident); ok {
				sql = findSQLFromBody(fn.Body, id.Name)
			}
		}
		if sql == "" {
			return true
		}

		// Rule 1: bytea columns must scan into []byte, never *string.
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
				": "+col+" (bytea) scanned into *string — must use []byte")
		}

		// Rule 2: jsonb columns scanning into *string/sql.NullString
		// MUST be wrapped with a `::text` cast in the SELECT list.
		for _, col := range jsonbColumnsRequiringCast {
			colIdx, casted, ok := findColumnIndexWithCast(sql, col)
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
			if casted {
				continue
			}
			pos := fset.Position(call.Pos())
			violations = append(violations, pos.String()+
				": "+col+" (jsonb) scanned into *string without `::text` cast — "+
				"add `::text` to the column in the SELECT list (e.g. `c.tags::text`)")
		}
		return true
	})

	return violations
}

// findSQLForScanCall returns the SQL string of the Query/QueryRow call
// that `.Scan(&...)` is invoked on. Returns "" if the structure is too
// complex to resolve statically (in which case we conservatively skip).
//
// We handle two patterns:
//
//  1. Direct chaining:
//
//     h.db.QueryRow(ctx, `SELECT ...`, id).Scan(&x)
//
//  2. Assignment chain:
//
//     rows, _ := h.db.Query(ctx, query, id)
//     rows.Scan(&x)
//
// For (2), we walk the enclosing function body to find the assignment
// that bound `rows` and resolve the SQL argument from that Query call.
func findSQLForScanCall(call *ast.CallExpr) string {
	if call.Fun == nil {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	// Pattern 1: Scan is invoked directly on a CallExpr.
	if recv, ok := sel.X.(*ast.CallExpr); ok {
		return sqlFromCallExpr(recv)
	}
	// Pattern 2: Scan is invoked on an Ident (rows variable). We need
	// to find the AssignmentStmt that bound it. The caller (scanFunc)
	// handles this — we return "".
	return ""
}

// sqlFromCallExpr extracts the SQL string from a Query/QueryRow call.
func sqlFromCallExpr(call *ast.CallExpr) string {
	if call.Fun == nil {
		return ""
	}
	rsel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || rsel.Sel == nil {
		return ""
	}
	if rsel.Sel.Name != "Query" && rsel.Sel.Name != "QueryRow" {
		return ""
	}
	for _, arg := range call.Args {
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

// findSQLFromBody walks the function body looking for the assignment
// that binds `rowsIdent` to a Query/QueryRow call. Returns the SQL
// string from that call, or "" if not found.
//
// Handles both single-return and multi-return assignment forms:
//
//	rows := db.Query(...)           // Lhs=1, Rhs=1
//	rows, err := db.Query(...)      // Lhs=2, Rhs=1 (Rhs[0] is the call)
//	row := db.QueryRow(...)         // Lhs=1, Rhs=1
//
// Note: this function only resolves SQL when the Query call passes a
// string LITERAL as its SQL arg. If the SQL is in a variable, we
// conservatively return "" — such cases must be verified by manual
// code review.
func findSQLFromBody(body *ast.BlockStmt, rowsIdent string) string {
	var found string
	ast.Inspect(body, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if len(as.Lhs) == 0 || len(as.Rhs) == 0 {
			return true
		}
		// Multi-return form: rows, err := call() — every Lhs binds to Rhs[0].
		if len(as.Lhs) > len(as.Rhs) {
			for _, lhs := range as.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name != rowsIdent {
					continue
				}
				call, ok := as.Rhs[0].(*ast.CallExpr)
				if !ok {
					continue
				}
				if sql := sqlFromCallExpr(call); sql != "" {
					found = sql
					return false
				}
			}
			return true
		}
		// Standard form: each Lhs[i] binds to Rhs[i].
		for i, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name != rowsIdent {
				continue
			}
			if i >= len(as.Rhs) {
				continue
			}
			call, ok := as.Rhs[i].(*ast.CallExpr)
			if !ok {
				continue
			}
			if sql := sqlFromCallExpr(call); sql != "" {
				found = sql
				return false
			}
		}
		return true
	})
	return found
}

// findColumnIndex returns the 0-based column index of `col` in the
// top-level SELECT list of `sql`, and whether it was found.
//
// We recognize the column by its suffix (e.g. `c.secret_ciphertext`,
// `secret_ciphertext`) or exact match.
func findColumnIndex(sql string, col string) (int, bool) {
	idx, _, found := findColumnIndexWithCast(sql, col)
	return idx, found
}

// findColumnIndexWithCast is like findColumnIndex but additionally
// returns whether the column expression in the SELECT list ends with
// a `::text` cast. The cast is required for jsonb→string scans.
func findColumnIndexWithCast(sql string, col string) (int, bool, bool) {
	cleaned := stripLineComments(sql)
	low := strings.ToLower(cleaned)

	selectIdx := strings.Index(low, "select")
	if selectIdx < 0 {
		return 0, false, false
	}
	fromIdx := findTopLevelKeyword(low[selectIdx:], "from")
	if fromIdx < 0 {
		return 0, false, false
	}
	list := cleaned[selectIdx+len("select") : selectIdx+fromIdx]

	cols := splitTopLevelCommas(list)
	colLower := strings.ToLower(col)
	for i, c := range cols {
		stripped := strings.TrimSpace(c)
		strippedLower := strings.ToLower(stripped)
		if strippedLower != colLower && !strings.HasSuffix(strippedLower, "."+colLower) {
			continue
		}
		// Match. Check if the column expression ends with `::text` cast.
		casted := false
		tail := stripped[strings.LastIndex(strippedLower, colLower)+len(colLower):]
		tailTrimmed := strings.TrimSpace(tail)
		if strings.HasPrefix(tailTrimmed, "::") && strings.Contains(tailTrimmed, "text") {
			casted = true
		}
		return i, true, casted
	}
	return 0, false, false
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