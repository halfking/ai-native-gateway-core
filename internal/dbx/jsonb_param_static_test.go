package dbx

// Static regression guard for the pgx SimpleProtocol []byte→jsonb trap.
//
// docs/2026-09-05-pg-error-audit-and-environment.md §3.2 (and §1 row 3): the
// whole pool is created with pgx.QueryExecModeSimpleProtocol (db/db.go), under
// which a []byte argument is inlined as a bytea hex literal (`'\x7b22...'`).
// PostgreSQL cannot parse that as json/jsonb, so every write of a []byte into
// a jsonb/json column fails with SQLSTATE 22P02 "invalid input syntax for
// type json". Canonical bindings are, in order of preference:
//
//  1. internal/dbx.NormalizeJSONB (validates + returns string), or
//  2. string(json.Marshal(...)) at the call site plus `$N::text::jsonb` in
//     the SQL (see internal/trace/stage_events.go, proxy/store_pg.go,
//     bg/integrity_probe_sink.go for the established pattern).
//
// This test statically scans bg/ and domains/ for the known dangerous
// combination:
//
//   - a variable assigned from json.Marshal / json.MarshalIndent (or from a
//     helper whose Go signature returns []byte), passed directly as a SQL
//     argument in a query whose jsonb context is established either by an
//     explicit `$N[::text]::jsonb` cast, or by an INSERT/UPDATE that targets a
//     jsonb/json column according to sql/objects/tables/*.sql; or
//   - a `[]byte(...)` conversion passed directly as such an argument.
//
// Precision rules (low false positives by design):
//   - `string(...)`-wrapped arguments, NormalizeJSONB results, SQL literals
//     and plain literals are safe;
//   - a `// dbx:jsonb-safe` comment on the DB call (or on the offending
//     assignment) whitelists the site for the rare case where a []byte is
//     provably not bound to this pool (e.g. a different driver);
//   - unknown-typed arguments are treated as safe: the guard prefers silence
//     over noise, because the runtime trap is loud (22P02) while the guard is
//     only a first line of defense.
//
// This file is a repo-level lint test living in dbx only because that is
// where the binding contract is documented; it scans sibling packages via
// relative paths and does not exercise dbx code.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const jsonbSafeMarker = "dbx:jsonb-safe"

type jsonbStaticViolation struct {
	File   string
	Line   int
	Detail string
}

// ---------------------------------------------------------------------------
// Source scanning primitives
// ---------------------------------------------------------------------------

// jsonbBlankComments replaces comment bodies with spaces (newlines preserved so
// line numbers stay valid). String literal contents are kept.
func jsonbBlankComments(src string) string {
	out := []byte(src)
	i := 0
	for i < len(src) {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				j = len(src) - i
			}
			for k := i; k < i+j; k++ {
				out[k] = ' '
			}
			i += j
		case strings.HasPrefix(src[i:], "/*"):
			j := strings.Index(src[i+2:], "*/")
			end := len(src)
			if j >= 0 {
				end = i + 2 + j + 2
			}
			for k := i; k < end; k++ {
				if src[k] != '\n' {
					out[k] = ' '
				}
			}
			i = end
		case src[i] == '`':
			j := strings.IndexByte(src[i+1:], '`')
			if j < 0 {
				i = len(src)
				break
			}
			i += j + 2
		case src[i] == '"':
			j := i + 1
			for j < len(src) {
				if src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == '"' {
					break
				}
				j++
			}
			i = j + 1
		case src[i] == '\'':
			j := i + 1
			for j < len(src) {
				if src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == '\'' {
					break
				}
				j++
			}
			i = j + 1
		default:
			i++
		}
	}
	return string(out)
}

// jsonbSpanLines returns the raw source lines start..end (1-based, inclusive);
// used for whitelist detection.
func jsonbSpanLines(src string, start, end int) []string {
	lines := strings.Split(src, "\n")
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return nil
	}
	return lines[start-1 : end]
}

