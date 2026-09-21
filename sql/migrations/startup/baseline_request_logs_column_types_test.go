package startup

// R36 (2026-09-17 audit) — column-type contract for the request_logs family
// across the three 01-schema.sql baselines. Closes the guard gap that let
// R34 遗留#2 happen: the R15-era contract (baseline_ensure_functions_contract_test.go)
// pins ensure-function BODIES only, so the hot-table column drift (10 columns,
// 6 of them hard type-family mismatches vs the partitioned mother table) sat
// in a blind spot until ensureRequestLogsCurrentMonthView's dynamic
// hot∩parent UNION rebuild started failing fresh installs with 42804.
// Migration 717 heals existing installs; this test keeps the three baselines
// from re-drifting (a fresh install replays baseline + migrations, so the
// baseline itself must be aligned).

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

type baselineColumn struct {
	name string
	typ  string
}

// requestLogsCreateRe spans one CREATE TABLE body through its closing "\n)".
// The terminator must be anchored on what pg_dump emits after the paren —
// "WITH (" (storage params), "PARTITION BY", or ";" — otherwise the
// non-greedy match leaks past the block and swallows following statements
// (the partitioned mother closes with "PARTITION BY", not ");").
var requestLogsCreateRe = regexp.MustCompile(`(?s)CREATE TABLE public\.(request_logs|request_logs_hot) \((.*?)\n\)(?:\s*(?:WITH \(|PARTITION BY|;))`)

// splitColumns splits a CREATE TABLE body into top-level column definitions,
// ignoring nested parens (numeric(14,8), character varying(255)) and
// CHECK/EXCLUDE constraints that contain commas.
func splitColumns(body string) []string {
	var parts []string
	depth := 0
	cur := strings.Builder{}
	for _, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case '\n':
			if depth == 0 {
				parts = append(parts, cur.String())
				cur.Reset()
				continue
			}
		}
		cur.WriteRune(r)
	}
	if strings.TrimSpace(cur.String()) != "" {
		parts = append(parts, cur.String())
	}
	return parts
}

// parseRequestLogsColumns extracts column → normalized type for one CREATE
// TABLE block. Constraints (PRIMARY KEY/UNIQUE/... start with a keyword) are
// skipped; types are whitespace-normalized so varchar( 255 ) == varchar(255).
func parseRequestLogsColumns(t *testing.T, body string) map[string]string {
	t.Helper()
	cols := map[string]string{}
	for _, line := range splitColumns(body) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.Trim(fields[0], `"`)
		switch strings.ToUpper(name) {
		case "PRIMARY", "UNIQUE", "CHECK", "CONSTRAINT", "EXCLUDE", "FOREIGN":
			continue
		}
		// Type = everything up to the first trailing keyword that is not part
		// of the type (NOT NULL / DEFAULT / references). Type tokens may carry
		// parens: character varying(255), numeric(14,8), text[].
		typ := line[len(fields[0]):]
		typ = strings.TrimSpace(typ)
		for _, trailer := range []string{" NOT NULL", " NULL", " DEFAULT"} {
			if i := strings.Index(typ, trailer); i >= 0 {
				typ = typ[:i]
			}
		}
		// Strip the trailing comma and any inline collation noise.
		typ = strings.TrimSuffix(strings.TrimSpace(typ), ",")
		typ = strings.Join(strings.Fields(typ), " ")
		cols[name] = typ
	}
	return cols
}

func parseBothRequestLogsTables(t *testing.T, sql string) (mother, hot map[string]string) {
	t.Helper()
	found := map[string]map[string]string{}
	for _, m := range requestLogsCreateRe.FindAllStringSubmatch(sql, -1) {
		if _, dup := found[m[1]]; dup {
			t.Fatalf("multiple CREATE TABLE blocks matched for %s — regex anchor is leaking across statements", m[1])
		}
		found[m[1]] = parseRequestLogsColumns(t, m[2])
	}
	mother, ok := found["request_logs"]
	if !ok {
		t.Fatalf("request_logs mother CREATE TABLE not found")
	}
	hot, ok = found["request_logs_hot"]
	if !ok {
		t.Fatalf("request_logs_hot CREATE TABLE not found")
	}
	return mother, hot
}

func TestBaselineRequestLogsHotColumnTypesMatchMother(t *testing.T) {
	for label, rel := range baselineEnsureSources {
		t.Run(label, func(t *testing.T) {
			sql := readBaseline(t, rel)
			mother, hot := parseBothRequestLogsTables(t, sql)

			var mismatches []string
			for col, mType := range mother {
				hType, ok := hot[col]
				if !ok {
					continue // hot may legitimately carry a subset of columns
				}
				if mType != hType {
					mismatches = append(mismatches, fmt.Sprintf("%s: mother=%q hot=%q", col, mType, hType))
				}
			}
			if len(mismatches) > 0 {
				t.Fatalf("request_logs_hot column types drifted from request_logs (42804 on fresh-install UNION rebuild; see migration 717):\n%s",
					strings.Join(mismatches, "\n"))
			}
		})
	}
}

// TestBaselineRequestLogsAlignedColumnsPinned locks the exact post-717 types
// of the ten healed columns so a future regeneration cannot silently
// reintroduce ANY of them (the mother-side values are the authority).
func TestBaselineRequestLogsAlignedColumnsPinned(t *testing.T) {
	want := map[string]string{
		"agent_name":           "character varying(255)",
		"agent_type":           "character varying(50)",
		"api_key_fingerprint":  "character varying(16)",
		"customer_id":          "bigint",
		"task_id":              "character varying(255)",
		"protocol_conversion":  "boolean",
		"ir_extensions":        "jsonb",
		"sanitizer_mutations":  "jsonb",
		"content_safety_score": "jsonb",
		"dlp_violations":       "jsonb",
	}
	sql := readBaseline(t, baselineEnsureSources["canonical"])
	_, hot := parseBothRequestLogsTables(t, sql)
	for col, typ := range want {
		got, ok := hot[col]
		if !ok {
			t.Errorf("request_logs_hot.%s missing from canonical baseline", col)
			continue
		}
		if got != typ {
			t.Errorf("request_logs_hot.%s = %q, want %q (migration 717 target)", col, got, typ)
		}
	}
}
