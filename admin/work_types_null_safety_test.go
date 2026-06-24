package admin

import (
	"os"
	"strings"
	"testing"
)

// readWorkTypesSource returns the raw source of admin/work_types.go
// so tests can guard against the 2026-06-24 NULL-safety regression
// without spinning up a database. Keeping this in a dedicated file
// avoids coupling handleStats to a build helper we haven't needed yet.
func readWorkTypesSource(t *testing.T) string {
	t.Helper()
	// The file lives next to this test (both in admin/) so a relative
	// path is enough. We re-read every test invocation so edits land
	// without a test-binary rebuild.
	src, err := os.ReadFile("work_types.go")
	if err != nil {
		t.Fatalf("read work_types.go: %v", err)
	}
	return string(src)
}

// TestHandleStats_NullSafeIsAutoFilter guards the 2026-06-24 fix that
// addressed the heatmap "暂无数据" bug on llmgateway.internal.example.com/routing-v2.
// The work-types endpoint has the same anti-pattern in three places —
// explicit-model branch `is_auto_request = FALSE` is NULL-unsafe and
// silently drops every historical request_logs row where the writer
// never set the bool column.
//
// These three queries live inline in handleStats. Rather than spin up
// a build helper for the fix (handleStats has only 3 query sites and
// would grow a parallel API surface just for the test), we pin the
// contract via a source-level check: each query must use one of the
// NULL-safe forms (`IS NOT TRUE` or `COALESCE(..., FALSE) = FALSE`).
func TestHandleStats_NullSafeIsAutoFilter(t *testing.T) {
	src := readWorkTypesSource(t)

	// Locate handleStats and assert that, within its body, the
	// NULL-unsafe `is_auto_request = FALSE` form never appears in
	// any SQL query (we strip single-line `//` comments first so the
	// prose explanation in the source doesn't trip the assertion).
	idx := strings.Index(src, "func (h *WorkTypeHandlers) handleStats(")
	if idx < 0 {
		t.Fatal("could not locate WorkTypeHandlers.handleStats in admin/work_types.go")
	}
	endIdx := strings.Index(src[idx:], "\nfunc ")
	if endIdx < 0 {
		endIdx = len(src)
	} else {
		endIdx += idx
	}
	body := src[idx:endIdx]

	bodyNoComments := stripLineComments(body)

	if strings.Contains(bodyNoComments, "is_auto_request = FALSE") {
		t.Fatalf("handleStats still contains NULL-unsafe `is_auto_request = FALSE` in a SQL query. PostgreSQL three-valued logic excludes rows where is_auto_request IS NULL, silently dropping hundreds of historical explicit-model requests. Replace with `is_auto_request IS NOT TRUE` (or `COALESCE(is_auto_request, FALSE) = FALSE`):\n%s", bodyNoComments)
	}

	// The handler should have at least three NULL-safe clauses — one
	// per query that originally carried the anti-pattern (wtDirect,
	// l1Dist, top_models). Allow either form to keep the test
	// implementation-flexible.
	mustContain := []string{
		"is_auto_request IS NOT TRUE",
	}
	for _, want := range mustContain {
		occurrences := strings.Count(bodyNoComments, want)
		if occurrences < 3 {
			t.Fatalf("handleStats should use %q at least 3 times (wtDirect + l1Dist + top_models queries); found %d. Body:\n%s", want, occurrences, bodyNoComments)
		}
	}
}