// jsonbExtractCalls finds every `recv.Exec(/Query(/QueryRow(...)` style call in
// blanked source and returns the call argument text plus its 1-based start and
// end line numbers.
func jsonbExtractCalls(blanked string) []struct {
	Args  string
	Start int
	End   int
} {
	reCall := regexp.MustCompile(`\.\s*(Exec|ExecContext|Query|QueryContext|QueryRow|QueryRowContext)\s*\(`)
	var calls []struct {
		Args  string
		Start int
		End   int
	}
	for _, loc := range reCall.FindAllStringIndex(blanked, -1) {
		// reCall matched up to and including '(' (the regex ends with `\(`),
		// so the open paren is the last matched character.
		openIdx := loc[1] - 1
		depth := 0
		i := openIdx
		closed := -1
		for i < len(blanked) {
			switch {
			case blanked[i] == '`':
				j := strings.IndexByte(blanked[i+1:], '`')
				if j < 0 {
					i = len(blanked)
					continue
				}
				i += j + 2
				continue
			case blanked[i] == '"':
				j := i + 1
				for j < len(blanked) {
					if blanked[j] == '\\' {
						j += 2
						continue
					}
					if blanked[j] == '"' {
						break
					}
					j++
				}
				i = j + 1
				continue
			case blanked[i] == '(':
				depth++
			case blanked[i] == ')':
				depth--
				if depth == 0 {
					closed = i
				}
			}
			if closed >= 0 {
				break
			}
			i++
		}
		if closed < 0 {
			continue
		}
		calls = append(calls, struct {
			Args  string
			Start int
			End   int
		}{
			Args:  blanked[openIdx+1 : closed],
			Start: strings.Count(blanked[:loc[0]], "\n") + 1,
			End:   strings.Count(blanked[:closed+1], "\n") + 1,
		})
	}
	return calls
}

// jsonbSplitTopLevelArgs splits a call argument list at depth-0 commas,
// respecting string literals.
func jsonbSplitTopLevelArgs(args string) []string {
	var out []string
	depth := 0
	start := 0
	i := 0
	for i < len(args) {
		switch {
		case args[i] == '`':
			j := strings.IndexByte(args[i+1:], '`')
			if j < 0 {
				i = len(args)
				continue
			}
			i += j + 2
			continue
		case args[i] == '"':
			j := i + 1
			for j < len(args) {
				if args[j] == '\\' {
					j += 2
					continue
				}
				if args[j] == '"' {
					break
				}
				j++
			}
			i = j + 1
			continue
		case args[i] == '(':
			depth++
		case args[i] == ')':
			depth--
		case args[i] == ',' && depth == 0:
			out = append(out, args[start:i])
			start = i + 1
		}
		i++
	}
	out = append(out, args[start:])
	return out
}

// ---------------------------------------------------------------------------
// Known jsonb columns per table (from sql/objects/tables/*.sql)
// ---------------------------------------------------------------------------

