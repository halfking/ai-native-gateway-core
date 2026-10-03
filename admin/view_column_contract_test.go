// view_column_contract_test.go —— 198 号审计（全面审计v3/2026-10-03/198）：
// 「读端引用了视图没有的列」这一族的**通用**守卫。
//
// 197 号落的是宽表专用门（usage_ledger，29 个读端）。本轮把它泛化成
// 「视图列契约」：同一个抽取器 + 同一个字面量级扫描器，服务两张列被冻结的表：
//
//   - usage_ledger / _with_current_month / _hot（20 列，表列集即视图列集）
//   - v_routable_credential_models（**33 个生产读端**，输出列仅 13，且它同时 JOIN
//     了四张表 —— 拼装视图的列集与任何基表都不是子集关系，最容易漂移）
//
// ## 三个设计决定，都是被实测逼出来的，不是先验偏好
//
//	① **列集从 pg_dump 基线派生，不手维护、不取「最新迁移」**。
//	   v_routable_credential_models 的最后一次 CREATE 在迁移 335，而 672 / 701
//	   虽然编号更大、也提到该视图，但**只出现在注释里**（实测 grep）；
//	   「取最新迁移」会取错源。基线是 fresh install 实际执行的那一份。
//
//	② **字面量级作用域，不是文件级、也不是声明级**。
//	   本轮先写了文件级扫描，在 admin/routing.go 上立刻踩到既有门文档里记过的那
//	   类误报：同一文件里 `UPDATE ... FROM (VALUES …) AS v(id, priority)` 的别名
//	   v，与另一条查询里 `FROM v_routable_credential_models v` 的别名 v 被混为一谈
//	   ⇒ 误报 `v.id` / `v.priority`。view_source_column_contract_test.go 的注释
//	   写着「按声明级会误报 13 次」—— **本轮证明按文件级同样会误报**。
//	   ⇒ 每个字符串字面量独立判定，用 go/ast 取字面量（热图那条谓词是双引号串，
//	  正则只盯反引号 raw string 会整类漏网）。
//
//	③ **派生自身必须有护栏**：视图找不到 / 抽出的列过少 / 锚点列缺失 ⇒ 直接失败。
//	   否则派生一坏，门会安静下来，而「安静」正是这个族最危险的形态。
package admin

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// viewColumnContract is one governed relation set.
type viewColumnContract struct {
	// name is the schema object whose column set is frozen.
	name string
	// relations are the read-side names a query may bind an alias to
	// (the base table plus its cross-month view / hot heap table).
	relations []string
	// source is where the column set is derived from: "table" reads a
	// CREATE TABLE body, "view" reads a pg_dump CREATE VIEW body.
	source string
	// anchor is a column that must be present in the derived set. It is the
	// anti-garbage guard: a parser regression yields a wrong set, and the
	// anchor turns that into a loud failure instead of a silent gate.
	anchor string
	// forbidden are columns proven NOT to exist on this relation. The mirror of
	// anchor: minColumns/anchor catch an under-collected or garbage parse, and
	// this catches an OVER-collected one. A parse that runs past the closing
	// paren swallows the following table's real column names, and those names
	// then pass as "known" — a phantom column would slip through a gate that
	// looked healthy. Over-collection is the quiet failure mode here, so it
	// needs its own guard rather than a larger minColumns.
	forbidden []string
	// minColumns keeps a truncated parse from looking like a small schema.
	minColumns int
	// minReaders is the floor on how many production Go files this contract must
	// be able to see. Without it the gate has a silent zero state: if the repo
	// root stops resolving, or the walk starts skipping a tree, the scan finds
	// nothing, reports nothing, and passes — the classic always-true gate. A
	// coverage number that only gets printed in a log is decoration; making it
	// an assertion is what makes it a guard.
	minReaders int
}

