package admin

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// V6-W1.6 R8 / migration 610: the /api/logs list & detail surfaces must
// project request_class + due_at (column list, scan targets, filter).
// Offline source/compile-level guards — same pattern as the telemetry
// contract tests; no database needed.

func TestRequestLogsColumnsCarryClass(t *testing.T) {
	if !strings.Contains(requestLogsListCols, "rl.request_class") ||
		!strings.Contains(requestLogsListCols, "rl.due_at") {
		t.Fatalf("requestLogsListCols missing 608 columns")
	}
	if !strings.Contains(requestLogsDetailCols, "rl.request_class") {
		t.Fatalf("requestLogsDetailCols did not inherit 608 columns")
	}
	// The scan targets must exist and sit AFTER SessionTitle (column order
	// is positional: …, session_title, request_class, due_at, [trace_seq]).
	src, err := os.ReadFile("logs.go")
	if err != nil {
		t.Fatalf("read logs.go: %v", err)
	}
	s := string(src)
	scanTail := `&l.SessionTitle,
		// V6-W1.6 R8 (migration 610): request class + due time (LAST fixed
		// columns; the conditional trace_seq append below stays after them).
		&l.RequestClass, &l.DueAt,`
	if !strings.Contains(s, scanTail) {
		t.Fatalf("scanRequestListRow does not scan class/due after SessionTitle")
	}
	if !strings.Contains(s, `&detail.RequestClass, &detail.DueAt,`) {
		t.Fatalf("getLog detail scan missing class/due")
	}
	if !strings.Contains(s, `addFilter("rl.request_class = $%d", v)`) {
		t.Fatalf("listLogs missing request_class filter")
	}
}

func TestRequestLogRowClassJSONShape(t *testing.T) {
	c := "scheduled"
	d := time.Unix(1800000123, 0).UTC()
	row := requestLogRow{RequestClass: &c, DueAt: &d}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, `"request_class":"scheduled"`) {
		t.Fatalf("JSON missing request_class: %s", got)
	}
	if !strings.Contains(got, `"due_at":"2027-01-15T`) {
		t.Fatalf("JSON missing due_at: %s", got)
	}
	// Immediate rows omit both (omitempty) — API shape stays unchanged for
	// pre-608 traffic.
	b2, _ := json.Marshal(requestLogRow{})
	if strings.Contains(string(b2), "request_class") || strings.Contains(string(b2), "due_at") {
		t.Fatalf("omitempty broken: %s", b2)
	}
}
