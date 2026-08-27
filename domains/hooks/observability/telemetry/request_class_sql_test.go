package telemetry

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Offline contract tests for migration 608 (V6-W1.6 R8): request_class /
// due_at must be wired through EVERY write statement of request_logs_hot and
// keep the column ↔ placeholder ↔ arg alignment. Reads client.go source
// only — same offline pattern as TestRequestLogMainTableExcludesBodyColumns.

func readClientSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	return string(src)
}

func functionBody(t *testing.T, src, signature string) string {
	t.Helper()
	start := strings.Index(src, signature)
	if start < 0 {
		t.Fatalf("signature not found: %s", signature)
	}
	end := strings.Index(src[start+1:], "\nfunc ")
	if end < 0 {
		t.Fatalf("function end not found after %s", signature)
	}
	return src[start : start+1+end]
}

func TestInsertRequestLogCarriesRequestClass(t *testing.T) {
	body := functionBody(t, readClientSource(t), "func (c *Client) insertRequestLog")
	for _, want := range []string{
		"request_class, due_at",               // column list tail
		"requestClassArg(entry.RequestClass)", // arg-side immediate default
		"$102",                                // due_at placeholder
		"entry.RequestClass",                  // $101 arg
		"entry.DueAt",                         // $102 arg
		"request_class        = COALESCE(EXCLUDED.request_class, request_logs_hot.request_class)",
		"due_at               = COALESCE(EXCLUDED.due_at, request_logs_hot.due_at)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("insertRequestLog missing 608 contract piece %q", want)
		}
	}
	// Arg ordering: RequestClass/DueAt must come AFTER CustomerID ($100 ↔
	// customer_id, "the LAST column" per the 507 comment, now second-to-last).
	cust := strings.Index(body, "entry.CustomerID,")
	cls := strings.Index(body, "requestClassArg(entry.RequestClass),")
	if cust < 0 || cls < 0 || cls < cust {
		t.Fatalf("608 args must follow CustomerID: cust=%d class=%d", cust, cls)
	}
	// Highest placeholder must be 102 (2 new columns after 507's $100).
	re := regexp.MustCompile(`\$(\d+)`)
	max := 0
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		n := 0
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
		}
		if n > max {
			max = n
		}
	}
	if max != 102 {
		t.Fatalf("max placeholder = %d, want 102", max)
	}
}

func TestUpdateRequestLogCarriesRequestClass(t *testing.T) {
	src := readClientSource(t)
	idx := strings.Index(src, "UPDATE request_logs_hot\n")
	if idx < 0 {
		t.Fatalf("UPDATE statement not found")
	}
	seg := src[idx : idx+20000]
	for _, want := range []string{
		"request_class = CASE WHEN $98 IS NULL THEN request_class ELSE $98 END",
		"due_at = CASE WHEN $98 IS NULL THEN due_at ELSE $99 END",
	} {
		if !strings.Contains(seg, want) {
			t.Fatalf("UPDATE missing 608 assignment %q", want)
		}
	}
}

func TestRequestLogEntryHasClassFields(t *testing.T) {
	e := &RequestLogEntry{}
	c := "scheduled"
	d := time.Unix(1800000000, 0).UTC()
	e.RequestClass = &c
	e.DueAt = &d
	if *e.RequestClass != "scheduled" || !e.DueAt.Equal(d) {
		t.Fatalf("entry class fields not settable")
	}
}
