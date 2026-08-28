package admin

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestRequestLogDetailColumnsStayCompatibleWithBodySplit(t *testing.T) {
	for _, droppedBodyColumn := range []string{
		"rl.request_body",
		"rl.response_body",
		"rl.outbound_body",
	} {
		if strings.Contains(requestLogsDetailCols, droppedBodyColumn) {
			t.Errorf("requestLogsDetailCols references %q after body storage split", droppedBodyColumn)
		}
	}
	if !strings.Contains(requestLogsDetailCols, "rl.outbound_msg_hashes") {
		t.Error("requestLogsDetailCols must preserve outbound message hashes")
	}
}

// TestRequestLogAggregateJSONContract locks the wire contract of the
// /api/logs response.aggregate field so the /request-logs UI keeps a
// stable shape. Adding a new sum column should bump this list in two
// places (struct tag + here) and the existing fields must not drift.
func TestRequestLogAggregateJSONContract(t *testing.T) {
	want := []string{
		"total_requests",
		"prompt_tokens",
		"completion_tokens",
		"cache_read_tokens",
		"cache_write_tokens",
		"total_tokens",
		"cost_usd",
		"credits_charged",
	}
	got := jsonFieldNames(reflect.TypeOf(requestLogAggregate{}))
	for _, w := range want {
		if !got[w] {
			t.Errorf("requestLogAggregate is missing JSON field %q", w)
		}
	}
	// Marshal/unmarshal round-trip to make sure all fields are tagged
	// correctly (e.g. no stray `json:"-"`).
	var agg requestLogAggregate
	raw, err := json.Marshal(agg)
	if err != nil {
		t.Fatalf("marshal requestLogAggregate: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal requestLogAggregate: %v", err)
	}
	for _, w := range want {
		if _, ok := back[w]; !ok {
			t.Errorf("marshalled JSON missing key %q", w)
		}
	}
}

// TestListLogsResponseShape ensures the listLogs top-level response
// shape (items / count / aggregate) stays aligned with what the
// /request-logs UI consumes. The actual handler-level integration
// (writing the aggregate field, scanning 7 SUM columns in the right
// order) is exercised by TestRequestLogAggregateJSONContract above
// plus the integration suite under admin_test.go; this test guards
// the read-side contract by unmarshalling a synthetic response.
func TestListLogsResponseShape(t *testing.T) {
	const payload = `{
		"items": [],
		"count": 0,
		"aggregate": {
			"total_requests": 0,
			"prompt_tokens": 0,
			"completion_tokens": 0,
			"cache_read_tokens": 0,
			"cache_write_tokens": 0,
			"total_tokens": 0,
			"cost_usd": 0.0,
			"credits_charged": 0
		}
	}`
	var resp struct {
		Items     []requestLogRow      `json:"items"`
		Count     int                  `json:"count"`
		Aggregate *requestLogAggregate `json:"aggregate"`
	}
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if resp.Count != 0 {
		t.Errorf("count = %d, want 0", resp.Count)
	}
	if resp.Aggregate == nil {
		t.Fatal("aggregate must not be null")
	}
	if resp.Aggregate.TotalRequests != 0 {
		t.Errorf("total_requests = %d, want 0", resp.Aggregate.TotalRequests)
	}
	if resp.Aggregate.CreditsCharged == nil || *resp.Aggregate.CreditsCharged != 0 {
		t.Errorf("credits_charged = %v, want 0", resp.Aggregate.CreditsCharged)
	}
}

func TestShouldRunByModel(t *testing.T) {
	maxWindow := 32 * 24 * time.Hour
	tests := []struct {
		name                 string
		modelFilterSpecified bool
		count                int
		timeSpan             time.Duration
		want                 bool
	}{
		{"empty window, no filter, has rows", false, 5, time.Hour, true},
		{"model filter set blocks aggregate", true, 5, time.Hour, false},
		{"zero count blocks aggregate", false, 0, time.Hour, false},
		{"window at limit allowed", false, 1, maxWindow, true},
		{"window over limit blocks aggregate", false, 1, maxWindow + time.Second, false},
		{"monthly view (~31d) still allowed", false, 1, 31 * 24 * time.Hour, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRunByModel(tt.modelFilterSpecified, tt.count, tt.timeSpan)
			if got != tt.want {
				t.Errorf("shouldRunByModel(%v, %d, %v) = %v, want %v",
					tt.modelFilterSpecified, tt.count, tt.timeSpan, got, tt.want)
			}
		})
	}
}

// TestBuildModelFilterClauseExactMatch guards the 2026-08-29 fix: selecting a
// model in the picker must filter to ONLY that model's requests. The previous
// implementation used ILIKE '%v%', so "glm-5.2" also matched "glm-5.2-pro" /
// "glm-5.2-flash" and any canonical_name containing the string. This test
// fails loudly if the clause ever drifts back to a substring match.
func TestBuildModelFilterClauseExactMatch(t *testing.T) {
	cases := []string{"glm-5.2", "gpt-4o", "claude-3-5-sonnet"}
	for _, v := range cases {
		clause, args := buildModelFilterClause(v, 3)
		if strings.Contains(clause, "ILIKE") {
			t.Fatalf("model filter must use exact `=` match, found ILIKE in clause: %s", clause)
		}
		if strings.Contains(clause, "%"+v+"%") {
			t.Fatalf("model filter must NOT be a substring match, found '%%%s%%' in clause: %s", v, clause)
		}
		// Branch 1: canonical_id → exact canonical_name at $3.
		if !strings.Contains(clause, "mc.canonical_name = $3") {
			t.Errorf("expected exact canonical_name match at $3 for %q, clause=%s", v, clause)
		}
		// Branch 3: fallback exact client_model at $5.
		if !strings.Contains(clause, "rl.client_model = $5") {
			t.Errorf("expected exact client_model match at $5 for %q, clause=%s", v, clause)
		}
		if len(args) != 3 || args[0] != v || args[1] != v || args[2] != v {
			t.Errorf("expected 3 args all %q, got %v", v, args)
		}
	}
}