var viewColumnContracts = []viewColumnContract{
	{
		name:      "usage_ledger",
		relations: []string{"usage_ledger", "usage_ledger_with_current_month", "usage_ledger_hot"},
		source:    "table",
		anchor:    "rate_multiplier", // added by migration 736
		// 33da7e609's three phantoms, proven absent against the real database.
		forbidden:  []string{"work_type", "gw_session_id", "compression_strategy"},
		minColumns: 19, // 19 real + rate_multiplier; a truncated parse yields 17
		minReaders: 25, // measured 29; slack for a legitimate reader removal
	},
	{
		name:      "v_routable_credential_models",
		relations: []string{"v_routable_credential_models"},
		source:    "view",
		anchor:    "is_routable",
		// "case" is in this list because it was ACTUALLY over-collected: the
		// CASE keyword sits alone on its own line in the dumped body and reads
		// exactly like "    cmb.credential_id,". Catching it retroactively
		// matters more than the column itself — a keyword in the known set is
		// proof the forbidden list does not cover the over-collection class.
		forbidden:  []string{"id", "priority", "health_status", "quota_state", "case"},
		minColumns: 12, // 13 real output columns
		minReaders: 25, // measured 33; slack for a legitimate reader removal
	},
}

var (
	// pgDumpViewStart matches the pg_dump view header in the baseline schema.
	pgDumpViewStart = regexp.MustCompile(`^CREATE VIEW public\.([a-z_][a-z0-9_]*) AS\s*$`)
	// pgDumpNextObject is the `--\n-- Name: …` separator pg_dump writes between
	// objects; it is the only reliable end-of-view boundary.
	pgDumpNextObject = regexp.MustCompile(`(?m)^--\n-- Name: `)
	// createTableHeader matches only the header, up to the opening paren. The
	// body is then read to its matching close by paren depth: pg_dump emits
	// ") PARTITION BY RANGE (…)" for partitioned tables, so terminating on a
	// literal "\n);" runs past the table and swallows the next object's real
	// column names into this relation's set.
	createTableHeader = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:public\.)?([a-z_][a-z0-9_]*)\s*\(`)

	// outputColumnAlias matches a computed output column: pg_dump ends those
	// lines with "AS name,".
	outputColumnAlias = regexp.MustCompile(`(?i)\bAS\s+([a-z_][a-z0-9_]*)\s*,?\s*$`)
	// bareOutputColumn matches "    cmb.credential_id," — pg_dump puts every
	// non-computed output column on its own line.
	bareOutputColumn = regexp.MustCompile(`(?i)^\s*(?:[a-z_][a-z0-9_]*\.)?([a-z_][a-z0-9_]*)\s*,?\s*$`)
	// alterAddColumn is whitespace-tolerant across newlines: migration 736
	// writes "ALTER TABLE usage_ledger\n  ADD COLUMN IF NOT EXISTS x …".
	alterAddColumn = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(?:public\.)?([a-z_][a-z0-9_]*)\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)

	// viewAliasBind captures the alias assigned to a governed relation.
	viewAliasBind = regexp.MustCompile(`(?is)(?:FROM|JOIN)\s+(?:public\.)?([a-z_][a-z0-9_]*)\s+(?:AS\s+)?([a-z_][a-z0-9_]*)`)
)

// sqlKeywordAliases are the words that can follow a relation name in a FROM/JOIN
// clause without being an alias. Binding one of them as an alias would make the
// scanner look at columns of a keyword.
var sqlKeywordAliases = map[string]bool{
	"where": true, "group": true, "order": true, "limit": true, "offset": true,
	"on": true, "and": true, "or": true, "select": true, "having": true,
	"union": true, "left": true, "inner": true, "full": true, "cross": true,
	"join": true, "using": true, "fetch": true, "for": true, "set": true,
	"as": true, "values": true, "returning": true, "window": true,
	"natural": true, "lateral": true, "only": true, "table": true,
}

// sqlStandaloneKeywords are words pg_dump can place alone on a line inside a
// view body. A CASE expression is the real case: baseline line 19105 is
// literally " CASE", indistinguishable in shape from "    cmb.credential_id,".
// Letting one through adds a non-column to the known set, which is silent — no
// guard fires and the gate only gets weaker.
var sqlStandaloneKeywords = map[string]bool{
	"case": true, "when": true, "then": true, "else": true, "end": true,
	"select": true, "from": true, "where": true, "and": true, "or": true,
	"not": true, "null": true, "true": true, "false": true, "distinct": true,
	"all": true, "any": true, "array": true, "coalesce": true, "exists": true,
	"in": true, "is": true, "as": true, "on": true, "using": true, "join": true,
	"left": true, "right": true, "inner": true, "outer": true, "full": true,
	"cross": true, "group": true, "order": true, "by": true, "having": true,
	"limit": true, "offset": true, "union": true, "except": true,
	"intersect": true, "with": true, "into": true, "values": true,
	"returning": true, "between": true, "like": true, "ilike": true,
	"similar": true, "cast": true, "over": true, "partition": true,
	"window": true, "filter": true, "asymmetric": true, "symmetric": true,
}

// deriveViewColumns reads a governed object's column set out of the repository's
// own baseline schema. Returns a sorted, deduplicated slice.
func deriveViewColumns(t *testing.T, c viewColumnContract) []string {
	t.Helper()
	root := repoRootFromCaller(t)
	baselinePath := filepath.Join(root, "deploy", "sql", "schemas", "baseline", "01-schema.sql")
	baseline, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("read baseline schema: %v", err)
	}

	cols := map[string]bool{}
	switch c.source {
	case "table":
		body, found := "", false
		// Iterate every CREATE TABLE and keep the one for this relation. A
		// non-greedy single-shot match would happily return whichever table
		// appears first in the dump — the guards below exist precisely because
		// that mistake produces a plausible-looking, wrong column set.
		for _, m := range createTableHeader.FindAllSubmatchIndex(baseline, -1) {
			if string(baseline[m[2]:m[3]]) != c.name {
				continue
			}
			// m[1] is the end of the whole match, i.e. one past the opening
			// paren; balancedParenBody wants the paren itself.
			if bodyText, ok := balancedParenBody(string(baseline), m[1]-1); ok {
				body = bodyText
				found = true
				break
			}
			// Keep looking: a later definition of the same name may be the
			// well-formed one, and silently falling through to "not found"
			// would read like a missing schema.
		}
		if !found {
			t.Fatalf("baseline has no balanced CREATE TABLE body for %q", c.name)
		}
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "--") {
				continue
			}
			fields := strings.Fields(strings.TrimSuffix(line, ","))
			if len(fields) < 2 || isConstraintKeyword(fields[0]) {
				continue
			}
			cols[fields[0]] = true
		}
		// A partitioned table's columns can also arrive via ADD COLUMN.
		alterAdds := 0
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", "docs", "node_modules", "vendor":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".sql") || strings.HasSuffix(d.Name(), ".down.sql") {
				return nil
			}
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			for _, hit := range alterAddColumn.FindAllSubmatch(src, -1) {
				if string(hit[1]) == c.name {
					cols[string(hit[2])] = true
					alterAdds++
				}
			}
			return nil
		})
		if alterAdds == 0 && c.name == "usage_ledger" {
			t.Fatalf("no ADD COLUMN migration found for %s; migration 736 adds rate_multiplier, "+
				"so alterAddColumn has regressed", c.name)
		}
	case "view":
		loc := findPgDumpView(baseline, c.name)
		if loc == "" {
			t.Fatalf("baseline schema has no CREATE VIEW public.%s", c.name)
		}
		for _, line := range strings.Split(loc, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			t := strings.TrimSpace(line)
			if t == "" {
				continue
			}
			if m := outputColumnAlias.FindStringSubmatch(t); m != nil {
				// A cast's "::text" or a SQL keyword must not become a column.
				if !isNotAColumnWord(m[1]) {
					cols[strings.ToLower(m[1])] = true
				}
				continue
			}
			if m := bareOutputColumn.FindStringSubmatch(line); m != nil {
				if !isNotAColumnWord(m[1]) {
					cols[strings.ToLower(m[1])] = true
				}
			}
		}
	default:
		t.Fatalf("unknown source kind %q", c.source)
	}

	out := make([]string, 0, len(cols))
	for k := range cols {
		out = append(out, k)
	}
	sort.Strings(out)

	// Anti-garbage guards: a broken derivation must be loud, never quiet.
	if len(out) < c.minColumns {
		t.Fatalf("%s: derived only %d columns (min %d): %v", c.name, len(out), c.minColumns, out)
	}
	if !viewColumnSetHas(out, c.anchor) {
		t.Fatalf("%s: derived column set is missing anchor %q: %v", c.name, c.anchor, out)
	}
	for _, banned := range c.forbidden {
		if viewColumnSetHas(out, banned) {
			t.Fatalf("%s: derived column set contains %q, which the real schema does not have — "+
				"the parse over-collected (it ran past the object's closing paren and swallowed a "+
				"neighbouring object's columns). A gate built on this set would let a phantom column "+
				"named %q pass as known: %v", c.name, banned, banned, out)
		}
	}
	return out
}

func findPgDumpView(baseline []byte, name string) string {
	lines := strings.Split(string(baseline), "\n")
	for i, line := range lines {
		m := pgDumpViewStart.FindStringSubmatch(line)
		if m == nil || m[1] != name {
			continue
		}
		// pg_dump writes each object as: header, body, then a separator comment.
		var body []string
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "--") && strings.Contains(lines[j], "-- Name:") {
				break
			}
			body = append(body, lines[j])
		}
		return strings.Join(body, "\n")
	}
	return ""
}

// balancedParenBody returns the text between the paren AT openIdx and its
// matching close, respecting nesting. This is what keeps a partitioned table's
// trailing ") PARTITION BY RANGE (…)" and whatever follows it out of the body.
//
// openIdx must be the index OF the opening paren, not the index after it. That
// distinction is not a detail: the first version of this gate passed the
// regex's end offset (which points just PAST the paren), so depth never saw the
// opening paren and the scan returned at the first ")" it met — the one inside
// `cost_usd numeric(12,6)`. The result was a plausible-looking 16-column schema
// that was missing latency_ms / success / error_kind, and the gate then reported
// those three REAL columns as phantom refs in admin/usage.go and
// admin/usage_provider_detail.go. A guard that fails loudly on correct code is
// worse than no guard, so the precondition is asserted here rather than trusted.
func balancedParenBody(src string, openIdx int) (string, bool) {
	if openIdx < 0 || openIdx >= len(src) || src[openIdx] != '(' {
		return "", false
	}
	depth := 0
	// inString guards against a paren inside a SQL string literal — pg_dump emits
	// values like DEFAULT 'a)b'::text, and an unbalanced literal would terminate
	// the body early, reproducing exactly the failure above.
	inString := false
	for i := openIdx; i < len(src); i++ {
		ch := src[i]
		if inString {
			if ch == '\'' {
				// A doubled '' is an escaped quote, not a terminator.
				if i+1 < len(src) && src[i+1] == '\'' {
					i++
					continue
				}
				inString = false
			}
			continue
		}
		switch ch {
		case '\'':
			inString = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return src[openIdx+1 : i], true
			}
		}
	}
	return "", false
}

// isNotAColumnWord reports whether a captured word must not enter the column
// set. Two independent reasons: it can only be a FROM/JOIN keyword, or pg_dump
// can leave it alone on a line where a column reference is expected.
func isNotAColumnWord(word string) bool {
	w := strings.ToLower(word)
	return sqlKeywordAliases[w] || sqlStandaloneKeywords[w]
}

func isConstraintKeyword(word string) bool {
	switch strings.ToUpper(word) {
	case "PRIMARY", "FOREIGN", "CONSTRAINT", "UNIQUE", "CHECK", "EXCLUDE", "LIKE":
		return true
	}
	return false
}

func viewColumnSetHas(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// scanLiteralForSourcelessCols judges ONE SQL literal in isolation. Per-literal is
// the only scope that survives real handlers: a single Go declaration routinely
// carries two unrelated queries, and an alias in one of them says nothing about
// the other (198号 hit this: `FROM (VALUES …) AS v(id, priority)` in an UPDATE on
// credential_model_bindings, falsely attributed to the routing view).
func scanLiteralForSourcelessCols(literal string, governed map[string]bool, known map[string]bool) []string {
	aliases := map[string]bool{}
	for _, m := range viewAliasBind.FindAllStringSubmatch(literal, -1) {
		relation, alias := m[1], strings.ToLower(m[2])
		if governed[relation] && !sqlKeywordAliases[alias] {
			aliases[alias] = true
		}
	}
	names := make([]string, 0, len(aliases))
	for a := range aliases {
		names = append(names, a)
	}
	sort.Strings(names)

	var findings []string
	seen := map[string]bool{}
	for _, alias := range names {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(alias) + `\.([a-z_][a-z0-9_]*)`)
		for _, m := range re.FindAllStringSubmatch(literal, -1) {
			col := strings.ToLower(m[1])
			if known[col] || seen[alias+"."+col] {
				continue
			}
			seen[alias+"."+col] = true
			findings = append(findings, alias+"."+col)
		}
	}
	sort.Strings(findings)
	return findings
}

// goStringLiterals returns every string literal in a Go file. go/ast rather than
// a regex: a handler's SQL may live in a double-quoted string, a raw string, or
// be assembled with fmt.Sprintf from several pieces.
func goStringLiterals(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		// A file that does not parse is not this gate's business; skipping keeps
		// the gate from failing on unrelated syntax work in flight.
		return nil
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconvUnquote(lit.Value); err == nil {
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

func strconvUnquote(literal string) (string, error) {
	if len(literal) >= 2 && literal[0] == '`' {
		return literal[1 : len(literal)-1], nil
	}
	if len(literal) >= 2 && literal[0] == '"' {
		return unquoteDoubleQuoted(literal)
	}
	return "", fmt.Errorf("not a string literal")
}

