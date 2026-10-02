package admin

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// appendSessionAnalyticsListFilters applies task_id, date_from, and date_to
// to the session list WHERE clause. Callers used to forward these query
// parameters while the SQL ignored them, so a date-bounded Top page was the
// global cost ranking. A present parameter that cannot be parsed is an error
// rather than a silent no-op.
//
// Date windows overlap the session: last_request_at >= from AND
// first_request_at <= to. task_id matches session_dim.task_id (the column
// the list JSON exposes; migration 350 copies the first request_logs.gw_task_id)
// or session_summaries.gw_task_id (added by migration 655, and often still
// null when only session_dim was filled).
func appendSessionAnalyticsListFilters(where string, args []any, argCount int, taskID, dateFromRaw, dateToRaw string) (string, []any, int, error) {
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		where += fmt.Sprintf(" AND (sd.task_id = $%d OR ss.gw_task_id = $%d)", argCount, argCount)
		args = append(args, taskID)
		argCount++
	}

	from, err := parseSessionListTime(dateFromRaw, false)
	if err != nil {
		return "", nil, 0, fmt.Errorf("invalid date_from: %w", err)
	}
	to, err := parseSessionListTime(dateToRaw, true)
	if err != nil {
		return "", nil, 0, fmt.Errorf("invalid date_to: %w", err)
	}
	if from != nil && to != nil && from.After(*to) {
		return "", nil, 0, fmt.Errorf("date_from must be <= date_to")
	}
	if from != nil {
		where += " AND ss.last_request_at >= $" + strconv.Itoa(argCount)
		args = append(args, *from)
		argCount++
	}
	if to != nil {
		where += " AND ss.first_request_at <= $" + strconv.Itoa(argCount)
		args = append(args, *to)
		argCount++
	}
	return where, args, argCount, nil
}

// parseSessionListTime accepts RFC3339 or a UTC calendar date (2006-01-02).
// endOfDay extends a calendar date through 23:59:59.999999999Z so the
// inclusive upper bound covers that day. An empty value means the bound is
// unset.
func parseSessionListTime(raw string, endOfDay bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		utc := ts.UTC()
		return &utc, nil
	}
	day, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, fmt.Errorf("want RFC3339 or YYYY-MM-DD")
	}
	day = day.UTC()
	if endOfDay {
		// Microsecond precision matches timestamptz. A nanosecond short of
		// the next midnight rounds up in Postgres and would include that instant.
		day = day.Add(24*time.Hour - time.Microsecond)
	}
	return &day, nil
}
