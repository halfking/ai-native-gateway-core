// Command schemadiff compares the live 34-side PG17 pg_dump against the repository
// SSOT schema file (which the project documents as regenerated from 252 pg-252-pg17).
//
//	go run .db-audit/schemadiff/main.go -live .db-audit/out/34_schema.sql -repo sql/schema/01-schema.sql
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

type table struct {
	name    string
	cols    map[string]string // column -> type text
	partOf  string
	isPart  bool
	comment string
}

type index struct {
	name  string
	table string
	def   string
}

var (
	// pg_dump form: CREATE TABLE public.foo (
	reDumpTable = regexp.MustCompile(`^CREATE TABLE (\S+) \(\s*$`)
	// project DDL: CREATE TABLE [IF NOT EXISTS] [public.]foo (
	reRepoTable = regexp.MustCompile(`(?i)^CREATE\s+(?:UNLOGGED\s+|TEMP\s+|LOCAL\s+TEMP\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?((?:public\.)?"?[A-Za-z_][\w$]*"?)`)
	reRepoPart  = regexp.MustCompile(`(?i)PARTITION\s+OF\s+((?:public\.)?"?[A-Za-z_][\w$]*"?)`)
	reIdent     = regexp.MustCompile(`"(?:[^"]|"")*"|[A-Za-z_][\w$]*`)
	// column line inside a table body. Both the column name and the type may be
	// double-quoted because the live dump was produced with --quote-all-identifiers.
	reCol = regexp.MustCompile(`^\s*("(?:[^"]|"")*"|[A-Za-z_][\w$]*)\s+("(?:[^"]|"")*"|[A-Za-z][\w]*(?:\s+[A-Za-z][\w]*)*?(?:\s*\([^)]*\))?(?:\[\])*(?:\[\])?)`)
	// constraint starts that must not be read as columns
	kwConstraint = map[string]bool{"constraint": true, "primary": true, "unique": true, "check": true, "foreign": true, "exclude": true}
	reIndex      = regexp.MustCompile(`(?i)^CREATE\s+(UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?("(?:[^"]|"")*"|[A-Za-z_][\w$]*)\s+ON\s+(?:ONLY\s+)?("(?:[^"]|"")*"|[A-Za-z_][\w$]*)`)
	reAttach     = regexp.MustCompile(`(?i)^ALTER\s+TABLE\s+(?:ONLY\s+)?[^\s]+\s+ATTACH\s+PARTITION\s+("(?:[^"]|"")*"|[A-Za-z_][\w$]*(?:\."(?:[^"]|"")*"|\.[A-Za-z_][\w$]*)?)`)
)

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

func normIdent(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return strings.ToLower(unquote(s))
}

func normType(s string) string {
	// strip quotes, drop a redundant public. qualifier, collapse whitespace:
	// "timestamp" with "time zone" -> timestamp with time zone
	// public.injection_action        -> injection_action
	fields := strings.Fields(s)
	for i, f := range fields {
		f = strings.ToLower(unquote(f))
		f = strings.TrimPrefix(f, "public.")
		fields[i] = f
	}
	return strings.Join(fields, " ")
}

// matchParen returns the index of the ')' that closes the '(' at open, or -1.
func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
func splitTop(s string) []string {
	var out []string
	var cur strings.Builder
	depth, inQ := 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQ:
			if c == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					cur.WriteByte(c)
					cur.WriteByte(s[i+1])
					i++
					continue
				}
				inQ = false
			}
		case c == '"':
			inQ = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out, sc.Err()
}

// splitSchema splits a possibly schema-qualified, possibly quoted identifier
// (as emitted by pg_dump --quote-all-identifiers) into its parts.
func splitSchema(s string) (schema, name string) {
	s = strings.TrimSpace(s)
	var parts []string
	var cur strings.Builder
	inQ := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQ {
			if c == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					cur.WriteByte(c)
					cur.WriteByte(s[i+1])
					i++
					continue
				}
				inQ = false
				continue
			}
			cur.WriteByte(c)
			continue
		}
		switch c {
		case '"':
			inQ = true
		case '.':
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	parts = append(parts, cur.String())
	if len(parts) == 1 {
		return "public", unquote(parts[0])
	}
	return unquote(parts[0]), unquote(parts[1])
}

