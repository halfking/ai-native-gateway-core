package admin

import (
	"fmt"
	"net/http"
	"time"
)

// boardTimeRange is the resolved window for dashboard board queries.
type boardTimeRange struct {
	Start  time.Time
	End    time.Time
	Days   int
	Custom bool
}

func boardTimeRangeFromRequest(r *http.Request) (boardTimeRange, error) {
	startStr := queryString(r, "start")
	endStr := queryString(r, "end")
	if startStr == "" && endStr == "" {
		days := boardDays(r)
		return boardPresetTimeRange(days, time.Now().UTC()), nil
	}
	start, end, err := resolveUsageTimeRange(r, 1)
	if err != nil {
		return boardTimeRange{}, err
	}
	days := int(end.Sub(start) / (24 * time.Hour))
	if days < 1 {
		days = 1
	}
	return boardTimeRange{
		Start:  start,
		End:    end,
		Days:   days,
		Custom: true,
	}, nil
}

func (tr boardTimeRange) includesTodayUTC() bool {
	now := time.Now().UTC()
	todayStart := now.Truncate(24 * time.Hour)
	return tr.End.After(todayStart)
}

// trendBucketMinutes picks chart bucket size: today=5m, 7d=15m, 30d+=1h.
func (tr boardTimeRange) trendBucketMinutes() int {
	if tr.Custom {
		span := tr.End.Sub(tr.Start)
		switch {
		case span <= 48*time.Hour:
			return 5
		case span <= 14*24*time.Hour:
			return 15
		default:
			return 60
		}
	}
	switch {
	case tr.Days <= 1:
		return 5
	case tr.Days <= 7:
		return 15
	default:
		return 60
	}
}

func sqlTrendBucket(column string, minutes int) string {
	if minutes >= 60 {
		return fmt.Sprintf("date_trunc('hour', %s)", column)
	}
	return fmt.Sprintf(
		"date_trunc('hour', %s) + (FLOOR(EXTRACT(minute FROM %s)::int / %d) * interval '%d minutes')",
		column, column, minutes, minutes,
	)
}

func (tr boardTimeRange) trendBucketUnit() string {
	if tr.trendBucketMinutes() >= 60 {
		return "hour"
	}
	return "minute"
}

func boardMinuteWhere(tr boardTimeRange, tenantID string, extraStartArg int) (where string, args []any) {
	args = []any{tr.Start, tr.End}
	where = "bucket >= $1 AND bucket < $2"
	argN := 3
	if tenantID != "" {
		where += fmt.Sprintf(" AND tenant_id = $%d", argN)
		args = append(args, tenantID)
		argN++
	}
	_ = extraStartArg
	return where, args
}

func boardLogsWhere(tr boardTimeRange, alias, tenantID string) (where string, args []any) {
	args = []any{tr.Start, tr.End}
	where = alias + ".ts >= $1 AND " + alias + ".ts < $2"
	if tenantID != "" {
		where += " AND " + alias + ".tenant_id = $3"
		args = append(args, tenantID)
	}
	return where, args
}

func daysToBoardTimeRange(days int) boardTimeRange {
	return boardPresetTimeRange(days, time.Now().UTC())
}

// boardPresetTimeRange maps preset tabs to calendar-aligned UTC windows.
func boardPresetTimeRange(days int, now time.Time) boardTimeRange {
	if days < 1 {
		days = 1
	}
	todayStart := now.Truncate(24 * time.Hour)
	return boardTimeRange{
		Start:  todayStart.Add(-time.Duration(days-1) * 24 * time.Hour),
		End:    now,
		Days:   days,
		Custom: false,
	}
}

// boardRequestLogsFromClause returns the cross-partition union view for dashboard queries.
func boardRequestLogsFromClause() (from string, alias string) {
	return "request_logs_with_current_month AS r", "r"
}

func boardStatsCoverageSlack(tr boardTimeRange) time.Duration {
	return time.Duration(tr.trendBucketMinutes()) * time.Minute
}

func (tr boardTimeRange) dimMinuteWhere(tenantID string) (where string, args []any) {
	args = []any{tr.Start, tr.End}
	where = "bucket >= $1 AND bucket < $2"
	if tenantID != "" {
		where += " AND tenant_id = $3"
		args = append(args, tenantID)
	}
	return where, args
}
