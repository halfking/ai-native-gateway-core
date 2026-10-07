package admin

import (
	"fmt"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
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

// boardLogsWhere is the single WHERE builder for every board read face that goes
// through request_logs (fallbackBoardSummary, fallbackBoardTrends,
// fallbackBoardPies/fallbackDimPie, fillOverviewCountsFromLogs).
//
// The probe exclusion is applied HERE, once, rather than at each call site: the
// board's 总请求数 and 成功率 must describe real user traffic only. Probe rows
// share the same request_status values as real traffic, so a status filter
// cannot separate them — and probes fail far more often, which is exactly why
// including them made the board report a success rate no operator could
// reproduce from the request log page.
//
// bg.ProbeTrafficExclusionPredicateView is the view-safe variant of the same
// predicate (the physical-table variant references origin_stage, which the
// 113-column view does not project — using it here would 42703 on every call).
func boardLogsWhere(tr boardTimeRange, alias, tenantID string) (where string, args []any) {
	args = []any{tr.Start, tr.End}
	where = alias + ".ts >= $1 AND " + alias + ".ts < $2"
	if tenantID != "" {
		where += " AND " + alias + ".tenant_id = $3"
		args = append(args, tenantID)
	}
	where += " AND " + fmt.Sprintf(bg.ProbeTrafficExclusionPredicateView, alias, alias, alias)
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
// 2026-08-31: switch to the _without_customer_id companion view. The full
// view adds a LATERAL JOIN back to request_logs_hot/request_logs to expose
// customer_id (added by migration 575), which turns every COUNT/SUM/AVG
// over the August 2026 columnar partition (314K rows) into an 11s+ Seq Scan
// + Nested Loop that exhausts the 8s dashboard fallback timeout. The
// board fallback path (admin/dashboard_board_queries.go:fallbackBoardSummary,
// admin/dashboard_board_fallback.go:fallbackBoardPies/fillOverviewCountsFromLogs)
// doesn't read customer_id at all, so we skip the LATERAL JOIN entirely.
func boardRequestLogsFromClause() (from string, alias string) {
	return "request_logs_with_current_month_without_customer_id AS r", "r"
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