func loadDump(path string) (map[string]*table, []index) {
	lines, err := readLines(path)
	if err != nil {
		panic(err)
	}
	tables := map[string]*table{}
	var idxs []index
	var cur *table
	var depth int
	// after depth returns to 0 we still need to see a trailing
	// "PARTITION OF parent" clause, which pg_dump emits on its own line.
	tail := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if cur == nil {
			if m := reDumpTable.FindStringSubmatch(t); m != nil {
				schema, name := splitSchema(m[1])
				if schema != "public" {
					continue
				}
				cur = &table{name: strings.ToLower(name), cols: map[string]string{}}
				tables[cur.name] = cur
				depth = 1
			} else if m := reIndex.FindStringSubmatch(t); m != nil {
								schema, tbl := splitSchema(m[3])
				if schema == "public" {
					idxs = append(idxs, index{name: strings.ToLower(unquote(m[2])), table: strings.ToLower(tbl), def: t})
				}
			}
			continue
		}
		if tail {
			// Stay in tail mode across blank lines: pg_dump emits a blank line after
			// ");" for ordinary tables but writes "PARTITION OF ..." immediately
			// after the closing paren for partitions.
			if t == "" {
				continue
			}
			if strings.HasPrefix(t, "PARTITION OF ") {
				cur.isPart = true
				cur.partOf = normIdent(reIdent.FindString(t))
			}
			cur = nil
			tail = false
			continue
		}
		depth += strings.Count(t, "(") - strings.Count(t, ")")
		if depth <= 0 {
			tail = true
			if strings.HasPrefix(t, "PARTITION OF ") {
				cur.isPart = true
				cur.partOf = normIdent(reIdent.FindString(t))
			}
			continue
		}
		if kwConstraint[strings.ToLower(strings.Fields(t+" ")[0])] {
			continue
		}
		m := reCol.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		name := normIdent(m[1])
		typ := normType(m[2])
		if name == "" || typ == "" {
			continue
		}
		if _, dup := cur.cols[name]; !dup {
			cur.cols[name] = typ
		}
	}
	// pg_dump emits partitions as plain CREATE TABLE plus a separate
	// "ALTER TABLE ONLY <parent> ATTACH PARTITION <child> ..." statement.
	for _, ln := range lines {
		if m := reAttach.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
			_, child := splitSchema(m[1])
			if t, ok := tables[strings.ToLower(child)]; ok {
				t.isPart = true
			}
		}
	}
	return tables, idxs
}

// repoType extracts the full type text from a column definition tail. A regex for
// the type alone truncates multi-word builtins ("timestamp with time zone" ->
// "timestamp", "character varying(64)" -> "character"), which then shows up as a
// phantom type diff. Cutting at the first column-constraint keyword is exact.
func repoType(rest string) string {
	up := strings.ToUpper(rest)
	cut := len(rest)
	for _, kw := range []string{" NOT NULL", " NULL", " DEFAULT", " PRIMARY KEY", " PRIMARY", " UNIQUE", " REFERENCES", " CHECK", " COLLATE", " GENERATED", " CONSTRAINT", " ON CONFLICT", " ON UPDATE"} {
		if i := strings.Index(up, kw); i >= 0 && i < cut {
			cut = i
		}
	}
	return strings.TrimSpace(rest[:cut])
}

