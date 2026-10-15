package admin

import (
	"fmt"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
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
// through _request_logs (fallbackBoardSummary, fallbackBoardTrends,
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

// boardRequestLogsFromClause returns the FROM source for dashboard fallback
// queries.
//
// 2026-10-15（r1033）：换到**会话族原生源**（session_turns hot ∪ parent，
// 115 列 v1 形状投影）。旧的 request_logs_with_current_month_without_customer_id
// 是纯 v1 视图（_request_logs_hot UNION ALL _request_logs，无 session 分支，
// 生产 pg_get_viewdef 实证）——S4 停写后该腿冻结，分钟统计缺覆盖的窗口里
// fallback 会把「没有数据」和「停在停写前」混在同一张饼上。
//
// 列兼容性：fallback 全部查询（summary/trends/pies/error-drill/overview
// counts）引用的列——ts/tenant_id/provider_id/request_status/success/
// error_kind/client_model/outbound_model/prompt_tokens/completion_tokens/
// cache_*_tokens/cost_usd/latency_ms/agent_name/client_profile/client_ip/
// identity_hash + 探针谓词的 quality_flags/task_type/origin_actor + credits
// 表达式的 credits_charged/credits_rate_multiplier——全部在
// canonicalColumnOrderV2（115 列契约）里，投影按名字一一供给。
//
// 性能：hot∪parent 两腿 UNION ALL，外层 ts 谓词可下推进两臂（t.ts 为简单
// 列引用）；fallback 只在分钟统计缺覆盖时触发（常态不跑），正确性优先。
// ≤8d 窗口如需进一步提速可仿 f1b027645 换 hot-only 腿（SessionFamilyTurns
// HotSourceSQL），此处保持全族以防 >7d 回退窗漏 parent 行。
func boardRequestLogsFromClause() (from string, alias string) {
	return dbpkg.SessionFamilyTurnsSourceSQL() + " AS r", "r"
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
