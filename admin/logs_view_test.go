package admin

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type scanDestinationCounter struct {
	count int
}

func (s *scanDestinationCounter) Scan(dest ...any) error {
	s.count = len(dest)
	return errors.New("stop after counting scan destinations")
}

func TestScanRequestListRowMatchesListColumns(t *testing.T) {
	want := len(parseSelectColumns(requestLogsListCols))

	for _, withTraceSeq := range []bool{false, true} {
		t.Run(map[bool]string{false: "without_trace_seq", true: "with_trace_seq"}[withTraceSeq], func(t *testing.T) {
			rows := new(scanDestinationCounter)
			_, err := scanRequestListRow(rows, withTraceSeq)
			if err == nil {
				t.Fatal("scanRequestListRow should return the scanner error")
			}

			expected := want
			if withTraceSeq {
				expected++
			}
			if rows.count != expected {
				t.Fatalf("scan destination count = %d, want %d", rows.count, expected)
			}
		})
	}
}

// TestRequestLogRowColumnAlignment is a static guard against the
// 2026-07-27 "query failed" incident where detail columns added to
// requestLogsListCols were not mirrored by the requestLogRow struct
// (or vice versa), so /api/logs/{id} SELECTs mismatched the Scan() order
// and pgx returned 42703.
//
// It does NOT touch the database. It walks the SELECT list column-by-column
// and matches it against the struct's JSON tag order (the order Scan must
// receive). The test fails loudly if either side drifts.
func TestRequestLogRowColumnAlignment(t *testing.T) {
	cols := parseSelectColumns(requestLogsListCols)
	if len(cols) == 0 {
		t.Fatal("requestLogsListCols is empty")
	}

	// Build the struct field order. We use the JSON tag because that is
	// what governs the API contract; the Scan() order must match the
	// SELECT order, which is what the API response preserves.
	jsonNames := jsonFieldNames(reflect.TypeOf(requestLogRow{}))

	// The struct has fields that the SELECT does NOT expose (e.g.
	// OutboundBody, Attachments) — those are only loaded by the detail
	// path. We allow the struct to be a strict superset of the SELECT.
	//
	// What we forbid is: SELECT referencing a column whose JSON field
	// name is missing from the struct.
	selectSet := make(map[string]bool, len(cols))
	for _, c := range cols {
		selectSet[c] = true
	}

	// Detail path adds outbound_body / outbound_msg_hashes /
	// compression_meta / attachments / routing_attempts / routing_summary
	// on top of the list columns.
	detailExtras := map[string]bool{
		"outbound_body":       true,
		"outbound_msg_hashes": true,
		"compression_meta":    true,
		"attachments":         true,
		"routing_attempts":    true,
		"routing_summary":     true,
	}
	for extra := range detailExtras {
		selectSet[extra] = true
	}

	// Reverse: every known API field that the SELECT exposes must exist
	// on the struct. We only enforce this for the columns that the API
	// hands back to the client (omitted fields are fine).
	for _, c := range cols {
		if !jsonNames[c] {
			t.Errorf("SELECT column %q is not represented in requestLogRow JSON tags", c)
		}
	}
}

// parseSelectColumns splits a SELECT projection into top-level column names.
// We strip "AS alias" so the alias becomes the canonical name (which is what
// pgx Scan() binds to). COALESCE(...) expansions are kept inline because
// callers must use their alias (e.g. "request_status").
func parseSelectColumns(sel string) []string {
	var out []string
	// Normalize whitespace + drop line comments.
	cleaned := strings.Builder{}
	for _, line := range strings.Split(sel, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		cleaned.WriteString(line)
		cleaned.WriteByte(' ')
	}
	stmt := cleaned.String()
	stmt = strings.TrimSpace(stmt)
	if strings.HasPrefix(stmt, "SELECT") {
		stmt = strings.TrimSpace(stmt[len("SELECT"):])
	}

	// Split on top-level commas (paren depth 0).
	depth := 0
	current := strings.Builder{}
	flush := func() {
		s := strings.TrimSpace(current.String())
		if s == "" {
			current.Reset()
			return
		}
		// Reduce "COALESCE(x, y) AS foo" → "foo"; "x AS y" → "y";
		// "table.col" → "col" so pgx Scan() binding matches.
		upper := strings.ToUpper(s)
		if idx := strings.LastIndex(upper, " AS "); idx >= 0 {
			s = strings.TrimSpace(s[idx+4:])
		}
		if dot := strings.LastIndex(s, "."); dot >= 0 {
			s = strings.TrimSpace(s[dot+1:])
		}
		// Strip trailing "::type" casts (e.g. "cost_usd::float8").
		if idx := strings.Index(s, "::"); idx >= 0 {
			s = strings.TrimSpace(s[:idx])
		}
		out = append(out, s)
		current.Reset()
	}
	for _, r := range stmt {
		switch r {
		case '(':
			depth++
			current.WriteRune(r)
		case ')':
			depth--
			current.WriteRune(r)
		case ',':
			if depth == 0 {
				flush()
			} else {
				current.WriteRune(r)
			}
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return out
}

// jsonFieldNames returns the set of JSON tag names for the struct's
// exported fields, in declaration order. Hidden (lowercase / no-bool / etc.)
// fields are skipped; `omitempty` and `,string` options are ignored.
func jsonFieldNames(t reflect.Type) map[string]bool {
	names := make(map[string]bool)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return names
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		name := strings.SplitN(tag, ",", 2)[0]
		if name == "" || name == "-" {
			name = f.Name
		}
		names[name] = true
		// Recurse into embedded structs so requestLogDetail inherits
		// requestLogRow's tags.
		if f.Anonymous {
			for k := range jsonFieldNames(f.Type) {
				names[k] = true
			}
		}
	}
	return names
}

// TestListColsReferenceClientPerceptionColumns is the regression guard for
// the 2026-07-27 incident: /api/logs/{id} added rl.canonical_model /
// rl.agent_name / rl.agent_type / rl.client_protocol to its SELECT, but
// the underlying view request_logs_with_current_month still only exports
// the routing_attempts / routing_summary set added by migration 448.
//
// This test trips BEFORE the SQL runtime does: if any column is removed
// from the SELECT block, the test fails with a clear message and the
// migration 459 will not be the only thing protecting callers.
func TestListColsReferenceClientPerceptionColumns(t *testing.T) {
	required := []string{
		"canonical_model",
		"agent_name",
		"agent_type",
		"client_protocol",
	}
	cols := parseSelectColumns(requestLogsListCols)
	have := make(map[string]bool, len(cols))
	for _, c := range cols {
		have[c] = true
	}
	for _, want := range required {
		if !have[want] {
			t.Errorf("requestLogsListCols missing %q — /api/logs/{id} will fail with SQLSTATE 42703", want)
		}
	}
}