// TestHandleStats_L1TaskGroupByExpression guards the 2026-06-24 bug
// where `by_l1_task` silently came back as `{}` even though the
// request_logs table had hundreds of rows. Root cause: the SELECT
// expression references `is_auto_request` inside a CASE branch (via
// the effectiveTaskExpr) but the GROUP BY referenced only the raw
// `task_type` column. PostgreSQL rejects that combination with
// "column request_logs.is_auto_request must appear in the GROUP BY
// clause or be used in an aggregate function", and the handler
// swallowed the rows error so the response shape looked valid with
// an empty distribution. The fix is `GROUP BY (expr)` so PG accepts
// the composite key.
func TestHandleStats_L1TaskGroupByExpression(t *testing.T) {
	src := readWorkTypesSource(t)

	idx := strings.Index(src, "func (h *WorkTypeHandlers) handleStats(")
	if idx < 0 {
		t.Fatal("could not locate WorkTypeHandlers.handleStats")
	}
	endIdx := strings.Index(src[idx:], "\nfunc ")
	if endIdx < 0 {
		endIdx = len(src)
	} else {
		endIdx += idx
	}
	body := stripLineComments(src[idx:endIdx])

	// Find the l1Dist query by its distinctive WHERE 24h clause and
	// assert that its GROUP BY uses the wrapped expression form.
	// We anchor on the only query that uses `GROUP BY task_type` (or
	// `GROUP BY (task_type_eff)` after the fix). Any future
	// regression to bare `task_type` GROUP BY is the regression we
	// want to catch.
	if strings.Contains(body, "GROUP BY task_type\n") {
		t.Fatalf("handleStats l1Dist query uses bare `GROUP BY task_type` while SELECT references is_auto_request inside a CASE branch. PG rejects this with `column must appear in GROUP BY` and the handler swallows rows err, so by_l1_task silently comes back as {}. Replace with `GROUP BY (%s)` where the wrapped expression is the same one used in SELECT:\n%s", "COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END)", body)
	}
	if !strings.Contains(body, "GROUP BY (") {
		t.Fatalf("handleStats l1Dist query must GROUP BY (expr) (the wrapped COALESCE/CASE expression). Found no `GROUP BY (` in handleStats body:\n%s", body)
	}
}

// TestHandleStats_NullSafeTotalSpecified guards the total_specified
// SUM counter. Just like the audit handler, the original code used
// `NOT is_auto_request` inside a SUM(CASE ...) — which is NULL when
// the bool column is NULL, and SUM ignores NULL, so total_specified
// was always reported as 0. The fix wraps with COALESCE so NULL rows
// count as explicit-model requests.
func TestHandleStats_NullSafeTotalSpecified(t *testing.T) {
	src := readWorkTypesSource(t)

	idx := strings.Index(src, "func (h *WorkTypeHandlers) handleStats(")
	if idx < 0 {
		t.Fatal("could not locate WorkTypeHandlers.handleStats")
	}
	endIdx := strings.Index(src[idx:], "\nfunc ")
	if endIdx < 0 {
		endIdx = len(src)
	} else {
		endIdx += idx
	}
	body := src[idx:endIdx]
	bodyNoComments := stripLineComments(body)

	if strings.Contains(bodyNoComments, "NOT is_auto_request AND client_model IS NOT NULL") &&
		!strings.Contains(bodyNoComments, "NOT COALESCE(is_auto_request, FALSE) AND client_model IS NOT NULL") {
		t.Fatalf("handleStats total_specified SUM still uses NULL-unsafe `NOT is_auto_request`. SUM ignores NULL, so total_specified is always 0. Replace with `NOT COALESCE(is_auto_request, FALSE)`:\n%s", bodyNoComments)
	}
}

// stripLineComments removes `// ...` line comments so source-level
// SQL pattern assertions don't match the explanatory prose around
// each query. Block comments and SQL `--` line comments inside string
// literals are intentionally left alone (we only care about Go
// single-line comments here).
func stripLineComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	for _, line := range strings.Split(src, "\n") {
		// Find `//` not inside a string literal. Simple heuristic:
		// count unescaped double-quotes on the line; if even, the
		// `//` outside any string is the comment boundary.
		if idx := indexLineComment(line); idx >= 0 {
			line = line[:idx]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func indexLineComment(line string) int {
	inStr := false
	for i := 0; i < len(line)-1; i++ {
		c := line[i]
		if c == '"' && (i == 0 || line[i-1] != '\\') {
			inStr = !inStr
			continue
		}
		if !inStr && c == '/' && line[i+1] == '/' {
			return i
		}
	}
	return -1
}