func loadRepo(path string) map[string]*table {
	raw, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	txt := strings.ReplaceAll(string(raw), "\r\n", "\n")
	// strip comments
	var noBlock strings.Builder
	{
		inBlock := false
		for _, line := range strings.Split(txt, "\n") {
			if inBlock {
				if i := strings.Index(line, "*/"); i >= 0 {
					line = line[i+2:]
					inBlock = false
				} else {
					continue
				}
			}
			for {
				i := strings.Index(line, "/*")
				if i < 0 {
					break
				}
				j := strings.Index(line[i:], "*/")
				if j < 0 {
					line = line[:i]
					inBlock = true
					break
				}
				line = line[:i] + " " + line[i+j+2:]
			}
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
			noBlock.WriteString(line)
			noBlock.WriteByte('\n')
		}
	}
	txt = noBlock.String()

	tables := map[string]*table{}
	// split into statements on top-level semicolons
	stmts := splitStatements(txt)
	for _, st := range stmts {
		s := strings.TrimSpace(st)
		if !strings.HasPrefix(strings.ToUpper(s), "CREATE") {
			continue
		}
		loc := reRepoTable.FindStringSubmatch(s)
		if loc == nil {
			continue
		}
		t := &table{name: normIdent(loc[1]), cols: map[string]string{}}
		open := strings.Index(s, "(")
		if open < 0 {
			continue
		}
		closeIdx := matchParen(s, open)
		if closeIdx <= open {
			continue
		}
		body := s[open+1 : closeIdx]
		if m := reRepoPart.FindStringSubmatch(s); m != nil {
			t.isPart = true
			t.partOf = normIdent(m[1])
		}
		for _, part := range splitTop(body) {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			head := strings.ToLower(strings.Fields(p)[0])
			if kwConstraint[head] {
				continue
			}
			m := reCol.FindStringSubmatch(p)
			if m == nil {
				continue
			}
			name := normIdent(m[1])
			typ := normType(repoType(p[len(m[0])-len(m[2]):]))
			if name == "" || typ == "" {
				continue
			}
			if _, dup := t.cols[name]; !dup {
				t.cols[name] = typ
			}
		}
		tables[t.name] = t
	}
	return tables
}

func splitStatements(s string) []string {
	var out []string
	var cur strings.Builder
	depth, inQ, inDollar := 0, false, ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inDollar != "" {
			cur.WriteByte(c)
			if strings.HasPrefix(s[i:], inDollar) {
				cur.WriteString(s[i+1 : i+len(inDollar)])
				i += len(inDollar) - 1
				inDollar = ""
			}
			continue
		}
		switch {
		case inQ:
			if c == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					cur.WriteByte(c)
					cur.WriteByte(s[i+1])
					i++
					continue
				}
				inQ = false
			}
		case c == '"':
			inQ = true
		case (c == '$') && (i == 0 || !isIdentByte(s[i-1])):
			if j := strings.Index(s[i+1:], "$"); j >= 0 {
				inDollar = s[i : i+j+2]
				cur.WriteString(inDollar)
				i += len(inDollar) - 1
				continue
			}
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ';' && depth == 0:
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// loadLiveTSV consumes the authoritative inventory emitted by
// sql/audit/2026-10-02-db-audit-collect.sql style queries:
//   table|column|type       (base tables only, partitions excluded)
//   ===PARTITIONED_PARENT_MARKER===  then  table|partkey
//   ===INDEX_INVENTORY===            then  index|table|definition
// This is preferred over parsing pg_dump output because partition detection and
// quoted identifiers are handled by the server rather than by this parser.
func loadLiveTSV(path string) (map[string]*table, []index, map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	tables := map[string]*table{}
	var idxs []index
	parents := map[string]string{}
	mode := "cols"
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "ERROR") || strings.HasPrefix(t, "psql:") {
			continue
		}
		switch {
		case strings.HasPrefix(t, "===PARTITIONED_PARENT_MARKER==="):
			mode = "parents"
			continue
		case strings.HasPrefix(t, "===INDEX_INVENTORY==="):
			mode = "indexes"
			continue
		}
		parts := strings.Split(t, "|")
		switch mode {
		case "cols":
			if len(parts) < 3 {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(parts[0]))
			tbl, ok := tables[name]
			if !ok {
				tbl = &table{name: name, cols: map[string]string{}}
				tables[name] = tbl
			}
			tbl.cols[strings.ToLower(strings.TrimSpace(parts[1]))] = normType(parts[2])
		case "parents":
			if len(parts) >= 2 {
				parents[strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
			}
		case "indexes":
			if len(parts) >= 3 {
				idxs = append(idxs, index{
					name:  strings.ToLower(strings.TrimSpace(parts[0])),
					table: strings.ToLower(strings.TrimSpace(parts[1])),
					def:   strings.TrimSpace(parts[2]),
				})
			}
		}
	}
	return tables, idxs, parents, nil
}