func unquoteDoubleQuoted(literal string) (string, error) {
	body := literal[1 : len(literal)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) {
			i++
			switch body[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			default:
				b.WriteByte(body[i])
			}
			continue
		}
		b.WriteByte(body[i])
	}
	return b.String(), nil
}

// TestViewColumnContract_SourcelessColumnRefs is the gate.
//
// Why P2-grade: a reference to a column the schema does not have is a permanent
// 42703, and the dashboard read path downgrades that (admin/dashboard_degrade.go:59
// IsSchemaBehindError) into **200 + all zeros**. The operator sees "no spend this
// month" while the money was spent — worse than a 500, and nothing at runtime
// distinguishes the two.
func TestViewColumnContract_SourcelessColumnRefs(t *testing.T) {
	root := repoRootFromCaller(t)
	skipDir := map[string]bool{".git": true, "docs": true, "node_modules": true, "vendor": true}

	type derived struct {
		contract viewColumnContract
		known    map[string]bool
		governed map[string]bool
	}
	var all []derived
	for _, c := range viewColumnContracts {
		cols := deriveViewColumns(t, c)
		d := derived{contract: c, known: map[string]bool{}, governed: map[string]bool{}}
		for _, col := range cols {
			d.known[col] = true
		}
		for _, rel := range c.relations {
			d.governed[rel] = true
		}
		all = append(all, d)
		// Log the whole set, not just its size. Over-collection adds a column
		// that is not in `forbidden` and so trips no guard — printing the set
		// every run is what makes that visible instead of invisible.
		t.Logf("%s: %d columns derived from the baseline schema: %s", c.name, len(cols), strings.Join(cols, " "))
	}

	offenders := 0
	scanned := 0
	readersPerContract := make([]int, len(all))
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipDir[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		for di, d := range all {
			// Cheap pre-filter on the raw bytes before parsing: a file that never
			// names a governed relation cannot violate the contract.
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			relevant := false
			for rel := range d.governed {
				if strings.Contains(string(raw), rel) {
					relevant = true
					break
				}
			}
			if !relevant {
				continue
			}
			scanned++
			readersPerContract[di]++
			rel, _ := filepath.Rel(root, path)
			for _, literal := range goStringLiterals(t, path) {
				if !strings.Contains(literal, "SELECT") && !strings.Contains(literal, "select") {
					continue
				}
				findings := scanLiteralForSourcelessCols(literal, d.governed, d.known)
				if len(findings) == 0 {
					continue
				}
				offenders++
				t.Errorf("%s: query on %s references column(s) the schema does not have: %s\n"+
					"  a 42703 here is downgraded by IsSchemaBehindError into 200 + all zeros\n"+
					"  offending SQL: %s",
					rel, d.contract.name, strings.Join(findings, ", "), sqlSnippet(literal, findings))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	t.Logf("scanned %d (file, relation) pairs, %d offending literals", scanned, offenders)
	// The coverage assertion, stated where it can fail. Reporting the number is
	// not enough — a reader removed by a refactor, a directory added to
	// skipDir, or a repoRootFromCaller that resolves elsewhere all shrink the
	// scan without producing a single finding.
	for di, d := range all {
		t.Logf("%s: reached %d production readers (floor %d)", d.contract.name, readersPerContract[di], d.contract.minReaders)
		if readersPerContract[di] < d.contract.minReaders {
			t.Errorf("%s: the gate only reached %d production readers, expected at least %d.\n"+
				"  This is the failure mode a phantom-column gate cannot report on its own: a gate that\n"+
				"  sees less of the tree stops protecting the part it no longer sees. Raise minReaders\n"+
				"  only after checking that the readers were really deleted.",
				d.contract.name, readersPerContract[di], d.contract.minReaders)
		}
	}
}

// sqlSnippet returns the lines of a query around its first offending column
// reference. Reporting "string literal #40" instead makes the reader count
// string literals in the file to find the query; a gate that cannot be acted on
// without archaeology gets ignored.
func sqlSnippet(literal string, findings []string) string {
	lines := strings.Split(literal, "\n")
	hit, found := -1, false
	for n, line := range lines {
		lower := strings.ToLower(line)
		for _, f := range findings {
			if strings.Contains(lower, strings.ToLower(f)) {
				hit, found = n, true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return literal
	}
	lo := hit - 3
	if lo < 0 {
		lo = 0
	}
	hi := hit + 4
	if hi > len(lines) {
		hi = len(lines)
	}
	var b strings.Builder
	for n := lo; n < hi; n++ {
		marker := "   "
		if n == hit {
			marker = " >>"
		}
		b.WriteString(marker + " " + strings.TrimSpace(lines[n]) + "\n")
	}
	return b.String()
}

// TestViewColumnContract_DetectorDiscriminates is the gate's own premise: a
// detector that never fires must be distinguishable from a clean tree.
func TestViewColumnContract_DetectorDiscriminates(t *testing.T) {
	usage := viewColumnContracts[0]
	routing := viewColumnContracts[1]

	usageCols := deriveViewColumns(t, usage)
	usageKnown := map[string]bool{}
	usageGoverned := map[string]bool{}
	for _, c := range usageCols {
		usageKnown[c] = true
	}
	for _, r := range usage.relations {
		usageGoverned[r] = true
	}

	routingCols := deriveViewColumns(t, routing)
	routingKnown := map[string]bool{}
	routingGoverned := map[string]bool{}
	for _, c := range routingCols {
		routingKnown[c] = true
	}
	for _, r := range routing.relations {
		routingGoverned[r] = true
	}

	cases := []struct {
		name     string
		literal  string
		governed map[string]bool
		known    map[string]bool
		want     []string
	}{
		{
			name:     "original usage_enhanced bug is caught",
			literal:  "SELECT COUNT(DISTINCT ul.gw_session_id) FROM usage_ledger_with_current_month ul",
			governed: usageGoverned, known: usageKnown,
			want: []string{"ul.gw_session_id"},
		},
		{
			name:     "the three shipped phantoms are all caught",
			literal:  "SELECT ul.work_type, ul.compression_strategy FROM usage_ledger ul WHERE ul.total_tokens > 0",
			governed: usageGoverned, known: usageKnown,
			want: []string{"ul.compression_strategy", "ul.work_type"},
		},
		{
			name:     "real columns pass, including the one migration 736 added",
			literal:  "SELECT ul.cost_usd, ul.total_tokens, ul.rate_multiplier FROM usage_ledger ul",
			governed: usageGoverned, known: usageKnown,
			want: nil,
		},
		{
			name:     "same column on the right table is not a violation",
			literal:  "SELECT COUNT(DISTINCT l.gw_session_id) FROM request_logs l",
			governed: usageGoverned, known: usageKnown,
			want: nil,
		},
		{
			name: "a VALUES alias in another statement must not borrow the view's identity",
			// This is the real 198号 false positive: admin/routing.go:1335-1340
			// updates credential_model_bindings from a VALUES list aliased
			// v(id, priority) in the same file as a query on the routing view.
			literal:  "UPDATE credential_model_bindings SET manual_priority = v.priority FROM (VALUES ($1::bigint, $2::int)) AS v(id, priority) WHERE credential_model_bindings.id = v.id",
			governed: routingGoverned, known: routingKnown,
			want: nil,
		},
		{
			name:     "a real phantom on the routing view is caught",
			literal:  "SELECT v.billing_mode, v.weight FROM v_routable_credential_models v",
			governed: routingGoverned, known: routingKnown,
			want: []string{"v.weight"},
		},
		{
			name:     "routing view real columns pass",
			literal:  "SELECT v.is_routable, v.unavailable_reason, v.credential_label FROM v_routable_credential_models v WHERE v.is_routable",
			governed: routingGoverned, known: routingKnown,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scanLiteralForSourcelessCols(tc.literal, tc.governed, tc.known)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v (literal: %s)", got, tc.want, tc.literal)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
