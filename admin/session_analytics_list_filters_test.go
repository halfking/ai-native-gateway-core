package admin

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAppendSessionListFilters_TaskAndRFC3339(t *testing.T) {
	where, args, next, err := appendSessionAnalyticsListFilters(
		" WHERE 1=1", nil, 2,
		"task-9",
		"2026-10-01T00:00:00Z",
		"2026-10-02T12:00:00Z",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, "sd.task_id = $2 OR ss.gw_task_id = $2") {
		t.Fatalf("task filter missing: %s", where)
	}
	if !strings.Contains(where, "ss.last_request_at >= $3") || !strings.Contains(where, "ss.first_request_at <= $4") {
		t.Fatalf("date filter missing: %s", where)
	}
	if next != 5 {
		t.Fatalf("next arg = %d, want 5", next)
	}
	if len(args) != 3 || args[0] != "task-9" {
		t.Fatalf("args = %#v", args)
	}
	from := args[1].(time.Time)
	to := args[2].(time.Time)
	if !from.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("from = %s", from)
	}
	if !to.Equal(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("to = %s", to)
	}
}

func TestAppendSessionListFilters_CalendarDayIsInclusive(t *testing.T) {
	_, args, _, err := appendSessionAnalyticsListFilters(" WHERE 1=1", nil, 1, "", "2026-10-01", "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	from := args[0].(time.Time)
	to := args[1].(time.Time)
	if !from.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("from = %s", from)
	}
	if !to.Equal(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC).Add(-time.Microsecond)) {
		t.Fatalf("to = %s", to)
	}
}

func TestAppendSessionListFilters_AbsentParamsDoNotChangeWhere(t *testing.T) {
	where, args, next, err := appendSessionAnalyticsListFilters(" WHERE 1=1", []any{"tenant"}, 2, "  ", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if where != " WHERE 1=1" || len(args) != 1 || next != 2 {
		t.Fatalf("where=%q args=%#v next=%d", where, args, next)
	}
}

func TestListTaskFilterColumnsExistInStartupMigrations(t *testing.T) {
	where, _, _, err := appendSessionAnalyticsListFilters(" WHERE 1=1", nil, 1, "task-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, "sd.task_id") || !strings.Contains(where, "ss.gw_task_id") {
		t.Fatalf("where = %s", where)
	}
	summaries, err := os.ReadFile("../sql/migrations/startup/655_session_summaries_schema_reconcile.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summaries), "ADD COLUMN IF NOT EXISTS gw_task_id text") {
		t.Fatal("ss.gw_task_id is not a session_summaries column in migration 655")
	}
	dim, err := os.ReadFile("../sql/migrations/startup/350_session_analytics_fix.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dim), "task_id       VARCHAR(128)") {
		t.Fatal("sd.task_id is not declared on session_dim in migration 350")
	}
}

func TestAppendSessionListFilters_RejectsIgnoredShapes(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
		want string
	}{
		{"bad from", "10/01/2026", "", "invalid date_from"},
		{"bad to", "", "yesterday", "invalid date_to"},
		{"inverted", "2026-10-02T00:00:00Z", "2026-10-01T00:00:00Z", "date_from must be <= date_to"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := appendSessionAnalyticsListFilters(" WHERE 1=1", nil, 1, "", tc.from, tc.to)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