func main() {
	livePath := flag.String("live", "", "live pg_dump --schema-only (ignored when -live-tsv is set)")
	liveTSV := flag.String("live-tsv", "", "authoritative live inventory TSV (preferred)")
	repoPath := flag.String("repo", "sql/schema/01-schema.sql", "repo SSOT schema")
	flag.Parse()

	var live map[string]*table
	var liveIdx []index
	var liveParents map[string]string
	if *liveTSV != "" {
		var err error
		live, liveIdx, liveParents, err = loadLiveTSV(*liveTSV)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read live tsv:", err)
			os.Exit(1)
		}
	} else {
		live, liveIdx = loadDump(*livePath)
	}
	repo := loadRepo(*repoPath)

	// Only compare base tables (skip partitions: they are runtime-created on both sides).
	liveBase := map[string]*table{}
	for n, t := range live {
		if !t.isPart {
			liveBase[n] = t
		}
	}
	repoBase := map[string]*table{}
	for n, t := range repo {
		if !t.isPart {
			repoBase[n] = t
		}
	}

	var onlyLive, onlyRepo []string
	isBackup := func(n string) bool { return strings.HasPrefix(n, "bak_") }
	for n := range liveBase {
		if isBackup(n) {
			continue
		}
		if _, ok := repoBase[n]; !ok {
			onlyLive = append(onlyLive, n)
		}
	}
	for n := range repoBase {
		if _, ok := liveBase[n]; !ok {
			onlyRepo = append(onlyRepo, n)
		}
	}
	sort.Strings(onlyLive)
	sort.Strings(onlyRepo)

	var liveBackups []string
	for n := range liveBase {
		if isBackup(n) {
			liveBackups = append(liveBackups, n)
		}
	}
	sort.Strings(liveBackups)

	fmt.Printf("== SUMMARY ==\n")
	fmt.Printf("live public relations (base tables, excl. partitions): %d\n", len(liveBase)-len(liveBackups))
	fmt.Printf("live partitions: %d\n", len(live)-len(liveBase))
	fmt.Printf("live bak_* backup tables: %d\n", len(liveBackups))
	fmt.Printf("repo base tables: %d\n", len(repoBase))
	fmt.Printf("live indexes:     %d\n", len(liveIdx))
	fmt.Printf("only in live (excl. bak_*): %d\nonly in repo: %d\n", len(onlyLive), len(onlyRepo))
	fmt.Printf("\n== bak_* BACKUP TABLES PRESENT ONLY IN LIVE 34 (%d) ==\n", len(liveBackups))
	for _, n := range liveBackups {
		fmt.Printf("  ! %s\n", n)
	}

	fmt.Printf("\n== TABLES ONLY IN LIVE 34 (%d) ==\n", len(onlyLive))
	for _, n := range onlyLive {
		fmt.Printf("  + %s (%d cols)\n", n, len(liveBase[n].cols))
	}
	fmt.Printf("\n== TABLES ONLY IN REPO 252-SSOT (%d) ==\n", len(onlyRepo))
	for _, n := range onlyRepo {
		fmt.Printf("  - %s (%d cols)\n", n, len(repoBase[n].cols))
	}

	if len(liveParents) > 0 {
		fmt.Printf("\n== PARTITION KEYS (live, %d partitioned parents) ==\n", len(liveParents))
		var pnames []string
		for n := range liveParents {
			pnames = append(pnames, n)
		}
		sort.Strings(pnames)
		for _, n := range pnames {
			inRepo := "no"
			if rt, ok := repoBase[n]; ok {
				if m := reRepoPart.FindStringSubmatch(rt.partOf + rt.name); m != nil {
					inRepo = m[0]
				}
				if rt.isPart {
					inRepo = "partition (" + rt.partOf + ")"
				}
			}
			fmt.Printf("  %-40s key=%-28s repo_view=%s\n", n, liveParents[n], inRepo)
		}
	}

	// column-level diff for common tables
	type cdiff struct {
		table     string
		missing   []string // in repo, not in live
		extra     []string // in live, not in repo
		typeDiffer []string
	}
	var diffs []cdiff
	colMissingTotal, colExtraTotal := 0, 0
	for n, lt := range liveBase {
		if isBackup(n) {
			continue
		}
		rt, ok := repoBase[n]
		if !ok {
			continue
		}
		var d cdiff
		d.table = n
		for c := range rt.cols {
			if _, ok := lt.cols[c]; !ok {
				d.missing = append(d.missing, c)
			}
		}
		for c := range lt.cols {
			if _, ok := rt.cols[c]; !ok {
				d.extra = append(d.extra, c)
			}
		}
		for c, rt2 := range rt.cols {
			if lt2, ok := lt.cols[c]; ok {
				a := strings.ToLower(strings.Join(strings.Fields(rt2), " "))
				b := strings.ToLower(strings.Join(strings.Fields(lt2), " "))
				if a != b && a != b+"[]" {
					d.typeDiffer = append(d.typeDiffer, fmt.Sprintf("%s: repo=%s live=%s", c, a, b))
				}
			}
		}
		sort.Strings(d.missing)
		sort.Strings(d.extra)
		sort.Strings(d.typeDiffer)
		colMissingTotal += len(d.missing)
		colExtraTotal += len(d.extra)
		if len(d.missing) > 0 || len(d.extra) > 0 || len(d.typeDiffer) > 0 {
			diffs = append(diffs, d)
		}
	}
	sort.Slice(diffs, func(i, j int) bool {
		if len(diffs[i].missing) != len(diffs[j].missing) {
			return len(diffs[i].missing) > len(diffs[j].missing)
		}
		return diffs[i].table < diffs[j].table
	})
	fmt.Printf("\n== COLUMN DIFFS on %d common tables ==\n", len(liveBase)-len(onlyLive))
	fmt.Printf("columns in repo-SSOT but missing live: %d\ncolumns in live but absent from repo-SSOT: %d\n", colMissingTotal, colExtraTotal)
	fmt.Printf("tables with column diffs: %d\n", len(diffs))
	for _, d := range diffs {
		fmt.Printf("  %s\n", d.table)
		for _, c := range d.missing {
			fmt.Printf("      MISSING-IN-LIVE  %s\n", c)
		}
		for _, c := range d.extra {
			fmt.Printf("      EXTRA-IN-LIVE    %s\n", c)
		}
		for _, c := range d.typeDiffer {
			fmt.Printf("      TYPE             %s\n", c)
		}
	}

	// index comparison (live index names vs repo index definitions)
	repoIdxNames := map[string]bool{}
	{
		raw, _ := os.ReadFile(*repoPath)
		for _, m := range reIndex.FindAllStringSubmatch(string(raw), -1) {
			repoIdxNames[strings.ToLower(unquote(m[2]))] = true
		}
	}
	liveIdxOnly := 0
	liveIdxTotal := 0
	for _, ix := range liveIdx {
		if strings.HasPrefix(ix.name, "bak_") {
			continue
		}
		liveIdxTotal++
		if !repoIdxNames[ix.name] {
			liveIdxOnly++
		}
	}
	fmt.Printf("\n== INDEXES ==\n")
	fmt.Printf("live indexes (excl. bak_*): %d\n", liveIdxTotal)
	fmt.Printf("live index names not found in repo SSOT file: %d\n", liveIdxOnly)
}