var (
	jsonbReCreateTable = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?"?(\w+)"?\s*\((.*)\)\s*;?`)
	jsonbReColumn      = regexp.MustCompile(`(?m)^\s*(\w+)\s+jsonb?\b`)
)

func jsonbLoadSchema(root string) map[string]map[string]bool {
	schema := map[string]map[string]bool{}
	dir := filepath.Join(root, "sql", "objects", "tables")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return schema
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if m := jsonbReCreateTable.FindStringSubmatch(string(data)); m != nil {
			tbl := m[1]
			cols := schema[tbl]
			if cols == nil {
				cols = map[string]bool{}
			}
			for _, c := range jsonbReColumn.FindAllStringSubmatch(m[2], -1) {
				cols[c[1]] = true
			}
			schema[tbl] = cols
		}
	}
	return schema
}

// ---------------------------------------------------------------------------
// []byte-producing functions and per-file []byte variables
// ---------------------------------------------------------------------------

var (
	jsonbReFuncMulti  = regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*\([^)]*\)\s*\(([^)]*)\)\s*\{`)
	jsonbReFuncSingle = regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*\([^)]*\)\s*\[\]byte\s*\{`)
	jsonbReAssign     = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_]\w*(?:[ \t]*,[ \t]*[A-Za-z_]\w*)*)[ \t]*:?[ \t]*=[ \t]*([A-Za-z_][\w.]*)[ \t]*\(`)
	jsonbReVarDecl    = regexp.MustCompile(`(?m)^[ \t]*var\s+([A-Za-z_]\w*)\s+\[\]byte\b`)
	jsonbReVarConv    = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_]\w*)[ \t]*:?[ \t]*=[ \t]*\[\]byte\(`)
	jsonbReByteArg    = regexp.MustCompile(`^\[\]byte\(`)
	jsonbRePlainIdent = regexp.MustCompile(`^[A-Za-z_]\w*$`)
)

// jsonbCollectByteFuncs scans files for functions returning []byte (position of
// the []byte in the return list is recorded) and seeds the two stdlib entries.
func jsonbCollectByteFuncs(files []string) map[string][]int {
	funcs := map[string][]int{
		"json.Marshal":       {0},
		"json.MarshalIndent": {0},
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		blanked := jsonbBlankComments(string(data))
		for _, m := range jsonbReFuncMulti.FindAllStringSubmatch(blanked, -1) {
			name := m[1]
			parts := strings.Split(m[2], ",")
			for pos, p := range parts {
				field := strings.TrimSpace(p)
				if field == "[]byte" || strings.HasSuffix(field, "[]byte") {
					funcs[name] = append(funcs[name], pos)
					break
				}
			}
		}
		for _, m := range jsonbReFuncSingle.FindAllStringSubmatch(blanked, -1) {
			funcs[m[1]] = append(funcs[m[1]], 0)
		}
	}
	return funcs
}

// jsonbCollectByteVars returns the []byte variables of one file (name →
// assignment lines, 1-based, ascending; a name may be reused across functions).
func jsonbCollectByteVars(blanked string, byteFuncs map[string][]int) map[string][]int {
	vars := map[string][]int{}
	assignLine := func(name string, line int) {
		if name != "_" && name != "" {
			vars[name] = append(vars[name], line)
		}
	}
	for i, line := range strings.Split(blanked, "\n") {
		lineNo := i + 1
		for _, m := range jsonbReAssign.FindAllStringSubmatch(line, -1) {
			names := strings.Split(m[1], ",")
			callee := strings.TrimSpace(m[2])
			positions, ok := byteFuncs[callee]
			if !ok {
				// also try bare name for qualified callees (pkg.Helper)
				if idx := strings.LastIndexByte(callee, '.'); idx >= 0 {
					positions, ok = byteFuncs[callee[idx+1:]]
				}
			}
			if !ok {
				continue
			}
			for _, pos := range positions {
				if pos < len(names) {
					assignLine(strings.TrimSpace(names[pos]), lineNo)
				}
			}
		}
		for _, m := range jsonbReVarDecl.FindAllStringSubmatch(line, -1) {
			assignLine(m[1], lineNo)
		}
		for _, m := range jsonbReVarConv.FindAllStringSubmatch(line, -1) {
			assignLine(m[1], lineNo)
		}
	}
	return vars
}

// jsonbNearestByteLine picks the assignment closest above the call, falling
// back to the first one (single-function files).
func jsonbNearestByteLine(lines []int, callLine int) int {
	best := -1
	for _, l := range lines {
		if l <= callLine && l > best {
			best = l
		}
	}
	if best == -1 {
		best = lines[0]
	}
	return best
}

// ---------------------------------------------------------------------------
// SQL-side jsonb context detection
// ---------------------------------------------------------------------------

var (
	jsonbReCastParam = regexp.MustCompile(`\$(\d+)(?:::text)?::jsonb?\b`)
	jsonbReCastAS    = regexp.MustCompile(`CAST\s*\(\s*\$(\d+)\s+AS\s+jsonb?\s*\)`)
	jsonbReInsert    = regexp.MustCompile(`(?is)INSERT\s+INTO\s+(?:ONLY\s+)?(?:public\.)?"?(\w+)"?\s*\(([^)]*)\)\s*VALUES\s*\(([^)]*)\)`)
	jsonbReUpdate    = regexp.MustCompile(`(?is)UPDATE\s+(?:public\.)?"?(\w+)"?\s+SET\s+(.*?)(?:\s+WHERE\b|\s+ON\s+CONFLICT\b|\s+RETURNING\b|$)`)
	jsonbReUpdateCol = regexp.MustCompile(`(\w+)\s*=\s*\$(\d+)`)
	jsonbReParamN    = regexp.MustCompile(`\$(\d+)`)
	jsonbReLiteral   = regexp.MustCompile("(?s)\\b([A-Za-z_]\\w*)[ \\t]*:?=[ \\t]*`([^`]*)`")
)

// jsonbResolveSQL returns the SQL text for the first call argument, if it can
// be resolved (inline literal or a repo-known string/const identifier).
func jsonbResolveSQL(expr string, literals map[string]string) (string, bool) {
	expr = strings.TrimSpace(expr)
	switch {
	case strings.HasPrefix(expr, "`"):
		if end := strings.IndexByte(expr[1:], '`'); end >= 0 {
			return expr[1 : 1+end], true
		}
	case strings.HasPrefix(expr, `"`):
		if unquoted, err := strconv.Unquote(expr); err == nil {
			return unquoted, true
		}
	default:
		if sql, ok := literals[expr]; ok {
			return sql, true
		}
	}
	return "", false
}

// jsonbJSONBParams returns the set of 1-based placeholder positions bound to a
// jsonb/json context in the given SQL text.
func jsonbJSONBParams(sql string, schema map[string]map[string]bool) map[int]bool {
	set := map[int]bool{}
	for _, m := range jsonbReCastParam.FindAllStringSubmatch(sql, -1) {
		n, _ := strconv.Atoi(m[1])
		set[n] = true
	}
	for _, m := range jsonbReCastAS.FindAllStringSubmatch(sql, -1) {
		n, _ := strconv.Atoi(m[1])
		set[n] = true
	}
	if m := jsonbReInsert.FindStringSubmatch(sql); m != nil {
		tbl := m[1]
		cols := splitClean(m[2], ",")
		vals := splitClean(m[3], ",")
		for i, col := range cols {
			if i < len(vals) && schema[tbl] != nil && schema[tbl][col] {
				for _, p := range jsonbReParamN.FindAllStringSubmatch(vals[i], -1) {
					n, _ := strconv.Atoi(p[1])
					set[n] = true
				}
			}
		}
	}
	if m := jsonbReUpdate.FindStringSubmatch(sql); m != nil {
		tbl := m[1]
		for _, c := range jsonbReUpdateCol.FindAllStringSubmatch(m[2], -1) {
			if schema[tbl] != nil && schema[tbl][c[1]] {
				n, _ := strconv.Atoi(c[2])
				set[n] = true
			}
		}
	}
	return set
}

func splitClean(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// ---------------------------------------------------------------------------
// The scanner
// ---------------------------------------------------------------------------

// jsonbScanFiles scans the given .go files for []byte→jsonb violations.
func jsonbScanFiles(files []string, schema map[string]map[string]bool) ([]jsonbStaticViolation, int) {
	byteFuncs := jsonbCollectByteFuncs(files)
	literals := jsonbRepoLiterals(files)
	var violations []jsonbStaticViolation
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		raw := string(data)
		blanked := jsonbBlankComments(raw)
		byteVars := jsonbCollectByteVars(blanked, byteFuncs)
		for _, call := range jsonbExtractCalls(blanked) {
			args := jsonbSplitTopLevelArgs(call.Args)
			if len(args) == 0 {
				continue
			}
			// pgx signatures lead with the context: Exec(ctx, sql, args...).
			// The SQL argument is the first argument that is a string literal
			// or resolves to a known literal identifier; a plain identifier
			// that is NOT a known literal is the context, so the SQL (if any)
			// sits at index 1.
			sqlIdx := -1
			e0 := strings.TrimSpace(args[0])
			switch {
			case strings.HasPrefix(e0, "`"), strings.HasPrefix(e0, `"`):
				sqlIdx = 0
			default:
				if _, isLiteral := literals[e0]; isLiteral {
					sqlIdx = 0
				} else if len(args) > 1 {
					sqlIdx = 1
				}
			}
			if sqlIdx < 0 {
				continue
			}
			sql, ok := jsonbResolveSQL(args[sqlIdx], literals)
			if !ok {
				continue
			}
			jsonbSet := jsonbJSONBParams(sql, schema)
			if len(jsonbSet) == 0 {
				continue
			}
			// whitelist: marker within the call span, or on the line directly
			// above the call
			whitelisted := false
			for _, ln := range jsonbSpanLines(raw, call.Start-1, call.End) {
				if strings.Contains(ln, jsonbSafeMarker) {
					whitelisted = true
					break
				}
			}
			if whitelisted {
				continue
			}
			params := args[sqlIdx+1:]
			for n := range jsonbSet {
				if n-1 >= len(params) {
					continue
				}
				expr := strings.TrimSpace(params[n-1])
				switch {
				case expr == "", expr == "nil",
					strings.HasPrefix(expr, "string("),
					strings.HasPrefix(expr, "`"),
					strings.HasPrefix(expr, `"`),
					strings.HasPrefix(expr, "NULL"),
					strings.Contains(expr, "NormalizeJSONB"):
					continue
				}
				// []byte literal conversion straight into a jsonb param
				if jsonbReByteArg.MatchString(expr) {
					violations = append(violations, jsonbStaticViolation{
						File:   f,
						Line:   call.Start,
						Detail: "[]byte(...) passed directly as a jsonb/json parameter; use string(...) + ::text::jsonb (doc §3.2)",
					})
					continue
				}
				if jsonbRePlainIdent.MatchString(expr) {
					if lines, isByte := byteVars[expr]; isByte {
						line := jsonbNearestByteLine(lines, call.Start)
						// whitelist on the assignment line too
						safe := false
						for _, ln := range jsonbSpanLines(raw, line, line) {
							if strings.Contains(ln, jsonbSafeMarker) {
								safe = true
								break
							}
						}
						if safe {
							continue
						}
						violations = append(violations, jsonbStaticViolation{
							File: f,
							Line: call.Start,
							Detail: expr + " (assigned []byte at line " + strconv.Itoa(line) +
								") passed directly as a jsonb/json parameter; use string(...) + ::text::jsonb or dbx.NormalizeJSONB (doc §3.2)",
						})
					}
				}
			}
		}
	}
	return violations, len(files)
}

// jsonbRepoLiterals builds an identifier → backtick literal map so calls whose
// SQL lives in a package-level const/var can still be resolved.
func jsonbRepoLiterals(files []string) map[string]string {
	// cached per invocation set; cheap enough for test use
	m := map[string]string{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		blanked := jsonbBlankComments(string(data))
		for _, loc := range jsonbReLiteral.FindAllStringSubmatchIndex(blanked, -1) {
			name := blanked[loc[2]:loc[3]]
			m[name] = blanked[loc[4]:loc[5]]
		}
	}
	return m
}

func jsonbCollectGoFiles(roots []string) ([]string, error) {
	var files []string
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == "vendor" || info.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestJSONBParamScanSelfCheck feeds known-bad and known-good fixtures through
// the scanner to prove the detection logic itself works (a scanner that never
// fires is not a guard).
func TestJSONBParamScanSelfCheck(t *testing.T) {
	files := map[string]string{
		"bad_marshal.go": `package sample

import "encoding/json"

func Bad(db execer) {
	payload, _ := json.Marshal(map[string]string{"a": "1"})
	_, _ = db.Exec(` + "`" + `INSERT INTO t (data) VALUES ($1::jsonb)` + "`" + `, payload)
}
`,
		"bad_byteconv.go": `package sample

func Bad2(db execer, raw string) {
	_, _ = db.Query(` + "`" + `SELECT * FROM t WHERE d = $1::jsonb` + "`" + `, []byte(raw))
}
`,
		"good_string.go": `package sample

import "encoding/json"

func Good(db execer) {
	payload, _ := json.Marshal(map[string]string{"a": "1"})
	_, _ = db.Exec(` + "`" + `INSERT INTO t (data) VALUES ($1::text::jsonb)` + "`" + `, string(payload))
}
`,
		"good_whitelist.go": `package sample

import "encoding/json"

func Whitelisted(db execer) {
	payload, _ := json.Marshal(map[string]string{"a": "1"})
	// dbx:jsonb-safe: bound to a non-gateway pool with binary protocol
	_, _ = db.Exec(` + "`" + `INSERT INTO t (data) VALUES ($1::jsonb)` + "`" + `, payload)
}
`,
		"good_nonjsonb.go": `package sample

import "encoding/json"

func NonJSONB(db execer) {
	payload, _ := json.Marshal(map[string]string{"a": "1"})
	_, _ = db.Exec(` + "`" + `INSERT INTO t (data) VALUES ($1::text)` + "`" + `, payload)
}
`,
	}
	dir := t.TempDir()
	var paths []string
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	violations, scanned := jsonbScanFiles(paths, map[string]map[string]bool{})
	if scanned != len(paths) {
		t.Fatalf("scanner did not visit all fixtures: %d/%d", scanned, len(paths))
	}
	byFile := map[string][]jsonbStaticViolation{}
	for _, v := range violations {
		byFile[filepath.Base(v.File)] = append(byFile[filepath.Base(v.File)], v)
	}
	for _, want := range []string{"bad_marshal.go", "bad_byteconv.go"} {
		if len(byFile[want]) != 1 {
			t.Errorf("%s: expected exactly 1 violation, got %d (%+v)", want, len(byFile[want]), byFile[want])
		}
	}
	for _, clean := range []string{"good_string.go", "good_whitelist.go", "good_nonjsonb.go"} {
		if len(byFile[clean]) != 0 {
			t.Errorf("%s: expected 0 violations, got %d (%+v)", clean, len(byFile[clean]), byFile[clean])
		}
	}
}

// TestJSONBParamScanBGAndDomainsClean runs the scanner over the real bg/ and
// domains/ trees and fails on any residual []byte→jsonb violation.
func TestJSONBParamScanBGAndDomainsClean(t *testing.T) {
	roots := []string{filepath.Join("..", "..", "bg"), filepath.Join("..", "..", "domains")}
	files, err := jsonbCollectGoFiles(roots)
	if err != nil {
		t.Fatal(err)
	}
	// The scan must actually cover the trees; if the layout ever moves, this
	// guard must be repointed rather than silently scanning nothing.
	if len(files) < 300 {
		t.Fatalf("jsonb static scan only found %d Go files under bg/ + domains/ — scan coverage regressed", len(files))
	}
	sentinels := []string{
		// previously fixed site (doc §1 row 3)
		filepath.Join("..", "..", "bg", "integrity_probe_sink.go"),
		// the other known-clean jsonb writers, kept as coverage anchors
		filepath.Join("..", "..", "domains", "streaming", "integrity", "recorder.go"),
		filepath.Join("..", "..", "bg", "provider_error_aggregator.go"),
	}
	for _, s := range sentinels {
		found := false
		for _, f := range files {
			if f == s {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("scan set does not include coverage anchor %s", s)
		}
	}

	schema := jsonbLoadSchema(filepath.Join("..", ".."))
	// The schema source must keep resolving the columns behind the two
	// historically broken writers.
	if schema["tuning_proposals"]["proposal"] != true || schema["tuning_proposals"]["evidence"] != true {
		t.Error("schema map lost tuning_proposals.proposal/evidence jsonb columns; sql/objects/tables parsing regressed")
	}
	if schema["fault_events"]["metadata"] != true {
		t.Error("schema map lost fault_events.metadata jsonb column; sql/objects/tables parsing regressed")
	}

	violations, _ := jsonbScanFiles(files, schema)
	for _, v := range violations {
		rel, _ := filepath.Rel(filepath.Join("..", ".."), v.File)
		t.Errorf("%s:%d: %s", rel, v.Line, v.Detail)
	}
	t.Logf("jsonb static scan visited %d non-test Go files under bg/ + domains/, violations: %d", len(files), len(violations))
}
