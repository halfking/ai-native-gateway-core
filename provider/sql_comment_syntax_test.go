package provider

// File: sql_comment_syntax_test.go
//
// Why this file exists:
//   2026-08-09 audit (linked to AUDIT-2026-08-09-seq1479-symlink-mislabel-
//   and-audit-sql-regression.md): 154 production swap to
//   releases/1478-c4ab07de/ caused 79.2% of HTTP responses to be 500 for
//   ~7 minutes with
//     "failed to get candidates from provider" +
//     "ERROR: syntax error at or near \"audit\" (SQLSTATE 42601)".
//
//   Root cause: a 10-line Go-style `//` block comment was inserted inside
//   the SQL string literal of `loadCandidatesByModalityDB`
//   (provider/client.go:1094-1103) by audit-gate commit 58c579d7.
//   PostgreSQL does NOT recognize `//` as a comment marker (only `--`
//   and `/* */`), so the parser tripped on the first non-quoted token
//   ("audit") and returned SQLSTATE 42601.
//
// This test scans every multi-line backtick raw string in
// provider/client.go and fails fast if any line begins (after whitespace)
// with `//`. The check is pure lexical — no DB connection required.

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestNoGoStyleCommentsInsideSQLRawStrings pins the regression by lexically
// inspecting every multi-line raw-string literal in provider/client.go.
//
// The scan is intentionally conservative: it only fails on lines that begin
// with `//` (a single-line Go-style comment marker) inside a raw string. PG
// will reject such lines with SQLSTATE 42601 "syntax error" pointing at the
// first non-quoted token of the offending line.
func TestNoGoStyleCommentsInsideSQLRawStrings(t *testing.T) {
	src := readProviderFile(t, "client.go")

	for _, blk := range multiLineRawStrings(src) {
		body := blk.body
		op := blk.openLine
		for offset, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimLeft(line, " \t")
			if strings.HasPrefix(trimmed, "//") {
				t.Errorf(
					"provider/client.go:%d: Go-style // comment inside a "+
						"multi-line raw-string SQL block (opened on line %d). "+
						"PostgreSQL only accepts -- and /* */ as comment "+
						"markers; a leading // turns this block into "+
						"SQLSTATE 42601 syntax error at parse time. See "+
						"AUDIT-2026-08-09-seq1479-symlink-mislabel-and-"+
						"audit-sql-regression.md. Offending line: %q",
					op+offset+1, op, strings.TrimSpace(line),
				)
			}
		}
	}
}

// multiLineRawStrings scans a Go source for raw-string literals that span
// more than one physical line. Returns (openLine, body) for each block.
// Single-line raw strings (“ `…` “ on one line) are skipped because
// they cannot contain Go-style //-comment lines anyway.
func multiLineRawStrings(src string) []rawStringBlock {
	var blocks []rawStringBlock
	lines := strings.Split(src, "\n")

	inRaw := false
	bodyStart := 0
	body := strings.Builder{}
	openLine := 0
	for i, line := range lines {
		lineno := i + 1
		if !inRaw {
			// Heuristic: line ends with a single backtick.
			trimmed := strings.TrimRight(line, " \t")
			if !strings.HasSuffix(trimmed, "`") {
				continue
			}
			if strings.Count(trimmed, "`") != 1 {
				continue
			}
			inRaw = true
			openLine = lineno
			body.Reset()
			idx := strings.LastIndex(line, "`")
			if idx >= 0 && idx+1 < len(line) {
				body.WriteString(line[idx+1:])
				body.WriteString("\n")
			}
			bodyStart = lineno
			continue
		}
		// Inside a raw string: look for the closing backtick.
		idx := strings.Index(line, "`")
		if idx < 0 {
			body.WriteString(line)
			body.WriteString("\n")
			continue
		}
		// Closing backtick on this line — capture body up to it.
		body.WriteString(line[:idx])
		_ = bodyStart
		blocks = append(blocks, rawStringBlock{
			openLine: openLine,
			body:     body.String(),
		})
		inRaw = false
		// Don't bother rest-of-line: any new raw string on the rest will
		// be picked up on the next iteration.
	}
	return blocks
}

type rawStringBlock struct {
	openLine int    // 1-based
	body     string // text BETWEEN the opening and closing backticks
}

// readProviderFile returns the file contents of a sibling .go file in this
// package directory. We use runtime.Caller to locate our own source file at
// test time, which keeps the test hermetic regardless of where `go test`
// is invoked from.
func readProviderFile(t *testing.T, fname string) string {
	t.Helper()

	// Locate this test file's directory via runtime.Caller.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	dir := thisFile[:strings.LastIndex(thisFile, "/")]
	// thisFile has the form /…/provider/sql_comment_syntax_test.go
	data, err := os.ReadFile(dir + "/" + fname)
	if err != nil {
		t.Fatalf("read %s: %v", fname, err)
	}
	return string(data)
}
